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
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
)

var errDatabaseBackupPublication = errors.New("completed local dump publication failed; staging retained")

var errDatabaseBackupHeld = errors.New("database backup or restore remains held; execution evidence requires reconciliation")

// Stages describe only controlled execution boundaries. Error never renders the
// wrapped cause, which may contain arbitrary tool or filesystem diagnostics.
type databaseBackupPhase uint8
type databaseBackupStage uint8

const (
	databaseBackupPhaseInspection databaseBackupPhase = iota + 1
	databaseBackupPhasePreflight
	databaseBackupPhaseRecheck
	databaseBackupPhaseExecution
	databaseBackupPhasePostImport
)

const (
	databaseBackupStagePlan databaseBackupStage = iota + 1
	databaseBackupStageBindingLock
	databaseBackupStageHistory
	databaseBackupStageDaemon
	databaseBackupStageOrigin
	databaseBackupStageContainerRead
	databaseBackupStageContainerIdentity
	databaseBackupStageVolume
	databaseBackupStageProcess
	databaseBackupStageStability
	databaseBackupStageDaemonRecheck
	databaseBackupStageArchive
	databaseBackupStageArchiveList
	databaseBackupStageAuthority
	databaseBackupStageRestore
	databaseBackupStageReadiness
	databaseBackupStageWriterStop
	databaseBackupStageWriterState
	databaseBackupStageSQLClients
	databaseBackupStageImportTransport
	databaseBackupStageImportTerminal
	databaseBackupStageImportExit
	databaseBackupStageWriterResume
)

func (p databaseBackupPhase) name() string {
	switch p {
	case databaseBackupPhaseInspection:
		return "inspection"
	case databaseBackupPhasePreflight:
		return "preflight"
	case databaseBackupPhaseRecheck:
		return "recheck"
	case databaseBackupPhaseExecution:
		return "execution"
	case databaseBackupPhasePostImport:
		return "post_import"
	}
	return "unknown"
}

func (s databaseBackupStage) name() string {
	switch s {
	case databaseBackupStagePlan:
		return "plan"
	case databaseBackupStageBindingLock:
		return "binding_lock"
	case databaseBackupStageHistory:
		return "history"
	case databaseBackupStageDaemon:
		return "daemon"
	case databaseBackupStageOrigin:
		return "origin"
	case databaseBackupStageContainerRead:
		return "container_read"
	case databaseBackupStageContainerIdentity:
		return "container_identity"
	case databaseBackupStageVolume:
		return "volume"
	case databaseBackupStageProcess:
		return "process"
	case databaseBackupStageStability:
		return "stability"
	case databaseBackupStageDaemonRecheck:
		return "daemon_recheck"
	case databaseBackupStageArchive:
		return "archive"
	case databaseBackupStageArchiveList:
		return "archive_list"
	case databaseBackupStageAuthority:
		return "authority"
	case databaseBackupStageRestore:
		return "restore"
	case databaseBackupStageReadiness:
		return "readiness"
	case databaseBackupStageWriterStop:
		return "writer_stop"
	case databaseBackupStageWriterState:
		return "writer_state"
	case databaseBackupStageSQLClients:
		return "sql_clients"
	case databaseBackupStageImportTransport:
		return "import_transport"
	case databaseBackupStageImportTerminal:
		return "import_terminal"
	case databaseBackupStageImportExit:
		return "import_exit"
	case databaseBackupStageWriterResume:
		return "writer_resume"
	}
	return "unknown"
}

type databaseBackupStageError struct {
	phase databaseBackupPhase
	stage databaseBackupStage
	cause error
}

func (e *databaseBackupStageError) Error() string {
	return "database backup execution failed at " + e.phase.name() + "/" + e.stage.name()
}
func (e *databaseBackupStageError) Unwrap() error { return e.cause }

func databaseBackupErrorAt(phase databaseBackupPhase, stage databaseBackupStage, cause error) error {
	if cause == nil {
		cause = errDatabaseBackupHeld
	}
	return &databaseBackupStageError{phase: phase, stage: stage, cause: cause}
}

func databaseBackupErrorPhase(err error, phase databaseBackupPhase) error {
	var stage *databaseBackupStageError
	if errors.As(err, &stage) {
		out := *stage
		out.phase = phase
		return &out
	}
	return databaseBackupErrorAt(phase, 0, err)
}

