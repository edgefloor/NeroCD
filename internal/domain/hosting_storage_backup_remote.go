package domain

import (
	"errors"
	"math"
	"strings"
	"time"
)

// HostingStorageBackupTransferVersion and related constants identify remote storage backup documents and capabilities.
const (
	HostingStorageBackupTransferVersion         = "owned-upload.s3-transfer.v1"
	HostingStorageBackupRemoteCapability        = "hosting_storage_remote_backup_v1"
	RunTypeStorageBackupTransfer                = "storage_backup_transfer"
	HostingStorageBackupRemoteCredentialPurpose = "hosting_storage_remote_backup_credentials.v1"
)

// ErrInvalidStorageBackupRemote reports invalid storage backup remote input.
var ErrInvalidStorageBackupRemote = errors.New("invalid storage remote backup request")

// HostingStorageBackupTransferRetryInput describes the requested storage backup transfer retry.
type HostingStorageBackupTransferRetryInput = HostingDatabaseBackupTransferRetryInput

// HostingStorageBackupTransfer records a storage backup transfer operation and its state.
type HostingStorageBackupTransfer struct {
	ID                 string                     `json:"id"`
	Version            string                     `json:"version"`
	ProjectID          string                     `json:"project_id"`
	TargetID           string                     `json:"target_id"`
	TargetRevision     int64                      `json:"target_revision"`
	DestinationID      string                     `json:"destination_id"`
	Mode               string                     `json:"mode"`
	BackupID           string                     `json:"backup_id,omitempty"`
	RestoreOperationID string                     `json:"restore_operation_id,omitempty"`
	State              string                     `json:"state"`
	FailureCode        string                     `json:"failure_code"`
	Blob               *HostingStorageArchiveBlob `json:"blob,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

// Validate checks the storage backup transfer fields and invariants.
func (t HostingStorageBackupTransfer) Validate() error {
	if !validRuntimeReferenceID(t.ID) || t.Version != HostingStorageBackupTransferVersion || !validRuntimeReferenceID(t.ProjectID) || !validRuntimeReferenceID(t.TargetID) || t.TargetRevision < 1 || !validRuntimeReferenceID(t.DestinationID) || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || !ValidBackupRemoteFailure(t.FailureCode) {
		return ErrInvalidStorageBackupRemote
	}
	switch t.State {
	case "queued", "dispatched", "applying", "succeeded", "failed", "uncertain":
	default:
		return ErrInvalidStorageBackupRemote
	}
	switch t.Mode {
	case "validate":
		if t.BackupID != "" || t.RestoreOperationID != "" || t.Blob != nil {
			return ErrInvalidStorageBackupRemote
		}
	case "upload", "download":
		if !validRuntimeReferenceID(t.BackupID) || t.Blob == nil || t.Blob.Validate() != nil {
			return ErrInvalidStorageBackupRemote
		}
		if t.Mode == "download" && !validRuntimeReferenceID(t.RestoreOperationID) || t.Mode == "upload" && t.RestoreOperationID != "" {
			return ErrInvalidStorageBackupRemote
		}
	default:
		return ErrInvalidStorageBackupRemote
	}
	return nil
}

// HostingStorageBackupTransferPlan contains opaque key references and immutable object identity,
// never plaintext credentials, local paths or executable commands.
type HostingStorageBackupTransferPlan struct {
	Limits         HostingStorageArchiveLimits         `json:"limits"`
	Operation      HostingStorageBackupTransfer        `json:"operation"`
	Target         HostingDatabaseBackupTargetSnapshot `json:"target"`
	ActorID        string                              `json:"actor_id"`
	RunnerID       string                              `json:"runner_id"`
	ServerID       string                              `json:"server_id"`
	DaemonID       string                              `json:"daemon_id"`
	LocationID     string                              `json:"location_id"`
	ObjectKey      string                              `json:"object_key"`
	MaxBytes       int64                               `json:"max_bytes"`
	TimeoutSeconds int                                 `json:"timeout_seconds"`
	Hash           string                              `json:"hash"`
}

// StorageRemoteObjectKey returns the immutable remote object key for a storage backup blob.
func StorageRemoteObjectKey(backup string, blob HostingStorageArchiveBlob) string {
	return "nerocd/storage/" + backup + "/" + strings.TrimPrefix(blob.SHA256, "sha256:") + ".tar"
}

// Validate checks the storage backup transfer plan fields and invariants.
func (p HostingStorageBackupTransferPlan) Validate() error {
	h := p.Hash
	p.Hash = ""
	if h != HostingDocumentHash(p) || p.Operation.Validate() != nil || p.Target.Validate() != nil || p.Operation.ProjectID != p.Target.Target.ProjectID || p.Operation.TargetID != p.Target.Target.ID || p.Operation.TargetRevision != p.Target.Target.Revision || !validRuntimeReferenceID(p.ActorID) || !validRuntimeReferenceID(p.RunnerID) || !validRuntimeReferenceID(p.ServerID) || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || p.MaxBytes < 5 || p.MaxBytes > HostingStorageArchiveMaxBytes {
		return ErrInvalidStorageBackupRemote
	}
	if p.Operation.Mode == "validate" {
		if p.ObjectKey != "" || p.LocationID != "" || p.DaemonID != "" {
			return ErrInvalidStorageBackupRemote
		}
	} else if p.Limits.Validate() != nil || p.Operation.Blob.BackupID != p.Operation.BackupID || p.Operation.Blob.DestinationID != p.Operation.DestinationID || p.Operation.Blob.DaemonID != p.DaemonID || p.LocationID != p.Operation.BackupID || p.Operation.Blob.SizeBytes > p.MaxBytes || p.ObjectKey != StorageRemoteObjectKey(p.Operation.BackupID, *p.Operation.Blob) || p.DaemonID == "" {
		return ErrInvalidStorageBackupRemote
	}
	return nil
}

// HostingStorageBackupTransferMutation reports a fenced storage backup transfer execution
// outcome.
type HostingStorageBackupTransferMutation struct {
	LocalCopy  *BackupLocalCopyProof  `json:"local_copy,omitempty"`
	RemoteCopy *BackupRemoteCopyProof `json:"remote_copy,omitempty"`
	HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
	DaemonID    string `json:"daemon_id"`
}

// Validate checks the storage backup transfer mutation fields and invariants.
func (in HostingStorageBackupTransferMutation) Validate() error {
	if in.LocalCopy != nil && (in.LocalCopy.Validate() != nil || in.Phase != "complete" || in.RemoteCopy != nil) || in.RemoteCopy != nil && (in.RemoteCopy.Validate() != nil || in.Phase != "complete") {
		return ErrInvalidStorageBackupRemote
	}
	a := in.HostingAuthority
	if !validRuntimeReferenceID(a.OperationID) || a.RunID != "storage_transfer_"+a.OperationID || !validRuntimeReferenceID(a.LeaseID) || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(a.Fence) < 1 || len(a.Fence) > 256 || !validRuntimeReferenceID(in.ReceiptID) || !imageSHA256.MatchString(in.PlanHash) || !ValidBackupRemoteFailure(in.FailureCode) || len(in.DaemonID) < 1 || len(in.DaemonID) > 256 {
		return ErrInvalidStorageBackupRemote
	}
	switch in.Phase {
	case "intent", "complete", "failed", "uncertain":
	default:
		return ErrInvalidStorageBackupRemote
	}
	return nil
}

// ValidateStorageBackupTransferTask checks the storage backup transfer task contract.
func ValidateStorageBackupTransferTask(r TaskRun) error {
	s := r.RunSpec
	if s.Type != RunTypeStorageBackupTransfer || !validRuntimeReferenceID(s.HostingOperationID) || r.ID != "storage_transfer_"+s.HostingOperationID || (s.HostingMode != "validate" && s.HostingMode != "upload" && s.HostingMode != "download") || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(r.Workflow.Steps) != 0 {
		return ErrInvalidStorageBackupRemote
	}
	return nil
}

// ValidateStorageTransferCopyProof binds additive deletion ownership to the
// immutable transfer mode and blob. Legacy nil proofs remain restorable.
func ValidateStorageTransferCopyProof(p HostingStorageBackupTransferPlan, in HostingStorageBackupTransferMutation) error {
	if in.RemoteCopy != nil && (p.Operation.Mode != "upload" || in.Phase != "complete") || in.LocalCopy != nil && (p.Operation.Mode != "download" || in.Phase != "complete" || p.Operation.Blob == nil || in.LocalCopy.Bytes != p.Operation.Blob.SizeBytes) {
		return ErrInvalidStorageBackupRemote
	}
	return nil
}
