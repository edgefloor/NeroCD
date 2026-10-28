package main

// Backup and restore stay small owner-only offline operations. The local
// scheduler reuses this same context-aware publication path; it does not add
// remote storage, encryption, or PITR policy.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
	"nerocd/internal/store"
)

const backupManifestVersion = 1
const maxBackupMetadataBytes = 64 << 10

// Persisted specifications are data supplied by prior versions of the API, not
// trusted backup metadata. Bound both decoding and recursive traversal so a
// corrupted JSONB value cannot turn an offline backup into an unbounded walk.
const (
	maxStoredRunSpecBytes = 1 << 20
	maxStoredRunSpecDepth = 32
	maxStoredRunSpecNodes = 4096
)

// backupCommand is an internal test seam. Production always uses the standard
// executable lookup; tests can simulate an interrupted dump/restore without
// exposing database URLs through a shell wrapper.
var backupCommand = exec.CommandContext

type backupFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type runnerFileInventory struct {
	Provider  string `json:"provider"`
	Reference string `json:"reference"`
	Version   string `json:"version"`
}
type backupManifest struct {
	Version            int                   `json:"version"`
	CreatedAt          time.Time             `json:"created_at"`
	ApplicationVersion string                `json:"application_version"`
	SchemaIdentity     string                `json:"schema_identity"`
	Database           string                `json:"database"`
	Migrations         []string              `json:"migrations"`
	Files              []backupFile          `json:"files"`
	RunnerFiles        []runnerFileInventory `json:"runner_file_inventory,omitempty"`
}

func backupDatabase(args []string) error {
	return backupDatabaseContext(context.Background(), args)
}

// backupDatabaseContext is deliberately internal: callers share the same
// descriptor-confined staging and manifest protocol while a supervisor can
// bound a scheduled invocation without putting credentials in a child argv.
func backupDatabaseContext(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	databaseURL := fs.String("database-url", "", "PostgreSQL connection URL (development only)")
	output := fs.String("output-dir", "", "existing owner-only directory for a new backup")
	runnerRoot := fs.String("runner-file-root", "", "optional owner-only runner_file root; metadata only, no contents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedURL, err := resolveBackupRestoreDatabaseURL(fs, *databaseURL)
	if err != nil {
		return err
	}
	// The first failure before a database URL is admitted cannot be persisted;
	// production operations documents require the invoking supervisor to retain
	// that bounded command outcome. Once the owner DB is reachable, record only
	// a closed outcome/reason enum—never an archive path or tool diagnostic.
	backupOutcome, backupReason := "failure", "preflight"
	defer func() { recordBackupOperationalResult(resolvedURL, backupOutcome, backupReason) }()
	if strings.TrimSpace(*output) == "" {
		return errors.New("backup requires --output-dir")
	}
	outputDirectory, err := openSecureBackupDirectory(*output)
	if err != nil {
		return errors.New("backup output directory must already exist and be owner-only")
	}
	defer func() { _ = outputDirectory.Close() }()
	random, err := randomRuntimeHex(6)
	if err != nil {
		return errors.New("backup name could not be created")
	}
	name := "backup-" + time.Now().UTC().Format("20060102T150405Z") + "-" + random
	stagingName := "." + name
	staging, err := createBackupDirectoryAt(outputDirectory, stagingName)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			removeBackupStagingAt(outputDirectory, staging, stagingName)
		}
		_ = staging.Close()
	}()
	dump, err := createBackupFileAt(staging, "database.dump")
	if err != nil {
		return err
	}
	defer func() { _ = dump.Close() }()
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	backupReason = "dump"
	if err := runBackupToolIO(ctx, "pg_dump", resolvedURL, nil, dump, "--format=custom", "--no-owner", "--no-acl"); err != nil {
		return err
	}
	if err := dump.Sync(); err != nil {
		return errors.New("backup dump could not be synchronized")
	}
	file, err := checksumOpenBackupFile(dump, "database.dump")
	if err != nil {
		return err
	}
	migrations, databaseName, err := backupDatabaseState(ctx, resolvedURL)
	if err != nil {
		return err
	}
	applicationVersion, schemaIdentity, expectedMigrations, err := backupCompatibility()
	if err != nil {
		return errors.New("backup compatibility identity could not be created")
	}
	if strings.Join(migrations, "\n") != strings.Join(expectedMigrations, "\n") {
		return errors.New("source schema is incompatible with this application build")
	}
	requirements, err := runnerFileRequirements(ctx, resolvedURL)
	if err != nil {
		return err
	}
	manifest := backupManifest{Version: backupManifestVersion, CreatedAt: time.Now().UTC(), ApplicationVersion: applicationVersion, SchemaIdentity: schemaIdentity, Database: databaseName, Migrations: migrations, Files: []backupFile{file}}
	if len(requirements) != 0 && strings.TrimSpace(*runnerRoot) == "" {
		return errors.New("backup requires runner-file root for persisted runner_file bindings")
	}
	if strings.TrimSpace(*runnerRoot) != "" {
		inventory, err := inventoryRunnerFiles(*runnerRoot, requirements)
		if err != nil {
			return err
		}
		manifest.RunnerFiles = inventory
	}
	if err := writeBackupManifestAt(staging, manifest); err != nil {
		return err
	}
	final := filepath.Join(*output, name)
	backupReason = "publish"
	if err := publishBackupDirectoryAt(outputDirectory, stagingName, name); err != nil {
		return errors.New("backup directory could not be durably published")
	}
	published = true
	backupOutcome, backupReason = "success", "none"
	fmt.Println(final)
	return nil
}

