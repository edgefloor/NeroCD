package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type observationFixture struct {
	pg          *store.PostgresStore
	db          *pgxpool.Pool
	resource    domain.Resource
	server      domain.Server
	destination domain.Destination
	token       string
}

func newObservationFixture(t *testing.T) observationFixture {
	t.Helper()
	pg, db, resource, server, destination := hostingExecutionFixture(t)
	f := observationFixture{pg: pg, db: db, resource: resource, server: server, destination: destination, token: fmt.Sprintf("%x", sha256.Sum256([]byte("image-hosting-fixture-token")))}
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingObservationCapability); err != nil {
		t.Fatal(err)
	}
	op, err := pg.RequestHostingDeployment(t.Context(), "hop_observed", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("observed_request"))
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	admitted, err := pg.HostingApplyBegin(t.Context(), server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.HostingComplete(t.Context(), server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "observation_initial_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "observed_container", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f observationFixture) target(t *testing.T) domain.HostingObservationTarget {
	t.Helper()
	page, err := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{Limit: 50})
	if err != nil || len(page.Targets) != 1 {
		t.Fatalf("targets %+v %v", page, err)
	}
	return page.Targets[0]
}
func observationBody(target domain.HostingObservationTarget) domain.HostingRuntimeObservation {
	return domain.HostingRuntimeObservation{BindingID: target.BindingID, SampleID: target.SampleID, SampleDurationMS: 1, ContainerState: "running", Readiness: "ready"}
}
func (f observationFixture) status(t *testing.T) domain.HostingRuntimeStatus {
	t.Helper()
	v, err := f.pg.HostingRuntimeStatus(t.Context(), "usr_bootstrap", f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f observationFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingObservationReceiptsFreshnessAndBoundedStorage(t *testing.T) {
	f := newObservationFixture(t)
	if v := f.status(t); v.Freshness != "unknown" || v.Reason != "never_observed" || v.LastObservation != nil {
		t.Fatalf("synthesized %+v", v)
	}
	target := f.target(t)
	if repeated := f.target(t); !reflect.DeepEqual(target, repeated) {
		t.Fatal("pending changed")
	}
	body := observationBody(target)
	ack, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body)
	if err != nil {
		t.Fatal(err)
	}
	if again, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); e != nil || again != ack {
		t.Fatalf("retry %+v %v", again, e)
	}
	body.Readiness = "not_ready"
	if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed retry", e)
	}
	body.Readiness = "ready"
	if v := f.status(t); v.Freshness != "fresh" || v.Readiness != "ready" || v.ObservedAt == nil || !v.ObservedAt.Equal(ack.ReceivedAt) || v.CurrentContainerID != target.ContainerID {
		t.Fatalf("fresh %+v", v)
	}
	f.exec(t, `UPDATE runners SET last_heartbeat_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, f.server.RunnerID)
	if v := f.status(t); v.Freshness != "fresh" || v.AgentReachability != "unknown" {
		t.Fatalf("heartbeat conflated %+v", v)
	}
	// Pending replacement and expiry do not erase accepted evidence.
	next := f.target(t)
	f.exec(t, `UPDATE hosting_runtime_observations SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb($1::text))`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano))
	if v := f.status(t); v.Freshness != "fresh" {
		t.Fatal(v)
	}
	if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(next)); !errors.Is(e, store.ErrConflict) {
		t.Fatal("expired accepted", e)
	}
	next = f.target(t)
	if next.SampleID == target.SampleID {
		t.Fatal("sample reused")
	}
	if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(next)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); !errors.Is(e, store.ErrConflict) {
		t.Fatal("evicted retry", e)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_runtime_observations`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	f.exec(t, `UPDATE hosting_runtime_observations SET accepted_ack=jsonb_set(accepted_ack,'{received_at}',to_jsonb($1::text))`, time.Now().Add(-31*time.Second).UTC().Format(time.RFC3339Nano))
	f.exec(t, `UPDATE runners SET last_heartbeat_at=clock_timestamp() WHERE id=$1`, f.server.RunnerID)
	if v := f.status(t); v.Freshness != "stale" || v.Reason != "stale" || v.Readiness != "unknown" || v.LastObservation == nil || v.AgentReachability != "reachable" {
		t.Fatalf("stale %+v", v)
	}
}

