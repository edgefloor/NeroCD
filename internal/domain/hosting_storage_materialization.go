package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"strings"
)

// These versions identify explicit owned-storage admission and compilation.
const (
	HostingOwnedStorageRuntimeCapability      = "hosting_owned_storage_runtime_v1"
	HostingOwnedStorageRuntimePolicyVersion   = "public-image.owned-storage.runtime-env.release-ports.v1"
	HostingOwnedStorageRuntimeCompilerVersion = "image-hosting.owned-storage.runtime-env.v1"
	HostingOwnedStorageCapability             = "hosting_owned_storage_v1"
	HostingOwnedStoragePolicyVersion          = "public-image.owned-storage.release-ports.v1"
	HostingOwnedStorageCompilerVersion        = "image-hosting.owned-storage.v1"
	HostingStorageProtocolJSONLimit           = 32 << 10
)

// HostingStoragePlan is frozen before its containing plan is hashed. Its origin
// survives operation failure and later deployments; it is not a create permit.
type HostingStoragePlan struct {
	Reference           HostingStorageRef `json:"reference"`
	VolumeName          string            `json:"volume_name"`
	MountPath           string            `json:"mount_path"`
	CreationOperationID string            `json:"creation_operation_id"`
	CreationNonce       string            `json:"creation_nonce"`
	DaemonID            string            `json:"daemon_id"`
}

// HostingHealthyStorage preserves the resolved mount and original creation plan
// across later releases. It is evidence, never permission to create a volume.
type HostingHealthyStorage struct {
	Plan             HostingStoragePlan `json:"plan"`
	CreationPlanHash string             `json:"creation_plan_hash"`
}

// ValidateHostingHealthyStorage rejects invented or omitted storage evidence.
func ValidateHostingHealthyStorage(healthy HostingHealthy) error {
	if healthy.Stack != nil || healthy.Snapshot.Stack != (HostingStackRef{}) {
		return ValidateHostingStackHealthy(healthy)
	}
	if healthy.Snapshot.Storage == (HostingStorageRef{}) {
		if healthy.Storage != nil {
			return ErrInvalidHostingStorage
		}
		return ValidateHostingStorageSnapshot(healthy.Snapshot)
	}
	if healthy.Storage == nil || healthy.Storage.Plan.Validate(healthy.Snapshot, healthy.Release) != nil || !imageSHA256.MatchString(healthy.Storage.CreationPlanHash) {
		return ErrInvalidHostingStorage
	}
	return nil
}

// HostingStorageMaterialization records irreversible creation admission, not
// Docker existence. Only a committed exact receipt advances it to materialized.
type HostingStorageMaterialization struct {
	StorageRef          HostingStorageRef `json:"storage_ref"`
	State               string            `json:"state"`
	CreationOperationID string            `json:"creation_operation_id"`
	CreationPlanHash    string            `json:"creation_plan_hash"`
	CreationNonce       string            `json:"creation_nonce"`
	DaemonID            string            `json:"daemon_id"`
	Labels              map[string]string `json:"labels"`
	MaterializedReceipt *HostingReceipt   `json:"materialized_receipt,omitempty"`
}

// HostingStorageCreateRequest requests durable creation admission for one plan.
type HostingStorageCreateRequest struct {
	HostingAuthority
	PlanHash      string            `json:"plan_hash"`
	StorageRef    HostingStorageRef `json:"storage_ref"`
	CreationNonce string            `json:"creation_nonce"`
}

// HostingStorageCreateAck grants CreatePermitted only to the transaction
// inserting the origin. Every retry must inspect, not create.
type HostingStorageCreateAck struct {
	Storage         HostingStorage                `json:"storage"`
	Materialization HostingStorageMaterialization `json:"materialization"`
	CreatePermitted bool                          `json:"create_permitted"`
}

// DecodeHostingStorageProtocolJSON decodes one bounded closed document, rejecting
// duplicate keys and case aliases before typed decoding. Protocol field and
// label names are canonical lowercase; label values remain case-sensitive.
func DecodeHostingStorageProtocolJSON(raw []byte, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > HostingStorageProtocolJSONLimit || len(trimmed) == 0 || trimmed[0] != '{' || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return ErrInvalidHostingStorage
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrInvalidHostingStorage
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrInvalidHostingStorage
	}
	return nil
}

func storageProtocolUniqueJSON(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || key != strings.ToLower(key) || seen[key] {
				return false
			}
			seen[key] = true
			if !storageProtocolUniqueJSON(d) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !storageProtocolUniqueJSON(d) {
				return false
			}
		}
	default:
		return false
	}
	_, err = d.Token()
	return err == nil
}

// HostingStorageMaterializedRequest contains nonsecret observations only.
// The controller derives and compares the complete closed label map itself.
type HostingStorageMaterializedRequest struct {
	HostingAuthority
	ReceiptID           string            `json:"receipt_id"`
	PlanHash            string            `json:"plan_hash"`
	StorageRef          HostingStorageRef `json:"storage_ref"`
	CreationOperationID string            `json:"creation_operation_id"`
	CreationPlanHash    string            `json:"creation_plan_hash"`
	CreationNonce       string            `json:"creation_nonce"`
	DaemonID            string            `json:"daemon_id"`
	VolumeName          string            `json:"volume_name"`
	Driver              string            `json:"driver"`
	Scope               string            `json:"scope"`
	Labels              map[string]string `json:"labels"`
	Options             map[string]string `json:"options"`
}

