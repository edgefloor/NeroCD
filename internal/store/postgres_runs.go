package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListRuns(ctx context.Context, projectID string) ([]domain.TaskRun, error) {
	result, err := s.ListRunsPage(ctx, projectID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	total64, err := s.queries.CountRuns(ctx, projectID)
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListRunsPage(ctx, sqlcgen.ListRunsPageParams{ProjectID: projectID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	runs := make([]domain.TaskRun, 0, len(rows))
	for _, row := range rows {
		run, err := taskRunFromSQLC(row)
		if err != nil {
			return PageResult[domain.TaskRun]{}, err
		}
		runs = append(runs, run)
	}
	return PageResult[domain.TaskRun]{Items: runs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	runSpec, workflow, state, err := runJSON(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	inserted, err := s.queries.CreateRun(ctx, sqlcgen.CreateRunParams{ID: run.ID, ProjectID: run.ProjectID, TemplateID: run.TemplateID, RunSpec: runSpec, Workflow: workflow, WorkflowState: state, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(inserted)
}

func (s *PostgresStore) CreateRunRequest(ctx context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	runSpec, workflow, state, err := runJSON(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.TaskRun{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	inserted, err := queries.CreateRun(ctx, sqlcgen.CreateRunParams{ID: run.ID, ProjectID: run.ProjectID, TemplateID: run.TemplateID, RunSpec: runSpec, Workflow: workflow, WorkflowState: state, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt})
	if err != nil {
		return domain.TaskRun{}, err
	}
	run, err = taskRunFromSQLC(inserted)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return domain.TaskRun{}, err
	}
	if approval != nil {
		if _, err := queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt}); err != nil {
			return domain.TaskRun{}, err
		}
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.TaskRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) UpdateRunStatus(ctx context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, id); err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := s.queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: id, Status: status, FinishedAt: finishedAt})
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(updated)
}

func (s *PostgresStore) UpdateRunWorkflowState(ctx context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, id); err != nil {
		return domain.TaskRun{}, err
	}
	raw, err := json.Marshal(workflowState)
	if err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := s.queries.UpdateRunWorkflowState(ctx, sqlcgen.UpdateRunWorkflowStateParams{ID: id, WorkflowState: raw})
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(updated)
}

func (s *PostgresStore) CreateRunLog(ctx context.Context, log domain.RunLog) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, log.RunID); err != nil {
		return err
	}
	if err := queries.AcquireRunLogLock(ctx, log.RunID); err != nil {
		return err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateRunLogForLease(ctx context.Context, log domain.RunLog, runnerID, leaseID string, attempt int, fence string, now time.Time) (domain.RunLog, error) {
	if log.EventKey != "" {
		logs, err := s.CreateRunLogsForLease(ctx, []domain.RunLog{log}, log.RunID, runnerID, leaseID, attempt, fence, now)
		if err != nil {
			return domain.RunLog{}, err
		}
		return logs[0], nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLog{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	// Serialize all log allocation for a run, including controller and runner events.
	if err := queries.AcquireRunLogLock(ctx, log.RunID); err != nil {
		return domain.RunLog{}, err
	}
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: leaseID, RunID: log.RunID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
		return domain.RunLog{}, ErrNotFound
	} else if err != nil {
		return domain.RunLog{}, err
	}
	log, err = insertRunLogWithSequence(ctx, tx, log)
	if err != nil {
		return domain.RunLog{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLog{}, err
	}
	return log, nil
}

func (s *PostgresStore) CreateRunLogsForLease(ctx context.Context, logs []domain.RunLog, runID, runnerID, leaseID string, attempt int, fence string, _ time.Time) ([]domain.RunLog, error) {
	if len(logs) == 0 {
		return []domain.RunLog{}, nil
	}
	for _, log := range logs {
		if log.RunID != runID || log.EventKey == "" || log.RequestedSequence <= 0 || log.LeaseID != leaseID || log.Attempt != attempt {
			return nil, ErrConflict
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return nil, err
	}
	// Exact replay is permitted after expiry, but only for the complete original
	// attempt identity. A persisted event key must not turn a partial or forged
	// capability into replay authority.
	if _, err := queries.GetLeaseReplayIdentity(ctx, sqlcgen.GetLeaseReplayIdentityParams{
		LeaseID: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence,
	}); err == pgx.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	results := make([]domain.RunLog, len(logs))
	newEvent := make([]bool, len(logs))
	hasNew := false
	for i, log := range logs {
		row, lookupErr := queries.GetRunnerEventByKey(ctx, sqlcgen.GetRunnerEventByKeyParams{RunID: runID, EventKey: &log.EventKey})
		if lookupErr == nil {
			existing := runLogFromSQLC(row)
			if existing.LeaseID != leaseID || existing.Attempt != attempt || existing.RequestedSequence != log.RequestedSequence || existing.Stream != log.Stream || existing.Message != log.Message {
				return nil, ErrConflict
			}
			results[i] = existing
			continue
		}
		if lookupErr != pgx.ErrNoRows {
			return nil, lookupErr
		}
		newEvent[i] = true
		hasNew = true
	}
	if hasNew {
		if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
	}
	for i, log := range logs {
		if !newEvent[i] {
			continue
		}
		attempt32 := int32(attempt)
		requested := int32(log.RequestedSequence)
		row, err := queries.InsertRunnerEventWithSequence(ctx, sqlcgen.InsertRunnerEventWithSequenceParams{
			ID: log.ID, RunID: runID, Sequence: requested, Stream: log.Stream, Message: log.Message, CreatedAt: log.CreatedAt,
			EventKey: &log.EventKey, LeaseID: &leaseID, Attempt: &attempt32, RequestedSequence: &requested,
		})
		if err != nil {
			return nil, err
		}
		results[i] = runLogFromSQLC(row)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *PostgresStore) ListRunLogs(ctx context.Context, runID string) ([]domain.RunLog, error) {
	result, err := s.ListRunLogsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	total64, err := s.queries.CountRunLogs(ctx, runID)
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListRunLogsPage(ctx, sqlcgen.ListRunLogsPageParams{RunID: runID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	logs := make([]domain.RunLog, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, runLogFromSQLC(row))
	}
	return PageResult[domain.RunLog]{Items: logs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) ListArtifacts(ctx context.Context, runID string) ([]domain.ArtifactRecord, error) {
	result, err := s.ListArtifactsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	total64, err := s.queries.CountArtifacts(ctx, strings.TrimSpace(runID))
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListArtifactsPage(ctx, sqlcgen.ListArtifactsPageParams{RunID: strings.TrimSpace(runID), PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	artifacts := make([]domain.ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		artifacts = append(artifacts, artifactFromSQLC(row))
	}
	return PageResult[domain.ArtifactRecord]{Items: artifacts, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateArtifact(ctx context.Context, artifact domain.ArtifactRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, artifact.RunID); err != nil {
		return err
	}
	if _, err := queries.CreateArtifact(ctx, sqlcgen.CreateArtifactParams{ID: artifact.ID, RunID: artifact.RunID, LeaseID: artifact.LeaseID, Name: artifact.Name, Path: artifact.Path, Found: artifact.Found, Required: artifact.Required, Size: artifact.Size, Kind: artifact.Kind, CreatedAt: artifact.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateArtifactForLease(ctx context.Context, artifact domain.ArtifactRecord, runnerID string, attempt int, fence string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: artifact.LeaseID, RunID: artifact.RunID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := queries.CreateArtifact(ctx, sqlcgen.CreateArtifactParams{ID: artifact.ID, RunID: artifact.RunID, LeaseID: artifact.LeaseID, Name: artifact.Name, Path: artifact.Path, Found: artifact.Found, Required: artifact.Required, Size: artifact.Size, Kind: artifact.Kind, CreatedAt: artifact.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
