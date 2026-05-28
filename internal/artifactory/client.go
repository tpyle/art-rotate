package artifactory

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL *url.URL
	HTTP    *http.Client
}

type Options struct {
	BaseURL            string
	Timeout            time.Duration
	InsecureSkipVerify bool
}

func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("artifactory base URL is required")
	}
	u, err := url.Parse(strings.TrimRight(opts.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.InsecureSkipVerify},
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL: u,
		HTTP:    &http.Client{Timeout: timeout, Transport: tr},
	}, nil
}

// APIError represents a non-2xx response from the Access API.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	return fmt.Sprintf("artifactory %s: HTTP %d: %s", e.Path, e.Status, body)
}

func (c *Client) endpoint(path string) string {
	return c.BaseURL.String() + path
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Path: req.URL.Path, Body: string(body)}
	}
	return body, nil
}
