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

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errBackupRemoteHeld = errors.New("remote backup evidence requires reconciliation")

func backupRemoteReceiptMatches(p domain.HostingDatabaseBackupTransferPlan, in domain.HostingDatabaseBackupTransferMutation, out domain.HostingDatabaseBackupTransfer) bool {
	prior := p.Operation
	return out.Validate() == nil && out.ID == prior.ID && out.ProjectID == prior.ProjectID && out.TargetID == prior.TargetID && out.TargetRevision == prior.TargetRevision && out.DestinationID == prior.DestinationID && out.Mode == prior.Mode && out.BackupID == prior.BackupID && out.RestoreOperationID == prior.RestoreOperationID && domain.HostingDocumentHash(out.Blob) == domain.HostingDocumentHash(prior.Blob) && out.FailureCode == in.FailureCode
}

func executeDatabaseBackupTransferClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *DatabaseExecutionConfig) error {
	return executeDatabaseBackupTransferWithEngine(s, j, drain, server, token, claim, endpoint, config, openDatabaseBackupEngine)
}
func executeDatabaseBackupTransferWithEngine(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *DatabaseExecutionConfig, open func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error)) error {
	if config == nil || !config.RemoteBackups || !config.LogicalBackups || domain.ValidateDatabaseBackupTransferTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errBackupRemoteHeld
	}
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	p, err := c.HostingDatabaseBackupTransferPlan(s.Context(), a)
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID || p.Operation.Mode != claim.Run.RunSpec.HostingMode {
		return errBackupRemoteHeld
	}
	for _, old := range j.Snapshot().DatabaseBackupTransfers {
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
		current, err := c.HostingDatabaseBackupTransferPlan(ctx, a)
		if err != nil || domain.HostingDocumentHash(current) != domain.HostingDocumentHash(p) {
			return errBackupRemoteHeld
		}
		return nil
	}
	var remote *backupRemoteS3
	record := func(phase, failure string) (runner.JournalDatabaseBackupTransfer, error) {
		id := attemptMutationKey("dbremote", a.LeaseID, a.Attempt, phase)
		request := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: id, PlanHash: p.Hash, Phase: phase, FailureCode: failure, DaemonID: daemon}
		if phase == "complete" && p.Operation.Mode == "upload" && remote != nil {
			request.RemoteCopy = remote.ownership
		}
		if phase == "complete" && p.Operation.Mode == "download" {
			request.LocalCopy = e.localCopy
		}
		return j.AppendDatabaseBackupTransfer(runner.JournalDatabaseBackupTransfer{ID: id, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, s), Plan: p, Request: request, CreatedAt: time.Now().UTC()})
	}
	policy, err := databaseBackupRemotePolicy(*config)
	if err != nil {
		return err
	}
	if err = before(ctx); err != nil {
		return err
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: attemptMutationKey("dbremotecredentials", a.LeaseID, a.Attempt, "delivery"), PlanHash: p.Hash, TargetID: p.Operation.TargetID, TargetRevision: p.Operation.TargetRevision, DaemonID: daemon}
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
		freshRequest.DeliveryID = attemptMutationKey("dbremotecredentials", a.LeaseID, a.Attempt, "renew_"+strconv.Itoa(deliverySequence))
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
		out, e := c.HostingDatabaseBackupTransferMutation(ctx, entry.Request)
		if e != nil {
			return e
		}
		if !backupRemoteReceiptMatches(p, entry.Request, out) {
			return errBackupRemoteHeld
		}
		return j.AckDatabaseBackupTransfer(entry.ID)
	}
	defer remote.Close()
	remote.transport.before = before
	intent, err := record("intent", "")
	if err != nil {
		return err
	}
	if out, err := c.HostingDatabaseBackupTransferMutation(ctx, intent.Request); err != nil {
		return err
	} else if !backupRemoteReceiptMatches(p, intent.Request, out) {
		return errBackupRemoteHeld
	}
	err = executeBackupRemoteObject(ctx, e, remote, p, before)
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
	if out, err := c.HostingDatabaseBackupTransferMutation(transport, entry.Request); err != nil {
		return err
	} else if !backupRemoteReceiptMatches(p, entry.Request, out) {
		return errBackupRemoteHeld
	}
	if err = j.AckDatabaseBackupTransfer(intent.ID); err != nil {
		return err
	}
	return j.AckDatabaseBackupTransfer(entry.ID)
}

