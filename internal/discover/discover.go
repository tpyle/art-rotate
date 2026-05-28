// Package discover probes a registry hostname (and its parent domains) to
// locate the Artifactory instance that fronts it.
package discover

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"time"
)

// Result describes a successful discovery.
type Result struct {
	// Host is the Artifactory hostname that responded OK to /ping.
	Host string
	// BaseURL is "https://" + Host with no trailing slash.
	BaseURL string
	// Probed lists the hosts we tried, in order, for logging/debugging.
	Probed []string
}

type Options struct {
	HTTPClient *http.Client
	// MaxStrips is how many leading subdomain labels we'll strip while
	// looking for Artifactory. Zero means probe only the given host.
	MaxStrips int
	// Scheme defaults to "https".
	Scheme string
}

// Probe walks candidate hostnames (the registry host and its parent domains)
// looking for Artifactory. Returns nil, nil if nothing matched.
func Probe(ctx context.Context, host string, opts Options) (*Result, error) {
	scheme := opts.Scheme
	if scheme == "" {
		scheme = "https"
	}
	cli := opts.HTTPClient
	if cli == nil {
		cli = &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		}
	}
	candidates := Candidates(host, opts.MaxStrips)
	probed := make([]string, 0, len(candidates))
	for _, h := range candidates {
		probed = append(probed, h)
		base := scheme + "://" + h
		if ok, err := pingArtifactory(ctx, cli, base); err == nil && ok {
			return &Result{Host: h, BaseURL: base, Probed: probed}, nil
		}
	}
	return &Result{Probed: probed}, nil
}

// Candidates returns the original host plus up to maxStrips parent domains.
// It refuses to strip below 2 dot-separated labels (so we don't end up
// probing ".com").
func Candidates(host string, maxStrips int) []string {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	out := []string{host}
	cur := host
	for range maxStrips {
		dot := strings.IndexByte(cur, '.')
		if dot < 0 {
			break
		}
		next := cur[dot+1:]
		if strings.Count(next, ".") < 1 {
			// "example.com" — refuse to strip to bare TLD.
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

// pingArtifactory returns true when base+/artifactory/api/system/ping responds
// with body "OK". This endpoint is unauthenticated on a default Artifactory
// install.
func pingArtifactory(ctx context.Context, c *http.Client, base string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/artifactory/api/system/ping", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(body)) == "OK", nil
}
