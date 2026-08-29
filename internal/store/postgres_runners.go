package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	rows, err := s.queries.ListRunners(ctx)
	if err != nil {
		return nil, fmt.Errorf("list runners query: %w", err)
	}
	runners := make([]domain.Runner, 0, len(rows))
	for _, row := range rows {
		runners = append(runners, runnerFromSQLC(row))
	}
	return runners, nil
}

func (s *PostgresStore) GetRunnerByID(ctx context.Context, id string) (domain.Runner, error) {
	row, err := s.queries.GetRunnerByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("get runner by id query: %w", err)
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) RegisterRunner(ctx context.Context, runner domain.Runner, opts ...MutationOption) (domain.Runner, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		upserted, err := s.queries.RegisterRunner(ctx, sqlcgen.RegisterRunnerParams{ID: runner.ID, Name: runner.Name, Tags: runner.Tags, Capabilities: runner.Capabilities, Status: runner.Status, RegisteredAt: runner.RegisteredAt, LastHeartbeatAt: runner.LastHeartbeatAt, TokenHash: runner.TokenHash})
		if isUniqueViolation(err) {
			return domain.Runner{}, ErrConflict
		}
		if err != nil {
			return domain.Runner{}, fmt.Errorf("register runner query: %w", err)
		}
		return runnerFromSQLC(upserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RegisterRunner(ctx, sqlcgen.RegisterRunnerParams{ID: runner.ID, Name: runner.Name, Tags: runner.Tags, Capabilities: runner.Capabilities, Status: runner.Status, RegisteredAt: runner.RegisteredAt, LastHeartbeatAt: runner.LastHeartbeatAt, TokenHash: runner.TokenHash})
	if err != nil {
		return domain.Runner{}, fmt.Errorf("register runner query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Runner{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Runner{}, fmt.Errorf("commit transaction: %w", err)
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) CreateRunnerEnrollment(ctx context.Context, enrollment domain.RunnerEnrollment, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	ttl := enrollment.ExpiresAt.Sub(enrollment.CreatedAt)
	created, err := queries.CreateRunnerEnrollment(ctx, sqlcgen.CreateRunnerEnrollmentParams{ID: enrollment.ID, TokenHash: enrollment.TokenHash, RunnerID: enrollment.RunnerID, RunnerName: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, CreatedBy: enrollment.CreatedBy, TtlMicroseconds: ttl.Microseconds()})
	if isUniqueViolation(err) {
		return domain.RunnerEnrollment{}, ErrConflict
	}
	if err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("create runner enrollment query: %w", err)
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("create audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("commit transaction: %w", err)
	}
	return runnerEnrollmentFromSQLC(created), nil
}

func (s *PostgresStore) RevokeRunnerEnrollment(ctx context.Context, enrollmentID string, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	revoked, err := queries.RevokeUnusedRunnerEnrollment(ctx, enrollmentID)
	if err == pgx.ErrNoRows {
		return domain.RunnerEnrollment{}, ErrNotFound
	}
	if err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("revoke unused runner enrollment query: %w", err)
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("create audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, fmt.Errorf("commit transaction: %w", err)
	}
	return runnerEnrollmentFromSQLC(revoked), nil
}

func (s *PostgresStore) ConsumeRunnerEnrollment(ctx context.Context, consume domain.RunnerEnrollmentConsume, audit domain.AuditEvent) (domain.Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	enrollment, err := queries.LockRunnerEnrollmentByTokenHash(ctx, consume.TokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("lock runner enrollment by token hash query: %w", err)
	}
	if enrollment.UsedAt != nil {
		if enrollment.ConsumeRequestID == nil || enrollment.CredentialHash == nil || *enrollment.ConsumeRequestID != consume.RequestID || *enrollment.CredentialHash != consume.CredentialHash {
			return domain.Runner{}, ErrConflict
		}
		runnerRow, getErr := queries.GetRunnerByID(ctx, enrollment.RunnerID)
		if getErr != nil || runnerRow.TokenHash != consume.CredentialHash {
			return domain.Runner{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Runner{}, fmt.Errorf("commit transaction: %w", err)
		}
		return runnerFromSQLC(runnerRow), nil
	}
	requestID, credentialHash := consume.RequestID, consume.CredentialHash
	if _, err := queries.MarkRunnerEnrollmentUsed(ctx, sqlcgen.MarkRunnerEnrollmentUsedParams{ID: enrollment.ID, ConsumeRequestID: &requestID, CredentialHash: &credentialHash}); err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	} else if err != nil {
		return domain.Runner{}, fmt.Errorf("mark runner enrollment used query: %w", err)
	}
	runnerRow, err := queries.CreateRunnerForEnrollment(ctx, sqlcgen.CreateRunnerForEnrollmentParams{ID: enrollment.RunnerID, Name: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, TokenHash: consume.CredentialHash})
	if isUniqueViolation(err) {
		return domain.Runner{}, ErrConflict
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("create runner for enrollment query: %w", err)
	}
	audit.ActorID = enrollment.RunnerID
	audit.TargetID = enrollment.RunnerID
	audit.Metadata = map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.Runner{}, fmt.Errorf("create audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Runner{}, fmt.Errorf("commit transaction: %w", err)
	}
	return runnerFromSQLC(runnerRow), nil
}

func (s *PostgresStore) UpdateRunnerToken(ctx context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time, opts ...MutationOption) (domain.Runner, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		updated, err := s.queries.UpdateRunnerToken(ctx, sqlcgen.UpdateRunnerTokenParams{ID: runnerID, TokenHash: tokenHash, Status: status, LastHeartbeatAt: updatedAt})
		if err == pgx.ErrNoRows {
			return domain.Runner{}, ErrNotFound
		}
		if err != nil {
			return domain.Runner{}, fmt.Errorf("update runner token query: %w", err)
		}
		return runnerFromSQLC(updated), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.UpdateRunnerToken(ctx, sqlcgen.UpdateRunnerTokenParams{ID: runnerID, TokenHash: tokenHash, Status: status, LastHeartbeatAt: updatedAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("update runner token query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Runner{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Runner{}, fmt.Errorf("commit transaction: %w", err)
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) GetRunnerByTokenHash(ctx context.Context, tokenHash string) (domain.Runner, error) {
	runner, err := s.queries.GetRunnerByTokenHash(ctx, tokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("get runner by token hash query: %w", err)
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) HeartbeatRunner(ctx context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	runner, err := s.queries.HeartbeatRunner(ctx, sqlcgen.HeartbeatRunnerParams{ID: id, LastHeartbeatAt: heartbeatAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, fmt.Errorf("heartbeat runner query: %w", err)
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration, opts ...MutationOption) (domain.ClaimedRun, error) {
	audit := resolveMutationOptions(opts)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	markedStale, err := queries.MarkStaleRunnerForClaim(ctx, sqlcgen.MarkStaleRunnerForClaimParams{RunnerID: runnerID, StaleBefore: now.Add(-2 * ttl)})
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("mark stale runner for claim query: %w", err)
	}
	if markedStale > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("commit transaction: %w", err)
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	runnerRow, err := queries.GetActiveRunnerForClaim(ctx, sqlcgen.GetActiveRunnerForClaimParams{ID: runnerID, LastHeartbeatAt: now.Add(-2 * ttl)})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("get active runner for claim query: %w", err)
	}
	runner := runnerFromSQLC(runnerRow)
	// One durable cursor row both serializes claims by the same runner and keeps
	// bounded scans moving across persistently incompatible queued work.
	if err := queries.EnsureRunnerClaimCursor(ctx, runner.ID); err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("ensure runner claim cursor query: %w", err)
	}
	storedCursor, err := queries.LockRunnerClaimCursor(ctx, runner.ID)
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("lock runner claim cursor query: %w", err)
	}
	// Claim locks runner cursor -> task run. Completion, periodic expiry, and
	// cancellation do not need a cursor and lock task run -> per-run log advisory
	// lock -> lease. Fenced log/artifact paths omit task run and begin at
	// advisory/lease. No path acquires a runner cursor after a task-run lock.
	if err := expireLeasesAtDBClock(ctx, queries); err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("expire leases at db clock: %w", err)
	}
	var claimedRun *domain.TaskRun
	type claimCandidate struct {
		id           string
		claimOrderAt time.Time
	}
	var cursorOrder *time.Time
	cursorID := ""
	if storedCursor.ClaimOrderAt != nil && storedCursor.RunID != nil {
		cursorOrder = storedCursor.ClaimOrderAt
		cursorID = *storedCursor.RunID
	}
	reachedQueueEnd := false
	for batch := 0; batch < claimCandidateMaxBatches && claimedRun == nil; batch++ {
		candidates := make([]claimCandidate, 0, claimCandidateBatchSize)
		if cursorOrder == nil {
			rows, listErr := queries.ListQueuedClaimCandidatesFromHead(ctx, claimCandidateBatchSize)
			err = listErr
			for _, row := range rows {
				candidates = append(candidates, claimCandidate{id: row.ID, claimOrderAt: row.ClaimOrderAt})
			}
		} else {
			rows, listErr := queries.ListQueuedClaimCandidatesAfterCursor(ctx, sqlcgen.ListQueuedClaimCandidatesAfterCursorParams{ClaimOrderAt: *cursorOrder, RunID: cursorID, CandidateLimit: claimCandidateBatchSize})
			err = listErr
			for _, row := range rows {
				candidates = append(candidates, claimCandidate{id: row.ID, claimOrderAt: row.ClaimOrderAt})
			}
		}
		if err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("list queued claim candidates query: %w", err)
		}
		for _, candidate := range candidates {
			cursorOrder = &candidate.claimOrderAt
			cursorID = candidate.id
			row, err := queries.GetRun(ctx, candidate.id)
			if err != nil {
				return domain.ClaimedRun{}, fmt.Errorf("get run query: %w", err)
			}
			run, err := taskRunFromSQLC(row)
			if err != nil {
				return domain.ClaimedRun{}, err
			}
			if covers(runner.Tags, run.RunnerTags) && contains(runner.Capabilities, claimRunType(run)) {
				claimedRun = &run
				break
			}
		}
		if len(candidates) < claimCandidateBatchSize {
			reachedQueueEnd = true
			break
		}
	}
	if claimedRun == nil {
		if reachedQueueEnd {
			cursorOrder = nil
			cursorID = ""
		}
		if err := queries.StoreRunnerClaimCursor(ctx, sqlcgen.StoreRunnerClaimCursorParams{ClaimOrderAt: cursorOrder, RunID: cursorID, RunnerID: runner.ID}); err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("store runner claim cursor query: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("commit transaction: %w", err)
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err := queries.StoreRunnerClaimCursor(ctx, sqlcgen.StoreRunnerClaimCursorParams{ClaimOrderAt: cursorOrder, RunID: cursorID, RunnerID: runner.ID}); err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("store runner claim cursor query: %w", err)
	}
	attempt, err := queries.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: claimedRun.ID, RunnerID: &runner.ID})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	} else if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("claim queued run query: %w", err)
	}
	claimedRun.Status = domain.RunRunning
	claimedRun.RunnerID = &runner.ID
	leaseID, err := newLeaseToken("lease")
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("new lease token: %w", err)
	}
	fence, err := newLeaseToken("fence")
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("new fence token: %w", err)
	}
	leaseRow, err := queries.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: leaseID, RunID: claimedRun.ID, RunnerID: runner.ID, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
	if err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("create active run lease query: %w", err)
	}
	lease := runLeaseFromSQLC(leaseRow)
	// A typed deployment run gains its attempt only in the same transaction that
	// creates its lease. Generic runs intentionally have no deployment row.
	if deployment, lookupErr := queries.LockDeploymentForRun(ctx, claimedRun.ID); lookupErr == nil {
		if _, err = queries.CreateDeploymentAttemptForLease(ctx, sqlcgen.CreateDeploymentAttemptForLeaseParams{TaskRunID: claimedRun.ID, LeaseID: lease.ID, RunnerID: runner.ID, Attempt: int32(lease.Attempt), Fence: lease.Fence}); err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("create deployment attempt for lease query: %w", err)
		}
		if deployment.Status == domain.DeploymentQueued {
			if _, err = queries.AssignDeploymentForLease(ctx, claimedRun.ID); err != nil {
				return domain.ClaimedRun{}, fmt.Errorf("assign deployment for lease query: %w", err)
			}
		}
	} else if lookupErr != pgx.ErrNoRows {
		return domain.ClaimedRun{}, fmt.Errorf("lock deployment for run query: %w", lookupErr)
	}
	if audit != nil && audit.ID != "" {
		audit.TargetID = claimedRun.ID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"runner_id": runner.ID, "lease_id": lease.ID, "attempt": lease.Attempt, "fence": lease.Fence})
		if err := createAuditWithQueries(ctx, queries, *audit); err != nil {
			return domain.ClaimedRun{}, fmt.Errorf("create audit event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ClaimedRun{}, fmt.Errorf("commit transaction: %w", err)
	}
	return domain.ClaimedRun{Lease: lease, Run: *claimedRun, PrimitivePlan: primitivePlanForRun(*claimedRun)}, nil
}

