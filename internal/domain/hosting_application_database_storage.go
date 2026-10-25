package domain

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	HostingApplicationDatabaseStorageVersion         = "application-database.storage.v1"
	HostingApplicationDatabaseStoragePolicyVersion   = "public-image.application-database.owned-storage.v1"
	HostingApplicationDatabaseStorageCompilerVersion = "image-hosting.application-database.owned-storage.v1"
	HostingApplicationDatabaseStorageCapability      = "hosting_application_database_storage_v1"
	HostingApplicationDatabaseStorageStrategy        = "replacement_with_interruption"
)

// HostingApplicationDatabaseStorageInput reviews current resource-owned metadata.
// Volume names, paths, labels and creation authority are never caller input.
type HostingApplicationDatabaseStorageInput struct {
	ExpectedStorageGeneration int64  `json:"expected_storage_generation"`
	ExpectedStorageHash       string `json:"expected_storage_hash"`
	AcknowledgeInterruption   bool   `json:"acknowledge_interruption"`
}

func (in HostingApplicationDatabaseStorageInput) Validate() error {
	if in.ExpectedStorageGeneration != 1 || !imageSHA256.MatchString(in.ExpectedStorageHash) || !in.AcknowledgeInterruption {
		return ErrInvalidHostingStorage
	}
	return nil
}
func (in *HostingApplicationDatabaseStorageInput) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStorageInput
	var value wire
	if decodeApplicationDatabaseDocument(raw, &value, 4096) != nil || HostingApplicationDatabaseStorageInput(value).Validate() != nil {
		return ErrInvalidHostingStorage
	}
	*in = HostingApplicationDatabaseStorageInput(value)
	return nil
}

// HostingApplicationDatabaseStorageReview is a read snapshot, never admission.
type HostingApplicationDatabaseStorageReview struct {
	Version                         string `json:"version"`
	Strategy                        string `json:"strategy"`
	ExpectedStorageGeneration       int64  `json:"expected_storage_generation"`
	ExpectedStorageHash             string `json:"expected_storage_hash"`
	AcknowledgeInterruptionRequired bool   `json:"acknowledge_interruption_required"`
}

// HostingApplicationDatabaseStorage pins one resource-owned volume and policy.
type HostingApplicationDatabaseStorage struct {
	Version    string            `json:"version"`
	Strategy   string            `json:"strategy"`
	StorageRef HostingStorageRef `json:"storage_ref"`
	Hash       string            `json:"hash"`
}

func NewHostingApplicationDatabaseStorage(ref HostingStorageRef) HostingApplicationDatabaseStorage {
	out := HostingApplicationDatabaseStorage{Version: HostingApplicationDatabaseStorageVersion, Strategy: HostingApplicationDatabaseStorageStrategy, StorageRef: ref}
	out.Hash = out.DocumentHash()
	return out
}
func (p HostingApplicationDatabaseStorage) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}
func (p HostingApplicationDatabaseStorage) Validate(s HostingSnapshot) error {
	if p.Version != HostingApplicationDatabaseStorageVersion || p.Strategy != HostingApplicationDatabaseStorageStrategy || !validHostingStorageRef(p.StorageRef) || p.StorageRef != s.Storage || s.PolicyVersion != HostingApplicationDatabaseStoragePolicyVersion || p.Hash != p.DocumentHash() {
		return ErrInvalidHostingStorage
	}
	return nil
}

// HostingApplicationDatabaseStorageState exposes only durable correlation and
// explicitly offered recovery choices. Actions still require fresh admission.
type HostingApplicationDatabaseStorageState struct {
	RecoveryAction    string   `json:"recovery_action"`
	RecoveryState     string   `json:"recovery_state"`
	Version           string   `json:"version"`
	Phase             string   `json:"phase"`
	ReceiptID         string   `json:"receipt_id"`
	StateHash         string   `json:"state_hash"`
	StageHash         string   `json:"stage_hash"`
	PreviousStateHash string   `json:"previous_state_hash"`
	CandidateHash     string   `json:"candidate_hash"`
	RecoveryActions   []string `json:"recovery_actions"`
}