// HostingStorageMaterializedAck acknowledges committed exact storage evidence.
type HostingStorageMaterializedAck struct {
	Materialization HostingStorageMaterialization `json:"materialization"`
	Receipt         HostingReceipt                `json:"receipt"`
}

func validHostingStorageRef(ref HostingStorageRef) bool {
	return len(ref.StorageID) == 36 && ref.StorageID[:4] == "hsv_" && hostingStorageRequest.MatchString(ref.StorageID[4:]) && ref.Generation == 1 && imageSHA256.MatchString(ref.Hash)
}

// ValidateHostingStorageSnapshot prevents mixed or silently ignored policy fields.
func ValidateHostingStorageSnapshot(s HostingSnapshot) error {
	if err := ValidateHostingDatabaseSnapshot(s); err != nil {
		return err
	}
	has := s.Storage != (HostingStorageRef{})
	if !has {
		if s.PolicyVersion == HostingOwnedStoragePolicyVersion || s.PolicyVersion == HostingOwnedStorageRuntimePolicyVersion || s.PolicyVersion == HostingDatabasePolicyVersion || s.PolicyVersion == HostingApplicationDatabaseStoragePolicyVersion {
			return ErrInvalidHostingStorage
		}
		return nil
	}
	if s.PolicyVersion == HostingOwnedStorageRuntimePolicyVersion {
		if !validHostingStorageRef(s.Storage) || s.LocalBuild != (HostingLocalBuildSource{}) || s.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || s.Database != (HostingDatabaseExecutionRef{}) || ValidateHostingRuntimeConfigurationRef(s) != nil {
			return ErrInvalidHostingStorage
		}
		return nil
	}
	if s.PolicyVersion == HostingApplicationDatabaseStoragePolicyVersion {
		if !validHostingStorageRef(s.Storage) || s.LocalBuild != (HostingLocalBuildSource{}) || s.Database != (HostingDatabaseExecutionRef{}) || s.StorageRouteContinuity != (HostingStorageRouteContinuityRef{}) || ValidateHostingRuntimeConfigurationRef(s) != nil {
			return ErrInvalidHostingStorage
		}
		return nil
	}
	if !validHostingStorageRef(s.Storage) || (s.PolicyVersion != HostingOwnedStoragePolicyVersion && s.PolicyVersion != HostingDatabasePolicyVersion) || s.LocalBuild != (HostingLocalBuildSource{}) || s.RuntimeConfiguration != (HostingRuntimeConfigurationRef{}) {
		return ErrInvalidHostingStorage
	}
	return nil
}

// HostingStorageCreationNonce is nonsecret provenance, not authorization. It is
// fixed before hashing the first plan and reused from durable history thereafter.
func HostingStorageCreationNonce(ref HostingStorageRef, operation, daemon string) string {
	return HostingDocumentHash(struct {
		Reference         HostingStorageRef
		Operation, Daemon string
	}{ref, operation, daemon})[7:39]
}

// Validate binds the resolved storage descriptor to its snapshot and daemon.
func (p HostingStoragePlan) Validate(s HostingSnapshot, release HostingRelease) error {
	if !validHostingStorageRef(p.Reference) || ValidateHostingStorageSnapshot(s) != nil || p.Reference != s.Storage || p.VolumeName != "ncdv_"+p.Reference.StorageID[4:] || ValidateHostingStorageMountPath(p.MountPath) != nil || !validRuntimeReferenceID(p.CreationOperationID) || !validRuntimeReferenceID(p.DaemonID) || p.DaemonID != release.DaemonID || p.CreationNonce != HostingStorageCreationNonce(p.Reference, p.CreationOperationID, p.DaemonID) {
		return ErrInvalidHostingStorage
	}
	return nil
}

