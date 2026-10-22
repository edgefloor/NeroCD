package hostingjobs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	address := os.Getenv("NEROCD_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL for River PostgreSQL tests")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("river_jobs_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func migrateTestPool(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		applied, err := MigrateNextTx(t.Context(), tx)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !applied {
			return
		}
	}
}
func newTestClient(t *testing.T, pool *pgxpool.Pool, handler Handler, cfg Config) *Client {
	t.Helper()
	client, err := New(t.Context(), pool, handler, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Cancel(ctx); err != nil {
			t.Errorf("cancel client: %v", err)
		}
	})
	return client
}
func insertTestJob(t *testing.T, client *Client, pool *pgxpool.Pool, id string) int64 {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	job, err := client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return job
}
func waitJobState(t *testing.T, pool *pgxpool.Pool, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := pool.QueryRow(t.Context(), "SELECT state::text FROM river_job WHERE id=$1", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d did not reach %s", id, want)
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker handshake")
	}
}

func TestTransactionalVisibilityRollbackAndQueueDedup(t *testing.T) {
	pool := testPool(t)
	migrateTestPool(t, pool)
	if _, err := pool.Exec(t.Context(), "CREATE TABLE fixture_operations(id text PRIMARY KEY,prepared boolean NOT NULL DEFAULT false)"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	handlerErrors := make(chan error, 4)
	client := newTestClient(t, pool, HandlerFunc(func(ctx context.Context, id string) error {
		tag, err := pool.Exec(ctx, "UPDATE fixture_operations SET prepared=true WHERE id=$1", id)
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("missing committed operation")
		}
		handlerErrors <- err
		started <- struct{}{}
		return err
	}), Config{})
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), "INSERT INTO fixture_operations(id)VALUES('operation_commit')"); err != nil {
		t.Fatal(err)
	}
	jobID, err := client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: "operation_commit"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: "operation_commit"})
	if err != nil || same != jobID {
		t.Fatalf("transaction dedup %d %d %v", jobID, same, err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job").Scan(&count); err != nil || count != 0 {
		t.Fatal("job visible before commit")
	}
	select {
	case <-started:
		t.Fatal("handler ran before commit")
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	if err := <-handlerErrors; err != nil {
		t.Fatal(err)
	}
	waitJobState(t, pool, jobID, "completed")
	// Active-state dedup intentionally does not replace durable business idempotency.
	next := insertTestJob(t, client, pool, "operation_commit")
	if next == jobID {
		t.Fatal("completed job still blocked new queue insert")
	}
	await(t, started)
	if err := <-handlerErrors; err != nil {
		t.Fatal(err)
	}
	waitJobState(t, pool, next, "completed")
	rollback, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rollback.Exec(t.Context(), "INSERT INTO fixture_operations(id)VALUES('operation_rollback')"); err != nil {
		t.Fatal(err)
	}
	rollbackID, err := client.EnqueueTx(t.Context(), rollback, PrepareDeploymentArgs{OperationID: "operation_rollback"})
	if err != nil {
		t.Fatal(err)
	}
	if err = rollback.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE id=$1", rollbackID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled-back job survived")
	}
	select {
	case <-started:
		t.Fatal("rolled-back handler ran")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWorkerFiniteRetryAndSanitizedPersistence(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			migrateTestPool(t, pool)
			var attempts atomic.Int32
			client := newTestClient(t, pool, HandlerFunc(func(context.Context, string) error {
				attempts.Add(1)
				if mode == "panic" {
					panic("private-handler-secret")
				}
				if mode == "invalid" {
					return fmt.Errorf("private-handler-secret: %w", ErrInvalidOperation)
				}
				return errors.New("private-handler-secret")
			}), Config{})
			job := insertTestJob(t, client, pool, "operation_"+mode)
			state := "discarded"
			wantAttempts := int32(3)
			if mode == "invalid" {
				state = "cancelled"
				wantAttempts = 1
			}
			waitJobState(t, pool, job, state)
			var stored, kind, queue, args string
			var max int
			if err := pool.QueryRow(t.Context(), "SELECT errors::text,kind,queue,args::text,max_attempts FROM river_job WHERE id=$1", job).Scan(&stored, &kind, &queue, &args, &max); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "private-handler-secret") || !strings.Contains(stored, "hosting_") || kind != PrepareDeploymentKind || queue != Queue || max != 3 || attempts.Load() != wantAttempts || strings.Contains(args, "private") {
				t.Fatalf("unsafe or unbounded job state: errors=%s attempts=%d max=%d", stored, attempts.Load(), max)
			}
		})
	}
}

