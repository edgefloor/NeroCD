package domain

import "time"

// OIDCExternalIdentity binds one provider-owned stable subject to a local user.
type OIDCExternalIdentity struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

// OIDCLoginFlow is the durable, single-use browser authorization transaction.
// Every security-sensitive browser value is represented only by its hash.
type OIDCLoginFlow struct {
	ID           string
	StateHash    string
	NonceHash    string
	VerifierHash string
	RedirectPath string
	Issuer       string
	ClientID     string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}
