package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RunTypeDatabaseBackup                 = "database_backup"
	HostingDatabaseBackupCapability       = "hosting_postgresql_backup_v1"
	HostingDatabaseBackupVersion          = "postgresql.logical-local.v1"
	HostingDatabaseBackupMaxBytes   int64 = 1 << 30
)

var ErrInvalidDatabaseBackup = errors.New("invalid hosted PostgreSQL backup request")

// HostingDatabaseBackupInput identifies one immediate logical snapshot request.
type HostingDatabaseBackupInput struct {
	RequestID                  string                 `json:"request_id"`
	ExpectedResourceGeneration int64                  `json:"expected_resource_generation"`
	RemoteTarget               *BackupTargetSelection `json:"remote_target,omitempty"`
}

func (in HostingDatabaseBackupInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.RemoteTarget != nil && in.RemoteTarget.Validate() != nil {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupRestoreInput creates an independent private database.
type HostingDatabaseBackupRestoreInput struct {
	RequestID    string              `json:"request_id"`
	Name         string              `json:"name"`
	RemoteSource *BackupRemoteSource `json:"remote_source,omitempty"`
}

func (in HostingDatabaseBackupRestoreInput) Validate() error {
	for _, r := range in.Name {
		if unicode.IsControl(r) {
			return ErrInvalidDatabaseBackup
		}
	}
	if !validRuntimeReferenceID(in.RequestID) || len(in.Name) < 1 || len(in.Name) > 128 || strings.TrimSpace(in.Name) != in.Name || !utf8.ValidString(in.Name) || strings.ContainsAny(in.Name, "\x00\r\n\t") || in.RemoteSource != nil && in.RemoteSource.Validate() != nil {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupBlob contains only immutable nonsecret archive metadata.
type HostingDatabaseBackupBlob struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
	Format string `json:"format"`
}

func (b HostingDatabaseBackupBlob) Validate() error {
	if !imageSHA256.MatchString(b.Digest) || b.Bytes < 5 || b.Bytes > HostingDatabaseBackupMaxBytes || b.Format != "postgresql_custom" {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupOperation is safe for project viewers. Local locations,
// credential references and runner lease authority are deliberately separate.
type HostingDatabaseBackupOperation struct {
	RemoteTarget     *BackupTargetSelection     `json:"remote_target,omitempty"`
	MaxBytes         int64                      `json:"max_bytes"`
	TimeoutSeconds   int                        `json:"timeout_seconds"`
	ID               string                     `json:"id"`
	Version          string                     `json:"version"`
	ProjectID        string                     `json:"project_id"`
	SourceResourceID string                     `json:"source_resource_id"`
	SourceGeneration int64                      `json:"source_generation"`
	BackupID         string                     `json:"backup_id"`
	Mode             string                     `json:"mode"`
	State            string                     `json:"state"`
	TargetResourceID string                     `json:"target_resource_id,omitempty"`
	Blob             *HostingDatabaseBackupBlob `json:"blob,omitempty"`
	Consistency      string                     `json:"consistency"`
	FailureCode      string                     `json:"failure_code,omitempty"`
	OwnershipHeld    bool                       `json:"ownership_held"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

// HostingDatabaseBackupPlan is delivered only to the authenticated exact runner.
// The location ID is opaque: its path is selected by protected operator config.
type HostingDatabaseBackupPlan struct {
	Operation      HostingDatabaseBackupOperation `json:"operation"`
	ActorID        string                         `json:"actor_id"`
	RunnerID       string                         `json:"runner_id"`
	Source         HostingHealthy                 `json:"source"`
	SourceStorage  HostingStorage                 `json:"source_storage"`
	SourcePlan     HostingApplyPlan               `json:"source_plan"`
	Credentials    []HostingDatabaseCredentialRef `json:"credentials"`
	Target         *HostingHealthy                `json:"target,omitempty"`
	TargetStorage  *HostingStorage                `json:"target_storage,omitempty"`
	TargetPlan     *HostingApplyPlan              `json:"target_plan,omitempty"`
	LocationID     string                         `json:"location_id"`
	MaxBytes       int64                          `json:"max_bytes"`
	TimeoutSeconds int                            `json:"timeout_seconds"`
	Hash           string                         `json:"hash"`
}

func (p HostingDatabaseBackupPlan) Validate() error {
	hash := p.Hash
	p.Hash = ""
	if hash != HostingDocumentHash(p) || p.Operation.Version != HostingDatabaseBackupVersion || !validRuntimeReferenceID(p.Operation.ID) || !validRuntimeReferenceID(p.ActorID) || p.RunnerID != p.Source.Snapshot.RunnerID || p.Operation.SourceResourceID != p.Source.Snapshot.ResourceID || p.SourcePlan.Hash != p.Source.PlanHash || (ValidateHostingDatabasePlan(p.SourcePlan) != nil && ValidateHostingDatabasePlanV2(p.SourcePlan) != nil) || ValidateHostingHealthyStorage(p.Source) != nil || p.Source.Snapshot.Database.Engine != "postgresql" || p.Source.Snapshot.Database.RecipeVersion != HostingDatabasePostgresRecipe || p.SourceStorage.Validate() != nil || p.SourceStorage.Reference() != p.Source.Snapshot.Storage || p.MaxBytes < 5 || p.MaxBytes > HostingDatabaseBackupMaxBytes || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || p.LocationID != p.Operation.BackupID {
		return ErrInvalidDatabaseBackup
	}
	if p.Operation.MaxBytes != p.MaxBytes || p.Operation.TimeoutSeconds != p.TimeoutSeconds || p.Operation.ProjectID != p.SourceStorage.ProjectID || p.Operation.SourceGeneration != p.Source.Snapshot.ResourceGeneration || p.Source.Snapshot != p.SourcePlan.Snapshot || p.Source.Release != p.SourcePlan.Release || p.Source.Storage == nil || p.SourcePlan.Storage == nil || p.Source.Storage.Plan != *p.SourcePlan.Storage || HostingDocumentHash(p.Credentials) != HostingDocumentHash(p.SourcePlan.Database.Credentials) {
		return ErrInvalidDatabaseBackup
	}
	if p.Operation.Mode == "restore" {
		if p.TargetStorage == nil || p.TargetStorage.Validate() != nil || p.Target == nil || p.TargetStorage.Reference() != p.Target.Snapshot.Storage || p.TargetPlan == nil || p.Target.Snapshot.Database.Engine != "postgresql" || p.Target.Snapshot.Database.RecipeVersion != HostingDatabasePostgresRecipe || p.Target.Snapshot != p.TargetPlan.Snapshot || p.Target.Release != p.TargetPlan.Release || p.Target.ContainerID == p.Source.ContainerID || p.TargetStorage.ID == p.SourceStorage.ID || p.TargetStorage.VolumeName == p.SourceStorage.VolumeName || p.Target.Snapshot.BindingID == p.Source.Snapshot.BindingID || p.Operation.TargetResourceID != p.Target.Snapshot.ResourceID || p.Target.Snapshot.DestinationID != p.Source.Snapshot.DestinationID || p.Target.Release.DaemonID != p.Source.Release.DaemonID || p.Target.Snapshot.ResourceID == p.Source.Snapshot.ResourceID || p.TargetPlan.Hash != p.Target.PlanHash || ValidateHostingDatabasePlan(*p.TargetPlan) != nil || ValidateHostingHealthyStorage(*p.Target) != nil || p.Operation.Blob == nil || p.Operation.Blob.Validate() != nil {
			return ErrInvalidDatabaseBackup
		}
	} else if p.Operation.Mode != "backup" || p.Target != nil || p.TargetPlan != nil {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupMutation is append-before-send phase evidence. Intent is
// recorded before effects; replay may report a receipt but never repeat import.
type HostingDatabaseBackupMutation struct {
	LocalCopy *BackupLocalCopyProof `json:"local_copy,omitempty"`
	HostingAuthority
	ReceiptID   string                     `json:"receipt_id"`
	PlanHash    string                     `json:"plan_hash"`
	Phase       string                     `json:"phase"`
	Blob        *HostingDatabaseBackupBlob `json:"blob,omitempty"`
	FailureCode string                     `json:"failure_code,omitempty"`
}

func (in HostingDatabaseBackupMutation) Validate() error {
	if in.LocalCopy != nil && (in.Phase != "complete" || in.LocalCopy.Validate() != nil || in.Blob == nil || in.LocalCopy.Bytes != in.Blob.Bytes) {
		return ErrInvalidDatabaseBackup
	}
	if !validRuntimeReferenceID(in.OperationID) || !validRuntimeReferenceID(in.RunID) || !validRuntimeReferenceID(in.LeaseID) || !validRuntimeReferenceID(in.ReceiptID) || in.Attempt < 1 || in.Attempt > math.MaxInt32 || in.Fence == "" || len(in.Fence) > 256 || !imageSHA256.MatchString(in.PlanHash) {
		return ErrInvalidDatabaseBackup
	}
	switch in.Phase {
	case "intent", "failed", "uncertain":
		if in.Blob != nil {
			return ErrInvalidDatabaseBackup
		}
	case "complete":
		if in.Blob == nil || in.Blob.Validate() != nil {
			return ErrInvalidDatabaseBackup
		}
	default:
		return ErrInvalidDatabaseBackup
	}
	switch in.FailureCode {
	case "", "execution_failed", "publication_failed", "interrupted":
	default:
		return ErrInvalidDatabaseBackup
	}
	return nil
}
func ValidateDatabaseBackupTask(run TaskRun) error {
	s := run.RunSpec
	if s.Type != RunTypeDatabaseBackup || !validRuntimeReferenceID(s.HostingOperationID) || run.ID != "db_backup_"+s.HostingOperationID || (s.HostingMode != "backup" && s.HostingMode != "restore") || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(run.Workflow.Steps) != 0 {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// DecodeHostingDatabaseBackupJSON validates one bounded closed protocol document.
func DecodeHostingDatabaseBackupJSON(raw []byte, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > 1<<20 || len(trimmed) == 0 || trimmed[0] != '{' || !validRuntimeJSONUnicode(raw) || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return ErrInvalidDatabaseBackup
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalidDatabaseBackup
	}
	return nil
}
func (o HostingDatabaseBackupOperation) Validate() error {
	if o.RemoteTarget != nil && o.RemoteTarget.Validate() != nil {
		return ErrInvalidDatabaseBackup
	}
	if !validRuntimeReferenceID(o.ID) || !validRuntimeReferenceID(o.ProjectID) || !validRuntimeReferenceID(o.SourceResourceID) || !validRuntimeReferenceID(o.BackupID) || o.SourceGeneration < 1 || o.MaxBytes < 1 || o.MaxBytes > HostingDatabaseBackupMaxBytes || o.TimeoutSeconds < 1 || o.TimeoutSeconds > 3600 || o.Version != HostingDatabaseBackupVersion || o.Consistency != "logical_snapshot_source_writable" || o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() {
		return ErrInvalidDatabaseBackup
	}
	switch o.State {
	case "queued", "waiting_target", "dispatched", "applying", "succeeded", "failed", "uncertain":
	default:
		return ErrInvalidDatabaseBackup
	}
	if o.Mode == "backup" {
		if o.BackupID != o.ID || o.TargetResourceID != "" {
			return ErrInvalidDatabaseBackup
		}
	} else if o.Mode == "restore" {
		if o.BackupID == o.ID || !validRuntimeReferenceID(o.TargetResourceID) || o.TargetResourceID == o.SourceResourceID {
			return ErrInvalidDatabaseBackup
		}
	} else {
		return ErrInvalidDatabaseBackup
	}
	if o.Blob != nil && o.Blob.Validate() != nil || o.State == "succeeded" && (o.Blob == nil || o.OwnershipHeld) || o.State == "uncertain" && !o.OwnershipHeld {
		return ErrInvalidDatabaseBackup
	}
	return nil
}

// HostingDatabaseBackupLimits is resolved only from protected operator config.
type HostingDatabaseBackupLimits struct {
	MaxBytes       int64
	TimeoutSeconds int
}

func (l HostingDatabaseBackupLimits) Validate() error {
	if l.MaxBytes < 5 || l.MaxBytes > HostingDatabaseBackupMaxBytes || l.TimeoutSeconds < 1 || l.TimeoutSeconds > 3600 {
		return ErrInvalidDatabaseBackup
	}
	return nil
}
func DefaultHostingDatabaseBackupLimits() HostingDatabaseBackupLimits {
	return HostingDatabaseBackupLimits{MaxBytes: HostingDatabaseBackupMaxBytes, TimeoutSeconds: 900}
}
