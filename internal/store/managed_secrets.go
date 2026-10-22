package store

import (
	"context"
	"math"
	"regexp"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
)

// ManagedSecretRecord is a storage-only encrypted version. It deliberately cannot serialize encryption details.
type ManagedSecretRecord struct {
	Metadata domain.ManagedSecretVersion `json:"-"`
	Envelope secretcrypto.Envelope       `json:"-"`
}

// ManagedSecretRepository serializes version changes, irreversible revocation and mandatory reveal audits.
// GetManagedSecretRecord never grants permission to disclose plaintext. A caller must decrypt,
// then successfully commit AuthorizeManagedSecretReveal before returning any plaintext.
type ManagedSecretRepository interface {
	ListManagedSecrets(context.Context, string, Page) (PageResult[domain.ManagedSecret], error)
	GetManagedSecret(context.Context, string) (domain.ManagedSecret, error)
	ListManagedSecretVersions(context.Context, string, Page) (PageResult[domain.ManagedSecretVersion], error)
	GetManagedSecretRecord(context.Context, string, int64) (ManagedSecretRecord, error)
	CreateManagedSecret(context.Context, domain.ManagedSecret, ManagedSecretRecord, domain.AuditEvent) (domain.ManagedSecret, error)
	RotateManagedSecret(context.Context, string, int64, ManagedSecretRecord, domain.AuditEvent) (domain.ManagedSecret, error)
	RevokeManagedSecret(context.Context, string, int64, domain.AuditEvent) (domain.ManagedSecret, error)
	AuthorizeManagedSecretReveal(context.Context, string, int64, domain.AuditEvent) error
}

var _ ManagedSecretRepository = (*MemoryStore)(nil)
var _ ManagedSecretRepository = (*PostgresStore)(nil)

var managedSecretKeyID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validManagedRecord(record ManagedSecretRecord) bool {
	e := record.Envelope
	return record.Metadata.SecretID != "" && record.Metadata.Version > 0 && record.Metadata.CreatedBy != "" && e.Version == 1 && managedSecretKeyID.MatchString(e.KeyID) && len(e.Data) >= 29 && len(e.Data) <= 65564
}
func validManagedExpected(version int64) bool { return version > 0 && version < math.MaxInt64 }

// Normalize audit contents at the persistence boundary: caller metadata cannot carry a value.
func managedSecretAudit(event domain.AuditEvent, action string, secret domain.ManagedSecret, version int64) (domain.AuditEvent, error) {
	if event.ID == "" || event.ActorID == "" {
		return domain.AuditEvent{}, ErrConflict
	}
	metadata := map[string]any{"project_id": secret.ProjectID, "version": version}
	if requestID, ok := event.Metadata["request_id"].(string); ok && len(requestID) <= 128 {
		metadata["request_id"] = requestID
	}
	event.Action = action
	event.TargetID = secret.ID
	event.Metadata = metadata
	return event, nil
}
func copyManagedSecret(item domain.ManagedSecret) domain.ManagedSecret {
	if item.RevokedAt != nil {
		v := *item.RevokedAt
		item.RevokedAt = &v
	}
	return item
}
func copyManagedRecord(record ManagedSecretRecord) ManagedSecretRecord {
	record.Envelope.Data = append([]byte(nil), record.Envelope.Data...)
	return record
}
