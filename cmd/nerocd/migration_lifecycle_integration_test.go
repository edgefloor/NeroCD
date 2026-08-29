package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationLifecyclePG17(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if base == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("nerocd_migrate_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	pool, err := pgxpool.New(ctx, base+"&search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := []Migration{{Version: "0001", SQL: "CREATE TABLE marker (id int primary key); INSERT INTO marker VALUES (1)", Checksum: "one"}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- migrateWithSource(ctx, pool, m, MigrationOptions{LockKey: 99123, PerMigrationTimeout: time.Second})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger=%d err=%v", n, err)
	}
	if err := migrateWithSource(ctx, pool, []Migration{{Version: "0001", SQL: "SELECT 1", Checksum: "drift"}}, MigrationOptions{LockKey: 99123}); err == nil {
		t.Fatal("accepted checksum drift")
	}
	if err := migrateWithSource(ctx, pool, []Migration{{Version: "0002", SQL: "CREATE TABLE doomed (id int); SELECT 1/0", Checksum: "bad"}}, MigrationOptions{LockKey: 99123}); err == nil {
		t.Fatal("accepted failing migration")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version='0002'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed migration ledger=%d err=%v", n, err)
	}
	if err := migrateWithSource(ctx, pool, []Migration{{Version: "0003", SQL: "SELECT pg_sleep(1)", Checksum: "slow"}}, MigrationOptions{LockKey: 99124, PerMigrationTimeout: 10 * time.Millisecond}); err == nil {
		t.Fatal("accepted timed-out migration")
	}
}
