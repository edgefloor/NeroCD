package domain

import (
	"encoding/hex"
	"strings"
	"time"
)

// HostingSourceCredentialRequest identifies delivery under one live build attempt.
// It never chooses a secret: the immutable build snapshot supplies that reference.
type HostingSourceCredentialRequest struct {
	Authority  HostingBuildAuthority `json:"authority"`
	DeliveryID string                `json:"delivery_id"`
}

// HostingSourceCredentialWindow is a bounded authorization window, not revocation
// of bytes that have already been delivered. Replays never extend UseUntil.
type HostingSourceCredentialWindow struct {
	DeliveryID  string    `json:"delivery_id"`
	RequestHash string    `json:"request_hash"`
	UseUntil    time.Time `json:"use_until"`
}

// ValidHostingSourceDeliveryID accepts canonical encodings of 128 or 256 bits.
// Clients must generate these opaque identifiers with cryptographic randomness.
func ValidHostingSourceDeliveryID(id string) bool {
	if (len(id) != 32 && len(id) != 64) || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
