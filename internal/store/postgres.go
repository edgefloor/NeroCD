package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/dialect"
	"github.com/stephenafamo/bob/dialect/psql/im"
	"github.com/stephenafamo/bob/dialect/psql/sm"
	"github.com/stephenafamo/bob/dialect/psql/um"
	bobtypes "github.com/stephenafamo/bob/types"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
	bobqueries "nerocd/internal/store/bobgen/queries"
)

type PostgresStore struct {
	db *bobDB
}

func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresStore{db: newBobDB(db)}, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	user, err := bobmodels.Users.Query(
		sm.Where(bobmodels.Users.Columns.Email.EQ(psql.Arg(email))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromGenerated(user), nil
}

func (s *PostgresStore) CreateSession(ctx context.Context, session domain.Session, tokenHash string) error {
	_, err := bobmodels.Sessions.Insert(sessionSetter(session, tokenHash)).One(ctx, s.db.generated())
	return err
}

func (s *PostgresStore) GetPrincipalBySessionTokenHash(ctx context.Context, tokenHash string, now time.Time) (domain.User, error) {
	user, err := bobqueries.PrincipalBySessionTokenHash(tokenHash, now).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromPrincipalRow(user), nil
}

func (s *PostgresStore) RevokeSessionByTokenHash(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	revoked := sql.Null[time.Time]{V: revokedAt, Valid: true}
	count, err := bobmodels.Sessions.Update(
		bobmodels.SessionSetter{RevokedAt: &revoked}.UpdateMod(),
		um.Where(bobmodels.Sessions.Columns.TokenHash.EQ(psql.Arg(tokenHash)).And(bobmodels.Sessions.Columns.RevokedAt.IsNull())),
	).Exec(ctx, s.db.generated())
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateAPIToken(ctx context.Context, token domain.APIToken) (domain.APIToken, error) {
	inserted, err := bobmodels.APITokens.Insert(apiTokenSetter(token)).One(ctx, s.db.generated())
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromGenerated(inserted), nil
}

func (s *PostgresStore) GetAPITokenByHash(ctx context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	lastUsedAt := sql.Null[time.Time]{V: now, Valid: true}
	token, err := bobmodels.APITokens.Update(
		bobmodels.APITokenSetter{LastUsedAt: &lastUsedAt}.UpdateMod(),
		um.Where(
			bobmodels.APITokens.Columns.TokenHash.EQ(psql.Arg(tokenHash)).
				And(bobmodels.APITokens.Columns.Status.EQ(psql.Arg(domain.TokenActive))).
				And(bobmodels.APITokens.Columns.RevokedAt.IsNull()).
				And(bobmodels.APITokens.Columns.ExpiresAt.IsNull().Or(bobmodels.APITokens.Columns.ExpiresAt.GT(psql.Arg(now)))),
		),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromGenerated(token), nil
}

func (s *PostgresStore) RevokeAPIToken(ctx context.Context, tokenID string, revokedAt time.Time) (domain.APIToken, error) {
	status := domain.TokenRevoked
	revoked := sql.Null[time.Time]{V: revokedAt, Valid: true}
	token, err := bobmodels.APITokens.Update(
		bobmodels.APITokenSetter{Status: &status, RevokedAt: &revoked}.UpdateMod(),
		um.Where(bobmodels.APITokens.Columns.ID.EQ(psql.Arg(tokenID)).And(bobmodels.APITokens.Columns.RevokedAt.IsNull())),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromGenerated(token), nil
}

func (s *PostgresStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := bobqueries.ActiveProjects().All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromGeneratedRow(row.ID, row.Name, row.Description, row.CreatedAt, row.ArchivedAt))
	}
	return projects, nil
}

func (s *PostgresStore) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	inserted, err := bobmodels.Projects.Insert(projectSetter(project)).One(ctx, s.db.generated())
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromGeneratedModel(inserted), nil
}

func (s *PostgresStore) UpdateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	updated, err := bobmodels.Projects.Update(
		bobmodels.ProjectSetter{
			Name:        &project.Name,
			Description: &project.Description,
		}.UpdateMod(),
		um.Set(um.SetCol("updated_at").To(psql.Raw("now()"))),
		um.Where(bobmodels.Projects.Columns.ID.EQ(psql.Arg(project.ID)).And(bobmodels.Projects.Columns.ArchivedAt.IsNull())),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromGeneratedModel(updated), nil
}

func (s *PostgresStore) ArchiveProject(ctx context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	archived, err := bobmodels.Projects.Update(
		bobmodels.ProjectSetter{
			ArchivedAt: &sql.Null[time.Time]{V: archivedAt, Valid: true},
		}.UpdateMod(),
		um.Set(um.SetCol("updated_at").To(psql.Raw("now()"))),
		um.Where(bobmodels.Projects.Columns.ID.EQ(psql.Arg(id)).And(bobmodels.Projects.Columns.ArchivedAt.IsNull())),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromGeneratedModel(archived), nil
}

func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	if projectID != "" {
		rows, err := bobqueries.ProjectMembersByProject(projectID).All(ctx, s.db.generated())
		if err != nil {
			return nil, err
		}
		members := make([]domain.ProjectMember, 0, len(rows))
		for _, row := range rows {
			members = append(members, projectMemberFromGeneratedByProject(row))
		}
		return members, nil
	}
	rows, err := bobqueries.ProjectMembers().All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, projectMemberFromGenerated(row))
	}
	return members, nil
}

