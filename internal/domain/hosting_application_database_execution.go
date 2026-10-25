package domain

import (
	"bytes"
	"encoding/json"
	"io"
)

// These bounds reserve room for the plan and stage in a future 256 KiB journal.
// No document in this file grants live lease or mutation authority.
const (
	HostingApplicationDatabaseExecutionVersion = "application-database.execution.v1"
	HostingApplicationDatabaseProofJSONLimit   = 128 << 10
	HostingApplicationDatabaseStageJSONLimit   = 16 << 10
	HostingApplicationDatabaseJournalJSONLimit = 256 << 10
)

// HostingDatabaseNetworkMemberEvidence is the closed serialized form of retained
// member proof. ApplicationHealthy is settled historical evidence only.
type HostingDatabaseNetworkMemberEvidence struct {
	Binding              HostingDatabaseBinding                `json:"binding"`
	EndpointPlan         HostingDatabaseBindingEndpointPlan    `json:"endpoint_plan"`
	Receipt              HostingDatabaseBindingEndpointReceipt `json:"receipt"`
	ApplicationHealthy   HostingHealthy                        `json:"application_healthy"`
	DatabasePlan         HostingApplyPlan                      `json:"database_plan"`
	ApplicationProjectID string                                `json:"application_project_id"`
	DatabaseProjectID    string                                `json:"database_project_id"`
}

// HostingDatabaseNetworkRetirementEvidence serializes exact settled retirement.
type HostingDatabaseNetworkRetirementEvidence struct {
	Attached     HostingDatabaseNetworkMemberEvidence  `json:"attached"`
	RetirePlan   HostingDatabaseBindingEndpointPlan    `json:"retire_plan"`
	RetireIntent HostingDatabaseBindingEndpointReceipt `json:"retire_intent"`
	Detached     HostingDatabaseBindingEndpointReceipt `json:"detached"`
}

func networkMemberEvidence(p HostingDatabaseNetworkMemberProof) HostingDatabaseNetworkMemberEvidence {
	return HostingDatabaseNetworkMemberEvidence(p)
}

func (p HostingDatabaseNetworkMemberEvidence) input() HostingDatabaseNetworkMemberProof {
	return HostingDatabaseNetworkMemberProof(p)
}

func networkEvidence(members []HostingDatabaseNetworkMemberProof, retired []HostingDatabaseNetworkRetirementProof) ([]HostingDatabaseNetworkMemberEvidence, []HostingDatabaseNetworkRetirementEvidence) {
	m := make([]HostingDatabaseNetworkMemberEvidence, 0, len(members))
	for _, p := range members {
		m = append(m, networkMemberEvidence(p))
	}
	r := make([]HostingDatabaseNetworkRetirementEvidence, 0, len(retired))
	for _, p := range retired {
		r = append(r, HostingDatabaseNetworkRetirementEvidence{networkMemberEvidence(p.Attached), p.RetirePlan, p.RetireIntent, p.Detached})
	}
	return m, r
}
func networkEvidenceInput(members []HostingDatabaseNetworkMemberEvidence, retired []HostingDatabaseNetworkRetirementEvidence) ([]HostingDatabaseNetworkMemberProof, []HostingDatabaseNetworkRetirementProof) {
	m := make([]HostingDatabaseNetworkMemberProof, 0, len(members))
	for _, p := range members {
		m = append(m, p.input())
	}
	r := make([]HostingDatabaseNetworkRetirementProof, 0, len(retired))
	for _, p := range retired {
		r = append(r, HostingDatabaseNetworkRetirementProof{Attached: p.Attached.input(), RetirePlan: p.RetirePlan, RetireIntent: p.RetireIntent, Detached: p.Detached})
	}
	return m, r
}