type databaseBackupEngine struct {
	writers        []domain.HostingStorageArchiveWriterState
	writersResumed bool
	localCopy      *domain.BackupLocalCopyProof
	image          *imageEngine
	host           DatabaseEngineConfig
	root           *os.Root
	store          *artifactfs.Store
	stream         func(context.Context, []string, io.Reader, io.Writer) error
}

func openDatabaseBackupEngine(ctx context.Context, config DatabaseExecutionConfig, endpoint string) (*databaseBackupEngine, error) {
	if !config.LogicalBackups {
		return nil, errDatabaseBackupHeld
	}
	host, err := NewDatabaseEngineConfig(config.StateRoot, config.SecretRoot)
	if err != nil {
		return nil, errDatabaseBackupHeld
	}
	image, err := newImageEngine(endpoint)
	if err != nil {
		return nil, errDatabaseBackupHeld
	}
	if validateDatabaseDaemon(ctx, image, host) != nil {
		_ = image.Close()
		return nil, errDatabaseBackupHeld
	}
	path := filepath.Join(config.StateRoot, "logical-backups")
	if err = os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		_ = image.Close()
		return nil, errDatabaseBackupHeld
	}
	if databaseFilePolicy(path, 0o700, os.Geteuid(), os.Getegid(), true) != nil {
		_ = image.Close()
		return nil, errDatabaseBackupHeld
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		_ = image.Close()
		return nil, errDatabaseBackupHeld
	}
	store, err := artifactfs.OpenStore(path, domain.HostingDatabaseBackupMaxBytes)
	if err != nil {
		_ = root.Close()
		_ = image.Close()
		return nil, errDatabaseBackupHeld
	}
	e := &databaseBackupEngine{image: image, host: host, root: root, store: store}
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		all := append([]string{"--host", image.endpoint, "--config", image.configDir}, args...)
		return archiveOSStream(ctx, "docker", all, image.configDir, in, out)
	}
	return e, nil
}
func (e *databaseBackupEngine) Close() error {
	var rootErr, storeErr error
	if e.root != nil {
		rootErr = e.root.Close()
	}
	if e.store != nil {
		storeErr = e.store.Close()
	}
	return errors.Join(rootErr, storeErr, e.image.Close())
}

// Docker reports mounts as an unordered collection. Preserve every tuple and
// its multiplicity while comparing fresh copies of the observation slices.
func databaseBackupContainerHash(container databaseContainer) string {
	container.Mounts = slices.Clone(container.Mounts)
	sort.Slice(container.Mounts, func(i, j int) bool {
		a, b := container.Mounts[i], container.Mounts[j]
		switch {
		case a.Type != b.Type:
			return a.Type < b.Type
		case a.Name != b.Name:
			return a.Name < b.Name
		case a.Driver != b.Driver:
			return a.Driver < b.Driver
		case a.Source != b.Source:
			return a.Source < b.Source
		case a.Destination != b.Destination:
			return a.Destination < b.Destination
		default:
			return !a.RW && b.RW
		}
	})
	return domain.HostingDocumentHash(container)
}

