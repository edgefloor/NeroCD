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
		rows, err := tx.Query(ctx, `SELECT id FROM server_bootstrap_operations WHERE project_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, project, limit, offset)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
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
		rows, e := tx.Query(ctx, `SELECT o.id FROM server_bootstrap_operations o WHERE NOT o.restore_held AND o.created_at + interval '1 hour'>clock_timestamp() AND ((o.state='installed_waiting_enrollment' OR o.state='registered' AND EXISTS(SELECT 1 FROM server_bootstrap_commands g WHERE g.operation_id=o.id AND g.action='install' AND g.body->>'grant_project'='true')) OR EXISTS(SELECT 1 FROM server_bootstrap_commands c WHERE c.operation_id=o.id AND c.receipt IS NULL AND c.deadline>clock_timestamp() AND c.attempt<8 AND (c.lease_expires_at IS NULL OR c.lease_expires_at<=clock_timestamp()))) ORDER BY md5(o.id || floor(extract(epoch FROM clock_timestamp())/5)::text) LIMIT 64`)
		if e != nil {
			return false, e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return false, e
			}
			ids = append(ids, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
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
	var intent bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM server_bootstrap_commands WHERE operation_id=$1 AND action='install' AND effect_intent AND (lease_expires_at<=clock_timestamp() OR receipt IS NOT NULL))`, op.IDs.OperationID).Scan(&intent)
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
	_, e = tx.Exec(ctx, `UPDATE server_bootstrap_commands SET receipt=jsonb_build_object('receipt',$2::jsonb || jsonb_build_object('command_id',id,'lease_id',lease_id,'attempt',attempt,'fence',fence),'operation',$3::jsonb) WHERE operation_id=$1 AND action='install' AND effect_intent AND receipt IS NULL AND lease_expires_at<=clock_timestamp()`, op.IDs.OperationID, receipt, operation)
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
	var consumed bool
	e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.id=$1 AND e.runner_id=$2 AND e.used_at IS NOT NULL AND e.consume_request_id IS NOT NULL AND e.credential_hash=r.token_hash AND r.status='active')`, op.IDs.EnrollmentID, op.IDs.RunnerID).Scan(&consumed)
	return consumed, e
}
