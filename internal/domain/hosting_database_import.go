package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// External imports have separate authority from platform-created backup archives.
const (
	HostingDatabaseImportStageVersion          = "mysql.compose-external-s3-stage.v1"
	HostingDatabaseImportVersion               = "mysql.compose-external-import.v1"
	HostingDatabaseImportStageCapability       = "hosting_compose_mysql_import_stage_v1"
	HostingDatabaseImportCapability            = "hosting_compose_mysql_import_v1"
	RunTypeDatabaseImportStage                 = "database_import_stage"
	RunTypeDatabaseImport                      = "database_import"
	HostingDatabaseImportMaxBytes        int64 = 10 << 30
	HostingDatabaseImportStageTTL              = time.Hour
)

// ErrInvalidDatabaseImport reports a rejected external SQL import document.
var ErrInvalidDatabaseImport = errors.New("invalid external database import")

var databaseImportFileID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// HostingDatabaseImportS3Source selects an existing configured S3 destination and exact key.
type HostingDatabaseImportS3Source struct {
	TargetID  string `json:"target_id"`
	Revision  int64  `json:"revision"`
	ObjectKey string `json:"object_key"`
}

// Validate rejects endpoints, control bytes and unbounded object keys.
func (s HostingDatabaseImportS3Source) Validate() error {
	if !validRuntimeReferenceID(s.TargetID) || s.Revision < 1 || !validImportObjectKey(s.ObjectKey) {
		return ErrInvalidDatabaseImport
	}
	return nil
}

func validImportObjectKey(key string) bool {
	return len(key) > 0 && len(key) <= 1024 && utf8.ValidString(key) && !strings.ContainsFunc(key, unicode.IsControl)
}

// HostingDatabaseImportStageInput binds inspection to the intended current target.
type HostingDatabaseImportStageInput struct {
	RequestID                   string                        `json:"request_id"`
	Component                   string                        `json:"component"`
	ExpectedDeployedOperationID string                        `json:"expected_deployed_operation_id"`
	ExpectedResourceGeneration  int64                         `json:"expected_resource_generation"`
	S3Source                    HostingDatabaseImportS3Source `json:"s3_source"`
}

// Validate checks inspection input without granting SQL or backup provenance.
func (in HostingDatabaseImportStageInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.Component != "mysql" || !validRuntimeReferenceID(in.ExpectedDeployedOperationID) || in.ExpectedResourceGeneration < 1 || in.S3Source.Validate() != nil {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportSourceIdentity records identifiers returned by the actual GET.
// Neither an ETag nor a version ID is a content digest.
type HostingDatabaseImportSourceIdentity struct {
	VersionID string `json:"version_id,omitempty"`
	ETag      string `json:"etag,omitempty"`
}

// Validate bounds opaque source identifiers without interpreting their contents.
func (i HostingDatabaseImportSourceIdentity) Validate() error {
	if i.VersionID == "" && i.ETag == "" {
		return ErrInvalidDatabaseImport
	}
	for _, value := range []string{i.VersionID, i.ETag} {
		if len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return ErrInvalidDatabaseImport
		}
	}
	return nil
}

// HostingDatabaseImportBlob describes fully verified decoded SQL, never an owned backup.
type HostingDatabaseImportBlob struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
	Format string `json:"format"`
}

