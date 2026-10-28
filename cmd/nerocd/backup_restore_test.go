package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestBackupToolKeepsPasswordsOffArgvAndRemovesPrivatePassfile(t *testing.T) {
	password := `private:pass\word % value`
	for _, source := range []string{"userinfo", "query", "environment"} {
		for _, outcome := range []string{"success", "failure", "canceled"} {
			t.Run(source+"/"+outcome, func(t *testing.T) {
				t.Setenv("PGPASSWORD", "ambient-password")
				t.Setenv("NEROCD_DATABASE_URL", "postgres://owner:parent-secret@db/control")
				t.Setenv("NEROCD_DATABASE_URL_FILE", "/private/owner-credential")
				t.Setenv("NEROCD_TOKEN", "parent-token")
				t.Setenv("PGPASSFILE", "/unused/ambient-passfile")
				connection, err := url.Parse("postgres://owner@db:5433/control?sslmode=require&application_name=backup&passfile=/unused/url-passfile")
				if err != nil {
					t.Fatal(err)
				}
				switch source {
				case "userinfo":
					connection.User = url.UserPassword("owner", password)
				case "query":
					connection.User = url.UserPassword("owner", "superseded-password")
					query := connection.Query()
					query.Set("password", password)
					connection.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
				case "environment":
					t.Setenv("PGPASSWORD", password)
				}
				previous := backupCommand
				t.Cleanup(func() { backupCommand = previous })
				var command *exec.Cmd
				var passfile string
				backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
					if tool != "pg_dump" || len(args) != 2 || args[1] != "--format=custom" || !strings.HasPrefix(args[0], "--dbname=") {
						t.Fatal("unexpected tool invocation", tool, args)
					}
					for _, secret := range []string{password, url.QueryEscape(password), "ambient-password", "superseded-password", "parent-secret"} {
						if strings.Contains(strings.Join(args, " "), secret) {
							t.Fatal("credential reached tool argv")
						}
					}
					parsed, err := url.Parse(strings.TrimPrefix(args[0], "--dbname="))
					if err != nil {
						t.Fatal("invalid tool URL")
					}
					if _, present := parsed.User.Password(); present || parsed.Query().Get("password") != "" || parsed.Query().Get("sslmode") != "require" || parsed.Query().Get("application_name") != "backup" || parsed.Host != "db:5433" || parsed.Path != "/control" || parsed.User.Username() != "owner" {
						t.Fatal("tool URL changed connection semantics or retained a password")
					}
					passfile = parsed.Query().Get("passfile")
					fileInfo, err := os.Stat(passfile)
					if err != nil || fileInfo.Mode().Perm() != 0600 {
						t.Fatal("passfile is not private", err)
					}
					parentInfo, err := os.Stat(filepath.Dir(passfile))
					if err != nil || parentInfo.Mode().Perm() != 0700 {
						t.Fatal("passfile directory is not private", err)
					}
					raw, err := os.ReadFile(passfile)
					if err != nil || string(raw) != "*:*:*:*:private\\:pass\\\\word % value\n" {
						t.Fatal("libpq password escaping is incorrect", err)
					}
					script := "exit 0"
					if outcome == "failure" {
						script = "echo secret-tool-diagnostic >&2; exit 1"
					}
					command = exec.CommandContext(ctx, "sh", "-c", script)
					return command
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if outcome == "canceled" {
					cancel()
				}
				err = runBackupTool(ctx, "pg_dump", connection.String(), "--format=custom")
				if (err == nil) != (outcome == "success") || err != nil && err.Error() != "pg_dump failed" {
					t.Fatal("unexpected tool outcome", err)
				}
				for _, entry := range command.Env {
					if strings.HasPrefix(entry, "NEROCD_") || strings.HasPrefix(entry, "PGPASSWORD=") {
						t.Fatal("parent credential reached tool environment")
					}
				}
				if _, err := os.Stat(filepath.Dir(passfile)); !os.IsNotExist(err) {
					t.Fatal("credential directory remains after tool exit", err)
				}
			})
		}
	}
}

