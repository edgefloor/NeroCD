package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

func captureDispatchCounts(t *testing.T, f networkCaptureFixture) string {
	t.Helper()
	var result string
	if err := f.db.QueryRow(f.ctx, `SELECT jsonb_build_array((SELECT count(*) FROM run_leases WHERE run_id IN(SELECT run_id FROM hosting_database_network_captures)),(SELECT count(*) FROM audit_events WHERE action='hosting.database_network_capture.claimed'),(SELECT jsonb_agg(jsonb_build_array(t.id,t.status,t.next_attempt) ORDER BY t.id) FROM task_runs t JOIN hosting_database_network_captures c ON c.run_id=t.id))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Each extra candidate has a real settled source and its own owned volume.
func captureDispatchSource(t *testing.T, f networkCaptureFixture, n int) networkCaptureFixture {
	t.Helper()
	view, err := f.service.CreateHostingDatabase(f.ctx, domain.HostingDatabaseCreateInput{RequestID: fmt.Sprintf("%032x", n+1000), ProjectID: f.resource.ProjectID, ProjectEnvironmentID: f.resource.ProjectEnvironmentID, Name: fmt.Sprintf("capture queue %d", n), DestinationID: f.op.Snapshot.DestinationID, Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe})
	if err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(f.db).ReadHostingStorage(f.ctx, view.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	storage := domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	op, err := f.service.RequestHostingDatabaseDeployment(f.ctx, view.Resource.ID, databaseExecutionInput("capture_source"))
	if err != nil {
		t.Fatal(err)
	}
	source := prepareDatabaseExecution(t, storageMaterializationFixture{pg: f.pg, db: f.db, runner: f.runner, resource: view.Resource, storage: storage}, op)
	ack, err := source.pg.HostingStorageCreateBegin(f.ctx, source.runner, source.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.pg.HostingStorageMaterialized(f.ctx, source.runner, source.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = source.pg.HostingApplyBegin(f.ctx, source.runner, source.authority); err != nil {
		t.Fatal(err)
	}
	if _, err = source.pg.HostingComplete(f.ctx, source.runner, domain.HostingCompleteRequest{HostingAuthority: source.authority, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: domain.HostingDocumentHash(op.ID)[7:], ImageID: source.op.Release.ImageID, DaemonID: source.op.Release.DaemonID, PlanHash: source.op.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	return networkCaptureFixture{storageMaterializationFixture: source, service: f.service, ctx: f.ctx, credential: f.credential}
}

func TestPostgresDatabaseNetworkCaptureClaimNextLifecycle(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, engine)
			if _, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("empty capture queue", err)
			}
			c := f.request(t)
			claim, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute)
			if err != nil || claim.Capture.ID != c.ID || claim.Lease.Attempt != 1 {
				t.Fatal("queued capture was not selected", err)
			}
			before := captureDispatchCounts(t, f)
			replay, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute)
			if err != nil || !reflect.DeepEqual(replay, claim) || captureDispatchCounts(t, f) != before {
				t.Fatal("claim response loss minted new authority", err)
			}
			if _, err = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(claim)); err != nil {
				t.Fatal(err)
			}
			before = captureDispatchCounts(t, f)
			if _, err = f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute); !errors.Is(err, store.ErrNotFound) || captureDispatchCounts(t, f) != before {
				t.Fatal("committed origin selected again", err)
			}
		})
	}
}

func TestPostgresDatabaseNetworkCaptureClaimNextDoesNotResetExpiry(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	c := f.request(t)
	claim, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.Lease.ID); err != nil {
		t.Fatal(err)
	}
	before := captureDispatchCounts(t, f)
	for range 2 {
		if _, err = f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("expired capture automatically reclaimed", err)
		}
	}
	if captureDispatchCounts(t, f) != before {
		t.Fatal("automatic poll reset uncertain capture")
	}
	explicit := f.claim(t, c)
	if explicit.Lease.Attempt <= claim.Lease.Attempt || explicit.Lease.ID == claim.Lease.ID {
		t.Fatal("explicit recovery semantics changed")
	}
}

func TestPostgresDatabaseNetworkCaptureClaimNextConcurrentAndPriority(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	old := f.request(t)
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	type result struct {
		claim domain.HostingDatabaseNetworkCaptureClaim
		err   error
	}
	done := make(chan result, 2)
	joined := 0
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for joined < 2 {
			select {
			case <-done:
				joined++
			case <-cleanup.Done():
				t.Error("capture poll did not join")
				return
			}
		}
	}()
	for range 2 {
		go func() {
			c, e := f.pg.ClaimNextHostingDatabaseNetworkCapture(ctx, f.runner, f.credential, time.Minute)
			done <- result{c, e}
		}()
	}
	var selected domain.HostingDatabaseNetworkCaptureClaim
	for joined < 2 {
		select {
		case r := <-done:
			joined++
			if r.err == nil {
				if selected.Lease.ID != "" && selected.Lease.ID != r.claim.Lease.ID {
					t.Fatal("concurrent poll minted competing lease")
				}
				selected = r.claim
			} else if !errors.Is(r.err, store.ErrConflict) {
				t.Fatal(r.err)
			}
		case <-ctx.Done():
			t.Fatal("capture polling deadline")
		}
	}
	if selected.Capture.ID != old.ID {
		t.Fatal("neither poll selected queued capture")
	}
	// Complete that capture before constructing two distinct queued sources.
	if _, err := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(selected)); err != nil {
		t.Fatal(err)
	}
	older := captureDispatchSource(t, f, 1)
	queued := older.request(t)
	newer := captureDispatchSource(t, f, 2)
	running := newer.claim(t, newer.request(t))
	replay, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute)
	if err != nil || replay.Lease.ID != running.Lease.ID || replay.Capture.ID == queued.ID {
		t.Fatal("live response replay did not precede older queued work", err)
	}
}

func TestPostgresDatabaseNetworkCaptureClaimNextBoundedContention(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	first := f.request(t)
	for n := 1; n < 33; n++ {
		next := captureDispatchSource(t, f, n)
		next.request(t)
	}
	ids, err := sqlcgen.New(f.db).ListDatabaseNetworkCaptureCandidates(f.ctx, sqlcgen.ListDatabaseNetworkCaptureCandidatesParams{RunnerID: f.runner, CredentialHash: f.credential})
	if err != nil || len(ids) != 33 || ids[0] != first.ID {
		t.Fatal("bounded candidate fixture is not eligible and ordered", err)
	}
	blocker, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Error(e)
		}
	}()
	for _, id := range ids[:32] {
		if _, err = blocker.Exec(f.ctx, `SELECT id FROM task_runs WHERE id=$1 FOR UPDATE`, "capture_"+id); err != nil {
			t.Fatal(err)
		}
	}
	before := captureDispatchCounts(t, f)
	if _, err = f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute); !errors.Is(err, store.ErrConflict) {
		t.Fatal("bounded/contended selection falsely reported queue exhaustion", err)
	}
	if captureDispatchCounts(t, f) != before {
		t.Fatal("bounded scan modified contended work")
	}
	if err = blocker.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if claim, e := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute); e != nil || claim.Capture.ID != first.ID {
		t.Fatal("released candidate was not selectable", e)
	}
}

func TestPostgresDatabaseNetworkCaptureClaimNextRevocationAfterSelection(t *testing.T) {
	for _, path := range []string{"source", "grant", "capability", "credential", "enrollment", "actor"} {
		t.Run(path, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, "postgresql")
			actor, _ := proxyLegacyToken(t, f.pg, f.service, "tok_capture_next", []string{domain.RoleSystemAdmin})
			if _, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID, actor.ID, destinationEvent("capture_next")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
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
					t.Error(e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("selected capture did not join")
					}
				}
			}()
			var pid int
			if blocker.QueryRow(ctx, `SELECT pg_backend_pid(),pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.op.Snapshot.DestinationID).Scan(&pid, new(any)) != nil {
				t.Fatal("destination blocker failed")
			}
			before := captureDispatchCounts(t, f)
			started = true
			go func() {
				_, e := f.pg.ClaimNextHostingDatabaseNetworkCapture(ctx, f.runner, f.credential, time.Minute)
				done <- e
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
					t.Fatal("selection wait probe failed")
				}
				if waiting {
					break
				}
				select {
				case e := <-done:
					joined = true
					t.Fatal("capture did not reach authoritative lock check", e)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("selection deadline")
				}
			}
			switch path {
			case "source":
				_, err = blocker.Exec(ctx, `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.resource.ID)
			case "grant":
				_, err = blocker.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, f.op.Snapshot.DestinationID, f.op.ProjectID)
			case "capability":
				_, err = blocker.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability)
			case "credential":
				_, err = blocker.Exec(ctx, `UPDATE runners SET token_hash=$2 WHERE id=$1`, f.runner, strings.Repeat("f", 64))
			case "enrollment":
				_, err = blocker.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner)
			case "actor":
				_, err = blocker.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, actor.ID)
			}
			if err != nil {
				t.Fatal("revocation setup", err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				joined = true
				if !errors.Is(e, store.ErrNotFound) {
					t.Fatal("revoked selected capture did not fail closed", e)
				}
			case <-ctx.Done():
				t.Fatal("selection did not join")
			}
			if captureDispatchCounts(t, f) != before {
				t.Fatal("revocation minted capture authority")
			}
			if _, err = f.pg.ClaimNextHostingDatabaseNetworkCapture(ctx, f.runner, f.credential, time.Minute); !errors.Is(err, store.ErrNotFound) || captureDispatchCounts(t, f) != before {
				t.Fatal("already revoked candidate remained selectable", err)
			}
		})
	}
}

func TestPostgresDatabaseNetworkCaptureClaimNextFinalExpiry(t *testing.T) {
	for _, path := range []string{"actor", "lease"} {
		t.Run(path, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, "postgresql")
			actor, _ := proxyLegacyToken(t, f.pg, f.service, "tok_capture_next_expiry", []string{domain.RoleSystemAdmin})
			if _, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID, actor.ID, destinationEvent("capture_next_expiry")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
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
					t.Error(e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("capture expiry waiter did not join")
					}
				}
			}()
			var pid int
			if blocker.QueryRow(ctx, `SELECT pg_backend_pid(),pg_advisory_xact_lock(hashtextextended(current_schema()||':capture-next-expiry',0))`).Scan(&pid, new(any)) != nil {
				t.Fatal("expiry blocker failed")
			}
			if _, err = f.db.Exec(ctx, `CREATE FUNCTION capture_next_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_network_capture.claimed' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':capture-next-expiry',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER capture_next_expiry BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION capture_next_expiry()`); err != nil {
				t.Fatal(err)
			}
			var expiry time.Time
			if path == "actor" {
				if f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1 RETURNING expires_at`, actor.ID).Scan(&expiry) != nil {
					t.Fatal("actor expiry setup failed")
				}
			}
			before := captureDispatchCounts(t, f)
			ttl := time.Minute
			if path == "lease" {
				ttl = time.Second
			}
			started = true
			go func() {
				_, e := f.pg.ClaimNextHostingDatabaseNetworkCapture(ctx, f.runner, f.credential, ttl)
				done <- e
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
					t.Fatal("expiry wait probe failed")
				}
				if waiting {
					break
				}
				select {
				case e := <-done:
					joined = true
					t.Fatal("claim did not reach final audit wait", e)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("expiry barrier deadline")
				}
			}
			if path == "lease" {
				// The uncommitted lease was created before the observed audit wait.
				// Waiting a full TTL after that observation proves it has expired.
				if f.db.QueryRow(ctx, `SELECT clock_timestamp()+interval '1100 milliseconds'`).Scan(&expiry) != nil {
					t.Fatal("lease expiry observation failed")
				}
			}
			for {
				var expired bool
				if f.db.QueryRow(ctx, `SELECT clock_timestamp()>$1`, expiry).Scan(&expired) != nil {
					t.Fatal("expiry clock failed")
				}
				if expired {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("expiry clock deadline")
				}
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				joined = true
				if !errors.Is(e, store.ErrNotFound) {
					t.Fatal("expired final authority committed claim", e)
				}
			case <-ctx.Done():
				t.Fatal("expiry waiter deadline")
			}
			if captureDispatchCounts(t, f) != before {
				t.Fatal("expired authority retained claim or audit")
			}
		})
	}
}
