package main

import (
	"slices"
	"testing"
	"time"

	"nerocd/db"
)

// These histories retain the exact embedded filenames and bytes from each
// branch. The ledger uses full paths, including both independently added 0100s.
func TestMigrationBranchHistoriesPreserveFullFilenameIdentity(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	all := make([]Migration, 0, len(files))
	for _, name := range files {
		raw, err := db.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, Migration{Version: name, SQL: string(raw), Checksum: sqlChecksum(raw)})
	}
	const security = "migrations/0100_hosting_function_schemas.sql"
	for _, history := range []string{"remote_0100", "local_0103", "pending_0104"} {
		t.Run(history, func(t *testing.T) {
			pool := ownerMigrationTestPool(t)
			var prior []Migration
			for _, migration := range all {
				include := migration.Version != security && migration.Version <= "migrations/0103_hosting_mysql_backup_retention.sql"
				switch history {
				case "remote_0100":
					include = migration.Version <= "migrations/0099_hosting_stored_dockerfile.sql" || migration.Version == security
				case "pending_0104":
					include = migration.Version != security && migration.Version <= "migrations/0104_hosting_compose_mysql_backups.sql"
				}
				if include {
					prior = append(prior, migration)
				}
			}
			options := MigrationOptions{SetTimeout: 2 * time.Minute, PerMigrationTimeout: 30 * time.Second, LockKey: 768316409}
			if err := migrateWithSource(t.Context(), pool, prior, options); err != nil {
				t.Fatal("install historical branch", err)
			}
			if err := migrateWithSource(t.Context(), pool, all, options); err != nil {
				t.Fatal("upgrade combined branch", err)
			}
			if err := migrateWithSource(t.Context(), pool, all, options); err != nil {
				t.Fatal("replay combined migration", err)
			}
			var count int
			if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(all) {
				t.Fatal("migration identities changed", count, err)
			}
			for _, migration := range all {
				var checksum string
				if err := pool.QueryRow(t.Context(), "SELECT checksum FROM schema_migrations WHERE version=$1", migration.Version).Scan(&checksum); err != nil || checksum != migration.Checksum {
					t.Fatal("migration bytes changed", migration.Version, err)
				}
			}
			var schema string
			if err := pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"nerocd_hosting_compose_lease", "nerocd_component_backup_lease", "nerocd_component_backup_stack_hold", "nerocd_database_backup_admission", "nerocd_database_backup_receipt"} {
				var config []string
				if err := pool.QueryRow(t.Context(), "SELECT p.proconfig FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname=$1", name).Scan(&config); err != nil || !slices.Contains(config, "search_path=pg_catalog, "+schema+", pg_temp") {
					t.Fatal("hosting function schema binding changed", name, config, err)
				}
			}
			var mysql bool
			if err := pool.QueryRow(t.Context(), "SELECT nerocd_mysql_component_backup_schema_compatible()").Scan(&mysql); err != nil || !mysql {
				t.Fatal("combined MySQL component schema unavailable", err)
			}
		})
	}
}
