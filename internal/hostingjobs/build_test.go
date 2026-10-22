package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type buildTestHandler struct {
	build func(context.Context, string) error
}

func (*buildTestHandler) PrepareDeployment(context.Context, string) error { return ErrInvalidOperation }
func (h *buildTestHandler) PrepareBuild(ctx context.Context, id string) error {
	return h.build(ctx, id)
}

func insertBuildJob(t *testing.T, client *Client, pool *pgxpool.Pool, id string) int64 {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	job, err := client.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestBuildProtocolAndSanitizedWorker(t *testing.T) {
	args := PrepareBuildArgs{BuildID: "build_one"}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != `{"build_id":"build_one"}` || args.Kind() != PrepareBuildKind {
		t.Fatalf("invalid build protocol %s %v", encoded, err)
	}
	calls := 0
	worker := prepareBuildWorker{handler: &buildTestHandler{build: func(context.Context, string) error { calls++; return nil }}}
	if err = worker.Work(t.Context(), &river.Job[PrepareBuildArgs]{Args: args}); err != nil || calls != 1 {
		t.Fatal("supplied handler not invoked", err)
	}
	if err = worker.Work(t.Context(), &river.Job[PrepareBuildArgs]{Args: PrepareBuildArgs{BuildID: "../foreign"}}); err == nil || calls != 1 {
		t.Fatal("invalid identity reached handler")
	}
	for _, mode := range []string{"error", "panic", "cancel", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := prepareBuildWorker{handler: &buildTestHandler{build: func(context.Context, string) error {
				switch mode {
				case "panic":
					panic("private-build-secret")
				case "cancel":
					cancel()
				case "invalid":
					return fmt.Errorf("private-build-secret: %w", ErrInvalidOperation)
				}
				return errors.New("private-build-secret")
			}}}
			err := w.Work(ctx, &river.Job[PrepareBuildArgs]{Args: args})
			if err == nil || strings.Contains(err.Error(), "private-build-secret") {
				t.Fatal("unsanitized worker failure")
			}
		})
	}
	if _, err = (&Client{}).EnqueueBuildTx(t.Context(), nil, args); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported client admitted build", err)
	}
}

func TestBuildTransactionalVisibilityRollbackAndConcurrentDedup(t *testing.T) {
	pool := testPool(t)
	migrateTestPool(t, pool)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE fixture_builds(id text PRIMARY KEY,dispatches integer NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	handlerErrors := make(chan error, 1)
	release := make(chan struct{})
	var once sync.Once
	releaseWork := func() { once.Do(func() { close(release) }) }
	defer releaseWork()
	var calls atomic.Int32
	client := newTestClient(t, pool, &buildTestHandler{build: func(ctx context.Context, id string) error {
		tag, err := pool.Exec(ctx, `UPDATE fixture_builds SET dispatches=dispatches+1 WHERE id=$1`, id)
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("missing committed build")
		}
		handlerErrors <- err
		started <- struct{}{}
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}}, Config{})
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `INSERT INTO fixture_builds(id) VALUES('build_one')`); err != nil {
		t.Fatal(err)
	}
	first, err := client.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: "build_one"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := client.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: "build_one"})
	if err != nil || same != first {
		t.Fatal("transaction dedup failed", err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job`).Scan(&count); err != nil || count != 0 {
		t.Fatal("job visible before commit")
	}
	select {
	case <-started:
		t.Fatal("build dispatched before commit")
	default:
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	if err = <-handlerErrors; err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			tx, e := pool.Begin(t.Context())
			if e != nil {
				results <- result{err: e}
				return
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			id, e := client.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: "build_one"})
			if e == nil {
				e = tx.Commit(t.Context())
			}
			results <- result{id, e}
		})
	}
	group.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.id != first {
			t.Fatal("concurrent duplicate escaped active uniqueness", result.err)
		}
	}
	releaseWork()
	waitJobState(t, pool, first, "completed")
	next := insertBuildJob(t, client, pool, "build_one")
	if next == first {
		t.Fatal("completed job suppressed new request")
	}
	await(t, started)
	if err = <-handlerErrors; err != nil {
		t.Fatal(err)
	}
	waitJobState(t, pool, next, "completed")
	var dispatches int
	if err = pool.QueryRow(t.Context(), `SELECT dispatches FROM fixture_builds WHERE id='build_one'`).Scan(&dispatches); err != nil || dispatches != 2 || calls.Load() != 2 {
		t.Fatal("handler did not run exactly once per committed job", err)
	}
	rollback, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rollback.Rollback(t.Context()) }()
	if _, err = rollback.Exec(t.Context(), `INSERT INTO fixture_builds(id) VALUES('build_rollback')`); err != nil {
		t.Fatal(err)
	}
	rolled, err := client.EnqueueBuildTx(t.Context(), rollback, PrepareBuildArgs{BuildID: "build_rollback"})
	if err != nil {
		t.Fatal(err)
	}
	if err = rollback.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM river_job WHERE id=$1)+(SELECT count(*) FROM fixture_builds WHERE id='build_rollback')`, rolled).Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled-back build work persisted", err)
	}
}

