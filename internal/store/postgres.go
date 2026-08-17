package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

var (
	_ ProjectRepository       = (*PostgresStore)(nil)
	_ ProjectMemberRepository = (*PostgresStore)(nil)
	_ TemplateRepository      = (*PostgresStore)(nil)
	_ SourceRepository        = (*PostgresStore)(nil)
	_ RunRepository           = (*PostgresStore)(nil)
	_ RunnerRepository        = (*PostgresStore)(nil)
	_ UserRepository          = (*PostgresStore)(nil)
	_ SessionRepository       = (*PostgresStore)(nil)
	_ APITokenRepository      = (*PostgresStore)(nil)
	_ ApprovalRepository      = (*PostgresStore)(nil)
	_ AuditRepository         = (*PostgresStore)(nil)
)

// A claim or periodic reaper transaction may lock at most this many expired
// task runs. Keeping maintenance bounded prevents a large expired fleet from
// turning one runner claim into a global lock/materialization operation.
const leaseExpiryBatchSize = 64

func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 20
	config.MinConns = 0
	config.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	queries := sqlcgen.New(pool)
	compatible, err := queries.SchedulerSchemaCompatible(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if compatible == nil || !*compatible {
		pool.Close()
		return nil, fmt.Errorf("database schema is incompatible: apply fenced lease, claim cursor, runner replay, and runner enrollment migrations before starting NeroCD")
	}
	return &PostgresStore{pool: pool, queries: queries}, nil
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	user, err := s.queries.GetUserByEmail(ctx, email)
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) CreateSession(ctx context.Context, session domain.Session, tokenHash string) error {
	return s.queries.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt})
}

func (s *PostgresStore) GetPrincipalBySessionTokenHash(ctx context.Context, tokenHash string, now time.Time) (domain.User, error) {
	user, err := s.queries.GetPrincipalBySessionTokenHash(ctx, sqlcgen.GetPrincipalBySessionTokenHashParams{TokenHash: tokenHash, ExpiresAt: now})
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) RevokeSessionByTokenHash(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	count, err := s.queries.RevokeSessionByTokenHash(ctx, sqlcgen.RevokeSessionByTokenHashParams{TokenHash: tokenHash, RevokedAt: &revokedAt})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateAPIToken(ctx context.Context, token domain.APIToken) (domain.APIToken, error) {
	inserted, err := s.queries.CreateAPIToken(ctx, sqlcgen.CreateAPITokenParams{ID: token.ID, Name: token.Name, Kind: token.Kind, TokenHash: token.TokenHash, Roles: token.Roles, Status: token.Status, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(inserted), nil
}

func (s *PostgresStore) GetAPITokenByHash(ctx context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	token, err := s.queries.GetAPITokenByHash(ctx, sqlcgen.GetAPITokenByHashParams{TokenHash: tokenHash, LastUsedAt: &now})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(token), nil
}

func (s *PostgresStore) RevokeAPIToken(ctx context.Context, tokenID string, revokedAt time.Time) (domain.APIToken, error) {
	token, err := s.queries.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{ID: tokenID, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(token), nil
}

func (s *PostgresStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromSQLC(row))
	}
	return projects, nil
}

func (s *PostgresStore) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	inserted, err := s.queries.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(inserted), nil
}

func (s *PostgresStore) UpdateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	updated, err := s.queries.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(updated), nil
}

func (s *PostgresStore) ArchiveProject(ctx context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	archived, err := s.queries.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(archived), nil
}

func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.queries.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, projectMemberListRowFromSQLC(row))
	}
	return members, nil
}

func (s *PostgresStore) UpsertProjectMember(ctx context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	upserted, err := s.queries.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	user, err := s.queries.GetUserByID(ctx, upserted.UserID)
	if err == pgx.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return projectMemberFromSQLC(upserted, user), nil
}

func (s *PostgresStore) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	rows, err := s.queries.ListRepositories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repositories := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repositories = append(repositories, repositoryFromSQLC(row))
	}
	return repositories, nil
}

func (s *PostgresStore) CreateRepository(ctx context.Context, repository domain.Repository) (domain.Repository, error) {
	inserted, err := s.queries.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, CreatedAt: repository.CreatedAt})
	if err != nil {
		return domain.Repository{}, err
	}
	return repositoryFromSQLC(inserted), nil
}