func (s *PostgresStore) UpsertProjectMember(ctx context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	upserted, err := bobmodels.ProjectMembers.Insert(projectMemberSetter(member),
		im.OnConflict(psql.Quote("project_id"), psql.Quote("user_id")).DoUpdate(
			im.SetExcluded("role", "updated_at"),
		),
	).One(ctx, s.db.generated())
	if err != nil {
		return domain.ProjectMember{}, err
	}
	user, err := bobmodels.FindUser(ctx, s.db.generated(), upserted.UserID)
	if err == sql.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return projectMemberFromGeneratedModel(upserted, user), nil
}

func (s *PostgresStore) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.Repositories.Columns.Name).Asc(),
	}
	if projectID != "" {
		mods = append(mods, sm.Where(bobmodels.Repositories.Columns.ProjectID.EQ(psql.Arg(projectID))))
	}
	rows, err := bobmodels.Repositories.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	repositories := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repositories = append(repositories, repositoryFromGenerated(row))
	}
	return repositories, nil
}

func (s *PostgresStore) CreateRepository(ctx context.Context, repository domain.Repository) (domain.Repository, error) {
	inserted, err := bobmodels.Repositories.Insert(repositorySetter(repository)).One(ctx, s.db.generated())
	if err != nil {
		return domain.Repository{}, err
	}
	return repositoryFromGenerated(inserted), nil
}

func (s *PostgresStore) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.AccessKeys.Columns.Name).Asc(),
	}
	if projectID != "" {
		mods = append(mods, sm.Where(bobmodels.AccessKeys.Columns.ProjectID.EQ(psql.Arg(projectID))))
	}
	rows, err := bobmodels.AccessKeys.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	keys := make([]domain.AccessKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, accessKeyFromGenerated(row))
	}
	return keys, nil
}

func (s *PostgresStore) CreateAccessKey(ctx context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	inserted, err := bobmodels.AccessKeys.Insert(accessKeySetter(key)).One(ctx, s.db.generated())
	if err != nil {
		return domain.AccessKey{}, err
	}
	return accessKeyFromGenerated(inserted), nil
}

func (s *PostgresStore) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.Inventories.Columns.Name).Asc(),
	}
	if projectID != "" {
		mods = append(mods, sm.Where(bobmodels.Inventories.Columns.ProjectID.EQ(psql.Arg(projectID))))
	}
	rows, err := bobmodels.Inventories.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	inventories := make([]domain.Inventory, 0, len(rows))
	for _, row := range rows {
		inventories = append(inventories, inventoryFromGenerated(row))
	}
	return inventories, nil
}

