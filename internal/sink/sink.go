package sink

import "context"

// Payload is what every sink receives.
//
// On a successful rotation, AccessToken and TokenID are populated, plus the
// other token metadata. On a skipped rotation (gates not satisfied), Skipped
// is true and only OldTokenID and SkipReason are meaningful; all other fields
// are zero. Consumers can branch on the top-level Skipped boolean.
type Payload struct {
	AccessToken    string `json:"access_token,omitempty"`
	ReferenceToken string `json:"reference_token,omitempty"`
	TokenID        string `json:"token_id,omitempty"`
	ExpiresIn      int64  `json:"expires_in,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Audience       string `json:"audience,omitempty"`
	Refreshable    bool   `json:"refreshable,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	IsIdentity     bool   `json:"is_identity_token,omitempty"`

	// Skip marker — emitted in place of a new token when a rotation gate
	// (--min-age / --expires-within) was set but not satisfied.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	OldTokenID string `json:"old_token_id,omitempty"`
}

type Sink interface {
	Write(ctx context.Context, p Payload) error
	Name() string
}

// Multi fans out a payload to every sink and aggregates errors so a failure in
// one sink does not prevent emission to others.
type Multi struct {
	Sinks []Sink
}

func (m Multi) Write(ctx context.Context, p Payload) []SinkError {
	var errs []SinkError
	for _, s := range m.Sinks {
		if err := s.Write(ctx, p); err != nil {
			errs = append(errs, SinkError{Sink: s.Name(), Err: err})
		}
	}
	return errs
}

type SinkError struct {
	Sink string
	Err  error
}