func recordBackupOperationalResult(databaseURL, outcome, reason string) {
	if strings.TrimSpace(databaseURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return
	}
	defer pool.Close()
	// The schema check protects old installations from turning a failed backup
	// into a second operator-visible failure. Result recording is best effort;
	// it never changes backup publication semantics.
	_, _ = pool.Exec(ctx, `INSERT INTO backup_operational_results (outcome, reason) VALUES ($1, $2)`, outcome, reason)
}

func restoreDatabase(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	databaseURL := fs.String("database-url", "", "empty PostgreSQL target URL (development only)")
	input := fs.String("input-dir", "", "backup directory containing manifest.json")
	runnerRoot := fs.String("runner-file-root", "", "owner-only recovered runner_file root, required when inventory exists")
	allowDisposableTarget := fs.Bool("allow-disposable-target", false, "required acknowledgement that the restore target is an isolated disposable database")
	confirmedTarget := fs.String("confirm-target-database", "", "required exact target database name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedURL, err := resolveBackupRestoreDatabaseURL(fs, *databaseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" {
		return errors.New("restore requires --input-dir")
	}
	if err := validateRestoreConfirmation(*allowDisposableTarget, *confirmedTarget); err != nil {
		return err
	}
	manifest, dump, err := openVerifiedBackupArchive(*input)
	if err != nil {
		return err
	}
	defer func() { _ = dump.Close() }()
	if err := validateBackupCompatibility(manifest); err != nil {
		return err
	}
	if len(manifest.RunnerFiles) != 0 {
		if strings.TrimSpace(*runnerRoot) == "" {
			return errors.New("restore requires recovered runner-file inventory")
		}
		actual, err := inventoryRunnerFiles(*runnerRoot, manifest.RunnerFiles)
		if err != nil || !sameRunnerFileInventory(manifest.RunnerFiles, actual) {
			return errors.New("recovered runner-file inventory does not match backup")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, resolvedURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	releaseRestoreAdmission, err := acquireDisposableRestoreTarget(ctx, pool, *confirmedTarget)
	if err != nil {
		return err
	}
	defer releaseRestoreAdmission()
	if err := runBackupToolIO(ctx, "pg_restore", resolvedURL, dump, nil, "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl"); err != nil {
		return err
	}
	current, _, err := backupDatabaseState(ctx, resolvedURL)
	if err != nil {
		return err
	}
	if strings.Join(current, "\n") != strings.Join(manifest.Migrations, "\n") {
		return errors.New("restored schema migration set does not match manifest")
	}
	// Restoring credentials and live runner authority is unsafe. This single
	// transaction means a failure cannot report a half-invalidated target.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL`); err != nil {
		return err
	}
	if err := store.InvalidateRunnerLeasesForRestoreTx(ctx, tx); err != nil {
		return err
	}
	// A backup may predate a later admission or even the first operation on a
	// binding. Record every restored binding independently of its state row.
	if _, err := tx.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) SELECT id FROM resource_bindings ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=false`); err != nil {
		return err
	}
	// Readiness from a restored database cannot prove current host state, even
	// for a server that has never hosted a binding.
	if _, err := tx.Exec(ctx, `DELETE FROM server_readiness`); err != nil {
		return err
	}
	// Even an idle or disabled proxy may have served later generations after
	// this backup. Restored database state cannot prove its daemon generation.
	if _, err := tx.Exec(ctx, `INSERT INTO hosting_proxy_restore_holds(proxy_id) SELECT id FROM hosting_proxies ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	if err := store.InvalidateServerBootstrapForRestoreTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// runBackupTool supplies the database password through a private libpq passfile.
// Neither argv nor the child's inherited environment contains the owner URL or
// PGPASSWORD. Tool output may contain connection details, so errors stay bounded.
func runBackupTool(ctx context.Context, tool, databaseURL string, args ...string) error {
	return runBackupToolIO(ctx, tool, databaseURL, nil, nil, args...)
}

func runBackupToolIO(ctx context.Context, tool, databaseURL string, input io.Reader, output io.Writer, args ...string) (result error) {
	connection, err := url.Parse(databaseURL)
	if err != nil || connection.Host == "" || (connection.Scheme != "postgres" && connection.Scheme != "postgresql") {
		return errors.New("backup tool database URL is invalid")
	}
	// libpq treats '+' literally in URIs, unlike HTML form query encoding.
	query, err := url.ParseQuery(strings.ReplaceAll(connection.RawQuery, "+", "%2B"))
	if err != nil || len(query["password"]) > 1 {
		return errors.New("backup tool database URL is invalid")
	}
	// libpq passfiles cannot represent line breaks. SSL key passwords use a
	// different libpq mechanism and must not accidentally remain on argv.
	if query.Has("sslpassword") || query.Has("oauth_client_secret") {
		return errors.New("backup tools require SSL key and OAuth credentials outside the database URL")
	}
	password := os.Getenv("PGPASSWORD")
	if connection.User != nil {
		if supplied, ok := connection.User.Password(); ok {
			password = supplied
		}
		connection.User = url.User(connection.User.Username())
	}
	if query.Has("password") {
		password = query.Get("password")
		query.Del("password")
	}
	if strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("backup tool password cannot contain line breaks or NUL")
	}
	if password != "" {
		dir, err := os.MkdirTemp("", "nerocd-libpq-")
		if err != nil {
			return errors.New("backup tool credentials could not be staged")
		}
		defer func() {
			if err := os.RemoveAll(dir); err != nil && result == nil {
				result = errors.New("backup tool credentials could not be removed")
			}
		}()
		passfile := filepath.Join(dir, "pgpass")
		escaped := strings.NewReplacer("\\", "\\\\", ":", "\\:").Replace(password)
		// This file belongs to one process invocation and one explicit URL. The
		// wildcard fields also cover libpq multi-host and Unix-socket URLs.
		if err := os.WriteFile(passfile, []byte("*:*:*:*:"+escaped+"\n"), 0600); err != nil {
			return errors.New("backup tool credentials could not be staged")
		}
		// An explicit URI passfile overrides an inherited PGPASSFILE or a
		// caller's passfile parameter when the URL supplies its own password.
		query.Set("passfile", passfile)
		// Clear service-file passwords too. libpq uses the passfile when this
		// explicit password parameter is empty and PGPASSWORD is absent.
		query.Set("password", "")
	}
	connection.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	command := backupCommand(ctx, tool, append([]string{"--dbname=" + connection.String()}, args...)...)
	command.Env = make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PGPASSWORD" || strings.HasPrefix(key, "NEROCD_") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Stdin = input
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed", tool)
	}
	return nil
}