func (s *PostgresStore) CreateInventory(ctx context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	inserted, err := bobmodels.Inventories.Insert(inventorySetter(inventory)).One(ctx, s.db.generated())
	if err != nil {
		return domain.Inventory{}, err
	}
	return inventoryFromGenerated(inserted), nil
}

func (s *PostgresStore) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.TaskTemplates.Columns.Name).Asc(),
	}
	if projectID != "" {
		mods = append(mods, sm.Where(bobmodels.TaskTemplates.Columns.ProjectID.EQ(psql.Arg(projectID))))
	}
	rows, err := bobmodels.TaskTemplates.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	templates := make([]domain.TaskTemplate, 0, len(rows))
	for _, row := range rows {
		template, err := taskTemplateFromGenerated(row)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	return templates, nil
}

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (domain.TaskTemplate, error) {
	template, err := bobmodels.FindTaskTemplate(ctx, s.db.generated(), id)
	if err == sql.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromGenerated(template)
}

func (s *PostgresStore) CreateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	setter, err := taskTemplateSetter(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	inserted, err := bobmodels.TaskTemplates.Insert(setter).One(ctx, s.db.generated())
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromGenerated(inserted)
}

func (s *PostgresStore) UpdateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	setter, err := taskTemplateSetter(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	setter.ID = nil
	setter.ProjectID = nil
	updated, err := bobmodels.TaskTemplates.Update(
		setter.UpdateMod(),
		um.Set(um.SetCol("updated_at").To(psql.Raw("now()"))),
		um.Where(bobmodels.TaskTemplates.Columns.ID.EQ(psql.Arg(template.ID))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromGenerated(updated)
}

func (s *PostgresStore) ListRuns(ctx context.Context, projectID string) ([]domain.TaskRun, error) {
	result, err := s.ListRunsPage(ctx, projectID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.TaskRuns.Columns.StartedAt).Desc(),
	}
	if projectID != "" {
		mods = append(mods, sm.Where(bobmodels.TaskRuns.Columns.ProjectID.EQ(psql.Arg(projectID))))
	}
	baseQuery := bobmodels.TaskRuns.Query(mods...)
	total64, err := baseQuery.Count(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	if page.Enabled {
		mods = append(mods, sm.Limit(limit), sm.Offset(offset))
	}
	rows, err := bobmodels.TaskRuns.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	runs := make([]domain.TaskRun, 0, len(rows))
	for _, row := range rows {
		run, err := taskRunFromGenerated(row)
		if err != nil {
			return PageResult[domain.TaskRun]{}, err
		}
		runs = append(runs, run)
	}
	return PageResult[domain.TaskRun]{Items: runs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	setter, err := taskRunSetter(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	inserted, err := bobmodels.TaskRuns.Insert(setter).One(ctx, s.db.generated())
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromGenerated(inserted)
}

func (s *PostgresStore) CreateRunRequest(ctx context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	setter, err := taskRunSetter(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	auditSetter, err := auditEventSetter(audit)
	if err != nil {
		return domain.TaskRun{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskRun{}, err
	}
	defer tx.Rollback()
	inserted, err := bobmodels.TaskRuns.Insert(setter).One(ctx, tx.generated())
	if err != nil {
		return domain.TaskRun{}, err
	}
	run, err = taskRunFromGenerated(inserted)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx.generated(), log); err != nil {
		return domain.TaskRun{}, err
	}
	if approval != nil {
		if _, err := bobmodels.Approvals.Insert(approvalSetter(*approval)).One(ctx, tx.generated()); err != nil {
			return domain.TaskRun{}, err
		}
	}
	if _, err := bobmodels.AuditEvents.Insert(auditSetter).One(ctx, tx.generated()); err != nil {
		return domain.TaskRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) UpdateRunStatus(ctx context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	finished := nullTime(finishedAt)
	updated, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{Status: &status, FinishedAt: &finished}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(id))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromGenerated(updated)
}

func (s *PostgresStore) UpdateRunWorkflowState(ctx context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	raw, err := json.Marshal(workflowState)
	if err != nil {
		return domain.TaskRun{}, err
	}
	workflowStateJSON := bobtypes.NewJSON(json.RawMessage(raw))
	updated, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{WorkflowState: &workflowStateJSON}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(id))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromGenerated(updated)
}

func (s *PostgresStore) CreateRunLog(ctx context.Context, log domain.RunLog) error {
	_, err := insertRunLogWithSequence(ctx, s.db.generated(), log)
	return err
}

func (s *PostgresStore) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	rows, err := bobmodels.Runners.Query(
		sm.OrderBy(bobmodels.Runners.Columns.Name).Asc(),
	).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	runners := make([]domain.Runner, 0, len(rows))
	for _, row := range rows {
		runners = append(runners, runnerFromGenerated(row))
	}
	return runners, nil
}

func (s *PostgresStore) RegisterRunner(ctx context.Context, runner domain.Runner) (domain.Runner, error) {
	upserted, err := bobmodels.Runners.Insert(runnerSetter(runner),
		im.OnConflict(psql.Quote("id")).DoUpdate(
			im.SetExcluded("name", "tags", "capabilities", "status", "last_heartbeat_at"),
			im.SetCol("token_hash").To(psql.Raw("CASE WHEN excluded.token_hash = '' THEN runners.token_hash ELSE excluded.token_hash END")),
		),
	).One(ctx, s.db.generated())
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromGenerated(upserted), nil
}

func (s *PostgresStore) UpdateRunnerToken(ctx context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	updated, err := bobmodels.Runners.Update(
		bobmodels.RunnerSetter{TokenHash: &tokenHash, Status: &status, LastHeartbeatAt: &updatedAt}.UpdateMod(),
		um.Where(bobmodels.Runners.Columns.ID.EQ(psql.Arg(runnerID))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromGenerated(updated), nil
}

func (s *PostgresStore) GetRunnerByTokenHash(ctx context.Context, tokenHash string) (domain.Runner, error) {
	runner, err := bobmodels.Runners.Query(
		sm.Where(bobmodels.Runners.Columns.TokenHash.EQ(psql.Arg(tokenHash))),
		sm.Where(bobmodels.Runners.Columns.TokenHash.NE(psql.Arg(""))),
		sm.Where(bobmodels.Runners.Columns.Status.EQ(psql.Arg("active"))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromGenerated(runner), nil
}

func (s *PostgresStore) HeartbeatRunner(ctx context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	status := "active"
	runner, err := bobmodels.Runners.Update(
		bobmodels.RunnerSetter{Status: &status, LastHeartbeatAt: &heartbeatAt}.UpdateMod(),
		um.Where(bobmodels.Runners.Columns.ID.EQ(psql.Arg(id))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromGenerated(runner), nil
}

func (s *PostgresStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	defer tx.Rollback()
	if _, err := bobqueries.ExpireLeasesAndRequeueRuns(now).All(ctx, tx.generated()); err != nil {
		return domain.ClaimedRun{}, err
	}
	if _, err := bobqueries.MarkStaleRunners(now.Add(-2*ttl)).All(ctx, tx.generated()); err != nil {
		return domain.ClaimedRun{}, err
	}
	runnerRow, err := bobmodels.Runners.Query(
		sm.Where(bobmodels.Runners.Columns.ID.EQ(psql.Arg(runnerID))),
		sm.Where(bobmodels.Runners.Columns.Status.EQ(psql.Arg("active"))),
		sm.Where(bobmodels.Runners.Columns.LastHeartbeatAt.GTE(psql.Arg(now.Add(-2*ttl)))),
	).One(ctx, tx.generated())
	if err == sql.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	runner := runnerFromGenerated(runnerRow)
	rows, err := bobmodels.TaskRuns.Query(
		sm.Where(bobmodels.TaskRuns.Columns.Status.EQ(psql.Arg("queued"))),
		sm.OrderBy(bobmodels.TaskRuns.Columns.StartedAt).Asc(),
		sm.ForUpdate(),
	).All(ctx, tx.generated())
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	var claimedRun *domain.TaskRun
	for _, row := range rows {
		run, err := taskRunFromGenerated(row)
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		if !covers(runner.Tags, run.RunnerTags) || !contains(runner.Capabilities, claimRunType(run)) {
			continue
		}
		claimedRun = &run
		break
	}
	if claimedRun == nil {
		return domain.ClaimedRun{}, ErrNotFound
	}
	runStatus := domain.RunRunning
	runRunnerID := sql.Null[string]{V: runner.ID, Valid: true}
	if _, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{Status: &runStatus, RunnerID: &runRunnerID}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(claimedRun.ID))),
	).One(ctx, tx.generated()); err != nil {
		return domain.ClaimedRun{}, err
	}
	claimedRun.Status = domain.RunRunning
	claimedRun.RunnerID = &runner.ID
	lease := domain.RunLease{ID: leaseIDForRun(claimedRun.ID, now), RunID: claimedRun.ID, RunnerID: runner.ID, Status: domain.LeaseActive, ExpiresAt: now.Add(ttl), CreatedAt: now}
	insertedLease, err := bobmodels.RunLeases.Insert(leaseSetter(lease)).One(ctx, tx.generated())
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	lease = leaseFromGenerated(insertedLease)
	if err := tx.Commit(); err != nil {
		return domain.ClaimedRun{}, err
	}
	return domain.ClaimedRun{Lease: lease, Run: *claimedRun, PrimitivePlan: primitivePlanForRun(*claimedRun)}, nil
}

func (s *PostgresStore) CompleteLease(ctx context.Context, leaseID string, runnerID string, status string, completedAt time.Time) (domain.RunLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunLease{}, err
	}
	defer tx.Rollback()
	completedAtValue := sql.Null[time.Time]{V: completedAt, Valid: true}
	updatedLease, err := bobmodels.RunLeases.Update(
		bobmodels.RunLeaseSetter{Status: &status, CompletedAt: &completedAtValue}.UpdateMod(),
		um.Where(bobmodels.RunLeases.Columns.ID.EQ(psql.Arg(leaseID))),
		um.Where(bobmodels.RunLeases.Columns.RunnerID.EQ(psql.Arg(runnerID))),
		um.Where(bobmodels.RunLeases.Columns.Status.EQ(psql.Arg("active"))),
	).One(ctx, tx.generated())
	if err == sql.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	lease := leaseFromGenerated(updatedLease)
	finishedAtValue := sql.Null[time.Time]{V: completedAt, Valid: true}
	if _, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{Status: &status, FinishedAt: &finishedAtValue}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(lease.RunID))),
	).One(ctx, tx.generated()); err != nil {
		return domain.RunLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunLease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) CompleteLeaseRequest(ctx context.Context, leaseID string, runnerID string, status string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	auditSetter, err := auditEventSetter(audit)
	if err != nil {
		return domain.RunLease{}, err
	}
	var workflowStateJSON bobtypes.JSON[json.RawMessage]
	if workflowState != nil {
		workflowStateRaw, err := json.Marshal(workflowState)
		if err != nil {
			return domain.RunLease{}, err
		}
		workflowStateJSON = bobtypes.NewJSON(json.RawMessage(workflowStateRaw))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunLease{}, err
	}
	defer tx.Rollback()
	completedAtValue := sql.Null[time.Time]{V: completedAt, Valid: true}
	updatedLease, err := bobmodels.RunLeases.Update(
		bobmodels.RunLeaseSetter{Status: &status, CompletedAt: &completedAtValue}.UpdateMod(),
		um.Where(bobmodels.RunLeases.Columns.ID.EQ(psql.Arg(leaseID))),
		um.Where(bobmodels.RunLeases.Columns.RunnerID.EQ(psql.Arg(runnerID))),
		um.Where(bobmodels.RunLeases.Columns.Status.EQ(psql.Arg("active"))),
	).One(ctx, tx.generated())
	if err == sql.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	lease := leaseFromGenerated(updatedLease)
	runSetter := bobmodels.TaskRunSetter{Status: &runStatus}
	finishedAtValue := nullTime(finishedAt)
	runSetter.FinishedAt = &finishedAtValue
	if workflowState != nil {
		runSetter.WorkflowState = &workflowStateJSON
	}
	if _, err := bobmodels.TaskRuns.Update(
		runSetter.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(lease.RunID))),
	).One(ctx, tx.generated()); err != nil {
		return domain.RunLease{}, err
	}
	for _, log := range logs {
		if _, err := insertRunLogWithSequence(ctx, tx.generated(), log); err != nil {
			return domain.RunLease{}, err
		}
	}
	if _, err := bobmodels.AuditEvents.Insert(auditSetter).One(ctx, tx.generated()); err != nil {
		return domain.RunLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunLease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) ActiveLeaseForRun(ctx context.Context, runID string) (domain.RunLease, error) {
	lease, err := bobmodels.RunLeases.Query(
		sm.Where(bobmodels.RunLeases.Columns.RunID.EQ(psql.Arg(runID))),
		sm.Where(bobmodels.RunLeases.Columns.Status.EQ(psql.Arg("active"))),
		sm.OrderBy(bobmodels.RunLeases.Columns.CreatedAt).Desc(),
		sm.Limit(1),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return leaseFromGenerated(lease), nil
}

func (s *PostgresStore) GetLeaseForRunner(ctx context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	lease, err := bobmodels.RunLeases.Query(
		sm.Where(bobmodels.RunLeases.Columns.ID.EQ(psql.Arg(leaseID))),
		sm.Where(bobmodels.RunLeases.Columns.RunnerID.EQ(psql.Arg(runnerID))),
	).One(ctx, s.db.generated())
	if err == sql.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return leaseFromGenerated(lease), nil
}

func (s *PostgresStore) ListRunLogs(ctx context.Context, runID string) ([]domain.RunLog, error) {
	result, err := s.ListRunLogsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.RunLogs.Columns.RunID).Asc(),
		sm.OrderBy(bobmodels.RunLogs.Columns.Sequence).Asc(),
	}
	if runID != "" {
		mods = append(mods, sm.Where(bobmodels.RunLogs.Columns.RunID.EQ(psql.Arg(runID))))
	}
	total64, err := bobmodels.RunLogs.Query(mods...).Count(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	if page.Enabled {
		mods = append(mods, sm.Limit(limit), sm.Offset(offset))
	}
	rows, err := bobmodels.RunLogs.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	logs := make([]domain.RunLog, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, runLogFromGenerated(row))
	}
	return PageResult[domain.RunLog]{Items: logs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) ListArtifacts(ctx context.Context, runID string) ([]domain.ArtifactRecord, error) {
	result, err := s.ListArtifactsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.RunArtifacts.Columns.CreatedAt).Asc(),
		sm.OrderBy(bobmodels.RunArtifacts.Columns.Name).Asc(),
	}
	if strings.TrimSpace(runID) != "" {
		mods = append(mods, sm.Where(bobmodels.RunArtifacts.Columns.RunID.EQ(psql.Arg(runID))))
	}
	total64, err := bobmodels.RunArtifacts.Query(mods...).Count(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	if page.Enabled {
		mods = append(mods, sm.Limit(limit), sm.Offset(offset))
	}
	rows, err := bobmodels.RunArtifacts.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	artifacts := make([]domain.ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		artifacts = append(artifacts, artifactFromGenerated(row))
	}
	return PageResult[domain.ArtifactRecord]{Items: artifacts, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateArtifact(ctx context.Context, artifact domain.ArtifactRecord) error {
	_, err := bobmodels.RunArtifacts.Insert(artifactSetter(artifact)).One(ctx, s.db.generated())
	return err
}

func (s *PostgresStore) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.Approvals.Columns.CreatedAt).Desc(),
	}
	if status != "" {
		mods = append(mods, sm.Where(bobmodels.Approvals.Columns.Status.EQ(psql.Arg(status))))
	}
	rows, err := bobmodels.Approvals.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return nil, err
	}
	approvals := make([]domain.Approval, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, approvalFromGenerated(row))
	}
	return approvals, nil
}