// Validate enforces the decoded-size limit independently of the encoded input.
func (b HostingDatabaseImportBlob) Validate() error {
	if !imageSHA256.MatchString(b.Digest) || b.Bytes < 1 || b.Bytes > HostingDatabaseImportMaxBytes || b.Format != "mysql_sql" {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportStage is the nonsecret inspection state exposed to authorized viewers.
// Paths, credential references and daemon authority are kept in the protected plan/receipt.
type HostingDatabaseImportStage struct {
	ID                          string                               `json:"id"`
	Version                     string                               `json:"version"`
	TargetResourceID            string                               `json:"target_resource_id"`
	Component                   string                               `json:"component"`
	ExpectedDeployedOperationID string                               `json:"expected_deployed_operation_id"`
	ExpectedResourceGeneration  int64                                `json:"expected_resource_generation"`
	S3Source                    HostingDatabaseImportS3Source        `json:"s3_source"`
	State                       string                               `json:"state"`
	FailureCode                 string                               `json:"failure_code,omitempty"`
	SourceIdentity              *HostingDatabaseImportSourceIdentity `json:"source_identity,omitempty"`
	SourceBytes                 int64                                `json:"source_bytes,omitempty"`
	Compression                 string                               `json:"compression,omitempty"`
	StagedBlob                  *HostingDatabaseImportBlob           `json:"staged_blob,omitempty"`
	StageHash                   string                               `json:"stage_hash,omitempty"`
	ExpiresAt                   *time.Time                           `json:"expires_at,omitempty"`
	CreatedAt                   time.Time                            `json:"created_at"`
	UpdatedAt                   time.Time                            `json:"updated_at"`
}

// Validate checks stored state without applying live expiry, so historical replay stays readable.
func (s HostingDatabaseImportStage) Validate() error {
	if !databaseImportFileID.MatchString(s.ID) || s.Version != HostingDatabaseImportStageVersion || !validRuntimeReferenceID(s.TargetResourceID) || s.Component != "mysql" || !validRuntimeReferenceID(s.ExpectedDeployedOperationID) || s.ExpectedResourceGeneration < 1 || s.S3Source.Validate() != nil || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || s.SourceIdentity != nil && s.SourceIdentity.Validate() != nil {
		return ErrInvalidDatabaseImport
	}
	switch s.State {
	case "queued", "running":
		if s.FailureCode != "" || s.SourceBytes != 0 || s.Compression != "" || s.StagedBlob != nil || s.StageHash != "" || s.ExpiresAt != nil || s.SourceIdentity != nil {
			return ErrInvalidDatabaseImport
		}
	case "failed":
		if !ValidDatabaseImportFailure(s.FailureCode) || s.StageHash != "" || s.StagedBlob != nil || s.ExpiresAt != nil {
			return ErrInvalidDatabaseImport
		}
	case "ready", "expired", "consumed":
		if s.FailureCode != "" || s.SourceBytes < 1 || s.SourceBytes > HostingDatabaseImportMaxBytes || s.Compression != "plain" && s.Compression != "gzip" || s.StagedBlob == nil || s.StagedBlob.Validate() != nil || !imageSHA256.MatchString(s.StageHash) || s.ExpiresAt == nil || !s.ExpiresAt.After(s.CreatedAt) {
			return ErrInvalidDatabaseImport
		}
	default:
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportInput consumes a single ready receipt under reviewed target authority.
type HostingDatabaseImportInput struct {
	RequestID         string                               `json:"request_id"`
	StageID           string                               `json:"stage_id"`
	ExpectedStageHash string                               `json:"expected_stage_hash"`
	ComponentTarget   HostingDatabaseBackupComponentTarget `json:"component_target"`
}

// Validate retains explicit replacement consent and restricts this protocol to MySQL.
func (in HostingDatabaseImportInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || !databaseImportFileID.MatchString(in.StageID) || !imageSHA256.MatchString(in.ExpectedStageHash) || in.ComponentTarget.Validate() != nil || in.ComponentTarget.Component != "mysql" {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportOperation describes one confirmed import, never a retryable SQL script.
type HostingDatabaseImportOperation struct {
	ID                        string    `json:"id"`
	Version                   string    `json:"version"`
	StageID                   string    `json:"stage_id"`
	TargetResourceID          string    `json:"target_resource_id"`
	TargetComponent           string    `json:"target_component"`
	TargetDeployedOperationID string    `json:"target_deployed_operation_id"`
	State                     string    `json:"state"`
	OwnershipHeld             bool      `json:"ownership_held"`
	WritersResumed            bool      `json:"writers_resumed"`
	FailureCode               string    `json:"failure_code,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// Validate checks terminal ownership without permitting an uncertain import to resume writers.
func (o HostingDatabaseImportOperation) Validate() error {
	if !validRuntimeReferenceID(o.ID) || o.Version != HostingDatabaseImportVersion || !databaseImportFileID.MatchString(o.StageID) || !validRuntimeReferenceID(o.TargetResourceID) || o.TargetComponent != "mysql" || !validRuntimeReferenceID(o.TargetDeployedOperationID) || o.CreatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		return ErrInvalidDatabaseImport
	}
	switch o.State {
	case "queued", "running":
		if !o.OwnershipHeld || o.WritersResumed || o.FailureCode != "" {
			return ErrInvalidDatabaseImport
		}
	case "succeeded":
		if o.OwnershipHeld || !o.WritersResumed || o.FailureCode != "" {
			return ErrInvalidDatabaseImport
		}
	case "failed":
		if !ValidDatabaseImportFailure(o.FailureCode) || o.OwnershipHeld && o.WritersResumed {
			return ErrInvalidDatabaseImport
		}
	case "uncertain":
		if !o.OwnershipHeld || o.WritersResumed || !ValidDatabaseImportFailure(o.FailureCode) {
			return ErrInvalidDatabaseImport
		}
	default:
		return ErrInvalidDatabaseImport
	}
	return nil
}

// ValidDatabaseImportFailure is the closed, nonsecret error vocabulary for external import.
func ValidDatabaseImportFailure(code string) bool {
	switch code {
	case "access_denied", "authority_denied", "network_denied", "remote_unavailable", "object_mismatch", "unsupported_format", "too_large", "stage_unavailable", "stage_expired", "execution_failed", "interrupted":
		return true
	default:
		return false
	}
}

// HostingDatabaseImportStagePlan pins inspection to current target and S3 credential references.
// It is runner-only and never acquires backup creation or root SQL authority.
type HostingDatabaseImportStagePlan struct {
	Stage          HostingDatabaseImportStage          `json:"stage"`
	ActorID        string                              `json:"actor_id"`
	ProjectID      string                              `json:"project_id"`
	RunnerID       string                              `json:"runner_id"`
	Target         HostingHealthy                      `json:"target"`
	TargetPlan     HostingApplyPlan                    `json:"target_plan"`
	S3Target       HostingDatabaseBackupTargetSnapshot `json:"s3_target"`
	MaxBytes       int64                               `json:"max_bytes"`
	TimeoutSeconds int                                 `json:"timeout_seconds"`
	Hash           string                              `json:"hash"`
}

// Validate requires a real qualified current MySQL target and exact immutable source selection.
func (p HostingDatabaseImportStagePlan) Validate() error {
	hash := p.Hash
	p.Hash = ""
	if hash != HostingDocumentHash(p) || p.Stage.Validate() != nil || p.Stage.State != "queued" || !validRuntimeReferenceID(p.ActorID) || !validRuntimeReferenceID(p.ProjectID) || !validRuntimeReferenceID(p.RunnerID) || p.RunnerID != p.Target.Snapshot.RunnerID || p.Target.Snapshot.ResourceID != p.Stage.TargetResourceID || p.Target.Snapshot.ResourceGeneration != p.Stage.ExpectedResourceGeneration || p.Target.OperationID != p.Stage.ExpectedDeployedOperationID || p.Target.PlanHash != p.TargetPlan.Hash || p.S3Target.Validate() != nil || !p.S3Target.Target.Enabled || p.S3Target.Target.ProjectID != p.ProjectID || p.S3Target.Target.ID != p.Stage.S3Source.TargetID || p.S3Target.Target.Revision != p.Stage.S3Source.Revision || p.Target.Release.DaemonID == "" || p.MaxBytes != HostingDatabaseImportMaxBytes || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 {
		return ErrInvalidDatabaseImport
	}
	if _, err := HostingMySQLComponent(p.Target, p.TargetPlan, p.Stage.Component); err != nil {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportStageFile identifies only the newly created private stage inode.
// It must never refer to adopted or shared logical-backup CAS files.
type HostingDatabaseImportStageFile struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Bytes  int64  `json:"bytes"`
}

// HostingDatabaseImportReadyReceipt persists exact staging evidence across process restarts.
// Its hash binds all observed source facts, target authority, owned file identity and expiry.
type HostingDatabaseImportReadyReceipt struct {
	StageID        string                               `json:"stage_id"`
	PlanHash       string                               `json:"plan_hash"`
	SourceIdentity *HostingDatabaseImportSourceIdentity `json:"source_identity,omitempty"`
	SourceBytes    int64                                `json:"source_bytes"`
	Compression    string                               `json:"compression"`
	Blob           HostingDatabaseImportBlob            `json:"blob"`
	File           HostingDatabaseImportStageFile       `json:"file"`
	ReadyAt        time.Time                            `json:"ready_at"`
	ExpiresAt      time.Time                            `json:"expires_at"`
	Hash           string                               `json:"hash"`
}

// ValidateFor checks the exact immutable plan, without treating elapsed expiry as invalid history.
func (r HostingDatabaseImportReadyReceipt) ValidateFor(p HostingDatabaseImportStagePlan) error {
	hash := r.Hash
	r.Hash = ""
	if p.Validate() != nil || hash != HostingDocumentHash(r) || r.StageID != p.Stage.ID || r.PlanHash != p.Hash || r.SourceIdentity != nil && r.SourceIdentity.Validate() != nil || r.SourceBytes < 1 || r.SourceBytes > p.MaxBytes || r.Compression != "plain" && r.Compression != "gzip" || r.Blob.Validate() != nil || r.File.Inode == 0 || r.File.Device == 0 || r.File.Bytes != r.Blob.Bytes || r.ReadyAt.Before(p.Stage.CreatedAt) || !r.ExpiresAt.Equal(r.ReadyAt.Add(HostingDatabaseImportStageTTL)) {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// StageView returns the public ready facts without private paths, credentials or inode proof.
func (r HostingDatabaseImportReadyReceipt) StageView(p HostingDatabaseImportStagePlan) (HostingDatabaseImportStage, error) {
	if r.ValidateFor(p) != nil {
		return HostingDatabaseImportStage{}, ErrInvalidDatabaseImport
	}
	s := p.Stage
	s.State, s.StageHash, s.UpdatedAt = "ready", r.Hash, r.ReadyAt
	s.SourceBytes, s.Compression, s.StagedBlob = r.SourceBytes, r.Compression, &r.Blob
	s.SourceIdentity, s.ExpiresAt = r.SourceIdentity, &r.ExpiresAt
	return s, nil
}

// HostingDatabaseImportPlan authorizes a single consumption, not another remote download.
type HostingDatabaseImportPlan struct {
	Operation HostingDatabaseImportOperation    `json:"operation"`
	StagePlan HostingDatabaseImportStagePlan    `json:"stage_plan"`
	Receipt   HostingDatabaseImportReadyReceipt `json:"receipt"`
	Hash      string                            `json:"hash"`
}

// Validate binds the import to the inspected MySQL deployment, daemon and stage inode.
func (p HostingDatabaseImportPlan) Validate() error {
	hash := p.Hash
	p.Hash = ""
	t := p.StagePlan.Target
	if hash != HostingDocumentHash(p) || p.Operation.Validate() != nil || p.Operation.State != "queued" || p.Receipt.ValidateFor(p.StagePlan) != nil || p.Operation.StageID != p.StagePlan.Stage.ID || p.Operation.TargetResourceID != t.Snapshot.ResourceID || p.Operation.TargetDeployedOperationID != t.OperationID || p.Operation.TargetComponent != p.StagePlan.Stage.Component || p.Operation.CreatedAt.After(p.Receipt.ExpiresAt) {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// ValidateDatabaseImportTask prevents generic process/workflow fallback for either typed phase.
func ValidateDatabaseImportTask(run TaskRun) error {
	s := run.RunSpec
	prefix, mode := "db_import_stage_", "stage"
	if s.Type == RunTypeDatabaseImport {
		prefix, mode = "db_import_", "import"
	} else if s.Type != RunTypeDatabaseImportStage {
		return ErrInvalidDatabaseImport
	}
	if !validRuntimeReferenceID(s.HostingOperationID) || run.ID != prefix+s.HostingOperationID || s.HostingMode != mode || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(run.Workflow.Steps) != 0 {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportMutation is a fenced, idempotent inspection or SQL receipt.
// Ready contains only immutable non-secret file ownership and content evidence.
type HostingDatabaseImportMutation struct {
	HostingAuthority
	ReceiptID      string                             `json:"receipt_id"`
	PlanHash       string                             `json:"plan_hash"`
	Phase          string                             `json:"phase"`
	DaemonID       string                             `json:"daemon_id"`
	FailureCode    string                             `json:"failure_code,omitempty"`
	Ready          *HostingDatabaseImportReadyReceipt `json:"ready,omitempty"`
	Writers        []HostingStorageArchiveWriterState `json:"writers,omitempty"`
	WritersResumed bool                               `json:"writers_resumed"`
}

// Validate checks the closed receipt vocabulary without trusting a runner path.
func (m HostingDatabaseImportMutation) Validate() error {
	stage := m.RunID == "db_import_stage_"+m.OperationID
	if !stage && m.RunID != "db_import_"+m.OperationID || !validRuntimeReferenceID(m.OperationID) || !validRuntimeReferenceID(m.LeaseID) || m.Attempt < 1 || m.Attempt > 2147483647 || len(m.Fence) < 1 || len(m.Fence) > 256 || !validRuntimeReferenceID(m.ReceiptID) || !imageSHA256.MatchString(m.PlanHash) || len(m.DaemonID) < 1 || len(m.DaemonID) > 256 {
		return ErrInvalidDatabaseImport
	}
	if stage {
		if m.Phase != "stage_intent" && m.Phase != "ready" && m.Phase != "stage_failed" || len(m.Writers) != 0 || m.WritersResumed || (m.Phase == "ready") != (m.Ready != nil) {
			return ErrInvalidDatabaseImport
		}
	} else if m.Phase != "import_intent" && m.Phase != "complete" && m.Phase != "failed" && m.Phase != "uncertain" || m.Ready != nil {
		return ErrInvalidDatabaseImport
	}
	failed := m.Phase == "stage_failed" || m.Phase == "failed" || m.Phase == "uncertain"
	if failed != (m.FailureCode != "") || failed && !ValidDatabaseImportFailure(m.FailureCode) || m.Phase == "complete" && !m.WritersResumed || m.Phase == "uncertain" && m.WritersResumed {
		return ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportReceiptResult returns the saved outcome for exactly one receipt family.
type HostingDatabaseImportReceiptResult struct {
	Stage  *HostingDatabaseImportStage     `json:"stage,omitempty"`
	Import *HostingDatabaseImportOperation `json:"import,omitempty"`
}
