//go:build unix

package runnerruntime

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"nerocd/internal/storagearchive"
)

func storageRemoteReceiptMatches(p domain.HostingStorageBackupTransferPlan, in domain.HostingStorageBackupTransferMutation, out domain.HostingStorageBackupTransfer) bool {
	prior := p.Operation
	return out.Validate() == nil && out.ID == prior.ID && out.ProjectID == prior.ProjectID && out.TargetID == prior.TargetID && out.TargetRevision == prior.TargetRevision && out.DestinationID == prior.DestinationID && out.Mode == prior.Mode && out.BackupID == prior.BackupID && out.RestoreOperationID == prior.RestoreOperationID && domain.HostingDocumentHash(out.Blob) == domain.HostingDocumentHash(prior.Blob) && out.FailureCode == in.FailureCode
}

func executeStorageBackupTransferClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *StorageArchiveExecutionConfig) error {
	return executeStorageBackupTransferWithEngine(s, j, drain, server, token, claim, endpoint, config, openStorageRemoteEngine)
}
func executeStorageBackupTransferWithEngine(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *StorageArchiveExecutionConfig, open func(context.Context, StorageArchiveExecutionConfig, string) (*storageArchiveEngine, error)) error {
	if config == nil || !config.RemoteBackups || domain.ValidateStorageBackupTransferTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errBackupRemoteHeld
	}
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	p, err := c.HostingStorageBackupTransferPlan(s.Context(), a)
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID || p.Operation.Mode != claim.Run.RunSpec.HostingMode {
		return errBackupRemoteHeld
	}
	for _, old := range j.Snapshot().StorageBackupTransfers {
		if old.Request.OperationID == a.OperationID {
			return errBackupRemoteHeld
		}
	}
	ctx, cancel := context.WithTimeout(s.Context(), time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	e, err := open(ctx, *config, endpoint)
	if err != nil {
		return err
	}
	// Resource teardown must not reclassify a durably recorded operation.
	defer func() { _ = e.Close() }()
	daemon, err := e.image.daemonID(ctx)
	if err != nil || p.DaemonID != "" && p.DaemonID != daemon {
		return errBackupRemoteHeld
	}
	before := func(ctx context.Context) error {
		if e.image.verifyDaemon(ctx, daemon) != nil {
			return errBackupRemoteHeld
		}
		lease, err := c.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
		if err != nil || lease.RunID != a.RunID || lease.RunnerID != p.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || lease.Status != domain.LeaseActive || !time.Now().Before(lease.ExpiresAt) {
			return errBackupRemoteHeld
		}
		current, err := c.HostingStorageBackupTransferPlan(ctx, a)
		if err != nil || domain.HostingDocumentHash(current) != domain.HostingDocumentHash(p) {
			return errBackupRemoteHeld
		}
		return nil
	}
	var remote *backupRemoteS3
	var localCopy *domain.BackupLocalCopyProof
	var remoteCopy *domain.BackupRemoteCopyProof
	record := func(phase, failure string) (runner.JournalStorageBackupTransfer, error) {
		id := attemptMutationKey("storageremote", a.LeaseID, a.Attempt, phase)
		request := domain.HostingStorageBackupTransferMutation{LocalCopy: localCopy, RemoteCopy: remoteCopy, HostingAuthority: a, ReceiptID: id, PlanHash: p.Hash, Phase: phase, FailureCode: failure, DaemonID: daemon}
		return j.AppendStorageBackupTransfer(runner.JournalStorageBackupTransfer{ID: id, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, s), Plan: p, Request: request, CreatedAt: time.Now().UTC()})
	}
	policy, err := storageBackupRemotePolicy(*config)
	if err != nil {
		return err
	}
	if err = before(ctx); err != nil {
		return err
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: attemptMutationKey("storageremotecredentials", a.LeaseID, a.Attempt, "delivery"), PlanHash: p.Hash, TargetID: p.Operation.TargetID, TargetRevision: p.Operation.TargetRevision, DaemonID: daemon}
	credentials, err := c.HostingBackupRemoteCredentials(ctx, p.RunnerID, request, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP})
	if err != nil {
		return err
	}
	defer credentials.Close()
	deliverySequence := 0
	grant := backupRemoteGrant{useUntil: credentials.Metadata().UseUntil}
	grant.refresh = func(refreshCtx context.Context, access, secret []byte) (time.Time, error) {
		if err := before(refreshCtx); err != nil {
			return time.Time{}, err
		}
		deliverySequence++
		freshRequest := request
		freshRequest.DeliveryID = attemptMutationKey("storageremotecredentials", a.LeaseID, a.Attempt, "renew_"+strconv.Itoa(deliverySequence))
		fresh, err := c.HostingBackupRemoteCredentials(refreshCtx, p.RunnerID, freshRequest, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP})
		if err != nil {
			return time.Time{}, err
		}
		defer fresh.Close()
		if err = fresh.Use(func(nextAccess, nextSecret []byte) error {
			if subtle.ConstantTimeCompare(access, nextAccess) != 1 || subtle.ConstantTimeCompare(secret, nextSecret) != 1 {
				return errBackupRemoteHeld
			}
			return nil
		}); err != nil {
			return time.Time{}, err
		}
		return fresh.Metadata().UseUntil, nil
	}
	err = credentials.Use(func(access, secret []byte) error {
		var e error
		remote, e = newBackupRemoteS3(ctx, p.Target.Target, access, secret, policy, time.Duration(p.TimeoutSeconds)*time.Second, grant)
		return e
	})
	if err != nil {
		var safe backupRemoteError
		if !errors.As(err, &safe) || !domain.ValidBackupRemoteFailure(string(safe)) {
			return errBackupRemoteHeld
		}
		entry, e := record("failed", string(safe))
		if e != nil {
			return e
		}
		out, e := c.HostingStorageBackupTransferMutation(ctx, entry.Request)
		if e != nil {
			return e
		}
		if !storageRemoteReceiptMatches(p, entry.Request, out) {
			return errBackupRemoteHeld
		}
		return j.AckStorageBackupTransfer(entry.ID)
	}
	defer remote.Close()
	remote.transport.before = before
	intent, err := record("intent", "")
	if err != nil {
		return err
	}
	if out, err := c.HostingStorageBackupTransferMutation(ctx, intent.Request); err != nil {
		return err
	} else if !storageRemoteReceiptMatches(p, intent.Request, out) {
		return errBackupRemoteHeld
	}
	err = executeStorageRemoteObjectOwned(ctx, e, remote, p, before, &localCopy, &remoteCopy)
	phase, failure := "complete", ""
	if err != nil {
		phase = "failed"
		var safe backupRemoteError
		if errors.As(err, &safe) {
			failure = string(safe)
		} else {
			failure = "archive_unavailable"
		}
		if failure == "interrupted" || failure == "remote_unavailable" || failure == "authority_denied" {
			phase = "uncertain"
		}
	}
	entry, err := record(phase, failure)
	if err != nil {
		return err
	}
	transport, stop := context.WithTimeout(s.Context(), 5*time.Second)
	defer stop()
	if err = drain(transport); err != nil {
		return err
	}
	if out, err := c.HostingStorageBackupTransferMutation(transport, entry.Request); err != nil {
		return err
	} else if !storageRemoteReceiptMatches(p, entry.Request, out) {
		return errBackupRemoteHeld
	}
	if err = j.AckStorageBackupTransfer(intent.ID); err != nil {
		return err
	}
	return j.AckStorageBackupTransfer(entry.ID)
}

