//go:build unix

package runnerruntime

import (
	"context"
	"crypto/subtle"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"strconv"
	"time"
)

var errExternalImportHeld = errors.New("external database import evidence requires reconciliation")

func externalImportFailure(err error) string {
	if err == nil {
		return ""
	}
	var own databaseImportError
	if errors.As(err, &own) && domain.ValidDatabaseImportFailure(string(own)) {
		return string(own)
	}
	var remote backupRemoteError
	if errors.As(err, &remote) {
		switch string(remote) {
		case "access_denied", "authority_denied", "network_denied", "remote_unavailable", "object_mismatch", "interrupted":
			return string(remote)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "interrupted"
	}
	return "execution_failed"
}

func executeDatabaseImportClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, cfg *DatabaseExecutionConfig) error {
	return executeDatabaseImportWithEngine(s, j, drain, server, token, claim, endpoint, cfg, openExternalImportEngine)
}
func executeDatabaseImportWithEngine(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, cfg *DatabaseExecutionConfig, open func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error)) error {
	if cfg == nil || !cfg.ExternalImports || domain.ValidateDatabaseImportTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errExternalImportHeld
	}
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	client := runnerclient.New(server, token)
	stage := claim.Run.RunSpec.Type == domain.RunTypeDatabaseImportStage
	var sp domain.HostingDatabaseImportStagePlan
	var ip domain.HostingDatabaseImportPlan
	var err error
	if stage {
		sp, err = client.HostingDatabaseImportStagePlan(s.Context(), a)
	} else {
		ip, err = client.HostingDatabaseImportPlan(s.Context(), a)
		sp = ip.StagePlan
	}
	if err != nil {
		return err
	}
	if sp.RunnerID != claim.Lease.RunnerID {
		return errExternalImportHeld
	}
	for _, old := range j.Snapshot().DatabaseImports {
		if old.Request.OperationID == a.OperationID {
			return errExternalImportHeld
		}
	}
	ctx, cancel := context.WithTimeout(s.Context(), time.Duration(sp.TimeoutSeconds)*time.Second)
	defer cancel()
	e, err := open(ctx, *cfg, endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	daemon, err := e.image.daemonID(ctx)
	if err != nil || daemon != sp.Target.Release.DaemonID {
		return errExternalImportHeld
	}
	before := func(ctx context.Context) error {
		if e.image.verifyDaemon(ctx, daemon) != nil {
			return errExternalImportHeld
		}
		lease, err := client.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
		if err != nil || lease.RunID != a.RunID || lease.RunnerID != sp.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || lease.Status != domain.LeaseActive || !time.Now().Before(lease.ExpiresAt) {
			return errExternalImportHeld
		}
		if stage {
			p, err := client.HostingDatabaseImportStagePlan(ctx, a)
			if err != nil || domain.HostingDocumentHash(p) != domain.HostingDocumentHash(sp) {
				return errExternalImportHeld
			}
		} else {
			p, err := client.HostingDatabaseImportPlan(ctx, a)
			if err != nil || domain.HostingDocumentHash(p) != domain.HostingDocumentHash(ip) {
				return errExternalImportHeld
			}
		}
		return nil
	}
	if before(ctx) != nil {
		return errExternalImportHeld
	}
	record := func(phase, failure string, ready *domain.HostingDatabaseImportReadyReceipt) (runner.JournalDatabaseImport, error) {
		id := attemptMutationKey("externalimport", a.LeaseID, a.Attempt, phase)
		hash := sp.Hash
		if !stage {
			hash = ip.Hash
		}
		request := domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: id, PlanHash: hash, Phase: phase, DaemonID: daemon, FailureCode: failure, Ready: ready}
		if !stage && phase != "failed" {
			request.Writers = e.writers
			request.WritersResumed = e.writersResumed
		}
		if phase == "failed" {
			request.WritersResumed = true
		}
		entry := runner.JournalDatabaseImport{ID: id, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, s), Request: request, CreatedAt: time.Now().UTC()}
		if stage {
			entry.StagePlan = &sp
		} else {
			entry.ImportPlan = &ip
		}
		return j.AppendDatabaseImport(entry)
	}
	send := func(entry runner.JournalDatabaseImport) error {
		_, err := client.HostingDatabaseImportMutation(ctx, entry.Request)
		return err
	}
	var consumed *externalImportStageHandle
	finish := func(intent *runner.JournalDatabaseImport, phase, failure string, ready *domain.HostingDatabaseImportReadyReceipt) error {
		result, err := record(phase, failure, ready)
		if err != nil {
			return err
		}
		transport, stop := context.WithTimeout(s.Context(), 5*time.Second)
		defer stop()
		if err = drain(transport); err != nil {
			return err
		}
		if _, err = client.HostingDatabaseImportMutation(transport, result.Request); err != nil {
			return err
		}
		// Uncertain SQL is never acknowledged or automatically repeated after restart.
		if phase == "uncertain" {
			return errExternalImportHeld
		}
		if consumed != nil {
			if err = consumed.discard(); err != nil {
				return err
			}
		}
		if intent != nil {
			if err = j.AckDatabaseImport(intent.ID); err != nil {
				return err
			}
		}
		return j.AckDatabaseImport(result.ID)
	}
	if stage {
		_ = cleanupExpiredExternalImportStages(ctx, e.host, func(ctx context.Context, in domain.HostingDatabaseImportStageCleanup) (bool, error) {
			out, err := client.HostingDatabaseImportStageCleanup(ctx, in)
			return out.Delete, err
		})
		file, err := createExternalImportStage(ctx, e.host, sp)
		if err != nil {
			return finish(nil, "stage_failed", externalImportFailure(err), nil)
		}
		defer func() { _ = file.Close() }()
		var remote *backupRemoteS3
		if sp.Stage.Version != domain.HostingDatabaseImportUploadVersion {
			remote, err = externalImportRemote(ctx, client, a, sp, *cfg, before)
			if err != nil {
				_ = file.discard()
				return finish(nil, "stage_failed", externalImportFailure(err), nil)
			}
			defer remote.Close()
			remote.transport.before = before
		}
		intent, err := record("stage_intent", "", nil)
		if err != nil {
			return err
		}
		if err = send(intent); err != nil {
			return err
		}
		var ready domain.HostingDatabaseImportReadyReceipt
		var result databaseImportStageBytes
		var identity *domain.HostingDatabaseImportSourceIdentity
		if sp.Stage.Version == domain.HostingDatabaseImportUploadVersion {
			uploadCtx, stop := context.WithDeadline(ctx, *sp.Stage.UploadDeadline)
			body, e := client.HostingDatabaseImportUploadStream(uploadCtx, domain.HostingDatabaseImportUploadStream{HostingAuthority: a, PlanHash: sp.Hash})
			err = e
			if err == nil {
				result, err = inspectExternalSQL(uploadCtx, body, file.file, sp.MaxBytes)
				closeErr := body.Close()
				if err == nil {
					err = closeErr
				}
				if err == nil && result.SourceBytes != sp.Stage.UploadSource.Bytes {
					err = databaseImportError("object_mismatch")
				}
			}
			stop()
		} else {
			result, identity, err = remote.stageExternal(ctx, sp.S3Target.Target.Bucket, sp.Stage.S3Source.ObjectKey, file.file)
		}
		if err == nil {
			if before(ctx) != nil {
				err = databaseImportError("authority_denied")
			} else {
				ready, err = file.publish(sp, result, identity)
			}
		}
		if err != nil {
			_ = file.discard()
			return finish(&intent, "stage_failed", externalImportFailure(err), nil)
		}
		return finish(&intent, "ready", "", &ready)
	}
	file, err := openExternalImportStage(ctx, e.host, ip)
	if err != nil {
		return finish(nil, "failed", externalImportFailure(err), nil)
	}
	defer func() { _ = file.Close() }()
	consumed = file
	if err = e.externalImportPreflight(ctx, ip); err != nil {
		return finish(nil, "failed", externalImportFailure(err), nil)
	}
	// This marker prevents expiry cleanup from unlinking a possibly consumed SQL stage.
	if externalImportWriteDocument(file.root, file.id+".import", ip) != nil {
		return finish(nil, "failed", "stage_unavailable", nil)
	}
	intent, err := record("import_intent", "", nil)
	if err != nil {
		return err
	}
	if err = send(intent); err != nil {
		return err
	}
	_, err = e.externalImportSQL(ctx, ip, file.file, before)
	phase, failure := "complete", ""
	if err != nil {
		phase, failure = "uncertain", externalImportFailure(err)
	}
	if err = finish(&intent, phase, failure, nil); err != nil {
		return err
	}
	return nil
}

