package domain

import (
	"encoding/json"
	"errors"
)

// HostingDatabaseMaintenanceVersion preserves member-aware redeploy separately
// from the strict empty-network upgrade contract.
const HostingDatabaseMaintenanceVersion = "database-network.maintenance.v1"

// ErrInvalidHostingDatabaseMaintenance omits retained proof and daemon details.
var ErrInvalidHostingDatabaseMaintenance = errors.New("invalid database network maintenance")

// HostingDatabaseMaintenanceProof retains the original v1 capture and the exact
// settled v2 predecessor. Historical proofs never grant current mutation rights.
type HostingDatabaseMaintenanceProof struct {
	Version            string                                     `json:"version"`
	CapturePlan        HostingDatabaseNetworkCapturePlan          `json:"capture_plan"`
	OriginReceipt      HostingDatabaseNetworkOriginReceipt        `json:"origin_receipt"`
	PreviousPlan       HostingApplyPlan                           `json:"previous_plan"`
	PreviousHealthy    HostingHealthy                             `json:"previous_healthy"`
	MembershipRevision int64                                      `json:"membership_revision"`
	Members            []HostingDatabaseNetworkMemberEvidence     `json:"members"`
	Retired            []HostingDatabaseNetworkRetirementEvidence `json:"retired"`
	Hash               string                                     `json:"hash"`
}

// DocumentHash binds all original retained evidence, including empty arrays.
func (p HostingDatabaseMaintenanceProof) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}
func (p HostingDatabaseMaintenanceProof) input() HostingDatabaseNetworkPlanInput {
	m, r := networkEvidenceInput(p.Members, p.Retired)
	return HostingDatabaseNetworkPlanInput{Origin: p.OriginReceipt.Origin, MembershipRevision: p.MembershipRevision, Members: m, Retired: r}
}

// NewHostingDatabaseMaintenanceProof returns owned serialized evidence and
// rejects a network input whose origin differs from the captured receipt.
func NewHostingDatabaseMaintenanceProof(capture HostingDatabaseNetworkCapturePlan, receipt HostingDatabaseNetworkOriginReceipt, previous HostingApplyPlan, healthy HostingHealthy, in HostingDatabaseNetworkPlanInput) (HostingDatabaseMaintenanceProof, error) {
	if in.Origin != receipt.Origin {
		return HostingDatabaseMaintenanceProof{}, ErrInvalidHostingDatabaseMaintenance
	}
	members, retired := networkEvidence(in.Members, in.Retired)
	p := HostingDatabaseMaintenanceProof{Version: HostingDatabaseMaintenanceVersion, CapturePlan: capture, OriginReceipt: receipt, PreviousPlan: previous, PreviousHealthy: healthy, MembershipRevision: in.MembershipRevision, Members: members, Retired: retired}
	p.Hash = p.DocumentHash()
	raw, err := json.Marshal(p)
	var owned HostingDatabaseMaintenanceProof
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		return HostingDatabaseMaintenanceProof{}, ErrInvalidHostingDatabaseMaintenance
	}
	return owned, nil
}

