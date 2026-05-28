package rotate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"art-rotate/internal/artifactory"
)

func TestChooseMethod(t *testing.T) {
	cases := []struct {
		name             string
		requested        Method
		refreshable      bool
		haveRefreshToken bool
		want             Method
	}{
		{"auto+refreshable+token", MethodAuto, true, true, MethodRefresh},
		{"auto+refreshable+no-token", MethodAuto, true, false, MethodCreate},
		{"auto+not-refreshable+token", MethodAuto, false, true, MethodCreate},
		{"auto+nothing", MethodAuto, false, false, MethodCreate},
		{"explicit refresh", MethodRefresh, false, false, MethodRefresh},
		{"explicit create", MethodCreate, true, true, MethodCreate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseMethod(tc.requested, tc.refreshable, tc.haveRefreshToken)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBuildCreateRequest_MirrorsTokenInfo(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	info := &artifactory.TokenInfo{
		TokenID:     "tok-1",
		Subject:     "jfac@01h/users/svc-ci",
		Scope:       "applied-permissions/groups:readers",
		Audience:    "*@*",
		Refreshable: true,
		Expiry:      now.Add(2*time.Hour).Unix() * 1000,
		Description: "ci runner",
		ProjectKey:  "myproj",
	}
	req := BuildCreateRequest(info, Options{}, now)
	if req.Username != "svc-ci" {
		t.Errorf("username = %q, want svc-ci", req.Username)
	}
	if req.Scope != info.Scope || req.Audience != info.Audience {
		t.Errorf("scope/audience not mirrored: %+v", req)
	}
	if !req.Refreshable {
		t.Errorf("refreshable should mirror old token")
	}
	if req.ProjectKey != "myproj" {
		t.Errorf("project_key not mirrored: %q", req.ProjectKey)
	}
	if req.ExpiresIn == nil || *req.ExpiresIn != 7200 {
		t.Errorf("expires_in = %v, want 7200", req.ExpiresIn)
	}
	if !strings.HasPrefix(req.Description, "ci runner (rotated 2026-05-20") {
		t.Errorf("description = %q", req.Description)
	}
}

func TestBuildCreateRequest_NonExpiringStaysNonExpiring(t *testing.T) {
	info := &artifactory.TokenInfo{Subject: "user1", Expiry: 0}
	req := BuildCreateRequest(info, Options{}, time.Now())
	if req.ExpiresIn != nil {
		t.Errorf("expected nil ExpiresIn for non-expiring source, got %v", *req.ExpiresIn)
	}
}

func TestBuildCreateRequest_OverrideExpiresIn(t *testing.T) {
	info := &artifactory.TokenInfo{Subject: "user1", Expiry: 1700000000000}
	override := int64(60)
	req := BuildCreateRequest(info, Options{ExpiresIn: &override}, time.Now())
	if req.ExpiresIn == nil || *req.ExpiresIn != 60 {
		t.Errorf("expected ExpiresIn=60, got %v", req.ExpiresIn)
	}
}

func TestExtractUsername(t *testing.T) {
	cases := map[string]string{
		"jfac@01h/users/svc-ci": "svc-ci",
		"jfrt@server/users/me":  "me",
		"plain-user":            "plain-user",
		"":                      "",
		"with/slash/only":       "only",
	}
	for in, want := range cases {
		if got := extractUsername(in); got != want {
			t.Errorf("extractUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRotate_CreateFlow exercises the full Rotate flow against a fake
// Artifactory using create method, with revoke-old enabled.
func TestRotate_CreateFlow(t *testing.T) {
	var revoked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/access/api/v1/tokens/me":
			_, _ = io.WriteString(w, `{"token_id":"old","subject":"jfac@x/users/svc","scope":"applied-permissions/admin","audience":"*@*","refreshable":false,"expiry":0,"description":"orig"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/access/api/v1/tokens":
			var got artifactory.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got.Username != "svc" || got.Scope != "applied-permissions/admin" {
				t.Errorf("unexpected create body: %+v", got)
			}
			_, _ = io.WriteString(w, `{"access_token":"NEW","token_id":"new","expires_in":3600,"scope":"applied-permissions/admin","token_type":"Bearer"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/access/api/v1/tokens/old":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := artifactory.New(artifactory.Options{BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	res, err := Rotate(context.Background(), c, Options{
		Token:     "input",
		Method:    MethodAuto,
		RevokeOld: true,
	})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if res.Method != MethodCreate {
		t.Errorf("expected create method, got %s", res.Method)
	}
	if res.NewTokenID != "new" {
		t.Errorf("expected new token_id=new, got %s", res.NewTokenID)
	}
	if !revoked || !res.Revoked {
		t.Errorf("expected old token to be revoked")
	}
}

func TestIsIdentityToken(t *testing.T) {
	cases := map[string]bool{
		"applied-permissions/user":                           true,
		"applied-permissions/user applied-permissions/admin": true,
		"applied-permissions/admin":                          false,
		"applied-permissions/groups:readers":                 false,
		"":                                                   false,
	}
	for scope, want := range cases {
		got := IsIdentityToken(&artifactory.TokenInfo{Scope: scope})
		if got != want {
			t.Errorf("scope=%q: got %v, want %v", scope, got, want)
		}
	}
}

func TestBuildCreateRequest_IdentityTokenAutoIncludesReference(t *testing.T) {
	info := &artifactory.TokenInfo{
		Subject: "jfac@x/users/alice",
		Scope:   "applied-permissions/user",
	}
	req := BuildCreateRequest(info, Options{IncludeReference: IncludeRefAuto}, time.Now())
	if !req.IncludeReferenceToken {
		t.Errorf("expected IncludeReferenceToken=true for identity token, got false")
	}
}

func TestBuildCreateRequest_AccessTokenAutoDoesNotIncludeReference(t *testing.T) {
	info := &artifactory.TokenInfo{
		Subject: "jfac@x/users/svc",
		Scope:   "applied-permissions/groups:readers",
	}
	req := BuildCreateRequest(info, Options{IncludeReference: IncludeRefAuto}, time.Now())
	if req.IncludeReferenceToken {
		t.Errorf("expected IncludeReferenceToken=false for non-identity token, got true")
	}
}

func TestBuildCreateRequest_IncludeReferenceOverrides(t *testing.T) {
	info := &artifactory.TokenInfo{Scope: "applied-permissions/user"}
	if BuildCreateRequest(info, Options{IncludeReference: IncludeRefNo}, time.Now()).IncludeReferenceToken {
		t.Errorf("IncludeRefNo should force false even for identity token")
	}
	info2 := &artifactory.TokenInfo{Scope: "applied-permissions/admin"}
	if !BuildCreateRequest(info2, Options{IncludeReference: IncludeRefYes}, time.Now()).IncludeReferenceToken {
		t.Errorf("IncludeRefYes should force true even for non-identity token")
	}
}

// TestRotate_IdentityToken_EndToEnd verifies the identity-token create flow:
// the request must carry include_reference_token=true and the response's
// reference_token must be propagated all the way to the Result.
func TestRotate_IdentityToken_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/access/api/v1/tokens/me":
			_, _ = io.WriteString(w, `{"token_id":"id-old","subject":"jfac@x/users/alice","scope":"applied-permissions/user","audience":"*@*","refreshable":false,"description":"profile token"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/access/api/v1/tokens":
			var got artifactory.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&got)
			if !got.IncludeReferenceToken {
				t.Errorf("expected include_reference_token=true in create body, got %+v", got)
			}
			if got.Scope != "applied-permissions/user" || got.Username != "alice" {
				t.Errorf("unexpected create body: %+v", got)
			}
			_, _ = io.WriteString(w, `{"access_token":"jwt-NEW","reference_token":"cmVmdGtuOlNFQ1JFVA","token_id":"id-new","expires_in":0,"scope":"applied-permissions/user","token_type":"Bearer"}`)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := artifactory.New(artifactory.Options{BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := Rotate(context.Background(), c, Options{Token: "input"})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if res.Response.ReferenceToken != "cmVmdGtuOlNFQ1JFVA" {
		t.Errorf("expected reference_token to be propagated, got %q", res.Response.ReferenceToken)
	}
	if !IsIdentityToken(res.OldTokenInfo) {
		t.Errorf("expected IsIdentityToken=true on old token info")
	}
}

// TestRotate_RefreshFlowWhenAuto verifies that auto mode picks refresh when a
// refresh token is supplied and the source token is refreshable.
func TestRotate_RefreshFlowWhenAuto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/access/api/v1/tokens/me":
			_, _ = io.WriteString(w, `{"token_id":"old","subject":"u","refreshable":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/access/api/v1/tokens":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "grant_type=refresh_token") {
				t.Errorf("expected refresh form body, got %s", body)
			}
			_, _ = io.WriteString(w, `{"access_token":"NEW","token_id":"new","expires_in":60,"token_type":"Bearer","refresh_token":"rt-2"}`)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c, _ := artifactory.New(artifactory.Options{BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := Rotate(context.Background(), c, Options{
		Token:        "input",
		RefreshToken: "rt-1",
		Method:       MethodAuto,
	})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if res.Method != MethodRefresh {
		t.Errorf("expected refresh method, got %s", res.Method)
	}
	if res.Response.RefreshToken != "rt-2" {
		t.Errorf("expected new refresh token to be returned, got %q", res.Response.RefreshToken)
	}
}