func TestBuildAdmissionRequiresHandlerIdentityAndTransactionScope(t *testing.T) {
	pool := testPool(t)
	migrateTestPool(t, pool)
	missing, err := New(t.Context(), pool, HandlerFunc(func(context.Context, string) error { return nil }), Config{})
	if err != nil {
		t.Fatal(err)
	}
	real, err := New(t.Context(), pool, &buildTestHandler{build: func(context.Context, string) error { return nil }}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if missing.hasBuild || !real.hasBuild {
		t.Fatal("optional handler registration differs from supplied support")
	}
	var nilHandler *buildTestHandler
	if _, err = New(t.Context(), pool, nilHandler, Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("typed nil handler accepted")
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = missing.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: "unsupported"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing handler queued work", err)
	}
	if _, err = real.EnqueueBuildTx(t.Context(), nil, PrepareBuildArgs{BuildID: "nil_tx"}); !errors.Is(err, ErrInvalidOperation) {
		t.Fatal("nil transaction accepted")
	}
	for _, id := range []string{"", "../foreign", "private value", "\x00", strings.Repeat("a", 129)} {
		if _, err = real.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: id}); !errors.Is(err, ErrInvalidOperation) {
			t.Fatal("invalid build ID accepted", err)
		}
	}
	if _, err = tx.Exec(t.Context(), `SET LOCAL search_path=public`); err != nil {
		t.Fatal(err)
	}
	if _, err = real.EnqueueBuildTx(t.Context(), tx, PrepareBuildArgs{BuildID: "foreign_schema"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("foreign schema transaction accepted", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The same shared enqueue boundary also refuses a different configured
	// endpoint or database before it can insert anything in the caller's scope.
	for _, scope := range []string{"endpoint", "database"} {
		config := pool.Config().Copy()
		if scope == "endpoint" {
			config.ConnConfig.Host = "localhost"
		} else {
			config.ConnConfig.Database = "template1"
		}
		foreign, e := pgxpool.NewWithConfig(t.Context(), config)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(foreign.Close)
		foreignTx, e := foreign.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		_, e = real.EnqueueBuildTx(t.Context(), foreignTx, PrepareBuildArgs{BuildID: "foreign_" + scope})
		rollbackErr := foreignTx.Rollback(t.Context())
		if rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if !errors.Is(e, ErrInvalidConfiguration) {
			t.Fatalf("foreign %s transaction accepted: %v", scope, e)
		}
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected admission inserted a job", err)
	}
}

func TestBuildFiniteRetryAndSanitizedPersistence(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			migrateTestPool(t, pool)
			var attempts atomic.Int32
			client := newTestClient(t, pool, &buildTestHandler{build: func(context.Context, string) error {
				attempts.Add(1)
				switch mode {
				case "panic":
					panic("private-build-secret")
				case "invalid":
					return fmt.Errorf("private-build-secret: %w", ErrInvalidOperation)
				}
				return errors.New("private-build-secret")
			}}, Config{})
			job := insertBuildJob(t, client, pool, "build_"+mode)
			state, want := "discarded", int32(3)
			if mode == "invalid" {
				state, want = "cancelled", 1
			}
			waitJobState(t, pool, job, state)
			var stored, kind, queue, args string
			var max int
			if err := pool.QueryRow(t.Context(), `SELECT errors::text,kind,queue,args::text,max_attempts FROM river_job WHERE id=$1`, job).Scan(&stored, &kind, &queue, &args, &max); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "private-build-secret") || !strings.Contains(stored, "hosting_") || attempts.Load() != want || kind != PrepareBuildKind || queue != Queue || max != 3 {
				t.Fatal("unsafe or unbounded build failure persistence")
			}
			var fields map[string]any
			if json.Unmarshal([]byte(args), &fields) != nil || len(fields) != 1 || fields["build_id"] != "build_"+mode {
				t.Fatal("unexpected persisted build payload")
			}
		})
	}
}

func TestBuildHandlerSharesBoundedShutdown(t *testing.T) {
	for _, mode := range []string{"drain", "cancel", "escalate"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			migrateTestPool(t, pool)
			started, finished, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseWork := func() { once.Do(func() { close(release) }) }
			defer releaseWork()
			client := newTestClient(t, pool, &buildTestHandler{build: func(ctx context.Context, _ string) error {
				close(started)
				defer close(finished)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}, Config{DrainTimeout: 100 * time.Millisecond})
			job := insertBuildJob(t, client, pool, "build_shutdown")
			await(t, started)
			switch mode {
			case "drain":
				releaseWork()
				if err := client.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				waitJobState(t, pool, job, "completed")
			case "cancel":
				if err := client.Cancel(t.Context()); err != nil {
					t.Fatal(err)
				}
			default:
				if err := client.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			await(t, finished)
			await(t, client.Stopped())
			var stored string
			if err := pool.QueryRow(t.Context(), `SELECT coalesce(errors::text,'') FROM river_job WHERE id=$1`, job).Scan(&stored); err != nil || strings.Contains(stored, "context canceled") {
				t.Fatal("shutdown persisted raw context failure", err)
			}
		})
	}
}
