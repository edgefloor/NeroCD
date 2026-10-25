package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrBackupRemoteCredentials = errors.New("remote backup credentials unavailable")

type BackupRemoteCredentialRequest struct {
	Authority      HostingAuthority `json:"authority"`
	DeliveryID     string           `json:"delivery_id"`
	PlanHash       string           `json:"plan_hash"`
	TargetID       string           `json:"target_id"`
	TargetRevision int64            `json:"target_revision"`
	DaemonID       string           `json:"daemon_id"`
}

func (r BackupRemoteCredentialRequest) Validate() error {
	check := HostingDatabaseBackupTransferMutation{HostingAuthority: r.Authority, ReceiptID: r.DeliveryID, PlanHash: r.PlanHash, Phase: "intent", DaemonID: r.DaemonID}
	if check.Validate() != nil || !validRuntimeReferenceID(r.TargetID) || r.TargetRevision < 1 {
		return ErrBackupRemoteCredentials
	}
	return nil
}

type BackupRemoteCredentialMetadata struct {
	Purpose     string                        `json:"purpose"`
	Request     BackupRemoteCredentialRequest `json:"request"`
	RunnerID    string                        `json:"runner_id"`
	RequestHash string                        `json:"request_hash"`
	UseUntil    time.Time                     `json:"use_until"`
}

func (m BackupRemoteCredentialMetadata) Validate() error {
	if m.Purpose != HostingDatabaseBackupRemoteCredentialPurpose || m.Request.Validate() != nil || !validRuntimeReferenceID(m.RunnerID) || m.RequestHash != HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{m.RunnerID, m.Request}) || m.UseUntil.IsZero() {
		return ErrBackupRemoteCredentials
	}
	return nil
}

type BackupRemoteCredentials struct {
	metadata       BackupRemoteCredentialMetadata
	access, secret []byte
}

func NewBackupRemoteCredentials(m BackupRemoteCredentialMetadata, access, secret []byte) (*BackupRemoteCredentials, error) {
	if m.Validate() != nil || len(access) < 1 || len(access) > 256 || len(secret) < 1 || len(secret) > 4096 {
		return nil, ErrBackupRemoteCredentials
	}
	return &BackupRemoteCredentials{metadata: m, access: bytes.Clone(access), secret: bytes.Clone(secret)}, nil
}
func (c *BackupRemoteCredentials) Metadata() BackupRemoteCredentialMetadata { return c.metadata }
func (c *BackupRemoteCredentials) Use(fn func([]byte, []byte) error) error {
	if c == nil || len(c.access) == 0 || len(c.secret) == 0 || fn == nil {
		return ErrBackupRemoteCredentials
	}
	return fn(c.access, c.secret)
}
func (c *BackupRemoteCredentials) Close() {
	if c != nil {
		clear(c.access)
		clear(c.secret)
		c.access = nil
		c.secret = nil
	}
}
func (*BackupRemoteCredentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[protected remote backup credentials]")
}
func (*BackupRemoteCredentials) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// The explicit codec is restricted to authenticated protected runtime transport.
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
