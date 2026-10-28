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

const buildRegistryDeliveryAction = "hosting.build_registry_credential_authorized"

// Only this closed, non-secret document enters the append-only audit ledger.
type buildRegistryDeliveryAudit struct {
	Purpose       string                               `json:"purpose"`
	RunnerID      string                               `json:"runner_id"`
	Authority     domain.HostingBuildAuthority         `json:"authority"`
	SourceID      string                               `json:"source_id"`
	InputID       string                               `json:"input_id"`
	ProjectID     string                               `json:"project_id"`
	SecretID      string                               `json:"secret_id"`
	SecretVersion int64                                `json:"secret_version"`
	Kind          string                               `json:"kind"`
	Window        domain.HostingSourceCredentialWindow `json:"window"`
}

// AuthorizeHostingBuildRegistryCredential commits authorization before returning any
// envelope. It never uses historical receipt replay to grant credential access.
func (s *PostgresStore) AuthorizeHostingBuildRegistryCredential(ctx context.Context, runner string, in domain.HostingBuildRegistryCredentialRequest) (HostingBuildRegistryAuthorization, error) {
	var result HostingBuildRegistryAuthorization
	if runner == "" || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
		return result, ErrConflict
	}
	_, err := s.buildRunnerTransaction(ctx, runner, in.Authority, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		if b.State != "building" || b.SourceReceipt == nil || b.DaemonID == "" || b.Snapshot.Source.Settings.BuildRegistryCredential == nil {
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
		if err = q.LockHostingSourceDelivery(ctx, "build_registry:"+in.DeliveryID); err != nil {
			return b, err
		}
		ref := b.Snapshot.Source.Settings.BuildRegistryCredential
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.HostingBuildRegistryCredentialRequest
		}{runner, in})
		expected := buildRegistryDeliveryAudit{Purpose: "build_registry", RunnerID: runner, Authority: in.Authority, SourceID: src.ID, InputID: b.Snapshot.InputID, ProjectID: b.ProjectID, SecretID: ref.SecretID, SecretVersion: ref.Version, Kind: "registry", Window: domain.HostingSourceCredentialWindow{DeliveryID: in.DeliveryID, RequestHash: hash}}
		auditID := "aud_build_registry_delivery_" + in.DeliveryID
		oldAudit, lookupErr := q.GetHostingSourceDeliveryAudit(ctx, auditID)
		err = lookupErr
		var now, expiry time.Time
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return b, err
		}
		if err == nil {
			var old buildRegistryDeliveryAudit
			if json.Unmarshal(oldAudit.Metadata, &old) != nil || oldAudit.Action != buildRegistryDeliveryAction || oldAudit.TargetID != b.ID || oldAudit.ActorID != runner {
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
			if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: auditID, ActorID: runner, Action: buildRegistryDeliveryAction, TargetID: b.ID, CreatedAt: now, Metadata: metadata}); err != nil {
				return b, err
			}
		}
		record, err := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: ref.SecretID, Version: ref.Version})
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
		result = HostingBuildRegistryAuthorization{Window: expected.Window, ProjectID: b.ProjectID, Ref: *ref, Record: ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: record.SecretID, Version: record.Version, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}}
		return b, nil
	})
	if err != nil {
		return HostingBuildRegistryAuthorization{}, err
	}
	return result, nil
}

func validateBuildRegistrySecret(ctx context.Context, tx pgx.Tx, project string, settings domain.HostingBuildSettings) error {
	if settings.BuildRegistryCredential == nil {
		return nil
	}
	ref := settings.BuildRegistryCredential
	if ref.Validate() != nil || settings.Strategy != "dockerfile" && settings.Strategy != "compose" {
		return ErrConflict
	}
	return validateRuntimeReferences(ctx, sqlcgen.New(tx), project, []domain.HostingRuntimeSecretReference{{SecretID: ref.SecretID, Version: ref.Version}})
}
