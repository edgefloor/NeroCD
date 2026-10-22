package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func buildLogBatch(a domain.HostingBuildAuthority, key string) domain.HostingBuildLogBatch {
	return domain.HostingBuildLogBatch{Authority: a, Events: []domain.HostingBuildLogEvent{{EventKey: key, Sequence: 1, Component: "build", Stream: "stdout", Message: "hello <world>"}}}
}
func TestPostgresHostingBuildLogsReplayRetentionAndCursor(t *testing.T) {
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "logs")
	ctx := t.Context()
	in := buildLogBatch(a, "first")
	ts := time.Now().In(time.FixedZone("offset", 3600))
	in.Events[0].SourceTimestamp = &ts
	ack, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Authority != a || len(ack.Receipts) != 1 || ack.Receipts[0].Sequence != 1 {
		t.Fatalf("ack %+v", ack)
	}
	normalized, _ := domain.CanonicalHostingBuildLogs(in)
	if ack.Receipts[0].BodyHash != domain.HostingBuildLogBodyHash(a, normalized.Events[0]) {
		t.Fatal("unbound receipt")
	}
	page, err := f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 1)
	if err != nil || len(page.Items) != 1 || page.Gap || !page.HistoryComplete || page.NextSequence != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{a.Fence, a.LeaseID, "body_hash", "event_key", "authority", "runner_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("public leak %s", secret)
		}
	}
	// Same immutable event timestamp canonicalization survives a timezone change.
	utc := ts.UTC().Truncate(time.Microsecond)
	in.Events[0].SourceTimestamp = &utc
	replay, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, in)
	if err != nil || replay.Receipts[0] != ack.Receipts[0] {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET status='expired',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, in); err != nil {
		t.Fatal("historical replay", err)
	}
	mixed := in
	mixed.Events = append(append([]domain.HostingBuildLogEvent(nil), in.Events...), buildLogBatch(a, "new").Events[0])
	if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, mixed); err == nil {
		t.Fatal("mixed stale batch accepted")
	}
	changed := buildLogBatch(a, "first")
	changed.Events[0].Message = "changed"
	if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, changed); err == nil {
		t.Fatal("changed replay accepted")
	}
	// Retention is atomic with its audit and retains immutable acknowledgments.
	if _, err = f.db.Exec(ctx, `UPDATE task_runs SET status='failed' WHERE id=$1`, a.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_logs SET created_at=clock_timestamp()-interval '2 days' WHERE run_id=$1`, a.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_builds SET state='failed',revision=revision+1,failure_code='authority_lost' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	policy, err := f.pg.UpdateRunLogRetentionPolicy(ctx, domain.RunLogRetentionPolicy{Enabled: true, KeepDays: 1, BatchSize: 10, UpdatedBy: "usr_bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	audit := destinationEvent("log_retention")
	if err = f.pg.CreateAuditEvent(ctx, audit); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ExecuteRunLogRetention(ctx, "failed-retention", store.RunLogRetentionBodyHash(policy), audit); err == nil {
		t.Fatal("audit failure accepted")
	}
	page, err = f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.DeletedThroughSequence != 0 {
		t.Fatalf("rollback %+v %v", page, err)
	}
	audit.ID = "successful_log_retention"
	retained, err := f.pg.ExecuteRunLogRetention(ctx, "retention", store.RunLogRetentionBodyHash(policy), audit)
	if err != nil || retained.Deleted != 1 {
		t.Fatalf("retention %+v %v", retained, err)
	}
	page, err = f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 10)
	if err != nil || len(page.Items) != 0 || page.NextSequence != 1 || page.DeletedThroughSequence != 1 || !page.Gap || !page.Terminal {
		t.Fatalf("pruned %+v %v", page, err)
	}
	if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, in); err != nil {
		t.Fatal("pruned historical ack", err)
	}
	replayRetention, err := f.pg.ExecuteRunLogRetention(ctx, "retention", store.RunLogRetentionBodyHash(policy), audit)
	if err != nil || replayRetention.Deleted != 1 {
		t.Fatal("retention replay", err)
	}
}
func TestPostgresHostingBuildLogsAtomicBoundsAndGenericDenial(t *testing.T) {
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "bounds")
	ctx := t.Context()
	log := domain.RunLog{ID: "generic", RunID: a.RunID, EventKey: "event", LeaseID: a.LeaseID, Attempt: a.Attempt, RequestedSequence: 1, Sequence: 1, Stream: "stdout", Message: "generic", CreatedAt: time.Now()}
	if err := f.pg.CreateRunLog(ctx, log); !errors.Is(err, store.ErrConflict) {
		t.Fatal("controller bypass", err)
	}
	if _, err := f.pg.CreateRunLogsForLease(ctx, []domain.RunLog{log}, a.RunID, f.runner, a.LeaseID, a.Attempt, a.Fence, time.Now()); !errors.Is(err, store.ErrConflict) {
		t.Fatal("event bypass", err)
	}
	log.EventKey = ""
	if _, err := f.pg.CreateRunLogForLease(ctx, log, f.runner, a.LeaseID, a.Attempt, a.Fence, time.Now()); !errors.Is(err, store.ErrConflict) {
		t.Fatal("scalar bypass", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE hosting_build_log_states SET last_sequence=$2 WHERE build_id=$1`, b.ID, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, buildLogBatch(a, "overflow")); err == nil {
		t.Fatal("overflow accepted")
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1`, a.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial append %d %v", count, err)
	}
}

// Each case waits at the actual insert while expiry progresses independently of
// held authority rows. No source, engine, container or external process runs.
func TestPostgresHostingBuildLogsRejectExpiryAfterBlockedInsert(t *testing.T) {
	for _, kind := range []string{"lease", "creator"} {
		t.Run(kind, func(t *testing.T) {
			f := newBuildStoreFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES('log_creator','logs','service_account','log_creator',ARRAY['system_admin'],'active','usr_bootstrap')`); err != nil {
				t.Fatal(err)
			}
			event := destinationEvent("log_request")
			event.ActorID = "log_creator"
			b, err := f.pg.RequestHostingBuild(ctx, "expiry", f.src.ResourceID, "log_creator", buildInput("expiry"), buildPolicy(), buildEnqueue, event)
			if err != nil {
				t.Fatal(err)
			}
			event = destinationEvent("log_dispatch")
			event.ActorID = "log_creator"
			b, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, event)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: "lease_expiry", Attempt: 1, Fence: "fence_expiry"}
			if _, err = f.db.Exec(ctx, `UPDATE task_runs SET status='running',runner_id=$2 WHERE id=$1`, a.RunID, f.runner); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),1,$4)`, a.LeaseID, a.RunID, f.runner, a.Fence); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "lease":
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, a.LeaseID)
			case "creator":
				_, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id='log_creator'`)
			}

			if err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, `LOCK TABLE run_logs IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.pg.AppendHostingBuildLogs(ctx, f.runner, buildLogBatch(a, "blocked")); done <- e }()
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("did not block: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if _, err = f.db.Exec(ctx, `SELECT pg_sleep(2.1)`); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("expired authority committed logs")
			}
			var logs, receipts, last int
			if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_logs WHERE run_id=$1),(SELECT count(*) FROM hosting_build_log_receipts WHERE run_id=$1),(SELECT last_sequence FROM hosting_build_log_states WHERE run_id=$1)`, a.RunID).Scan(&logs, &receipts, &last); err != nil || logs != 0 || receipts != 0 || last != 0 {
				t.Fatalf("non-atomic %d %d %d %v", logs, receipts, last, err)
			}
		})
	}
}
func TestPostgresHostingBuildLogsReadByteBudget(t *testing.T) {
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "read-bounds")
	ctx := t.Context()
	for i := 0; i < 25; i++ {
		in := buildLogBatch(a, fmt.Sprint(i))
		in.Events[0].Message = strings.Repeat("<", 16000)
		if _, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, in); err != nil {
			t.Fatal(err)
		}
	}
	cursor := 0
	seen := 0
	for {
		page, err := f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, cursor, 200)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(page)
		if len(data) > domain.HostingBuildLogByteLimit || page.Gap {
			t.Fatalf("read bounds %d %+v", len(data), page)
		}
		for _, item := range page.Items {
			if item.Sequence != cursor+1 {
				t.Fatal("skipped output", cursor, item.Sequence)
			}
			cursor = item.Sequence
			seen++
			if item.MessageTruncated {
				t.Fatal("bounded typed message truncated")
			}
		}
		if page.NextSequence != cursor {
			t.Fatal("cursor skipped undelivered row")
		}
		if !page.Truncated {
			break
		}
		if len(page.Items) == 0 {
			t.Fatal("no progress")
		}
	}
	if seen != 25 {
		t.Fatal("missing output", seen)
	}
}

