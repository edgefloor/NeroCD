package domain

import (
	"encoding/hex"
	"errors"
	"slices"
)

const HostingProxyStorageSwitchVersion = "application-database.storage-proxy.v1"

// HostingProxyStorageSwitch is controller-derived exact interrupted-writer intent.
// It changes only the prerequisite of predecessor HTTP readiness; candidate
// HTTPS verification and complete route scope remain mandatory.
type HostingProxyStorageSwitch struct {
	Version                 string            `json:"version"`
	Action                  string            `json:"action"`
	OperationID             string            `json:"operation_id"`
	ContinuationHash        string            `json:"continuation_hash"`
	StorageRef              HostingStorageRef `json:"storage_ref"`
	StorageOriginHash       string            `json:"storage_origin_hash"`
	WriterStateHash         string            `json:"writer_state_hash"`
	CandidateHash           string            `json:"candidate_hash"`
	RouteScopeHash          string            `json:"route_scope_hash"`
	PriorGeneration         int64             `json:"prior_generation"`
	PriorPreviousGeneration int64             `json:"prior_previous_generation"`
	PriorEngineHash         string            `json:"prior_engine_hash"`
	PriorRequiredRouteIDs   []string          `json:"prior_required_route_ids"`
	FailedProxyOperationID  string            `json:"failed_proxy_operation_id,omitempty"`
	FailedGeneration        int64             `json:"failed_generation,omitempty"`
	FailedPlanHash          string            `json:"failed_plan_hash,omitempty"`
	FailedEngineHash        string            `json:"failed_engine_hash,omitempty"`
	Hash                    string            `json:"hash"`
}

func (p HostingProxyStorageSwitch) DocumentHash() string { p.Hash = ""; return HostingDocumentHash(p) }
func proxyStorageEngineHash(hash string) bool {
	value, err := hex.DecodeString(hash)
	return err == nil && len(value) == 32 && hex.EncodeToString(value) == hash
}
func (p HostingProxyStorageSwitch) Validate(snapshot HostingProxySnapshot, previous int64) error {
	if p.Version != HostingProxyStorageSwitchVersion || p.Hash != p.DocumentHash() || !hostingProxyID.MatchString(p.OperationID) || !imageSHA256.MatchString(p.ContinuationHash) || !validHostingStorageRef(p.StorageRef) || !imageSHA256.MatchString(p.StorageOriginHash) || !imageSHA256.MatchString(p.WriterStateHash) || !imageSHA256.MatchString(p.CandidateHash) || p.RouteScopeHash != HostingDocumentHash(snapshot.Routes) || p.PriorGeneration != previous || p.PriorGeneration < 1 || p.PriorPreviousGeneration < 0 || p.PriorPreviousGeneration >= p.PriorGeneration || !proxyStorageEngineHash(p.PriorEngineHash) || snapshot.VerifiedSwitch == nil {
		return ErrInvalidHostingStorage
	}
	if len(p.PriorRequiredRouteIDs) > len(snapshot.Routes) || !slices.IsSorted(p.PriorRequiredRouteIDs) {
		return ErrInvalidHostingStorage
	}
	routeIDs := map[string]bool{}
	for _, route := range snapshot.Routes {
		routeIDs[route.RouteID] = true
	}
	for i, id := range p.PriorRequiredRouteIDs {
		if !hostingProxyID.MatchString(id) || !routeIDs[id] || i > 0 && p.PriorRequiredRouteIDs[i-1] == id {
			return ErrInvalidHostingStorage
		}
	}
	switch p.Action {
	case "switch":
		if p.FailedProxyOperationID != "" || p.FailedGeneration != 0 || p.FailedPlanHash != "" || p.FailedEngineHash != "" {
			return ErrInvalidHostingStorage
		}
	case "complete_replacement":
		if !hostingProxyID.MatchString(p.FailedProxyOperationID) || p.FailedGeneration <= previous || snapshot.Generation <= p.FailedGeneration || !imageSHA256.MatchString(p.FailedPlanHash) || !proxyStorageEngineHash(p.FailedEngineHash) {
			return ErrInvalidHostingStorage
		}
	default:
		return errors.New("invalid interrupted storage proxy action")
	}
	return nil
}
