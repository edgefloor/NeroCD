package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

const databaseDeliveryAction = "hosting.database_credentials_authorized"

// This closed audit document contains immutable public bindings only.
type databaseDeliveryAudit struct {
	ProjectID  string                           `json:"project_id"`
	ResourceID string                           `json:"resource_id"`
	Metadata   hostingdatabaseprotocol.Metadata `json:"delivery"`
}

// AuthorizeHostingDatabaseCredentials commits audit metadata before releasing ciphertext.
func (s *PostgresStore) AuthorizeHostingDatabaseCredentials(ctx context.Context, runner, credentialHash string, in domain.HostingDatabaseCredentialRequest) (HostingDatabaseCredentialAuthorization, error) {
	return s.databaseCredentialTransaction(ctx, runner, credentialHash, in, nil)
}

// RevalidateHostingDatabaseCredentials rechecks the exact original window and authority.
func (s *PostgresStore) RevalidateHostingDatabaseCredentials(ctx context.Context, runner, credentialHash string, in domain.HostingDatabaseCredentialRequest, original hostingdatabaseprotocol.Metadata) error {
	a, err := s.databaseCredentialTransaction(ctx, runner, credentialHash, in, &original)
	a.Close()
	if err != nil {
		return fmt.Errorf("revalidate database credential delivery: %w", err)
	}
	return nil
}