func executeStorageRemoteObject(ctx context.Context, e *storageArchiveEngine, remote *backupRemoteS3, p domain.HostingStorageBackupTransferPlan, before func(context.Context) error) error {
	return executeStorageRemoteObjectOwned(ctx, e, remote, p, before, nil, nil)
}
func executeStorageRemoteObjectOwned(ctx context.Context, e *storageArchiveEngine, remote *backupRemoteS3, p domain.HostingStorageBackupTransferPlan, before func(context.Context) error, local **domain.BackupLocalCopyProof, remoteProof **domain.BackupRemoteCopyProof) (err error) {
	if before(ctx) != nil {
		return backupRemoteError("authority_denied")
	}
	if p.Operation.Mode == "validate" {
		return remote.validate(ctx, p.Target.Target.Bucket)
	}
	blob := *p.Operation.Blob
	key := strings.TrimPrefix(blob.SHA256, "sha256:")
	limits := storagearchive.Limits{ArchiveBytes: p.Limits.ArchiveBytes, DataBytes: p.Limits.DataBytes, FileBytes: p.Limits.FileBytes, Members: p.Limits.Members, Depth: p.Limits.Depth, PathBytes: p.Limits.PathBytes}
	if p.Operation.Mode == "upload" {
		file, stored, err := e.store.Open(ctx, key)
		if err != nil {
			return backupRemoteError("archive_unavailable")
		}
		// Reads or explicit Sync/publication establish the result before descriptor teardown.
		defer func() { _ = file.Close() }()
		if stored.Size != blob.SizeBytes {
			return backupRemoteError("archive_unavailable")
		}
		native, ok := file.(*os.File)
		if !ok {
			return backupRemoteError("archive_unavailable")
		}
		summary, err := storagearchive.Validate(ctx, native, limits)
		if err != nil || summary.Digest != blob.SHA256 || summary.ArchiveBytes != blob.SizeBytes {
			return backupRemoteError("archive_unavailable")
		}
		proof, err := remote.storageUploadOwned(ctx, p.Target.Target.Bucket, p.ObjectKey, blob, file)
		if err == nil && remoteProof != nil {
			*remoteProof = proof
		}
		return err
	}
	name, err := storageRemotePartialName(p.Operation.ID)
	if err != nil {
		return err
	}
	file, err := e.temporary.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	defer func() {
		if cleanupErr := removeStorageRemotePartial(e.temporary, name, file); cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err = remote.storageDownload(ctx, p.Target.Target.Bucket, p.ObjectKey, blob, file); err != nil {
		return err
	}
	if file.Sync() != nil || before(ctx) != nil {
		return backupRemoteError("authority_denied")
	}
	info, err := file.Stat()
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != blob.SizeBytes {
		return backupRemoteError("archive_unavailable")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	summary, err := storagearchive.Validate(ctx, file, limits)
	if err != nil || summary.Digest != blob.SHA256 || summary.ArchiveBytes != blob.SizeBytes {
		return backupRemoteError("object_mismatch")
	}
	if before(ctx) != nil {
		return backupRemoteError("authority_denied")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	if owned, ok := e.store.(interface {
		PutOwned(context.Context, io.Reader, string, int64) (artifactfs.Blob, artifactfs.Ownership, error)
	}); ok {
		_, proof, putErr := owned.PutOwned(ctx, file, key, blob.SizeBytes)
		if putErr != nil {
			return backupRemoteError("archive_unavailable")
		}
		if local != nil {
			*local = &domain.BackupLocalCopyProof{PoolID: proof.PoolID, Device: proof.Device, Inode: proof.Inode, Bytes: proof.Bytes, Created: proof.Created}
		}
	} else if _, err = e.store.Put(ctx, file, key, blob.SizeBytes); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	return nil
}

// Partial names come only from the immutable transfer identity. No caller path,
// directory scan or remote object key participates in cleanup.
func storageRemotePartialName(id string) (string, error) {
	if len(id) == 0 || len(id) > 128 {
		return "", backupRemoteError("archive_unavailable")
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return "", backupRemoteError("archive_unavailable")
		}
	}
	return id + ".partial", nil
}
func removeStorageRemotePartial(root *os.Root, name string, file *os.File) error {
	// Compare the opened inode with the rooted directory entry before unlinking.
	// Refuse symlinks, foreign ownership, hard links and replaced entries.
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			err = errBackupRemoteHeld
		}
	}
	if err == nil {
		var entry os.FileInfo
		entry, err = root.Lstat(name)
		if err == nil && (!os.SameFile(info, entry) || !entry.Mode().IsRegular()) {
			err = errBackupRemoteHeld
		}
	}
	if err == nil {
		err = root.Remove(name)
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return backupRemoteError("archive_unavailable")
	}
	return nil
}

