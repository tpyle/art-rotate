// art-rotate is a small CLI that takes an Artifactory access token and
// produces a new token with identical permissions, for credential rotation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"art-rotate/internal/artifactory"
	"art-rotate/internal/rotate"
	"art-rotate/internal/sink"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func run(args []string) int {
	fs := flag.NewFlagSet("art-rotate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `art-rotate — rotate a JFrog Artifactory access or identity token

Given an existing token, issue a new one with the same scope, audience and
refreshability. Both JWT access tokens and reference-style identity tokens are
supported; identity tokens are detected from the source token's
"applied-permissions/user" scope and the new token is requested with
include_reference_token=true so the output contains the opaque form.

Flows:
  refresh  POST /access/api/v1/tokens grant_type=refresh_token
           Requires the original refresh_token (issued at creation time).
  create   POST /access/api/v1/tokens with permissions copied from the old token.
           The caller (input token or --admin-token) must have permission to
           create tokens for the subject.

Usage:
  art-rotate --url URL --token TOKEN [flags]

Flags:
`)
		fs.PrintDefaults()
	}

	var (
		url          = fs.String("url", os.Getenv("ARTIFACTORY_URL"), "Artifactory base URL (env ARTIFACTORY_URL)")
		token        = fs.String("token", os.Getenv("ARTIFACTORY_TOKEN"), "Access token to rotate (env ARTIFACTORY_TOKEN)")
		refreshToken = fs.String("refresh-token", os.Getenv("ARTIFACTORY_REFRESH_TOKEN"), "Refresh token paired with --token; enables refresh flow in auto mode (env ARTIFACTORY_REFRESH_TOKEN)")
		adminToken   = fs.String("admin-token", os.Getenv("ARTIFACTORY_ADMIN_TOKEN"), "Bearer used for create/revoke when input token lacks permission (env ARTIFACTORY_ADMIN_TOKEN)")
		method       = fs.String("method", "auto", "Rotation method: auto | refresh | create")
		refTokenMode = fs.String("include-reference-token", "auto", "Ask Artifactory to also issue a reference (identity) token: auto | yes | no. Auto = on when the source token is an identity token.")
		revokeOld    = fs.Bool("revoke-old", false, "Revoke the old token after the new one is emitted")
		outputFile   = fs.String("output-file", "", "Path for the 'file' sink (written 0600, atomic)")
		expiresInArg = fs.Int64("expires-in", -1, "Override new-token TTL in seconds (default: mirror remaining TTL of old token; 0 = non-expiring)")
		description  = fs.String("description", "", "Override description (default: old description + ' (rotated YYYY-MM-DD)')")
		timeout      = fs.Duration("timeout", 30*time.Second, "HTTP timeout")
		insecure     = fs.Bool("insecure-skip-verify", false, "Skip TLS verification (DANGEROUS; dev only)")
		minAge       = fs.Duration("min-age", 0, "Rotate only if the current token was issued at least this long ago (e.g. 168h). Zero disables. OR-combined with --expires-within.")
		expiresWithin = fs.Duration("expires-within", 0, "Rotate only if the current token expires within this duration (e.g. 24h). Zero disables. Non-expiring tokens never satisfy this gate. OR-combined with --min-age.")
	)
	var outputs stringSlice
	fs.Var(&outputs, "output", "Output sink, repeatable or comma-separated: stdout | file (default: stdout)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	if *url == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --url and --token are required (see --help)")
		return 1
	}
	if len(outputs) == 0 {
		outputs = stringSlice{"stdout"}
	}
	if *insecure {
		fmt.Fprintln(os.Stderr, "WARN: TLS verification disabled (--insecure-skip-verify)")
	}

	sinks, err := buildSinks(outputs, *outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}

	client, err := artifactory.New(artifactory.Options{
		BaseURL:            *url,
		Timeout:            *timeout,
		InsecureSkipVerify: *insecure,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}

	includeRef, err := parseIncludeRef(*refTokenMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	opts := rotate.Options{
		Token:            *token,
		RefreshToken:     *refreshToken,
		AdminToken:       *adminToken,
		Method:           rotate.Method(*method),
		RevokeOld:        *revokeOld,
		Description:      *description,
		IncludeReference: includeRef,
		MinAge:           *minAge,
		ExpiresWithin:    *expiresWithin,
	}
	if *expiresInArg >= 0 {
		v := *expiresInArg
		opts.ExpiresIn = &v
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	res, err := rotate.Rotate(ctx, client, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}

	if res.Skipped {
		fmt.Fprintf(os.Stderr, "INFO: skipped rotation (%s); old token_id=%s\n", res.SkipReason, res.OldTokenID)
		skipPayload := sink.Payload{
			Skipped:    true,
			SkipReason: res.SkipReason,
			OldTokenID: res.OldTokenID,
		}
		multi := sink.Multi{Sinks: sinks}
		exit := 0
		if sinkErrs := multi.Write(ctx, skipPayload); len(sinkErrs) > 0 {
			for _, se := range sinkErrs {
				fmt.Fprintf(os.Stderr, "ERROR: sink %s: %v\n", se.Sink, se.Err)
			}
			exit = 2
		}
		return exit
	}

	isIdentity := rotate.IsIdentityToken(res.OldTokenInfo)
	kind := "access"
	if isIdentity {
		kind = "identity"
	}
	fmt.Fprintf(os.Stderr, "INFO: rotated %s token via %s; old token_id=%s new token_id=%s\n",
		kind, res.Method, res.OldTokenID, res.NewTokenID)
	if isIdentity && res.Response.ReferenceToken == "" {
		fmt.Fprintln(os.Stderr, "WARN: source looks like an identity token but the server did not return a reference_token; the new token is JWT-only. Pass --include-reference-token=yes if you need the opaque form.")
	}

	payload := sink.Payload{
		AccessToken:    res.Response.AccessToken,
		ReferenceToken: res.Response.ReferenceToken,
		TokenID:        res.Response.TokenID,
		ExpiresIn:      res.Response.ExpiresIn,
		Scope:          res.Response.Scope,
		Audience:       res.OldTokenInfo.Audience,
		Refreshable:    res.OldTokenInfo.Refreshable,
		RefreshToken:   res.Response.RefreshToken,
		IsIdentity:     isIdentity,
	}

	exit := 0
	multi := sink.Multi{Sinks: sinks}
	if sinkErrs := multi.Write(ctx, payload); len(sinkErrs) > 0 {
		for _, se := range sinkErrs {
			fmt.Fprintf(os.Stderr, "ERROR: sink %s: %v\n", se.Sink, se.Err)
		}
		exit = 2
	}

	if res.RevokeErr != nil {
		fmt.Fprintf(os.Stderr, "WARN: revoke old token %s failed: %v (new token already emitted)\n", res.OldTokenID, res.RevokeErr)
		exit = 2
	} else if res.Revoked {
		fmt.Fprintf(os.Stderr, "INFO: revoked old token %s\n", res.OldTokenID)
	}

	return exit
}

func parseIncludeRef(s string) (rotate.IncludeReference, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return rotate.IncludeRefAuto, nil
	case "yes", "true", "1":
		return rotate.IncludeRefYes, nil
	case "no", "false", "0":
		return rotate.IncludeRefNo, nil
	default:
		return 0, fmt.Errorf("invalid --include-reference-token: %q (want auto|yes|no)", s)
	}
}

func buildSinks(specs []string, filePath string) ([]sink.Sink, error) {
	var out []sink.Sink
	seen := map[string]bool{}
	for _, s := range specs {
		s = strings.ToLower(strings.TrimSpace(s))
		if seen[s] {
			continue
		}
		seen[s] = true
		switch s {
		case "stdout":
			out = append(out, sink.Stdout{})
		case "file":
			if filePath == "" {
				return nil, fmt.Errorf("--output file requires --output-file")
			}
			out = append(out, sink.File{Path: filePath})
		default:
			return nil, fmt.Errorf("unknown --output sink: %q", s)
		}
	}
	return out, nil
}
