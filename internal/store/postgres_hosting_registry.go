package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

// HostingRegistryAuthorization returns encrypted storage only after audit commit.
type HostingRegistryAuthorization struct {
	ProjectID  string
	Ref        domain.HostingRegistryCredentialRef
	Registries []string
	UseUntil   time.Time
	Record     ManagedSecretRecord
}

// Close clears the owned encrypted record.
func (a *HostingRegistryAuthorization) Close() {
	clear(a.Record.Envelope.Data)
	*a = HostingRegistryAuthorization{}
}

// MarshalJSON excludes authorization and encrypted storage from metadata.
func (HostingRegistryAuthorization) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// HostingRegistryRepository authorizes only a current pre-pull attempt.
type HostingRegistryRepository interface {
	AuthorizeHostingRegistryCredential(context.Context, string, string, domain.HostingRegistryCredentialRequest) (HostingRegistryAuthorization, error)
}

func requireHostingRegistryAgent(ctx context.Context, tx pgx.Tx, s domain.HostingSnapshot) error {
	if s.RegistryCredential == (domain.HostingRegistryCredentialRef{}) {
		return nil
	}
	if err := requireHostingReleaseAgent(ctx, tx, s.RunnerID); err != nil {
		return err
	}
	if domain.ValidateHostingRegistrySnapshot(s) != nil {
		return ErrConflict
	}
	capable, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: s.RunnerID, Capability: domain.HostingRegistryPullCapability})
	if err != nil {
		return err
	}
	if !capable {
		return ErrConflict
	}
	return nil
}
func validateHostingRegistrySecret(ctx context.Context, tx pgx.Tx, project string, s domain.HostingSnapshot) error {
	if s.RegistryCredential == (domain.HostingRegistryCredentialRef{}) {
		return nil
	}
	return validateRuntimeReferences(ctx, sqlcgen.New(tx), project, []domain.HostingRuntimeSecretReference{{SecretID: s.RegistryCredential.SecretID, Version: s.RegistryCredential.Version}})
}

// AuthorizeHostingRegistryCredential commits an immutable bounded audit window before decryption.
func (s *PostgresStore) AuthorizeHostingRegistryCredential(ctx context.Context, runner, credentialHash string, in domain.HostingRegistryCredentialRequest) (result HostingRegistryAuthorization, err error) {
	if runner == "" || credentialHash == "" || in.Validate() != nil {
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
	if op.Snapshot.RegistryCredential.Validate() != nil || domain.ValidateHostingRegistrySnapshot(op.Snapshot) != nil || op.State != "preparing" && op.State != "dispatched" || op.MutationHold || op.Release != nil || op.PreparedReceipt != nil || op.CompleteReceipt != nil || op.CleanupPlan != nil || op.StackStopPlan != nil || op.Snapshot.RunnerID != runner {
		return result, ErrConflict
	}
	if err = hostingRequireMode(ctx, tx, in.Authority, "deploy"); err != nil {
		return result, err
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
		return result, err
	}
	q := sqlcgen.New(tx)
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return result, err
	}
	if _, err = q.CheckHostingRuntimeEnvironmentCredential(ctx, sqlcgen.CheckHostingRuntimeEnvironmentCredentialParams{RunnerID: runner, CredentialHash: credentialHash}); err != nil {
		return result, ErrConflict
	}
	r, err := q.LockHostingRuntimeResource(ctx, op.ResourceID)
	if err != nil {
		return result, err
	}
	if r.Generation != op.Snapshot.ResourceGeneration || r.ProjectID != op.ProjectID {
		return result, ErrConflict
	}
	if err = q.LockHostingRuntimeEnvironmentDelivery(ctx, "registry:"+in.DeliveryID); err != nil {
		return result, err
	}
	hash := domain.HostingDocumentHash(struct {
		Runner   string
		Request  domain.HostingRegistryCredentialRequest
		Snapshot domain.HostingSnapshot
	}{runner, in, op.Snapshot})
	type audit struct {
		Purpose     string    `json:"purpose"`
		RequestHash string    `json:"request_hash"`
		UseUntil    time.Time `json:"use_until"`
	}
	expected := audit{Purpose: "registry_pull", RequestHash: hash}
	id := "aud_registry_delivery_" + in.DeliveryID
	old, e := q.GetHostingRuntimeEnvironmentDeliveryAudit(ctx, id)
	if e == nil {
		var a audit
		if old.ActorID != runner || old.TargetID != op.ID || old.Action != "hosting.registry_credential_authorized" || json.Unmarshal(old.Metadata, &a) != nil || a.Purpose != expected.Purpose || a.RequestHash != hash {
			return result, ErrConflict
		}
		expected.UseUntil = a.UseUntil
	} else if errors.Is(e, pgx.ErrNoRows) {
		w, e := q.HostingRuntimeEnvironmentDeliveryWindow(ctx, in.Authority.LeaseID)
		if e != nil {
			return result, e
		}
		expected.UseUntil = w.DatabaseNow.Add(time.Minute)
		if w.ExpiresAt.Before(expected.UseUntil) {
			expected.UseUntil = w.ExpiresAt
		}
		var metadata map[string]any
		if err = json.Unmarshal(buildJSON(expected), &metadata); err != nil {
			return result, err
		}
		if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: id, ActorID: runner, Action: "hosting.registry_credential_authorized", TargetID: op.ID, CreatedAt: w.DatabaseNow, Metadata: metadata}); err != nil {
			return result, err
		}
	} else {
		return result, e
	}
	ref := op.Snapshot.RegistryCredential
	record, err := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: ref.SecretID, Version: ref.Version})
	if err != nil {
		return result, err
	}
	if err = validateHostingRegistrySecret(ctx, tx, op.ProjectID, op.Snapshot); err != nil {
		return result, err
	}
	if err = runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return result, err
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return result, err
	}
	if err = hostingAuthorityExists(ctx, tx, runner, in.Authority, true); err != nil {
		return result, err
	}
	if _, err = q.CheckHostingRuntimeEnvironmentCredential(ctx, sqlcgen.CheckHostingRuntimeEnvironmentCredentialParams{RunnerID: runner, CredentialHash: credentialHash}); err != nil {
		return result, ErrConflict
	}
	now, e := q.HostingRuntimeEnvironmentDeliveryNow(ctx)
	if e != nil {
		return result, e
	}
	if !now.Before(expected.UseUntil) {
		return result, ErrConflict
	}
	var registries []string
	if op.Snapshot.Stack != (domain.HostingStackRef{}) {
		// hostingOperation loaded the immutable source by generation and verified its hash.
		if op.StackSource == nil {
			return result, ErrConflict
		}
		for _, service := range op.StackSource.Services {
			registry, e := domain.HostingImageRegistry(service.Image)
			if e != nil {
				return result, ErrConflict
			}
			if !slices.Contains(registries, registry) {
				registries = append(registries, registry)
			}
		}
	} else {
		registry, e := domain.HostingImageRegistry(op.Snapshot.ImageReference)
		if e != nil {
			return result, ErrConflict
		}
		registries = []string{registry}
	}
	result = HostingRegistryAuthorization{ProjectID: op.ProjectID, Ref: ref, Registries: registries, UseUntil: expected.UseUntil, Record: ManagedSecretRecord{Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}}
	err = tx.Commit(ctx)
	return result, err
}

// Format redacts all authorization storage.
func (HostingRegistryAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted registry authorization]")
}
