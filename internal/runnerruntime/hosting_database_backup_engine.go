//go:build unix

package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
)

var errDatabaseBackupPublication = errors.New("completed local dump publication failed; staging retained")

var errDatabaseBackupHeld = errors.New("database backup or restore remains held; execution evidence requires reconciliation")

type databaseBackupEngine struct {
	image  *imageEngine
	host   DatabaseEngineConfig
	root   *os.Root
	store  *artifactfs.Store
	stream func(context.Context, []string, io.Reader, io.Writer) error
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
	return errors.Join(e.root.Close(), e.store.Close(), e.image.Close())
}

// inspect is existing-only. No creation, adoption, network edits or secret
// delivery is possible on this path. The controller owns the full saved proof.
func (e *databaseBackupEngine) inspect(ctx context.Context, h domain.HostingHealthy, p domain.HostingApplyPlan, storage domain.HostingStorage) error {
	if domain.ValidateHostingHealthyStorage(h) != nil || p.Hash != h.PlanHash || p.Storage == nil || storage.Reference() != h.Snapshot.Storage || storage.Validate() != nil {
		return errDatabaseBackupHeld
	}
	r, present, err := e.host.readRecord(p.Database.NetworkName + ".json")
	if err != nil || !present || !r.InitReady || r.Binding != h.Snapshot.BindingID || r.Destination != h.Snapshot.DestinationID || r.Daemon != h.Release.DaemonID || r.Storage != storage.Reference() || r.VolumeNonce != h.Storage.Plan.CreationNonce || e.image.verifyDaemon(ctx, r.Daemon) != nil {
		return errDatabaseBackupHeld
	}
	if p.Database.Network != nil && domain.HostingDocumentHash(domain.HostingDatabaseNetworkOrigin(r)) != p.Database.Network.OriginHash {
		return errDatabaseBackupHeld
	}
	c, err := e.image.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil || !c.State.Running || c.State.Status != "running" || !databaseContainerMatches(c, h.OperationID, p, r, filepath.Join(e.host.secretRoot, databaseGeneration(h.OperationID, p))) {
		return errDatabaseBackupHeld
	}
	req := hostingOwnedVolumeRequest{Storage: storage, DaemonID: r.Daemon, OriginOperationID: h.Storage.Plan.CreationOperationID, OriginPlanHash: h.Storage.CreationPlanHash, CreationNonce: h.Storage.Plan.CreationNonce, KnownUsers: []domain.HostingHealthy{h}}
	if evidence, e := e.image.verifyOwnedVolume(ctx, req); e != nil || evidence.CreatedAt != r.VolumeCreatedAt {
		return errDatabaseBackupHeld
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 8192, "container", "top", h.ContainerID, "-eo", "pid,uid")
	if err != nil || !databaseUpgradeTopMatches(raw, c.State.Pid) {
		return errDatabaseBackupHeld
	}
	current, err := e.image.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil || domain.HostingDocumentHash(current) != domain.HostingDocumentHash(c) || e.image.verifyDaemon(ctx, r.Daemon) != nil {
		return errDatabaseBackupHeld
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
	var magic [5]byte
	if _, err = io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "PGDMP" {
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
		return errDatabaseBackupHeld
	}
	if p.Operation.Mode == "backup" {
		return e.inspect(ctx, p.Source, p.SourcePlan, p.SourceStorage)
	}
	if p.Operation.Mode == "restore" {
		if e.inspect(ctx, *p.Target, *p.TargetPlan, *p.TargetStorage) != nil {
			return errDatabaseBackupHeld
		}
		f, err := e.archive(ctx, p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		args := []string{"exec", "--user", "postgres", "--interactive", p.Target.ContainerID, "timeout", "--signal=TERM", "--kill-after=5s", "30s", "pg_restore", "--list"}
		if e.stream(ctx, args, f, io.Discard) != nil {
			return errDatabaseBackupHeld
		}
	}
	return nil
}

// run requires a durable, controller-acknowledged intent and fresh authority.
// It never repeats an existing file/import: all recovery is receipt-only.
func (e *databaseBackupEngine) run(parent context.Context, p domain.HostingDatabaseBackupPlan, before func(context.Context) error) (domain.HostingDatabaseBackupBlob, error) {
	if p.Validate() != nil {
		return domain.HostingDatabaseBackupBlob{}, errDatabaseBackupHeld
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
		return blob, err
	}
	defer func() { _ = lock.Close() }()
	if e.preflight(ctx, p) != nil || databaseAuthorize(ctx, before) != nil {
		return blob, errDatabaseBackupHeld
	}
	if p.Operation.Mode == "restore" {
		f, err := e.archive(ctx, p)
		if err != nil {
			return blob, err
		}
		defer func() { _ = f.Close() }()
		if databaseAuthorize(ctx, before) != nil {
			return blob, errDatabaseBackupHeld
		}
		if e.stream(ctx, databaseBackupArgs("restore", p.Target.ContainerID, p.TimeoutSeconds), f, io.Discard) != nil {
			return blob, errDatabaseBackupHeld
		}
		// Independent readiness on the exact database and unchanged provenance.
		if e.inspect(ctx, *p.Target, *p.TargetPlan, *p.TargetStorage) != nil || databaseAuthorize(ctx, before) != nil {
			return blob, errDatabaseBackupHeld
		}
		ready := []string{"exec", "--user", "postgres", p.Target.ContainerID, "pg_isready", "--username=postgres", "--dbname=app", "--timeout=5"}
		if e.stream(ctx, ready, nil, io.Discard) != nil {
			return blob, errDatabaseBackupHeld
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
	if e.stream(ctx, databaseBackupArgs("backup", p.Source.ContainerID, p.TimeoutSeconds), nil, &writer) != nil || file.Sync() != nil {
		return blob, errDatabaseBackupHeld
	}
	blob = domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Bytes: writer.n, Format: "postgresql_custom"}
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
	if _, err = e.store.Put(ctx, staged, strings.TrimPrefix(blob.Digest, "sha256:"), blob.Bytes); err != nil {
		return blob, errDatabaseBackupPublication
	}
	check := p
	check.Operation.Blob = &blob
	f, err := e.archive(ctx, check)
	if err != nil {
		return blob, err
	}
	_ = f.Close()
	return blob, nil
}
