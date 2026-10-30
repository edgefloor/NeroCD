//go:build unix

package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/domain"
)

const externalImportStageLimit = 4

type externalImportStageOwner struct {
	Plan domain.HostingDatabaseImportStagePlan `json:"plan"`
	File domain.HostingDatabaseImportStageFile `json:"file"`
}

type externalImportStageHandle struct {
	root *os.Root
	lock *os.File
	file *os.File
	id   string
}

func (h *externalImportStageHandle) Close() error {
	return errors.Join(h.file.Close(), h.root.Close(), h.lock.Close())
}

func openExternalImportRoot(config DatabaseEngineConfig) (*os.Root, error) {
	path := filepath.Join(config.stateRoot, "external-import-stages")
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, databaseImportError("stage_unavailable")
	}
	if databaseFilePolicy(path, 0o700, os.Geteuid(), os.Getegid(), true) != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	return root, nil
}

func externalImportFileProof(file *os.File) (domain.HostingDatabaseImportStageFile, error) {
	info, err := file.Stat()
	if err != nil {
		return domain.HostingDatabaseImportStageFile{}, databaseImportError("stage_unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() || info.Size() < 0 || info.Size() > domain.HostingDatabaseImportMaxBytes+1 {
		return domain.HostingDatabaseImportStageFile{}, databaseImportError("stage_unavailable")
	}
	return domain.HostingDatabaseImportStageFile{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Bytes: info.Size()}, nil
}

func externalImportWriteDocument(root *os.Root, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 1<<20 {
		return databaseImportError("stage_unavailable")
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	proof, proofErr := externalImportFileProof(file)
	if proofErr != nil {
		_ = file.Close()
		return proofErr
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		current, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return
		}
		observed, e := externalImportFileProof(current)
		_ = current.Close()
		if e == nil && observed.Device == proof.Device && observed.Inode == proof.Inode {
			_ = root.Remove(name)
		}
	}()
	_, writeErr := file.Write(raw)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	defer func() { _ = directory.Close() }()
	if directory.Sync() != nil {
		return databaseImportError("stage_unavailable")
	}
	committed = true
	return nil
}

func externalImportReadDocument(root *os.Root, name string, out any) error {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	defer func() { _ = file.Close() }()
	if _, err = externalImportFileProof(file); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || domain.DecodeHostingDatabaseBackupJSON(raw, out) != nil {
		return databaseImportError("stage_unavailable")
	}
	return nil
}

func createExternalImportStage(ctx context.Context, config DatabaseEngineConfig, p domain.HostingDatabaseImportStagePlan) (*externalImportStageHandle, error) {
	if p.Validate() != nil {
		return nil, databaseImportError("authority_denied")
	}
	pool, err := config.lock(ctx, "external_import_pool")
	if err != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	defer func() { _ = pool.Close() }()
	root, err := openExternalImportRoot(config)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*externalImportStageHandle, error) { _ = root.Close(); return nil, err }
	dir, err := root.Open(".")
	if err != nil {
		return fail(databaseImportError("stage_unavailable"))
	}
	entries, readErr := dir.ReadDir(externalImportStageLimit*4 + 1)
	_ = dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) >= externalImportStageLimit*4 {
		return fail(databaseImportError("stage_unavailable"))
	}
	owners := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".owner") {
			owners++
		}
	}
	if owners >= externalImportStageLimit {
		return fail(databaseImportError("stage_unavailable"))
	}
	lock, err := config.lock(ctx, "external_import_"+p.Stage.ID)
	if err != nil {
		return fail(databaseImportError("stage_unavailable"))
	}
	file, err := root.OpenFile(p.Stage.ID+".sql", os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = lock.Close()
		return fail(databaseImportError("stage_unavailable"))
	}
	h := &externalImportStageHandle{root: root, lock: lock, file: file, id: p.Stage.ID}
	proof, err := externalImportFileProof(file)
	if err == nil {
		err = externalImportWriteDocument(root, p.Stage.ID+".owner", externalImportStageOwner{Plan: p, File: proof})
	}
	if err != nil {
		// This exclusive, still-open inode was created here, never adopted.
		_ = root.Remove(p.Stage.ID + ".sql")
		_ = h.Close()
		return nil, err
	}
	return h, nil
}

func (h *externalImportStageHandle) publish(p domain.HostingDatabaseImportStagePlan, result databaseImportStageBytes, identity *domain.HostingDatabaseImportSourceIdentity) (domain.HostingDatabaseImportReadyReceipt, error) {
	var zero domain.HostingDatabaseImportReadyReceipt
	if h.id != p.Stage.ID || h.file.Sync() != nil {
		return zero, databaseImportError("stage_unavailable")
	}
	proof, err := externalImportFileProof(h.file)
	if err != nil || proof.Bytes != result.Blob.Bytes {
		return zero, databaseImportError("stage_unavailable")
	}
	now := time.Now().UTC()
	receipt := domain.HostingDatabaseImportReadyReceipt{StageID: h.id, PlanHash: p.Hash, SourceIdentity: identity, SourceBytes: result.SourceBytes, Compression: result.Compression, Blob: result.Blob, File: proof, ReadyAt: now, ExpiresAt: now.Add(domain.HostingDatabaseImportStageTTL)}
	receipt.Hash = domain.HostingDocumentHash(receipt)
	if receipt.ValidateFor(p) != nil || externalImportWriteDocument(h.root, h.id+".ready", receipt) != nil {
		return zero, databaseImportError("stage_unavailable")
	}
	return receipt, nil
}