// HostingApplicationDatabaseExecutionProof mirrors authenticated compiler input.
// Controllers load every original document and authorize live effects separately.
type HostingApplicationDatabaseExecutionProof struct {
	Version                    string                                     `json:"version"`
	OperationID                string                                     `json:"operation_id"`
	CreateNonce                string                                     `json:"create_nonce"`
	Binding                    HostingDatabaseBinding                     `json:"binding"`
	ApplicationProjectID       string                                     `json:"application_project_id"`
	DatabaseProjectID          string                                     `json:"database_project_id"`
	BindingApplicationSnapshot HostingSnapshot                            `json:"binding_application_snapshot"`
	BindingDatabasePlan        HostingApplyPlan                           `json:"binding_database_plan"`
	DatabasePlan               HostingApplyPlan                           `json:"database_plan"`
	DatabaseHealthy            HostingHealthy                             `json:"database_healthy"`
	Origin                     HostingDatabaseNetworkOrigin               `json:"origin"`
	MembershipRevision         int64                                      `json:"membership_revision"`
	Members                    []HostingDatabaseNetworkMemberEvidence     `json:"members"`
	Retired                    []HostingDatabaseNetworkRetirementEvidence `json:"retired"`
	Hash                       string                                     `json:"hash"`
}

// DocumentHash binds the exact canonical retained documents.
func (p HostingApplicationDatabaseExecutionProof) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}

func (p HostingApplicationDatabaseExecutionProof) input() HostingApplicationDatabasePlanInput {
	m, r := networkEvidenceInput(p.Members, p.Retired)
	return HostingApplicationDatabasePlanInput{OperationID: p.OperationID, CreateNonce: p.CreateNonce, Binding: p.Binding, ApplicationProjectID: p.ApplicationProjectID, DatabaseProjectID: p.DatabaseProjectID, BindingApplicationSnapshot: p.BindingApplicationSnapshot, BindingDatabasePlan: p.BindingDatabasePlan, DatabasePlan: p.DatabasePlan, DatabaseHealthy: p.DatabaseHealthy, Origin: p.Origin, MembershipRevision: p.MembershipRevision, Members: m, Retired: r}
}

// Validate checks historical inputs. Runtime effects require the settled v2 alias.
func (p HostingApplicationDatabaseExecutionProof) Validate() error {
	in := p.input()
	if p.Version != HostingApplicationDatabaseExecutionVersion || p.Hash != p.DocumentHash() || p.Members == nil || p.Retired == nil || len(p.Members) > 1 || p.OperationID == p.DatabaseHealthy.OperationID || p.OperationID == p.Origin.OriginOperation || !hostingProxyID.MatchString(p.OperationID) || !applicationDatabaseNonce(p.CreateNonce) || p.BindingApplicationSnapshot.PolicyVersion != HostingRuntimePolicyVersion || p.BindingApplicationSnapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || p.Binding.ValidateTargets(p.ApplicationProjectID, p.BindingApplicationSnapshot, p.DatabaseProjectID, p.BindingDatabasePlan) != nil || ValidateHostingDatabasePlanV2(p.DatabasePlan) != nil || !applicationDatabaseOrigin(in) {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if _, err := applicationDatabaseMembers(in); err != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	for _, r := range p.Retired {
		if r.RetirePlan.OperationID == p.OperationID {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	}
	for _, member := range p.Members {
		if member.ApplicationHealthy.Snapshot.PolicyVersion != HostingApplicationDatabasePolicyVersion || member.ApplicationHealthy.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{BindingID: p.Binding.ID, BindingHash: p.Binding.Hash}) {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > HostingApplicationDatabaseProofJSONLimit {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}

// NewHostingApplicationDatabaseExecutionProof returns an owned serialized proof.
func NewHostingApplicationDatabaseExecutionProof(in HostingApplicationDatabasePlanInput) (HostingApplicationDatabaseExecutionProof, error) {
	m, r := networkEvidence(in.Members, in.Retired)
	p := HostingApplicationDatabaseExecutionProof{Version: HostingApplicationDatabaseExecutionVersion, OperationID: in.OperationID, CreateNonce: in.CreateNonce, Binding: in.Binding, ApplicationProjectID: in.ApplicationProjectID, DatabaseProjectID: in.DatabaseProjectID, BindingApplicationSnapshot: in.BindingApplicationSnapshot, BindingDatabasePlan: in.BindingDatabasePlan, DatabasePlan: in.DatabasePlan, DatabaseHealthy: in.DatabaseHealthy, Origin: in.Origin, MembershipRevision: in.MembershipRevision, Members: m, Retired: r}
	p.Hash = p.DocumentHash()
	raw, err := json.Marshal(p)
	var owned HostingApplicationDatabaseExecutionProof
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		return owned, ErrInvalidHostingApplicationDatabasePlan
	}
	return owned, nil
}

// HostingApplicationDatabaseExecutionPlanInput returns owned validated input.
func HostingApplicationDatabaseExecutionPlanInput(p HostingApplicationDatabaseExecutionProof) (HostingApplicationDatabasePlanInput, error) {
	raw, err := json.Marshal(p)
	var owned HostingApplicationDatabaseExecutionProof
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		return HostingApplicationDatabasePlanInput{}, ErrInvalidHostingApplicationDatabasePlan
	}
	return owned.input(), nil
}

func decodeApplicationDatabaseDocument(raw []byte, out any, limit int) error {
	var fields map[string]any
	if len(raw) > limit || !validRuntimeJSONUnicode(raw) || json.Unmarshal(raw, &fields) != nil || fields == nil || databaseBindingHasNull(fields) || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}

// UnmarshalJSON rejects unknown, case-aliased, duplicate, null and large proofs.
func (p *HostingApplicationDatabaseExecutionProof) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseExecutionProof
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingApplicationDatabaseProofJSONLimit) != nil || HostingApplicationDatabaseExecutionProof(v).Validate() != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	*p = HostingApplicationDatabaseExecutionProof(v)
	return nil
}