func (s HostingApplicationDatabaseStorageState) Validate() error {
	if (s.RecoveryState == "none") != (s.RecoveryAction == "") {
		return ErrInvalidHostingStorage
	}
	switch s.RecoveryState {
	case "none", "queued", "running", "succeeded", "uncertain":
	default:
		return ErrInvalidHostingStorage
	}
	if s.RecoveryAction != "" && s.RecoveryAction != "resume_predecessor" && s.RecoveryAction != "complete_replacement" {
		return ErrInvalidHostingStorage
	}
	if len(s.RecoveryActions) > 1 {
		return ErrInvalidHostingStorage
	}
	for _, action := range s.RecoveryActions {
		if action != "resume_predecessor" && action != "complete_replacement" {
			return ErrInvalidHostingStorage
		}
	}
	if (s.StageHash == "") != (s.CandidateHash == "") || s.StageHash != "" && !imageSHA256.MatchString(s.StageHash) {
		return ErrInvalidHostingStorage
	}

	if s.Version != HostingApplicationDatabaseStorageVersion || !hostingProxyID.MatchString(s.ReceiptID) || !imageSHA256.MatchString(s.StateHash) || s.PreviousStateHash != "" && !imageSHA256.MatchString(s.PreviousStateHash) || s.CandidateHash != "" && !imageSHA256.MatchString(s.CandidateHash) {
		return ErrInvalidHostingStorage
	}
	switch s.Phase {
	case "stop_intent", "stopped", "candidate_stopped", "replacement_failed", "resume_intent", "resumed":
	default:
		return errors.New("invalid connected storage phase")
	}
	return nil
}

// HostingApplicationDatabaseStorageWriterReceipt records ordered observed facts.
// The surrounding request retains its exact attempt; receipts never grant effects.
type HostingApplicationDatabaseStorageWriterReceipt struct {
	PredecessorEndpoint *HostingDatabaseBindingEndpointObservation `json:"predecessor_endpoint,omitempty"`
	Version             string                                     `json:"version"`
	ReceiptID           string                                     `json:"receipt_id"`
	Phase               string                                     `json:"phase"`
	PlanHash            string                                     `json:"plan_hash"`
	StorageRef          HostingStorageRef                          `json:"storage_ref"`
	PreviousStateHash   string                                     `json:"previous_state_hash"`
	StageHash           string                                     `json:"stage_hash"`
	CandidateHash       string                                     `json:"candidate_hash"`
	PredecessorHash     string                                     `json:"predecessor_hash"`
	ObservationHash     string                                     `json:"observation_hash"`
	Hash                string                                     `json:"hash"`
}

func (r HostingApplicationDatabaseStorageWriterReceipt) DocumentHash() string {
	r.Hash = ""
	return HostingDocumentHash(r)
}
func (r HostingApplicationDatabaseStorageWriterReceipt) State() HostingApplicationDatabaseStorageState {
	return HostingApplicationDatabaseStorageState{Version: r.Version, Phase: r.Phase, ReceiptID: r.ReceiptID, StateHash: r.Hash, PreviousStateHash: r.PreviousStateHash, StageHash: r.StageHash, CandidateHash: r.CandidateHash, RecoveryActions: []string{}, RecoveryState: "none"}
}
func (r HostingApplicationDatabaseStorageWriterReceipt) Validate() error {
	if (r.Phase == "resumed") != (r.PredecessorEndpoint != nil) || r.PredecessorEndpoint != nil && (!r.PredecessorEndpoint.Present || !imageSHA256.MatchString("sha256:"+r.PredecessorEndpoint.Endpoint.EndpointID)) {
		return ErrInvalidHostingStorage
	}

	if r.State().Validate() != nil || !imageSHA256.MatchString(r.PlanHash) || !validHostingStorageRef(r.StorageRef) || !imageSHA256.MatchString(r.PredecessorHash) || r.Hash != r.DocumentHash() || r.StageHash != "" && !imageSHA256.MatchString(r.StageHash) || (r.StageHash == "") != (r.CandidateHash == "") {
		return ErrInvalidHostingStorage
	}
	intent := r.Phase == "stop_intent" || r.Phase == "resume_intent"
	if intent && r.ObservationHash != "" || !intent && !imageSHA256.MatchString(r.ObservationHash) {
		return ErrInvalidHostingStorage
	}
	return nil
}

type HostingApplicationDatabaseStorageWriterRequest struct {
	HostingAuthority
	Receipt HostingApplicationDatabaseStorageWriterReceipt `json:"receipt"`
}

