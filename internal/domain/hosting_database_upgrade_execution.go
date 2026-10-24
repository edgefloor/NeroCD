package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// These identifiers describe one staged empty-network transition, not authority.
const (
	HostingDatabaseUpgradeVersion    = "database-network.empty-upgrade.v1"
	HostingDatabaseUpgradeCapability = "hosting_database_network_upgrade_v1"
	// HostingDatabaseUpgradeProofJSONLimit bounds the composed historical proof.
	// Nested capture documents retain their existing, smaller decoder bounds.
	HostingDatabaseUpgradeProofJSONLimit = 64 << 10
)

// ErrInvalidHostingDatabaseUpgrade excludes historical proof and target details.
var ErrInvalidHostingDatabaseUpgrade = errors.New("invalid database network upgrade")

// HostingDatabaseUpgradeInput selects an already retained capture and target.
// No caller-provided runtime, membership, command or credential values are accepted.
type HostingDatabaseUpgradeInput struct {
	ExpectedGeneration int64  `json:"expected_generation"`
	CaptureID          string `json:"capture_id"`
	ExpectedOriginHash string `json:"expected_origin_hash"`
	IdempotencyKey     string `json:"idempotency_key"`
}

// Validate requires the current generation and complete retained origin identity.
func (in HostingDatabaseUpgradeInput) Validate() error {
	if (HostingDatabaseDeploymentInput{ExpectedGeneration: in.ExpectedGeneration, IdempotencyKey: in.IdempotencyKey}).Validate() != nil || len(in.CaptureID) != 36 || !strings.HasPrefix(in.CaptureID, "hnc_") || strings.Trim(in.CaptureID[4:], "0123456789abcdef") != "" || !imageSHA256.MatchString(in.ExpectedOriginHash) {
		return ErrInvalidHostingDatabaseUpgrade
	}
	return nil
}

// HostingDatabaseUpgradeProof carries complete nonsecret controller evidence.
// It never carries a historical lease, fence or permission for current effects.
type HostingDatabaseUpgradeProof struct {
	Version         string                              `json:"version"`
	CapturePlan     HostingDatabaseNetworkCapturePlan   `json:"capture_plan"`
	OriginReceipt   HostingDatabaseNetworkOriginReceipt `json:"origin_receipt"`
	PreviousPlan    HostingApplyPlan                    `json:"previous_plan"`
	PreviousHealthy HostingHealthy                      `json:"previous_healthy"`
	Hash            string                              `json:"hash"`
}

// DocumentHash binds every serialized proof field with the existing document hash.
func (p HostingDatabaseUpgradeProof) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}

func databaseUpgradeDocumentEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

// Validate checks historical identity only. Live owner, grants, generation,
// capabilities, credentials and mutation admission remain controller checks.
func (p HostingDatabaseUpgradeProof) Validate() error {
	c, r, previous, h := p.CapturePlan, p.OriginReceipt, p.PreviousPlan, p.PreviousHealthy
	if p.Version != HostingDatabaseUpgradeVersion || !imageSHA256.MatchString(p.Hash) || p.Hash != p.DocumentHash() || c.Validate() != nil || c.ValidateOrigin(r.Origin) != nil || !validRuntimeReferenceID(r.ID) || !imageSHA256.MatchString(r.BodyHash) || r.CreatedAt.IsZero() || r.ObservedAt.IsZero() || r.ObservedAt.After(r.CreatedAt.Add(30*time.Second)) || r.OperationID != c.OperationID || r.SourceOperationID != c.SourceOperationID || r.PlanHash != c.Hash || r.OriginHash != r.Origin.DocumentHash() || r.RunnerID != c.RunnerID {
		return ErrInvalidHostingDatabaseUpgrade
	}
	if validateDatabaseOriginalPlan(previous) != nil || !databaseNetworkSameTarget(previous, c.SourcePlan) || previous.Snapshot != c.SourcePlan.Snapshot || ValidateHostingHealthyStorage(h) != nil || !validRuntimeReferenceID(h.OperationID) || !databaseBindingDockerID(h.ContainerID) || h.PlanHash != previous.Hash || h.Snapshot != previous.Snapshot || h.Release != previous.Release || h.Storage == nil || !databaseUpgradeDocumentEqual(h.Storage.Plan, *previous.Storage) || h.Storage.CreationPlanHash != c.Materialization.CreationPlanHash {
		return ErrInvalidHostingDatabaseUpgrade
	}
	switch previous.CompilerVersion {
	case HostingDatabaseCompilerVersion:
		if !databaseUpgradeDocumentEqual(previous, c.SourcePlan) || !databaseUpgradeDocumentEqual(h, c.SourceHealthy) {
			return ErrInvalidHostingDatabaseUpgrade
		}
	case HostingDatabaseNetworkCompilerVersion:
		n := previous.Database.Network
		if n == nil || n.Members == nil || len(n.Members) != 0 || n.NetworkID != r.Origin.NetworkID || n.OriginHash != r.OriginHash || n.Alias != HostingDatabaseBindingAlias(c.BindingID) {
			return ErrInvalidHostingDatabaseUpgrade
		}
	default:
		return ErrInvalidHostingDatabaseUpgrade
	}
	return nil
}

