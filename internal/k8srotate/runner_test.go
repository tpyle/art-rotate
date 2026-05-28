package k8srotate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"art-rotate/internal/artifactory"
	"art-rotate/internal/discover"
	"art-rotate/internal/dockercfg"
)

func mustB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func dockerSecret(ns, name, host, user, pass string) *corev1.Secret {
	cfg := map[string]any{
		"auths": map[string]any{
			host: map[string]any{
				"username": user,
				"password": pass,
				"auth":     mustB64(user + ":" + pass),
			},
		},
	}
	raw, _ := json.Marshal(cfg)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: raw},
	}
}

// roundTripperFunc lets tests route requests to a single httptest server
// regardless of the URL host requested by the runner. We pretend the
// registry host doesn't exist (forces parent-domain probing) and serve all
// requests for the parent host from the fake server.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRun_EndToEnd_RotatesIdentityTokenViaParentDomain(t *testing.T) {
	// Fake Artifactory: ping OK, /tokens/me, create.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/artifactory/api/system/ping":
			_, _ = io.WriteString(w, "OK")
		case r.URL.Path == "/access/api/v1/tokens/me":
			_, _ = io.WriteString(w, `{"token_id":"old","subject":"jfac@x/users/alice","scope":"applied-permissions/user","audience":"*@*","refreshable":false,"description":"docker push"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/access/api/v1/tokens":
			var got artifactory.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&got)
			if !got.IncludeReferenceToken {
				t.Errorf("expected include_reference_token=true, got %+v", got)
			}
			_, _ = io.WriteString(w, `{"access_token":"jwt-NEW","reference_token":"REFTKN-NEW","token_id":"new","expires_in":0,"scope":"applied-permissions/user","token_type":"Bearer"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)

	// HTTP client used by both discover.Probe and the artifactory client:
	// route only requests to "artifactory.example.com" to the test server.
	// docker.artifactory.example.com fails (forcing a parent-domain strip).
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "artifactory.example.com" {
			req.URL.Scheme = srvURL.Scheme
			req.URL.Host = srvURL.Host
			return http.DefaultTransport.RoundTrip(req)
		}
		return nil, &noHostErr{host: req.URL.Host}
	})
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	// Seed two secrets: one targeting our fake Artifactory, one targeting a
	// non-Artifactory registry (should be left alone).
	matching := dockerSecret("team-a", "art-pull", "docker.artifactory.example.com", "alice", "OLD-PWD")
	other := dockerSecret("team-a", "docker-hub", "index.docker.io", "alice", "DH-PWD")
	kube := fake.NewSimpleClientset(matching, other)

	rep, err := Run(context.Background(), Options{
		Kube:            kube,
		Namespaces:      []string{"team-a"},
		MaxDomainStrips: 3,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DiscoverOptions: discover.Options{HTTPClient: httpClient},
		NewArtifactoryClient: func(base string) (*artifactory.Client, error) {
			c, err := artifactory.New(artifactory.Options{BaseURL: base})
			if err != nil {
				return nil, err
			}
			c.HTTP = httpClient // route artifactory.example.com through fake server
			return c, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rotated != 1 {
		t.Errorf("rotated = %d, want 1; report=%+v errs=%v", rep.Rotated, rep, rep.PerSecretErrs)
	}
	if rep.Failed != 0 {
		t.Errorf("failed = %d, errs=%v", rep.Failed, rep.PerSecretErrs)
	}

	// Verify the matching secret's password was updated to the new reference
	// token.
	got, err := kube.CoreV1().Secrets("team-a").Get(context.Background(), "art-pull", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated secret: %v", err)
	}
	cfg, err := dockercfg.Parse(got.Data[corev1.DockerConfigJsonKey])
	if err != nil {
		t.Fatalf("parse updated dockerconfigjson: %v", err)
	}
	if pw := cfg.Auths["docker.artifactory.example.com"].Password; pw != "REFTKN-NEW" {
		t.Errorf("password not rotated: got %q want REFTKN-NEW", pw)
	}
	wantAuth := mustB64("alice:REFTKN-NEW")
	if a := cfg.Auths["docker.artifactory.example.com"].Auth; a != wantAuth {
		t.Errorf("auth field not regenerated: got %q want %q", a, wantAuth)
	}

	// Non-Artifactory secret should be unchanged.
	got2, _ := kube.CoreV1().Secrets("team-a").Get(context.Background(), "docker-hub", metav1.GetOptions{})
	cfg2, _ := dockercfg.Parse(got2.Data[corev1.DockerConfigJsonKey])
	if pw := cfg2.Auths["index.docker.io"].Password; pw != "DH-PWD" {
		t.Errorf("non-artifactory secret was modified: pw=%q", pw)
	}
}

func TestRun_DryRunMakesNoChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifactory/api/system/ping" {
			_, _ = io.WriteString(w, "OK")
			return
		}
		if r.URL.Path == "/access/api/v1/tokens" && r.Method == http.MethodPost {
			t.Errorf("dry-run should not POST /tokens")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "artifactory.example.com" {
			req.URL.Scheme = srvURL.Scheme
			req.URL.Host = srvURL.Host
			return http.DefaultTransport.RoundTrip(req)
		}
		return nil, &noHostErr{host: req.URL.Host}
	})
	hc := &http.Client{Transport: transport}

	sec := dockerSecret("ns1", "s1", "docker.artifactory.example.com", "u", "p")
	kube := fake.NewSimpleClientset(sec)
	_, err := Run(context.Background(), Options{
		Kube:            kube,
		Namespaces:      []string{"ns1"},
		DryRun:          true,
		MaxDomainStrips: 3,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DiscoverOptions: discover.Options{HTTPClient: hc},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := kube.CoreV1().Secrets("ns1").Get(context.Background(), "s1", metav1.GetOptions{})
	cfg, _ := dockercfg.Parse(got.Data[corev1.DockerConfigJsonKey])
	if cfg.Auths["docker.artifactory.example.com"].Password != "p" {
		t.Errorf("dry-run mutated the secret")
	}
}

type noHostErr struct{ host string }

func (e *noHostErr) Error() string { return "no such host: " + e.host }