func (s *PostgresStore) ExpireLeases(ctx context.Context, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := expireLeasesAtDBClock(ctx, s.queries.WithTx(tx)); err != nil {
		return fmt.Errorf("expire leases at db clock: %w", err)
	}
	return tx.Commit(ctx)
}

func expireLeasesAtDBClock(ctx context.Context, queries *sqlcgen.Queries) error {
	runIDs, err := queries.ListExpiredRunningRunIDs(ctx, leaseExpiryBatchSize)
	if err != nil {
		return fmt.Errorf("list expired running run ids query: %w", err)
	}
	for _, runID := range runIDs {
		expired, err := queries.ExpireActiveLeasesForRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("expire active leases for run query: %w", err)
		}
		if expired == 0 {
			continue
		}
		if err := queries.ExpireDeploymentAttemptsForRun(ctx, runID); err != nil {
			return fmt.Errorf("expire deployment attempts for run query: %w", err)
		}
		if err := queries.RequeueExpiredRun(ctx, runID); err != nil {
			return fmt.Errorf("requeue expired run query: %w", err)
		}
	}
	return nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, runnerID, leaseID, fence string, attempt int, now time.Time, ttl time.Duration) (domain.RunLease, error) {
	// The WHERE clause is the fencing CAS: identity, generation, opaque fence and
	// unexpired active state must all still match at the instant of extension.
	lease, err := s.queries.RenewAuthorizedLease(ctx, sqlcgen.RenewAuthorizedLeaseParams{TtlMicroseconds: ttl.Microseconds(), LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	} else if err != nil {
		return domain.RunLease{}, fmt.Errorf("renew authorized lease query: %w", err)
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) CompleteLeaseRequest(ctx context.Context, leaseID string, runnerID string, status string, attempt int, fence string, completionKey string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("encode audit metadata: %w", err)
	}
	var workflowStateJSON json.RawMessage
	if workflowState != nil {
		workflowStateRaw, err := json.Marshal(workflowState)
		if err != nil {
			return domain.RunLease{}, fmt.Errorf("encode workflow state: %w", err)
		}
		workflowStateJSON = workflowStateRaw
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	runID, err := queries.GetLeaseRunID(ctx, leaseID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	} else if err != nil {
		return domain.RunLease{}, fmt.Errorf("get lease run id query: %w", err)
	}
	if _, err := queries.LockRunID(ctx, runID); err != nil {
		return domain.RunLease{}, fmt.Errorf("lock run id query: %w", err)
	}
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.RunLease{}, fmt.Errorf("acquire run log lock query: %w", err)
	}
	if deploymentRun, deploymentErr := queries.IsDeploymentRun(ctx, runID); deploymentErr != nil {
		return domain.RunLease{}, fmt.Errorf("is deployment run query: %w", deploymentErr)
	} else if deploymentRun {
		return domain.RunLease{}, ErrConflict
	}
	committed, err := queries.GetCommittedCompletion(ctx, sqlcgen.GetCommittedCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence, CompletionKey: &completionKey, Status: status})
	if err == nil {
		return runLeaseFromSQLC(committed), nil
	}
	if err != pgx.ErrNoRows {
		return domain.RunLease{}, fmt.Errorf("get committed completion query: %w", err)
	}
	leaseRow, err := queries.CompleteAuthorizedLease(ctx, sqlcgen.CompleteAuthorizedLeaseParams{Status: status, CompletionKey: &completionKey, LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		observed, lookupErr := queries.GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
		if lookupErr == nil && observed.CompletionKey != nil {
			return domain.RunLease{}, ErrConflict
		}
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("complete authorized lease query: %w", err)
	}
	lease := runLeaseFromSQLC(leaseRow)
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: lease.RunID, Status: runStatus, FinishedAt: finishedAt}); err != nil {
		return domain.RunLease{}, fmt.Errorf("update run status query: %w", err)
	}
	if workflowState != nil {
		if _, err := queries.UpdateRunWorkflowState(ctx, sqlcgen.UpdateRunWorkflowStateParams{ID: lease.RunID, WorkflowState: workflowStateJSON}); err != nil {
			return domain.RunLease{}, fmt.Errorf("update run workflow state query: %w", err)
		}
	}
	for _, log := range logs {
		if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
			return domain.RunLease{}, err
		}
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.RunLease{}, fmt.Errorf("create audit event query: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLease{}, fmt.Errorf("commit transaction: %w", err)
	}
	return lease, nil
}