func externalImportRemote(ctx context.Context, client runnerclient.Client, a domain.HostingAuthority, p domain.HostingDatabaseImportStagePlan, cfg DatabaseExecutionConfig, before func(context.Context) error) (*backupRemoteS3, error) {
	policy, err := databaseBackupRemotePolicy(cfg)
	if err != nil {
		return nil, err
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: attemptMutationKey("externalcredentials", a.LeaseID, a.Attempt, "delivery"), PlanHash: p.Hash, TargetID: p.Stage.S3Source.TargetID, TargetRevision: p.Stage.S3Source.Revision, DaemonID: p.Target.Release.DaemonID}
	options := runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: cfg.TrustedLoopbackHTTP}
	credentials, err := client.HostingBackupRemoteCredentials(ctx, p.RunnerID, request, options)
	if err != nil {
		return nil, databaseImportError("access_denied")
	}
	defer credentials.Close()
	if credentials.Metadata().Purpose != domain.HostingDatabaseImportCredentialPurpose {
		return nil, databaseImportError("authority_denied")
	}
	sequence := 0
	grant := backupRemoteGrant{useUntil: credentials.Metadata().UseUntil}
	grant.refresh = func(ctx context.Context, access, secret []byte) (time.Time, error) {
		if before(ctx) != nil {
			return time.Time{}, errExternalImportHeld
		}
		sequence++
		next := request
		next.DeliveryID = attemptMutationKey("externalcredentials", a.LeaseID, a.Attempt, "renew_"+strconv.Itoa(sequence))
		fresh, err := client.HostingBackupRemoteCredentials(ctx, p.RunnerID, next, options)
		if err != nil {
			return time.Time{}, err
		}
		defer fresh.Close()
		if fresh.Metadata().Purpose != domain.HostingDatabaseImportCredentialPurpose {
			return time.Time{}, errExternalImportHeld
		}
		err = fresh.Use(func(a, b []byte) error {
			if subtle.ConstantTimeCompare(a, access) != 1 || subtle.ConstantTimeCompare(b, secret) != 1 {
				return errExternalImportHeld
			}
			return nil
		})
		return fresh.Metadata().UseUntil, err
	}
	var remote *backupRemoteS3
	err = credentials.Use(func(access, secret []byte) error {
		var e error
		remote, e = newBackupRemoteS3(ctx, p.S3Target.Target, access, secret, policy, time.Duration(p.TimeoutSeconds)*time.Second, grant)
		return e
	})
	return remote, err
}

