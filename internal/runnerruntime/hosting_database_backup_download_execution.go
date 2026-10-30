//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errDatabaseDownload = errors.New("database backup download unavailable")

func executeDatabaseBackupDownloadClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, config *DatabaseExecutionConfig) error {
	if config == nil || !config.BackupDownloads || domain.ValidateDatabaseBackupTransferTask(claim.Run) != nil || claim.Run.RunSpec.HostingMode != domain.HostingDatabaseBackupDownloadMode || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errDatabaseDownload
	}
	// No download-specific durable record contains bytes, paths, or stream intent.
	// Startup replays only ordinary terminal metadata, never this function.
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	short, stop, err := s.RequestContext()
	if err != nil {
		return err
	}
	p, err := c.HostingDatabaseBackupDownloadPlan(short, a)
	stop()
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID {
		return errDatabaseDownload
	}
	err = streamDatabaseBackupDownload(s.Context(), c, a, p, filepath.Join(config.StateRoot, "logical-backups"))
	if s.Context().Err() != nil {
		return s.Context().Err()
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
	}
	if _, e := ensureJournaledCompletion(s, j, claim.Run.ID, claim.Lease, status); e != nil {
		return e
	}
	completionCtx, done, e := s.RequestContext()
	if e != nil {
		return e
	}
	defer done()
	if e = drain(completionCtx); e != nil {
		return e
	}
	// Completion retries contain metadata only, even after a stream disconnect.
	if e = journaledCompletion(s, j, server, token, claim.Run.ID, claim.Lease, status, nil); e != nil {
		return e
	}
	return nil
}

func streamDatabaseBackupDownload(parent context.Context, c runnerclient.Client, a domain.HostingAuthority, p domain.HostingDatabaseBackupDownloadPlan, root string) error {
	deadline := time.Now().Add(300 * time.Second)
	if p.ExpiresAt.Before(deadline) {
		deadline = p.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	recheck := func() error {
		freshCtx, freshCancel := context.WithTimeout(ctx, 5*time.Second)
		defer freshCancel()
		fresh, err := c.HostingDatabaseBackupDownloadPlan(freshCtx, a)
		if err != nil || domain.HostingDocumentHash(fresh) != domain.HostingDocumentHash(p) {
			return errDatabaseDownload
		}
		return nil
	}
	unavailable := func() error {
		if recheck() != nil {
			return errDatabaseDownload
		}
		_ = c.StreamDatabaseBackupDownload(ctx, domain.HostingDatabaseBackupDownloadStream{HostingAuthority: a, PlanHash: p.Hash, FailureCode: "archive_unavailable"}, p, nil)
		return errDatabaseDownload
	}
	store, err := artifactfs.OpenStore(root, domain.HostingDatabaseBackupMaxBytes)
	if err != nil {
		return unavailable()
	}
	defer func() { _ = store.Close() }()
	proof := artifactfs.Ownership{PoolID: p.LocalCopy.PoolID, Device: p.LocalCopy.Device, Inode: p.LocalCopy.Inode, Bytes: p.LocalCopy.Bytes, Created: p.LocalCopy.Created}
	file, blob, err := store.OpenOwned(ctx, strings.TrimPrefix(p.Blob.Digest, "sha256:"), proof)
	if err != nil {
		return unavailable()
	}
	defer func() { _ = file.Close() }()
	if blob.Size != p.Blob.Bytes || "sha256:"+blob.SHA256 != p.Blob.Digest {
		return unavailable()
	}
	// Cancellation owns descriptor closure. Join the callback before returning so
	// no attempt-owned cleanup survives supervisor teardown.
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { _ = file.Close(); close(closed) })
	defer func() {
		if !stopClose() {
			<-closed
		}
	}()
	if recheck() != nil {
		return errDatabaseDownload
	}
	err = c.StreamDatabaseBackupDownload(ctx, domain.HostingDatabaseBackupDownloadStream{HostingAuthority: a, PlanHash: p.Hash}, p, file)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
