package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func connectionAwaitWait(t *testing.T, ctx context.Context, f connectionFixture, blockerPID int, done chan error, until time.Time) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting, past bool
		if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))),clock_timestamp()>$2`, blockerPID, until).Scan(&waiting, &past); err != nil {
			t.Fatal("wait observation", err)
		}
		if waiting && past {
			return
		}
		select {
		case err := <-done:
			done <- err
			t.Fatal("call escaped required barrier", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("wait deadline", ctx.Err())
		}
	}
}

func TestPostgresDatabaseConnectionCurrentAuthorityAfterPlacementWait(t *testing.T) {
	for _, fault := range []string{"credential", "enrollment", "capability", "owner", "grant", "healthy_source"} {
		t.Run(fault, func(t *testing.T) {
			f := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t)
			// Demonstrate a valid permission before injecting the mutable fault.
			f.authorize(t, "create")
			ctx, cancel := context.WithTimeout(f.runnerContext, 15*time.Second)
			defer cancel()
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started, joined := false, false
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error("blocker cleanup", e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("authority call failed to join")
					}
				}
			}()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.appOperation.Snapshot.DestinationID); err != nil {
				t.Fatal(err)
			}
			before := connectionCounts(t, f)
			started = true
			go func() {
				_, e := f.pg.AuthorizeHostingApplicationDatabaseEffect(ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: domain.HostingDocumentHash(fault)[7:39], Action: "create"})
				done <- e
			}()
			connectionAwaitWait(t, ctx, f, pid, done, time.Time{})
			switch fault {
			case "credential":
				_, err = f.db.Exec(ctx, `UPDATE runners SET token_hash=repeat('a',64) WHERE id=$1`, f.runner)
			case "enrollment":
				_, err = f.db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner)
			case "capability":
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingApplicationDatabaseCapability)
			case "owner":
				_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
			case "grant":
				_, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, f.appOperation.Snapshot.DestinationID, f.application.ProjectID)
			case "healthy_source":
				rows, e := f.db.Exec(ctx, `UPDATE hosting_binding_state SET healthy=NULL WHERE binding_id=$1 AND healthy->>'operation_id'=$2`, f.appOperation.ApplicationDatabase.Binding.DatabaseBindingID, f.op.ID)
				err = e
				if err == nil && rows.RowsAffected() != 1 {
					t.Fatal("source-loss fault did not affect exact current source")
				}
			}
			if err != nil {
				t.Fatal("revocation fixture", err)
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err == nil || connectionCounts(t, f) != before {
				t.Fatal("revocation after wait accepted authority or partial records", err)
			}
		})
	}
}

func TestPostgresDatabaseConnectionClaimFinalActorClockAfterAuditWait(t *testing.T) {
	f := newConnectionFixture(t, "redis")
	actor, _ := proxyLegacyToken(t, f.pg, f.service, "tok_connection_claim_wait", []string{domain.RoleSystemAdmin})
	f.actor = actor.ID
	f = f.connect(t)
	ctx, cancel := context.WithTimeout(f.runnerContext, 15*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	started, joined := false, false
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Error("claim blocker cleanup", e)
		}
		if started && !joined {
			select {
			case <-done:
			case <-cleanup.Done():
				t.Error("claim call did not join")
			}
		}
	}()
	var key int64
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':connection-claim-wait',0),pg_backend_pid()`).Scan(&key, &pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION connection_claim_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='aud_connection_claim_wait' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':connection-claim-wait',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER connection_claim_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION connection_claim_wait()`); err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, actor.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	before := connectionCounts(t, f)
	started = true
	go func() {
		event := destinationEvent("connection_claim_wait")
		event.ID = "aud_connection_claim_wait"
		event.ActorID = f.runner
		_, e := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute, store.WithAudit(event))
		done <- e
	}()
	connectionAwaitWait(t, ctx, f, pid, done, expiry)
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err == nil || connectionCounts(t, f) != before {
		t.Fatal("expired actor gained claim authority after audit wait", err)
	}
}

func TestPostgresDatabaseConnectionEffectExpiryAndHashBinding(t *testing.T) {
	f := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t)
	if _, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, "", domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: strings.Repeat("1", 32), Action: "create"}); err == nil {
		t.Fatal("missing explicitly authenticated fingerprint accepted")
	}
	permission := f.authorize(t, "create")
	f = f.sendStage(t, "candidate_created")
	if _, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: permission.AuthorizationID, Action: "create"}); err == nil {
		t.Fatal("historical create permission revived after stage advance")
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.appAuthority.LeaseID); err != nil {
		t.Fatal(err)
	}
	before := connectionCounts(t, f)
	if _, err := f.pg.HostingApplicationDatabaseStage(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: f.appAuthority, Stage: f.stage}); err != nil {
		t.Fatal("exact expired stage receipt unavailable", err)
	}
	if _, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: strings.Repeat("2", 32), Action: "configure", ExpectedStageHash: f.stage.Hash}); err == nil || connectionCounts(t, f) != before {
		t.Fatal("expired historical ACK supplied fresh authority", err)
	}
}

func TestPostgresDatabaseConnectionFreshObserverCannotRenewOriginalEffects(t *testing.T) {
	f := newConnectionFixture(t, "redis")
	actor, _ := proxyLegacyToken(t, f.pg, f.service, "tok_connection_original_owner", []string{domain.RoleSystemAdmin})
	f.actor = actor.ID
	f = f.connect(t).prepareApp(t).sendStage(t, "candidate_created")
	if _, err := f.db.Exec(f.ctx, `UPDATE api_tokens SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.appAuthority.LeaseID); err != nil {
		t.Fatal(err)
	}
	status := domain.HostingStatusRequest{HostingAuthority: f.appAuthority}
	if _, err := f.pg.HostingStatus(f.runnerContext, f.runner, status); err == nil {
		t.Fatal("revoked creator borrowed absent observation authority")
	}
	in := domain.HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: f.appOperation.ID, StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 1, IdempotencyKey: "fresh_owner_observe"}
	op, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in)
	if err != nil {
		t.Fatal("current owner observation", err)
	}
	before := connectionCounts(t, f)
	if read, e := f.pg.HostingStatus(f.runnerContext, f.runner, status); e != nil || !read.ReconcileRequested || !read.MutationHold || connectionCounts(t, f) != before {
		t.Fatal("current observer could not inspect original held tuple", e)
	}
	claimed, err := f.pg.ClaimHostingObservation(f.runnerContext, f.runner, f.appAuthority, time.Minute)
	if err != nil || claimed.ObservationLease == nil || claimed.TaskMode != "observe" || claimed.ReconciliationRunID != "observe_"+op.ID {
		t.Fatal("startup observer claim", err)
	}
	lease := *claimed.ObservationLease
	before = connectionCounts(t, f)
	if replay, e := f.pg.ClaimHostingObservation(f.runnerContext, f.runner, f.appAuthority, time.Minute); e != nil || replay.ObservationLease == nil || !reflect.DeepEqual(*replay.ObservationLease, lease) || connectionCounts(t, f) != before {
		t.Fatal("observer response loss renewed its lease", e)
	}
	wrong := status
	wrong.Fence = "foreign_fence"
	if _, err = f.pg.HostingStatus(f.runnerContext, f.runner, wrong); err == nil {
		t.Fatal("foreign original tuple borrowed observer")
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, a); err != nil {
		t.Fatal("observer dispatch plan", err)
	}
	state, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, a)
	if err != nil || state.Stage == nil || state.Stage.Hash != f.stage.Hash || state.HoldOperationID != op.ID || state.MembershipRevision != 1 {
		t.Fatal("observer original state correlation", err)
	}
	for _, authority := range []domain.HostingAuthority{f.appAuthority, a} {
		if _, err = f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: authority, AuthorizationID: strings.Repeat("5", 32), Action: "configure", ExpectedStageHash: f.stage.Hash}); err == nil {
			t.Fatal("observer authority renewed original effects")
		}
	}
	stale, err := store.BindHostingDatabaseConnectionRunnerContext(t.Context(), f.runner, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ClaimHostingObservation(stale, f.runner, f.appAuthority, time.Minute); err == nil || connectionCounts(t, f) != before {
		t.Fatal("stale bearer claimed observation or left partial authority", err)
	}
	observation := domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: op.Release.DaemonID, PlanHash: op.Plan.Hash, ReleaseHash: op.Release.Hash, NoOwnedContainers: true}
	if _, err = f.pg.HostingReconciliation(f.runnerContext, f.runner, observation); err != nil {
		t.Fatal("fresh observer bounded evidence", err)
	}
	after, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, a)
	if err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("observation settled original stage/head/hold", err)
	}
	before = connectionCounts(t, f)
	if _, err = f.pg.HostingReconciliation(f.runnerContext, f.runner, observation); err != nil || connectionCounts(t, f) != before {
		t.Fatal("exact observation receipt replay changed authority", err)
	}
	if _, err = f.pg.HostingStatus(f.runnerContext, f.runner, status); err == nil {
		t.Fatal("completed observer lent authority to revoked original creator")
	}
	if _, err = f.pg.ClaimHostingObservation(f.runnerContext, f.runner, f.appAuthority, time.Minute); err == nil || connectionCounts(t, f) != before {
		t.Fatal("completed observer reset or requeued", err)
	}
}

