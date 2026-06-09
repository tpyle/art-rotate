// art-k8s-rotate is a Kubernetes-native cronjob that rotates Artifactory
// tokens stored inside dockerconfigjson Secrets. For each matching Secret it
// reads the registry hostnames, probes them (and their parent domains) to
// locate the Artifactory instance, rotates the token, and writes the new
// token back into the Secret.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"art-rotate/internal/discover"
	"art-rotate/internal/k8srotate"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("art-k8s-rotate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `art-k8s-rotate — rotate Artifactory tokens inside Kubernetes dockerconfigjson secrets

For every matching Secret of type kubernetes.io/dockerconfigjson, this tool:
  1. Extracts each registry host from .dockerconfigjson
  2. Probes the host (and up to --max-domain-strips parent domains) for an
     Artifactory instance via /artifactory/api/system/ping
  3. If found, uses the secret's password as a JFrog access/identity token to
     introspect and rotate it (re-using the same logic as 'art-rotate')
  4. Writes the new token back into the same Secret

Designed to run as a Kubernetes CronJob. See examples/cronjob.yaml.

Flags:
`)
		fs.PrintDefaults()
	}

	var (
		namespaces      = fs.String("namespaces", "", "Comma-separated list of namespaces to scan (mutually exclusive with --all-namespaces)")
		allNamespaces   = fs.Bool("all-namespaces", false, "Scan all namespaces visible to the service account")
		labelSelector   = fs.String("label-selector", "", "k8s label selector to restrict which Secrets are considered (e.g., 'art-rotate=true')")
		names           = fs.String("names", "", "Comma-separated list of secret names to allow; empty = no name filter")
		adminToken      = fs.String("admin-token", os.Getenv("ARTIFACTORY_ADMIN_TOKEN"), "Bearer used for create/revoke if the secret's own token lacks permission")
		maxDomainStrips = fs.Int("max-domain-strips", 3, "How many leading subdomain labels to strip when probing for Artifactory")
		minAge          = fs.Duration("min-age", 0, "Rotate only if the token was issued at least this long ago (e.g. 168h). Zero disables. OR-combined with --expires-within.")
		expiresWithin   = fs.Duration("expires-within", 0, "Rotate only if the token expires within this duration (e.g. 24h). Non-expiring tokens never satisfy this gate. Zero disables. OR-combined with --min-age.")
		revokeOld       = fs.Bool("revoke-old", false, "Revoke the old token after the new one is written back")
		dryRun          = fs.Bool("dry-run", false, "Report what would happen without modifying secrets or Artifactory")
		kubeconfig      = fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "Path to a kubeconfig (out-of-cluster); empty = in-cluster")
		logLevel        = fs.String("log-level", "info", "Log level: debug | info | warn | error")
		probeTimeout    = fs.Duration("probe-timeout", 10*time.Second, "HTTP timeout per Artifactory ping")
	)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	logger := newLogger(*logLevel)

	nsList := splitCSV(*namespaces)
	nameSet := map[string]bool{}
	for _, n := range splitCSV(*names) {
		nameSet[n] = true
	}
	if !*allNamespaces && len(nsList) == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: provide --namespaces or --all-namespaces (see --help)")
		return 1
	}

	cfg, err := kubeConfig(*kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: build kube config: %v\n", err)
		return 1
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: build kube client: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("starting",
		"all_namespaces", *allNamespaces,
		"namespaces", nsList,
		"label_selector", *labelSelector,
		"name_filter_count", len(nameSet),
		"dry_run", *dryRun,
	)

	rep, err := k8srotate.Run(ctx, k8srotate.Options{
		Kube:            client,
		Namespaces:      nsList,
		AllNamespaces:   *allNamespaces,
		LabelSelector:   *labelSelector,
		Names:           nameSet,
		AdminToken:      *adminToken,
		MaxDomainStrips: *maxDomainStrips,
		MinAge:          *minAge,
		ExpiresWithin:   *expiresWithin,
		RevokeOld:       *revokeOld,
		DryRun:          *dryRun,
		Logger:          logger,
		DiscoverOptions: discoverOptionsFromFlags(*probeTimeout),
	})
	if err != nil {
		logger.Error("run failed", "err", err)
		return 1
	}
	logger.Info("done",
		"scanned", rep.Scanned,
		"eligible", rep.Eligible,
		"rotated", rep.Rotated,
		"skipped", rep.Skipped,
		"failed", rep.Failed,
	)
	if rep.Failed > 0 {
		return 2
	}
	return 0
}

func discoverOptionsFromFlags(timeout time.Duration) discover.Options {
	return discover.Options{
		HTTPClient: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		},
	}
}

func kubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
