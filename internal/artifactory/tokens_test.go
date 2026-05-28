package artifactory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	c, err := New(Options{BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		srv.Close()
		t.Fatalf("new client: %v", err)
	}
	return c, srv
}

func TestIntrospect(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/access/api/v1/tokens/me" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer abc" {
			t.Errorf("missing/bad auth header: %q", got)
		}
		_, _ = io.WriteString(w, `{
			"token_id": "tok-1",
			"subject": "jfac@01h/users/svc-ci",
			"scope": "applied-permissions/groups:readers",
			"audience": "*@*",
			"refreshable": true,
			"issued_at": 1700000000,
			"expiry": 1800000000000,
			"description": "ci runner",
			"project_key": "myproj"
		}`)
	})
	defer srv.Close()

	info, err := c.Introspect(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if info.TokenID != "tok-1" || info.Subject != "jfac@01h/users/svc-ci" {
		t.Errorf("unexpected info: %+v", info)
	}
	if !info.Refreshable {
		t.Errorf("expected refreshable=true")
	}
	if info.ProjectKey != "myproj" {
		t.Errorf("expected project_key=myproj, got %q", info.ProjectKey)
	}
}

func TestCreate(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/access/api/v1/tokens" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected json content-type, got %q", ct)
		}
		var got CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got.Username != "svc-ci" || got.Scope != "applied-permissions/groups:readers" {
			t.Errorf("unexpected body: %+v", got)
		}
		_, _ = io.WriteString(w, `{
			"access_token": "new-jwt",
			"token_id": "tok-2",
			"expires_in": 3600,
			"scope": "applied-permissions/groups:readers",
			"token_type": "Bearer"
		}`)
	})
	defer srv.Close()

	resp, err := c.Create(context.Background(), "admin", CreateRequest{
		Username: "svc-ci",
		Scope:    "applied-permissions/groups:readers",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if resp.AccessToken != "new-jwt" || resp.TokenID != "tok-2" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestRefresh(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("expected form content-type, got %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		if !strings.Contains(s, "grant_type=refresh_token") ||
			!strings.Contains(s, "refresh_token=rt-1") ||
			!strings.Contains(s, "access_token=at-1") {
			t.Errorf("unexpected form body: %s", s)
		}
		_, _ = io.WriteString(w, `{"access_token":"new","token_id":"tok-3","expires_in":60,"token_type":"Bearer","refresh_token":"rt-2"}`)
	})
	defer srv.Close()

	resp, err := c.Refresh(context.Background(), "at-1", "rt-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if resp.RefreshToken != "rt-2" {
		t.Errorf("expected refresh_token=rt-2, got %q", resp.RefreshToken)
	}
}

func TestRevoke(t *testing.T) {
	called := false
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/access/api/v1/tokens/tok-1" {
			t.Errorf("expected path /access/api/v1/tokens/tok-1, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	if err := c.Revoke(context.Background(), "abc", "tok-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !called {
		t.Error("revoke handler not called")
	}
}

func TestAPIErrorOnNon2xx(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"errors":[{"message":"nope"}]}`)
	})
	defer srv.Close()

	_, err := c.Introspect(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusForbidden {
		t.Errorf("expected 403, got %d", apiErr.Status)
	}
}
