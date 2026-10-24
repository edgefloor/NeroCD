package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

type networkCaptureFixture struct {
	storageMaterializationFixture
	service    *app.Service
	ctx        context.Context
	credential string
}

func TestPostgresDatabaseNetworkCaptureAuthorityAfterWait(t *testing.T) {
	for _, path := range []string{"request_audit_expiry", "request_deferred_expiry", "record_actor_expiry", "record_lease_expiry", "record_deferred_lease_expiry", "replay_actor_expiry", "grant_revocation", "runner_revocation", "actor_revocation", "lease_revocation"} {
		t.Run(path, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, "postgresql")
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			principal, _ := proxyLegacyToken(t, f.pg, f.service, "tok_capture_wait", []string{domain.RoleSystemAdmin})
			var c domain.HostingDatabaseNetworkCapture
			var claim domain.HostingDatabaseNetworkCaptureClaim
			var in domain.HostingDatabaseNetworkOriginRequest
			request := strings.HasPrefix(path, "request_")
			if !request {
				var err error
				c, err = f.pg.RequestHostingDatabaseNetworkCapture(ctx, f.op.ID, principal.ID, destinationEvent("capture_wait_request"))
				if err != nil {
					t.Fatal(err)
				}
				claim = f.claim(t, c)
				in = captureOriginRequest(claim)
			}
			call := func() error {
				if request {
					_, err := f.pg.RequestHostingDatabaseNetworkCapture(ctx, f.op.ID, principal.ID, destinationEvent("capture_wait_request"))
					return err
				}
				_, err := f.pg.RecordHostingDatabaseNetworkOrigin(ctx, f.runner, f.credential, in)
				return err
			}
			if path == "replay_actor_expiry" {
				if err := call(); err != nil {
					t.Fatal(err)
				}
			}
			before := captureCounts(t, f)
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
					t.Error("capture blocker rollback failed")
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("capture authority request did not join")
					}
				}
			}()
			var key int64
			var pid int
			if blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':capture-wait',0),pg_backend_pid()`).Scan(&key, &pid) != nil {
				t.Fatal("capture barrier identity failed")
			}
			switch path {
			case "grant_revocation", "runner_revocation", "actor_revocation", "lease_revocation":
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.op.Snapshot.DestinationID)
			case "replay_actor_expiry":
				_, err = blocker.Exec(ctx, `LOCK TABLE hosting_database_network_origin_receipts IN ACCESS EXCLUSIVE MODE`)
			default:
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key)
				if err != nil {
					t.Fatal(err)
				}
				ddl := `CREATE FUNCTION capture_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'hosting.database_network_capture.%' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':capture-wait',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER capture_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION capture_wait()`
				if strings.Contains(path, "deferred") {
					table := "hosting_database_network_origin_receipts"
					if request {
						table = "hosting_database_network_captures"
					}
					ddl = `CREATE FUNCTION capture_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':capture-wait',0)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER capture_wait AFTER INSERT ON ` + table + ` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION capture_wait()`
				}
				_, err = f.db.Exec(ctx, ddl)
			}
			if err != nil {
				t.Fatal("capture barrier setup failed", err)
			}
			var expiry time.Time
			if strings.Contains(path, "lease_expiry") {
				if f.db.QueryRow(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, claim.Lease.ID).Scan(&expiry) != nil {
					t.Fatal("lease expiry setup failed")
				}
			} else if strings.Contains(path, "expiry") {
				if f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry) != nil {
					t.Fatal("actor expiry setup failed")
				}
			}
			started = true
			go func() { done <- call() }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
					t.Fatal("capture barrier observation failed")
				}
				if waiting {
					break
				}
				select {
				case <-done:
					joined = true
					t.Fatal("capture did not reach barrier")
				case <-ctx.Done():
					t.Fatal("capture barrier deadline")
				case <-ticker.C:
				}
			}
			switch path {
			case "grant_revocation":
				_, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.op.Snapshot.DestinationID)
			case "runner_revocation":
				_, err = f.db.Exec(ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner)
			case "lease_revocation":
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET status='canceled' WHERE id=$1`, claim.Lease.ID)
			case "actor_revocation":
				_, err = f.db.Exec(ctx, `UPDATE api_tokens SET revoked_at=clock_timestamp(),status='revoked' WHERE id=$1`, principal.ID)
			default:
				for {
					var expired bool
					if f.db.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expiry).Scan(&expired) != nil {
						t.Fatal("capture expiry clock failed")
					}
					if expired {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("capture expiry deadline")
					case <-ticker.C:
					}
				}
			}
			if err != nil {
				t.Fatal("capture revocation failed", err)
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
				if err == nil {
					t.Fatal("expired/revoked authority survived wait")
				}
			case <-ctx.Done():
				t.Fatal("capture request did not join")
			}
			if captureCounts(t, f) != before {
				t.Fatal("expired capture left receipt/audit/terminal side effects")
			}
		})
	}
}