func TestPostgresHostingBuildLogsCurrentAuthorityAndHistoricalAck(t *testing.T) {
	for _, kind := range []string{"source", "destination_grant", "runner", "enrollment", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := newBuildStoreFixture(t)
			b, a := f.start(t, "revocation")
			ctx := t.Context()
			in := buildLogBatch(a, "committed")
			if _, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, in); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "source":
				v2 := f.src
				v2.Generation = 2
				v2.RequestedRef = "changed"
				_, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, v2, destinationEvent("change_source"))
			case "destination_grant":
				_, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, b.Snapshot.DestinationID)
			case "runner":
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=ARRAY['image_deploy'] WHERE id=$1`, f.runner)
			case "enrollment":
				_, err = f.db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner)
			case "cancel":
				_, err = f.pg.CancelHostingBuild(ctx, "usr_bootstrap", b.ID, b.Revision, destinationEvent("cancel_logs"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, in); err != nil {
				t.Fatal("exact committed ack lost", err)
			}
			if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, buildLogBatch(a, "new")); err == nil {
				t.Fatal("new output accepted without current authority")
			}
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_log_receipts WHERE build_id=$1`, b.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("receipt count %d %v", count, err)
			}
		})
	}
}

func TestPostgresHostingBuildLogsConcurrentReplayAndRetentionPrefix(t *testing.T) {
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "concurrent-logs")
	ctx := t.Context()
	in := buildLogBatch(a, "same")
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			ack, e := f.pg.AppendHostingBuildLogs(ctx, f.runner, in)
			if e == nil && (len(ack.Receipts) != 1 || ack.Receipts[0].Sequence != 1) {
				e = fmt.Errorf("inconsistent receipt %+v", ack)
			}
			done <- e
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"second", "third"} {
		if _, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, buildLogBatch(a, key)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`UPDATE run_leases SET status='expired' WHERE run_id=$1`, `UPDATE task_runs SET status='failed' WHERE id=$1`, `UPDATE run_logs SET created_at=clock_timestamp()-interval '2 days' WHERE run_id=$1 AND sequence<>2`} {
		if _, err := f.db.Exec(ctx, query, a.RunID); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := f.pg.UpdateRunLogRetentionPolicy(ctx, domain.RunLogRetentionPolicy{Enabled: true, KeepDays: 1, BatchSize: 10, UpdatedBy: "usr_bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.pg.ExecuteRunLogRetention(ctx, "prefix", store.RunLogRetentionBodyHash(policy), destinationEvent("prefix"))
	if err != nil || result.Deleted != 1 {
		t.Fatalf("non-prefix deletion %+v %v", result, err)
	}
	page, err := f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != 2 || page.DeletedThroughSequence != 1 || page.NextSequence != 2 || !page.Truncated {
		t.Fatalf("prefix page %+v %v", page, err)
	}
	var last int
	if err = f.db.QueryRow(ctx, `SELECT last_sequence FROM hosting_build_log_states WHERE build_id=$1`, b.ID).Scan(&last); err != nil || last != 3 {
		t.Fatalf("counter reset %d %v", last, err)
	}
}

func TestPostgresHostingBuildLogsMigrationHistoryAndReadiness(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	// Reproduce a pre-0051 build with only a retained suffix. Previously deleted
	// output is intentionally unknown; the migration must not invent its extent.
	if _, err := f.db.Exec(ctx, `DROP TRIGGER hosting_build_log_dispatch ON hosting_build_tasks; DROP TABLE hosting_build_log_receipts,hosting_build_log_states; DROP FUNCTION nerocd_build_log_dispatch(),nerocd_build_log_immutable(),nerocd_hosting_build_log_schema_compatible(); ALTER TABLE hosting_build_tasks DROP CONSTRAINT hosting_build_tasks_log_identity; ALTER TABLE run_logs DROP COLUMN component,DROP COLUMN source_timestamp`); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing0051 readiness %v %v", ok, err)
	}
	b, a := f.start(t, "legacy-logs")
	if _, err := f.db.Exec(ctx, `INSERT INTO run_logs(id,run_id,sequence,stream,message,created_at) VALUES('old',$1,17,'stdout',$2,clock_timestamp())`, a.RunID, strings.Repeat("世界", 10000)); err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedMigrations(ctx, f.db, "0050_hosting_build_deployments.sql", "0051_hosting_build_logs.sql"); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(ctx); !ok || err != nil {
		t.Fatalf("restored0051 readiness %v %v", ok, err)
	}
	page, err := f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 200)
	if err != nil || page.HistoryComplete || !page.Gap || page.DeletedThroughSequence != 0 || len(page.Items) != 1 || page.NextSequence != 17 || !page.Items[0].MessageTruncated || len(page.Items[0].Message) > domain.HostingBuildLogMessageLimit {
		t.Fatalf("legacy history %+v %v", page, err)
	}
	if _, err = f.pg.AppendHostingBuildLogs(ctx, f.runner, buildLogBatch(a, "legacy-new")); err == nil {
		t.Fatal("legacy history accepted typed output")
	}
	for _, trigger := range []struct{ table, name string }{{"hosting_build_tasks", "hosting_build_log_dispatch"}, {"hosting_build_log_states", "hosting_build_log_state_identity"}, {"hosting_build_log_receipts", "hosting_build_log_receipt_identity"}} {
		if _, err = f.db.Exec(ctx, "ALTER TABLE "+trigger.table+" DISABLE TRIGGER "+trigger.name); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.pg.SchemaCompatible(ctx); ok || err != nil {
			t.Fatalf("disabled trigger readiness %s %v %v", trigger.name, ok, err)
		}
		if _, err = f.db.Exec(ctx, "ALTER TABLE "+trigger.table+" ENABLE TRIGGER "+trigger.name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_log_states SET history_complete=true WHERE build_id=$1`, b.ID); err == nil {
		t.Fatal("legacy proof rewritten")
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_log_states SET last_sequence=0 WHERE build_id=$1`, b.ID); err == nil {
		t.Fatal("counter rewound")
	}
}

func TestPostgresGenericLogAppendCompletionLockOrder(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Now()
	run := domain.TaskRun{ID: "run_generic_log_order", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell"}, Status: domain.RunRunning, RunnerID: &f.runner, RunnerTags: []string{}, RequestedBy: "usr_bootstrap", StartedAt: now}
	if _, err := f.pg.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	leaseID := "lease_generic_log_order"
	fence := "fence_generic_log_order"
	if _, err := f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),1,$4)`, leaseID, run.ID, f.runner, fence); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, run.ID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	event := domain.RunLog{ID: "log_generic_order", RunID: run.ID, EventKey: "generic_order", LeaseID: leaseID, Attempt: 1, RequestedSequence: 1, Sequence: 1, Stream: "stdout", Message: "metadata test", CreatedAt: now}
	appendDone := make(chan error, 1)
	go func() {
		_, e := f.pg.CreateRunLogsForLease(ctx, []domain.RunLog{event}, run.ID, f.runner, leaseID, 1, fence, now)
		appendDone <- e
	}()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-appendDone:
			t.Fatalf("append did not wait %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	completeDone := make(chan error, 1)
	go func() {
		finished := time.Now()
		_, e := f.pg.CompleteLeaseRequest(ctx, leaseID, f.runner, domain.RunSucceeded, 1, fence, "generic_order_complete", finished, domain.RunSucceeded, &finished, nil, nil, destinationEvent("generic_order_complete"))
		completeDone <- e
	}()
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-appendDone; err != nil {
		t.Fatal("generic append", err)
	}
	if err = <-completeDone; err != nil {
		t.Fatal("generic completion", err)
	}
	if _, err = f.pg.CreateRunLogsForLease(ctx, []domain.RunLog{event}, run.ID, f.runner, leaseID, 1, fence, now); err != nil {
		t.Fatal("legacy historical replay", err)
	}
}

func TestPostgresHostingBuildLogsRestoreKeepsReceiptsAndCounters(t *testing.T) {
	container := os.Getenv("NEROCD_TEST_PG_CONTAINER")
	if container == "" {
		t.Skip("set task-owned NEROCD_TEST_PG_CONTAINER for pg_dump/pg_restore")
	}
	f := newBuildStoreFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	b, a := f.start(t, "logs-restore")
	in := buildLogBatch(a, "restore")
	ack, err := f.pg.AppendHostingBuildLogs(ctx, f.runner, in)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = f.db.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if err != nil || parsed.User == nil {
		t.Fatal("invalid fixture DSN")
	}
	dump := exec.CommandContext(ctx, "docker", "exec", container, "pg_dump", "--username", parsed.User.Username(), "--dbname", strings.TrimPrefix(parsed.Path, "/"), "--schema", schema, "--format=custom", "--no-owner", "--no-privileges")
	archive, err := dump.Output()
	if err != nil {
		t.Fatal("pg_dump", err)
	}
	target := fmt.Sprintf("nerocd_build_logs_restore_%d", time.Now().UnixNano())
	targetID := pgx.Identifier{target}.Sanitize()
	if _, err = f.db.Exec(ctx, "CREATE DATABASE "+targetID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, e := f.db.Exec(cleanup, "DROP DATABASE "+targetID+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	}()
	restore := exec.CommandContext(ctx, "docker", "exec", "-i", container, "pg_restore", "--username", parsed.User.Username(), "--dbname", target, "--no-owner", "--no-privileges", "--exit-on-error")
	restore.Stdin = bytes.NewReader(archive)
	if output, e := restore.CombinedOutput(); e != nil {
		t.Fatalf("pg_restore: %v %.1000s", e, output)
	}
	config := f.db.Config().Copy()
	config.ConnConfig.Database = target
	restoredDB, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	restored, err := store.OpenPostgres(ctx, config.ConnConfig.ConnString())
	if err != nil {
		t.Fatal("restored readiness", err)
	}
	defer closePostgresStore(t, restored)
	if _, err = restoredDB.Exec(ctx, `UPDATE run_leases SET status='expired',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	replay, err := restored.AppendHostingBuildLogs(ctx, f.runner, in)
	if err != nil || len(replay.Receipts) != 1 || replay.Receipts[0] != ack.Receipts[0] {
		t.Fatalf("restored exact receipt %+v %v", replay, err)
	}
	page, err := restored.ListHostingBuildLogs(ctx, "usr_bootstrap", b.ID, 0, 200)
	if err != nil || len(page.Items) != 1 || !page.HistoryComplete || page.NextSequence != 1 {
		t.Fatalf("restored counter %+v %v", page, err)
	}
	tx, err := restoredDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL search_path=pg_catalog,pg_temp`); err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err = tx.QueryRow(ctx, "SELECT "+pgx.Identifier{schema, "nerocd_hosting_build_log_schema_compatible"}.Sanitize()+"()").Scan(&ready); err != nil || !ready {
		t.Fatalf("schema-bound0051 %v %v", ready, err)
	}
}