// inspect is existing-only. No creation, adoption, network edits or secret
// delivery is possible on this path. The controller owns the full saved proof.
func (e *databaseBackupEngine) inspect(ctx context.Context, h domain.HostingHealthy, p domain.HostingApplyPlan, storage domain.HostingStorage) error {
	failure := func(stage databaseBackupStage) error {
		return databaseBackupErrorAt(databaseBackupPhaseInspection, stage, errDatabaseBackupHeld)
	}
	if domain.ValidateHostingHealthyStorage(h) != nil || p.Hash != h.PlanHash || p.Storage == nil || storage.Reference() != h.Snapshot.Storage || storage.Validate() != nil {
		return failure(databaseBackupStagePlan)
	}
	r, present, err := e.host.readRecord(p.Database.NetworkName + ".json")
	if err != nil || !present || !r.InitReady || r.Binding != h.Snapshot.BindingID || r.Destination != h.Snapshot.DestinationID || r.Daemon != h.Release.DaemonID || r.Storage != storage.Reference() || r.VolumeNonce != h.Storage.Plan.CreationNonce {
		return failure(databaseBackupStageHistory)
	}
	if e.image.verifyDaemon(ctx, r.Daemon) != nil {
		return failure(databaseBackupStageDaemon)
	}
	if p.Database.Network != nil && domain.HostingDocumentHash(domain.HostingDatabaseNetworkOrigin(r)) != p.Database.Network.OriginHash {
		return failure(databaseBackupStageOrigin)
	}
	c, err := e.image.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil {
		return failure(databaseBackupStageContainerRead)
	}
	if !c.State.Running || c.State.Status != "running" || !databaseContainerMatches(c, h.OperationID, p, r, filepath.Join(e.host.secretRoot, databaseGeneration(h.OperationID, p))) {
		return failure(databaseBackupStageContainerIdentity)
	}
	req := hostingOwnedVolumeRequest{Storage: storage, DaemonID: r.Daemon, OriginOperationID: h.Storage.Plan.CreationOperationID, OriginPlanHash: h.Storage.CreationPlanHash, CreationNonce: h.Storage.Plan.CreationNonce, KnownUsers: []domain.HostingHealthy{h}}
	if evidence, e := e.image.verifyOwnedVolume(ctx, req); e != nil || evidence.CreatedAt != r.VolumeCreatedAt {
		return failure(databaseBackupStageVolume)
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 8192, "container", "top", h.ContainerID, "-eo", "pid,uid")
	if err != nil || !databaseUpgradeTopMatches(raw, c.State.Pid) {
		return failure(databaseBackupStageProcess)
	}
	current, err := e.image.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil || databaseBackupContainerHash(current) != databaseBackupContainerHash(c) {
		return failure(databaseBackupStageStability)
	}
	if e.image.verifyDaemon(ctx, r.Daemon) != nil {
		return failure(databaseBackupStageDaemonRecheck)
	}
	return nil
}
func databaseBackupArgs(mode, container string, seconds int) []string {
	args := []string{"exec", "--user", "postgres", "--interactive", container, "timeout", "--signal=TERM", "--kill-after=5s", strconv.Itoa(seconds) + "s"}
	if mode == "backup" {
		return append(args, "pg_dump", "--username=postgres", "--dbname=app", "--format=custom", "--no-owner", "--no-acl")
	}
	return append(args, "pg_restore", "--username=postgres", "--dbname=app", "--role=ncd_app", "--no-owner", "--no-acl", "--exit-on-error", "--single-transaction")
}

const databaseMySQLDumpHeader = "-- NeroCD MySQL logical dump v1\n"

func databaseBackupEngineArgs(p domain.HostingDatabaseBackupPlan, mode, container string, seconds int) []string {
	if p.Operation.Version != domain.HostingDatabaseMySQLBackupVersion {
		return databaseBackupArgs(mode, container, seconds)
	}
	args := []string{"exec", "--user", "mysql", "--interactive", container, "timeout", "--signal=TERM", "--kill-after=5s", strconv.Itoa(seconds) + "s"}
	if mode == "backup" {
		return append(args, "mysqldump", "--defaults-extra-file="+databaseSecretMount+"/admin.cnf", "--single-transaction", "--quick", "--skip-lock-tables", "--no-tablespaces", "--set-gtid-purged=OFF", "--routines", "--events", "--triggers", "--hex-blob", "--column-statistics=0", "app")
	}
	return append(args, "mysql", "--defaults-extra-file="+databaseSecretMount+"/admin.cnf", "--database=app", "--binary-mode=1", "--local-infile=0", "--skip-reconnect", "--batch")
}

func databaseMySQLQueryArgs(container, query string) []string {
	return []string{"exec", "--user", "mysql", container, "timeout", "--signal=TERM", "--kill-after=5s", "10s", "mysql", "--defaults-extra-file=" + databaseSecretMount + "/admin.cnf", "--batch", "--skip-column-names", "--execute=" + query}
}

type databaseBackupBoundedWriter struct {
	w      io.Writer
	n, max int64
}

