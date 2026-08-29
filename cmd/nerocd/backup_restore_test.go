package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

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
	if err := writeAtomicJSON(filepath.Join(archive, "manifest.json"), manifest); err != nil {
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
	if err := writeAtomicJSON(filepath.Join(archive, "manifest.json"), manifest); err != nil {
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

func TestWriteAtomicBackupManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest := backupManifest{Version: backupManifestVersion, Files: []backupFile{{Path: "database.dump", SHA256: "abc", Bytes: 7}}}
	if err := writeAtomicJSON(path, manifest); err != nil {
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
