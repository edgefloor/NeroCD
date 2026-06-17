package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nerocd/internal/domain"
)

type Page struct {
	Limit   int
	Offset  int
	Enabled bool
}

type PageResult[T any] struct {
	Items  []T
	Limit  int
	Offset int
	Total  int
}

type ProjectRepository interface {
	ListProjects(context.Context) ([]domain.Project, error)
	CreateProject(context.Context, domain.Project) (domain.Project, error)
	UpdateProject(context.Context, domain.Project) (domain.Project, error)
	ArchiveProject(context.Context, string, time.Time) (domain.Project, error)
}

type ProjectMemberRepository interface {
	ListProjectMembers(context.Context, string) ([]domain.ProjectMember, error)
	UpsertProjectMember(context.Context, domain.ProjectMember) (domain.ProjectMember, error)
}

type TemplateRepository interface {
	ListTemplates(context.Context, string) ([]domain.TaskTemplate, error)
	GetTemplate(context.Context, string) (domain.TaskTemplate, error)
	CreateTemplate(context.Context, domain.TaskTemplate) (domain.TaskTemplate, error)
	UpdateTemplate(context.Context, domain.TaskTemplate) (domain.TaskTemplate, error)
}

type SourceRepository interface {
	ListRepositories(context.Context, string) ([]domain.Repository, error)
	CreateRepository(context.Context, domain.Repository) (domain.Repository, error)
	ListAccessKeys(context.Context, string) ([]domain.AccessKey, error)
	CreateAccessKey(context.Context, domain.AccessKey) (domain.AccessKey, error)
	ListInventories(context.Context, string) ([]domain.Inventory, error)
	CreateInventory(context.Context, domain.Inventory) (domain.Inventory, error)
}

type RunRepository interface {
	ListRuns(context.Context, string) ([]domain.TaskRun, error)
	ListRunsPage(context.Context, string, Page) (PageResult[domain.TaskRun], error)
	ListRunLogs(context.Context, string) ([]domain.RunLog, error)
	ListRunLogsPage(context.Context, string, Page) (PageResult[domain.RunLog], error)
	ListArtifacts(context.Context, string) ([]domain.ArtifactRecord, error)
	ListArtifactsPage(context.Context, string, Page) (PageResult[domain.ArtifactRecord], error)
	CreateRun(context.Context, domain.TaskRun) (domain.TaskRun, error)
	CreateRunRequest(context.Context, domain.TaskRun, domain.RunLog, *domain.Approval, domain.AuditEvent) (domain.TaskRun, error)
	UpdateRunStatus(context.Context, string, string, *time.Time) (domain.TaskRun, error)
	UpdateRunWorkflowState(context.Context, string, domain.WorkflowState) (domain.TaskRun, error)
	CreateRunLog(context.Context, domain.RunLog) error
	CreateArtifact(context.Context, domain.ArtifactRecord) error
}

type RunnerRepository interface {
	ListRunners(context.Context) ([]domain.Runner, error)
	RegisterRunner(context.Context, domain.Runner) (domain.Runner, error)
	UpdateRunnerToken(context.Context, string, string, string, time.Time) (domain.Runner, error)
	GetRunnerByTokenHash(context.Context, string) (domain.Runner, error)
	HeartbeatRunner(context.Context, string, time.Time) (domain.Runner, error)
	ClaimRun(context.Context, string, time.Time, time.Duration) (domain.ClaimedRun, error)
	CompleteLease(context.Context, string, string, string, time.Time) (domain.RunLease, error)
	CompleteLeaseRequest(context.Context, string, string, string, time.Time, string, *time.Time, *domain.WorkflowState, []domain.RunLog, domain.AuditEvent) (domain.RunLease, error)
	ActiveLeaseForRun(context.Context, string) (domain.RunLease, error)
	GetLeaseForRunner(context.Context, string, string) (domain.RunLease, error)
}

type UserRepository interface {
	GetUserByEmail(context.Context, string) (domain.User, error)
}

