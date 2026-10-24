package domain

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HostingDatabaseCredentialRequest selects credentials pinned by one live task.
type HostingDatabaseCredentialRequest struct {
	Authority  HostingAuthority `json:"authority"`
	DeliveryID string           `json:"delivery_id"`
	PlanHash   string           `json:"plan_hash"`
	Mode       string           `json:"mode"`
}

// Validate bounds public intent without accepting arbitrary secret references.
func (in HostingDatabaseCredentialRequest) Validate() error {
	a := in.Authority
	prefix := "run_"
	if in.Mode == "observe" {
		prefix = "observe_"
	} else if in.Mode != "deploy" {
		return errors.New("invalid database credential request")
	}
	if a.Attempt < 1 || a.Attempt > math.MaxInt32 || a.RunID != prefix+a.OperationID || !ValidHostingRuntimeDeliveryID(in.DeliveryID) || !imageSHA256.MatchString(in.PlanHash) {
		return errors.New("invalid database credential request")
	}
	for _, v := range []string{a.OperationID, a.RunID, a.LeaseID, a.Fence} {
		if v == "" || len(v) > 256 || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return errors.New("invalid database credential request")
		}
	}
	return nil
}
