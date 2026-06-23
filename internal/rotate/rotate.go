package rotate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"art-rotate/internal/artifactory"
)

type Method string

const (
	MethodAuto    Method = "auto"
	MethodCreate  Method = "create"
	MethodRefresh Method = "refresh"
)

// IncludeReference is a tri-state for --include-reference-token:
//
//	IncludeRefAuto — set true when the old token looks like an identity token
//	                 (scope contains applied-permissions/user)
//	IncludeRefYes  — always request a reference token
//	IncludeRefNo   — never request one
type IncludeReference int

const (
	IncludeRefAuto IncludeReference = iota
	IncludeRefYes
	IncludeRefNo
)

type Options struct {
	Token        string
	RefreshToken string
	AdminToken   string
	Method       Method
	RevokeOld    bool

	// Optional overrides; zero values mean "mirror the old token".
	ExpiresIn        *int64
	Description      string
	IncludeReference IncludeReference

	// Rotation gates (OR-combined). Zero values disable each gate.
	//   MinAge        — rotate only when token age (now − issued_at) >= MinAge.
	//   ExpiresWithin — rotate only when remaining lifetime (expiry − now) <= ExpiresWithin.
	// If both are zero, rotation always proceeds. If both are non-zero,
	// rotation proceeds when EITHER gate is satisfied. A non-expiring token
	// (Expiry == 0) never satisfies ExpiresWithin.
	MinAge        time.Duration
	ExpiresWithin time.Duration

	// Now lets tests inject a clock; defaults to time.Now.
	Now func() time.Time
}

type Result struct {
	OldTokenID   string
	NewTokenID   string
	Method       Method
	Revoked      bool
	RevokeErr    error
	Response     *artifactory.CreateResponse
	OldTokenInfo *artifactory.TokenInfo

	// Skipped is true when the rotation gates (MinAge / ExpiresWithin) were
	// set and not satisfied; in that case no new token was issued. The other
	// rotation fields (NewTokenID, Method, Response) are zero/nil.
	Skipped    bool
	SkipReason string
}