// Validate requires a v2 predecessor; it cannot broaden empty-upgrade admission.
func (p HostingDatabaseMaintenanceProof) Validate() error {
	if p.Version != HostingDatabaseMaintenanceVersion || p.Hash != p.DocumentHash() || p.Members == nil || p.Retired == nil || ValidateHostingDatabasePlanV2(p.PreviousPlan) != nil || !databaseNetworkSameTarget(p.CapturePlan.SourcePlan, p.PreviousPlan) || p.PreviousPlan.Snapshot.ResourceGeneration < p.CapturePlan.SourcePlan.Snapshot.ResourceGeneration {
		return ErrInvalidHostingDatabaseMaintenance
	}
	// Validate the original capture using the unchanged strict v1 branch.
	capture := HostingDatabaseUpgradeProof{Version: HostingDatabaseUpgradeVersion, CapturePlan: p.CapturePlan, OriginReceipt: p.OriginReceipt, PreviousPlan: p.CapturePlan.SourcePlan, PreviousHealthy: p.CapturePlan.SourceHealthy}
	capture.Hash = capture.DocumentHash()
	if capture.Validate() != nil || p.PreviousHealthy.PlanHash != p.PreviousPlan.Hash || !databaseUpgradeDocumentEqual(p.PreviousHealthy.Snapshot, p.PreviousPlan.Snapshot) || p.PreviousHealthy.Release != p.PreviousPlan.Release || !validRuntimeReferenceID(p.PreviousHealthy.OperationID) || !databaseBindingDockerID(p.PreviousHealthy.ContainerID) || p.PreviousHealthy.Storage == nil || !databaseUpgradeDocumentEqual(p.PreviousHealthy.Storage.Plan, *p.PreviousPlan.Storage) || p.PreviousHealthy.Storage.CreationPlanHash != p.CapturePlan.Materialization.CreationPlanHash {
		return ErrInvalidHostingDatabaseMaintenance
	}
	db := *p.PreviousPlan.Database
	db.Network = nil
	if _, err := CompileHostingDatabasePlanV2(p.PreviousPlan.Snapshot, p.PreviousPlan.Release, p.PreviousHealthy, p.PreviousPlan, *p.PreviousPlan.Storage, db, p.input()); err != nil {
		return ErrInvalidHostingDatabaseMaintenance
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > HostingApplicationDatabaseProofJSONLimit {
		return ErrInvalidHostingDatabaseMaintenance
	}
	return nil
}

// HostingDatabaseMaintenancePlanInput returns owned, validated network input.
func HostingDatabaseMaintenancePlanInput(p HostingDatabaseMaintenanceProof) (HostingDatabaseNetworkPlanInput, error) {
	raw, err := json.Marshal(p)
	var owned HostingDatabaseMaintenanceProof
	if err != nil || json.Unmarshal(raw, &owned) != nil {
		return HostingDatabaseNetworkPlanInput{}, ErrInvalidHostingDatabaseMaintenance
	}
	return owned.input(), nil
}

// CompileHostingDatabaseMaintenancePlan preserves volume, network, credentials,
// release and recipe while compiling the complete historical member transition.
func CompileHostingDatabaseMaintenancePlan(s HostingSnapshot, r HostingRelease, proof HostingDatabaseMaintenanceProof) (HostingApplyPlan, error) {
	in, err := HostingDatabaseMaintenancePlanInput(proof)
	if err != nil {
		return HostingApplyPlan{}, err
	}
	db := *proof.PreviousPlan.Database
	db.Network = nil
	p, err := CompileHostingDatabasePlanV2(s, r, proof.PreviousHealthy, proof.PreviousPlan, *proof.PreviousPlan.Storage, db, in)
	if err != nil {
		return HostingApplyPlan{}, ErrInvalidHostingDatabaseMaintenance
	}
	return p, nil
}

// ValidateHostingDatabaseMaintenanceOperation checks exact family and output.
// Callers must dispatch to this validator for this sidecar, not the v1 validator.
func ValidateHostingDatabaseMaintenanceOperation(op HostingOperation) error {
	if op.DatabaseMaintenance == nil {
		if op.Plan != nil && op.Plan.CompilerVersion == HostingDatabaseNetworkCompilerVersion {
			return ErrInvalidHostingDatabaseMaintenance
		}
		return nil
	}
	proof := *op.DatabaseMaintenance
	if proof.Validate() != nil || !validRuntimeReferenceID(op.ID) || op.ID == proof.PreviousHealthy.OperationID || op.ID == proof.CapturePlan.OperationID || op.ID == proof.CapturePlan.SourceOperationID || op.ProjectID != proof.CapturePlan.ProjectID || op.ResourceID != proof.CapturePlan.ResourceID || op.BindingID != proof.CapturePlan.BindingID || op.Snapshot.ResourceGeneration < proof.PreviousPlan.Snapshot.ResourceGeneration || op.DatabaseUpgrade != nil || op.ApplicationDatabase != nil || op.StorageArchive != nil || op.CleanupPlan != nil {
		return ErrInvalidHostingDatabaseMaintenance
	}
	for _, m := range proof.Members {
		if op.ID == m.EndpointPlan.OperationID || op.ID == m.ApplicationHealthy.OperationID {
			return ErrInvalidHostingDatabaseMaintenance
		}
	}
	for _, r := range proof.Retired {
		if op.ID == r.RetirePlan.OperationID {
			return ErrInvalidHostingDatabaseMaintenance
		}
	}
	// Even queued input must preserve exact predecessor configuration and target.
	if _, err := CompileHostingDatabaseMaintenancePlan(op.Snapshot, proof.PreviousPlan.Release, proof); err != nil {
		return ErrInvalidHostingDatabaseMaintenance
	}
	if op.Plan == nil && op.Release == nil {
		if op.PreparedReceipt != nil {
			return ErrInvalidHostingDatabaseMaintenance
		}
		return nil
	}
	if op.Plan == nil || op.Release == nil {
		return ErrInvalidHostingDatabaseMaintenance
	}
	want, err := CompileHostingDatabaseMaintenancePlan(op.Snapshot, *op.Release, proof)
	if err != nil || !databaseUpgradeDocumentEqual(want, *op.Plan) {
		return ErrInvalidHostingDatabaseMaintenance
	}
	return nil
}

// UnmarshalJSON closes and bounds the entire composed proof.
func (p *HostingDatabaseMaintenanceProof) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseMaintenanceProof
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingApplicationDatabaseProofJSONLimit) != nil || HostingDatabaseMaintenanceProof(v).Validate() != nil {
		return ErrInvalidHostingDatabaseMaintenance
	}
	*p = HostingDatabaseMaintenanceProof(v)
	return nil
}