func (s *PostgresStore) databaseCredentialTransaction(ctx context.Context, runner, credentialHash string, in domain.HostingDatabaseCredentialRequest, original *hostingdatabaseprotocol.Metadata) (result HostingDatabaseCredentialAuthorization, err error) {
	if runner == "" || credentialHash == "" || in.Validate() != nil {
		return result, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin database credential authorization: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	defer func() {
		if err != nil {
			result.Close()
		}
	}()
	q := sqlcgen.New(tx)
	before, err := hostingOperation(ctx, tx, in.Authority.OperationID)
	if err == nil && before.DatabaseMaintenance != nil {
		ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, credentialHash)
	}
	if err != nil {
		return result, fmt.Errorf("read credential operation: %w", err)
	}
	// Match reconciliation's run -> lease -> operation -> placement lock order.
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return result, fmt.Errorf("lock credential operation tasks: %w", err)
	}
	if err = hostingAuthorityExists(ctx, tx, runner, in.Authority, true); err != nil {
		return result, fmt.Errorf("check credential lease authority: %w", err)
	}
	if _, err = q.LockHostingOperation(ctx, in.Authority.OperationID); err != nil {
		return result, fmt.Errorf("lock credential operation: %w", hostingError(err))
	}
	op, err := hostingOperation(ctx, tx, in.Authority.OperationID)
	if err != nil {
		return result, fmt.Errorf("read locked credential operation: %w", err)
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID || op.Snapshot.RunnerID != runner || op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) || op.Plan == nil || op.Plan.Database == nil || op.Release == nil || op.PreparedReceipt == nil || op.CleanupPlan != nil || op.Plan.Hash != in.PlanHash || !hostingOperationPlanMatches(op) {
		return result, ErrConflict
	}
	observe := in.Mode == "observe"
	if observe {
		if !op.ReconcileRequested || op.ReconciliationRunID != in.Authority.RunID {
			return result, ErrConflict
		}
	} else if op.RunID != in.Authority.RunID || op.State != "prepared" || op.CompleteReceipt != nil || op.RestoreOwnershipHeld {
		return result, ErrConflict
	}
	if err = hostingRequireMode(ctx, tx, in.Authority, in.Mode); err != nil {
		return result, fmt.Errorf("check credential task mode: %w", err)
	}
	if err = hostingOperationTargetGate(ctx, tx, op, observe); err != nil {
		return result, fmt.Errorf("check credential operation target: %w", err)
	}
	if err = hostingFreshStorageGate(ctx, tx, op); err != nil {
		return result, fmt.Errorf("check credential storage authority: %w", err)
	}
	if !observe {
		state, e := q.LockHostingAdmissionState(ctx, op.BindingID)
		if e != nil {
			return result, fmt.Errorf("lock credential binding hold: %w", e)
		}
		admission, e := q.ReadHostingStorageOperationAdmission(ctx, op.ID)
		if errors.Is(e, pgx.ErrNoRows) {
			if state.HoldOperationID != nil {
				return result, ErrConflict
			}
		} else if e != nil {
			return result, fmt.Errorf("read credential storage admission: %w", e)
		} else {
			// Storage admission owns a hold before writer ApplyBegin. Preserve it
			// only when the exact attempt and prepared plan explain that hold.
			if state.HoldOperationID == nil || *state.HoldOperationID != op.ID || !storageAdmissionMatches(admission, in.Authority, in.PlanHash, op.Snapshot.Storage) {
				return result, ErrConflict
			}
			storage, e := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
			if e != nil {
				return result, fmt.Errorf("read credential storage identity: %w", e)
			}
			origin, e := readHostingStorageMaterialization(ctx, tx, storage)
			if e != nil || origin.CreationOperationID != op.Plan.Storage.CreationOperationID || origin.CreationNonce != op.Plan.Storage.CreationNonce || origin.DaemonID != op.Release.DaemonID {
				return result, ErrConflict
			}
		}
	}
	if err = checkDatabaseDeliverySecrets(ctx, q, op); err != nil {
		return result, fmt.Errorf("validate credential references: %w", err)
	}
	if err = q.LockHostingDatabaseDelivery(ctx, in.DeliveryID); err != nil {
		return result, fmt.Errorf("lock credential delivery identity: %w", err)
	}
	expected := databaseDeliveryAudit{ProjectID: op.ProjectID, ResourceID: op.ResourceID, Metadata: hostingdatabaseprotocol.Metadata{RunnerID: runner, Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: op.Snapshot.Database, RequestHash: hostingdatabaseprotocol.RequestHash(runner, in), Purpose: hostingdatabaseprotocol.Purpose, References: slices.Clone(op.Plan.Database.Credentials)}}
	auditID := "aud_database_delivery_" + in.DeliveryID
	old, lookupErr := q.GetHostingDatabaseDeliveryAudit(ctx, auditID)
	if lookupErr == nil {
		var previous databaseDeliveryAudit
		if old.Action != databaseDeliveryAction || old.ActorID != runner || old.TargetID != op.ID || json.Unmarshal(old.Metadata, &previous) != nil {
			return result, ErrConflict
		}
		expected.Metadata.UseUntil = previous.Metadata.UseUntil
		if domain.HostingDocumentHash(expected) != domain.HostingDocumentHash(previous) {
			return result, ErrConflict
		}
	} else if errors.Is(lookupErr, pgx.ErrNoRows) && original == nil {
		window, e := q.HostingDatabaseDeliveryWindow(ctx, in.Authority.LeaseID)
		if e != nil {
			return result, fmt.Errorf("read credential delivery lease deadline: %w", e)
		}
		expected.Metadata.UseUntil = window.DatabaseNow.Add(time.Minute)
		if window.ExpiresAt.Before(expected.Metadata.UseUntil) {
			expected.Metadata.UseUntil = window.ExpiresAt
		}
		var metadata map[string]any
		if err = json.Unmarshal(buildJSON(expected), &metadata); err != nil {
			return result, fmt.Errorf("encode credential audit metadata: %w", err)
		}
		if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: auditID, ActorID: runner, Action: databaseDeliveryAction, TargetID: op.ID, CreatedAt: window.DatabaseNow, Metadata: metadata}); err != nil {
			return result, fmt.Errorf("audit database credential authorization: %w", err)
		}
	} else {
		return result, ErrConflict
	}
	if original != nil && domain.HostingDocumentHash(*original) != domain.HostingDocumentHash(expected.Metadata) {
		return result, ErrConflict
	}
	result = HostingDatabaseCredentialAuthorization{ProjectID: op.ProjectID, Metadata: expected.Metadata}
	if original == nil {
		result.Records = make(map[domain.HostingDatabaseCredentialRef]ManagedSecretRecord, len(expected.Metadata.References))
		for _, ref := range expected.Metadata.References {
			record, e := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: ref.SecretID, Version: ref.Version})
			if e != nil {
				return result, fmt.Errorf("read scoped encrypted credential: %w", e)
			}
			result.Records[ref] = ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: record.SecretID, Version: record.Version, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}
		}
	}
	// Secret and audit locks may wait. Recheck all time-dependent authority after
	// those waits; retained row locks prevent subsequent credential replacement.
	if err = finalDatabaseAuthority(ctx, tx, runner, in.Authority, op, observe, false); err != nil {
		return result, fmt.Errorf("confirm final credential authority: %w", err)
	}
	if _, err = q.CheckHostingDatabaseDeliveryCredential(ctx, sqlcgen.CheckHostingDatabaseDeliveryCredentialParams{RunnerID: runner, CredentialHash: credentialHash}); err != nil {
		return result, ErrConflict
	}
	now, e := q.HostingDatabaseDeliveryNow(ctx)
	if e != nil {
		return result, fmt.Errorf("check credential delivery database time: %w", e)
	}
	if !now.Before(expected.Metadata.UseUntil) {
		return result, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit database credential authorization: %w", err)
	}
	return result, nil
}

func checkDatabaseDeliverySecrets(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) error {
	refs := slices.Clone(op.Plan.Database.Credentials)
	slices.SortFunc(refs, func(a, b domain.HostingDatabaseCredentialRef) int { return strings.Compare(a.SecretID, b.SecretID) })
	for _, ref := range refs {
		if _, err := q.CheckHostingDatabaseDeliverySecret(ctx, sqlcgen.CheckHostingDatabaseDeliverySecretParams{SecretID: ref.SecretID, ProjectID: op.ProjectID, Version: ref.Version, ResourceID: op.ResourceID, Role: ref.Role, Username: ref.Username}); err != nil {
			return fmt.Errorf("check pinned database credential: %w", hostingError(err))
		}
	}
	return nil
}
