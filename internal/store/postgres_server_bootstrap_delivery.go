package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

// AuthorizeServerBootstrapDelivery persists enrollment and remote intent together.
// Returned secrets have no JSON representation and are never stored in public evidence.
func (s *PostgresStore) AuthorizeServerBootstrapDelivery(ctx context.Context, actor, id string, in domain.ServerBootstrapStage, ring *secretcrypto.Keyring, event domain.AuditEvent) (ServerBootstrapDelivery, error) {
	var zero ServerBootstrapDelivery
	if ring == nil || event.ActorID != actor {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (ServerBootstrapDelivery, error) {
		op, err := bootstrapLock(ctx, tx, actor, id)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		row, err := q.BootstrapCommandLock(ctx, sqlcgen.BootstrapCommandLockParams{OperationID: id, ID: in.Command.ID})
		if err != nil {
			return zero, hostingError(err)
		}
		now, err := q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if op.RestoreHeld || row.ActorID != actor || !bootstrapStageMatches(row, in, now) || row.Action != "install" && row.Action != "probe" && row.Action != "inspect_owned" {
			return zero, ErrConflict
		}
		if row.Action == "install" {
			if actor != op.CreatedBy || op.State != "reviewed" && (op.State != "running" || !row.EffectIntent) {
				return zero, ErrConflict
			}
			if _, err = bootstrapReview(ctx, q, op, now); err != nil {
				return zero, err
			}
		}
		grant, err := q.BootstrapGrantLock(ctx, id)
		if err != nil {
			return zero, hostingError(err)
		}
		if grant.PrincipalID != actor || grant.Purpose != "ssh_bootstrap" || grant.TargetHash != domain.HostingDocumentHash(op.Input.Target) || grant.SecretID != op.Input.SSHSecretID || grant.SecretVersion != op.Input.SSHSecretVersion || grant.RevokedAt != nil || !grant.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		secret, err := q.LockManagedSecret(ctx, grant.SecretID)
		if err != nil {
			return zero, hostingError(err)
		}
		if secret.ProjectID != op.Input.ProjectID || secret.Status != "active" {
			return zero, ErrConflict
		}
		record, err := q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: grant.SecretID, Version: grant.SecretVersion})
		if err != nil {
			return zero, hostingError(err)
		}
		key, err := ring.Open(secretcrypto.Binding{ProjectID: op.Input.ProjectID, SecretID: grant.SecretID, Version: grant.SecretVersion}, secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext})
		if err != nil {
			return zero, err
		}
		var token []byte
		if row.Action == "install" {
			binding, e := bootstrapEnrollmentBinding(op.Input.ProjectID, id, op.IDs.EnrollmentID)
			if e != nil {
				return zero, e
			}
			credential, e := q.BootstrapCredential(ctx, id)
			if errors.Is(e, pgx.ErrNoRows) {
				if row.EffectIntent {
					return zero, ErrConflict
				}
				random := make([]byte, 32)
				if _, err = rand.Read(random); err != nil {
					return zero, err
				}
				token = []byte("nce_" + hex.EncodeToString(random))
				digest := sha256.Sum256(token)
				envelope, e := ring.Seal(binding, token)
				if e != nil {
					return zero, e
				}
				enrollment := domain.RunnerEnrollment{ID: op.IDs.EnrollmentID, TokenHash: hex.EncodeToString(digest[:]), RunnerID: op.IDs.RunnerID, RunnerName: op.Input.Name, Tags: []string{}, Capabilities: domain.ServerBootstrapCapabilities(), CreatedBy: actor, CreatedAt: now, ExpiresAt: row.Deadline}
				enrollmentAudit := event
				enrollmentAudit.ID += ".enrollment"
				enrollmentAudit.Action = "runner.enrollment_created"
				enrollmentAudit.TargetID = op.IDs.EnrollmentID
				enrollmentAudit.Metadata = map[string]any{"operation_id": id, "runner_id": op.IDs.RunnerID}
				if _, err = createRunnerEnrollmentTx(ctx, q, enrollment, enrollmentAudit); err != nil {
					return zero, err
				}
				if err = q.BootstrapCredentialCreate(ctx, sqlcgen.BootstrapCredentialCreateParams{OperationID: id, EnrollmentID: op.IDs.EnrollmentID, EnvelopeVersion: int32(envelope.Version), KeyID: envelope.KeyID, Ciphertext: envelope.Data}); err != nil {
					return zero, err
				}
			} else if e != nil {
				return zero, e
			} else {
				if credential.EnrollmentID != op.IDs.EnrollmentID {
					return zero, ErrConflict
				}
				token, err = ring.Open(binding, secretcrypto.Envelope{Version: int(credential.EnvelopeVersion), KeyID: credential.KeyID, Data: credential.Ciphertext})
				if err != nil {
					return zero, err
				}
			}
			enrollment, err := q.BootstrapEnrollmentLock(ctx, op.IDs.EnrollmentID)
			if err != nil {
				return zero, err
			}
			if enrollment.UsedAt != nil || enrollment.RevokedAt != nil || !enrollment.ExpiresAt.After(now) {
				return zero, ErrConflict
			}
			if !row.EffectIntent {
				if err = q.BootstrapCommandIntent(ctx, row.ID); err != nil {
					return zero, err
				}
				updated, e := q.BootstrapUpdate(ctx, sqlcgen.BootstrapUpdateParams{ID: id, State: "running", RemoteHold: true})
				if e != nil {
					return zero, e
				}
				op, err = bootstrapOperation(updated)
				if err != nil {
					return zero, err
				}
				row.EffectIntent = true
				if err = bootstrapAudit(ctx, q, event, actor, "install_intent", id); err != nil {
					return zero, err
				}
			}
		}
		if err = bootstrapProjection(ctx, q, &op); err != nil {
			return zero, err
		}
		now, err = q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !bootstrapStageMatches(row, in, now) || !grant.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		if row.Action == "install" {
			if op.Review == nil || op.Review.Validate(op, now) != nil {
				return zero, ErrConflict
			}
		}
		if err = proxyCheckLegacyToken(ctx, tx, actor); err != nil {
			return zero, err
		}
		stage, err := bootstrapStage(row)
		if err != nil {
			return zero, err
		}
		return ServerBootstrapDelivery{Operation: op, Stage: stage, SSHPrivateKey: key, EnrollmentToken: token}, nil
	})
}
