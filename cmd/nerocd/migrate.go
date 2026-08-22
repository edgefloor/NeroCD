package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/store"
)

func migrateDatabase(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	databaseURL := fs.String("database-url", os.Getenv("NEROCD_DATABASE_URL"), "PostgreSQL connection URL")
	seed := fs.Bool("seed", false, "deprecated; development seeding is seed-dev")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := configuredMode()
	if err != nil {
		return err
	}
	if mode == modeProduction {
		if fs.Lookup("database-url").Value.String() != "" || *seed {
			return errors.New("production migrate requires secret-file configuration and --seed=false")
		}
		value, configErr := loadDatabaseURL(mode)
		if configErr != nil {
			return configErr
		}
		*databaseURL = value
	}
	if *seed {
		return errors.New("development seeding is a separate seed-dev command")
	}
	if *databaseURL == "" {
		return errors.New("database URL is required via --database-url or NEROCD_DATABASE_URL")
	}
	if err := validateDatabaseURL(*databaseURL); err != nil {
		return err
	}

	database, err := pgxpool.New(context.Background(), *databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	migrations, err := migrationFiles()
	if err != nil {
		return err
	}
	source := make([]Migration, 0, len(migrations))
	for _, name := range migrations {
		content, err := db.Files.ReadFile(name)
		if err != nil {
			return err
		}
		source = append(source, Migration{Version: name, SQL: string(content), Checksum: sqlChecksum(content)})
	}
	return migrateWithSource(ctx, database, source, MigrationOptions{SetTimeout: 2 * time.Minute, PerMigrationTimeout: 30 * time.Second, LockKey: 768316409})
}

type Migration struct{ Version, SQL, Checksum string }
type MigrationOptions struct {
	SetTimeout, PerMigrationTimeout time.Duration
	LockKey                         int64
}

func migrateWithSource(ctx context.Context, database *pgxpool.Pool, migrations []Migration, options MigrationOptions) error {
	if options.SetTimeout <= 0 {
		options.SetTimeout = 2 * time.Minute
	}
	if options.PerMigrationTimeout <= 0 {
		options.PerMigrationTimeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, options.SetTimeout)
	defer cancel()
	conn, err := database.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, options.LockKey); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, options.LockKey)
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, migration := range migrations {
		appliedChecksum := ""
		err = conn.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, migration.Version).Scan(&appliedChecksum)
		switch {
		case err == nil && appliedChecksum == migration.Checksum:
			fmt.Printf("skipped %s\n", migration.Version)
			continue
		case err == nil && appliedChecksum != migration.Checksum:
			return fmt.Errorf("migration %s checksum changed after it was applied", migration.Version)
		case err != pgx.ErrNoRows:
			return err
		}
		migrationCtx, migrationCancel := context.WithTimeout(ctx, options.PerMigrationTimeout)
		tx, err := conn.Begin(migrationCtx)
		if err != nil {
			migrationCancel()
			return err
		}
		if _, err := tx.Exec(migrationCtx, migration.SQL); err != nil {
			_ = tx.Rollback(migrationCtx)
			migrationCancel()
			return fmt.Errorf("apply %s: %w", migration.Version, err)
		}
		if _, err := tx.Exec(migrationCtx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, migration.Version, migration.Checksum); err != nil {
			_ = tx.Rollback(migrationCtx)
			migrationCancel()
			return err
		}
		if err := tx.Commit(migrationCtx); err != nil {
			migrationCancel()
			return err
		}
		migrationCancel()
		fmt.Printf("applied %s\n", migration.Version)
	}
	return nil
}

func seedDevelopmentDatabase(args []string) error {
	if mode, err := configuredMode(); err != nil {
		return err
	} else if mode == modeProduction {
		return errors.New("production rejects development seed data")
	}
	fs := flag.NewFlagSet("seed-dev", flag.ExitOnError)
	databaseURL := fs.String("database-url", os.Getenv("NEROCD_DATABASE_URL"), "PostgreSQL connection URL")
	seedFile := fs.String("seed-file", os.Getenv("NEROCD_DEV_SEED_FILE"), "explicit development seed SQL file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateDatabaseURL(*databaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(*seedFile) == "" {
		return errors.New("seed-dev requires --seed-file or NEROCD_DEV_SEED_FILE")
	}
	database, err := pgxpool.New(context.Background(), *databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	content, err := os.ReadFile(*seedFile)
	if err != nil {
		return errors.New("development seed file cannot be read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := database.Exec(ctx, string(content)); err != nil {
		return fmt.Errorf("apply development seed: %w", err)
	}
	if _, err := database.Exec(ctx, "UPDATE identity_bootstrap_state SET completed_by = 'development_seed', completed_at = clock_timestamp() WHERE singleton = TRUE AND completed_by IS NULL"); err != nil {
		return errors.New("mark development seed bootstrap state")
	}
	fmt.Println("applied development seed")
	return nil
}

func bootstrapAdmin(args []string) error {
	fs := flag.NewFlagSet("bootstrap-admin", flag.ExitOnError)
	email := fs.String("email", "", "initial administrator email")
	name := fs.String("name", "", "initial administrator name")
	passwordFile := fs.String("password-file", "", "owner-only password file")
	passwordStdin := fs.Bool("password-stdin", false, "read one password from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*passwordFile == "" && !*passwordStdin) || (*passwordFile != "" && *passwordStdin) {
		return errors.New("bootstrap-admin requires exactly one of --password-file or --password-stdin")
	}
	var raw []byte
	var err error
	if *passwordFile != "" {
		raw, err = readOwnerOnlyProductionSecret(strings.TrimSpace(*passwordFile))
	} else {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if len(raw) > 4096 {
			err = errors.New("bootstrap password is too large")
		}
	}
	if err != nil {
		return errors.New("bootstrap password cannot be read")
	}
	password := strings.TrimSpace(string(raw))
	if password == "" {
		return errors.New("bootstrap password is required")
	}
	cfg, err := loadRuntimeConfig(":8080")
	if err != nil {
		return err
	}
	if cfg.databaseURL == "" {
		return errors.New("bootstrap-admin requires a configured PostgreSQL database")
	}
	service, closeStore, err := newService(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	user, err := service.BootstrapAdmin(context.Background(), app.BootstrapAdminInput{Email: *email, Name: *name, Password: password})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errors.New("bootstrap-admin has already completed")
		}
		return err
	}
	fmt.Printf("bootstrapped administrator %s\n", user.ID)
	return nil
}

func migrationFiles() ([]string, error) {
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, "migrations/"+entry.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("no migration files found")
	}
	return files, nil
}

func sqlChecksum(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum)
}

func validateDatabaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("database URL is invalid: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("database URL must use postgres:// or postgresql://")
	}
	if parsed.Host == "" {
		return errors.New("database URL host is required")
	}
	return nil
}