func TestLifecycleDrainAndCancellation(t *testing.T) {
	for _, mode := range []string{"drain", "escalate", "cancel", "caller deadline"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			migrateTestPool(t, pool)
			started := make(chan struct{})
			cancelled := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			handler := HandlerFunc(func(ctx context.Context, _ string) error {
				close(started)
				defer close(finished)
				if mode == "caller deadline" {
					<-release
					return nil
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					close(cancelled)
					return ctx.Err()
				}
			})
			drain := 100 * time.Millisecond
			if mode == "cancel" {
				drain = time.Second
			}
			client := newTestClient(t, pool, handler, Config{DrainTimeout: drain})
			insertTestJob(t, client, pool, "operation_lifecycle")
			await(t, started)
			if mode == "caller deadline" {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				defer cancel()
				if err := client.Stop(ctx); !errors.Is(err, ErrStopTimeout) {
					t.Fatalf("noncooperative bounded stop: %v", err)
				}
				close(release)
				await(t, finished)
				if err := client.Cancel(t.Context()); err != nil {
					t.Fatal(err)
				}
				await(t, client.Stopped())
				return
			}
			done := make(chan error, 1)
			go func() {
				if mode == "cancel" {
					done <- client.Cancel(t.Context())
				} else {
					done <- client.Stop(t.Context())
				}
			}()
			if mode == "drain" {
				select {
				case <-cancelled:
					t.Fatal("draining handler cancelled early")
				case err := <-done:
					t.Fatalf("stop returned before handler finish: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				close(release)
			} else {
				await(t, cancelled)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("bounded lifecycle did not stop")
			}
			await(t, finished)
			await(t, client.Stopped())
		})
	}
}

func TestConfigurationAndMissingMigrationFailClosed(t *testing.T) {
	if _, err := New(t.Context(), nil, nil, Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing handler/pool accepted")
	}
	pool := testPool(t)
	handler := HandlerFunc(func(context.Context, string) error { return nil })
	client, err := New(t.Context(), pool, handler, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Start(t.Context()); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("unmigrated worker started: %v", err)
	}
	for _, cfg := range []Config{{MaxWorkers: 9}, {DrainTimeout: -1}, {JobTimeout: 6 * time.Minute}, {Schema: "different"}} {
		if _, err := New(t.Context(), pool, handler, cfg); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatal("unsafe config accepted")
		}
	}
	migrateTestPool(t, pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: "private value"}); !errors.Is(err, ErrInvalidOperation) {
		t.Fatal("invalid operation accepted")
	}
	if _, err = tx.Exec(t.Context(), "SET LOCAL search_path=public"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: "operation_other_schema"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("foreign schema transaction accepted")
	}
}

func TestConcurrentActiveQueueUniqueness(t *testing.T) {
	pool := testPool(t)
	migrateTestPool(t, pool)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWork := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWork()
	var attempts atomic.Int32
	client := newTestClient(t, pool, HandlerFunc(func(ctx context.Context, _ string) error {
		if attempts.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), Config{})
	first := insertTestJob(t, client, pool, "operation_concurrent")
	await(t, started)
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			id, err := client.EnqueueTx(t.Context(), tx, PrepareDeploymentArgs{OperationID: "operation_concurrent"})
			if err == nil {
				err = tx.Commit(t.Context())
			}
			results <- result{id, err}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.err != nil || r.id != first {
			t.Fatalf("concurrent queue dedup: %+v first=%d", r, first)
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job").Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent queue duplicates persisted")
	}
	releaseWork()
	waitJobState(t, pool, first, "completed")
	if attempts.Load() != 1 {
		t.Fatal("duplicate handler invocation")
	}
}
