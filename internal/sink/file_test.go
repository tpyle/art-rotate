package sink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSink_WritesAtomicallyWith0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	f := File{Path: path}

	want := Payload{AccessToken: "secret", TokenID: "tok-1", Refreshable: true}
	if err := f.Write(context.Background(), want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("perms = %o, want 0600", mode)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Payload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.TokenID != want.TokenID {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Overwriting must still succeed (atomic rename replaces the file).
	want2 := Payload{AccessToken: "secret2", TokenID: "tok-2"}
	if err := f.Write(context.Background(), want2); err != nil {
		t.Fatalf("Write second time: %v", err)
	}
	b, _ = os.ReadFile(path)
	_ = json.Unmarshal(b, &got)
	if got.AccessToken != "secret2" {
		t.Errorf("expected overwrite, got %q", got.AccessToken)
	}

	// No leftover temp files from the .art-rotate-* pattern.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "token.json" {
			t.Errorf("leftover file: %s", e.Name())
		}
	}
}
