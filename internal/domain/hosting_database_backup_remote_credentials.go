package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// ErrBackupRemoteCredentials reports unavailable or invalid remote backup credentials.
var ErrBackupRemoteCredentials = errors.New("remote backup credentials unavailable")

// HostingDatabaseImportCredentialPurpose identifies credentials issued for external SQL staging.
const HostingDatabaseImportCredentialPurpose = "hosting_mysql_external_stage_credentials.v1"

// HostingStorageBackupRetentionCredentialPurpose identifies credentials issued for storage backup
// retention.
const HostingStorageBackupRetentionCredentialPurpose = "hosting_storage_backup_retention_credentials.v1"

// HostingDatabaseBackupRetentionCredentialPurpose identifies credentials issued for database
// backup retention.
const HostingDatabaseBackupRetentionCredentialPurpose = "hosting_postgresql_backup_retention_credentials.v1"

// HostingDatabaseMySQLBackupRetentionCredentialPurpose identifies native MySQL retention credentials.
const HostingDatabaseMySQLBackupRetentionCredentialPurpose = "hosting_mysql_backup_retention_credentials.v1"

// BackupRemoteCredentialRequest binds a credential delivery to a runner lease, target, and plan.
type BackupRemoteCredentialRequest struct {
	RetentionCopyID string           `json:"retention_copy_id,omitempty"`
	Authority       HostingAuthority `json:"authority"`
	DeliveryID      string           `json:"delivery_id"`
	PlanHash        string           `json:"plan_hash"`
	TargetID        string           `json:"target_id"`
	TargetRevision  int64            `json:"target_revision"`
	DaemonID        string           `json:"daemon_id"`
}

// Validate checks the backup remote credential request fields and invariants.
func (r BackupRemoteCredentialRequest) Validate() error {
	if !validRuntimeReferenceID(r.TargetID) || r.TargetRevision < 1 {
		return ErrBackupRemoteCredentials
	}
	if r.RetentionCopyID != "" {
		a := r.Authority
		if !validRuntimeReferenceID(r.RetentionCopyID) || !validRuntimeReferenceID(a.OperationID) || (a.RunID != "db_retention_"+a.OperationID && a.RunID != "storage_retention_"+a.OperationID) || !validRuntimeReferenceID(a.LeaseID) || a.Attempt < 1 || a.Attempt > math.MaxInt32 || len(a.Fence) < 1 || len(a.Fence) > 256 || !validRuntimeReferenceID(r.DeliveryID) || !imageSHA256.MatchString(r.PlanHash) || len(r.DaemonID) < 1 || len(r.DaemonID) > 256 {
			return ErrBackupRemoteCredentials
		}
		return nil
	}
	if r.Authority.RunID == "db_import_stage_"+r.Authority.OperationID {
		check := HostingDatabaseImportMutation{HostingAuthority: r.Authority, ReceiptID: r.DeliveryID, PlanHash: r.PlanHash, Phase: "stage_intent", DaemonID: r.DaemonID}
		if check.Validate() != nil {
			return ErrBackupRemoteCredentials
		}
		return nil
	}
	if r.Authority.RunID == "storage_transfer_"+r.Authority.OperationID {
		check := HostingStorageBackupTransferMutation{HostingAuthority: r.Authority, ReceiptID: r.DeliveryID, PlanHash: r.PlanHash, Phase: "intent", DaemonID: r.DaemonID}
		if check.Validate() != nil {
			return ErrBackupRemoteCredentials
		}
		return nil
	}
	check := HostingDatabaseBackupTransferMutation{HostingAuthority: r.Authority, ReceiptID: r.DeliveryID, PlanHash: r.PlanHash, Phase: "intent", DaemonID: r.DaemonID}
	if check.Validate() != nil {
		return ErrBackupRemoteCredentials
	}
	return nil
}

// BackupRemoteCredentialMetadata records the identity and deadline of a credential delivery.
type BackupRemoteCredentialMetadata struct {
	Purpose     string                        `json:"purpose"`
	Request     BackupRemoteCredentialRequest `json:"request"`
	RunnerID    string                        `json:"runner_id"`
	RequestHash string                        `json:"request_hash"`
	UseUntil    time.Time                     `json:"use_until"`
}

