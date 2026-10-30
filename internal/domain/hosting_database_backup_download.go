package domain

import (
	"errors"
	"math"
	"time"
)

// HostingDatabaseBackupDownloadCapability advertises explicitly configured local archive streaming.
const HostingDatabaseBackupDownloadCapability = "hosting_database_backup_download_v1"

// HostingDatabaseBackupDownloadVersion identifies the exact local-read plan protocol.
const HostingDatabaseBackupDownloadVersion = "database.logical-local-download.v1"

// HostingDatabaseBackupDownloadMode is a local read without S3 or restore authority.
const HostingDatabaseBackupDownloadMode = "browser_download"

// ErrInvalidDatabaseBackupDownload reports invalid download identity or framing.
var ErrInvalidDatabaseBackupDownload = errors.New("invalid database backup download")

// HostingDatabaseBackupDownloadOptions reports current authorized download eligibility.
type HostingDatabaseBackupDownloadOptions struct {
	Available  bool                       `json:"available"`
	ReasonCode string                     `json:"reason_code"`
	BackupID   string                     `json:"backup_id"`
	Blob       *HostingDatabaseBackupBlob `json:"blob,omitempty"`
}

// HostingDatabaseBackupDownloadPlan identifies one ephemeral read of an exact local copy.
// It contains no filesystem paths, callback URLs, credentials, or archive bytes.
type HostingDatabaseBackupDownloadPlan struct {
	Version       string                    `json:"version"`
	BackupVersion string                    `json:"backup_version"`
	ServerID      string                    `json:"server_id"`
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
	Blob          HostingDatabaseBackupBlob `json:"blob"`
	LocalCopy     BackupLocalCopyProof      `json:"local_copy"`
	ExpiresAt     time.Time                 `json:"expires_at"`
	Hash          string                    `json:"hash"`
}

// Validate verifies immutable scope, copy identity, and the plan hash.
func (p HostingDatabaseBackupDownloadPlan) Validate() error {
	h := p.Hash
	p.Hash = ""
	for _, id := range []string{p.SessionID, p.ActorID, p.ProjectID, p.ResourceID, p.BindingID, p.BackupID, p.CopyID, p.RunnerID, p.DestinationID, p.ServerID} {
		if !validRuntimeReferenceID(id) {
			return ErrInvalidDatabaseBackupDownload
		}
	}
	if h != HostingDocumentHash(p) || p.Blob.Validate() != nil || p.LocalCopy.Validate() != nil || p.Version != HostingDatabaseBackupDownloadVersion || HostingDatabaseBackupFormatFor(p.BackupVersion) != p.Blob.Format || p.DaemonID == "" || len(p.DaemonID) > 256 || p.LocalCopy.Bytes != p.Blob.Bytes || p.ExpiresAt.IsZero() {
		return ErrInvalidDatabaseBackupDownload
	}
	return nil
}

// HostingDatabaseBackupDownloadStream binds the multipart stream to live lease authority.
type HostingDatabaseBackupDownloadStream struct {
	HostingAuthority
	PlanHash    string `json:"plan_hash"`
	FailureCode string `json:"failure_code,omitempty"`
}

// Validate checks the complete fenced stream authority and closed failure code.
func (in HostingDatabaseBackupDownloadStream) Validate() error {
	if in.FailureCode != "" && in.FailureCode != "archive_unavailable" {
		return ErrInvalidDatabaseBackupDownload
	}
	a := in.HostingAuthority
	if !validRuntimeReferenceID(a.OperationID) || a.RunID != "db_transfer_"+a.OperationID || !validRuntimeReferenceID(a.LeaseID) || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(a.Fence) < 1 || len(a.Fence) > 256 || !imageSHA256.MatchString(in.PlanHash) {
		return ErrInvalidDatabaseBackupDownload
	}
	return nil
}
