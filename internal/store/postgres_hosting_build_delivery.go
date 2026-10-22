package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

const sourceDeliveryAction = "hosting.source_credential_authorized"

// Only this closed, non-secret document enters the append-only audit ledger.
type sourceDeliveryAudit struct {
	Purpose       string                               `json:"purpose"`
	RunnerID      string                               `json:"runner_id"`
	Authority     domain.HostingBuildAuthority         `json:"authority"`
	SourceID      string                               `json:"source_id"`
	InputID       string                               `json:"input_id"`
	ProjectID     string                               `json:"project_id"`
	GrantID       string                               `json:"grant_id"`
	GrantVersion  int64                                `json:"grant_version"`
	SecretID      string                               `json:"secret_id"`
	SecretVersion int64                                `json:"secret_version"`
	Kind          string                               `json:"kind"`
	Window        domain.HostingSourceCredentialWindow `json:"window"`
}

// AuthorizeHostingSourceCredential commits authorization before returning any
// envelope. It never uses historical receipt replay to grant credential access.
func (s *PostgresStore) AuthorizeHostingSourceCredential(ctx context.Context, runner string, in domain.HostingSourceCredentialRequest) (HostingSourceCredentialAuthorization, error) {
	var result HostingSourceCredentialAuthorization
	if runner == "" || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
		return result, ErrConflict
	}
	_, err := s.buildRunnerTransaction(ctx, runner, in.Authority, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		if b.State != "sourcing" || b.SourceReceipt != nil || b.Snapshot.Grant == nil || b.Snapshot.Source.CredentialKind == "public" {
			return b, ErrConflict
		}
		q := sqlcgen.New(tx)
		resource, err := q.LockHostingBuildResource(ctx, b.ResourceID)
		if err != nil || resource.ProjectID != b.ProjectID || resource.Generation != b.Snapshot.ResourceGeneration {
			return b, ErrConflict
		}
		src, err := readBuildSource(ctx, tx, b.ResourceID)
		if err != nil || domain.HostingDocumentHash(src) != domain.HostingDocumentHash(b.Snapshot.Source) {
			return b, ErrConflict
		}
		if err = freshBuildAuthority(ctx, tx, runner, in.Authority, b); err != nil {
			return b, err
		}
		// Acquire the delivery-only lock after the existing build lock order. No other
		// build's locks are acquired below it, so different builds cannot form a cycle.
		if err = q.LockHostingSourceDelivery(ctx, in.DeliveryID); err != nil {
			return b, err
		}
		grant := b.Snapshot.Grant
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.HostingSourceCredentialRequest
		}{runner, in})
		expected := sourceDeliveryAudit{Purpose: "git_fetch", RunnerID: runner, Authority: in.Authority, SourceID: src.ID, InputID: b.Snapshot.InputID, ProjectID: b.ProjectID, GrantID: grant.ID, GrantVersion: grant.Version, SecretID: grant.SecretID, SecretVersion: grant.SecretVersion, Kind: grant.Kind, Window: domain.HostingSourceCredentialWindow{DeliveryID: in.DeliveryID, RequestHash: hash}}
		auditID := "aud_source_delivery_" + in.DeliveryID
		oldAudit, lookupErr := q.GetHostingSourceDeliveryAudit(ctx, auditID)
		err = lookupErr
		var now, expiry time.Time
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return b, err
		}
		if err == nil {
			var old sourceDeliveryAudit
			if json.Unmarshal(oldAudit.Metadata, &old) != nil || oldAudit.Action != sourceDeliveryAction || oldAudit.TargetID != b.ID || oldAudit.ActorID != runner {
				return b, ErrConflict
			}
			expected.Window.UseUntil = old.Window.UseUntil
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(expected) {
				return b, ErrConflict
			}
		} else {
			lease, e := q.HostingSourceDeliveryLeaseWindow(ctx, in.Authority.LeaseID)
			if e != nil {
				return b, e
			}
			now, expiry = lease.DatabaseNow, lease.ExpiresAt
			expected.Window.UseUntil = now.Add(time.Minute)
			if expiry.Before(expected.Window.UseUntil) {
				expected.Window.UseUntil = expiry
			}
			metadata := map[string]any{}
			if err = json.Unmarshal(buildJSON(expected), &metadata); err != nil {
				return b, err
			}
			if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: auditID, ActorID: runner, Action: sourceDeliveryAction, TargetID: b.ID, CreatedAt: now, Metadata: metadata}); err != nil {
				return b, err
			}
		}
		record, err := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: grant.SecretID, Version: grant.SecretVersion})
		if err != nil {
			return b, err
		}
		// The audit INSERT may wait. Recheck all live temporal authority after it,
		// and the original delivery window after lease renewal, before committing.
		if err = freshBuildAuthority(ctx, tx, runner, in.Authority, b); err != nil {
			return b, err
		}
		if now, err = q.HostingSourceDeliveryNow(ctx); err != nil {
			return b, err
		}
		if !now.Before(expected.Window.UseUntil) {
			return b, ErrConflict
		}
		result = HostingSourceCredentialAuthorization{Window: expected.Window, ProjectID: b.ProjectID, SecretID: grant.SecretID, SecretVersion: grant.SecretVersion, Kind: grant.Kind, Record: ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: record.SecretID, Version: record.Version, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}}
		return b, nil
	})
	if err != nil {
		return HostingSourceCredentialAuthorization{}, err
	}
	return result, nil
}