func reconcileDatabaseImportJournal(ctx context.Context, c Config, j *runner.AttemptJournal) error {
	return reconcileDatabaseImportJournalWithCleanup(ctx, c, j, func(ctx context.Context, p domain.HostingDatabaseImportPlan) error {
		host, err := NewDatabaseEngineConfig(c.Database.StateRoot, c.Database.SecretRoot)
		if err != nil {
			return errExternalImportHeld
		}
		return removeConsumedExternalImportStage(ctx, host, p)
	}, func(ctx context.Context, p domain.HostingDatabaseImportStagePlan) error {
		host, err := NewDatabaseEngineConfig(c.Database.StateRoot, c.Database.SecretRoot)
		if err != nil {
			return errExternalImportHeld
		}
		return discardInterruptedLocalUpload(ctx, host, p)
	})
}
func reconcileDatabaseImportJournalWithCleanup(ctx context.Context, c Config, j *runner.AttemptJournal, cleanup func(context.Context, domain.HostingDatabaseImportPlan) error, discardUpload func(context.Context, domain.HostingDatabaseImportStagePlan) error) error {
	entries := j.Snapshot().DatabaseImports
	if len(entries) == 0 {
		return nil
	}
	if c.Database == nil || !c.Database.ExternalImports {
		return errExternalImportHeld
	}
	ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: c.Database.TrustedLoopbackHTTP})
	client := runnerclient.New(c.Server, c.Token)
	for _, entry := range entries {
		if entry.Request.Phase == "stage_intent" || entry.Request.Phase == "import_intent" {
			if entry.StagePlan != nil && entry.StagePlan.Stage.Version == domain.HostingDatabaseImportUploadVersion {
				terminal := false
				for _, saved := range entries {
					if saved.Request.Phase != "stage_intent" && saved.Request.HostingAuthority == entry.Request.HostingAuthority && saved.Request.PlanHash == entry.Request.PlanHash {
						terminal = true
						break
					}
				}
				if !terminal {
					if discardUpload == nil || discardUpload(ctx, *entry.StagePlan) != nil {
						return errExternalImportHeld
					}
				}
			}
			continue
		}
		// Persisted terminal evidence can be resent, never execution or a remote GET.
		if _, err := client.HostingDatabaseImportMutation(ctx, entry.Request); err != nil {
			return err
		}
		if entry.Request.Phase == "uncertain" {
			return errExternalImportHeld
		}
		if entry.ImportPlan != nil && (entry.Request.Phase == "complete" || entry.Request.Phase == "failed") {
			if err := cleanup(ctx, *entry.ImportPlan); err != nil {
				return err
			}
		}
		for _, intent := range entries {
			if (intent.Request.Phase == "stage_intent" || intent.Request.Phase == "import_intent") && intent.Request.HostingAuthority == entry.Request.HostingAuthority && intent.Request.PlanHash == entry.Request.PlanHash {
				if err := j.AckDatabaseImport(intent.ID); err != nil {
					return err
				}
			}
		}
		if err := j.AckDatabaseImport(entry.ID); err != nil {
			return err
		}
	}
	if len(j.Snapshot().DatabaseImports) != 0 {
		return errExternalImportHeld
	}
	return nil
}
