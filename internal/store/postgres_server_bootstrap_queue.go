package store

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/serverbootstrap"
	"nerocd/internal/store/sqlcgen"
)

// ConfigureServerBootstrapProcessor freezes the processor before queue startup.
func (s *PostgresStore) ConfigureServerBootstrapProcessor(handler func(context.Context, string) error) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	s.serverBootstrapHandler = handler
	return nil
}

// PrepareServerBootstrap delegates only a durable operation identifier.
func (s *PostgresStore) PrepareServerBootstrap(ctx context.Context, id string) error {
	if s.serverBootstrapHandler == nil {
		return nil
	}
	return s.serverBootstrapHandler(ctx, id)
}
func (h deploymentOnlyHandler) PrepareServerBootstrap(ctx context.Context, id string) error {
	return h.store.PrepareServerBootstrap(ctx, id)
}

// ServerBootstrapActor resolves a persisted actor; it grants no delivery authority.
func (s *PostgresStore) ServerBootstrapActor(ctx context.Context, id string) (string, error) {
	row, err := s.queries.BootstrapGet(ctx, id)
	return row.ActorID, hostingError(err)
}

// ListServerBootstraps bounds materialization and rechecks project ownership.
func (s *PostgresStore) ListServerBootstraps(ctx context.Context, actor, project string, limit, offset int) ([]domain.ServerBootstrapOperation, error) {
	if limit < 1 || limit > 101 || offset < 0 || offset > 100000 {
		return nil, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.ServerBootstrapOperation, error) {
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, true); err != nil {
			return nil, err
		}
		ids, err := sqlcgen.New(tx).BootstrapListIDs(ctx, sqlcgen.BootstrapListIDsParams{ProjectID: project, Limit: int32(limit), Offset: int32(offset)})
		if err != nil {
			return nil, err
		}
		out := make([]domain.ServerBootstrapOperation, 0, len(ids))
		q := sqlcgen.New(tx)
		for _, id := range ids {
			row, e := q.BootstrapReadLock(ctx, id)
			if e != nil {
				return nil, e
			}
			op, e := bootstrapOperation(row)
			if e != nil {
				return nil, e
			}
			if e = bootstrapProjection(ctx, q, &op); e != nil {
				return nil, e
			}
			out = append(out, op)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// Recovery uses the existing hosting loop, independently of finite River retention.
func (s *PostgresStore) recoverServerBootstrapDispatch(ctx context.Context) error {
	if s.serverBootstrapHandler == nil || s.hostingJobs == nil {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, e := sqlcgen.New(tx).BootstrapDueOperations(ctx)
		if e != nil {
			return false, e
		}
		for _, id := range ids {
			if _, e = s.hostingJobs.EnqueueServerBootstrapTx(ctx, tx, hostingjobs.ServerBootstrapArgs{OperationID: id}); e != nil {
				return false, e
			}
		}
		return true, nil
	})
	return err
}

func bootstrapTrustedReview(ctx context.Context, q *sqlcgen.Queries, op domain.ServerBootstrapOperation, in domain.ServerBootstrapCommand) error {
	row, e := q.BootstrapCommandLock(ctx, sqlcgen.BootstrapCommandLockParams{OperationID: op.IDs.OperationID, ID: in.ProbeReceiptID})
	if e != nil {
		return hostingError(e)
	}
	if row.Action != "probe" || row.Receipt == nil {
		return ErrConflict
	}
	var stored struct {
		Receipt domain.ServerBootstrapReceipt `json:"receipt"`
	}
	if json.Unmarshal(row.Receipt, &stored) != nil || stored.Receipt.Probe == nil || stored.Receipt.Outcome != "observed" {
		return ErrConflict
	}
	p := stored.Receipt.Probe
	r := in.Review
	if r == nil || r.Prerequisites != p.Prerequisites || !r.ProbeObservedAt.Equal(p.ObservedAt) || !r.ProbeExpiresAt.Equal(p.ExpiresAt) {
		return ErrConflict
	}
	m := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: op.Input.ControllerURL, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
	canonical, hash, err := serverbootstrap.CanonicalManifest(m)
	if err != nil || hash != r.ManifestSHA256 || !bytes.Equal(canonical, r.Manifest) {
		return ErrConflict
	}
	return nil
}

func bootstrapReconcileOwned(ctx context.Context, tx pgx.Tx, op domain.ServerBootstrapOperation, owned domain.ServerBootstrapOwnedReceipt) error {
	q := sqlcgen.New(tx)
	raw, e := q.BootstrapLatestReview(ctx, op.IDs.OperationID)
	if e != nil {
		return e
	}
	var review domain.ServerBootstrapCommand
	if json.Unmarshal(raw, &review) != nil || review.Review == nil || owned.InstallationID != op.IDs.InstallationID || owned.OperationID != op.IDs.InstallOperationID || owned.ManifestSHA256 != review.Review.ManifestSHA256 || owned.State != "installed" || !owned.ArtifactMatches || owned.ServiceJobPending || !owned.ServiceActive {
		return ErrConflict
	}
	intent, e := q.BootstrapHasSettledIntent(ctx, op.IDs.OperationID)
	if e != nil {
		return e
	}
	if !intent {
		return ErrConflict
	}
	// The exact owned helper receipt closes only the original durable install intent.
	// Its old lease is never renewed and retained remote ownership is never cleared.
	receipt, _ := json.Marshal(domain.ServerBootstrapReceipt{Outcome: "installed", ManifestSHA256: owned.ManifestSHA256, RemoteOperationID: owned.OperationID, Owned: &owned})
	accepted := op
	accepted.State = "installed_waiting_enrollment"
	accepted.Version++
	operation, err := json.Marshal(accepted)
	if err != nil {
		return err
	}
	e = q.BootstrapReconcileIntent(ctx, sqlcgen.BootstrapReconcileIntentParams{Receipt: receipt, Operation: operation, OperationID: op.IDs.OperationID})
	return e
}

func bootstrapOwnedMatches(op domain.ServerBootstrapOperation, hash string, owned *domain.ServerBootstrapOwnedReceipt) bool {
	return owned != nil && owned.InstallationID == op.IDs.InstallationID && owned.OperationID == op.IDs.InstallOperationID && owned.ManifestSHA256 == hash && owned.State == "installed" && owned.ArtifactMatches && owned.ServiceActive && !owned.ServiceJobPending
}

// ServerBootstrapEnrollmentConsumed is independent of SSH and install command state.
// Registration still revalidates the credential binding transactionally at completion.
func (s *PostgresStore) ServerBootstrapEnrollmentConsumed(ctx context.Context, actor, id string) (bool, error) {
	op, e := s.GetServerBootstrap(ctx, actor, id)
	if e != nil {
		return false, e
	}
	return s.queries.BootstrapEnrollmentConsumed(ctx, sqlcgen.BootstrapEnrollmentConsumedParams{ID: op.IDs.EnrollmentID, RunnerID: op.IDs.RunnerID})
}