func (w *databaseBackupBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.n {
		return 0, errors.New("logical backup byte limit exceeded")
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}
func (e *databaseBackupEngine) archive(ctx context.Context, p domain.HostingDatabaseBackupPlan) (io.ReadSeekCloser, error) {
	if p.Operation.Blob == nil || p.Operation.Blob.Validate() != nil {
		return nil, errDatabaseBackupHeld
	}
	f, b, err := e.store.Open(ctx, strings.TrimPrefix(p.Operation.Blob.Digest, "sha256:"))
	if err != nil {
		return nil, errDatabaseBackupHeld
	}
	if b.Size != p.Operation.Blob.Bytes || b.Size > p.MaxBytes || "sha256:"+b.SHA256 != p.Operation.Blob.Digest {
		_ = f.Close()
		return nil, errDatabaseBackupHeld
	}
	magic := "PGDMP"
	if domain.HostingDatabaseBackupFormatFor(p.Operation.Version) == "mysql_sql" {
		magic = databaseMySQLDumpHeader
	}
	header := make([]byte, len(magic))
	if p.Operation.Blob.Format != domain.HostingDatabaseBackupFormatFor(p.Operation.Version) {
		_ = f.Close()
		return nil, errDatabaseBackupHeld
	}
	if _, err = io.ReadFull(f, header); err != nil || string(header) != magic {
		_ = f.Close()
		return nil, errDatabaseBackupHeld
	}
	if _, err = f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, errDatabaseBackupHeld
	}
	return f, nil
}
func (e *databaseBackupEngine) preflight(parent context.Context, p domain.HostingDatabaseBackupPlan) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if p.Validate() != nil {
		return databaseBackupErrorAt(databaseBackupPhasePreflight, databaseBackupStagePlan, errDatabaseBackupHeld)
	}
	if p.Component != nil {
		return e.componentPreflight(ctx, p)
	}
	if p.Operation.Mode == "backup" {
		if err := e.inspect(ctx, p.Source, p.SourcePlan, p.SourceStorage); err != nil {
			return databaseBackupErrorPhase(err, databaseBackupPhasePreflight)
		}
	}
	if p.Operation.Mode == "restore" {
		if err := e.inspect(ctx, *p.Target, *p.TargetPlan, *p.TargetStorage); err != nil {
			return databaseBackupErrorPhase(err, databaseBackupPhasePreflight)
		}
		f, err := e.archive(ctx, p)
		if err != nil {
			return databaseBackupErrorAt(databaseBackupPhasePreflight, databaseBackupStageArchive, err)
		}
		defer func() { _ = f.Close() }()
		args := []string{"exec", "--user", "postgres", "--interactive", p.Target.ContainerID, "timeout", "--signal=TERM", "--kill-after=5s", "30s", "pg_restore", "--list"}
		var archive io.Reader = f
		if p.Operation.Version == domain.HostingDatabaseMySQLBackupVersion {
			args = databaseMySQLQueryArgs(p.Target.ContainerID, "SELECT 1")
			archive = nil
		}
		if e.stream(ctx, args, archive, io.Discard) != nil {
			return databaseBackupErrorAt(databaseBackupPhasePreflight, databaseBackupStageArchiveList, errDatabaseBackupHeld)
		}
	}
	return nil
}

