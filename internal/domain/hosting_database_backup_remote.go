package domain

import (
	"errors"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// HostingDatabaseBackupTargetVersion and related constants identify remote database backup documents and capabilities.
const (
	HostingDatabaseBackupTargetVersion                 = "postgresql.backup-target.v1"
	HostingDatabaseBackupTransferVersion               = "postgresql.logical-s3-transfer.v1"
	HostingDatabaseBackupRemoteCapability              = "hosting_postgresql_remote_backup_v1"
	RunTypeDatabaseBackupTransfer                      = "database_backup_transfer"
	HostingDatabaseBackupRemoteCredentialPurpose       = "hosting_postgresql_remote_backup_credentials.v1"
	HostingDatabaseMySQLBackupTransferVersion          = "mysql.logical-s3-transfer.v1"
	HostingDatabaseMySQLBackupRemoteCapability         = "hosting_mysql_remote_backup_v1"
	HostingDatabaseMySQLBackupRemoteCredentialPurpose  = "hosting_mysql_remote_backup_credentials.v1"
	HostingDatabaseMySQLRemoteRestoreCapability        = "hosting_mysql_remote_restore_v1"
	HostingDatabaseMySQLRemoteRestoreCredentialPurpose = "hosting_mysql_remote_restore_credentials.v1"
)

// ErrInvalidDatabaseBackupRemote reports invalid database backup remote input.
var ErrInvalidDatabaseBackupRemote = errors.New("invalid database remote backup request")

// HostingDatabaseBackupTransferVersionFor binds uploads to their local backup format.
func HostingDatabaseBackupTransferVersionFor(backupVersion string) string {
	switch backupVersion {
	case HostingDatabaseBackupVersion, HostingDatabaseBackupComponentVersion:
		return HostingDatabaseBackupTransferVersion
	case HostingDatabaseMySQLBackupVersion, HostingDatabaseMySQLBackupComponentVersion:
		return HostingDatabaseMySQLBackupTransferVersion
	default:
		return ""
	}
}

// HostingDatabaseBackupRemoteCapabilityFor selects the persisted engine and mode adapter.
func HostingDatabaseBackupRemoteCapabilityFor(version, mode string) string {
	switch version {
	case HostingDatabaseBackupTransferVersion:
		return HostingDatabaseBackupRemoteCapability
	case HostingDatabaseMySQLBackupTransferVersion:
		if mode == "download" {
			return HostingDatabaseMySQLRemoteRestoreCapability
		}
		if mode == "upload" {
			return HostingDatabaseMySQLBackupRemoteCapability
		}
	}
	return ""
}

// HostingDatabaseBackupRemoteCredentialPurposeFor separates persisted engine and mode purposes.
func HostingDatabaseBackupRemoteCredentialPurposeFor(version, mode string) string {
	switch version {
	case HostingDatabaseBackupTransferVersion:
		return HostingDatabaseBackupRemoteCredentialPurpose
	case HostingDatabaseMySQLBackupTransferVersion:
		if mode == "download" {
			return HostingDatabaseMySQLRemoteRestoreCredentialPurpose
		}
		if mode == "upload" {
			return HostingDatabaseMySQLBackupRemoteCredentialPurpose
		}
	}
	return ""
}

var remoteBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var remoteRegionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)

// BackupTargetSelection pins configuration rather than following future edits.
type BackupTargetSelection struct {
	TargetID string `json:"target_id"`
	Revision int64  `json:"revision"`
}