func validateRestoreConfirmation(allowed bool, confirmed string) error {
	if !allowed || strings.TrimSpace(confirmed) == "" {
		return errors.New("restore requires --allow-disposable-target and --confirm-target-database")
	}
	return nil
}

// acquireDisposableRestoreTarget has no destructive action. It serializes
// restore admission, requires operator confirmation of the exact database, and
// rejects every user-visible object category before pg_restore gets a chance to
// connect. It deliberately does not terminate another session.
func acquireDisposableRestoreTarget(ctx context.Context, pool *pgxpool.Pool, confirmed string) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	release := func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('nerocd_restore_admission_v1'))`)
		conn.Release()
	}
	var database string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		conn.Release()
		return nil, err
	}
	if database != strings.TrimSpace(confirmed) {
		conn.Release()
		return nil, errors.New("restore target database does not match explicit confirmation")
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('nerocd_restore_admission_v1'))`).Scan(&locked); err != nil {
		conn.Release()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, errors.New("another restore admission is active for this database")
	}
	var activeSessions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid <> pg_backend_pid()`).Scan(&activeSessions); err != nil {
		release()
		return nil, err
	}
	if activeSessions != 0 {
		release()
		return nil, errors.New("restore target has active sessions")
	}
	var objects int
	const userObjects = `
