package discover

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCandidates(t *testing.T) {
	cases := []struct {
		host string
		max  int
		want []string
	}{
		{"docker.artifactory.example.com", 3, []string{"docker.artifactory.example.com", "artifactory.example.com", "example.com"}},
		{"acme.jfrog.io", 3, []string{"acme.jfrog.io", "jfrog.io"}},
		{"jfrog.io", 3, []string{"jfrog.io"}},
		{"docker.acme.jfrog.io:443", 2, []string{"docker.acme.jfrog.io", "acme.jfrog.io", "jfrog.io"}},
		{"single", 5, []string{"single"}},
		{"", 5, nil},
	}
	for _, tc := range cases {
		got := Candidates(tc.host, tc.max)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Candidates(%q, %d) = %v, want %v", tc.host, tc.max, got, tc.want)
		}
	}
}

// clientThatRoutesTo returns an *http.Client whose Transport ignores the
// requested URL host and instead dials the given httptest.Server. We also
// disable TLS verification because httptest's TLS cert is self-signed.
func clientThatRoutesTo(t *testing.T, serverURL string, knownHost string) *http.Client {
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == knownHost {
			// Rewrite to actually hit the test server.
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			return tr.RoundTrip(req)
		}
		// Pretend the candidate doesn't exist.
		return nil, &net404{host: req.URL.Host}
	})}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

type net404 struct{ host string }

func (e *net404) Error() string { return "no such host: " + e.host }

func TestProbe_FindsArtifactoryAtParentDomain(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifactory/api/system/ping" {
			_, _ = io.WriteString(w, "OK")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Only the parent domain "artifactory.example.com" answers; the docker.*
	// subdomain does not.
	cli := clientThatRoutesTo(t, srv.URL, "artifactory.example.com")
	res, err := Probe(context.Background(), "docker.artifactory.example.com", Options{
		HTTPClient: cli,
		MaxStrips:  3,
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Host != "artifactory.example.com" {
		t.Errorf("Host = %q, want artifactory.example.com", res.Host)
	}
	if res.BaseURL != "https://artifactory.example.com" {
		t.Errorf("BaseURL = %q", res.BaseURL)
	}
	if len(res.Probed) != 2 {
		t.Errorf("expected 2 probes, got %v", res.Probed)
	}
}

func TestProbe_NoArtifactoryFound(t *testing.T) {
	cli := &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		return nil, &net404{host: req.URL.Host}
	})}
	res, err := Probe(context.Background(), "foo.bar.example.com", Options{HTTPClient: cli, MaxStrips: 3})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Host != "" {
		t.Errorf("expected no host, got %q", res.Host)
	}
	if len(res.Probed) == 0 || !strings.Contains(strings.Join(res.Probed, ","), "bar.example.com") {
		t.Errorf("expected parent domain in probes, got %v", res.Probed)
	}
}
