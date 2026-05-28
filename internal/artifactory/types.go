package artifactory

// TokenInfo is the response from GET /access/api/v1/tokens/me.
// Fields mirror the JFrog Access API; only the ones needed for rotation
// are decoded.
type TokenInfo struct {
	TokenID     string `json:"token_id"`
	Subject     string `json:"subject"`
	Scope       string `json:"scope"`
	Audience    string `json:"audience"`
	Refreshable bool   `json:"refreshable"`
	IssuedAt    int64  `json:"issued_at"`
	Expiry      int64  `json:"expiry"`
	Description string `json:"description"`
	ProjectKey  string `json:"project_key"`
}

// CreateRequest is the JSON body for POST /access/api/v1/tokens (create flow).
//
// IncludeReferenceToken asks the server to also issue a reference (opaque)
// token alongside the JWT access token. Identity tokens — the kind a user
// generates from their JFrog profile page — are reference tokens, so this
// must be true when rotating one.
type CreateRequest struct {
	Username              string `json:"username,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	Audience              string `json:"audience,omitempty"`
	Refreshable           bool   `json:"refreshable"`
	ExpiresIn             *int64 `json:"expires_in,omitempty"`
	Description           string `json:"description,omitempty"`
	ProjectKey            string `json:"project_key,omitempty"`
	IncludeReferenceToken bool   `json:"include_reference_token,omitempty"`
}

// CreateResponse is the success payload returned by token creation or refresh.
//
// ReferenceToken is the opaque, "cmVmdGtu…"-style token returned when
// include_reference_token=true. This is the form users see in the UI as their
// "identity token".
type CreateResponse struct {
	AccessToken    string `json:"access_token"`
	TokenID        string `json:"token_id"`
	ExpiresIn      int64  `json:"expires_in"`
	Scope          string `json:"scope"`
	TokenType      string `json:"token_type"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	ReferenceToken string `json:"reference_token,omitempty"`
}
