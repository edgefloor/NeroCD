package store_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type readinessFixture struct {
	pg          *store.PostgresStore
	db          *pgxpool.Pool
	server      domain.Server
	destination domain.Destination
	credential  string
}

func newReadinessFixture(t *testing.T) readinessFixture {
	t.Helper()
	pg, db := openPostgresIntegrationStore(t, t.Context(), "server_readiness")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	now := time.Now().UTC()
	credential := "readiness_credential"
	enrollment, err := pg.CreateRunnerEnrollment(t.Context(), domain.RunnerEnrollment{ID: "enroll_readiness", TokenHash: "readiness_enrollment", RunnerID: "runner_readiness", RunnerName: "readiness", Tags: []string{}, Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.ServerReadinessCapability}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, destinationEvent("readiness_enroll"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := pg.ConsumeRunnerEnrollment(t.Context(), domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "readiness_consume", CredentialHash: credential}, destinationEvent("readiness_consume"))
	if err != nil {
		t.Fatal(err)
	}
	server, destination, err := pg.CreateServerWithDestination(t.Context(), domain.Server{ID: "server_readiness", RunnerID: runner.ID, Name: "readiness", CreatedBy: "usr_bootstrap", CreatedAt: now}, domain.Destination{ID: "dest_readiness", ServerID: "server_readiness", Name: "default", RuntimeKind: "docker_compose", CreatedAt: now}, destinationEvent("readiness_server"))
	if err != nil {
		t.Fatal(err)
	}
	return readinessFixture{pg, db, server, destination, credential}
}
func (f readinessFixture) challenge(t *testing.T) domain.ServerReadinessChallenge {
	t.Helper()
	v, e := f.pg.IssueServerReadiness(t.Context(), f.server.RunnerID, f.credential)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func readinessBody(c domain.ServerReadinessChallenge) domain.ServerReadinessReport {
	return domain.ServerReadinessReport{SampleID: c.SampleID, Profile: c.Profile, DurationMS: 1, OS: "linux", Architecture: "amd64", Execution: "enabled", Configuration: "matched", Docker: "available", DockerAPIVersion: "1.54", DaemonID: "daemon_ready", DaemonOS: "linux", DaemonArchitecture: "amd64", CLI: "available", WorkRoot: "accessible", JournalRoot: "accessible", WorkFreeBytes: 1 << 30, JournalFreeBytes: 1 << 30}
}
func (f readinessFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.db.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f readinessFixture) status(t *testing.T, status, reason string) {
	t.Helper()
	v, e := f.pg.GetServerReadiness(t.Context(), f.server.ID)
	if e != nil || v.Status != status || v.Reason != reason {
		t.Fatalf("status %+v %v want %s/%s", v, e, status, reason)
	}
}

func TestPostgresServerReadinessReplayFreshnessAndSupersession(t *testing.T) {
	f := newReadinessFixture(t)
	f.status(t, "unknown", "no_sample")
	c := f.challenge(t)
	if again := f.challenge(t); !reflect.DeepEqual(c, again) {
		t.Fatal("pending changed")
	}
	if c.ExpiresAt.Sub(c.IssuedAt) != 10*time.Second || c.ServerID != f.server.ID || c.DestinationID != f.destination.ID || c.RunnerID != f.server.RunnerID {
		t.Fatal(c)
	}
	body := readinessBody(c)
	ack, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body)
	if e != nil || !ack.Accepted {
		t.Fatal(ack, e)
	}
	f.status(t, "ready", "qualified")
	f.exec(t, `UPDATE runners SET last_heartbeat_at=clock_timestamp()-interval '5 minutes'`)
	f.status(t, "ready", "qualified")
	next := f.challenge(t)
	if next.SampleID == c.SampleID {
		t.Fatal("sample reused")
	}
	replay, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body)
	if e != nil || replay != ack {
		t.Fatal("replay changed", replay, e)
	}
	body.CLI = "missing"
	if _, e = f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	f.exec(t, `UPDATE server_readiness SET accepted_ack=jsonb_set(accepted_ack,'{observed_at}',to_jsonb((clock_timestamp()-interval '31 seconds')))`)
	f.status(t, "unknown", "stale")
	f.exec(t, `UPDATE server_readiness SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb((clock_timestamp()-interval '1 second')))`)
	if _, e = f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, readinessBody(next)); !errors.Is(e, store.ErrConflict) {
		t.Fatal("expired", e)
	}
	replacement := f.challenge(t)
	if _, e = f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, readinessBody(next)); !errors.Is(e, store.ErrConflict) {
		t.Fatal("superseded", e)
	}
	blocked := readinessBody(replacement)
	blocked.WorkRoot = "unsafe"
	if _, e = f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, blocked); e != nil {
		t.Fatal(e)
	}
	f.status(t, "blocked", "prerequisite_failed")
	if _, e = f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, readinessBody(c)); !errors.Is(e, store.ErrConflict) {
		t.Fatal("evicted", e)
	}
	var count int
	if e = f.db.QueryRow(t.Context(), `SELECT count(*) FROM server_readiness`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestPostgresServerReadinessAuthority(t *testing.T) {
	for _, mutation := range []struct{ name, sql string }{
		{"capability", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_readiness_v1')`}, {"shell", `UPDATE runners SET capabilities=array_append(capabilities,'shell')`}, {"inactive", `UPDATE runners SET status='disabled'`}, {"credential", `UPDATE runners SET token_hash='rotated'`}, {"revoked", `UPDATE runner_enrollments SET revoked_at=clock_timestamp()`}, {"unused", `UPDATE runner_enrollments SET used_at=NULL,consume_request_id=NULL,credential_hash=NULL`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newReadinessFixture(t)
			body := readinessBody(f.challenge(t))
			if _, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body); e != nil {
				t.Fatal(e)
			}
			f.exec(t, mutation.sql)
			f.status(t, "unknown", "authority_unavailable")
			if _, e := f.pg.IssueServerReadiness(t.Context(), f.server.RunnerID, f.credential); !errors.Is(e, store.ErrConflict) {
				t.Fatal(e)
			}
			if _, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body); !errors.Is(e, store.ErrConflict) {
				t.Fatal(e)
			}
		})
	}
	f := newReadinessFixture(t)
	if _, e := f.pg.IssueServerReadiness(t.Context(), f.server.RunnerID, "wrong"); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE runners SET token_hash='rotated'`)
	if _, e := f.pg.IssueServerReadiness(t.Context(), f.server.RunnerID, "rotated"); !errors.Is(e, store.ErrConflict) {
		t.Fatal("rotated unenrolled", e)
	}
	var count int
	if e := f.db.QueryRow(t.Context(), `SELECT count(*) FROM server_readiness`).Scan(&count); e != nil || count != 0 {
		t.Fatal("unauthorized insert", count, e)
	}
}

func TestPostgresServerReadinessCurrentPinsAndRestore(t *testing.T) {
	f := newReadinessFixture(t)
	body := readinessBody(f.challenge(t))
	if _, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body); e != nil {
		t.Fatal(e)
	}
	f.status(t, "ready", "qualified")
	resource := createDestinationResource(t, f.pg, "readiness_resource", "proj_platform")
	grantDestination(t, f.pg, f.destination.ID, resource.ProjectID, "readiness_grant")
	if _, e := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "readiness_binding", ResourceID: resource.ID, DestinationID: f.destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("readiness_binding")); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,daemon_id) VALUES('readiness_binding','dest_readiness',18491,'other_daemon')`)
	f.status(t, "blocked", "daemon_mismatch")
	f.exec(t, `UPDATE hosting_binding_state SET restore_admitted=false`)
	f.status(t, "blocked", "restore_hold")
	f.exec(t, `UPDATE hosting_binding_state SET restore_admitted=true`)
	f.exec(t, `INSERT INTO hosting_binding_restore_holds(binding_id) VALUES('readiness_binding')`)
	f.status(t, "blocked", "restore_hold")
	f.exec(t, `UPDATE server_readiness SET accepted_ack=jsonb_set(accepted_ack,'{observed_at}',to_jsonb((clock_timestamp()-interval '31 seconds')))`)
	f.status(t, "blocked", "restore_hold")
	f.exec(t, `DELETE FROM server_readiness`)
	f.status(t, "blocked", "restore_hold")
	f.exec(t, `UPDATE hosting_binding_state SET restore_admitted=false`)
	f.status(t, "blocked", "restore_hold")
	f.exec(t, `UPDATE runners SET token_hash=''`)
	f.status(t, "unknown", "authority_unavailable")
}

