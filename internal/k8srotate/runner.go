// Package k8srotate finds Kubernetes dockerconfigjson secrets, locates the
// Artifactory instance behind each registry host, rotates the token, and
// writes the new credentials back to the Secret.
package k8srotate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"art-rotate/internal/artifactory"
	"art-rotate/internal/discover"
	"art-rotate/internal/dockercfg"
	"art-rotate/internal/rotate"
)

type Options struct {
	// Kube provides access to Secrets. Required.
	Kube kubernetes.Interface
	// Namespaces is the explicit list to scan. If empty, AllNamespaces must
	// be true.
	Namespaces    []string
	AllNamespaces bool
	// LabelSelector restricts which Secrets are considered (k8s syntax).
	LabelSelector string
	// Names, if non-empty, restricts to Secrets whose name is in the set.
	Names map[string]bool
	// AdminToken is an optional bearer used for create/revoke when the
	// password in the secret can't authorize those calls itself.
	AdminToken string
	// MaxDomainStrips is the parent-domain probe depth.
	MaxDomainStrips int
	// MinAge: skip secrets whose token was issued less than this ago.
	MinAge time.Duration
	// RevokeOld asks Artifactory to delete the old token after success.
	RevokeOld bool
	// DryRun logs what would happen without mutating cluster state or
	// Artifactory state.
	DryRun bool
	// Logger receives one structured event per secret/host.
	Logger *slog.Logger
	// Now lets tests inject a clock.
	Now func() time.Time
	// NewArtifactoryClient lets tests replace the real HTTP client.
	NewArtifactoryClient func(baseURL string) (*artifactory.Client, error)
	// DiscoverOptions exposes the discover.Options the runner will use.
	DiscoverOptions discover.Options
}

type Report struct {
	Scanned       int
	Eligible      int
	Rotated       int
	Skipped       int
	Failed        int
	PerSecretErrs []error
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Kube == nil {
		return nil, errors.New("Kube client is required")
	}
	if !opts.AllNamespaces && len(opts.Namespaces) == 0 {
		return nil, errors.New("either Namespaces or AllNamespaces must be set")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewArtifactoryClient == nil {
		opts.NewArtifactoryClient = func(base string) (*artifactory.Client, error) {
			return artifactory.New(artifactory.Options{BaseURL: base, Timeout: 30 * time.Second})
		}
	}
	if opts.MaxDomainStrips == 0 {
		opts.MaxDomainStrips = 3
	}

	rep := &Report{}
	namespaces := opts.Namespaces
	if opts.AllNamespaces {
		namespaces = []string{metav1.NamespaceAll}
	}

	for _, ns := range namespaces {
		secrets, err := opts.Kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: opts.LabelSelector,
		})
		if err != nil {
			return rep, fmt.Errorf("list secrets in ns=%q: %w", ns, err)
		}
		for i := range secrets.Items {
			s := &secrets.Items[i]
			if s.Type != corev1.SecretTypeDockerConfigJson {
				continue
			}
			if len(opts.Names) > 0 && !opts.Names[s.Name] {
				continue
			}
			rep.Scanned++
			if err := processSecret(ctx, opts, s, rep); err != nil {
				rep.Failed++
				rep.PerSecretErrs = append(rep.PerSecretErrs, fmt.Errorf("%s/%s: %w", s.Namespace, s.Name, err))
				opts.Logger.Error("rotate failed", "namespace", s.Namespace, "name", s.Name, "err", err)
			}
		}
	}
	return rep, nil
}

func processSecret(ctx context.Context, opts Options, s *corev1.Secret, rep *Report) error {
	raw, ok := s.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return errors.New("missing .dockerconfigjson key")
	}
	cfg, err := dockercfg.Parse(raw)
	if err != nil {
		return err
	}
	changed := false
	for host, entry := range cfg.Auths {
		log := opts.Logger.With("namespace", s.Namespace, "name", s.Name, "host", host)
		if entry.Password == "" {
			log.Info("skip: no password")
			rep.Skipped++
			continue
		}
		rep.Eligible++
		normalized := dockercfg.NormalizeHost(host)
		disc, err := discover.Probe(ctx, normalized, discover.Options{
			MaxStrips:  opts.MaxDomainStrips,
			HTTPClient: opts.DiscoverOptions.HTTPClient,
			Scheme:     opts.DiscoverOptions.Scheme,
		})
		if err != nil {
			log.Warn("artifactory probe error", "err", err)
			rep.Skipped++
			continue
		}
		if disc.Host == "" {
			log.Info("skip: not an artifactory instance", "probed", disc.Probed)
			rep.Skipped++
			continue
		}
		client, err := opts.NewArtifactoryClient(disc.BaseURL)
		if err != nil {
			return fmt.Errorf("artifactory client for %s: %w", disc.BaseURL, err)
		}
		if opts.MinAge > 0 {
			info, err := client.Introspect(ctx, entry.Password)
			if err != nil {
				log.Warn("introspect for min-age check failed", "err", err)
				rep.Skipped++
				continue
			}
			age := opts.Now().Sub(time.Unix(info.IssuedAt/1000, 0))
			if age < opts.MinAge {
				log.Info("skip: token younger than min-age", "age", age, "min_age", opts.MinAge)
				rep.Skipped++
				continue
			}
		}
		if opts.DryRun {
			log.Info("dry-run: would rotate", "artifactory", disc.BaseURL)
			continue
		}
		res, err := rotate.Rotate(ctx, client, rotate.Options{
			Token:      entry.Password,
			AdminToken: opts.AdminToken,
			Method:     rotate.MethodAuto,
			RevokeOld:  opts.RevokeOld,
		})
		if err != nil {
			return fmt.Errorf("rotate via %s: %w", disc.BaseURL, err)
		}
		newPassword := res.Response.AccessToken
		if res.Response.ReferenceToken != "" {
			// Identity tokens authenticate to the docker registry via the
			// opaque reference token form.
			newPassword = res.Response.ReferenceToken
		}
		cfg.SetPassword(host, newPassword)
		changed = true
		log.Info("rotated",
			"artifactory", disc.BaseURL,
			"old_token_id", res.OldTokenID,
			"new_token_id", res.NewTokenID,
			"method", res.Method,
			"revoked_old", res.Revoked,
		)
		if res.RevokeErr != nil {
			log.Warn("revoke old token failed", "err", res.RevokeErr)
		}
		rep.Rotated++
	}
	if !changed {
		return nil
	}
	encoded, err := cfg.Encode()
	if err != nil {
		return fmt.Errorf("encode dockerconfigjson: %w", err)
	}
	s.Data[corev1.DockerConfigJsonKey] = encoded
	if _, err := opts.Kube.CoreV1().Secrets(s.Namespace).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("conflict updating secret (try again): %w", err)
		}
		return fmt.Errorf("update secret: %w", err)
	}
	return nil
}