func newNetworkCaptureFixture(t *testing.T, engine string) networkCaptureFixture {
	t.Helper()
	f, service, ctx := databaseExecutionFixture(t, engine)
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("capture_source"))
	if err != nil {
		t.Fatal(err)
	}
	f = prepareDatabaseExecution(t, f, op)
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "capture_source_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("a", 64), ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
		t.Fatal(err)
	}
	return networkCaptureFixture{storageMaterializationFixture: f, service: service, ctx: ctx, credential: fmt.Sprintf("%x", sha256.Sum256([]byte("image-hosting-fixture-token")))}
}
func (f networkCaptureFixture) request(t *testing.T) domain.HostingDatabaseNetworkCapture {
	t.Helper()
	c, e := f.service.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID)
	if e != nil {
		t.Fatal("capture request failed", e)
	}
	return c
}
func (f networkCaptureFixture) claim(t *testing.T, c domain.HostingDatabaseNetworkCapture) domain.HostingDatabaseNetworkCaptureClaim {
	t.Helper()
	a, e := f.pg.ClaimHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, c.ID, time.Minute)
	if e != nil {
		t.Fatal("capture claim failed", e)
	}
	return a
}
func captureAuthority(c domain.HostingDatabaseNetworkCaptureClaim) domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: c.Capture.ID, RunID: c.Lease.RunID, LeaseID: c.Lease.ID, Attempt: c.Lease.Attempt, Fence: c.Lease.Fence}
}
func captureOriginRequest(c domain.HostingDatabaseNetworkCaptureClaim) domain.HostingDatabaseNetworkOriginRequest {
	p := c.Capture.Plan
	m := p.Materialization
	return domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: captureAuthority(c), ReceiptID: "captured_origin", PlanHash: p.Hash, ObservedAt: time.Now().UTC(), Origin: domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: m.DaemonID, Configuration: p.SourcePlan.Snapshot.Database.ConfigurationHash, OriginOperation: m.CreationOperationID, OriginPlan: m.CreationPlanHash, Nonce: strings.Repeat("b", 32), Storage: m.StorageRef, VolumeNonce: m.CreationNonce, VolumeCreatedAt: "2026-10-23T00:00:00Z", NetworkID: strings.Repeat("c", 64), InitReady: true}}
}
func captureCounts(t *testing.T, f networkCaptureFixture) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	var out string
	if f.db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_database_network_captures),(SELECT count(*) FROM hosting_database_network_origin_receipts),(SELECT count(*) FROM audit_events WHERE action LIKE 'hosting.database_network_capture.%'),(SELECT count(*) FROM run_leases WHERE run_id LIKE 'capture_%' AND status='succeeded'),(SELECT count(*) FROM task_runs WHERE id LIKE 'capture_%' AND status='succeeded'))::text`).Scan(&out) != nil {
		t.Fatal("capture counts failed")
	}
	return out
}

func TestPostgresDatabaseNetworkCaptureBothEnginesAndHistoricalReplay(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, engine)
			source, e := f.pg.GetHostingOperation(f.ctx, f.op.ID)
			if e != nil {
				t.Fatal(e)
			}
			c := f.request(t)
			if c.Plan.Validate() != nil || c.Receipt != nil || c.Plan.Materialization.MaterializedReceipt == nil {
				t.Fatal("capture omitted creation evidence")
			}
			if _, e = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); !errors.Is(e, store.ErrNotFound) {
				t.Fatal("generic runner claimed read-only capture", e)
			}
			replay := f.request(t)
			if !reflect.DeepEqual(replay, c) {
				t.Fatal("request replay changed capture")
			}
			claim := f.claim(t, c)
			if claim.Lease.ID == f.authority.LeaseID || claim.Lease.Fence == f.authority.Fence {
				t.Fatal("historical deployment lease reused")
			}
			if _, e = f.pg.HostingDatabaseNetworkCapturePlan(f.ctx, f.runner, f.credential, captureAuthority(claim)); e != nil {
				t.Fatal(e)
			}
			input := captureOriginRequest(claim)
			receipt, e := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, input)
			if e != nil {
				t.Fatal("origin record failed", e)
			}
			before := captureCounts(t, f)
			if _, e = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.Lease.ID); e != nil {
				t.Fatal(e)
			}
			again, e := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, input)
			if e != nil || !reflect.DeepEqual(again, receipt) || captureCounts(t, f) != before {
				t.Fatal("historical receipt replay changed observation/audit", e)
			}
			final := f.request(t)
			if final.Receipt == nil || !final.Receipt.ObservedAt.Equal(input.ObservedAt) || !final.Receipt.CreatedAt.Equal(receipt.CreatedAt) {
				t.Fatal("request replay claimed a fresh observation")
			}
			if _, e = f.pg.HostingDatabaseNetworkCapturePlan(f.ctx, f.runner, f.credential, captureAuthority(claim)); e == nil {
				t.Fatal("historical lease regained observation permission")
			}
			input.Origin.NetworkID = strings.Repeat("d", 64)
			if _, e = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, input); e == nil {
				t.Fatal("committed origin overwritten")
			}
			after, e := f.pg.GetHostingOperation(f.ctx, f.op.ID)
			if e != nil || !reflect.DeepEqual(source, after) {
				t.Fatal("capture changed source hosting state")
			}
			raw, _ := json.Marshal(receipt)
			if strings.Contains(string(raw), `"fence"`) || strings.Contains(string(raw), `"lease_id"`) {
				t.Fatal("receipt exposed authority")
			}
		})
	}
}

func TestPostgresDatabaseNetworkCaptureClaimsAndStaleAttempt(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	f.ctx = ctx
	c := f.request(t)
	claims := make([]domain.HostingDatabaseNetworkCaptureClaim, 2)
	errs := make([]error, 2)
	type outcome struct {
		index int
		claim domain.HostingDatabaseNetworkCaptureClaim
		err   error
	}
	// Two callers report once each; cancellation cleanup drains both results.
	done := make(chan outcome, 2)
	started, joined := 0, 0
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for joined < started {
			select {
			case <-done:
				joined++
			case <-cleanup.Done():
				t.Error("simultaneous capture claim did not join")
				return
			}
		}
	}()
	for i := range claims {
		started++
		go func() {
			claim, err := f.pg.ClaimHostingDatabaseNetworkCapture(ctx, f.runner, f.credential, c.ID, time.Minute)
			done <- outcome{i, claim, err}
		}()
	}
	for joined < started {
		select {
		case result := <-done:
			joined++
			claims[result.index], errs[result.index] = result.claim, result.err
		case <-ctx.Done():
			t.Fatal("simultaneous capture claim deadline")
		}
	}
	if errs[0] != nil || errs[1] != nil || claims[0].Lease.ID != claims[1].Lease.ID {
		t.Fatal("simultaneous claims minted competing leases", errs)
	}
	old := captureOriginRequest(claims[0])
	if _, e := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, old.LeaseID); e != nil {
		t.Fatal(e)
	}
	next := f.claim(t, c)
	if next.Lease.Attempt <= claims[0].Lease.Attempt || next.Lease.Fence == old.Fence {
		t.Fatal("expired attempt did not get new authority")
	}
	before := captureCounts(t, f)
	if _, e := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, old); e == nil || before != captureCounts(t, f) {
		t.Fatal("stale publication accepted")
	}
	if _, e := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(next)); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresDatabaseNetworkCaptureBlockedClaimsCancelAndJoin(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	c := f.request(t)
	ctx, stop := context.WithTimeout(f.ctx, 15*time.Second)
	defer stop()
	claims, cancel := context.WithCancel(ctx)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Both blocked calls can finish without a receiver after cancellation.
	done := make(chan error, 2)
	started, joined := 0, 0
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := blocker.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error("blocked claim rollback failed")
		}
		for joined < started {
			select {
			case <-done:
				joined++
			case <-cleanup.Done():
				t.Error("blocked capture claim did not join")
				return
			}
		}
	}()
	before := captureCounts(t, f)
	var pid int
	if blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid) != nil {
		t.Fatal("blocked claim identity unavailable")
	}
	if _, err = blocker.Exec(ctx, `SELECT id FROM task_runs WHERE id=$1 FOR UPDATE`, c.RunID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		started++
		go func() {
			_, err := f.pg.ClaimHostingDatabaseNetworkCapture(claims, f.runner, f.credential, c.ID, time.Minute)
			done <- err
		}()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		// A row-lock waiter can queue behind the first waiter rather than the
		// original blocker. Include that bounded two-caller wait chain.
		if f.db.QueryRow(ctx, `WITH RECURSIVE waiters(pid) AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT a.pid FROM pg_stat_activity a JOIN waiters w ON w.pid=ANY(pg_blocking_pids(a.pid))
) SELECT count(*) FROM waiters`, pid).Scan(&waiting) != nil {
			t.Fatal("blocked claim observation failed")
		}
		if waiting == 2 {
			break
		}
		select {
		case <-done:
			joined++
			t.Fatal("claim did not reach its barrier")
		case <-ctx.Done():
			t.Fatal("blocked claim barrier deadline")
		case <-ticker.C:
		}
	}
	cancel()
	// The blocker remains held: both calls must join because they were canceled.
	for joined < started {
		select {
		case err := <-done:
			joined++
			if !errors.Is(err, context.Canceled) {
				t.Error("blocked claim did not return cancellation")
			}
		case <-ctx.Done():
			t.Fatal("canceled blocked claim join deadline")
		}
	}
	if captureCounts(t, f) != before {
		t.Fatal("canceled blocked claims created effects")
	}
}

func TestPostgresDatabaseNetworkCaptureConcurrentRequestsConverge(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, engine)
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				capture domain.HostingDatabaseNetworkCapture
				err     error
			}
			// Exactly two callers each report once; cleanup must drain both.
			done := make(chan outcome, 2)
			started, joined := 0, 0
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := blocker.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error("request barrier rollback failed")
				}
				for joined < started {
					select {
					case <-done:
						joined++
					case <-cleanup.Done():
						t.Error("capture request did not join")
						return
					}
				}
			}()
			var pid int
			if blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid) != nil {
				t.Fatal("request barrier identity unavailable")
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.op.Snapshot.DestinationID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				started++
				go func() {
					capture, err := f.pg.RequestHostingDatabaseNetworkCapture(ctx, f.op.ID, "usr_bootstrap", destinationEvent("same_capture"))
					done <- outcome{capture, err}
				}()
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting int
				if f.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&waiting) != nil {
					t.Fatal("request barrier observation failed")
				}
				if waiting == 2 {
					break
				}
				select {
				case <-done:
					joined++
					t.Fatal("request returned before both reached the barrier")
				case <-ctx.Done():
					t.Fatal("both requests did not reach destination barrier")
				case <-ticker.C:
				}
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			var accepted []domain.HostingDatabaseNetworkCapture
			for joined < started {
				select {
				case result := <-done:
					joined++
					if result.err != nil {
						t.Errorf("identical concurrent request failed: %v", result.err)
					} else {
						accepted = append(accepted, result.capture)
					}
				case <-ctx.Done():
					t.Fatal("concurrent request join deadline")
				}
			}
			if len(accepted) != 2 || !reflect.DeepEqual(accepted[0], accepted[1]) {
				t.Error("concurrent requests did not return the same accepted capture")
			}
			var captures, tasks, audits, receipts, leases int
			id := domain.HostingDatabaseNetworkCaptureID(f.op.ID)
			if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_database_network_captures WHERE id=$1),(SELECT count(*) FROM task_runs WHERE id='capture_'||$1::text),(SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='hosting.database_network_capture.requested'),(SELECT count(*) FROM hosting_database_network_origin_receipts WHERE capture_id=$1),(SELECT count(*) FROM run_leases WHERE run_id='capture_'||$1::text)`, id).Scan(&captures, &tasks, &audits, &receipts, &leases); err != nil {
				t.Fatal(err)
			}
			if captures != 1 || tasks != 1 || audits != 1 || receipts != 0 || leases != 0 {
				t.Fatal("concurrent request created duplicate or premature effects")
			}
		})
	}
}