func readinessWaitForLock(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if e := db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid())`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation never waited for lock")
}

func TestPostgresServerReadinessLockWaitRevalidatesAuthorityAndDeadline(t *testing.T) {
	for _, tc := range []struct{ name, lock, change string }{
		{"runner_rotation", `SELECT id FROM runners FOR UPDATE`, `UPDATE runners SET token_hash='rotated'`},
		{"enrollment_revocation", `SELECT id FROM runner_enrollments FOR UPDATE`, `UPDATE runner_enrollments SET revoked_at=clock_timestamp()`},
		{"enrollment_credential", `SELECT id FROM runner_enrollments FOR UPDATE`, `UPDATE runner_enrollments SET credential_hash='different_credential'`},
		{"deadline", `SELECT server_id FROM server_readiness FOR UPDATE`, `UPDATE server_readiness SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb((clock_timestamp()-interval '1 second')))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadinessFixture(t)
			body := readinessBody(f.challenge(t))
			tx, e := f.db.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(t.Context(), tc.lock); e != nil {
				t.Fatal(e)
			}
			result := make(chan error, 1)
			go func() {
				_, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body)
				result <- e
			}()
			readinessWaitForLock(t, f.db)
			if _, e = tx.Exec(t.Context(), tc.change); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(t.Context()); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-result:
				if !errors.Is(e, store.ErrConflict) {
					t.Fatal("accepted after lockwait", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("report hung")
			}
		})
	}
}

