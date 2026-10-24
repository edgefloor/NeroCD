package domain

import "slices"

// HostingApplicationDatabaseStageVersion identifies a durable nonsecret stage.
const HostingApplicationDatabaseStageVersion = "application-database.stage.v1"

// HostingApplicationDatabaseStage retains actual candidate and endpoint evidence
// separately from readiness. Even a ready stage is not HostingHealthy settlement.
// Ready does not complete the operation or release its mutation hold. Replacement
// still requires separate predecessor retirement receipts and atomic completion.
// Unknown create outcomes without a container ID remain operation uncertainty;
// they cannot enter this candidate protocol or authorize another create.
type HostingApplicationDatabaseStage struct {
	Version         string                                 `json:"version"`
	ReceiptID       string                                 `json:"receipt_id"`
	OperationID     string                                 `json:"operation_id"`
	PlanHash        string                                 `json:"plan_hash"`
	Phase           string                                 `json:"phase"`
	Candidate       HostingApplicationDatabaseCandidate    `json:"candidate"`
	AttachPlan      HostingDatabaseBindingEndpointPlan     `json:"attach_plan"`
	EndpointReceipt *HostingDatabaseBindingEndpointReceipt `json:"endpoint_receipt,omitempty"`
	FailureCode     string                                 `json:"failure_code,omitempty"`
	Hash            string                                 `json:"hash"`
}

// DocumentHash binds one immutable stage; replay does not authorize its effects.
func (s HostingApplicationDatabaseStage) DocumentHash() string {
	s.Hash = ""
	return HostingDocumentHash(s)
}

// Validate checks stage exclusivity and the exact observed endpoint when present.
func (s HostingApplicationDatabaseStage) Validate(p HostingApplyPlan, in HostingApplicationDatabasePlanInput) error {
	if s.Version != HostingApplicationDatabaseStageVersion || !validRuntimeReferenceID(s.ReceiptID) || s.OperationID != in.OperationID || s.PlanHash != p.Hash || s.Hash != s.DocumentHash() || ValidateHostingApplicationDatabaseAttachPlan(s.AttachPlan, p, in, s.Candidate) != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if s.Phase == "uncertain" {
		if s.EndpointReceipt != nil || !slices.Contains([]string{"authority_lost", "ownership_mismatch", "inspection_failed", "mutation_unconfirmed", "interrupted"}, s.FailureCode) {
			return ErrInvalidHostingApplicationDatabasePlan
		}
		return nil
	}
	if s.FailureCode != "" {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	switch s.Phase {
	case "candidate_created":
		if s.EndpointReceipt != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	case "attach_intent", "network_configured", "attached", "ready":
		phase := "attach_intent"
		if s.Phase == "attached" || s.Phase == "ready" {
			phase = "attached"
		}
		if s.EndpointReceipt == nil || s.EndpointReceipt.Phase != phase || s.EndpointReceipt.Validate(s.AttachPlan, in.Binding) != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	default:
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}

// ValidateTransition permits identical replay, ordered progress and terminal
// uncertainty. Recovery requires a new admitted operation and fresh authority.
func (s HostingApplicationDatabaseStage) ValidateTransition(previous *HostingApplicationDatabaseStage, p HostingApplyPlan, in HostingApplicationDatabasePlanInput) error {
	if s.Validate(p, in) != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if previous == nil {
		if s.Phase != "candidate_created" {
			return ErrInvalidHostingApplicationDatabasePlan
		}
		return nil
	}
	if previous.Validate(p, in) != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if s.Hash == previous.Hash {
		return nil
	}
	if s.ReceiptID == previous.ReceiptID || s.Candidate != previous.Candidate || s.AttachPlan != previous.AttachPlan || previous.Phase == "uncertain" || previous.Phase == "ready" {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if s.Phase == "uncertain" {
		return nil
	}
	next := map[string]string{"candidate_created": "attach_intent", "attach_intent": "network_configured", "network_configured": "attached", "attached": "ready"}
	if next[previous.Phase] != s.Phase {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	switch s.Phase {
	case "attach_intent":
		if s.EndpointReceipt.ValidateTransition(nil, s.AttachPlan, in.Binding) != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	case "network_configured", "ready":
		if !databaseUpgradeDocumentEqual(s.EndpointReceipt, previous.EndpointReceipt) {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	case "attached":
		if s.EndpointReceipt.ValidateTransition(previous.EndpointReceipt, s.AttachPlan, in.Binding) != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	}
	return nil
}

// UnmarshalJSON bounds stage data and rejects malformed structure. Contextual
// candidate, endpoint and ordering checks require ValidateTransition afterward.
func (s *HostingApplicationDatabaseStage) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStage
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingApplicationDatabaseStageJSONLimit) != nil || v.Version != HostingApplicationDatabaseStageVersion || !validRuntimeReferenceID(v.ReceiptID) || !hostingProxyID.MatchString(v.OperationID) || !imageSHA256.MatchString(v.PlanHash) || v.Hash != HostingApplicationDatabaseStage(v).DocumentHash() || !slices.Contains([]string{"candidate_created", "attach_intent", "network_configured", "attached", "ready", "uncertain"}, v.Phase) {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	*s = HostingApplicationDatabaseStage(v)
	return nil
}
