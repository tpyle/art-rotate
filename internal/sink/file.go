package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type File struct {
	Path string
}

func (f File) Name() string { return "file:" + f.Path }

func (f File) Write(_ context.Context, p Payload) error {
	if f.Path == "" {
		return fmt.Errorf("file sink: empty path")
	}
	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, ".art-rotate-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("encode token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, f.Path); err != nil {
		cleanup()
		return fmt.Errorf("rename to %s: %w", f.Path, err)
	}
	return nil
}
