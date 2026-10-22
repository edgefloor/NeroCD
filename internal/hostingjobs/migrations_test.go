package hostingjobs

import (
	"errors"
	"testing"
)

func TestMigrationBundleDeterministic(t *testing.T) {
	a, err := CompatibilityEntries()
	if err != nil || len(a) != 7 {
		t.Fatalf("pinned bundle: %v %v", a, err)
	}
	b, err := CompatibilityEntries()
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("nondeterministic bundle")
		}
	}
	bundle, err := MigrationBundle()
	if err != nil {
		t.Fatal(err)
	}
	bundle[0].Name = "mutated"
	other, err := MigrationBundle()
	if err != nil || other[0].Name == "mutated" {
		t.Fatal("bundle aliases caller")
	}
}
func TestMigrationRollbackPrefixAndResume(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := MigrateNextTx(ctx, tx)
	if err != nil || !applied {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = pool.QueryRow(ctx, "SELECT to_regclass(current_schema()||'.river_migration') IS NOT NULL OR to_regclass(current_schema()||'.nerocd_river_migration_checksums') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("first migration rollback left schema or ledger")
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err = MigrateNextTx(ctx, tx); err != nil || !applied {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "CREATE TABLE river_job(id integer)"); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = MigrateNextTx(ctx, tx); !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("expected migration2 failure: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var appliedCount, ledgerCount int
	if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM river_migration),(SELECT count(*) FROM nerocd_river_migration_checksums)").Scan(&appliedCount, &ledgerCount); err != nil || appliedCount != 1 || ledgerCount != 1 {
		t.Fatal("failed upgrade changed verified prefix")
	}
	if err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_type WHERE typnamespace=current_schema()::regnamespace AND typname='river_job_state')").Scan(&exists); err != nil || exists {
		t.Fatal("partial failing migration retained enum")
	}
	if _, err = pool.Exec(ctx, "DROP TABLE river_job"); err != nil {
		t.Fatal(err)
	}
	migrateTestPool(t, pool)
	if err = Validate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	migrateTestPool(t, pool)
	if entries, err := AppliedCompatibilityEntries(ctx, pool); err != nil || len(entries) != 7 {
		t.Fatal("exact final bundle validation failed")
	}
}
func TestMigrationRejectsUnverifiedUnexpectedAndDriftedState(t *testing.T) {
	for _, mode := range []string{"missing ledger", "checksum drift", "missing version", "unexpected version", "unexpected line", "orphan table"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			ctx := t.Context()
			if mode != "orphan table" {
				migrateTestPool(t, pool)
			}
			sql := map[string]string{"missing ledger": "DROP TABLE nerocd_river_migration_checksums", "checksum drift": "UPDATE nerocd_river_migration_checksums SET checksum='private-drift' WHERE version=1", "missing version": "DELETE FROM river_migration WHERE version=2", "unexpected version": "INSERT INTO river_migration(line,version)VALUES('main',999)", "unexpected line": "INSERT INTO river_migration(line,version)VALUES('foreign',1)", "orphan table": "CREATE TABLE river_job(id integer)"}[mode]
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := Validate(ctx, pool); !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("unsafe state validated: %v", err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err = MigrateNextTx(ctx, tx); !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("unsafe state migrated or fabricated evidence: %v", err)
			}
		})
	}
}