func (r HostingApplicationDatabaseStorageWriterRequest) Validate() error {
	if !connectionRequestAuthority(r.HostingAuthority) || r.Receipt.Validate() != nil {
		return ErrInvalidHostingStorage
	}
	return nil
}
func (r *HostingApplicationDatabaseStorageWriterRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStorageWriterRequest
	var value wire
	if decodeApplicationDatabaseDocument(raw, &value, 32768) != nil || HostingApplicationDatabaseStorageWriterRequest(value).Validate() != nil || !applicationStorageRequired(raw, "receipt", []string{"version", "receipt_id", "phase", "plan_hash", "storage_ref", "previous_state_hash", "stage_hash", "candidate_hash", "predecessor_hash", "observation_hash", "hash"}) {
		return ErrInvalidHostingStorage
	}
	*r = HostingApplicationDatabaseStorageWriterRequest(value)
	return nil
}

// HostingApplicationDatabaseStorageEffectRequest asks for fresh authority under
// the current writer-state hash; an intent receipt alone cannot authorize Docker.
type HostingApplicationDatabaseStorageEffectRequest struct {
	HostingAuthority
	AuthorizationID string `json:"authorization_id"`
	Action          string `json:"action"`
	StateHash       string `json:"state_hash"`
	StageHash       string `json:"stage_hash"`
}

func (r HostingApplicationDatabaseStorageEffectRequest) Validate() error {
	if !connectionRequestAuthority(r.HostingAuthority) || !hostingStorageRequest.MatchString(r.AuthorizationID) || !imageSHA256.MatchString(r.StateHash) || r.StageHash != "" && !imageSHA256.MatchString(r.StageHash) {
		return ErrInvalidHostingStorage
	}
	switch r.Action {
	case "stop_predecessor", "stop_candidate", "resume_predecessor":
	default:
		return ErrInvalidHostingStorage
	}
	return nil
}
func (r *HostingApplicationDatabaseStorageEffectRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStorageEffectRequest
	var value wire
	if decodeApplicationDatabaseDocument(raw, &value, 16384) != nil || HostingApplicationDatabaseStorageEffectRequest(value).Validate() != nil || !applicationStorageRequired(raw, "", []string{"authorization_id", "action", "state_hash", "stage_hash"}) {
		return ErrInvalidHostingStorage
	}
	*r = HostingApplicationDatabaseStorageEffectRequest(value)
	return nil
}

type HostingApplicationDatabaseStorageEffectAuthorization struct {
	HostingApplicationDatabaseEffectAuthorization
	StateHash string `json:"state_hash"`
}

// HostingApplicationDatabaseStorageRecoveryPlan is a task identity, never a lease.
type HostingApplicationDatabaseStorageRecoveryPlan struct {
	Version          string            `json:"version"`
	OperationID      string            `json:"operation_id"`
	Action           string            `json:"action"`
	RunID            string            `json:"run_id"`
	StorageRef       HostingStorageRef `json:"storage_ref"`
	StorageStateHash string            `json:"storage_state_hash"`
	RequestHash      string            `json:"request_hash"`
	Hash             string            `json:"hash"`
}

func (p HostingApplicationDatabaseStorageRecoveryPlan) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}
func (p HostingApplicationDatabaseStorageRecoveryPlan) Validate() error {
	if p.Version != HostingApplicationDatabaseStorageVersion || !hostingProxyID.MatchString(p.OperationID) || p.Action != "resume_predecessor" && p.Action != "complete_replacement" || !hostingProxyID.MatchString(p.RunID) || !validHostingStorageRef(p.StorageRef) || !imageSHA256.MatchString(p.StorageStateHash) || !imageSHA256.MatchString(p.RequestHash) || p.Hash != p.DocumentHash() {
		return ErrInvalidHostingStorage
	}
	return nil
}

type HostingApplicationDatabaseStorageProxyPlan struct {
	Operation HostingOperation                `json:"operation"`
	Stage     HostingApplicationDatabaseStage `json:"stage"`
	Storage   HostingStorageCreateAck         `json:"storage"`
}

// Validate keeps the old effect authorization framing without extending its action enum.
func (a HostingApplicationDatabaseStorageEffectAuthorization) Validate() error {
	base := a.HostingApplicationDatabaseEffectAuthorization
	action, stage := base.Action, base.StageHash
	base.Action, base.StageHash = "create", ""
	if base.Validate() != nil || !imageSHA256.MatchString(a.StateHash) || stage != "" && !imageSHA256.MatchString(stage) {
		return ErrInvalidHostingStorage
	}
	switch action {
	case "stop_predecessor", "stop_candidate", "resume_predecessor":
		return nil
	default:
		return ErrInvalidHostingStorage
	}
}

