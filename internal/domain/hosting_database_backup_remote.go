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

const (
	HostingDatabaseBackupTargetVersion           = "postgresql.backup-target.v1"
	HostingDatabaseBackupTransferVersion         = "postgresql.logical-s3-transfer.v1"
	HostingDatabaseBackupRemoteCapability        = "hosting_postgresql_remote_backup_v1"
	RunTypeDatabaseBackupTransfer                = "database_backup_transfer"
	HostingDatabaseBackupRemoteCredentialPurpose = "hosting_postgresql_remote_backup_credentials.v1"
)

var ErrInvalidDatabaseBackupRemote = errors.New("invalid PostgreSQL remote backup request")
var remoteBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var remoteRegionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)

// BackupTargetSelection pins configuration rather than following future edits.
type BackupTargetSelection struct {
	TargetID string `json:"target_id"`
	Revision int64  `json:"revision"`
}

func (r BackupTargetSelection) Validate() error {
	if !validRuntimeReferenceID(r.TargetID) || r.Revision < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

type BackupRemoteSource struct {
	TransferID             string `json:"transfer_id"`
	ExpectedTargetRevision int64  `json:"expected_target_revision"`
}

func (r BackupRemoteSource) Validate() error {
	if !validRuntimeReferenceID(r.TransferID) || r.ExpectedTargetRevision < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

type HostingDatabaseBackupTargetInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Name             string `json:"name"`
	Endpoint         string `json:"endpoint"`
	Bucket           string `json:"bucket"`
	Region           string `json:"region"`
	Enabled          bool   `json:"enabled"`
}

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
func (in HostingDatabaseBackupTargetInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 0 || len(in.Name) < 1 || len(in.Name) > 128 || strings.TrimSpace(in.Name) != in.Name || strings.ContainsFunc(in.Name, unicode.IsControl) || ValidateBackupRemoteEndpoint(in.Endpoint) != nil || !remoteBucketName.MatchString(in.Bucket) || strings.Contains(in.Bucket, "..") || strings.Contains(in.Bucket, ".-") || strings.Contains(in.Bucket, "-.") || net.ParseIP(in.Bucket) != nil || !remoteRegionName.MatchString(in.Region) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

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

func (t HostingDatabaseBackupTarget) Validate() error {
	if !validRuntimeReferenceID(t.ID) || !validRuntimeReferenceID(t.ProjectID) || t.Version != HostingDatabaseBackupTargetVersion || t.Revision < 1 || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return ErrInvalidDatabaseBackupRemote
	}
	return (HostingDatabaseBackupTargetInput{RequestID: "validate", Name: t.Name, Endpoint: t.Endpoint, Bucket: t.Bucket, Region: t.Region}).Validate()
}

type BackupRemoteCredentialRef struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
}
type HostingDatabaseBackupTargetSnapshot struct {
	Target HostingDatabaseBackupTarget `json:"target"`
	Access BackupRemoteCredentialRef   `json:"access"`
	Secret BackupRemoteCredentialRef   `json:"secret"`
}

func (s HostingDatabaseBackupTargetSnapshot) Validate() error {
	if s.Target.Validate() != nil || !validRuntimeReferenceID(s.Access.SecretID) || !validRuntimeReferenceID(s.Secret.SecretID) || s.Access.SecretID == s.Secret.SecretID || s.Access.Version < 1 || s.Secret.Version < 1 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

type HostingDatabaseBackupTargetValidationInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	DestinationID    string `json:"destination_id"`
}

func (in HostingDatabaseBackupTargetValidationInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 1 || !validRuntimeReferenceID(in.DestinationID) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

type HostingDatabaseBackupTransferRetryInput struct {
	RequestID          string `json:"request_id"`
	ExpectedTransferID string `json:"expected_transfer_id"`
}

func (in HostingDatabaseBackupTransferRetryInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || !validRuntimeReferenceID(in.ExpectedTransferID) {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}

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

func ValidBackupRemoteFailure(s string) bool {
	switch s {
	case "", "access_denied", "network_denied", "remote_unavailable", "object_mismatch", "archive_unavailable", "interrupted", "authority_denied":
		return true
	}
	return false
}
func (t HostingDatabaseBackupTransfer) Validate() error {
	if !validRuntimeReferenceID(t.ID) || t.Version != HostingDatabaseBackupTransferVersion || !validRuntimeReferenceID(t.ProjectID) || !validRuntimeReferenceID(t.TargetID) || t.TargetRevision < 1 || !validRuntimeReferenceID(t.DestinationID) || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || !ValidBackupRemoteFailure(t.FailureCode) {
		return ErrInvalidDatabaseBackupRemote
	}
	switch t.State {
	case "queued", "dispatched", "applying", "succeeded", "failed", "uncertain":
	default:
		return ErrInvalidDatabaseBackupRemote
	}
	switch t.Mode {
	case "validate":
		if t.BackupID != "" || t.RestoreOperationID != "" || t.Blob != nil {
			return ErrInvalidDatabaseBackupRemote
		}
	case "upload", "download":
		if !validRuntimeReferenceID(t.BackupID) || t.Blob == nil || t.Blob.Validate() != nil {
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

// Transfer plans contain opaque key references and immutable object identity,
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

func BackupRemoteObjectKey(backup string, blob HostingDatabaseBackupBlob) string {
	return "nerocd/postgresql/" + backup + "/" + strings.TrimPrefix(blob.Digest, "sha256:") + ".dump"
}
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

type HostingDatabaseBackupTransferMutation struct {
	HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
	DaemonID    string `json:"daemon_id"`
}

func (in HostingDatabaseBackupTransferMutation) Validate() error {
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
func ValidateDatabaseBackupTransferTask(r TaskRun) error {
	s := r.RunSpec
	if s.Type != RunTypeDatabaseBackupTransfer || !validRuntimeReferenceID(s.HostingOperationID) || r.ID != "db_transfer_"+s.HostingOperationID || (s.HostingMode != "validate" && s.HostingMode != "upload" && s.HostingMode != "download") || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(r.Workflow.Steps) != 0 {
		return ErrInvalidDatabaseBackupRemote
	}
	return nil
}
