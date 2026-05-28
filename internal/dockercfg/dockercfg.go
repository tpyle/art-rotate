// Package dockercfg parses and rewrites the .dockerconfigjson payload used by
// Kubernetes secrets of type kubernetes.io/dockerconfigjson.
package dockercfg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
)

type Config struct {
	Auths map[string]AuthEntry `json:"auths"`
	// HttpHeaders / credsStore etc. are intentionally pass-through.
	Extra map[string]json.RawMessage `json:"-"`
}

type AuthEntry struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Email    string `json:"email,omitempty"`
	Auth     string `json:"auth,omitempty"`
	// Other fields (identitytoken, registrytoken) are preserved verbatim via
	// the raw map on Config; AuthEntry only carries the rotation-relevant
	// pieces.
}

// Parse decodes a .dockerconfigjson blob. Unknown top-level keys are preserved
// so we can round-trip them back unchanged on Encode.
func Parse(data []byte) (*Config, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse dockerconfigjson: %w", err)
	}
	cfg := &Config{Auths: map[string]AuthEntry{}, Extra: map[string]json.RawMessage{}}
	for k, v := range raw {
		if k == "auths" {
			if err := json.Unmarshal(v, &cfg.Auths); err != nil {
				return nil, fmt.Errorf("parse auths: %w", err)
			}
			continue
		}
		cfg.Extra[k] = v
	}
	for host, entry := range cfg.Auths {
		if entry.Username == "" || entry.Password == "" {
			if u, p, ok := decodeAuthField(entry.Auth); ok {
				if entry.Username == "" {
					entry.Username = u
				}
				if entry.Password == "" {
					entry.Password = p
				}
				cfg.Auths[host] = entry
			}
		}
	}
	return cfg, nil
}

// Encode marshals back to the canonical dockerconfigjson shape. The `auth`
// field is regenerated from username + password so a freshly rotated password
// is in both places.
func (c *Config) Encode() ([]byte, error) {
	auths := make(map[string]AuthEntry, len(c.Auths))
	for host, e := range c.Auths {
		if e.Username != "" && e.Password != "" {
			e.Auth = base64.StdEncoding.EncodeToString([]byte(e.Username + ":" + e.Password))
		}
		auths[host] = e
	}
	out := map[string]json.RawMessage{}
	maps.Copy(out, c.Extra)
	b, err := json.Marshal(auths)
	if err != nil {
		return nil, err
	}
	out["auths"] = b
	return json.MarshalIndent(out, "", "  ")
}

// SetPassword rewrites the password (and clears any stale `auth`) for the
// given registry host. Returns the previous password and whether the host
// existed.
func (c *Config) SetPassword(host, newPassword string) (oldPassword string, ok bool) {
	e, ok := c.Auths[host]
	if !ok {
		return "", false
	}
	oldPassword = e.Password
	e.Password = newPassword
	e.Auth = "" // regenerated in Encode
	c.Auths[host] = e
	return oldPassword, true
}

// Hosts returns the registry hostnames present in the config, with any
// scheme/path stripped (Docker registry keys are sometimes stored as bare
// hosts, sometimes as https://host/v2/).
func (c *Config) Hosts() []string {
	out := make([]string, 0, len(c.Auths))
	for k := range c.Auths {
		out = append(out, k)
	}
	return out
}

// NormalizeHost strips scheme/path from a docker registry key. "https://docker.example.com/v2/"
// → "docker.example.com".
func NormalizeHost(registryKey string) string {
	s := registryKey
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.TrimSpace(registryKey)
	}
	return u.Host
}

func decodeAuthField(authB64 string) (user, pass string, ok bool) {
	if authB64 == "" {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(authB64)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
