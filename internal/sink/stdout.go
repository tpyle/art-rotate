package sink

import (
	"context"
	"encoding/json"
	"os"
)

type Stdout struct{}

func (Stdout) Name() string { return "stdout" }

func (Stdout) Write(_ context.Context, p Payload) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}
