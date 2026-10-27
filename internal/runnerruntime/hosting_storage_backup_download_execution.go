//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errStorageDownload = errors.New("storage archive download unavailable")

func executeStorageBackupDownloadClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, config *StorageArchiveExecutionConfig) error {
	if config == nil || !config.BackupDownloads || domain.ValidateStorageBackupTransferTask(claim.Run) != nil || claim.Run.RunSpec.HostingMode != domain.HostingStorageBackupDownloadMode || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errStorageDownload
	}
	// No download-specific durable record contains bytes, paths, or stream intent.
	// Startup replays only ordinary terminal metadata, never this function.
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	short, stop, err := s.RequestContext()
	if err != nil {
		return err
	}
	p, err := c.HostingStorageBackupDownloadPlan(short, a)
	stop()
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID {
		return errStorageDownload
	}
	err = streamStorageBackupDownload(s.Context(), c, a, p, config.ArtifactRoot)
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

func streamStorageBackupDownload(parent context.Context, c runnerclient.Client, a domain.HostingAuthority, p domain.HostingStorageBackupDownloadPlan, root string) error {
	deadline := time.Now().Add(300 * time.Second)
	if p.ExpiresAt.Before(deadline) {
		deadline = p.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	recheck := func() error {
		freshCtx, freshCancel := context.WithTimeout(ctx, 5*time.Second)
		defer freshCancel()
		fresh, err := c.HostingStorageBackupDownloadPlan(freshCtx, a)
		if err != nil || domain.HostingDocumentHash(fresh) != domain.HostingDocumentHash(p) {
			return errStorageDownload
		}
		return nil
	}
	unavailable := func() error {
		if recheck() != nil {
			return errStorageDownload
		}
		_ = c.StreamStorageBackupDownload(ctx, domain.HostingStorageBackupDownloadStream{HostingAuthority: a, PlanHash: p.Hash, FailureCode: "archive_unavailable"}, p, nil)
		return errStorageDownload
	}
	store, err := artifactfs.OpenStore(root, domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		return unavailable()
	}
	defer func() { _ = store.Close() }()
	proof := artifactfs.Ownership{PoolID: p.LocalCopy.PoolID, Device: p.LocalCopy.Device, Inode: p.LocalCopy.Inode, Bytes: p.LocalCopy.Bytes, Created: p.LocalCopy.Created}
	file, blob, err := store.OpenOwned(ctx, strings.TrimPrefix(p.Blob.SHA256, "sha256:"), proof)
	if err != nil {
		return unavailable()
	}
	defer func() { _ = file.Close() }()
	if blob.Size != p.Blob.SizeBytes || "sha256:"+blob.SHA256 != p.Blob.SHA256 {
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
		return errStorageDownload
	}
	err = c.StreamStorageBackupDownload(ctx, domain.HostingStorageBackupDownloadStream{HostingAuthority: a, PlanHash: p.Hash}, p, file)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