// ValidateHostingApplicationDatabaseOperation checks sidecar and exact compiler
// output. This is historical consistency, never current owner or lease authority.
func ValidateHostingApplicationDatabaseOperation(op HostingOperation) error {
	if op.ApplicationDatabase == nil {
		if op.ApplicationDatabaseRouteContinuity != nil || op.ApplicationDatabaseYield != nil || op.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || op.Plan != nil && op.Plan.ApplicationDatabase != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
		return nil
	}
	if op.ApplicationDatabaseYield != nil && (op.ApplicationDatabaseRouteContinuity == nil || op.ApplicationDatabaseYield.Validate() != nil || op.Plan == nil || op.ApplicationDatabaseYield.PlanHash != op.Plan.Hash || op.ApplicationDatabaseYield.ContinuationHash != op.ApplicationDatabaseRouteContinuity.Hash) {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	proof := *op.ApplicationDatabase
	if op.ApplicationDatabaseRouteContinuity != nil && op.ApplicationDatabaseRouteContinuity.Validate(op) != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if proof.Validate() != nil || op.ID != proof.OperationID || op.ProjectID != proof.Binding.ProjectID || op.ResourceID != proof.Binding.ApplicationResourceID || op.BindingID != proof.Binding.ApplicationBindingID || op.Snapshot.PolicyVersion != HostingApplicationDatabasePolicyVersion || proof.Binding.ValidateTargets(op.ProjectID, op.Snapshot, proof.DatabaseProjectID, proof.BindingDatabasePlan) != nil || op.DatabaseUpgrade != nil || op.DatabaseMaintenance != nil || op.CleanupPlan != nil || op.StorageArchive != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if op.Release == nil && op.Plan == nil {
		if op.PreparedReceipt != nil {
			return ErrInvalidHostingApplicationDatabasePlan
		}
		return nil
	}
	if op.Release == nil || op.Plan == nil || *op.Release != op.Plan.Release || op.Snapshot != op.Plan.Snapshot || ValidateHostingApplicationDatabasePlan(*op.Plan, proof.input()) != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	if len(proof.Members) == 1 && op.Plan.PreviousHealthy == nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}