func executeBackupRemoteObject(ctx context.Context, e *databaseBackupEngine, remote *backupRemoteS3, p domain.HostingDatabaseBackupTransferPlan, before func(context.Context) error) error {
	if before(ctx) != nil {
		return backupRemoteError("authority_denied")
	}
	if p.Operation.Mode == "validate" {
		return remote.validate(ctx, p.Target.Target.Bucket)
	}
	blob := *p.Operation.Blob
	key := strings.TrimPrefix(blob.Digest, "sha256:")
	if p.Operation.Mode == "upload" {
		file, stored, err := e.store.Open(ctx, key)
		if err != nil {
			return backupRemoteError("archive_unavailable")
		}
		// Reads or explicit Sync/publication establish the result before descriptor teardown.
		defer func() { _ = file.Close() }()
		if stored.Size != blob.Bytes {
			return backupRemoteError("archive_unavailable")
		}
		var magic [5]byte
		if _, err = io.ReadFull(file, magic[:]); err != nil || string(magic[:]) != "PGDMP" {
			return backupRemoteError("archive_unavailable")
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return backupRemoteError("archive_unavailable")
		}
		return remote.upload(ctx, p.Target.Target.Bucket, p.ObjectKey, blob, file)
	}
	// Fresh, exact-operation staging stays private and is retained on all errors.
	file, err := e.root.OpenFile(p.Operation.ID+".partial", os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	// Reads or explicit Sync/publication establish the result before descriptor teardown.
	defer func() { _ = file.Close() }()
	if err = remote.download(ctx, p.Target.Target.Bucket, p.ObjectKey, blob, file); err != nil {
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
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != blob.Bytes {
		return backupRemoteError("archive_unavailable")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	_, proof, err := e.store.PutOwned(ctx, file, key, blob.Bytes)
	if err != nil {
		return backupRemoteError("archive_unavailable")
	}
	e.localCopy = &domain.BackupLocalCopyProof{PoolID: proof.PoolID, Device: proof.Device, Inode: proof.Inode, Bytes: proof.Bytes, Created: proof.Created}
	return nil
}

func reconcileDatabaseBackupTransferJournal(ctx context.Context, c Config, j *runner.AttemptJournal) error {
	entries := j.Snapshot().DatabaseBackupTransfers
	if len(entries) == 0 {
		return nil
	}
	if c.Database == nil || !c.Database.RemoteBackups {
		return errBackupRemoteHeld
	}
	ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: c.Database.TrustedLoopbackHTTP})
	client := runnerclient.New(c.Server, c.Token)
	for _, entry := range entries {
		if entry.Request.Phase == "intent" {
			paired := false
			for _, result := range entries {
				if result.Request.Phase != "intent" && result.Request.HostingAuthority == entry.Request.HostingAuthority && result.Plan.Hash == entry.Plan.Hash {
					paired = true
				}
			}
			if !paired && entry.Plan.Operation.Mode == "upload" {
				a := entry.Request.HostingAuthority
				lease, err := client.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
				if err != nil || lease.RunID != a.RunID || lease.RunnerID != entry.Plan.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || time.Now().Before(lease.ExpiresAt) {
					return errBackupRemoteHeld
				}
				request := entry.Request
				request.Phase = "uncertain"
				request.FailureCode = "interrupted"
				request.ReceiptID = attemptMutationKey("dbremote", a.LeaseID, a.Attempt, "uncertain")
				result := entry
				result.ID = request.ReceiptID
				result.Request = request
				result.CreatedAt = time.Now().UTC()
				if _, err = j.AppendDatabaseBackupTransfer(result); err != nil {
					return err
				}
				out, err := client.HostingDatabaseBackupTransferMutation(ctx, request)
				if err != nil {
					return err
				}
				if !backupRemoteReceiptMatches(entry.Plan, request, out) {
					return errBackupRemoteHeld
				}
				if err = j.AckDatabaseBackupTransfer(entry.ID); err != nil {
					return err
				}
				if err = j.AckDatabaseBackupTransfer(result.ID); err != nil {
					return err
				}
			}
			continue
		}
		out, err := client.HostingDatabaseBackupTransferMutation(ctx, entry.Request)
		if err != nil {
			return err
		}
		if !backupRemoteReceiptMatches(entry.Plan, entry.Request, out) {
			return errBackupRemoteHeld
		}
		for _, intent := range entries {
			if intent.Request.Phase == "intent" && intent.Request.HostingAuthority == entry.Request.HostingAuthority && intent.Plan.Hash == entry.Plan.Hash {
				if err = j.AckDatabaseBackupTransfer(intent.ID); err != nil {
					return err
				}
			}
		}
		if err = j.AckDatabaseBackupTransfer(entry.ID); err != nil {
			return err
		}
	}
	if len(j.Snapshot().DatabaseBackupTransfers) > 0 {
		return errBackupRemoteHeld
	}
	return nil
}
