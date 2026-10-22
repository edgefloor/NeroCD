package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/hostingjobs"
)

func ownerMigrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	address := os.Getenv("NEROCD_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("river_owner_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
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
	pool, err := pgxpool.New(t.Context(), parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func TestOwnerCLIInstallsRiverAndBindsBackupCompatibility(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	pool := ownerMigrationTestPool(t)
	address := pool.Config().ConnConfig.ConnString()
	if err := migrateDatabase([]string{"--database-url", address}); err != nil {
		t.Fatal(err)
	}
	if err := hostingjobs.Validate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := migrateDatabase([]string{"--database-url", address}); err != nil {
		t.Fatalf("idempotent owner migrate: %v", err)
	}
	actual, _, err := backupDatabaseState(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	_, _, expected, err := backupCompatibility()
	if err != nil || strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatal("backup did not include exact native and River migration identity")
	}
	if _, err = pool.Exec(t.Context(), "UPDATE nerocd_river_migration_checksums SET checksum='changed' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = backupDatabaseState(t.Context(), address); err == nil {
		t.Fatal("backup accepted River checksum mismatch")
	}
}
func TestOwnerLockSerializesRiverUpgrade(t *testing.T) {
	pool := ownerMigrationTestPool(t)
	source := []Migration{{Version: "fixture/001.sql", SQL: "CREATE TABLE marker(id integer)", Checksum: "marker"}}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- migrateWithSource(t.Context(), pool, source, MigrationOptions{LockKey: 99129, HostingJobs: true})
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := hostingjobs.Validate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatal("native ledger changed during concurrent migration")
	}
}