func (a *HostingApplicationDatabaseStorageEffectAuthorization) UnmarshalJSON(raw []byte) error {
	var wire struct {
		AuthorizationID    string    `json:"authorization_id"`
		OperationID        string    `json:"operation_id"`
		PlanHash           string    `json:"plan_hash"`
		Action             string    `json:"action"`
		StageHash          string    `json:"stage_hash"`
		LeaseID            string    `json:"lease_id"`
		Attempt            int       `json:"attempt"`
		Fence              string    `json:"fence"`
		MembershipRevision int64     `json:"membership_revision"`
		UseUntil           time.Time `json:"use_until"`
		StateHash          string    `json:"state_hash"`
	}
	if decodeApplicationDatabaseDocument(raw, &wire, 16384) != nil {
		return ErrInvalidHostingStorage
	}
	value := HostingApplicationDatabaseStorageEffectAuthorization{HostingApplicationDatabaseEffectAuthorization: HostingApplicationDatabaseEffectAuthorization{AuthorizationID: wire.AuthorizationID, OperationID: wire.OperationID, PlanHash: wire.PlanHash, Action: wire.Action, StageHash: wire.StageHash, LeaseID: wire.LeaseID, Attempt: wire.Attempt, Fence: wire.Fence, MembershipRevision: wire.MembershipRevision, UseUntil: wire.UseUntil}, StateHash: wire.StateHash}
	if value.Validate() != nil {
		return ErrInvalidHostingStorage
	}
	*a = value
	return nil
}

// HostingDatabaseNetworkResumeEvidence retains the original attachment while
// pinning the new endpoint proven by the explicit failed-replacement recovery.
type HostingDatabaseNetworkResumeEvidence struct {
	Version             string                                         `json:"version"`
	OperationID         string                                         `json:"operation_id"`
	MembershipRevision  int64                                          `json:"membership_revision"`
	OriginalReceiptHash string                                         `json:"original_receipt_hash"`
	WriterReceipt       HostingApplicationDatabaseStorageWriterReceipt `json:"writer_receipt"`
}

// HostingDatabaseMemberEndpoint resolves current evidence without rewriting the
// historical attach receipt or manufacturing a successful deployment.
func HostingDatabaseMemberEndpoint(p HostingDatabaseNetworkMemberProof) (HostingDatabaseBindingEndpoint, string, string, bool) {
	endpoint, ok := p.Receipt.ObservedMembership(p.EndpointPlan, p.Binding)
	if !ok {
		return endpoint, "", "", false
	}
	if p.Resume == nil {
		return endpoint, p.Receipt.ReceiptID, p.Receipt.Hash, true
	}
	r := p.Resume
	w := r.WriterReceipt
	if r.Version != HostingApplicationDatabaseStorageVersion || !hostingProxyID.MatchString(r.OperationID) || r.MembershipRevision <= p.EndpointPlan.MembershipRevision || r.OriginalReceiptHash != p.Receipt.Hash || w.Validate() != nil || w.Phase != "resumed" || w.PredecessorEndpoint == nil || w.PredecessorEndpoint.Endpoint.Target != endpoint.Target || w.PredecessorEndpoint.Endpoint.EndpointID == endpoint.EndpointID || w.PredecessorHash != HostingDocumentHash(p.ApplicationHealthy) || p.ApplicationHealthy.Storage != nil && w.StorageRef != p.ApplicationHealthy.Storage.Plan.Reference {
		return endpoint, "", "", false
	}
	return w.PredecessorEndpoint.Endpoint, w.ReceiptID, w.Hash, true
}

func applicationStorageRequired(raw []byte, nested string, keys []string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	if nested != "" {
		if json.Unmarshal(object[nested], &object) != nil {
			return false
		}
	}
	for _, key := range keys {
		value, ok := object[key]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}
func (s *HostingApplicationDatabaseStorageState) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStorageState
	var value wire
	if decodeApplicationDatabaseDocument(raw, &value, 16384) != nil || !applicationStorageRequired(raw, "", []string{"version", "phase", "receipt_id", "state_hash", "previous_state_hash", "stage_hash", "candidate_hash", "recovery_actions", "recovery_action", "recovery_state"}) || HostingApplicationDatabaseStorageState(value).Validate() != nil {
		return ErrInvalidHostingStorage
	}
	*s = HostingApplicationDatabaseStorageState(value)
	return nil
}