SELECT count(*) FROM (
  SELECT n.oid FROM pg_namespace n WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'public') AND n.nspname !~ '^pg_toast'
  UNION ALL SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' OR (n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast')
  UNION ALL SELECT p.oid FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' OR (n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast')
  UNION ALL SELECT t.oid FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='public' OR (n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast')
  UNION ALL SELECT o.oid FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname='public' OR (n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast')
  UNION ALL SELECT x.oid FROM pg_extension x WHERE x.extname <> 'plpgsql'
) objects`
	if err := conn.QueryRow(ctx, userObjects).Scan(&objects); err != nil {
		release()
		return nil, err
	}
	if objects != 0 {
		release()
		return nil, errors.New("restore target must contain no user schemas or objects")
	}
	return release, nil
}

func decodeBackupManifest(raw []byte) (backupManifest, error) {
	if len(raw) == 0 || len(raw) > maxBackupMetadataBytes {
		return backupManifest{}, errors.New("backup manifest is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest backupManifest
	if err := decoder.Decode(&manifest); err != nil {
		return backupManifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return backupManifest{}, errors.New("backup manifest must contain exactly one JSON document")
	}
	return manifest, nil
}

// resolveBackupRestoreDatabaseURL refuses process arguments and plaintext
// environment credentials in production. Backup and restore require the
// migration-owner credential: restore changes schema and both operations must
// be able to inspect the complete durable state.
func resolveBackupRestoreDatabaseURL(fs *flag.FlagSet, supplied string) (string, error) {
	mode, err := configuredMode()
	if err != nil {
		return "", err
	}
	if mode == modeProduction {
		providedOnCommandLine := false
		fs.Visit(func(item *flag.Flag) {
			if item.Name == "database-url" {
				providedOnCommandLine = true
			}
		})
		if providedOnCommandLine || strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL")) != "" {
			return "", errors.New("production backup and restore require the owner secret file, not a database URL argument or environment value")
		}
		if strings.TrimSpace(os.Getenv("NEROCD_DATABASE_CREDENTIAL")) != "owner" {
			return "", errors.New("production backup and restore require the owner database credential")
		}
		return loadDatabaseURL(mode)
	}
	if strings.TrimSpace(supplied) == "" {
		supplied = strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL"))
	}
	if supplied == "" {
		return "", errors.New("backup and restore require --database-url or NEROCD_DATABASE_URL")
	}
	if err := validateDatabaseURL(supplied); err != nil {
		return "", err
	}
	return supplied, nil
}

func ensureSecureBackupParent(path string) error {
	if err := secureBackupDirectory(path); err != nil {
		return errors.New("backup output directory must already exist")
	}
	return nil
}

func ensureSecureBackupFile(path string) error {
	if err := secureBackupRegular(path); err != nil {
		return errors.New("backup file must be a private regular file")
	}
	return nil
}

func checksumOpenBackupFile(file *os.File, name string) (backupFile, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return backupFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, file)
	if err != nil {
		return backupFile{}, err
	}
	return backupFile{Path: name, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

func validateBackupCompatibility(manifest backupManifest) error {
	application, schema, migrations, err := backupCompatibility()
	if err != nil || manifest.ApplicationVersion != application || manifest.SchemaIdentity != schema || strings.Join(manifest.Migrations, "\n") != strings.Join(migrations, "\n") {
		return errors.New("backup application or schema compatibility check failed")
	}
	return nil
}

func backupCompatibility() (string, string, []string, error) {
	files, err := migrationFiles()
	if err != nil {
		return "", "", nil, err
	}
	identity := sha256.New()
	migrations := make([]string, 0, len(files))
	for _, name := range files {
		contents, err := db.Files.ReadFile(name)
		if err != nil {
			return "", "", nil, err
		}
		checksum := sqlChecksum(contents)
		migrations = append(migrations, name+":"+checksum)
		_, _ = io.WriteString(identity, name+"\x00"+checksum+"\n")
	}
	riverEntries, err := hostingjobs.CompatibilityEntries()
	if err != nil {
		return "", "", nil, err
	}
	for _, entry := range riverEntries {
		migrations = append(migrations, entry)
		_, _ = io.WriteString(identity, entry+"\n")
	}
	return version, "sha256:" + hex.EncodeToString(identity.Sum(nil)), migrations, nil
}
func backupDatabaseState(ctx context.Context, databaseURL string) ([]string, string, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, "", err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT version || ':' || checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, "", err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return nil, "", err
	}
	riverEntries, err := hostingjobs.AppliedCompatibilityEntries(ctx, pool)
	if err != nil {
		return nil, "", errors.New("backup River migration compatibility check failed")
	}
	versions = append(versions, riverEntries...)
	return versions, name, nil
}
func inventoryRunnerFiles(root string, requirements []runnerFileInventory) ([]runnerFileInventory, error) {
	resolver, err := runner.OpenFileSecretResolver(root)
	if err != nil {
		return nil, fmt.Errorf("runner-file root is unsafe: %w", err)
	}
	defer func() { _ = resolver.Close() }()
	result := append([]runnerFileInventory(nil), requirements...)
	sortRunnerFileInventory(result)
	for _, requirement := range result {
		if requirement.Provider != domain.ProviderRunnerFile || strings.TrimSpace(requirement.Reference) == "" || strings.TrimSpace(requirement.Version) == "" {
			return nil, errors.New("runner-file inventory is invalid")
		}
		if _, err := resolver.ReadBytes(requirement.Reference); err != nil {
			return nil, errors.New("runner-file inventory cannot satisfy required logical binding")
		}
	}
	return result, nil
}

func runnerFileRequirements(ctx context.Context, databaseURL string) ([]runnerFileInventory, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	unique := map[string]runnerFileInventory{}
	add := func(item runnerFileInventory) {
		unique[item.Provider+"\x00"+item.Reference+"\x00"+item.Version] = item
	}
	collectBindings := func(raw []byte) error {
		var bindings []domain.SecretBinding
		if err := decodeStoredBackupJSON(raw, '[', &bindings); err != nil {
			return errors.New("stored runner-file bindings are invalid")
		}
		return collectRunnerFileBindings(bindings, add)
	}
	collectRunSpec := func(raw []byte) error {
		var spec domain.RunSpec
		if err := decodeStoredBackupJSON(raw, '{', &spec); err != nil {
			return errors.New("stored run specification is invalid")
		}
		return collectRunnerFilesFromRunSpec(spec, add)
	}
	collectWorkflow := func(raw []byte) error {
		var workflow domain.Workflow
		if err := decodeStoredBackupJSON(raw, '{', &workflow); err != nil {
			return errors.New("stored workflow is invalid")
		}
		return collectRunnerFilesFromWorkflow(workflow, add)
	}

	rows, err := pool.Query(ctx, `SELECT secret_bindings FROM environments`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err := collectBindings(raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("stored environment secret bindings are invalid: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, sourceQuery := range []struct {
		name  string
		query string
	}{
		{name: "task template", query: `SELECT run_spec, workflow FROM task_templates`},
		{name: "task run", query: `SELECT run_spec, workflow FROM task_runs`},
	} {
		rows, err = pool.Query(ctx, sourceQuery.query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rawSpec, rawWorkflow []byte
			if err := rows.Scan(&rawSpec, &rawWorkflow); err != nil {
				rows.Close()
				return nil, err
			}
			if err := collectRunSpec(rawSpec); err != nil {
				rows.Close()
				return nil, fmt.Errorf("stored %s run specification is invalid: %w", sourceQuery.name, err)
			}
			if err := collectWorkflow(rawWorkflow); err != nil {
				rows.Close()
				return nil, fmt.Errorf("stored %s workflow is invalid: %w", sourceQuery.name, err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	result := make([]runnerFileInventory, 0, len(unique))
	for _, item := range unique {
		result = append(result, item)
	}
	sortRunnerFileInventory(result)
	return result, nil
}

func decodeStoredBackupJSON(raw []byte, opening byte, target any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxStoredRunSpecBytes || raw[0] != opening {
		return errors.New("invalid stored JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("stored JSON must contain exactly one document")
	}
	return nil
}

func collectRunnerFilesFromRunSpec(spec domain.RunSpec, add func(runnerFileInventory)) error {
	state := runnerFileCollectionState{add: add}
	return state.runSpec(spec, 0)
}

func collectRunnerFilesFromWorkflow(workflow domain.Workflow, add func(runnerFileInventory)) error {
	state := runnerFileCollectionState{add: add}
	return state.workflow(workflow, 0)
}

type runnerFileCollectionState struct {
	nodes int
	add   func(runnerFileInventory)
}

func (s *runnerFileCollectionState) runSpec(spec domain.RunSpec, depth int) error {
	if depth > maxStoredRunSpecDepth || s.nodes >= maxStoredRunSpecNodes {
		return errors.New("stored run specification exceeds backup traversal limit")
	}
	s.nodes++
	if err := collectRunnerFileBindings(spec.Secrets, s.add); err != nil {
		return err
	}
	if spec.Workflow != nil {
		return s.workflow(*spec.Workflow, depth+1)
	}
	return nil
}

func (s *runnerFileCollectionState) workflow(workflow domain.Workflow, depth int) error {
	if depth > maxStoredRunSpecDepth || len(workflow.Steps) > maxStoredRunSpecNodes-s.nodes {
		return errors.New("stored workflow exceeds backup traversal limit")
	}
	for _, step := range workflow.Steps {
		if err := s.runSpec(step.RunSpec, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func collectRunnerFileBindings(bindings []domain.SecretBinding, add func(runnerFileInventory)) error {
	for _, binding := range bindings {
		// Validate every stored binding, not only runner_file values. An invalid
		// record is a corrupted execution specification and must stop publication.
		if err := runner.ValidateSecretBinding(binding); err != nil {
			return errors.New("stored runner-file binding is invalid")
		}
		provider := strings.ToLower(strings.TrimSpace(binding.Provider))
		if provider == domain.ProviderRunnerFile {
			add(runnerFileInventory{
				Provider:  domain.ProviderRunnerFile,
				Reference: strings.TrimSpace(binding.Reference),
				Version:   strings.TrimSpace(binding.Version),
			})
		}
	}
	return nil
}

func sortRunnerFileInventory(items []runnerFileInventory) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Provider != items[j].Provider {
			return items[i].Provider < items[j].Provider
		}
		if items[i].Reference != items[j].Reference {
			return items[i].Reference < items[j].Reference
		}
		return items[i].Version < items[j].Version
	})
}

func sameRunnerFileInventory(expected, actual []runnerFileInventory) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}