func (s *PostgresStore) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	rows, err := s.queries.ListAccessKeys(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]domain.AccessKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, accessKeyFromSQLC(row))
	}
	return keys, nil
}

func (s *PostgresStore) CreateAccessKey(ctx context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	inserted, err := s.queries.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
	if err != nil {
		return domain.AccessKey{}, err
	}
	return accessKeyFromSQLC(inserted), nil
}

func (s *PostgresStore) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	rows, err := s.queries.ListInventories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	inventories := make([]domain.Inventory, 0, len(rows))
	for _, row := range rows {
		inventories = append(inventories, inventoryFromSQLC(row))
	}
	return inventories, nil
}

func (s *PostgresStore) CreateInventory(ctx context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	inserted, err := s.queries.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
	if err != nil {
		return domain.Inventory{}, err
	}
	return inventoryFromSQLC(inserted), nil
}

func (s *PostgresStore) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	rows, err := s.queries.ListTemplates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	templates := make([]domain.TaskTemplate, 0, len(rows))
	for _, row := range rows {
		template, err := taskTemplateFromSQLC(row)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	return templates, nil
}

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (domain.TaskTemplate, error) {
	template, err := s.queries.GetTemplate(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(template)
}

func (s *PostgresStore) CreateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	inserted, err := s.queries.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(inserted)
}

func (s *PostgresStore) UpdateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	updated, err := s.queries.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(updated)
}

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

func (s *PostgresStore) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	rows, err := s.queries.ListRunners(ctx)
	if err != nil {
		return nil, err
	}
	runners := make([]domain.Runner, 0, len(rows))
	for _, row := range rows {
		runners = append(runners, runnerFromSQLC(row))
	}
	return runners, nil
}

func (s *PostgresStore) RegisterRunner(ctx context.Context, runner domain.Runner) (domain.Runner, error) {
	upserted, err := s.queries.RegisterRunner(ctx, sqlcgen.RegisterRunnerParams{ID: runner.ID, Name: runner.Name, Tags: runner.Tags, Capabilities: runner.Capabilities, Status: runner.Status, RegisteredAt: runner.RegisteredAt, LastHeartbeatAt: runner.LastHeartbeatAt, TokenHash: runner.TokenHash})
	if isUniqueViolation(err) {
		return domain.Runner{}, ErrConflict
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(upserted), nil
}

func (s *PostgresStore) CreateRunnerEnrollment(ctx context.Context, enrollment domain.RunnerEnrollment, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	ttl := enrollment.ExpiresAt.Sub(enrollment.CreatedAt)
	created, err := queries.CreateRunnerEnrollment(ctx, sqlcgen.CreateRunnerEnrollmentParams{ID: enrollment.ID, TokenHash: enrollment.TokenHash, RunnerID: enrollment.RunnerID, RunnerName: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, CreatedBy: enrollment.CreatedBy, TtlMicroseconds: ttl.Microseconds()})
	if isUniqueViolation(err) {
		return domain.RunnerEnrollment{}, ErrConflict
	}
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return runnerEnrollmentFromSQLC(created), nil
}

func (s *PostgresStore) RevokeRunnerEnrollment(ctx context.Context, enrollmentID string, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	revoked, err := queries.RevokeUnusedRunnerEnrollment(ctx, enrollmentID)
	if err == pgx.ErrNoRows {
		return domain.RunnerEnrollment{}, ErrNotFound
	}
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return runnerEnrollmentFromSQLC(revoked), nil
}

func (s *PostgresStore) ConsumeRunnerEnrollment(ctx context.Context, consume domain.RunnerEnrollmentConsume, audit domain.AuditEvent) (domain.Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	enrollment, err := queries.LockRunnerEnrollmentByTokenHash(ctx, consume.TokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
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
			return domain.Runner{}, err
		}
		return runnerFromSQLC(runnerRow), nil
	}
	requestID, credentialHash := consume.RequestID, consume.CredentialHash
	if _, err := queries.MarkRunnerEnrollmentUsed(ctx, sqlcgen.MarkRunnerEnrollmentUsedParams{ID: enrollment.ID, ConsumeRequestID: &requestID, CredentialHash: &credentialHash}); err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	} else if err != nil {
		return domain.Runner{}, err
	}
	runnerRow, err := queries.CreateRunnerForEnrollment(ctx, sqlcgen.CreateRunnerForEnrollmentParams{ID: enrollment.RunnerID, Name: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, TokenHash: consume.CredentialHash})
	if isUniqueViolation(err) {
		return domain.Runner{}, ErrConflict
	}
	if err != nil {
		return domain.Runner{}, err
	}
	audit.ActorID = enrollment.RunnerID
	audit.TargetID = enrollment.RunnerID
	audit.Metadata = map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.Runner{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runnerRow), nil
}