func TestBackupToolRejectsUnrepresentableCredentialsBeforeStarting(t *testing.T) {
	previous := backupCommand
	t.Cleanup(func() { backupCommand = previous })
	backupCommand = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("invalid credentials started a tool")
		return nil
	}
	for _, connection := range []string{
		"postgres://owner:line%0Abreak@db/control",
		"postgres://owner:line%0Dbreak@db/control",
		"postgres://owner:nul%00byte@db/control",
		"postgres://owner@db/control?password=one&password=two",
		"postgres://owner@db/control?password=%invalid",
		"postgres://owner@db/control?sslpassword=private-key-secret",
		"postgres://owner@db/control?oauth_client_secret=private-key-secret",
		"postgres://owner@db/control?oauth_client_%73ecret=private-key-secret",
	} {
		if err := runBackupTool(t.Context(), "pg_restore", connection); err == nil || strings.Contains(err.Error(), "private-key-secret") {
			t.Fatal("invalid credentials accepted or exposed")
		}
	}
}

func TestBackupToolPreservesURIQueryEncoding(t *testing.T) {
	t.Setenv("PGPASSWORD", "")
	previous := backupCommand
	t.Cleanup(func() { backupCommand = previous })
	backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
		parsed, err := url.Parse(strings.TrimPrefix(args[0], "--dbname="))
		if err != nil {
			t.Fatal(err)
		}
		values := make(map[string]string)
		for _, pair := range strings.Split(parsed.RawQuery, "&") {
			key, value, _ := strings.Cut(pair, "=")
			decoded, err := url.PathUnescape(value)
			if err != nil {
				t.Fatal(err)
			}
			values[key] = decoded
		}
		if values["options"] != "-c default_transaction_read_only=on" || values["application_name"] != "backup+verify run" || values["sslcert"] != "/private/cert files/client.pem" {
			t.Fatalf("libpq URI values changed: %#v", values)
		}
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	if err := runBackupTool(t.Context(), "pg_dump", "postgres://owner@db/control?options=-c%20default_transaction_read_only%3Don&application_name=backup+verify%20run&sslcert=%2Fprivate%2Fcert%20files%2Fclient.pem"); err != nil {
		t.Fatal(err)
	}
}