func (s *PostgresStore) GetLeaseForCompletion(ctx context.Context, leaseID, runnerID string, attempt int, fence string) (domain.RunLease, error) {
	lease, err := s.queries.GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("get lease for completion query: %w", err)
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) AuthorizeSecretAccess(ctx context.Context, request domain.SecretAccessRequest) (domain.SecretAccessGrant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	// The exact lease row is locked before idempotency lookup. A prior audit can
	// therefore never authorize a read after DB-clock expiry or reassignment.
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{
		LeaseID: request.LeaseID, RunID: request.RunID, RunnerID: request.RunnerID,
		Attempt: int32(request.Attempt), Fence: request.Fence,
	}); err == pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, ErrNotFound
	} else if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("lock authorized lease query: %w", err)
	}
	expected := secretAccessAudit(request, request.RequestedAt)
	var replay *domain.AuditEvent
	existingRow, err := queries.GetAuditEventByID(ctx, request.AccessID)
	if err == nil {
		existing, mapErr := auditEventFromSQLC(existingRow)
		if mapErr != nil {
			return domain.SecretAccessGrant{}, fmt.Errorf("decode audit event row: %w", mapErr)
		}
		if !secretAccessAuditsEqual(existing, expected) {
			return domain.SecretAccessGrant{}, ErrConflict
		}
		replay = &existing
	}
	if err != nil && err != pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, fmt.Errorf("get audit event by id query: %w", err)
	}
	runContext, err := queries.GetRunContextForSecretAccess(ctx, request.RunID)
	if err == pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, ErrNotFound
	} else if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("get run context for secret access query: %w", err)
	}
	run := domain.TaskRun{ID: request.RunID}
	if err := json.Unmarshal(runContext.RunSpec, &run.RunSpec); err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("decode run spec: %w", err)
	}
	if err := json.Unmarshal(runContext.Workflow, &run.Workflow); err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("decode workflow: %w", err)
	}
	if err := json.Unmarshal(runContext.WorkflowState, &run.WorkflowState); err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("decode workflow state: %w", err)
	}
	if !runAuthorizesSecretAccess(run, request) {
		return domain.SecretAccessGrant{}, ErrNotFound
	}
	if replay != nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.SecretAccessGrant{}, fmt.Errorf("commit transaction: %w", err)
		}
		return secretAccessGrant(request, replay.CreatedAt), nil
	}
	metadata, err := json.Marshal(expected.Metadata)
	if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("encode audit metadata: %w", err)
	}
	createdRow, err := queries.CreateSecretAccessAudit(ctx, sqlcgen.CreateSecretAccessAuditParams{
		ID: request.AccessID, ActorID: request.RunnerID, TargetID: request.RunID, Metadata: metadata,
	})
	if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("create secret access audit query: %w", err)
	}
	created, err := auditEventFromSQLC(createdRow)
	if err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("decode audit event row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SecretAccessGrant{}, fmt.Errorf("commit transaction: %w", err)
	}
	return secretAccessGrant(request, created.CreatedAt), nil
}