func openExternalImportStage(ctx context.Context, config DatabaseEngineConfig, p domain.HostingDatabaseImportPlan) (*externalImportStageHandle, error) {
	if p.Validate() != nil {
		return nil, databaseImportError("authority_denied")
	}
	lock, err := config.lock(ctx, "external_import_"+p.Operation.StageID)
	if err != nil {
		return nil, databaseImportError("stage_unavailable")
	}
	root, err := openExternalImportRoot(config)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	var owner externalImportStageOwner
	var receipt domain.HostingDatabaseImportReadyReceipt
	id := p.Operation.StageID
	if externalImportReadDocument(root, id+".owner", &owner) != nil || owner.Plan.Hash != p.StagePlan.Hash || owner.Plan.Validate() != nil || externalImportReadDocument(root, id+".ready", &receipt) != nil || domain.HostingDocumentHash(receipt) != domain.HostingDocumentHash(p.Receipt) || receipt.ValidateFor(owner.Plan) != nil {
		_ = root.Close()
		_ = lock.Close()
		return nil, databaseImportError("stage_unavailable")
	}
	file, err := root.OpenFile(id+".sql", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = root.Close()
		_ = lock.Close()
		return nil, databaseImportError("stage_unavailable")
	}
	h := &externalImportStageHandle{root: root, lock: lock, file: file, id: id}
	proof, err := externalImportFileProof(file)
	if err != nil || proof != receipt.File || proof.Device != owner.File.Device || proof.Inode != owner.File.Inode {
		_ = h.Close()
		return nil, databaseImportError("stage_unavailable")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &databaseImportEncodedReader{ctx: ctx, input: io.LimitReader(file, receipt.Blob.Bytes+1)})
	if err != nil || ctx.Err() != nil || n != receipt.Blob.Bytes || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != receipt.Blob.Digest {
		_ = h.Close()
		return nil, databaseImportError("object_mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = h.Close()
		return nil, databaseImportError("stage_unavailable")
	}
	// The caller streams this retained descriptor, not a second pathname lookup.
	return h, nil
}

// discard removes only this stage's privately created inode under its stage lock.
// No logical-backup CAS path or other stage is ever a cleanup candidate.
func (h *externalImportStageHandle) discard() error {
	retained, err := externalImportFileProof(h.file)
	if err != nil {
		return err
	}
	current, err := h.root.OpenFile(h.id+".sql", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	proof, err := externalImportFileProof(current)
	_ = current.Close()
	if err != nil || proof.Device != retained.Device || proof.Inode != retained.Inode {
		return databaseImportError("stage_unavailable")
	}
	var owner externalImportStageOwner
	if externalImportReadDocument(h.root, h.id+".owner", &owner) != nil || owner.Plan.Stage.ID != h.id || owner.Plan.Validate() != nil || owner.File.Device != proof.Device || owner.File.Inode != proof.Inode {
		return databaseImportError("stage_unavailable")
	}
	for _, suffix := range []string{".ready", ".import"} {
		file, openErr := h.root.OpenFile(h.id+suffix, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return databaseImportError("stage_unavailable")
		}
		_, err = externalImportFileProof(file)
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	// The stage namespace is exclusively owned; all participating workers obey
	// the same stage lock, which remains held through these removals.
	for _, suffix := range []string{".sql", ".ready", ".import", ".owner"} {
		if err = h.root.Remove(h.id + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return databaseImportError("stage_unavailable")
		}
	}
	return nil
}

// cleanupExpiredExternalImportStages examines at most the fixed private-stage
// budget. An import intent or busy lock preserves its input for reconciliation.
func cleanupExpiredExternalImportStages(ctx context.Context, config DatabaseEngineConfig, authorize func(context.Context, domain.HostingDatabaseImportStageCleanup) (bool, error)) error {
	if authorize == nil {
		return databaseImportError("authority_denied")
	}
	pool, err := config.lock(ctx, "external_import_pool")
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	defer func() { _ = pool.Close() }()
	root, err := openExternalImportRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	entries, readErr := directory.ReadDir(externalImportStageLimit*4 + 1)
	_ = directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) > externalImportStageLimit*4 {
		return databaseImportError("stage_unavailable")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".owner") {
			continue
		}
		var owner externalImportStageOwner
		if externalImportReadDocument(root, entry.Name(), &owner) != nil || owner.Plan.Validate() != nil || entry.Name() != owner.Plan.Stage.ID+".owner" {
			return databaseImportError("stage_unavailable")
		}
		id := owner.Plan.Stage.ID
		// A short attempt avoids blocking unrelated cleanup on an active stage.
		lockCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		lock, lockErr := config.lock(lockCtx, "external_import_"+id)
		cancel()
		if lockErr != nil {
			if ctx.Err() != nil {
				return databaseImportError("interrupted")
			}
			continue
		}
		func() {
			defer func() { _ = lock.Close() }()
			if _, err = root.Lstat(id + ".import"); err == nil || !errors.Is(err, os.ErrNotExist) {
				return
			}
			request := domain.HostingDatabaseImportStageCleanup{StageID: id, PlanHash: owner.Plan.Hash}
			expires := owner.Plan.Stage.CreatedAt.Add(time.Duration(owner.Plan.TimeoutSeconds)*time.Second + domain.HostingDatabaseImportStageTTL)
			if _, err = root.Lstat(id + ".ready"); err == nil {
				var receipt domain.HostingDatabaseImportReadyReceipt
				if externalImportReadDocument(root, id+".ready", &receipt) != nil || receipt.ValidateFor(owner.Plan) != nil {
					err = databaseImportError("stage_unavailable")
					return
				}
				expires = receipt.ExpiresAt
				request.ReadyHash = receipt.Hash
			} else if !errors.Is(err, os.ErrNotExist) {
				return
			}
			err = nil
			if time.Now().Before(expires) {
				return
			}
			allowed, authErr := authorize(ctx, request)
			if authErr != nil {
				err = databaseImportError("authority_denied")
				return
			}
			if !allowed {
				return
			}
			file, openErr := root.OpenFile(id+".sql", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if openErr != nil {
				err = databaseImportError("stage_unavailable")
				return
			}
			defer func() { _ = file.Close() }()
			proof, proofErr := externalImportFileProof(file)
			if proofErr != nil || proof.Device != owner.File.Device || proof.Inode != owner.File.Inode {
				err = databaseImportError("stage_unavailable")
				return
			}
			h := &externalImportStageHandle{root: root, lock: lock, file: file, id: id}
			err = h.discard()
		}()
		if err != nil {
			return databaseImportError("stage_unavailable")
		}
	}
	return nil
}

// removeConsumedExternalImportStage runs only after the exact terminal controller
// receipt was accepted. It never fetches or executes SQL during reconciliation.
func removeConsumedExternalImportStage(ctx context.Context, config DatabaseEngineConfig, p domain.HostingDatabaseImportPlan) error {
	if p.Validate() != nil {
		return databaseImportError("authority_denied")
	}
	root, err := openExternalImportRoot(config)
	if err != nil {
		return err
	}
	absent := true
	for _, suffix := range []string{".sql", ".owner", ".ready", ".import"} {
		_, e := root.Lstat(p.Operation.StageID + suffix)
		if !errors.Is(e, os.ErrNotExist) {
			absent = false
		}
	}
	_ = root.Close()
	if absent {
		return nil
	}
	h, err := openExternalImportStage(ctx, config, p)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	return h.discard()
}

// discardInterruptedLocalUpload removes only an unpublished owned upload inode.
// The durable intent remains held; this does not acknowledge or repeat a body.
func discardInterruptedLocalUpload(ctx context.Context, config DatabaseEngineConfig, p domain.HostingDatabaseImportStagePlan) error {
	if p.Stage.Version != domain.HostingDatabaseImportUploadVersion || p.Validate() != nil {
		return databaseImportError("stage_unavailable")
	}
	lock, err := config.lock(ctx, "external_import_"+p.Stage.ID)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	root, err := openExternalImportRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, suffix := range []string{".ready", ".import"} {
		if _, e := root.Lstat(p.Stage.ID + suffix); e == nil {
			return nil
		} else if !errors.Is(e, os.ErrNotExist) {
			return databaseImportError("stage_unavailable")
		}
	}
	var owner externalImportStageOwner
	err = externalImportReadDocument(root, p.Stage.ID+".owner", &owner)
	if err != nil {
		if _, e := root.Lstat(p.Stage.ID + ".owner"); errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return databaseImportError("stage_unavailable")
	}
	if owner.Plan.Validate() != nil || owner.Plan.Hash != p.Hash {
		return databaseImportError("stage_unavailable")
	}
	file, err := root.OpenFile(p.Stage.ID+".sql", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return databaseImportError("stage_unavailable")
	}
	defer func() { _ = file.Close() }()
	proof, err := externalImportFileProof(file)
	if err != nil || proof.Device != owner.File.Device || proof.Inode != owner.File.Inode {
		return databaseImportError("stage_unavailable")
	}
	handle := externalImportStageHandle{root: root, lock: lock, file: file, id: p.Stage.ID}
	return handle.discard()
}