func TestPostgresHostingObservationCurrentAuthority(t *testing.T) {
	for _, mutation := range []struct{ name, sql string }{
		{"capability", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_observation_v1')`},
		{"token", `UPDATE runners SET token_hash=''`},
		{"enrollment", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE used_at IS NOT NULL`},
		{"grant", `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`},
		{"archive", `UPDATE projects SET archived_at=clock_timestamp() WHERE id='proj_platform'`},
		{"restore", `UPDATE hosting_binding_state SET restore_admitted=false`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newObservationFixture(t)
			target := f.target(t)
			body := observationBody(target)
			if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); e != nil {
				t.Fatal(e)
			}
			f.exec(t, mutation.sql)
			if _, e := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); e == nil {
				t.Fatal("revoked retry accepted")
			}
			page, e := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{Limit: 50})
			if e != nil || len(page.Targets) != 0 {
				t.Fatal(page, e)
			}
			if mutation.name != "archive" {
				v := f.status(t)
				if v.Freshness != "blocked" || v.ContainerState != "unknown" || v.LastObservation == nil {
					t.Fatal(v)
				}
			}
		})
	}
}

func TestPostgresHostingObservationTargetChangeAndMutationIsolation(t *testing.T) {
	f := newObservationFixture(t)
	target := f.target(t)
	body := observationBody(target)
	if _, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); err != nil {
		t.Fatal(err)
	}
	// A live mutation holds the previous settled target without authorizing recovery.
	f.exec(t, `UPDATE hosting_binding_state SET hold_operation_id='hop_observed'`)
	var before []byte
	if err := f.db.QueryRow(t.Context(), `SELECT to_jsonb(bs) FROM hosting_binding_state bs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	next := f.target(t)
	if next.OperationID != target.OperationID {
		t.Fatal(next)
	}
	if _, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(next)); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := f.db.QueryRow(t.Context(), `SELECT to_jsonb(bs) FROM hosting_binding_state bs`).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatal("mutation state changed", err)
	}
	old := f.target(t)
	f.exec(t, `UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{container_id}','"replacement_container"')`)
	if _, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(old)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old target accepted", err)
	}
	v := f.status(t)
	if v.Freshness != "unknown" || v.Reason != "target_changed" || v.Readiness != "unknown" || v.LastObservation == nil || v.CurrentContainerID != "replacement_container" || v.ObservedContainerID != target.ContainerID {
		t.Fatal(v)
	}
	if f.target(t).ContainerID != "replacement_container" {
		t.Fatal("replacement not observed")
	}
	if _, err := f.pg.ReportHostingObservation(t.Context(), "another_runner", f.token, observationBody(old)); err == nil {
		t.Fatal("wrong runner accepted")
	}
}

func TestPostgresHostingObservationExpiryAfterBlockingWrite(t *testing.T) {
	f := newObservationFixture(t)
	target := f.target(t)
	// A trigger delays the actual write past the DB challenge deadline.
	f.exec(t, `UPDATE hosting_runtime_observations SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb($1::text))`, time.Now().Add(200*time.Millisecond).UTC().Format(time.RFC3339Nano))
	f.exec(t, `CREATE FUNCTION delay_observation_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3);RETURN NEW;END;$$; CREATE TRIGGER delay_observation_write BEFORE UPDATE ON hosting_runtime_observations FOR EACH ROW EXECUTE FUNCTION delay_observation_write()`)
	if _, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(target)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("late report accepted", err)
	}
	var ack []byte
	if err := f.db.QueryRow(t.Context(), `SELECT accepted_ack FROM hosting_runtime_observations`).Scan(&ack); err != nil || len(ack) != 0 {
		t.Fatal("expired write committed", err, string(ack))
	}
}

func TestPostgresHostingObservationSchemaGuard(t *testing.T) {
	f := newObservationFixture(t)
	f.exec(t, `DROP TABLE hosting_runtime_observations`)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || ok {
		t.Fatal(ok, err)
	}
	if pg, err := store.OpenPostgres(t.Context(), f.db.Config().ConnConfig.ConnString()); err == nil {
		_ = pg.Close()
		t.Fatal("missing observation schema accepted")
	}
}