func (s *PostgresStore) CancelRunRequest(ctx context.Context, runID string, canceledAt time.Time, log domain.RunLog, audit domain.AuditEvent) (domain.TaskRun, error) {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("encode audit metadata: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	status, err := queries.LockCancellableRunStatus(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	} else if err != nil {
		return domain.TaskRun{}, fmt.Errorf("lock cancellable run status query: %w", err)
	}
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.TaskRun{}, fmt.Errorf("reject generic deployment run: %w", err)
	}
	if domain.IsTerminalRunStatus(status) {
		return domain.TaskRun{}, ErrNotFound
	}
	canceledAt, err = queries.DatabaseClock(ctx)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("database clock query: %w", err)
	}
	log.CreatedAt = canceledAt
	audit.CreatedAt = canceledAt
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.TaskRun{}, fmt.Errorf("acquire run log lock query: %w", err)
	}
	if err := queries.CancelActiveLeasesForRun(ctx, runID); err != nil {
		return domain.TaskRun{}, fmt.Errorf("cancel active leases for run query: %w", err)
	}
	if err := queries.CancelRun(ctx, sqlcgen.CancelRunParams{RunID: runID, FinishedAt: &canceledAt}); err != nil {
		return domain.TaskRun{}, fmt.Errorf("cancel run query: %w", err)
	}
	updated, err := queries.GetRun(ctx, runID)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("get run query: %w", err)
	}
	run, err := taskRunFromSQLC(updated)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.TaskRun{}, fmt.Errorf("create audit event query: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskRun{}, fmt.Errorf("commit transaction: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) ActiveLeaseForRun(ctx context.Context, runID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRun(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("get active lease for run query: %w", err)
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) GetLeaseForRunner(ctx context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRunner(ctx, sqlcgen.GetActiveLeaseForRunnerParams{LeaseID: leaseID, RunnerID: runnerID})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, fmt.Errorf("get active lease for runner query: %w", err)
	}
	return runLeaseFromSQLC(lease), nil
}
