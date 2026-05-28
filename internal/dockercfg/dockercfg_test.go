package dockercfg

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAndEncode_RoundTrip(t *testing.T) {
	input := `{
		"auths": {
			"docker.example.com": {
				"username": "alice",
				"password": "tok-1",
				"auth": "YWxpY2U6dG9rLTE="
			},
			"other.example.com": {
				"auth": "Ym9iOnRvay0y"
			}
		},
		"HttpHeaders": {"User-Agent": "docker"}
	}`
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Auths["docker.example.com"].Password; got != "tok-1" {
		t.Errorf("docker.example.com password = %q, want tok-1", got)
	}
	if got := cfg.Auths["other.example.com"].Username; got != "bob" {
		t.Errorf("other.example.com auth-derived username = %q, want bob", got)
	}
	if got := cfg.Auths["other.example.com"].Password; got != "tok-2" {
		t.Errorf("other.example.com auth-derived password = %q, want tok-2", got)
	}

	if _, ok := cfg.SetPassword("docker.example.com", "tok-NEW"); !ok {
		t.Fatal("SetPassword reported missing host")
	}

	encoded, err := cfg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("decode encoded: %v", err)
	}
	if _, ok := back["HttpHeaders"]; !ok {
		t.Error("HttpHeaders extra field lost on encode")
	}

	cfg2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := cfg2.Auths["docker.example.com"].Password; got != "tok-NEW" {
		t.Errorf("after round-trip password = %q, want tok-NEW", got)
	}
	wantAuth := base64.StdEncoding.EncodeToString([]byte("alice:tok-NEW"))
	if got := cfg2.Auths["docker.example.com"].Auth; got != wantAuth {
		t.Errorf("regenerated auth = %q, want %q", got, wantAuth)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"docker.example.com":          "docker.example.com",
		"https://docker.example.com":  "docker.example.com",
		"https://docker.example.com/": "docker.example.com",
		"https://docker.example.com/v2/": "docker.example.com",
		"docker.example.com:443":      "docker.example.com:443",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetPassword_MissingHost(t *testing.T) {
	cfg, _ := Parse([]byte(`{"auths":{"a":{"username":"u","password":"p"}}}`))
	if _, ok := cfg.SetPassword("b", "x"); ok {
		t.Error("expected ok=false for missing host")
	}
}

func TestParse_RejectsGarbage(t *testing.T) {
	_, err := Parse([]byte("not json"))
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected parse error, got %v", err)
	}
}