// Rotate runs the full introspect → (refresh|create) → optional revoke flow.
func Rotate(ctx context.Context, c *artifactory.Client, opts Options) (*Result, error) {
	if opts.Token == "" {
		return nil, fmt.Errorf("token to rotate is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	info, err := c.Introspect(ctx, opts.Token)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}

	if skip, reason := evaluateGates(info, opts, now()); skip {
		return &Result{
			OldTokenID:   info.TokenID,
			OldTokenInfo: info,
			Skipped:      true,
			SkipReason:   reason,
		}, nil
	}

	method := chooseMethod(opts.Method, info.Refreshable, opts.RefreshToken != "")
	authBearer := opts.AdminToken
	if authBearer == "" {
		authBearer = opts.Token
	}

	var resp *artifactory.CreateResponse
	switch method {
	case MethodRefresh:
		resp, err = c.Refresh(ctx, opts.Token, opts.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("refresh: %w", err)
		}
	case MethodCreate:
		req := BuildCreateRequest(info, opts, now())
		resp, err = c.Create(ctx, authBearer, req)
		if err != nil {
			return nil, fmt.Errorf("create: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}

	res := &Result{
		OldTokenID:   info.TokenID,
		NewTokenID:   resp.TokenID,
		Method:       method,
		Response:     resp,
		OldTokenInfo: info,
	}

	if opts.RevokeOld {
		// Prefer admin token; fall back to self-revoke with the still-valid
		// old token. The new token may not yet have permission to revoke.
		revokeBearer := opts.AdminToken
		if revokeBearer == "" {
			revokeBearer = opts.Token
		}
		if err := c.Revoke(ctx, revokeBearer, info.TokenID); err != nil {
			res.RevokeErr = err
		} else {
			res.Revoked = true
		}
	}
	return res, nil
}

// evaluateGates checks the MinAge and ExpiresWithin gates against the
// introspected token. It returns (true, reason) when at least one gate is
// configured and none of the configured gates are satisfied — i.e. the token
// is still healthy enough to keep. When no gates are set, it always returns
// (false, "") so behavior is unchanged.
//
// Semantics:
//   - MinAge satisfied        when (now - issued_at) >= MinAge.
//   - ExpiresWithin satisfied when expiry > 0 AND (expiry - now) <= ExpiresWithin.
//   - Gates combine with OR  — proceeding requires any configured gate to be satisfied.
func evaluateGates(info *artifactory.TokenInfo, opts Options, now time.Time) (skip bool, reason string) {
	if opts.MinAge == 0 && opts.ExpiresWithin == 0 {
		return false, ""
	}

	// info.IssuedAt and info.Expiry are unix epoch SECONDS (not ms) — the
	// JFrog Access API returns them that way.
	var age, remaining time.Duration
	if info.IssuedAt > 0 {
		age = now.Sub(time.Unix(info.IssuedAt, 0))
	}
	if info.Expiry > 0 {
		remaining = time.Unix(info.Expiry, 0).Sub(now)
	}

	if opts.MinAge > 0 && age >= opts.MinAge {
		return false, ""
	}
	if opts.ExpiresWithin > 0 && info.Expiry > 0 && remaining <= opts.ExpiresWithin {
		return false, ""
	}

	// Build a reason describing what failed. Only mention the gates that
	// were actually set.
	parts := make([]string, 0, 2)
	if opts.MinAge > 0 {
		parts = append(parts, fmt.Sprintf("age %s < min-age %s", age.Round(time.Second), opts.MinAge))
	}
	if opts.ExpiresWithin > 0 {
		if info.Expiry == 0 {
			parts = append(parts, "token is non-expiring (expires-within can never apply)")
		} else {
			parts = append(parts, fmt.Sprintf("remaining %s > expires-within %s", remaining.Round(time.Second), opts.ExpiresWithin))
		}
	}
	return true, strings.Join(parts, "; ")
}

func chooseMethod(requested Method, refreshable, haveRefreshToken bool) Method {
	switch requested {
	case MethodCreate:
		return MethodCreate
	case MethodRefresh:
		return MethodRefresh
	default: // auto or unset
		if refreshable && haveRefreshToken {
			return MethodRefresh
		}
		return MethodCreate
	}
}

// IsIdentityToken returns true when the introspected token looks like a
// user-generated identity token (scope is the user-applied-permissions
// marker). These tokens are emitted as opaque reference tokens.
func IsIdentityToken(info *artifactory.TokenInfo) bool {
	return strings.Contains(info.Scope, "applied-permissions/user")
}

// BuildCreateRequest derives a CreateRequest that mirrors the old token's
// permissions. Subject is typically "jfrt@<server-id>/users/<username>" or
// "jfac@<service-id>/users/<username>"; we extract everything after the last
// "/users/" as the username.
func BuildCreateRequest(info *artifactory.TokenInfo, opts Options, now time.Time) artifactory.CreateRequest {
	req := artifactory.CreateRequest{
		Username:              extractUsername(info.Subject),
		Scope:                 info.Scope,
		Audience:              info.Audience,
		Refreshable:           info.Refreshable,
		ProjectKey:            info.ProjectKey,
		Description:           deriveDescription(info.Description, opts.Description, now),
		IncludeReferenceToken: shouldIncludeReference(info, opts.IncludeReference),
	}
	switch {
	case opts.ExpiresIn != nil:
		v := *opts.ExpiresIn
		req.ExpiresIn = &v
	case info.Expiry > 0 && info.IssuedAt > 0 && info.Expiry > info.IssuedAt:
		// Issue the new token with the OLD token's ORIGINAL lifetime
		// (expiry − issued_at), not its remaining time. Otherwise rotating
		// a near-expiry token — which --expires-within explicitly
		// encourages — would mint a near-expired replacement. Both fields
		// are unix epoch seconds.
		lifetime := info.Expiry - info.IssuedAt
		req.ExpiresIn = &lifetime
	default:
		// Old token is non-expiring (or lifetime indeterminate); omit so
		// the new token inherits the server's default for this subject.
	}
	return req
}

func extractUsername(subject string) string {
	if subject == "" {
		return ""
	}
	if i := strings.LastIndex(subject, "/users/"); i >= 0 {
		return subject[i+len("/users/"):]
	}
	if i := strings.LastIndex(subject, "/"); i >= 0 {
		return subject[i+1:]
	}
	return subject
}

func shouldIncludeReference(info *artifactory.TokenInfo, pref IncludeReference) bool {
	switch pref {
	case IncludeRefYes:
		return true
	case IncludeRefNo:
		return false
	default:
		return IsIdentityToken(info)
	}
}

func deriveDescription(old, override string, now time.Time) string {
	if override != "" {
		return override
	}
	suffix := fmt.Sprintf("(rotated %s)", now.UTC().Format("2006-01-02"))
	if old == "" {
		return suffix
	}
	return old + " " + suffix
}