// Validate checks the backup remote credential metadata fields and invariants.
func (m BackupRemoteCredentialMetadata) Validate() error {
	expected := HostingDatabaseBackupRemoteCredentialPurpose
	if m.Request.RetentionCopyID == "" && m.Request.Authority.RunID == "db_transfer_"+m.Request.Authority.OperationID && (m.Purpose == HostingDatabaseMySQLBackupRemoteCredentialPurpose || m.Purpose == HostingDatabaseMySQLRemoteRestoreCredentialPurpose) {
		expected = m.Purpose
	}
	if m.Request.Authority.RunID == "storage_transfer_"+m.Request.Authority.OperationID {
		expected = HostingStorageBackupRemoteCredentialPurpose
	}
	if m.Request.RetentionCopyID != "" {
		expected = HostingDatabaseBackupRetentionCredentialPurpose
		if m.Request.Authority.RunID == "db_retention_"+m.Request.Authority.OperationID && m.Purpose == HostingDatabaseMySQLBackupRetentionCredentialPurpose {
			expected = HostingDatabaseMySQLBackupRetentionCredentialPurpose
		}
		if m.Request.Authority.RunID == "storage_retention_"+m.Request.Authority.OperationID {
			expected = HostingStorageBackupRetentionCredentialPurpose
		}
	}
	if m.Request.Authority.RunID == "db_import_stage_"+m.Request.Authority.OperationID && m.Request.RetentionCopyID == "" {
		expected = HostingDatabaseImportCredentialPurpose
	}
	if m.Purpose != expected || m.Request.Validate() != nil || !validRuntimeReferenceID(m.RunnerID) || m.RequestHash != HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{m.RunnerID, m.Request}) || m.UseUntil.IsZero() {
		return ErrBackupRemoteCredentials
	}
	return nil
}

// BackupRemoteCredentials holds plaintext delivery credentials. Call Close after use.
type BackupRemoteCredentials struct {
	metadata       BackupRemoteCredentialMetadata
	access, secret []byte
}

// NewBackupRemoteCredentials validates and copies plaintext credentials. Call Close after use.
func NewBackupRemoteCredentials(m BackupRemoteCredentialMetadata, access, secret []byte) (*BackupRemoteCredentials, error) {
	if m.Validate() != nil || len(access) < 1 || len(access) > 256 || len(secret) < 1 || len(secret) > 4096 {
		return nil, ErrBackupRemoteCredentials
	}
	return &BackupRemoteCredentials{metadata: m, access: bytes.Clone(access), secret: bytes.Clone(secret)}, nil
}

// Metadata returns the persisted credential delivery metadata.
func (c *BackupRemoteCredentials) Metadata() BackupRemoteCredentialMetadata { return c.metadata }

// Use calls fn with the held credential bytes while they are available.
func (c *BackupRemoteCredentials) Use(fn func([]byte, []byte) error) error {
	if c == nil || len(c.access) == 0 || len(c.secret) == 0 || fn == nil {
		return ErrBackupRemoteCredentials
	}
	return fn(c.access, c.secret)
}

// Close clears the held credential values.
func (c *BackupRemoteCredentials) Close() {
	if c != nil {
		clear(c.access)
		clear(c.secret)
		c.access = nil
		c.secret = nil
	}
}

// Format writes a redacted representation.
func (*BackupRemoteCredentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[protected remote backup credentials]")
}

// MarshalJSON returns an empty object without protected configuration values.
func (*BackupRemoteCredentials) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// WriteBackupRemoteCredentials is restricted to authenticated protected runtime transport.
// Ordinary JSON encoding of the credential holder can never reveal its values.
func WriteBackupRemoteCredentials(w io.Writer, c *BackupRemoteCredentials) error {
	if c == nil {
		return ErrBackupRemoteCredentials
	}
	wire := struct {
		Metadata BackupRemoteCredentialMetadata `json:"metadata"`
		Access   []byte                         `json:"access_key"`
		Secret   []byte                         `json:"secret_key"`
	}{c.metadata, c.access, c.secret}
	raw, err := json.Marshal(wire)
	if err != nil {
		return ErrBackupRemoteCredentials
	}
	defer clear(raw)
	_, err = w.Write(raw)
	return err
}

// ReadBackupRemoteCredentials reads a bounded credential document from protected transport.
func ReadBackupRemoteCredentials(r io.Reader) (*BackupRemoteCredentials, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 16385))
	defer clear(raw)
	if err != nil || len(raw) > 16384 {
		return nil, ErrBackupRemoteCredentials
	}
	var wire struct {
		Metadata BackupRemoteCredentialMetadata `json:"metadata"`
		Access   []byte                         `json:"access_key"`
		Secret   []byte                         `json:"secret_key"`
	}
	defer func() { clear(wire.Access); clear(wire.Secret) }()
	if DecodeHostingStorageProtocolJSON(raw, &wire) != nil {
		return nil, ErrBackupRemoteCredentials
	}
	return NewBackupRemoteCredentials(wire.Metadata, wire.Access, wire.Secret)
}
