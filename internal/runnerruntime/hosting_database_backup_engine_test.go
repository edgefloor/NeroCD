//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
)

func backupEngineFixture(t *testing.T) (*databaseBackupEngine, *databaseFake, domain.HostingDatabaseBackupPlan) {
	t.Helper()
	f, credentials := databaseFixture(t, "postgresql")
	t.Cleanup(credentials.Close)
	result := f.apply(t.Context(), credentials)
	if result.Outcome != "succeeded" {
		t.Fatal("database source setup", result)
	}
	state := f.containers[result.ContainerID]["State"].(map[string]any)
	state["Pid"] = 4321
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) == 5 && slices.Equal(args[:2], []string{"container", "top"}) {
			return []byte("PID UID\n4321 999\n"), nil, true
		}
		return nil, nil, false
	}
	h := domain.HostingHealthy{OperationID: f.req.OriginOperationID, ContainerID: result.ContainerID, PlanHash: f.plan.Hash, Snapshot: f.plan.Snapshot, Release: f.plan.Release, Storage: &domain.HostingHealthyStorage{Plan: *f.plan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
	p := domain.HostingDatabaseBackupPlan{Operation: domain.HostingDatabaseBackupOperation{MaxBytes: 1 << 20, TimeoutSeconds: 30, ID: "backup", Version: domain.HostingDatabaseBackupVersion, ProjectID: f.req.Storage.ProjectID, SourceResourceID: h.Snapshot.ResourceID, SourceGeneration: 1, BackupID: "backup", Mode: "backup", State: "queued", OwnershipHeld: true}, ActorID: "actor", RunnerID: h.Snapshot.RunnerID, Source: h, SourcePlan: f.plan, SourceStorage: f.req.Storage, Credentials: f.plan.Database.Credentials, LocationID: "backup", MaxBytes: 1 << 20, TimeoutSeconds: 30}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid backup plan")
	}
	path, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := artifactfs.OpenStore(path, p.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	e := &databaseBackupEngine{image: f.e, host: f.config, root: root, store: store}
	t.Cleanup(func() { _ = root.Close(); _ = store.Close() })
	return e, f, p
}
func TestDatabaseBackupEngineDumpCommandsAndPublication(t *testing.T) {
	e, f, p := backupEngineFixture(t)
	var commands [][]string
	payload := append([]byte("PGDMP"), bytes.Repeat([]byte("x"), 65532)...)
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		commands = append(commands, append([]string(nil), args...))
		if in != nil {
			t.Fatal("dump unexpectedly received credentials or input")
		}
		_, err := out.Write(payload)
		return err
	}
	before := len(f.calls)
	blob, err := e.run(t.Context(), p, f.before)
	if err != nil || blob.Bytes != 65537 {
		t.Fatal("logical dump", err)
	}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], databaseBackupArgs("backup", p.Source.ContainerID, 30)) {
		t.Fatal("noncanonical dump command", commands)
	}
	for _, arg := range commands[0] {
		if strings.Contains(arg, "PASSWORD") || strings.Contains(arg, "/var/") {
			t.Fatal("credential/path escaped into command")
		}
	}
	if f.creates != 1 || f.starts != 1 || f.stops != 0 || f.removes != 0 || len(f.calls) <= before {
		t.Fatal("dump changed database lifecycle")
	}
	p.Operation.Blob = &blob
	reader, err := e.archive(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	got, _ := io.ReadAll(reader)
	if !bytes.Equal(got, payload) {
		t.Fatal("published dump changed")
	}
	if _, err = e.root.Stat(p.LocationID + ".partial"); err != nil {
		t.Fatal("completed local staging was deleted")
	}
	if _, err = e.run(t.Context(), p, f.before); err == nil || len(commands) != 1 {
		t.Fatal("same-attempt dump was repeated")
	}
}
func TestDatabaseBackupEngineRejectsTamperAndRevokedAuthority(t *testing.T) {
	for _, kind := range []string{"authority", "mount", "daemon", "byte_limit", "publication", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			e, f, p := backupEngineFixture(t)
			effects := 0
			e.stream = func(ctx context.Context, _ []string, _ io.Reader, out io.Writer) error {
				effects++
				_, err := out.Write(append([]byte("PGDMP"), bytes.Repeat([]byte("x"), 100)...))
				return err
			}
			before := f.before
			ctx := t.Context()
			switch kind {
			case "authority":
				before = func(context.Context) error { return errors.New("revoked") }
			case "mount":
				p.SourceStorage.VolumeName = "foreign"
			case "daemon":
				p.Source.Release.DaemonID = "foreign"
			case "byte_limit":
				p.MaxBytes = 10
				p.Operation.MaxBytes = 10
			case "publication":
				if err := e.store.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			_, err := e.run(ctx, p, before)
			if err == nil {
				t.Fatal("unsafe dump accepted")
			}
			if (kind == "authority" || kind == "mount" || kind == "daemon" || kind == "cancel") && effects != 0 {
				t.Fatal("denied dump started")
			}
			if kind == "publication" {
				info, err := e.root.Stat(p.LocationID + ".partial")
				if err != nil || info.Size() != 105 {
					t.Fatal("completed dump removed after publication failure")
				}
			}
		})
	}
}
func TestDatabaseBackupArchiveRejectsCorruptTruncatedAndHardlinkedBlob(t *testing.T) {
	for _, kind := range []string{"digest", "truncated", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			e, _, p := backupEngineFixture(t)
			data := []byte("PGDMPvalid")
			sum := sha256.Sum256(data)
			key := hex.EncodeToString(sum[:])
			blob, err := e.store.Put(t.Context(), bytes.NewReader(data), key, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			p.Operation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + key, Bytes: blob.Size, Format: "postgresql_custom"}
			switch kind {
			case "digest":
				p.Operation.Blob.Digest = "sha256:" + strings.Repeat("f", 64)
			case "truncated":
				p.Operation.Blob.Bytes--
			case "hardlink": // Store's public Open checks hardlink count.
				rootPath := e.root.Name()
				if err = os.Link(filepath.Join(rootPath, key), filepath.Join(rootPath, "foreign-link")); err != nil {
					t.Fatal(err)
				}
			}
			if f, err := e.archive(t.Context(), p); err == nil {
				_ = f.Close()
				t.Fatal("invalid archive accepted")
			}
		})
	}
}
func TestDatabaseBackupGuestDeadlineAndRestoreOwnershipCommand(t *testing.T) {
	args := databaseBackupArgs("restore", strings.Repeat("a", 64), 900)
	joined := strings.Join(args, " ")
	for _, value := range []string{"timeout --signal=TERM --kill-after=5s 900s", "--user postgres", "--role=ncd_app", "--no-owner", "--no-acl", "--exit-on-error", "--single-transaction"} {
		if !strings.Contains(joined, value) {
			t.Fatal("missing bounded ownership argument", value)
		}
	}
	_ = time.Second
}