// Replay may reclaim only the exact private staging entry named by its durable
// download plan. It does not open Docker or request execution credentials.
func cleanupStorageRemoteReplay(c *StorageArchiveExecutionConfig, p domain.HostingStorageBackupTransferPlan) (err error) {
	if p.Operation.Mode != "download" {
		return nil
	}
	name, err := storageRemotePartialName(p.Operation.ID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(c.TemporaryRoot)
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return backupRemoteError("archive_unavailable")
	}
	root, err := os.OpenRoot(c.TemporaryRoot)
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = backupRemoteError("archive_unavailable")
		}
	}()
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return backupRemoteError("archive_unavailable")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	return removeStorageRemotePartial(root, name, file)
}

func openStorageRemoteEngine(ctx context.Context, c StorageArchiveExecutionConfig, endpoint string) (*storageArchiveEngine, error) {
	e, err := openStorageArchiveEngine(ctx, c, endpoint)
	if err != nil {
		return nil, err
	}
	return e.(*storageArchiveEngine), nil
}

func reconcileStorageBackupTransferJournal(ctx context.Context, c Config, j *runner.AttemptJournal) error {
	entries := j.Snapshot().StorageBackupTransfers
	if len(entries) == 0 {
		return nil
	}
	if c.StorageArchive == nil || !c.StorageArchive.RemoteBackups {
		return errBackupRemoteHeld
	}
	ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: c.StorageArchive.TrustedLoopbackHTTP})
	client := runnerclient.New(c.Server, c.Token)
	for _, entry := range entries {
		if entry.Request.Phase == "intent" {
			paired := false
			for _, result := range entries {
				if result.Request.Phase != "intent" && result.Request.HostingAuthority == entry.Request.HostingAuthority && result.Plan.Hash == entry.Plan.Hash {
					paired = true
				}
			}
			if !paired {
				a := entry.Request.HostingAuthority
				lease, err := client.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
				if err != nil || lease.RunID != a.RunID || lease.RunnerID != entry.Plan.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || time.Now().Before(lease.ExpiresAt) {
					return errBackupRemoteHeld
				}
				request := entry.Request
				request.Phase = "uncertain"
				request.FailureCode = "interrupted"
				request.ReceiptID = attemptMutationKey("storageremote", a.LeaseID, a.Attempt, "uncertain")
				result := entry
				result.ID = request.ReceiptID
				result.Request = request
				result.CreatedAt = time.Now().UTC()
				if _, err = j.AppendStorageBackupTransfer(result); err != nil {
					return err
				}
				out, err := client.HostingStorageBackupTransferMutation(ctx, request)
				if err != nil {
					return err
				}
				if !storageRemoteReceiptMatches(entry.Plan, request, out) {
					return errBackupRemoteHeld
				}
				if err = cleanupStorageRemoteReplay(c.StorageArchive, entry.Plan); err != nil {
					return err
				}
				if err = j.AckStorageBackupTransfer(entry.ID); err != nil {
					return err
				}
				if err = j.AckStorageBackupTransfer(result.ID); err != nil {
					return err
				}
			}
			continue
		}
		out, err := client.HostingStorageBackupTransferMutation(ctx, entry.Request)
		if err != nil {
			return err
		}
		if !storageRemoteReceiptMatches(entry.Plan, entry.Request, out) {
			return errBackupRemoteHeld
		}
		if err = cleanupStorageRemoteReplay(c.StorageArchive, entry.Plan); err != nil {
			return err
		}
		for _, intent := range entries {
			if intent.Request.Phase == "intent" && intent.Request.HostingAuthority == entry.Request.HostingAuthority && intent.Plan.Hash == entry.Plan.Hash {
				if err = j.AckStorageBackupTransfer(intent.ID); err != nil {
					return err
				}
			}
		}
		if err = j.AckStorageBackupTransfer(entry.ID); err != nil {
			return err
		}
	}
	if len(j.Snapshot().StorageBackupTransfers) > 0 {
		return errBackupRemoteHeld
	}
	return nil
}