// HostingDatabaseUpgradePlanInput derives only empty membership. An unchanged
// empty v2 predecessor retains its revision; the first v1 upgrade starts at one.
func HostingDatabaseUpgradePlanInput(proof HostingDatabaseUpgradeProof) (HostingDatabaseNetworkPlanInput, error) {
	if proof.Validate() != nil {
		return HostingDatabaseNetworkPlanInput{}, ErrInvalidHostingDatabaseUpgrade
	}
	revision := int64(1)
	if n := proof.PreviousPlan.Database.Network; n != nil {
		revision = n.MembershipRevision
	}
	return HostingDatabaseNetworkPlanInput{Origin: proof.OriginReceipt.Origin, MembershipRevision: revision, Members: []HostingDatabaseNetworkMemberProof{}}, nil
}

// ValidateHostingDatabaseUpgradeOperation requires the frozen target and exact
// compiler output. Ordinary operations with no upgrade proof keep their wire form.
func ValidateHostingDatabaseUpgradeOperation(op HostingOperation) error {
	if op.DatabaseUpgrade == nil {
		if op.Plan != nil && (op.Plan.CompilerVersion == HostingDatabaseNetworkCompilerVersion || op.Plan.Database != nil && op.Plan.Database.Network != nil) {
			return ErrInvalidHostingDatabaseUpgrade
		}
		return nil
	}
	proof := *op.DatabaseUpgrade
	if proof.Validate() != nil || !validRuntimeReferenceID(op.ID) || op.ID == proof.PreviousHealthy.OperationID || op.ProjectID != proof.CapturePlan.ProjectID || op.ResourceID != proof.CapturePlan.ResourceID || op.BindingID != proof.CapturePlan.BindingID || op.Snapshot != proof.PreviousPlan.Snapshot || op.StorageArchive != nil || op.CleanupPlan != nil {
		return ErrInvalidHostingDatabaseUpgrade
	}
	if op.Plan == nil && op.Release == nil {
		if op.PreparedReceipt != nil {
			return ErrInvalidHostingDatabaseUpgrade
		}
		return nil
	}
	if op.Plan == nil || op.Release == nil || *op.Release != proof.PreviousPlan.Release {
		return ErrInvalidHostingDatabaseUpgrade
	}
	in, err := HostingDatabaseUpgradePlanInput(proof)
	if err != nil {
		return err
	}
	database := *proof.PreviousPlan.Database
	database.Network = nil
	want, err := CompileHostingDatabasePlanV2(op.Snapshot, *op.Release, proof.PreviousHealthy, proof.PreviousPlan, *proof.PreviousPlan.Storage, database, in)
	if err != nil || !databaseUpgradeDocumentEqual(want, *op.Plan) {
		return ErrInvalidHostingDatabaseUpgrade
	}
	return nil
}

// UnmarshalJSON rejects open, duplicate, null and oversized request documents.
func (in *HostingDatabaseUpgradeInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseUpgradeInput
	var value wire
	if len(raw) > 4096 || decodeDatabaseBinding(raw, &value) != nil || HostingDatabaseUpgradeInput(value).Validate() != nil {
		return ErrInvalidHostingDatabaseUpgrade
	}
	*in = HostingDatabaseUpgradeInput(value)
	return nil
}

// UnmarshalJSON bounds the composed proof independently of its nested documents.
func (p *HostingDatabaseUpgradeProof) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseUpgradeProof
	var value wire
	var fields map[string]any
	if len(raw) > HostingDatabaseUpgradeProofJSONLimit || !validRuntimeJSONUnicode(raw) || json.Unmarshal(raw, &fields) != nil || fields == nil || databaseBindingHasNull(fields) || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return ErrInvalidHostingDatabaseUpgrade
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || HostingDatabaseUpgradeProof(value).Validate() != nil {
		return ErrInvalidHostingDatabaseUpgrade
	}
	*p = HostingDatabaseUpgradeProof(value)
	return nil
}