type SessionRepository interface {
	CreateSession(context.Context, domain.Session, string) error
	GetPrincipalBySessionTokenHash(context.Context, string, time.Time) (domain.User, error)
	RevokeSessionByTokenHash(context.Context, string, time.Time) error
}

type APITokenRepository interface {
	CreateAPIToken(context.Context, domain.APIToken) (domain.APIToken, error)
	GetAPITokenByHash(context.Context, string, time.Time) (domain.APIToken, error)
	RevokeAPIToken(context.Context, string, time.Time) (domain.APIToken, error)
}

type ApprovalRepository interface {
	ListApprovals(context.Context, string) ([]domain.Approval, error)
	CreateApproval(context.Context, domain.Approval) (domain.Approval, error)
	ApproveRun(context.Context, string, string, time.Time) (domain.Approval, error)
	RejectRun(context.Context, string, string, time.Time) (domain.Approval, error)
}

type AuditRepository interface {
	ListAuditEvents(context.Context) ([]domain.AuditEvent, error)
	ListAuditEventsPage(context.Context, Page) (PageResult[domain.AuditEvent], error)
	CreateAuditEvent(context.Context, domain.AuditEvent) error
}

type MemoryStore struct {
	mu                   sync.RWMutex
	users                []domain.User
	sessions             []domain.Session
	apiTokens            []domain.APIToken
	tokenHashBySessionID map[string]string
	projects             []domain.Project
	templates            []domain.TaskTemplate
	repositories         []domain.Repository
	accessKeys           []domain.AccessKey
	inventories          []domain.Inventory
	projectMembers       []domain.ProjectMember
	runs                 []domain.TaskRun
	runners              []domain.Runner
	leases               []domain.RunLease
	logs                 []domain.RunLog
	artifacts            []domain.ArtifactRecord
	approvals            []domain.Approval
	auditEvents          []domain.AuditEvent
}