func TestBackupToolURIQueryWithLibpq(t *testing.T) {
	container := os.Getenv("NEROCD_TEST_LIBPQ_CONTAINER")
	if container == "" {
		t.Skip("set NEROCD_TEST_LIBPQ_CONTAINER to a disposable PostgreSQL container with local postgres access")
	}
	t.Setenv("PGPASSWORD", "")
	previous := backupCommand
	t.Cleanup(func() { backupCommand = previous })
	backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "docker", append([]string{"exec", container, tool}, args...)...)
	}
	query := "DO $$ BEGIN IF current_setting('application_name') <> 'backup+verify run' OR current_setting('default_transaction_read_only') <> 'on' THEN RAISE EXCEPTION 'connection settings changed'; END IF; END $$;"
	if err := runBackupTool(t.Context(), "psql", "postgres://postgres@localhost/postgres?options=-c%20default_transaction_read_only%3Don&application_name=backup+verify%20run", "--set=ON_ERROR_STOP=1", "--command="+query); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBackupArchiveRejectsTamperingAndUnexpectedEntries(t *testing.T) {
	root := privateBackupTestDir(t)
	archive := filepath.Join(root, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	dump := []byte("backup-data")
	if err := os.WriteFile(filepath.Join(archive, "database.dump"), dump, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(dump)
	manifest := backupManifest{Version: backupManifestVersion, CreatedAt: time.Now().UTC(), ApplicationVersion: "test", SchemaIdentity: "sha256:test", Database: "nerocd", Files: []backupFile{{Path: "database.dump", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(dump))}}}
	if err := writeBackupTestManifest(archive, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackupArchive(archive); err != nil {
		t.Fatalf("verifyBackupArchive(valid) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "database.dump"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackupArchive(archive); err == nil {
		t.Fatal("verifyBackupArchive accepted checksum mismatch")
	}
	if err := os.WriteFile(filepath.Join(archive, "database.dump"), dump, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "unexpected"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackupArchive(archive); err == nil {
		t.Fatal("verifyBackupArchive accepted unexpected archive entry")
	}
}

func TestBackupExportPublishesPrivateVerifiedCopy(t *testing.T) {
	sourceRoot := privateBackupTestDir(t)
	destination := privateBackupTestDir(t)
	archive := filepath.Join(sourceRoot, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	dump := []byte("backup-data")
	if err := os.WriteFile(filepath.Join(archive, "database.dump"), dump, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(dump)
	manifest := backupManifest{Version: backupManifestVersion, CreatedAt: time.Now().UTC(), ApplicationVersion: "test", SchemaIdentity: "sha256:test", Database: "nerocd", Files: []backupFile{{Path: "database.dump", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(dump))}}}
	if err := writeBackupTestManifest(archive, manifest); err != nil {
		t.Fatal(err)
	}
	if err := backupExport([]string{"--input-dir", archive, "--output-dir", destination}); err != nil {
		t.Fatalf("backupExport() error = %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("export entries = %d, want 1", len(entries))
	}
	exported := filepath.Join(destination, entries[0].Name())
	if _, err := verifyBackupArchive(exported); err != nil {
		t.Fatalf("verifyBackupArchive(exported) error = %v", err)
	}
	info, err := os.Stat(exported)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("exported mode = %o, want 700", info.Mode().Perm())
	}
}

func TestRestoreRequiresExplicitDisposableTargetConfirmation(t *testing.T) {
	for name, tc := range map[string]struct {
		allowed  bool
		database string
	}{
		"missing_capability": {database: "restore_target"},
		"missing_database":   {allowed: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRestoreConfirmation(tc.allowed, tc.database); err == nil {
				t.Fatal("validateRestoreConfirmation accepted incomplete acknowledgement")
			}
		})
	}
	if err := validateRestoreConfirmation(true, "restore_target"); err != nil {
		t.Fatalf("validateRestoreConfirmation(valid) error = %v", err)
	}
}

func privateBackupTestDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBackupRestoreDatabaseURLProductionRequiresOwnerSecret(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	t.Setenv("NEROCD_IMAGE_REF", "registry.example.invalid/nerocd@sha256:"+strings.Repeat("a", 64))
	t.Setenv("NEROCD_OWNER_DATABASE_USER", "owner")
	t.Setenv("NEROCD_DATABASE_CREDENTIAL", "owner")
	t.Setenv("NEROCD_DATABASE_URL", "")
	secret := filepath.Join(t.TempDir(), "owner-url")
	if err := os.WriteFile(secret, []byte("postgres://owner:secret@postgres/nerocd?sslmode=disable\n"), 0400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_DATABASE_URL_FILE", secret)
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.String("database-url", "", "")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	got, err := resolveBackupRestoreDatabaseURL(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://owner:secret@postgres/nerocd?sslmode=disable" {
		t.Fatalf("unexpected resolved URL %q", got)
	}

	t.Setenv("NEROCD_DATABASE_URL", "postgres://owner:secret@postgres/nerocd")
	if _, err := resolveBackupRestoreDatabaseURL(fs, ""); err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf("plaintext production URL error = %v, want secret-file rejection", err)
	}
}

func TestBackupRestoreDatabaseURLRejectsProductionCommandLineURL(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.String("database-url", "", "")
	if err := fs.Parse([]string{"--database-url", "postgres://owner:secret@postgres/nerocd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBackupRestoreDatabaseURL(fs, "postgres://owner:secret@postgres/nerocd"); err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf("error = %v, want command-line secret-file rejection", err)
	}
}

func TestSecureBackupParentAndRunnerInventory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureSecureBackupParent(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "credential"), []byte("do-not-copy"), 0400); err != nil {
		t.Fatal(err)
	}
	requirements := []runnerFileInventory{{Provider: "runner_file", Reference: "credential", Version: "v1"}}
	inventory, err := inventoryRunnerFiles(root, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 1 || inventory[0] != requirements[0] {
		t.Fatalf("inventory = %#v", inventory)
	}
	if err := os.Chmod(filepath.Join(root, "credential"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := inventoryRunnerFiles(root, requirements); err == nil {
		t.Fatal("group-readable runner file was accepted")
	}
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	if err := ensureSecureBackupParent(root); err == nil {
		t.Fatal("group-readable backup parent was accepted")
	}
}

func TestWriteBackupManifest(t *testing.T) {
	directory := privateBackupTestDir(t)
	path := filepath.Join(directory, "manifest.json")
	manifest := backupManifest{Version: backupManifestVersion, Files: []backupFile{{Path: "database.dump", SHA256: "abc", Bytes: 7}}}
	if err := writeBackupTestManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("manifest mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 1`) || !strings.Contains(string(raw), `"sha256": "abc"`) {
		t.Fatalf("manifest did not contain expected fields: %s", raw)
	}
}

func TestBackupCompatibilityIsBoundToEmbeddedApplicationMigrations(t *testing.T) {
	application, identity, migrations, err := backupCompatibility()
	if err != nil {
		t.Fatal(err)
	}
	if application != version || !strings.HasPrefix(identity, "sha256:") || len(migrations) == 0 {
		t.Fatalf("compatibility = version=%q identity=%q migrations=%d", application, identity, len(migrations))
	}
	if !strings.Contains(migrations[0], ":sha256:") {
		t.Fatalf("migration identity is not checksummed: %q", migrations[0])
	}
}

func TestDecodeBackupManifestIsStrictAndBounded(t *testing.T) {
	valid := []byte(`{"version":1,"created_at":"2026-01-01T00:00:00Z","application_version":"v","schema_identity":"sha256:x","database":"db","migrations":[],"files":[{"path":"database.dump","sha256":"abc","bytes":1}]}`)
	if _, err := decodeBackupManifest(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"unknown":true}`),
		[]byte(`{"version":1}{"version":1}`),
		[]byte(`{"version":1,"files":[{"path":"database.dump","unknown":true}]}`),
		[]byte(strings.Repeat("x", maxBackupMetadataBytes+1)),
	} {
		if _, err := decodeBackupManifest(raw); err == nil {
			t.Fatalf("accepted invalid manifest %q", raw[:min(len(raw), 32)])
		}
	}
}

func TestBackupDumpCommandFailureCleansStagingWithoutPublishing(t *testing.T) {
	output, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0700); err != nil {
		t.Fatal(err)
	}
	previous := backupCommand
	backupCommand = func(context.Context, string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "exit 1")
	}
	t.Cleanup(func() { backupCommand = previous })
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_DATABASE_URL", "")
	err = backupDatabase([]string{"--database-url", "postgres://user:password@db/nerocd", "--output-dir", output})
	if err == nil || !strings.Contains(err.Error(), "pg_dump failed") {
		t.Fatalf("backup error = %v", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("interrupted dump published/staged entries: %#v", entries)
	}
}

func TestBackupPathRejectsWrongOwnerWhenRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("wrong-owner backup path requires root; Docker Desktop bind ownership coverage records its platform skip")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(file, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := ensureSecureBackupFile(file); err == nil {
		t.Fatal("wrong-owner backup file accepted")
	}
}

func TestRunnerFileCollectorCoversEnvironmentTemplateRunAndNestedWorkflow(t *testing.T) {
	unique := map[string]runnerFileInventory{}
	add := func(item runnerFileInventory) {
		unique[item.Provider+"\x00"+item.Reference+"\x00"+item.Version] = item
	}
	binding := func(name, reference, version string) domain.SecretBinding {
		return domain.SecretBinding{Name: name, Provider: domain.ProviderRunnerFile, Reference: reference, Target: "env:" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")), Required: true, Version: version}
	}
	// This represents environments.secret_bindings.
	if err := collectRunnerFileBindings([]domain.SecretBinding{binding("environment", "environment.json", "v1")}, add); err != nil {
		t.Fatal(err)
	}
	// This represents task_templates.run_spec plus its separate workflow JSON.
	template := domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{binding("template", "template.json", "v1")}, Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested-template", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{binding("duplicate", "template.json", "v1")}}}}}}
	if err := collectRunnerFilesFromRunSpec(template, add); err != nil {
		t.Fatal(err)
	}
	if err := collectRunnerFilesFromWorkflow(domain.Workflow{Steps: []domain.WorkflowStep{{ID: "template-step", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{binding("template-workflow", "template-workflow.json", "v2")}}}}}, add); err != nil {
		t.Fatal(err)
	}
	// This represents task_runs.run_spec and a nested generic-workflow step.
	run := domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{binding("run", "run.json", "v1")}, Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested-run", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{binding("run-workflow", "run-workflow.json", "v3")}}}}}}
	if err := collectRunnerFilesFromRunSpec(run, add); err != nil {
		t.Fatal(err)
	}
	items := make([]runnerFileInventory, 0, len(unique))
	for _, item := range unique {
		items = append(items, item)
	}
	sortRunnerFileInventory(items)
	want := []runnerFileInventory{
		{Provider: domain.ProviderRunnerFile, Reference: "environment.json", Version: "v1"},
		{Provider: domain.ProviderRunnerFile, Reference: "run-workflow.json", Version: "v3"},
		{Provider: domain.ProviderRunnerFile, Reference: "run.json", Version: "v1"},
		{Provider: domain.ProviderRunnerFile, Reference: "template-workflow.json", Version: "v2"},
		{Provider: domain.ProviderRunnerFile, Reference: "template.json", Version: "v1"},
	}
	if !sameRunnerFileInventory(want, items) {
		t.Fatalf("inventory = %#v, want %#v", items, want)
	}
}

func TestRunnerFileCollectorFailsClosedOnMalformedStoredSpecifications(t *testing.T) {
	if err := decodeStoredBackupJSON([]byte(`{"type":"shell","unexpected":true}`), '{', &domain.RunSpec{}); err == nil {
		t.Fatal("unknown RunSpec field was accepted")
	}
	if err := collectRunnerFileBindings([]domain.SecretBinding{{Name: "bad", Provider: domain.ProviderRunnerFile, Reference: "../escape", Target: "env:BAD", Version: "v1"}}, func(runnerFileInventory) {}); err == nil {
		t.Fatal("invalid runner-file binding was accepted")
	}
	deep := domain.RunSpec{Type: domain.RunTypeShell}
	for range maxStoredRunSpecDepth + 2 {
		deep = domain.RunSpec{Type: domain.RunTypeShell, Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "step", RunSpec: deep}}}}
	}
	if err := collectRunnerFilesFromRunSpec(deep, func(runnerFileInventory) {}); err == nil {
		t.Fatal("over-deep nested workflow was accepted")
	}
}

func TestBackupCompatibilityIncludesPinnedRiverAndRejectsOlderArchives(t *testing.T) {
	application, identity, migrations, err := backupCompatibility()
	if err != nil {
		t.Fatal(err)
	}
	riverCount := 0
	for _, entry := range migrations {
		if strings.HasPrefix(entry, "river/v0.44.1/main/") {
			riverCount++
		}
	}
	if riverCount != 7 {
		t.Fatalf("pinned River identity entries=%d", riverCount)
	}
	valid := backupManifest{ApplicationVersion: application, SchemaIdentity: identity, Migrations: migrations}
	if err := validateBackupCompatibility(valid); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"application", "identity", "river hash", "river version", "older backup"} {
		t.Run(mode, func(t *testing.T) {
			candidate := valid
			candidate.Migrations = append([]string(nil), migrations...)
			switch mode {
			case "application":
				candidate.ApplicationVersion = "older"
			case "identity":
				candidate.SchemaIdentity = "sha256:older"
			case "river hash":
				candidate.Migrations[len(candidate.Migrations)-1] += "changed"
			case "river version":
				candidate.Migrations[len(candidate.Migrations)-1] = strings.ReplaceAll(candidate.Migrations[len(candidate.Migrations)-1], "v0.44.1", "v0.43.0")
			case "older backup":
				candidate.Migrations = candidate.Migrations[:len(candidate.Migrations)-7]
			}
			if err := validateBackupCompatibility(candidate); err == nil {
				t.Fatal("incompatible backup accepted")
			}
		})
	}
}

func writeBackupTestManifest(path string, manifest backupManifest) error {
	directory, err := openSecureBackupDirectory(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return writeBackupManifestAt(directory, manifest)
}