func waitObservationBlock(t *testing.T, ctx context.Context, f observationFixture, pid uint32, fragment string) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position($2 in query)>0)`, int(pid), fragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}

func TestPostgresHostingObservationAuthorityAndExpiryAfterResourceWait(t *testing.T) {
	for _, mode := range []string{"rotation", "expiry", "restore"} {
		t.Run(mode, func(t *testing.T) {
			f := newObservationFixture(t)
			target := f.target(t)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource.ID); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "rotation":
				if _, err = blocker.Exec(ctx, `UPDATE runners SET token_hash=repeat('a',64) WHERE id=$1`, f.server.RunnerID); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if _, err = blocker.Exec(ctx, `UPDATE hosting_runtime_observations SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb($1::text))`, time.Now().Add(150*time.Millisecond).UTC().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			case "restore":
				if _, err = blocker.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=false`); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.pg.ReportHostingObservation(ctx, f.server.RunnerID, f.token, observationBody(target))
				done <- e
			}()
			joined := false
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				if !joined {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("report did not join")
					}
				}
			}()
			waitObservationBlock(t, ctx, f, blocker.Conn().PgConn().PID(), "resources")
			if mode == "expiry" {
				if _, err = f.db.Exec(ctx, `SELECT pg_sleep(0.2)`); err != nil {
					t.Fatal(err)
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
				if err == nil {
					t.Fatal("stale authority accepted", mode)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestPostgresHostingObservationSerializesWithSettlingReplacement(t *testing.T) {
	f := newObservationFixture(t)
	old := f.target(t)
	in := hostingDeploymentRequest()
	in.IdempotencyKey = "replacement"
	op, err := f.pg.RequestHostingDeployment(t.Context(), "hop_observed_replacement", f.resource.ID, "usr_bootstrap", in, destinationEvent("replacement_request"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	a, prepared := prepareHostingAttempt(t, f.pg, op, f.server.RunnerID)
	admitted, err := f.pg.HostingApplyBegin(t.Context(), f.server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `CREATE FUNCTION block_observation_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.healthy IS DISTINCT FROM OLD.healthy THEN PERFORM pg_advisory_xact_lock(9421054);END IF;RETURN NEW;END;$$;CREATE TRIGGER block_observation_settlement BEFORE UPDATE ON hosting_binding_state FOR EACH ROW EXECUTE FUNCTION block_observation_settlement()`)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(9421054)`); err != nil {
		t.Fatal(err)
	}
	complete := make(chan error, 1)
	go func() {
		_, e := f.pg.HostingComplete(ctx, f.server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "replacement_result", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "replacement_container", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}})
		complete <- e
	}()
	completeJoined := false
	reportJoined := false
	report := make(chan error, 1)
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !completeJoined {
			select {
			case <-complete:
			case <-time.After(time.Second):
				t.Error("completion did not join")
			}
		}
		if !reportJoined {
			select {
			case <-report:
			case <-time.After(time.Second):
				t.Error("report did not join")
			}
		}
	}()
	pid := blocker.Conn().PgConn().PID()
	waitObservationBlock(t, ctx, f, pid, "SetHostingHealthy")
	go func() {
		_, e := f.pg.ReportHostingObservation(ctx, f.server.RunnerID, f.token, observationBody(old))
		report <- e
	}()
	// Completion owns destination serialization while its healthy write blocks.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockHostingExecutionDestination%' AND query NOT LIKE '%pg_stat_activity%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-complete:
		completeJoined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-report:
		reportJoined = true
		if !errors.Is(err, store.ErrConflict) {
			t.Fatal("old target report", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if v := f.status(t); v.CurrentOperationID != op.ID || v.MutationHold || v.CurrentContainerID != "replacement_container" {
		t.Fatal(v)
	}
}

func TestPostgresHostingObservationViewerScopeAndLegacyUnknown(t *testing.T) {
	f := newObservationFixture(t)
	if _, err := f.pg.HostingRuntimeStatus(t.Context(), "usr_viewer", f.resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("cross project read", err)
	}
	_, err := f.pg.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "observation_viewer", ProjectID: f.resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingRuntimeStatus(t.Context(), "usr_viewer", f.resource.ID); err != nil {
		t.Fatal(err)
	}
	legacy := createDestinationResource(t, f.pg, "legacy_unknown", f.resource.ProjectID)
	v, err := f.pg.HostingRuntimeStatus(t.Context(), "usr_viewer", legacy.ID)
	if err != nil || v.Freshness != "unknown" || v.Reason != "never_observed" {
		t.Fatal(v, err)
	}
	f.exec(t, `DELETE FROM project_members WHERE id='observation_viewer'`)
	if _, err = f.pg.HostingRuntimeStatus(t.Context(), "usr_viewer", f.resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked viewer", err)
	}
}

func TestPostgresHostingObservationRowsRejectOversizedJSON(t *testing.T) {
	f := newObservationFixture(t)
	f.target(t)
	tooLarge, _ := json.Marshal(map[string]string{"padding": string(make([]byte, 8193))})
	if _, err := f.db.Exec(t.Context(), `UPDATE hosting_runtime_observations SET pending_target=$1`, tooLarge); err == nil {
		t.Fatal("oversized row accepted")
	}
}

func TestPostgresHostingObservationSerializesGrantRevocation(t *testing.T) {
	f := newObservationFixture(t)
	target := f.target(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource.ID); err != nil {
		t.Fatal(err)
	}
	report := make(chan error, 1)
	revoke := make(chan error, 1)
	go func() {
		_, e := f.pg.ReportHostingObservation(ctx, f.server.RunnerID, f.token, observationBody(target))
		report <- e
	}()
	reportJoined, revokeJoined := false, false
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !reportJoined {
			select {
			case <-report:
			case <-time.After(time.Second):
				t.Error("report did not join")
			}
		}
		if !revokeJoined {
			select {
			case <-revoke:
			case <-time.After(time.Second):
				t.Error("revoke did not join")
			}
		}
	}()
	waitObservationBlock(t, ctx, f, blocker.Conn().PgConn().PID(), "resources")
	go func() {
		_, e := f.pg.RevokeDestinationProject(ctx, f.destination.ID, f.resource.ProjectID, destinationEvent("observation_revoke"))
		revoke <- e
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockHostingDestination%' AND query NOT LIKE '%pg_stat_activity%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-report:
		reportJoined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-revoke:
		revokeJoined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = f.pg.ReportHostingObservation(ctx, f.server.RunnerID, f.token, observationBody(target)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked receipt replay", err)
	}
}