func NewMemoryStore() *MemoryStore {
	now := time.Now().UTC()
	finishedAt := now.Add(-20 * time.Minute)
	tplPlan := "tpl_plan"
	tplRotate := "tpl_rotate"
	return &MemoryStore{
		users: []domain.User{
			{ID: "usr_bootstrap", Email: "admin@example.local", Name: "Bootstrap Admin", Status: domain.UserActive, GlobalRole: domain.RoleSystemAdmin, PasswordHash: "sha256:8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918", CreatedAt: now},
			{ID: "usr_viewer", Email: "viewer@example.local", Name: "Security Viewer", Status: domain.UserActive, GlobalRole: "user", PasswordHash: "sha256:d35ca5051b82ffc326a3b0b6574a9a3161dee16b9478a199ee39cd803ce5b799", CreatedAt: now},
		},
		projects: []domain.Project{
			{ID: "proj_platform", Name: "Platform Automation", Description: "Shared infrastructure runbooks and deployments.", CreatedAt: now},
			{ID: "proj_security", Name: "Security Operations", Description: "Audited response and compliance automation.", CreatedAt: now},
		},
		templates: []domain.TaskTemplate{
			{ID: "tpl_patch", ProjectID: "proj_platform", Name: "Patch Linux Fleet", Kind: "ansible", RunSpec: domain.RunSpec{Type: "ansible", Inputs: map[string]any{"playbook": "patch.yml"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "ansible"}, Process: &domain.ProcessSpec{Command: []string{"ansible-playbook", "patch.yml"}, TimeoutSeconds: 1800}, Artifacts: []domain.ArtifactSpec{{Name: "patch-report", Path: "reports/patch.json"}}, Secrets: []domain.SecretBinding{{Name: "ansible-vault", Provider: "database", Reference: "sec_ansible_vault", Target: "env:ANSIBLE_VAULT_PASSWORD"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout runbooks", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "patch", Name: "Patch fleet", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: "ansible", Inputs: map[string]any{"playbook": "patch.yml"}}}}}, RunnerTags: []string{"linux", "prod"}, RequiresAck: true},
			{ID: "tpl_plan", ProjectID: "proj_platform", Name: "Terraform Plan", Kind: "opentofu", RunSpec: domain.RunSpec{Type: "opentofu", Inputs: map[string]any{"command": "plan"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "tofu"}, Process: &domain.ProcessSpec{Command: []string{"tofu", "plan", "-out=tfplan"}, TimeoutSeconds: 1200}, Artifacts: []domain.ArtifactSpec{{Name: "tfplan", Path: "tfplan", Required: true}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout IaC", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "plan", Name: "OpenTofu plan", DependsOn: []string{"checkout"}, RunSpec: domain.RunSpec{Type: "opentofu", Inputs: map[string]any{"command": "plan"}}}}}, RunnerTags: []string{"tofu"}, RequiresAck: false},
			{ID: "tpl_rotate", ProjectID: "proj_security", Name: "Rotate Service Tokens", Kind: "shell", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "./rotate-tokens.sh"}, Repository: &domain.RepositoryRef{ID: "repo_security_runbooks", Ref: "main", Path: "tokens"}, Process: &domain.ProcessSpec{Command: []string{"./rotate-tokens.sh"}, TimeoutSeconds: 600}, Secrets: []domain.SecretBinding{{Name: "token-admin", Provider: "database", Reference: "sec_token_admin", Target: "env:TOKEN_ADMIN"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout security runbooks", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "rotate", Name: "Rotate tokens", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "./rotate-tokens.sh"}}}}}, RunnerTags: []string{"secure"}, RequiresAck: true},
		},
		repositories: []domain.Repository{
			{ID: "repo_platform_runbooks", ProjectID: "proj_platform", Name: "Platform Runbooks", URL: "https://example.local/platform/runbooks.git", Provider: domain.ProviderGit, DefaultRef: "main", CreatedAt: now},
			{ID: "repo_security_runbooks", ProjectID: "proj_security", Name: "Security Runbooks", URL: "https://example.local/security/runbooks.git", Provider: domain.ProviderGit, DefaultRef: "main", CreatedAt: now},
		},
		accessKeys: []domain.AccessKey{
			{ID: "key_ansible_vault", ProjectID: "proj_platform", Name: "Ansible Vault", Kind: domain.AccessKeyPassword, Fingerprint: "sha256:seed-ansible-vault", CreatedAt: now},
			{ID: "key_token_admin", ProjectID: "proj_security", Name: "Token Admin", Kind: domain.AccessKeyPassword, Fingerprint: "sha256:seed-token-admin", CreatedAt: now},
		},
		inventories: []domain.Inventory{
			{ID: "inv_platform_prod", ProjectID: "proj_platform", Name: "Platform Production", Kind: domain.InventoryStatic, Source: "inventories/prod.ini", CreatedAt: now},
			{ID: "inv_security_response", ProjectID: "proj_security", Name: "Security Response", Kind: domain.InventoryStatic, Source: "inventories/response.ini", CreatedAt: now},
		},
		projectMembers: []domain.ProjectMember{
			{ID: "pm_proj_platform_usr_bootstrap", ProjectID: "proj_platform", UserID: "usr_bootstrap", Email: "admin@example.local", Name: "Bootstrap Admin", Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now},
			{ID: "pm_proj_security_usr_bootstrap", ProjectID: "proj_security", UserID: "usr_bootstrap", Email: "admin@example.local", Name: "Bootstrap Admin", Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now},
			{ID: "pm_proj_security_usr_viewer", ProjectID: "proj_security", UserID: "usr_viewer", Email: "viewer@example.local", Name: "Security Viewer", Role: domain.RoleViewer, CreatedAt: now, UpdatedAt: now},
		},
		runs: []domain.TaskRun{
			{ID: "run_001", ProjectID: "proj_platform", TemplateID: &tplPlan, RunSpec: domain.RunSpec{Type: domain.RunTypeOpenTofu, Inputs: map[string]any{"command": "plan"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "tofu"}, Process: &domain.ProcessSpec{Command: []string{"tofu", "plan", "-out=tfplan"}, TimeoutSeconds: 1200}, Artifacts: []domain.ArtifactSpec{{Name: "tfplan", Path: "tfplan", Required: true}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout IaC", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "git checkout"}}}, {ID: "plan", Name: "OpenTofu plan", DependsOn: []string{"checkout"}, RunSpec: domain.RunSpec{Type: domain.RunTypeOpenTofu, Inputs: map[string]any{"command": "plan"}}}}}, RunnerTags: []string{"tofu"}, Status: domain.RunSucceeded, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-22 * time.Minute), FinishedAt: &finishedAt},
			{ID: "run_002", ProjectID: "proj_security", TemplateID: &tplRotate, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "./rotate-tokens.sh"}, Repository: &domain.RepositoryRef{ID: "repo_security_runbooks", Ref: "main", Path: "tokens"}, Process: &domain.ProcessSpec{Command: []string{"./rotate-tokens.sh"}, TimeoutSeconds: 600}, Secrets: []domain.SecretBinding{{Name: "token-admin", Provider: "database", Reference: "sec_token_admin", Target: "env:TOKEN_ADMIN"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout security runbooks", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "git checkout"}}}, {ID: "rotate", Name: "Rotate tokens", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "./rotate-tokens.sh"}}}}}, RunnerTags: []string{"secure"}, Status: domain.RunWaitingApproval, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-5 * time.Minute)},
		},
		approvals: []domain.Approval{
			{ID: "apr_002", RunID: "run_002", Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: now.Add(-5 * time.Minute)},
		},
		tokenHashBySessionID: map[string]string{},
		logs: []domain.RunLog{
			{ID: "log_001", RunID: "run_001", Sequence: 1, Stream: domain.LogStdout, Message: "Initializing OpenTofu working directory", CreatedAt: now.Add(-22 * time.Minute)},
			{ID: "log_002", RunID: "run_001", Sequence: 2, Stream: domain.LogStdout, Message: "Plan completed with no destructive changes", CreatedAt: now.Add(-21 * time.Minute)},
			{ID: "log_003", RunID: "run_002", Sequence: 1, Stream: domain.LogSystem, Message: "Run is waiting for an authorized approval", CreatedAt: now.Add(-5 * time.Minute)},
		},
	}
}

func (s *MemoryStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.users {
		if user.Email == email {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) CreateSession(_ context.Context, session domain.Session, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, session)
	s.tokenHashBySessionID[session.ID] = tokenHash
	return nil
}

func (s *MemoryStore) GetPrincipalBySessionTokenHash(_ context.Context, tokenHash string, now time.Time) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] != tokenHash {
			continue
		}
		if !session.RevokedAt.IsZero() || !session.ExpiresAt.After(now) {
			return domain.User{}, ErrNotFound
		}
		for _, user := range s.users {
			if user.ID == session.UserID && user.Status == domain.UserActive {
				return user, nil
			}
		}
		return domain.User{}, ErrNotFound
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) RevokeSessionByTokenHash(_ context.Context, tokenHash string, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] == tokenHash {
			session.RevokedAt = revokedAt
			s.sessions[i] = session
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) CreateAPIToken(_ context.Context, token domain.APIToken) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiTokens = append(s.apiTokens, token)
	return token, nil
}

func (s *MemoryStore) GetAPITokenByHash(_ context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.TokenHash != tokenHash || token.Status != domain.TokenActive || token.RevokedAt != nil {
			continue
		}
		if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
			return domain.APIToken{}, ErrNotFound
		}
		token.LastUsedAt = &now
		s.apiTokens[i] = token
		return token, nil
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) RevokeAPIToken(_ context.Context, tokenID string, revokedAt time.Time) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.ID == tokenID && token.RevokedAt == nil {
			token.Status = domain.TokenRevoked
			token.RevokedAt = &revokedAt
			s.apiTokens[i] = token
			return token, nil
		}
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) ListProjects(context.Context) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Project, 0, len(s.projects))
	for _, project := range s.projects {
		if project.ArchivedAt == nil {
			out = append(out, project)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = append(s.projects, project)
	return project, nil
}

func (s *MemoryStore) UpdateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID {
			project.CreatedAt = existing.CreatedAt
			project.ArchivedAt = existing.ArchivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ArchiveProject(_ context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ListProjectMembers(_ context.Context, projectID string) ([]domain.ProjectMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ProjectMember, 0, len(s.projectMembers))
	for _, member := range s.projectMembers {
		if projectID == "" || member.ProjectID == projectID {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpsertProjectMember(_ context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID = existing.ID
			member.CreatedAt = existing.CreatedAt
			s.projectMembers[i] = member
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	return member, nil
}

func (s *MemoryStore) ListTemplates(_ context.Context, projectID string) ([]domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskTemplate, 0, len(s.templates))
	for _, template := range s.templates {
		if projectID == "" || template.ProjectID == projectID {
			out = append(out, template)
		}
	}
	return out, nil
}

func (s *MemoryStore) GetTemplate(_ context.Context, id string) (domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, template := range s.templates {
		if template.ID == id {
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}

func (s *MemoryStore) CreateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	return template, nil
}

func (s *MemoryStore) UpdateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.templates {
		if s.templates[i].ID == template.ID {
			s.templates[i] = template
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}

func (s *MemoryStore) ListRepositories(_ context.Context, projectID string) ([]domain.Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Repository, 0, len(s.repositories))
	for _, repository := range s.repositories {
		if projectID == "" || repository.ProjectID == projectID {
			out = append(out, repository)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateRepository(_ context.Context, repository domain.Repository) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = append(s.repositories, repository)
	return repository, nil
}

func (s *MemoryStore) ListAccessKeys(_ context.Context, projectID string) ([]domain.AccessKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AccessKey, 0, len(s.accessKeys))
	for _, key := range s.accessKeys {
		if projectID == "" || key.ProjectID == projectID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateAccessKey(_ context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKeys = append(s.accessKeys, key)
	return key, nil
}

func (s *MemoryStore) ListInventories(_ context.Context, projectID string) ([]domain.Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Inventory, 0, len(s.inventories))
	for _, inventory := range s.inventories {
		if projectID == "" || inventory.ProjectID == projectID {
			out = append(out, inventory)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateInventory(_ context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventories = append(s.inventories, inventory)
	return inventory, nil
}

func (s *MemoryStore) ListRuns(_ context.Context, projectID string) ([]domain.TaskRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskRun, 0, len(s.runs))
	for _, run := range s.runs {
		if projectID == "" || run.ProjectID == projectID {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	runs, err := s.ListRuns(ctx, projectID)
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	return paginateSlice(runs, page), nil
}

func (s *MemoryStore) ListRunLogs(_ context.Context, runID string) ([]domain.RunLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.RunLog, 0, len(s.logs))
	for _, log := range s.logs {
		if runID == "" || log.RunID == runID {
			out = append(out, log)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	logs, err := s.ListRunLogs(ctx, runID)
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	return paginateSlice(logs, page), nil
}

func (s *MemoryStore) ListArtifacts(_ context.Context, runID string) ([]domain.ArtifactRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ArtifactRecord, 0, len(s.artifacts))
	for _, artifact := range s.artifacts {
		if runID == "" || artifact.RunID == runID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	artifacts, err := s.ListArtifacts(ctx, runID)
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	return paginateSlice(artifacts, page), nil
}

func (s *MemoryStore) CreateRun(_ context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	return run, nil
}

func (s *MemoryStore) CreateRunRequest(_ context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	s.logs = append(s.logs, log)
	if approval != nil {
		s.approvals = append([]domain.Approval{*approval}, s.approvals...)
	}
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return run, nil
}

func (s *MemoryStore) UpdateRunStatus(_ context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, run := range s.runs {
		if run.ID == id {
			run.Status = status
			run.FinishedAt = finishedAt
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) UpdateRunWorkflowState(_ context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, run := range s.runs {
		if run.ID == id {
			run.WorkflowState = workflowState
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) ListRunners(context.Context) ([]domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Runner, len(s.runners))
	copy(out, s.runners)
	return out, nil
}

func (s *MemoryStore) RegisterRunner(_ context.Context, runner domain.Runner) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runners {
		if s.runners[i].ID == runner.ID {
			runner.RegisteredAt = s.runners[i].RegisteredAt
			if runner.TokenHash == "" {
				runner.TokenHash = s.runners[i].TokenHash
			}
			s.runners[i] = runner
			return runner, nil
		}
	}
	s.runners = append(s.runners, runner)
	return runner, nil
}

func (s *MemoryStore) UpdateRunnerToken(_ context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID != runnerID {
			continue
		}
		runner.TokenHash = tokenHash
		runner.Status = status
		runner.LastHeartbeatAt = updatedAt
		s.runners[i] = runner
		return runner, nil
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) GetRunnerByTokenHash(_ context.Context, tokenHash string) (domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, runner := range s.runners {
		if runner.TokenHash == tokenHash && runner.Status == domain.RunnerActive {
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) HeartbeatRunner(_ context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID == id {
			runner.Status = domain.RunnerActive
			runner.LastHeartbeatAt = heartbeatAt
			s.runners[i] = runner
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) ClaimRun(_ context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLeasesLocked(now)
	staleBefore := now.Add(-2 * ttl)
	for i, runner := range s.runners {
		if runner.Status == domain.RunnerActive && runner.LastHeartbeatAt.Before(staleBefore) {
			runner.Status = domain.RunnerStale
			s.runners[i] = runner
		}
	}
	var runner domain.Runner
	foundRunner := false
	for _, candidate := range s.runners {
		if candidate.ID == runnerID {
			runner = candidate
			foundRunner = true
			break
		}
	}
	if !foundRunner || runner.Status != domain.RunnerActive || runner.LastHeartbeatAt.Before(now.Add(-2*ttl)) {
		return domain.ClaimedRun{}, ErrNotFound
	}
	for i, run := range s.runs {
		if run.Status != domain.RunQueued || !covers(runner.Tags, run.RunnerTags) || !contains(runner.Capabilities, claimRunType(run)) {
			continue
		}
		run.Status = domain.RunRunning
		run.RunnerID = &runner.ID
		s.runs[i] = run
		lease := domain.RunLease{ID: leaseIDForRun(run.ID, now), RunID: run.ID, RunnerID: runner.ID, Status: domain.LeaseActive, ExpiresAt: now.Add(ttl), CreatedAt: now}
		s.leases = append(s.leases, lease)
		return domain.ClaimedRun{Lease: lease, Run: run, PrimitivePlan: primitivePlanForRun(run)}, nil
	}
	return domain.ClaimedRun{}, ErrNotFound
}

func (s *MemoryStore) expireLeasesLocked(now time.Time) {
	for i, lease := range s.leases {
		if lease.Status != domain.LeaseActive || lease.ExpiresAt.After(now) {
			continue
		}
		lease.Status = domain.LeaseExpired
		lease.CompletedAt = &now
		s.leases[i] = lease
		for j, run := range s.runs {
			if run.ID == lease.RunID && run.Status == domain.RunRunning {
				run.Status = domain.RunQueued
				run.RunnerID = nil
				run.FinishedAt = nil
				s.runs[j] = run
				break
			}
		}
	}
}

func (s *MemoryStore) CompleteLease(_ context.Context, leaseID string, runnerID string, status string, completedAt time.Time) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Status != domain.LeaseActive {
			continue
		}
		lease.Status = status
		lease.CompletedAt = &completedAt
		s.leases[i] = lease
		for j, run := range s.runs {
			if run.ID == lease.RunID {
				run.Status = status
				run.FinishedAt = &completedAt
				s.runs[j] = run
				break
			}
		}
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) CompleteLeaseRequest(_ context.Context, leaseID string, runnerID string, status string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Status != domain.LeaseActive {
			continue
		}
		lease.Status = status
		lease.CompletedAt = &completedAt
		s.leases[i] = lease
		for j, run := range s.runs {
			if run.ID != lease.RunID {
				continue
			}
			run.Status = runStatus
			run.FinishedAt = finishedAt
			if workflowState != nil {
				run.WorkflowState = *workflowState
			}
			s.runs[j] = run
			break
		}
		for _, log := range logs {
			s.createRunLogLocked(log)
		}
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) ActiveLeaseForRun(_ context.Context, runID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.RunID == runID && lease.Status == domain.LeaseActive {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) GetLeaseForRunner(_ context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunnerID == runnerID {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) CreateRunLog(_ context.Context, log domain.RunLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createRunLogLocked(log)
	return nil
}

func (s *MemoryStore) createRunLogLocked(log domain.RunLog) {
	for {
		conflict := false
		for _, existing := range s.logs {
			if existing.RunID == log.RunID && existing.Sequence == log.Sequence {
				conflict = true
				log.Sequence++
				break
			}
		}
		if !conflict {
			break
		}
	}
	s.logs = append(s.logs, log)
}

func (s *MemoryStore) CreateArtifact(_ context.Context, artifact domain.ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func covers(values []string, required []string) bool {
	for _, item := range required {
		if !contains(values, item) {
			return false
		}
	}
	return true
}

func claimRunType(run domain.TaskRun) string {
	if len(run.Workflow.Steps) == 0 {
		return run.RunSpec.Type
	}
	statusByID := map[string]string{}
	for _, step := range run.WorkflowState.Steps {
		statusByID[step.ID] = step.Status
	}
	for _, step := range run.Workflow.Steps {
		status := statusByID[step.ID]
		if status == "" {
			status = domain.WorkflowPending
		}
		if status != domain.WorkflowPending {
			continue
		}
		ready := true
		for _, dependency := range step.DependsOn {
			if statusByID[dependency] != domain.RunSucceeded {
				ready = false
				break
			}
		}
		if ready {
			return step.RunSpec.Type
		}
	}
	return run.RunSpec.Type
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func primitivePlanForRun(run domain.TaskRun) domain.RunnerPrimitivePlan {
	plan := domain.RunnerPrimitivePlan{RunID: run.ID, Process: run.RunSpec.Process, Artifacts: run.RunSpec.Artifacts, Secrets: run.RunSpec.Secrets}
	if run.RunSpec.Repository != nil {
		dest := run.RunSpec.Repository.Path
		if dest == "" {
			dest = "workspace"
		}
		plan.Checkout = &domain.CheckoutPlan{Repository: *run.RunSpec.Repository, DestPath: dest}
	}
	return plan
}

func leaseIDForRun(runID string, now time.Time) string {
	return fmt.Sprintf("lease_%s_%d", runID, now.UnixNano())
}

func (s *MemoryStore) ListApprovals(_ context.Context, status string) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Approval, 0, len(s.approvals))
	for _, approval := range s.approvals {
		if status == "" || approval.Status == status {
			out = append(out, approval)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateApproval(_ context.Context, approval domain.Approval) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals = append(s.approvals, approval)
	return approval, nil
}

func (s *MemoryStore) ApproveRun(_ context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			approval.Status = domain.ApprovalApproved
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &approvedAt
			s.approvals[i] = approval
			for j, run := range s.runs {
				if run.ID == runID {
					run.Status = domain.RunQueued
					s.runs[j] = run
					break
				}
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}

func (s *MemoryStore) RejectRun(_ context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			approval.Status = domain.ApprovalRejected
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &rejectedAt
			s.approvals[i] = approval
			for j, run := range s.runs {
				if run.ID == runID {
					run.Status = domain.RunCanceled
					run.FinishedAt = &rejectedAt
					s.runs[j] = run
					break
				}
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}

func (s *MemoryStore) ListAuditEvents(context.Context) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AuditEvent, len(s.auditEvents))
	copy(out, s.auditEvents)
	return out, nil
}

func (s *MemoryStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	events, err := s.ListAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	return paginateSlice(events, page), nil
}

func (s *MemoryStore) CreateAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = append([]domain.AuditEvent{event}, s.auditEvents...)
	return nil
}

func paginateSlice[T any](items []T, page Page) PageResult[T] {
	total := len(items)
	limit := page.Limit
	offset := page.Offset
	if !page.Enabled {
		limit = total
		offset = 0
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
	end := offset + limit
	if end > total {
		end = total
	}
	return PageResult[T]{Items: items[offset:end], Limit: limit, Offset: offset, Total: total}
}