// run requires a durable, controller-acknowledged intent and fresh authority.
// It never repeats an existing file/import: all recovery is receipt-only.
func (e *databaseBackupEngine) run(parent context.Context, p domain.HostingDatabaseBackupPlan, before func(context.Context) error) (domain.HostingDatabaseBackupBlob, error) {
	if p.Validate() != nil {
		return domain.HostingDatabaseBackupBlob{}, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStagePlan, errDatabaseBackupHeld)
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	var blob domain.HostingDatabaseBackupBlob
	binding := p.Source.Snapshot.BindingID
	if p.Operation.Mode == "restore" {
		binding = p.Target.Snapshot.BindingID
	}
	lock, err := e.host.lock(ctx, binding)
	if err != nil {
		return blob, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStageBindingLock, err)
	}
	defer func() { _ = lock.Close() }()
	if err := e.preflight(ctx, p); err != nil {
		return blob, databaseBackupErrorPhase(err, databaseBackupPhaseRecheck)
	}
	if databaseAuthorize(ctx, before) != nil {
		return blob, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStageAuthority, errDatabaseBackupHeld)
	}
	if p.Operation.Mode == "restore" {
		if p.Component != nil {
			return e.componentRestore(ctx, p, before)
		}
		f, err := e.archive(ctx, p)
		if err != nil {
			return blob, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStageArchive, err)
		}
		defer func() { _ = f.Close() }()
		if databaseAuthorize(ctx, before) != nil {
			return blob, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStageAuthority, errDatabaseBackupHeld)
		}
		if e.stream(ctx, databaseBackupEngineArgs(p, "restore", p.Target.ContainerID, p.TimeoutSeconds), f, io.Discard) != nil {
			return blob, databaseBackupErrorAt(databaseBackupPhaseExecution, databaseBackupStageRestore, errDatabaseBackupHeld)
		}
		// Independent readiness on the exact database and unchanged provenance.
		if err := e.inspect(ctx, *p.Target, *p.TargetPlan, *p.TargetStorage); err != nil {
			return blob, databaseBackupErrorPhase(err, databaseBackupPhasePostImport)
		}
		if databaseAuthorize(ctx, before) != nil {
			return blob, databaseBackupErrorAt(databaseBackupPhasePostImport, databaseBackupStageAuthority, errDatabaseBackupHeld)
		}
		ready := []string{"exec", "--user", "postgres", p.Target.ContainerID, "pg_isready", "--username=postgres", "--dbname=app", "--timeout=5"}
		if p.Operation.Version == domain.HostingDatabaseMySQLBackupVersion {
			ready = databaseMySQLQueryArgs(p.Target.ContainerID, "SELECT 1")
		}
		if e.stream(ctx, ready, nil, io.Discard) != nil {
			return blob, databaseBackupErrorAt(databaseBackupPhasePostImport, databaseBackupStageReadiness, errDatabaseBackupHeld)
		}
		return *p.Operation.Blob, nil
	}
	file, err := e.root.OpenFile(p.LocationID+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return blob, errDatabaseBackupHeld
	}
	// Never unlink a completed or uncertain local dump on a publication error.
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	writer := databaseBackupBoundedWriter{w: io.MultiWriter(file, hash), max: p.MaxBytes}
	if databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	if p.Operation.Version == domain.HostingDatabaseMySQLBackupVersion {
		// Single-transaction snapshots cover InnoDB tables. Refuse another storage
		// engine rather than mislabel a writable source's backup consistency.
		var result bytes.Buffer
		query := "SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_SCHEMA='app' AND TABLE_TYPE='BASE TABLE' AND ENGINE <> 'InnoDB'"
		bounded := databaseBackupBoundedWriter{w: &result, max: 128}
		if e.stream(ctx, databaseMySQLQueryArgs(p.Source.ContainerID, query), nil, &bounded) != nil || strings.TrimSpace(result.String()) != "0" {
			return blob, errDatabaseBackupHeld
		}
	}
	if domain.HostingDatabaseBackupFormatFor(p.Operation.Version) == "mysql_sql" {
		if _, err = writer.Write([]byte(databaseMySQLDumpHeader)); err != nil {
			return blob, err
		}
	}
	args := databaseBackupEngineArgs(p, "backup", p.Source.ContainerID, p.TimeoutSeconds)
	if p.Component != nil {
		component, err := domain.HostingDatabaseBackupComponentFor(p.Operation.Version, p.Source, p.SourcePlan, p.Component.Source)
		if err != nil {
			return blob, errDatabaseBackupHeld
		}
		args = databaseBackupComponentArgs("backup", component.ContainerID, p.TimeoutSeconds)
		if p.Operation.Version == domain.HostingDatabaseMySQLBackupComponentVersion {
			args = databaseBackupMySQLComponentArgs("backup", component.ContainerID, p.TimeoutSeconds)
		}
	}
	if e.stream(ctx, args, nil, &writer) != nil || file.Sync() != nil {
		return blob, errDatabaseBackupHeld
	}
	blob = domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Bytes: writer.n, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
	if blob.Validate() != nil {
		return blob, errDatabaseBackupHeld
	}
	if databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	// Validate the owned staging inode before publishing into the existing
	// no-clobber content-addressed store. Keep staging on any publication failure.
	info, err := file.Stat()
	if err != nil {
		return blob, errDatabaseBackupHeld
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != blob.Bytes {
		return blob, errDatabaseBackupHeld
	}
	if err = file.Close(); err != nil {
		return blob, errDatabaseBackupHeld
	}
	staged, err := e.root.OpenFile(p.LocationID+".partial", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return blob, errDatabaseBackupHeld
	}
	defer func() { _ = staged.Close() }()
	current, err := staged.Stat()
	if err != nil || !os.SameFile(info, current) {
		return blob, errDatabaseBackupHeld
	}
	var owned artifactfs.Ownership
	if _, owned, err = e.store.PutOwned(ctx, staged, strings.TrimPrefix(blob.Digest, "sha256:"), blob.Bytes); err != nil {
		return blob, errDatabaseBackupPublication
	}
	e.localCopy = &domain.BackupLocalCopyProof{PoolID: owned.PoolID, Device: owned.Device, Inode: owned.Inode, Bytes: owned.Bytes, Created: owned.Created}
	check := p
	check.Operation.Blob = &blob
	f, err := e.archive(ctx, check)
	if err != nil {
		return blob, err
	}
	_ = f.Close()
	return blob, nil
}
