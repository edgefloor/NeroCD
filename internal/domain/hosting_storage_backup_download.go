package domain

import (
	"errors"
	"math"
	"time"
)

const HostingStorageBackupDownloadCapability = "hosting_storage_backup_download_v1"
const HostingStorageBackupDownloadMode = "browser_download"

var ErrInvalidStorageBackupDownload = errors.New("invalid storage archive download")

// HostingStorageBackupDownloadOptions reports current authorized download eligibility.
type HostingStorageBackupDownloadOptions struct {
	Available  bool                       `json:"available"`
	ReasonCode string                     `json:"reason_code"`
	BackupID   string                     `json:"backup_id"`
	Blob       *HostingStorageArchiveBlob `json:"blob,omitempty"`
}

// HostingStorageBackupDownloadPlan identifies one ephemeral read of an exact local copy.
// It contains no filesystem paths, callback URLs, credentials, or archive bytes.
type HostingStorageBackupDownloadPlan struct {
	SessionID     string                    `json:"session_id"`
	ActorID       string                    `json:"actor_id"`
	ProjectID     string                    `json:"project_id"`
	ResourceID    string                    `json:"resource_id"`
	BindingID     string                    `json:"binding_id"`
	BackupID      string                    `json:"backup_id"`
	CopyID        string                    `json:"copy_id"`
	RunnerID      string                    `json:"runner_id"`
	DestinationID string                    `json:"destination_id"`
	DaemonID      string                    `json:"daemon_id"`
	Blob          HostingStorageArchiveBlob `json:"blob"`
	LocalCopy     BackupLocalCopyProof      `json:"local_copy"`
	ExpiresAt     time.Time                 `json:"expires_at"`
	Hash          string                    `json:"hash"`
}

func (p HostingStorageBackupDownloadPlan) Validate() error {
	h := p.Hash
	p.Hash = ""
	for _, id := range []string{p.SessionID, p.ActorID, p.ProjectID, p.ResourceID, p.BindingID, p.BackupID, p.CopyID, p.RunnerID, p.DestinationID} {
		if !validRuntimeReferenceID(id) {
			return ErrInvalidStorageBackupDownload
		}
	}
	if h != HostingDocumentHash(p) || p.Blob.Validate() != nil || p.LocalCopy.Validate() != nil || p.Blob.BackupID != p.BackupID || p.Blob.DestinationID != p.DestinationID || p.Blob.DaemonID != p.DaemonID || p.LocalCopy.Bytes != p.Blob.SizeBytes || p.ExpiresAt.IsZero() {
		return ErrInvalidStorageBackupDownload
	}
	return nil
}

// HostingStorageBackupDownloadStream binds the multipart stream to live lease authority.
type HostingStorageBackupDownloadStream struct {
	HostingAuthority
	PlanHash string `json:"plan_hash"`
}

func (in HostingStorageBackupDownloadStream) Validate() error {
	a := in.HostingAuthority
	if !validRuntimeReferenceID(a.OperationID) || a.RunID != "storage_transfer_"+a.OperationID || !validRuntimeReferenceID(a.LeaseID) || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(a.Fence) < 1 || len(a.Fence) > 256 || !imageSHA256.MatchString(in.PlanHash) {
		return ErrInvalidStorageBackupDownload
	}
	return nil
}