func TestPostgresServerReadinessConcurrentIssueReportAndPlacementLockIsolation(t *testing.T) {
	f := newReadinessFixture(t)
	c := f.challenge(t)
	body := readinessBody(c)
	resource := createDestinationResource(t, f.pg, "readiness_isolation", "proj_platform")
	grantDestination(t, f.pg, f.destination.ID, resource.ProjectID, "readiness_isolation")
	if _, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "readiness_isolation", ResourceID: resource.ID, DestinationID: f.destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("readiness_isolation_binding")); err != nil {
		t.Fatal(err)
	}
	tx, e := f.db.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Even destination execution advisory and row locks must not block diagnostics.
	if _, e = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:' || $1,0))`, f.destination.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `SELECT id FROM destinations WHERE id=$1 FOR UPDATE`, f.destination.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, resource.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `SELECT id FROM resource_bindings WHERE resource_id=$1 FOR UPDATE`, resource.ID); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Go(func() { _, e := f.pg.IssueServerReadiness(ctx, f.server.RunnerID, f.credential); results <- e })
		wg.Go(func() { _, e := f.pg.ReportServerReadiness(ctx, f.server.RunnerID, f.credential, body); results <- e })
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	f.status(t, "ready", "qualified")
}

func TestPostgresServerReadinessDeadlineUsesClockAfterReadinessLock(t *testing.T) {
	f := newReadinessFixture(t)
	body := readinessBody(f.challenge(t))
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `UPDATE server_readiness SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb(clock_timestamp()+interval '200 milliseconds'))`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, body)
		result <- e
	}()
	readinessWaitForLock(t, f.db)
	if _, err = tx.Exec(t.Context(), `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, store.ErrConflict) {
			t.Fatal("expired during lockwait", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("report hung")
	}
}

func TestPostgresServerReadinessSchemaGuard(t *testing.T) {
	f := newReadinessFixture(t)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	f.exec(t, `ALTER TABLE server_readiness DROP CONSTRAINT server_readiness_server_id_fkey`)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || ok {
		t.Fatal("missing FK remains ready", ok, err)
	}
}

func TestPostgresServerReadinessIssueWaitRevalidatesAuthority(t *testing.T) {
	for _, tc := range []struct{ name, lock, change string }{
		{"runner", `SELECT id FROM runners FOR UPDATE`, `UPDATE runners SET token_hash='rotated'`},
		{"enrollment", `SELECT id FROM runner_enrollments FOR UPDATE`, `UPDATE runner_enrollments SET revoked_at=clock_timestamp()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadinessFixture(t)
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(t.Context(), tc.lock); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, e := f.pg.IssueServerReadiness(t.Context(), f.server.RunnerID, f.credential); result <- e }()
			readinessWaitForLock(t, f.db)
			if _, err = tx.Exec(t.Context(), tc.change); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-result:
				if !errors.Is(err, store.ErrConflict) {
					t.Fatal("issue accepted after authority changed", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("issue hung")
			}
			var count int
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM server_readiness`).Scan(&count); err != nil || count != 0 {
				t.Fatal("issue inserted without authority", count, err)
			}
		})
	}
}

func TestPostgresServerReadinessDeadlineIncludesFinalUpdate(t *testing.T) {
	f := newReadinessFixture(t)
	challenge := f.challenge(t)
	f.exec(t, `UPDATE server_readiness SET pending_target=jsonb_set(pending_target,'{expires_at}',to_jsonb(clock_timestamp()+interval '400 milliseconds'))`)
	// Delay only the final acceptance UPDATE, after the initial deadline check.
	f.exec(t, `CREATE FUNCTION delay_readiness_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.accepted_body IS DISTINCT FROM OLD.accepted_body THEN PERFORM pg_sleep(0.6); END IF; RETURN NEW; END $$;
 CREATE TRIGGER delay_readiness_acceptance BEFORE UPDATE ON server_readiness FOR EACH ROW EXECUTE FUNCTION delay_readiness_acceptance()`)
	_, err := f.pg.ReportServerReadiness(t.Context(), f.server.RunnerID, f.credential, readinessBody(challenge))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("accepted sample after final UPDATE crossed original deadline: %v", err)
	}
	var pending string
	var accepted bool
	if err = f.db.QueryRow(t.Context(), `SELECT pending_target->>'sample_id',accepted_body IS NOT NULL OR accepted_ack IS NOT NULL OR accepted_target IS NOT NULL OR accepted_identity<>'' FROM server_readiness WHERE server_id=$1`, f.server.ID).Scan(&pending, &accepted); err != nil {
		t.Fatal(err)
	}
	if pending != challenge.SampleID || accepted {
		t.Fatalf("expired acceptance committed: pending=%q accepted=%v", pending, accepted)
	}
	f.status(t, "unknown", "no_sample")
}