// Validate checks the backup target selection fields and invariants.
func (r BackupTargetSelection) Validate() error {
	if !validRuntimeReferenceID(r.TargetID) || r.Revision < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// BackupRemoteSource selects an uploaded backup and its immutable target revision.
type BackupRemoteSource struct {
	TransferID             string `json:"transfer_id"`
	ExpectedTargetRevision int64  `json:"expected_target_revision"`
}

// Validate checks the backup remote source fields and invariants.
func (r BackupRemoteSource) Validate() error {
	if !validRuntimeReferenceID(r.TransferID) || r.ExpectedTargetRevision < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTargetInput describes the requested database backup target.
type HostingDatabaseBackupTargetInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Name             string `json:"name"`
	Endpoint         string `json:"endpoint"`
	Bucket           string `json:"bucket"`
	Region           string `json:"region"`
	Enabled          bool   `json:"enabled"`
}

// ValidateBackupRemoteEndpoint checks the backup remote endpoint.
func ValidateBackupRemoteEndpoint(endpoint string) error {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.TrimSpace(endpoint) != endpoint || len(endpoint) > 512 || strings.HasSuffix(u.Hostname(), ".") {
		return ErrInvalidDatabaseBackupRemote
	}
	if p := u.Port(); p != "" {
		if _, e := net.LookupPort("tcp", p); e != nil {
			return ErrInvalidDatabaseBackupRemote
		}
	}
	return nil
}

// Validate checks the database backup target input fields and invariants.
func (in HostingDatabaseBackupTargetInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 0 || len(in.Name) < 1 || len(in.Name) > 128 || strings.TrimSpace(in.Name) != in.Name || strings.ContainsFunc(in.Name, unicode.IsControl) || ValidateBackupRemoteEndpoint(in.Endpoint) != nil || !remoteBucketName.MatchString(in.Bucket) || strings.Contains(in.Bucket, "..") || strings.Contains(in.Bucket, ".-") || strings.Contains(in.Bucket, "-.") || net.ParseIP(in.Bucket) != nil || !remoteRegionName.MatchString(in.Region) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTarget records versioned remote backup target configuration.
type HostingDatabaseBackupTarget struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Version   string    `json:"version"`
	Revision  int64     `json:"revision"`
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	Bucket    string    `json:"bucket"`
	Region    string    `json:"region"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate checks the database backup target fields and invariants.
func (t HostingDatabaseBackupTarget) Validate() error {
	if !validRuntimeReferenceID(t.ID) || !validRuntimeReferenceID(t.ProjectID) || t.Version != HostingDatabaseBackupTargetVersion || t.Revision < 1 || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return ErrInvalidDatabaseBackupRemote
	}
	return (HostingDatabaseBackupTargetInput{RequestID: "validate", Name: t.Name, Endpoint: t.Endpoint, Bucket: t.Bucket, Region: t.Region}).Validate()
}

// BackupRemoteCredentialRef identifies one immutable managed credential version.
type BackupRemoteCredentialRef struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
}

// HostingDatabaseBackupTargetSnapshot binds target configuration to immutable credential
// references.
type HostingDatabaseBackupTargetSnapshot struct {
	Target HostingDatabaseBackupTarget `json:"target"`
	Access BackupRemoteCredentialRef   `json:"access"`
	Secret BackupRemoteCredentialRef   `json:"secret"`
}

// Validate checks the database backup target snapshot fields and invariants.
func (s HostingDatabaseBackupTargetSnapshot) Validate() error {
	if s.Target.Validate() != nil || !validRuntimeReferenceID(s.Access.SecretID) || !validRuntimeReferenceID(s.Secret.SecretID) || s.Access.SecretID == s.Secret.SecretID || s.Access.Version < 1 || s.Secret.Version < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTargetValidationInput describes the requested database backup target
// validation.
type HostingDatabaseBackupTargetValidationInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	DestinationID    string `json:"destination_id"`
}

// Validate checks the database backup target validation input fields and invariants.
func (in HostingDatabaseBackupTargetValidationInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 1 || !validRuntimeReferenceID(in.DestinationID) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTransferRetryInput describes the requested database backup transfer retry.
type HostingDatabaseBackupTransferRetryInput struct {
	RequestID          string `json:"request_id"`
	ExpectedTransferID string `json:"expected_transfer_id"`
}

// Validate checks the database backup transfer retry input fields and invariants.
func (in HostingDatabaseBackupTransferRetryInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || !validRuntimeReferenceID(in.ExpectedTransferID) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTransfer records a database backup transfer operation and its state.
type HostingDatabaseBackupTransfer struct {
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
	Blob               *HostingDatabaseBackupBlob `json:"blob,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

// ValidBackupRemoteFailure reports whether a remote transfer failure code is recognized.
func ValidBackupRemoteFailure(s string) bool {
	switch s {
	case "", "access_denied", "network_denied", "remote_unavailable", "object_mismatch", "archive_unavailable", "interrupted", "authority_denied":
		return true
	}
	return false
}

// Validate checks the database backup transfer fields and invariants.
func (t HostingDatabaseBackupTransfer) Validate() error {
	if !validRuntimeReferenceID(t.ID) || (t.Version != HostingDatabaseBackupTransferVersion && t.Version != HostingDatabaseMySQLBackupTransferVersion) || !validRuntimeReferenceID(t.ProjectID) || !validRuntimeReferenceID(t.TargetID) || t.TargetRevision < 1 || !validRuntimeReferenceID(t.DestinationID) || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || !ValidBackupRemoteFailure(t.FailureCode) {
		return ErrInvalidDatabaseBackupRemote
	}
	switch t.State {
	case "queued", "dispatched", "applying", "succeeded", "failed", "uncertain":
	default:
		return ErrInvalidDatabaseBackupRemote
	}
	switch t.Mode {
	case "validate":
		if t.Version != HostingDatabaseBackupTransferVersion || t.BackupID != "" || t.RestoreOperationID != "" || t.Blob != nil {
			return ErrInvalidDatabaseBackupRemote
		}
	case "upload", "download":
		format := "postgresql_custom"
		if t.Version == HostingDatabaseMySQLBackupTransferVersion {
			format = "mysql_sql"
		}
		if !validRuntimeReferenceID(t.BackupID) || t.Blob == nil || t.Blob.Validate() != nil || t.Blob.Format != format {
			return ErrInvalidDatabaseBackupRemote
		}
		if t.Mode == "download" && !validRuntimeReferenceID(t.RestoreOperationID) || t.Mode == "upload" && t.RestoreOperationID != "" {
			return ErrInvalidDatabaseBackupRemote
		}
	default:
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTransferPlan contains opaque key references and immutable object identity,
// never plaintext credentials, local paths or executable commands.
type HostingDatabaseBackupTransferPlan struct {
	Operation      HostingDatabaseBackupTransfer       `json:"operation"`
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

// BackupRemoteObjectKey returns the immutable remote object key for a database backup blob.
func BackupRemoteObjectKey(backup string, blob HostingDatabaseBackupBlob) string {
	if blob.Format == "mysql_sql" {
		return "nerocd/mysql/" + backup + "/" + strings.TrimPrefix(blob.Digest, "sha256:") + ".sql"
	}
	return "nerocd/postgresql/" + backup + "/" + strings.TrimPrefix(blob.Digest, "sha256:") + ".dump"
}

// Validate checks the database backup transfer plan fields and invariants.
func (p HostingDatabaseBackupTransferPlan) Validate() error {
	h := p.Hash
	p.Hash = ""
	if h != HostingDocumentHash(p) || p.Operation.Validate() != nil || p.Target.Validate() != nil || p.Operation.ProjectID != p.Target.Target.ProjectID || p.Operation.TargetID != p.Target.Target.ID || p.Operation.TargetRevision != p.Target.Target.Revision || !validRuntimeReferenceID(p.ActorID) || !validRuntimeReferenceID(p.RunnerID) || !validRuntimeReferenceID(p.ServerID) || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || p.MaxBytes < 5 || p.MaxBytes > HostingDatabaseBackupMaxBytes {
		return ErrInvalidDatabaseBackupRemote
	}
	if p.Operation.Mode == "validate" {
		if p.ObjectKey != "" || p.LocationID != "" || p.DaemonID != "" {
			return ErrInvalidDatabaseBackupRemote
		}
	} else if p.LocationID != p.Operation.BackupID || p.Operation.Blob.Bytes > p.MaxBytes || p.ObjectKey != BackupRemoteObjectKey(p.Operation.BackupID, *p.Operation.Blob) || p.DaemonID == "" {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// HostingDatabaseBackupTransferMutation reports a fenced database backup transfer execution
// outcome.
type HostingDatabaseBackupTransferMutation struct {
	LocalCopy  *BackupLocalCopyProof  `json:"local_copy,omitempty"`
	RemoteCopy *BackupRemoteCopyProof `json:"remote_copy,omitempty"`
	HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
	DaemonID    string `json:"daemon_id"`
}

// Validate checks the database backup transfer mutation fields and invariants.
func (in HostingDatabaseBackupTransferMutation) Validate() error {
	if in.LocalCopy != nil && (in.Phase != "complete" || in.LocalCopy.Validate() != nil || in.RemoteCopy != nil) {
		return ErrInvalidDatabaseBackupRemote
	}
	if in.RemoteCopy != nil && (in.Phase != "complete" || in.RemoteCopy.Validate() != nil) {
		return ErrInvalidDatabaseBackupRemote
	}
	a := in.HostingAuthority
	if !validRuntimeReferenceID(a.OperationID) || a.RunID != "db_transfer_"+a.OperationID || !validRuntimeReferenceID(a.LeaseID) || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(a.Fence) < 1 || len(a.Fence) > 256 || !validRuntimeReferenceID(in.ReceiptID) || !imageSHA256.MatchString(in.PlanHash) || !ValidBackupRemoteFailure(in.FailureCode) || len(in.DaemonID) < 1 || len(in.DaemonID) > 256 {
		return ErrInvalidDatabaseBackupRemote
	}
	switch in.Phase {
	case "intent", "complete", "failed", "uncertain":
	default:
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

// ValidateDatabaseBackupTransferTask checks the database backup transfer task contract.
func ValidateDatabaseBackupTransferTask(r TaskRun) error {
	s := r.RunSpec
	if s.Type != RunTypeDatabaseBackupTransfer || !validRuntimeReferenceID(s.HostingOperationID) || r.ID != "db_transfer_"+s.HostingOperationID || (s.HostingMode != "validate" && s.HostingMode != "upload" && s.HostingMode != "download") || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(r.Workflow.Steps) != 0 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}