func createAuditWithQueries(ctx context.Context, queries *sqlcgen.Queries, audit domain.AuditEvent) error {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return err
	}
	return queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) UpdateRunnerToken(ctx context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	updated, err := s.queries.UpdateRunnerToken(ctx, sqlcgen.UpdateRunnerTokenParams{ID: runnerID, TokenHash: tokenHash, Status: status, LastHeartbeatAt: updatedAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(updated), nil
}

func (s *PostgresStore) GetRunnerByTokenHash(ctx context.Context, tokenHash string) (domain.Runner, error) {
	runner, err := s.queries.GetRunnerByTokenHash(ctx, tokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) HeartbeatRunner(ctx context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	runner, err := s.queries.HeartbeatRunner(ctx, sqlcgen.HeartbeatRunnerParams{ID: id, LastHeartbeatAt: heartbeatAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	markedStale, err := queries.MarkStaleRunnerForClaim(ctx, sqlcgen.MarkStaleRunnerForClaimParams{RunnerID: runnerID, StaleBefore: now.Add(-2 * ttl)})
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	if markedStale > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, err
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	runnerRow, err := queries.GetActiveRunnerForClaim(ctx, sqlcgen.GetActiveRunnerForClaimParams{ID: runnerID, LastHeartbeatAt: now.Add(-2 * ttl)})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	runner := runnerFromSQLC(runnerRow)
	// One durable cursor row both serializes claims by the same runner and keeps
	// bounded scans moving across persistently incompatible queued work.
	if err := queries.EnsureRunnerClaimCursor(ctx, runner.ID); err != nil {
		return domain.ClaimedRun{}, err
	}
	storedCursor, err := queries.LockRunnerClaimCursor(ctx, runner.ID)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	// Claim locks runner cursor -> task run. Completion, periodic expiry, and
	// cancellation do not need a cursor and lock task run -> per-run log advisory
	// lock -> lease. Fenced log/artifact paths omit task run and begin at
	// advisory/lease. No path acquires a runner cursor after a task-run lock.
	if err := expireLeasesAtDBClock(ctx, queries); err != nil {
		return domain.ClaimedRun{}, err
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
			return domain.ClaimedRun{}, err
		}
		for _, candidate := range candidates {
			cursorOrder = &candidate.claimOrderAt
			cursorID = candidate.id
			row, err := queries.GetRun(ctx, candidate.id)
			if err != nil {
				return domain.ClaimedRun{}, err
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
			return domain.ClaimedRun{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, err
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err := queries.StoreRunnerClaimCursor(ctx, sqlcgen.StoreRunnerClaimCursorParams{ClaimOrderAt: cursorOrder, RunID: cursorID, RunnerID: runner.ID}); err != nil {
		return domain.ClaimedRun{}, err
	}
	attempt, err := queries.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: claimedRun.ID, RunnerID: &runner.ID})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	} else if err != nil {
		return domain.ClaimedRun{}, err
	}
	claimedRun.Status = domain.RunRunning
	claimedRun.RunnerID = &runner.ID
	leaseID, err := newLeaseToken("lease")
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	fence, err := newLeaseToken("fence")
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	leaseRow, err := queries.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: leaseID, RunID: claimedRun.ID, RunnerID: runner.ID, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	lease := runLeaseFromSQLC(leaseRow)
	if err := tx.Commit(ctx); err != nil {
		return domain.ClaimedRun{}, err
	}
	return domain.ClaimedRun{Lease: lease, Run: *claimedRun, PrimitivePlan: primitivePlanForRun(*claimedRun)}, nil
}

func (s *PostgresStore) ExpireLeases(ctx context.Context, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := expireLeasesAtDBClock(ctx, s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func expireLeasesAtDBClock(ctx context.Context, queries *sqlcgen.Queries) error {
	runIDs, err := queries.ListExpiredRunningRunIDs(ctx, leaseExpiryBatchSize)
	if err != nil {
		return err
	}
	for _, runID := range runIDs {
		expired, err := queries.ExpireActiveLeasesForRun(ctx, runID)
		if err != nil {
			return err
		}
		if expired == 0 {
			continue
		}
		if err := queries.RequeueExpiredRun(ctx, runID); err != nil {
			return err
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
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) CompleteLeaseRequest(ctx context.Context, leaseID string, runnerID string, status string, attempt int, fence string, completionKey string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.RunLease{}, err
	}
	var workflowStateJSON json.RawMessage
	if workflowState != nil {
		workflowStateRaw, err := json.Marshal(workflowState)
		if err != nil {
			return domain.RunLease{}, err
		}
		workflowStateJSON = workflowStateRaw
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	runID, err := queries.GetLeaseRunID(ctx, leaseID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	} else if err != nil {
		return domain.RunLease{}, err
	}
	if _, err := queries.LockRunID(ctx, runID); err != nil {
		return domain.RunLease{}, err
	}
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.RunLease{}, err
	}
	committed, err := queries.GetCommittedCompletion(ctx, sqlcgen.GetCommittedCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence, CompletionKey: &completionKey, Status: status})
	if err == nil {
		return runLeaseFromSQLC(committed), nil
	}
	if err != pgx.ErrNoRows {
		return domain.RunLease{}, err
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
		return domain.RunLease{}, err
	}
	lease := runLeaseFromSQLC(leaseRow)
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: lease.RunID, Status: runStatus, FinishedAt: finishedAt}); err != nil {
		return domain.RunLease{}, err
	}
	if workflowState != nil {
		if _, err := queries.UpdateRunWorkflowState(ctx, sqlcgen.UpdateRunWorkflowStateParams{ID: lease.RunID, WorkflowState: workflowStateJSON}); err != nil {
			return domain.RunLease{}, err
		}
	}
	for _, log := range logs {
		if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
			return domain.RunLease{}, err
		}
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.RunLease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) GetLeaseForCompletion(ctx context.Context, leaseID, runnerID string, attempt int, fence string) (domain.RunLease, error) {
	lease, err := s.queries.GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) AuthorizeSecretAccess(ctx context.Context, request domain.SecretAccessRequest) (domain.SecretAccessGrant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.SecretAccessGrant{}, err
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
		return domain.SecretAccessGrant{}, err
	}
	expected := secretAccessAudit(request, request.RequestedAt)
	var replay *domain.AuditEvent
	existingRow, err := queries.GetAuditEventByID(ctx, request.AccessID)
	if err == nil {
		existing, mapErr := auditEventFromSQLC(existingRow)
		if mapErr != nil {
			return domain.SecretAccessGrant{}, mapErr
		}
		if !secretAccessAuditsEqual(existing, expected) {
			return domain.SecretAccessGrant{}, ErrConflict
		}
		replay = &existing
	}
	if err != nil && err != pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, err
	}
	runContext, err := queries.GetRunContextForSecretAccess(ctx, request.RunID)
	if err == pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, ErrNotFound
	} else if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	run := domain.TaskRun{ID: request.RunID}
	if err := json.Unmarshal(runContext.RunSpec, &run.RunSpec); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := json.Unmarshal(runContext.Workflow, &run.Workflow); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := json.Unmarshal(runContext.WorkflowState, &run.WorkflowState); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if !runAuthorizesSecretAccess(run, request) {
		return domain.SecretAccessGrant{}, ErrNotFound
	}
	if replay != nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.SecretAccessGrant{}, err
		}
		return secretAccessGrant(request, replay.CreatedAt), nil
	}
	metadata, err := json.Marshal(expected.Metadata)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	createdRow, err := queries.CreateSecretAccessAudit(ctx, sqlcgen.CreateSecretAccessAuditParams{
		ID: request.AccessID, ActorID: request.RunnerID, TargetID: request.RunID, Metadata: metadata,
	})
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	created, err := auditEventFromSQLC(createdRow)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	return secretAccessGrant(request, created.CreatedAt), nil
}

