//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func executeDatabaseBackupClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *DatabaseExecutionConfig) error {
	return executeDatabaseBackupWithEngine(s, j, drain, server, token, claim, endpoint, config, openDatabaseBackupEngine)
}

func executeDatabaseBackupWithEngine(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *DatabaseExecutionConfig, open func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error)) error {
	if config == nil || !config.LogicalBackups || domain.ValidateDatabaseBackupTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errDatabaseBackupHeld
	}
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	p, err := c.HostingDatabaseBackupPlan(s.Context(), a)
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID || p.Operation.Mode != claim.Run.RunSpec.HostingMode {
		return errDatabaseBackupHeld
	}
	for _, old := range j.Snapshot().DatabaseBackup {
		if old.Request.OperationID == a.OperationID {
			return errDatabaseBackupHeld
		}
	}
	e, err := open(s.Context(), *config, endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	ctx, cancel := context.WithTimeout(s.Context(), time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	before := func(ctx context.Context) error {
		lease, err := c.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
		if err != nil || lease.RunID != a.RunID || lease.RunnerID != p.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || lease.Status != domain.LeaseActive || !time.Now().Before(lease.ExpiresAt) {
			return errDatabaseBackupHeld
		}
		current, err := c.HostingDatabaseBackupPlan(ctx, a)
		if err != nil || current.Hash != p.Hash || domain.HostingDocumentHash(current) != domain.HostingDocumentHash(p) {
			return errDatabaseBackupHeld
		}
		return nil
	}
	record := func(phase string, blob *domain.HostingDatabaseBackupBlob, failure string) (runner.JournalDatabaseBackup, error) {
		id := attemptMutationKey("dbbackup", a.LeaseID, a.Attempt, phase)
		request := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: id, PlanHash: p.Hash, Phase: phase, Blob: blob, FailureCode: failure}
		entry := runner.JournalDatabaseBackup{ID: id, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, s), Plan: p, Request: request, CreatedAt: time.Now().UTC()}
		return j.AppendDatabaseBackup(entry)
	}
	if err = e.preflight(ctx, p); err != nil {
		entry, e := record("failed", nil, "execution_failed")
		if e != nil {
			return e
		}
		transport, stop := context.WithTimeout(s.Context(), 5*time.Second)
		defer stop()
		if drain(transport) != nil {
			return errDatabaseBackupHeld
		}
		if response, sendErr := c.HostingDatabaseBackupMutation(transport, entry.Request); sendErr != nil {
			return sendErr
		} else if !databaseBackupReceiptMatches(p, entry.Request, response) {
			return errDatabaseBackupHeld
		}
		return j.AckDatabaseBackup(entry.ID)
	}
	intent, err := record("intent", nil, "")
	if err != nil {
		return err
	}
	if response, sendErr := c.HostingDatabaseBackupMutation(ctx, intent.Request); sendErr != nil {
		return sendErr
	} else if !databaseBackupReceiptMatches(p, intent.Request, response) {
		return errDatabaseBackupHeld
	}
	blob, executionErr := e.run(ctx, p, before)
	phase, failure := "complete", ""
	if executionErr != nil {
		phase, failure = "uncertain", "execution_failed"
		if errors.Is(executionErr, errDatabaseBackupPublication) {
			failure = "publication_failed"
		}
	}
	var proof *domain.HostingDatabaseBackupBlob
	if phase == "complete" {
		proof = &blob
	}
	entry, err := record(phase, proof, failure)
	if err != nil {
		return err
	}
	// A tool deadline is distinct from the lease deadline. Its durable failure
	// can still be reported under the remaining live attempt; lease loss keeps
	// the receipt local and never grants further execution authority.
	transport, stop := context.WithTimeout(s.Context(), 5*time.Second)
	defer stop()
	if err = drain(transport); err != nil {
		return err
	}
	if response, sendErr := c.HostingDatabaseBackupMutation(transport, entry.Request); sendErr != nil {
		return sendErr
	} else if !databaseBackupReceiptMatches(p, entry.Request, response) {
		return errDatabaseBackupHeld
	}
	if phase != "complete" {
		var stage *databaseBackupStageError
		if errors.As(executionErr, &stage) {
			out := *stage
			out.cause = errors.Join(errDatabaseBackupHeld, stage.cause)
			return &out
		}
		return errDatabaseBackupHeld
	}
	if err = j.AckDatabaseBackup(intent.ID); err != nil {
		return err
	}
	return j.AckDatabaseBackup(entry.ID)
}

// Read-only replay sends exact historical receipt bodies. Missing result evidence
// never invokes pg_restore, pg_dump, container creation or another task attempt.
func reconcileDatabaseBackupJournal(ctx context.Context, c Config, j *runner.AttemptJournal) error {
	entries := j.Snapshot().DatabaseBackup
	if len(entries) == 0 {
		return nil
	}
	if c.Database == nil || !c.Database.LogicalBackups {
		return errDatabaseBackupHeld
	}
	ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: c.Database.TrustedLoopbackHTTP})
	client := runnerclient.New(c.Server, c.Token)
	for _, entry := range entries {
		if entry.Request.Phase == "intent" {
			continue
		}
		if entry.Request.Phase == "uncertain" {
			return errDatabaseBackupHeld
		}
		if response, err := client.HostingDatabaseBackupMutation(ctx, entry.Request); err != nil {
			return err
		} else if !databaseBackupReceiptMatches(entry.Plan, entry.Request, response) {
			return errDatabaseBackupHeld
		}
		for _, intent := range entries {
			if intent.Request.Phase == "intent" && intent.Request.HostingAuthority == entry.Request.HostingAuthority && intent.Plan.Hash == entry.Plan.Hash {
				if err := j.AckDatabaseBackup(intent.ID); err != nil {
					return err
				}
			}
		}
		if err := j.AckDatabaseBackup(entry.ID); err != nil {
			return err
		}
	}
	if len(j.Snapshot().DatabaseBackup) > 0 {
		return errors.New("database backup execution intent has no durable result; ownership remains held")
	}
	return nil
}

func databaseBackupReceiptMatches(p domain.HostingDatabaseBackupPlan, in domain.HostingDatabaseBackupMutation, out domain.HostingDatabaseBackupOperation) bool {
	original := p.Operation
	held := in.Phase == "intent" || in.Phase == "uncertain" || in.Phase == "failed" && original.Mode == "restore"
	return out.ID == original.ID && out.Version == original.Version && out.ProjectID == original.ProjectID && out.SourceResourceID == original.SourceResourceID && out.SourceGeneration == original.SourceGeneration && out.BackupID == original.BackupID && out.Mode == original.Mode && out.TargetResourceID == original.TargetResourceID && out.MaxBytes == p.MaxBytes && out.TimeoutSeconds == p.TimeoutSeconds && out.OwnershipHeld == held && out.Consistency == "logical_snapshot_source_writable"
}