// CompileHostingOwnedStoragePlan reuses retained-release rules with an explicit
// storage compiler. No old compiler can accept the storage fields.
func CompileHostingOwnedStoragePlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy, retained []HostingHealthy, storage HostingStoragePlan) (HostingApplyPlan, error) {
	if storage.Validate(snapshot, release) != nil {
		return HostingApplyPlan{}, ErrInvalidHostingStorage
	}
	plan, err := compileRetainedHostingPlan(snapshot, release, previous, retained, HostingOwnedStoragePolicyVersion, HostingOwnedStorageCompilerVersion)
	if err != nil {
		return plan, err
	}
	plan.Storage = &storage
	// A shared writable volume permits only one application writer. The prior
	// release remains retained, but its container must be stopped first.
	plan.RetainPrevious = false
	plan.Hash = ""
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

// ExpectedHostingStorageLabels has one exact, controller-derived key set. A
// volume with additional labels or local-driver options is not adopted.
func ExpectedHostingStorageLabels(storage HostingStorage, m HostingStorageMaterialization) map[string]string {
	return map[string]string{
		"io.nerocd.storage.id": storage.ID, "io.nerocd.storage.hash": storage.Hash, "io.nerocd.storage.generation": "1",
		"io.nerocd.storage.project": storage.ProjectID, "io.nerocd.storage.resource": storage.ResourceID,
		"io.nerocd.storage.destination": storage.DestinationID, "io.nerocd.binding": storage.BindingID,
		"io.nerocd.storage.origin_plan": m.CreationPlanHash, "io.nerocd.storage.origin_operation": m.CreationOperationID, "io.nerocd.storage.creation_nonce": m.CreationNonce, "io.nerocd.storage.daemon": m.DaemonID,
	}
}

// Validate checks the immutable creation identity and its evidence state.
func (m HostingStorageMaterialization) Validate(storage HostingStorage) error {
	if storage.Validate() != nil || m.StorageRef != storage.Reference() || !validRuntimeReferenceID(m.CreationOperationID) || !imageSHA256.MatchString(m.CreationPlanHash) || !validRuntimeReferenceID(m.DaemonID) || m.CreationNonce != HostingStorageCreationNonce(m.StorageRef, m.CreationOperationID, m.DaemonID) || !maps.Equal(m.Labels, ExpectedHostingStorageLabels(storage, m)) {
		return ErrInvalidHostingStorage
	}
	if m.State == "create_admitted" && m.MaterializedReceipt == nil {
		return nil
	}
	if m.State == "materialized" && m.MaterializedReceipt != nil && validRuntimeReferenceID(m.MaterializedReceipt.ID) && imageSHA256.MatchString(m.MaterializedReceipt.BodyHash) && !m.MaterializedReceipt.CreatedAt.IsZero() {
		return nil
	}
	return ErrInvalidHostingStorage
}

// Validate checks creation document shape; the store checks live authority.
func (in HostingStorageCreateRequest) Validate() error {
	if in.Attempt < 1 || in.Attempt > math.MaxInt32 || !validHostingStorageRef(in.StorageRef) || !imageSHA256.MatchString(in.PlanHash) || !hostingStorageRequest.MatchString(in.CreationNonce) {
		return ErrInvalidHostingStorage
	}
	return nil
}

// Validate rejects observations outside the closed local-volume contract.
func (in HostingStorageMaterializedRequest) Validate() error {
	if in.Attempt < 1 || in.Attempt > math.MaxInt32 || !validRuntimeReferenceID(in.ReceiptID) || !validHostingStorageRef(in.StorageRef) || !imageSHA256.MatchString(in.PlanHash) || !imageSHA256.MatchString(in.CreationPlanHash) || !validRuntimeReferenceID(in.CreationOperationID) || !validRuntimeReferenceID(in.DaemonID) || !hostingStorageRequest.MatchString(in.CreationNonce) || in.VolumeName != "ncdv_"+in.StorageRef.StorageID[4:] || in.Driver != "local" || in.Scope != "local" || len(in.Options) != 0 || len(in.Labels) != 11 {
		return ErrInvalidHostingStorage
	}
	for key, value := range in.Labels {
		if len(key) > 128 || !validRuntimeReferenceID(value) {
			return ErrInvalidHostingStorage
		}
	}
	return nil
}

// Matches proves exact observation identity; it never infers physical existence.
func (in HostingStorageMaterializedRequest) Matches(storage HostingStorage, m HostingStorageMaterialization) error {
	if in.Validate() != nil || m.Validate(storage) != nil || in.StorageRef != m.StorageRef || in.CreationOperationID != m.CreationOperationID || in.CreationPlanHash != m.CreationPlanHash || in.CreationNonce != m.CreationNonce || in.DaemonID != m.DaemonID || in.VolumeName != storage.VolumeName || !maps.Equal(in.Labels, ExpectedHostingStorageLabels(storage, m)) {
		return errors.New("storage materialization identity mismatch")
	}
	return nil
}

// HostingStorageLabelsHash hashes only the canonical controller-derived map.
func HostingStorageLabelsHash(storage HostingStorage, m HostingStorageMaterialization) string {
	return HostingDocumentHash(ExpectedHostingStorageLabels(storage, m))
}

// CompileHostingOwnedStorageRuntimePlan pins runtime metadata and owned storage in one image plan.
func CompileHostingOwnedStorageRuntimePlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy, retained []HostingHealthy, storage HostingStoragePlan) (HostingApplyPlan, error) {
	if snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || snapshot.Database != (HostingDatabaseExecutionRef{}) || snapshot.LocalBuild != (HostingLocalBuildSource{}) || release.LocalBuild != (HostingBuildArtifact{}) || storage.Validate(snapshot, release) != nil {
		return HostingApplyPlan{}, ErrInvalidHostingStorage
	}
	plan, err := compileRetainedHostingPlan(snapshot, release, previous, retained, HostingOwnedStorageRuntimePolicyVersion, HostingOwnedStorageRuntimeCompilerVersion)
	if err != nil {
		return plan, err
	}
	plan.Storage = &storage
	plan.RetainPrevious = false
	plan.Hash = ""
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}