func TestPostgresDatabaseConnectionExpiredObservationCannotReset(t *testing.T) {
	f := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t).sendStage(t, "candidate_created")
	in := domain.HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: f.appOperation.ID, StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 1, IdempotencyKey: "observe_expiry"}
	if _, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.pg.ClaimHostingObservation(f.runnerContext, f.runner, f.appAuthority, time.Minute)
	if err != nil || claimed.ObservationLease == nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claimed.ObservationLease.ID); err != nil {
		t.Fatal(err)
	}
	before := connectionCounts(t, f)
	if _, err = f.pg.ClaimHostingObservation(f.runnerContext, f.runner, f.appAuthority, time.Minute); err == nil || connectionCounts(t, f) != before {
		t.Fatal("expired observation reset its task or lease", err)
	}
	if _, err = f.pg.HostingStatus(f.runnerContext, f.runner, domain.HostingStatusRequest{HostingAuthority: f.appAuthority}); err == nil {
		t.Fatal("expired observer supplied original status authority")
	}
	var status string
	var attempts int
	if err = f.db.QueryRow(f.ctx, `SELECT status,next_attempt-1 FROM task_runs WHERE id=$1`, claimed.ReconciliationRunID).Scan(&status, &attempts); err != nil || status != domain.RunRunning || attempts != int(claimed.ObservationLease.Attempt) {
		t.Fatal("expired observation changed task identity", err)
	}
}