func (s *PostgresStore) CreateApproval(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	inserted, err := bobmodels.Approvals.Insert(approvalSetter(approval)).One(ctx, s.db.generated())
	if err != nil {
		return domain.Approval{}, err
	}
	return approvalFromGenerated(inserted), nil
}

func (s *PostgresStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback()
	status := "approved"
	pending := "pending"
	approvedBy := sql.Null[string]{V: actorID, Valid: true}
	approvedAtValue := sql.Null[time.Time]{V: approvedAt, Valid: true}
	updated, err := bobmodels.Approvals.Update(
		bobmodels.ApprovalSetter{Status: &status, ApprovedBy: &approvedBy, ApprovedAt: &approvedAtValue}.UpdateMod(),
		um.Where(bobmodels.Approvals.Columns.RunID.EQ(psql.Arg(runID))),
		um.Where(bobmodels.Approvals.Columns.Status.EQ(psql.Arg(pending))),
	).One(ctx, tx.generated())
	if err == sql.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	runStatus := "queued"
	if _, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{Status: &runStatus}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(runID))),
	).One(ctx, tx.generated()); err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromGenerated(updated), nil
}

func (s *PostgresStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback()
	status := "rejected"
	pending := "pending"
	approvedBy := sql.Null[string]{V: actorID, Valid: true}
	approvedAt := sql.Null[time.Time]{V: rejectedAt, Valid: true}
	updated, err := bobmodels.Approvals.Update(
		bobmodels.ApprovalSetter{Status: &status, ApprovedBy: &approvedBy, ApprovedAt: &approvedAt}.UpdateMod(),
		um.Where(bobmodels.Approvals.Columns.RunID.EQ(psql.Arg(runID))),
		um.Where(bobmodels.Approvals.Columns.Status.EQ(psql.Arg(pending))),
	).One(ctx, tx.generated())
	if err == sql.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	runStatus := "canceled"
	finishedAt := sql.Null[time.Time]{V: rejectedAt, Valid: true}
	if _, err := bobmodels.TaskRuns.Update(
		bobmodels.TaskRunSetter{Status: &runStatus, FinishedAt: &finishedAt}.UpdateMod(),
		um.Where(bobmodels.TaskRuns.Columns.ID.EQ(psql.Arg(runID))),
	).One(ctx, tx.generated()); err != nil {
		return domain.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromGenerated(updated), nil
}

func (s *PostgresStore) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	mods := []bob.Mod[*dialect.SelectQuery]{
		sm.OrderBy(bobmodels.AuditEvents.Columns.CreatedAt).Desc(),
	}
	total64, err := bobmodels.AuditEvents.Query(mods...).Count(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	if page.Enabled {
		mods = append(mods, sm.Limit(limit), sm.Offset(offset))
	}
	rows, err := bobmodels.AuditEvents.Query(mods...).All(ctx, s.db.generated())
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := auditEventFromGenerated(row)
		if err != nil {
			return PageResult[domain.AuditEvent]{}, err
		}
		events = append(events, event)
	}
	return PageResult[domain.AuditEvent]{Items: events, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	setter, err := auditEventSetter(event)
	if err != nil {
		return err
	}
	_, err = bobmodels.AuditEvents.Insert(setter).One(ctx, s.db.generated())
	return err
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