func backupRestoreEngineFixture(t *testing.T) (*databaseBackupEngine, *databaseFake, domain.HostingDatabaseBackupPlan) {
	t.Helper()
	e, source, p := backupEngineFixture(t)
	payload := []byte("PGDMPfixture_archive")
	sum := sha256.Sum256(payload)
	key := hex.EncodeToString(sum[:])
	if _, err := e.store.Put(t.Context(), bytes.NewReader(payload), key, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	target, originalCredentials := databaseFixture(t, "postgresql")
	originalCredentials.Close()
	storage := target.req.Storage
	storage.BindingID += "_target"
	storage.ResourceID += "_target"
	storage.ID, storage.VolumeName = domain.HostingStorageIdentity(storage.BindingID)
	storage.Hash = storage.DocumentHash()
	snapshot := target.plan.Snapshot
	snapshot.BindingID = storage.BindingID
	snapshot.ResourceID = storage.ResourceID
	snapshot.RuntimeNamespace = "ncd_" + strings.Repeat("f", 32)
	snapshot.Storage = storage.Reference()
	recipe, _ := domain.HostingDatabaseRecipeFor("postgresql", domain.HostingDatabasePostgresRecipe)
	refs := recipe.Credentials
	for i := range refs {
		refs[i].SecretID = domain.HostingDatabaseSecretID(snapshot.ResourceID, refs[i].Role)
		refs[i].Version = 1
	}
	snapshot.Database.ConfigurationHash, _ = domain.HostingDatabaseConfigurationHash(snapshot, refs)
	sp := *target.plan.Storage
	sp.Reference = snapshot.Storage
	sp.VolumeName = storage.VolumeName
	sp.CreationOperationID = "target_origin"
	sp.CreationNonce = domain.HostingStorageCreationNonce(snapshot.Storage, sp.CreationOperationID, sp.DaemonID)
	plan, err := domain.CompileHostingDatabasePlan(snapshot, target.plan.Release, nil, sp, domain.HostingDatabasePlan{Reference: snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(snapshot.BindingID), Credentials: refs})
	if err != nil {
		t.Fatal(err)
	}
	target.plan = plan
	target.req = hostingOwnedVolumeRequest{Storage: storage, DaemonID: sp.DaemonID, OriginOperationID: sp.CreationOperationID, OriginPlanHash: plan.Hash, CreationNonce: sp.CreationNonce, CreatePermitted: false}
	target.volume = ownedVolumeInspection(target.req)
	target.config.stateRoot = source.config.stateRoot
	target.config.secretRoot = source.config.secretRoot
	credentials := databaseFixtureCredentials(t, plan)
	defer credentials.Close()
	target.creates = 1
	result := target.apply(t.Context(), credentials)
	if result.Outcome != "succeeded" {
		t.Fatal("target setup", result)
	}
	target.containers[result.ContainerID]["State"].(map[string]any)["Pid"] = 4321
	target.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) == 5 && slices.Equal(args[:2], []string{"container", "top"}) {
			return []byte("PID UID\n4321 999\n"), nil, true
		}
		return nil, nil, false
	}
	h := domain.HostingHealthy{OperationID: sp.CreationOperationID, ContainerID: result.ContainerID, PlanHash: plan.Hash, Snapshot: snapshot, Release: plan.Release, Storage: &domain.HostingHealthyStorage{Plan: sp, CreationPlanHash: plan.Hash}}
	p.Operation.ID = "restore"
	p.Operation.Mode = "restore"
	p.Operation.TargetResourceID = snapshot.ResourceID
	p.Operation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + key, Bytes: int64(len(payload)), Format: "postgresql_custom"}
	p.Target = &h
	p.TargetPlan = &plan
	p.TargetStorage = &storage
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid restore fixture")
	}
	e.image = target.e
	e.host = target.config
	return e, target, p
}
func TestDatabaseBackupRestoreNeedsOnlyTargetLockAndNeverSourceHealth(t *testing.T) {
	e, target, p := backupRestoreEngineFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	sourceLock, err := e.host.lock(ctx, p.Source.Snapshot.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sourceLock.Close() }()
	// The image inspector knows only the target. Any source inspection fails.
	imports := 0
	e.stream = func(_ context.Context, args []string, in io.Reader, _ io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "--single-transaction") {
			imports++
			raw, _ := io.ReadAll(in)
			if !bytes.HasPrefix(raw, []byte("PGDMP")) {
				t.Fatal("restore did not consume admitted artifact")
			}
		}
		return nil
	}
	blob, err := e.run(ctx, p, target.before)
	if err != nil || imports != 1 || blob.Digest != p.Operation.Blob.Digest {
		t.Fatal("source lock/health blocked artifact restore", err)
	}
	targetLock, err := e.host.lock(ctx, p.Target.Snapshot.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = targetLock.Close() }()
	blocked, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, err = e.run(blocked, p, target.before); err == nil || imports != 1 {
		t.Fatal("target exclusivity was bypassed")
	}
}
