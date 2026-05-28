package sink

import "context"

// Payload is what every sink receives. AccessToken and TokenID are always
// populated. ReferenceToken is set when the new token is an identity token
// (or include_reference_token was forced on).
type Payload struct {
	AccessToken    string `json:"access_token"`
	ReferenceToken string `json:"reference_token,omitempty"`
	TokenID        string `json:"token_id"`
	ExpiresIn      int64  `json:"expires_in,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Audience       string `json:"audience,omitempty"`
	Refreshable    bool   `json:"refreshable"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	IsIdentity     bool   `json:"is_identity_token,omitempty"`
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