func (s *PostgresStore) CancelRunRequest(ctx context.Context, runID string, canceledAt time.Time, log domain.RunLog, audit domain.AuditEvent) (domain.TaskRun, error) {
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
	status, err := queries.LockCancellableRunStatus(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	} else if err != nil {
		return domain.TaskRun{}, err
	}
	if domain.IsTerminalRunStatus(status) {
		return domain.TaskRun{}, ErrNotFound
	}
	canceledAt, err = queries.DatabaseClock(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	log.CreatedAt = canceledAt
	audit.CreatedAt = canceledAt
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CancelActiveLeasesForRun(ctx, runID); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CancelRun(ctx, sqlcgen.CancelRunParams{RunID: runID, FinishedAt: &canceledAt}); err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := queries.GetRun(ctx, runID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	run, err := taskRunFromSQLC(updated)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.TaskRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) ActiveLeaseForRun(ctx context.Context, runID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRun(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) GetLeaseForRunner(ctx context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRunner(ctx, sqlcgen.GetActiveLeaseForRunnerParams{LeaseID: leaseID, RunnerID: runnerID})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
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
	_, err := s.queries.CreateArtifact(ctx, sqlcgen.CreateArtifactParams{ID: artifact.ID, RunID: artifact.RunID, LeaseID: artifact.LeaseID, Name: artifact.Name, Path: artifact.Path, Found: artifact.Found, Required: artifact.Required, Size: artifact.Size, Kind: artifact.Kind, CreatedAt: artifact.CreatedAt})
	return err
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

func (s *PostgresStore) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	rows, err := s.queries.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	approvals := make([]domain.Approval, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, approvalFromSQLC(row))
	}
	return approvals, nil
}

func (s *PostgresStore) CreateApproval(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	inserted, err := s.queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt})
	if err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(inserted), nil
}

