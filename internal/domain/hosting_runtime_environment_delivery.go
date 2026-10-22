package domain

import (
	"encoding/hex"
	"strings"
	"time"
)

// HostingRuntimeEnvironmentRequest selects only the prepared operation's pinned document.
type HostingRuntimeEnvironmentRequest struct {
	Authority            HostingAuthority               `json:"authority"`
	PlanHash             string                         `json:"plan_hash"`
	RuntimeConfiguration HostingRuntimeConfigurationRef `json:"runtime_configuration"`
	DeliveryID           string                         `json:"delivery_id"`
}

// HostingRuntimeEnvironmentWindow retains the original delivery deadline across retries.
type HostingRuntimeEnvironmentWindow struct {
	DeliveryID  string    `json:"delivery_id"`
	RequestHash string    `json:"request_hash"`
	UseUntil    time.Time `json:"use_until"`
}

// ValidHostingRuntimeDeliveryID accepts a canonical cryptographically generated 128-bit ID.
func ValidHostingRuntimeDeliveryID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
