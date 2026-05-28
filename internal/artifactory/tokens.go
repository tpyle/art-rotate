package artifactory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	pathTokensMe = "/access/api/v1/tokens/me"
	pathTokens   = "/access/api/v1/tokens"
)

// Introspect calls GET /access/api/v1/tokens/me with the supplied bearer token.
func (c *Client) Introspect(ctx context.Context, bearer string) (*TokenInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(pathTokensMe), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var info TokenInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("decode /tokens/me: %w", err)
	}
	return &info, nil
}

// Create issues a new access token. The bearer must have permission to create
// tokens for the requested subject (typically the input token itself or an
// admin token).
func (c *Client) Create(ctx context.Context, bearer string, body CreateRequest) (*CreateResponse, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(pathTokens), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	respBody, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out CreateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode create response: %w", err)
	}
	return &out, nil
}

// Refresh exchanges a refresh token for a new access token. Per JFrog docs the
// endpoint accepts form-encoded grant_type=refresh_token plus the refresh and
// access tokens.
func (c *Client) Refresh(ctx context.Context, accessToken, refreshToken string) (*CreateResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("access_token", accessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(pathTokens), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	respBody, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out CreateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	return &out, nil
}

// Revoke deletes a token by id. Self-revoke is allowed when bearer == the
// token being revoked.
func (c *Client) Revoke(ctx context.Context, bearer, tokenID string) error {
	if tokenID == "" {
		return fmt.Errorf("revoke: empty token id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint(pathTokens+"/"+url.PathEscape(tokenID)), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	_, err = c.do(req)
	return err
}