func (s *PostgresStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalApproved, ApprovedBy: &actorID, ApprovedAt: &approvedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.QueueApprovedRun(ctx, runID); err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	} else if err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}

func (s *PostgresStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalRejected, ApprovedBy: &actorID, ApprovedAt: &rejectedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: runID, Status: domain.RunCanceled, FinishedAt: &rejectedAt}); err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}

func (s *PostgresStore) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	total64, err := s.queries.CountAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListAuditEventsPage(ctx, sqlcgen.ListAuditEventsPageParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := auditEventFromSQLC(row)
		if err != nil {
			return PageResult[domain.AuditEvent]{}, err
		}
		events = append(events, event)
	}
	return PageResult[domain.AuditEvent]{Items: events, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	return s.queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: event.ID, ActorID: event.ActorID, Action: event.Action, TargetID: event.TargetID, Metadata: metadata, CreatedAt: event.CreatedAt})
}

func decodeWorkflowState(raw []byte, workflowState *domain.WorkflowState) error {
	if len(raw) == 0 {
		*workflowState = domain.WorkflowState{}
		return nil
	}
	return json.Unmarshal(raw, workflowState)
}

func decodeRunSpec(raw []byte, runSpec *domain.RunSpec) error {
	if len(raw) == 0 {
		*runSpec = domain.RunSpec{Inputs: map[string]any{}}
		return nil
	}
	if err := json.Unmarshal(raw, runSpec); err != nil {
		return err
	}
	if runSpec.Inputs == nil {
		runSpec.Inputs = map[string]any{}
	}
	return nil
}

func decodeWorkflow(raw []byte, workflow *domain.Workflow) error {
	if len(raw) == 0 {
		*workflow = domain.Workflow{}
		return nil
	}
	return json.Unmarshal(raw, workflow)
}

func resolvePage(page Page, total int) (int, int) {
	limit := total
	offset := 0
	if page.Enabled {
		limit = page.Limit
		offset = page.Offset
	}
	if page.Enabled && limit == 0 {
		limit = total
	}
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	return limit, offset
}
