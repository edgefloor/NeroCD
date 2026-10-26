package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

const runtimeDeliveryAction = "hosting.runtime_environment_authorized"

// This closed audit document contains public bindings and references, never values.
type runtimeDeliveryAudit struct {
	Purpose    string                                  `json:"purpose"`
	RunnerID   string                                  `json:"runner_id"`
	Request    domain.HostingRuntimeEnvironmentRequest `json:"request"`
	ProjectID  string                                  `json:"project_id"`
	ResourceID string                                  `json:"resource_id"`
	EntryCount int                                     `json:"entry_count"`
	References []domain.HostingRuntimeSecretReference  `json:"references"`
	Window     domain.HostingRuntimeEnvironmentWindow  `json:"window"`
}

// AuthorizeHostingRuntimeEnvironment commits metadata audit before releasing encrypted records.
func (s *PostgresStore) AuthorizeHostingRuntimeEnvironment(ctx context.Context, runner, credentialHash string, in domain.HostingRuntimeEnvironmentRequest) (result HostingRuntimeEnvironmentAuthorization, err error) {
	if runner == "" || credentialHash == "" || hostingruntimeprotocol.ValidateRequest(in) != nil {
		return result, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollbackTransaction(ctx, tx)
	defer func() {
		if err != nil {
			result.Close()
		}
	}()
	if err = hostingAuthorityExists(ctx, tx, runner, in.Authority, true); err != nil {
		return result, err
	}
	op, err := hostingOperation(ctx, tx, in.Authority.OperationID)
	if err != nil {
		return result, err
	}
	if op.ApplicationDatabase != nil {
		ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, credentialHash)
		if err != nil {
			return result, err
		}
	}
	combinedStorage := op.Snapshot.PolicyVersion == domain.HostingOwnedStorageRuntimePolicyVersion && op.MutationHold
	if combinedStorage {
		if err = hostingStorageWriterReady(ctx, tx, in.Authority, op); err != nil {
			return result, err
		}
	}
	if op.State != "prepared" || op.MutationHold && !combinedStorage || op.CleanupPlan != nil || op.RunID != in.Authority.RunID || op.PreparedReceipt == nil || op.CompleteReceipt != nil || op.Plan == nil || op.Release == nil || op.Snapshot.RunnerID != runner || op.Plan.Hash != in.PlanHash || op.Snapshot.RuntimeConfiguration != in.RuntimeConfiguration || !hostingOperationPlanMatches(op) {
		return result, ErrConflict
	}
	if err = hostingRequireMode(ctx, tx, in.Authority, "deploy"); err != nil {
		return result, err
	}
	// Match request admission's destination -> resource -> configuration order.
	if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
		return result, err
	}
	q := sqlcgen.New(tx)
	r, err := q.LockHostingRuntimeResource(ctx, op.ResourceID)
	if err != nil {
		return result, err
	}
	if r.ProjectID != op.ProjectID || r.Generation != op.Snapshot.ResourceGeneration {
		return result, ErrConflict
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return result, err
	}
	// The target gate has acquired the runner SHARE lock in the established
	// placement order. Bind the bearer authenticated by the delivery service.
	if _, err = q.CheckHostingRuntimeEnvironmentCredential(ctx, sqlcgen.CheckHostingRuntimeEnvironmentCredentialParams{RunnerID: runner, CredentialHash: credentialHash}); err != nil {
		return result, ErrConflict
	}
	row, err := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: op.ResourceID, Generation: in.RuntimeConfiguration.Generation})
	if err != nil {
		return result, err
	}
	var doc domain.HostingRuntimeDocument
	if row.ProjectID != op.ProjectID || row.BodyHash != in.RuntimeConfiguration.Hash || json.Unmarshal(row.Body, &doc) != nil {
		return result, ErrConflict
	}
	doc, refs, err := domain.CanonicalHostingRuntimeDocument(doc.Entries)
	if err != nil || domain.HostingDocumentHash(doc) != row.BodyHash {
		return result, ErrConflict
	}
	if err = validateRuntimeReferences(ctx, q, op.ProjectID, refs); err != nil {
		return result, err
	}
	if op.ApplicationDatabase != nil {
		if err = connectionRuntimeSecretReferences(ctx, tx, op, refs); err != nil {
			return result, err
		}
	}
	if err = q.LockHostingRuntimeEnvironmentDelivery(ctx, in.DeliveryID); err != nil {
		return result, err
	}
	expected := runtimeDeliveryAudit{Purpose: hostingruntimeprotocol.Purpose, RunnerID: runner, Request: in, ProjectID: op.ProjectID, ResourceID: op.ResourceID, EntryCount: len(doc.Entries), References: refs, Window: domain.HostingRuntimeEnvironmentWindow{DeliveryID: in.DeliveryID, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash(runner, in)}}
	auditID := "aud_runtime_delivery_" + in.DeliveryID
	old, lookupErr := q.GetHostingRuntimeEnvironmentDeliveryAudit(ctx, auditID)
	if lookupErr == nil {
		var previous runtimeDeliveryAudit
		if old.Action != runtimeDeliveryAction || old.TargetID != op.ID || old.ActorID != runner || json.Unmarshal(old.Metadata, &previous) != nil {
			return result, ErrConflict
		}
		expected.Window.UseUntil = previous.Window.UseUntil
		if domain.HostingDocumentHash(previous) != domain.HostingDocumentHash(expected) {
			return result, ErrConflict
		}
	} else if errors.Is(lookupErr, pgx.ErrNoRows) {
		window, e := q.HostingRuntimeEnvironmentDeliveryWindow(ctx, in.Authority.LeaseID)
		if e != nil {
			return result, e
		}
		expected.Window.UseUntil = window.DatabaseNow.Add(time.Minute)
		if window.ExpiresAt.Before(expected.Window.UseUntil) {
			expected.Window.UseUntil = window.ExpiresAt
		}
		metadata := map[string]any{}
		if err = json.Unmarshal(buildJSON(expected), &metadata); err != nil {
			return result, err
		}
		if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: auditID, ActorID: runner, Action: runtimeDeliveryAction, TargetID: op.ID, CreatedAt: window.DatabaseNow, Metadata: metadata}); err != nil {
			return result, err
		}
	} else {
		return result, lookupErr
	}
	result = HostingRuntimeEnvironmentAuthorization{Window: expected.Window, ProjectID: op.ProjectID, Document: doc, Records: make(map[domain.HostingRuntimeSecretReference]ManagedSecretRecord, len(refs))}
	for _, ref := range refs {
		record, e := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: ref.SecretID, Version: ref.Version})
		if e != nil {
			return result, e
		}
		result.Records[ref] = ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: record.SecretID, Version: record.Version, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}
	}
	// The audit INSERT can block. All mutable configuration and reference rows
	// remain locked, but lease and actor time and capabilities must be checked now.
	if err = runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return result, err
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return result, err
	}
	if err = finalHostingRuntimeAuthority(ctx, tx, runner, in.Authority, op); err != nil {
		return result, err
	}
	if _, err = q.CheckHostingRuntimeEnvironmentCredential(ctx, sqlcgen.CheckHostingRuntimeEnvironmentCredentialParams{RunnerID: runner, CredentialHash: credentialHash}); err != nil {
		return result, ErrConflict
	}
	now, err := q.HostingRuntimeEnvironmentDeliveryNow(ctx)
	if err != nil {
		return result, err
	}
	if !now.Before(expected.Window.UseUntil) {
		return result, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}