func TestPostgresDatabaseNetworkCaptureHistoricalOriginSurvivesRedeploy(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, engine)
			c := f.request(t)
			claim := f.claim(t, c)
			in := captureOriginRequest(claim)
			receipt, err := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, in)
			if err != nil {
				t.Fatal(err)
			}
			next, err := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("capture_later_source"))
			if err != nil {
				t.Fatal(err)
			}
			replacement := prepareDatabaseExecution(t, f.storageMaterializationFixture, next)
			ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, replacement.request())
			if err != nil || ack.CreatePermitted {
				t.Fatal("later source recreated storage", err)
			}
			if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, replacement.proof(ack)); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, replacement.authority); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: replacement.authority, ReceiptID: "later_source_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("d", 64), ImageID: replacement.op.Release.ImageID, DaemonID: replacement.op.Release.DaemonID, PlanHash: replacement.op.Plan.Hash}}); err != nil {
				t.Fatal(err)
			}
			before := captureCounts(t, f)
			replay, err := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, in)
			if err != nil || !reflect.DeepEqual(replay, receipt) {
				t.Fatal("later source invalidated historical origin", err)
			}
			if got := f.request(t); got.Receipt == nil || !reflect.DeepEqual(*got.Receipt, receipt) {
				t.Fatal("request replay lost historical receipt")
			}
			if _, err = f.pg.ClaimHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, c.ID, time.Minute); err == nil {
				t.Fatal("historical receipt granted fresh observation")
			}
			changed := in
			changed.Origin.Nonce = strings.Repeat("e", 32)
			if _, err = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, changed); err == nil {
				t.Fatal("historical body changed")
			}
			if captureCounts(t, f) != before {
				t.Fatal("historical replay created lease/audit/terminal effects")
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, c.Plan.DestinationID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, in); err == nil {
				t.Fatal("revoked grant replayed retained origin")
			}
		})
	}
}