func TestPostgresDatabaseConnectionStorageAuthorityAfterWait(t *testing.T) {
	for _, phase := range []string{"create_begin", "materialized", "materialized_ack"} {
		t.Run(phase, func(t *testing.T) {
			f := newConnectionFixture(t, "redis").connect(t).prepareApp(t).ready(t).complete(t)
			op, err := f.service.RequestHostingDatabaseMaintenance(f.ctx, f.resource.ID, domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: 2, IdempotencyKey: "storage_wait"})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
				t.Fatal("storage maintenance claim", err)
			}
			a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, a); err != nil {
				t.Fatal(err)
			}
			op, err = f.pg.HostingPrepared(f.runnerContext, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "storage_wait_prepared", Release: op.DatabaseMaintenance.PreviousPlan.Release})
			if err != nil {
				t.Fatal(err)
			}
			mf := f.networkCaptureFixture
			mf.op, mf.authority, mf.ctx = op, a, f.runnerContext
			// A real successful admission establishes valid setup before revocation.
			ack, err := f.pg.HostingStorageCreateBegin(f.runnerContext, f.runner, mf.requestStorage())
			if err != nil || ack.CreatePermitted {
				t.Fatal("maintenance storage admission", err)
			}
			proof := mf.proof(ack)
			proof.ReceiptID = "storage_wait_materialized"
			if phase == "materialized_ack" {
				if _, err = f.pg.HostingStorageMaterialized(f.runnerContext, f.runner, proof); err != nil {
					t.Fatal("valid storage receipt before fault", err)
				}
			}
			ctx, cancel := context.WithTimeout(f.runnerContext, 15*time.Second)
			defer cancel()
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started, joined := false, false
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error("storage blocker cleanup", e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("storage authority call did not join")
					}
				}
			}()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, op.Snapshot.DestinationID); err != nil {
				t.Fatal(err)
			}
			const counts = `SELECT jsonb_build_array((SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT count(*) FROM hosting_storage_materialized_receipts),(SELECT count(*) FROM audit_events))::text`
			var before, after string
			if err = f.db.QueryRow(ctx, counts).Scan(&before); err != nil {
				t.Fatal(err)
			}
			started = true
			go func() {
				var e error
				if phase == "create_begin" {
					_, e = f.pg.HostingStorageCreateBegin(ctx, f.runner, mf.requestStorage())
				} else {
					_, e = f.pg.HostingStorageMaterialized(ctx, f.runner, proof)
				}
				done <- e
			}()
			connectionAwaitWait(t, ctx, f, pid, done, time.Time{})
			if phase == "materialized_ack" {
				_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
			} else {
				_, err = f.db.Exec(ctx, `UPDATE runners SET token_hash=repeat('a',64) WHERE id=$1`, f.runner)
			}
			if err != nil {
				t.Fatal("storage authority revocation", err)
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err == nil {
				t.Fatal("storage authority survived revocation while waiting")
			}
			if err = f.db.QueryRow(ctx, counts).Scan(&after); err != nil || before != after {
				t.Fatal("denied storage call left admission/receipt/audit", err)
			}
		})
	}
}
