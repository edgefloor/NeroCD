package hostingjobs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// RiverVersion is pinned with the pgx driver and its embedded migration bundle.
const RiverVersion = "v0.44.1"
const checksumTable = "nerocd_river_migration_checksums"

var (
	// ErrSchemaMismatch reports a missing, unexpected, or unverified migration set.
	ErrSchemaMismatch = errors.New("hosting_jobs_schema_mismatch")
	// ErrMigrationFailed reports an owner migration failure without database details.
	ErrMigrationFailed = errors.New("hosting_jobs_migration_failed")
)

// MigrationIdentity binds the pinned dependency to each migration name and both SQL directions.
type MigrationIdentity struct {
	Version  int
	Name     string
	Checksum string
}

func newMigrator(schema string) (*rivermigrate.Migrator[pgx.Tx], error) {
	return rivermigrate.New(riverpgxv5.New(nil), &rivermigrate.Config{Schema: schema, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// MigrationBundle returns a fresh, deterministic identity list derived from pinned vendor SQL.
func MigrationBundle() ([]MigrationIdentity, error) {
	migrator, err := newMigrator("")
	if err != nil {
		return nil, ErrMigrationFailed
	}
	versions := migrator.AllVersions()
	result := make([]MigrationIdentity, 0, len(versions))
	for _, migration := range versions {
		hash := sha256.Sum256([]byte(fmt.Sprintf("main\x00%d\x00%s\x00%s\x00%s", migration.Version, migration.Name, migration.SQLUp, migration.SQLDown)))
		result = append(result, MigrationIdentity{Version: migration.Version, Name: migration.Name, Checksum: fmt.Sprintf("sha256:%x", hash)})
	}
	return result, nil
}

// CompatibilityEntries returns stable entries for the platform backup schema identity.
func CompatibilityEntries() ([]string, error) {
	bundle, err := MigrationBundle()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(bundle))
	for _, m := range bundle {
		result = append(result, fmt.Sprintf("river/%s/main/%03d_%s:%s", RiverVersion, m.Version, m.Name, m.Checksum))
	}
	return result, nil
}

func tableExists(ctx context.Context, tx pgx.Tx, schema, table string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", pgx.Identifier{schema, table}.Sanitize()).Scan(&exists)
	return exists, err
}
func migrationPrefix(ctx context.Context, tx pgx.Tx) (string, []MigrationIdentity, int, bool, error) {
	var schema string
	if err := tx.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return "", nil, 0, false, ErrMigrationFailed
	}
	bundle, err := MigrationBundle()
	if err != nil {
		return "", nil, 0, false, err
	}
	migrator, err := newMigrator(schema)
	if err != nil {
		return "", nil, 0, false, ErrMigrationFailed
	}
	existing, err := migrator.ExistingVersionsTx(ctx, tx)
	if err != nil {
		return "", nil, 0, false, ErrSchemaMismatch
	}
	if len(existing) > len(bundle) {
		return "", nil, 0, false, ErrSchemaMismatch
	}
	for i, v := range existing {
		if v.Version != bundle[i].Version || v.Name != bundle[i].Name {
			return "", nil, 0, false, ErrSchemaMismatch
		}
	}
	riverExists, err := tableExists(ctx, tx, schema, "river_migration")
	if err != nil {
		return "", nil, 0, false, ErrMigrationFailed
	}
	if riverExists {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{schema, "river_migration"}.Sanitize()).Scan(&count); err != nil || count != len(existing) {
			return "", nil, 0, false, ErrSchemaMismatch
		}
	}
	ledgerExists, err := tableExists(ctx, tx, schema, checksumTable)
	if err != nil {
		return "", nil, 0, false, ErrMigrationFailed
	}
	if !ledgerExists {
		if riverExists || len(existing) > 0 {
			return "", nil, 0, false, ErrSchemaMismatch
		}
		// An orphan River table is not a known fresh schema and cannot acquire fabricated checksums.
		for _, table := range []string{"river_job", "river_queue", "river_leader", "river_client"} {
			exists, err := tableExists(ctx, tx, schema, table)
			if err != nil {
				return "", nil, 0, false, ErrMigrationFailed
			}
			if exists {
				return "", nil, 0, false, ErrSchemaMismatch
			}
		}
		return schema, bundle, 0, false, nil
	}
	rows, err := tx.Query(ctx, "SELECT version,name,checksum FROM "+pgx.Identifier{schema, checksumTable}.Sanitize()+" ORDER BY version")
	if err != nil {
		return "", nil, 0, false, ErrSchemaMismatch
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var v MigrationIdentity
		if err := rows.Scan(&v.Version, &v.Name, &v.Checksum); err != nil {
			return "", nil, 0, false, ErrSchemaMismatch
		}
		if i >= len(existing) || v != bundle[i] {
			return "", nil, 0, false, ErrSchemaMismatch
		}
		i++
	}
	if rows.Err() != nil || i != len(existing) {
		return "", nil, 0, false, ErrSchemaMismatch
	}
	return schema, bundle, i, true, nil
}

// MigrateNextTx applies at most one pinned River version and its checksum in the owner's transaction.
// The caller holds the platform advisory lock and owns commit/rollback. This function never commits.
// Each version is atomic; an interrupted upgrade retains only a verified applied prefix.
func MigrateNextTx(ctx context.Context, tx pgx.Tx) (bool, error) {
	if tx == nil {
		return false, ErrInvalidConfiguration
	}
	schema, bundle, prefix, ledgerExists, err := migrationPrefix(ctx, tx)
	if err != nil {
		return false, err
	}
	if prefix == len(bundle) {
		return false, nil
	}
	if !ledgerExists {
		if _, err := tx.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, checksumTable}.Sanitize()+" (version integer PRIMARY KEY CHECK(version>0), name text NOT NULL, checksum text NOT NULL)"); err != nil {
			return false, ErrMigrationFailed
		}
	}
	migrator, err := newMigrator(schema)
	if err != nil {
		return false, ErrMigrationFailed
	}
	next := bundle[prefix]
	// Upstream deprecates batching versions, so each caller transaction applies exactly one version.
	//nolint:staticcheck // The single-version transaction keeps checksum evidence atomic; Migrate cannot share that transaction.
	if _, err := migrator.MigrateTx(ctx, tx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{MaxSteps: 1, TargetVersion: next.Version}); err != nil {
		return false, ErrMigrationFailed
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+pgx.Identifier{schema, checksumTable}.Sanitize()+"(version,name,checksum) VALUES($1,$2,$3)", next.Version, next.Name, next.Checksum); err != nil {
		return false, ErrMigrationFailed
	}
	_, _, applied, _, err := migrationPrefix(ctx, tx)
	if err != nil || applied != prefix+1 {
		return false, ErrSchemaMismatch
	}
	return true, nil
}

// ValidateTx requires every pinned version, exact checksum evidence, and no unexpected migration line/version.
// It performs no migration and never fabricates evidence for an existing schema.
func ValidateTx(ctx context.Context, tx pgx.Tx) error {
	if tx == nil {
		return ErrInvalidConfiguration
	}
	_, bundle, prefix, exists, err := migrationPrefix(ctx, tx)
	if err != nil {
		return err
	}
	if !exists || prefix != len(bundle) {
		return ErrSchemaMismatch
	}
	return nil
}

// Validate checks migration compatibility without changing schema.
func Validate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrInvalidConfiguration
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ValidateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

// AppliedCompatibilityEntries validates actual migration evidence before returning the pinned backup entries.
func AppliedCompatibilityEntries(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	if err := Validate(ctx, pool); err != nil {
		return nil, err
	}
	return CompatibilityEntries()
}