func TestPostgresDatabaseNetworkCaptureOwnerAndSourceAdmission(t *testing.T) {
	f := newNetworkCaptureFixture(t, "postgresql")
	if _, err := f.db.Exec(f.ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('capture_viewer','proj_platform','usr_viewer','viewer')`); err != nil {
		t.Fatal(err)
	}
	before := captureCounts(t, f)
	if _, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID, "usr_viewer", destinationEvent("viewer_capture")); err == nil || captureCounts(t, f) != before {
		t.Fatal("viewer captured source")
	}
	if _, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, "missing_source", "usr_bootstrap", destinationEvent("missing_capture")); err == nil || captureCounts(t, f) != before {
		t.Fatal("unproven source captured")
	}
	c := f.request(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE project_members SET role='owner' WHERE id='capture_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID, "usr_viewer", destinationEvent("other_capture_actor")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed request actor replayed capture", err)
	}
	if _, err := f.pg.ClaimHostingDatabaseNetworkCapture(f.ctx, f.runner, "wrong", c.ID, time.Minute); err == nil {
		t.Fatal("wrong bearer claimed capture")
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.ClaimHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, c.ID, time.Minute); err == nil {
		t.Fatal("legacy runner claimed capture")
	}
}

func TestPostgresDatabaseNetworkCaptureDenialsAndRollback(t *testing.T) {
	for _, denial := range []string{"daemon", "origin", "storage", "configuration", "receipt", "runner", "bearer", "capability", "enrollment", "grant", "source_changed", "mutation_hold", "restore_hold", "canceled", "expired", "audit_failure"} {
		t.Run(denial, func(t *testing.T) {
			f := newNetworkCaptureFixture(t, "redis")
			c := f.request(t)
			claim := f.claim(t, c)
			in := captureOriginRequest(claim)
			runner, credential := f.runner, f.credential
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := f.db.Exec(f.ctx, sql, args...); err != nil {
					t.Fatal("denial setup failed", err)
				}
			}
			switch denial {
			case "daemon":
				in.Origin.Daemon = "foreign"
			case "origin":
				in.Origin.OriginPlan = "sha256:" + strings.Repeat("d", 64)
			case "storage":
				in.Origin.Storage.Generation++
			case "configuration":
				in.Origin.Configuration = "sha256:" + strings.Repeat("d", 64)
			case "receipt":
				in.PlanHash = "sha256:" + strings.Repeat("d", 64)
			case "runner":
				runner = "other"
			case "bearer":
				credential = "other"
			case "capability":
				exec(`UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability)
			case "enrollment":
				exec(`UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner)
			case "grant":
				exec(`UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, c.Plan.DestinationID)
			case "source_changed":
				if _, err := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("replacement")); err != nil {
					t.Fatal(err)
				}
			case "mutation_hold":
				exec(`UPDATE hosting_binding_state SET hold_operation_id=$1 WHERE binding_id=$2`, f.op.ID, f.op.BindingID)
			case "restore_hold":
				exec(`INSERT INTO hosting_binding_restore_holds(binding_id) VALUES($1)`, f.op.BindingID)
			case "canceled":
				exec(`UPDATE run_leases SET status='canceled' WHERE id=$1`, claim.Lease.ID)
			case "expired":
				exec(`UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.Lease.ID)
			case "audit_failure":
				exec(`CREATE FUNCTION capture_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_network_capture.recorded' THEN RAISE EXCEPTION 'capture fixture audit failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER capture_audit_failure BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION capture_audit_failure()`)
			}
			before := captureCounts(t, f)
			if _, err := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, runner, credential, in); err == nil || captureCounts(t, f) != before {
				t.Fatal("denied publication left evidence/audit/terminal effects")
			}
		})
	}
}
