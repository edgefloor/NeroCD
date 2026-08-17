package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/source"
	"nerocd/internal/store"
)

type requestIDContextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

type Service struct {
	auth      auth.Provider
	users     store.UserRepository
	sessions  store.SessionRepository
	apiTokens store.APITokenRepository
	projects  store.ProjectRepository
	members   store.ProjectMemberRepository
	templates store.TemplateRepository
	sources   store.SourceRepository
	runs      store.RunRepository
	runners   store.RunnerRepository
	approvals store.ApprovalRepository
	audit     store.AuditRepository
	registry  runner.Registry
	leaseTTL  time.Duration
}

func NewService(authProvider auth.Provider, users store.UserRepository, sessions store.SessionRepository, apiTokens store.APITokenRepository, projects store.ProjectRepository, members store.ProjectMemberRepository, templates store.TemplateRepository, sources store.SourceRepository, runs store.RunRepository, runners store.RunnerRepository, approvals store.ApprovalRepository, audit store.AuditRepository) *Service {
	return &Service{auth: authProvider, users: users, sessions: sessions, apiTokens: apiTokens, projects: projects, members: members, templates: templates, sources: sources, runs: runs, runners: runners, approvals: approvals, audit: audit, registry: runner.NewRegistry(), leaseTTL: 2 * time.Minute}
}

// SetLeaseTTL sets a bounded authority lifetime for controlled test and deployment environments.
func (s *Service) SetLeaseTTL(ttl time.Duration) error {
	if ttl < 5*time.Second || ttl > 10*time.Minute {
		return errors.New("lease TTL must be between 5s and 10m")
	}
	s.leaseTTL = ttl
	return nil
}

func (s *Service) CurrentPrincipal(ctx context.Context) (auth.Principal, error) {
	return s.auth.CurrentPrincipal(ctx)
}

func (s *Service) Ready(ctx context.Context) error {
	if _, err := s.projects.ListProjects(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) AuthenticateSessionToken(ctx context.Context, token string) (auth.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	user, err := s.sessions.GetPrincipalBySessionTokenHash(ctx, sessionTokenHash(token), time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return s.authenticateAPIToken(ctx, token)
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       user.ID,
		Email:    user.Email,
		Name:     user.Name,
		Roles:    globalPrincipalRoles(user.GlobalRole),
		Provider: domain.PrincipalLocal,
	}, nil
}

func (s *Service) authenticateAPIToken(ctx context.Context, token string) (auth.Principal, error) {
	apiToken, err := s.apiTokens.GetAPITokenByHash(ctx, apiTokenHash(token), time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       apiToken.ID,
		Email:    "",
		Name:     apiToken.Name,
		Roles:    apiToken.Roles,
		Provider: domain.PrincipalAPIToken,
	}, nil
}

type CreatedAPIToken struct {
	APIToken domain.APIToken `json:"api_token"`
	Token    string          `json:"token"`
}

type APITokenInput struct {
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Roles     []string   `json:"roles"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type RevokeAPITokenInput struct {
	TokenID string `json:"token_id"`
}

func (s *Service) CreateAPIToken(ctx context.Context, input APITokenInput) (CreatedAPIToken, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return CreatedAPIToken{}, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "api_token.create.denied", "api_tokens", map[string]any{"name": input.Name, "roles": input.Roles})
		return CreatedAPIToken{}, auth.ErrForbidden
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreatedAPIToken{}, errors.New("name is required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = domain.TokenKindServiceAccount
	}
	if kind != domain.TokenKindServiceAccount && kind != domain.TokenKindBootstrap {
		return CreatedAPIToken{}, errors.New("api token kind is invalid")
	}
	roles := normalizeAPITokenRoles(input.Roles)
	if len(roles) == 0 {
		return CreatedAPIToken{}, errors.New("roles are required")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		return CreatedAPIToken{}, errors.New("expires_at must be in the future")
	}
	token, tokenHash, err := newAPITokenSecret()
	if err != nil {
		return CreatedAPIToken{}, err
	}
	id, err := prefixedID("pat")
	if err != nil {
		return CreatedAPIToken{}, err
	}
	now := time.Now().UTC()
	apiToken, err := s.apiTokens.CreateAPIToken(ctx, domain.APIToken{ID: id, Name: name, Kind: kind, TokenHash: tokenHash, Roles: roles, Status: domain.TokenActive, CreatedBy: principal.ID, CreatedAt: now, ExpiresAt: input.ExpiresAt})
	if err != nil {
		return CreatedAPIToken{}, err
	}
	return CreatedAPIToken{APIToken: apiToken, Token: token}, s.writeAudit(ctx, principal.ID, "api_token.create", apiToken.ID, map[string]any{"name": apiToken.Name, "kind": apiToken.Kind, "roles": apiToken.Roles, "expires_at": apiToken.ExpiresAt})
}

func (s *Service) RevokeAPIToken(ctx context.Context, input RevokeAPITokenInput) (domain.APIToken, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.APIToken{}, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "api_token.revoke.denied", strings.TrimSpace(input.TokenID), nil)
		return domain.APIToken{}, auth.ErrForbidden
	}
	tokenID := strings.TrimSpace(input.TokenID)
	if tokenID == "" {
		return domain.APIToken{}, errors.New("token_id is required")
	}
	apiToken, err := s.apiTokens.RevokeAPIToken(ctx, tokenID, time.Now().UTC())
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiToken, s.writeAudit(ctx, principal.ID, "api_token.revoke", apiToken.ID, map[string]any{"name": apiToken.Name})
}

func (s *Service) ListProjects(ctx context.Context) ([]domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return projects, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Project, 0, len(projects))
	for _, project := range projects {
		if _, ok := allowed[project.ID]; ok {
			out = append(out, project)
		}
	}
	return out, nil
}

func (s *Service) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.members.ListProjectMembers(ctx, projectID)
	}
	members, err := s.members.ListProjectMembers(ctx, "")
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return members, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ProjectMember, 0, len(members))
	for _, member := range members {
		if _, ok := allowed[member.ProjectID]; ok {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *Service) ProjectRole(ctx context.Context, projectID string) (domain.ProjectRole, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.ProjectRole{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.ProjectRole{}, errors.New("project_id is required")
	}
	if isSystemAdmin(principal) {
		return projectRole(projectID, domain.RoleSystemAdmin), nil
	}
	members, err := s.members.ListProjectMembers(ctx, projectID)
	if err != nil {
		return domain.ProjectRole{}, err
	}
	for _, member := range members {
		if member.UserID == principal.ID {
			return projectRole(projectID, member.Role), nil
		}
	}
	return domain.ProjectRole{}, auth.ErrForbidden
}

func (s *Service) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.templates.ListTemplates(ctx, projectID)
	}
	templates, err := s.templates.ListTemplates(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterTemplatesForPrincipal(ctx, principal, templates)
}

func (s *Service) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListRepositories(ctx, projectID)
	}
	repositories, err := s.sources.ListRepositories(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterRepositoriesForPrincipal(ctx, principal, repositories)
}

func (s *Service) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListAccessKeys(ctx, projectID)
	}
	keys, err := s.sources.ListAccessKeys(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterAccessKeysForPrincipal(ctx, principal, keys)
}

func (s *Service) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListInventories(ctx, projectID)
	}
	inventories, err := s.sources.ListInventories(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterInventoriesForPrincipal(ctx, principal, inventories)
}

func (s *Service) ListRuns(ctx context.Context, projectID string) ([]domain.TaskRun, error) {
	result, err := s.ListRunsPage(ctx, projectID, store.Page{})
	return result.Items, err
}

func (s *Service) ListRunsPage(ctx context.Context, projectID string, page store.Page) (store.PageResult[domain.TaskRun], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.TaskRun]{}, err
		}
		return s.runs.ListRunsPage(ctx, projectID, page)
	}
	runs, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	filtered, err := s.filterRunsForPrincipal(ctx, principal, runs)
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	return paginateForService(filtered, page), nil
}

func (s *Service) ListRunLogs(ctx context.Context, runID string) ([]domain.RunLog, error) {
	result, err := s.ListRunLogsPage(ctx, runID, store.Page{})
	return result.Items, err
}

func (s *Service) ListRunLogsPage(ctx context.Context, runID string, page store.Page) (store.PageResult[domain.RunLog], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, err := s.runByID(ctx, runID)
		if err != nil {
			return store.PageResult[domain.RunLog]{}, err
		}
		if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.RunLog]{}, err
		}
		return s.runs.ListRunLogsPage(ctx, runID, page)
	}
	logs, err := s.runs.ListRunLogs(ctx, "")
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	allRuns, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	runs, err := s.filterRunsForPrincipal(ctx, principal, allRuns)
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	allowedRuns := map[string]struct{}{}
	for _, run := range runs {
		allowedRuns[run.ID] = struct{}{}
	}
	out := make([]domain.RunLog, 0, len(logs))
	for _, log := range logs {
		if _, ok := allowedRuns[log.RunID]; ok {
			out = append(out, log)
		}
	}
	return paginateForService(out, page), nil
}

func (s *Service) ListArtifacts(ctx context.Context, runID string) ([]domain.ArtifactRecord, error) {
	result, err := s.ListArtifactsPage(ctx, runID, store.Page{})
	return result.Items, err
}

func (s *Service) ListArtifactsPage(ctx context.Context, runID string, page store.Page) (store.PageResult[domain.ArtifactRecord], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, err := s.runByID(ctx, runID)
		if err != nil {
			return store.PageResult[domain.ArtifactRecord]{}, err
		}
		if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.ArtifactRecord]{}, err
		}
		return s.runs.ListArtifactsPage(ctx, runID, page)
	}
	if isSystemAdmin(principal) {
		return s.runs.ListArtifactsPage(ctx, "", page)
	}
	runs, err := s.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	allowed := map[string]bool{}
	for _, run := range runs {
		allowed[run.ID] = true
	}
	artifacts, err := s.runs.ListArtifacts(ctx, "")
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	out := make([]domain.ArtifactRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		if allowed[artifact.RunID] {
			out = append(out, artifact)
		}
	}
	return paginateForService(out, page), nil
}

type RunLogInput struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	EventKey string `json:"event_key"`
}

type RunEventInput struct {
	EventKey string `json:"event_key"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
}

type RunEventBatchInput struct {
	RunID   string          `json:"run_id"`
	LeaseID string          `json:"lease_id"`
	Attempt int             `json:"attempt"`
	Fence   string          `json:"fence"`
	Events  []RunEventInput `json:"events"`
}

type RunEventBatchAck struct {
	Events []domain.RunLog `json:"events"`
}

type ArtifactInput struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
}

func (s *Service) CreateArtifact(ctx context.Context, input ArtifactInput) (domain.ArtifactRecord, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.ArtifactRecord{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	leaseID := strings.TrimSpace(input.LeaseID)
	name := strings.TrimSpace(input.Name)
	path := strings.TrimSpace(input.Path)
	if runID == "" || leaseID == "" || name == "" || path == "" {
		return domain.ArtifactRecord{}, errors.New("run_id, lease_id, name, and path are required")
	}
	if input.Attempt <= 0 || strings.TrimSpace(input.Fence) == "" {
		return domain.ArtifactRecord{}, errors.New("attempt and fence are required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = domain.ArtifactFile
	}
	artifact := domain.ArtifactRecord{ID: mustPrefixedID("art"), RunID: runID, LeaseID: leaseID, Name: name, Path: path, Found: input.Found, Required: input.Required, Size: input.Size, Kind: kind, CreatedAt: time.Now().UTC()}
	if err := s.runs.CreateArtifactForLease(ctx, artifact, principal.ID, input.Attempt, input.Fence, artifact.CreatedAt); err != nil {
		return domain.ArtifactRecord{}, err
	}
	return artifact, nil
}

func (s *Service) AppendRunLog(ctx context.Context, input RunLogInput) (domain.RunLog, error) {
	result, err := s.AppendRunEvents(ctx, RunEventBatchInput{RunID: input.RunID, LeaseID: input.LeaseID, Attempt: input.Attempt, Fence: input.Fence, Events: []RunEventInput{{EventKey: input.EventKey, Sequence: input.Sequence, Stream: input.Stream, Message: input.Message}}})
	if err != nil {
		return domain.RunLog{}, err
	}
	return result.Events[0], nil
}

func (s *Service) AppendRunEvents(ctx context.Context, input RunEventBatchInput) (RunEventBatchAck, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return RunEventBatchAck{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	leaseID := strings.TrimSpace(input.LeaseID)
	if runID == "" || leaseID == "" {
		return RunEventBatchAck{}, errors.New("run_id and lease_id are required")
	}
	if input.Attempt <= 0 || strings.TrimSpace(input.Fence) == "" {
		return RunEventBatchAck{}, errors.New("attempt and fence are required")
	}
	if len(input.Events) == 0 || len(input.Events) > 64 {
		return RunEventBatchAck{}, errors.New("events batch must contain between 1 and 64 events")
	}
	totalBytes := 0
	logs := make([]domain.RunLog, 0, len(input.Events))
	seen := make(map[string]struct{}, len(input.Events))
	now := time.Now().UTC()
	for _, event := range input.Events {
		eventKey := strings.TrimSpace(event.EventKey)
		stream := strings.TrimSpace(event.Stream)
		if eventKey == "" || event.Sequence <= 0 {
			return RunEventBatchAck{}, errors.New("event_key and positive sequence are required")
		}
		if _, duplicate := seen[eventKey]; duplicate {
			return RunEventBatchAck{}, errors.New("event_key is duplicated within batch")
		}
		seen[eventKey] = struct{}{}
		if stream != domain.LogSystem && stream != domain.LogStdout && stream != domain.LogStderr {
			return RunEventBatchAck{}, errors.New("stream must be system, stdout, or stderr")
		}
		totalBytes += len(eventKey) + len(stream) + len(event.Message)
		if totalBytes > 256*1024 {
			return RunEventBatchAck{}, errors.New("events batch exceeds 256 KiB")
		}
		logID, err := prefixedID("log")
		if err != nil {
			return RunEventBatchAck{}, err
		}
		logs = append(logs, domain.RunLog{ID: logID, RunID: runID, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: stream, Message: event.Message, CreatedAt: now, EventKey: eventKey, LeaseID: leaseID, Attempt: input.Attempt})
	}
	persisted, err := s.runs.CreateRunLogsForLease(ctx, logs, runID, principal.ID, leaseID, input.Attempt, input.Fence, now)
	if err != nil {
		return RunEventBatchAck{}, err
	}
	return RunEventBatchAck{Events: persisted}, nil
}

func (s *Service) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	return s.runners.ListRunners(ctx)
}

func (s *Service) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := s.approvals.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return approvals, nil
	}
	runs, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return nil, err
	}
	visibleRuns, err := s.filterRunsForPrincipal(ctx, principal, runs)
	if err != nil {
		return nil, err
	}
	allowedRuns := map[string]struct{}{}
	for _, run := range visibleRuns {
		allowedRuns[run.ID] = struct{}{}
	}
	out := make([]domain.Approval, 0, len(approvals))
	for _, approval := range approvals {
		if _, ok := allowedRuns[approval.RunID]; ok {
			out = append(out, approval)
		}
	}
	return out, nil
}

func (s *Service) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, store.Page{})
	return result.Items, err
}

func (s *Service) ListAuditEventsPage(ctx context.Context, page store.Page) (store.PageResult[domain.AuditEvent], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	if isSystemAdmin(principal) {
		return s.audit.ListAuditEventsPage(ctx, page)
	}
	events, err := s.audit.ListAuditEvents(ctx)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	out := make([]domain.AuditEvent, 0, len(events))
	for _, event := range events {
		if event.ActorID == principal.ID {
			out = append(out, event)
			continue
		}
		projectID, ok := s.auditProjectID(ctx, event)
		if !ok {
			continue
		}
		if _, allowedProject := allowed[projectID]; allowedProject {
			out = append(out, event)
		}
	}
	return paginateForService(out, page), nil
}

type CreatedSession struct {
	Session domain.Session `json:"session"`
	Token   string         `json:"token"`
}

func (s *Service) CreateSession(ctx context.Context, email string, password string) (CreatedSession, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return CreatedSession{}, errors.New("email is required")
	}
	if password == "" {
		return CreatedSession{}, errors.New("password is required")
	}

	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return CreatedSession{}, errors.New("active user not found")
		}
		return CreatedSession{}, err
	}
	if user.Status != domain.UserActive {
		return CreatedSession{}, errors.New("user is not active")
	}
	if !verifyPassword(password, user.PasswordHash) {
		return CreatedSession{}, errors.New("invalid credentials")
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return CreatedSession{}, err
	}
	now := time.Now().UTC()
	sessionID, err := randomHex(16)
	if err != nil {
		return CreatedSession{}, err
	}
	session := domain.Session{
		ID:        "ses_" + sessionID,
		UserID:    user.ID,
		ExpiresAt: now.Add(12 * time.Hour),
		CreatedAt: now,
	}
	if err := s.sessions.CreateSession(ctx, session, tokenHash); err != nil {
		return CreatedSession{}, err
	}
	return CreatedSession{Session: session, Token: token}, s.writeAudit(ctx, user.ID, "session.create", session.ID, map[string]any{"provider": domain.PrincipalLocal})
}

func (s *Service) RevokeSessionToken(ctx context.Context, token string) error {
	principal, err := s.AuthenticateSessionToken(ctx, token)
	if err != nil {
		return err
	}
	if err := s.sessions.RevokeSessionByTokenHash(ctx, sessionTokenHash(token), time.Now().UTC()); err != nil {
		return err
	}
	return s.writeAudit(ctx, principal.ID, "session.revoke", principal.ID, nil)
}

type ProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ProjectMemberInput struct {
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

type AccessKeyInput struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type InventoryInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
}

func (s *Service) CreateProject(ctx context.Context, input ProjectInput) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Project{}, errors.New("name is required")
	}
	now := time.Now().UTC()
	id, err := prefixedID("proj")
	if err != nil {
		return domain.Project{}, err
	}
	project, err := s.projects.CreateProject(ctx, domain.Project{ID: id, Name: name, Description: strings.TrimSpace(input.Description), CreatedAt: now})
	if err != nil {
		return domain.Project{}, err
	}
	memberID, err := prefixedID("pm")
	if err == nil {
		_, _ = s.members.UpsertProjectMember(ctx, domain.ProjectMember{ID: memberID, ProjectID: project.ID, UserID: principal.ID, Email: principal.Email, Name: principal.Name, Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now})
	}
	return project, s.writeAudit(ctx, principal.ID, "project.create", project.ID, map[string]any{"name": project.Name})
}

func (s *Service) UpsertProjectMember(ctx context.Context, input ProjectMemberInput) (domain.ProjectMember, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	email := strings.TrimSpace(strings.ToLower(input.Email))
	role := strings.TrimSpace(input.Role)
	if projectID == "" {
		return domain.ProjectMember{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleOwner); err != nil {
		return domain.ProjectMember{}, err
	}
	if email == "" {
		return domain.ProjectMember{}, errors.New("email is required")
	}
	switch role {
	case domain.RoleOwner, domain.RoleMaintainer, domain.RoleViewer:
	default:
		return domain.ProjectMember{}, errors.New("role must be owner, maintainer, or viewer")
	}
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	foundProject := false
	for _, project := range projects {
		if project.ID == projectID {
			foundProject = true
			break
		}
	}
	if !foundProject {
		return domain.ProjectMember{}, store.ErrNotFound
	}
	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	if user.Status != domain.UserActive {
		return domain.ProjectMember{}, errors.New("user is not active")
	}
	now := time.Now().UTC()
	id, err := prefixedID("pm")
	if err != nil {
		return domain.ProjectMember{}, err
	}
	member, err := s.members.UpsertProjectMember(ctx, domain.ProjectMember{ID: id, ProjectID: projectID, UserID: user.ID, Email: user.Email, Name: user.Name, Role: role, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return member, s.writeAudit(ctx, principal.ID, "project.member.upsert", member.ProjectID, map[string]any{"member_user_id": member.UserID, "role": member.Role})
}

func (s *Service) CreateAccessKey(ctx context.Context, input AccessKeyInput) (domain.AccessKey, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.AccessKey{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	kind := strings.TrimSpace(input.Kind)
	fingerprint := strings.TrimSpace(input.Fingerprint)
	if projectID == "" {
		return domain.AccessKey{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.AccessKey{}, err
	}
	if name == "" {
		return domain.AccessKey{}, errors.New("name is required")
	}
	switch kind {
	case domain.AccessKeySSH, domain.AccessKeyPassword, domain.AccessKeyToken:
	default:
		return domain.AccessKey{}, errors.New("kind must be ssh, password, or token")
	}
	if fingerprint == "" {
		return domain.AccessKey{}, errors.New("fingerprint is required")
	}
	id, err := prefixedID("key")
	if err != nil {
		return domain.AccessKey{}, err
	}
	key, err := s.sources.CreateAccessKey(ctx, domain.AccessKey{ID: id, ProjectID: projectID, Name: name, Kind: kind, Fingerprint: fingerprint, CreatedAt: time.Now().UTC()})
	if err != nil {
		return domain.AccessKey{}, err
	}
	return key, s.writeAudit(ctx, principal.ID, "access_key.create", key.ID, map[string]any{"project_id": key.ProjectID, "kind": key.Kind, "fingerprint": key.Fingerprint})
}

func (s *Service) CreateInventory(ctx context.Context, input InventoryInput) (domain.Inventory, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Inventory{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	kind := strings.TrimSpace(input.Kind)
	source := strings.TrimSpace(input.Source)
	if projectID == "" {
		return domain.Inventory{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.Inventory{}, err
	}
	if name == "" {
		return domain.Inventory{}, errors.New("name is required")
	}
	switch kind {
	case domain.InventoryStatic, domain.InventoryDynamic:
	default:
		return domain.Inventory{}, errors.New("kind must be static or dynamic")
	}
	if source == "" {
		return domain.Inventory{}, errors.New("source is required")
	}
	id, err := prefixedID("inv")
	if err != nil {
		return domain.Inventory{}, err
	}
	inventory, err := s.sources.CreateInventory(ctx, domain.Inventory{ID: id, ProjectID: projectID, Name: name, Kind: kind, Source: source, CreatedAt: time.Now().UTC()})
	if err != nil {
		return domain.Inventory{}, err
	}
	return inventory, s.writeAudit(ctx, principal.ID, "inventory.create", inventory.ID, map[string]any{"project_id": inventory.ProjectID, "kind": inventory.Kind, "source": inventory.Source})
}

func (s *Service) UpdateProject(ctx context.Context, id string, input ProjectInput) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(id), domain.RoleMaintainer); err != nil {
		return domain.Project{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Project{}, errors.New("name is required")
	}
	project, err := s.projects.UpdateProject(ctx, domain.Project{ID: id, Name: name, Description: strings.TrimSpace(input.Description)})
	if err != nil {
		return domain.Project{}, err
	}
	return project, s.writeAudit(ctx, principal.ID, "project.update", project.ID, map[string]any{"name": project.Name})
}

func (s *Service) ArchiveProject(ctx context.Context, id string) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(id), domain.RoleMaintainer); err != nil {
		return domain.Project{}, err
	}
	project, err := s.projects.ArchiveProject(ctx, id, time.Now().UTC())
	if err != nil {
		return domain.Project{}, err
	}
	return project, s.writeAudit(ctx, principal.ID, "project.archive", project.ID, nil)
}

type TemplateInput struct {
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	RunSpec     domain.RunSpec  `json:"run_spec"`
	Workflow    domain.Workflow `json:"workflow"`
	RunnerTags  []string        `json:"runner_tags"`
	RequiresAck bool            `json:"requires_ack"`
}

type RepositoryInput struct {
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`
	DefaultRef string `json:"default_ref"`
}

func (s *Service) CreateRepository(ctx context.Context, input RepositoryInput) (domain.Repository, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	url := strings.TrimSpace(input.URL)
	if projectID == "" {
		return domain.Repository{}, errors.New("project_id is required")
	}
	if name == "" {
		return domain.Repository{}, errors.New("name is required")
	}
	if url == "" {
		return domain.Repository{}, errors.New("url is required")
	}
	if err := source.ValidateRepositoryURL(url); err != nil {
		return domain.Repository{}, err
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.Repository{}, err
	}
	id, err := prefixedID("repo")
	if err != nil {
		return domain.Repository{}, err
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = domain.ProviderGit
	}
	defaultRef := strings.TrimSpace(input.DefaultRef)
	if defaultRef == "" {
		defaultRef = "main"
	}
	repository, err := s.sources.CreateRepository(ctx, domain.Repository{ID: id, ProjectID: projectID, Name: name, URL: url, Provider: provider, DefaultRef: defaultRef, CreatedAt: time.Now().UTC()})
	if err != nil {
		return domain.Repository{}, err
	}
	return repository, s.writeAudit(ctx, principal.ID, "repository.create", repository.ID, map[string]any{"project_id": repository.ProjectID, "provider": repository.Provider})
}

type RunnerInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
}

type RegisteredRunner struct {
	Runner domain.Runner `json:"runner"`
	Token  string        `json:"token"`
}

type RunnerEnrollmentInput struct {
	RunnerID     string   `json:"runner_id"`
	RunnerName   string   `json:"runner_name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
	TTLSeconds   int      `json:"ttl_seconds"`
}

type CreatedRunnerEnrollment struct {
	Enrollment domain.RunnerEnrollment `json:"enrollment"`
	Token      string                  `json:"token"`
}

type RunnerEnrollmentRevokeInput struct {
	EnrollmentID string `json:"enrollment_id"`
}

type RunnerEnrollmentConsumeInput struct {
	RequestID      string `json:"request_id"`
	CredentialHash string `json:"credential_hash"`
}

type ConsumedRunnerEnrollment struct {
	Runner domain.Runner `json:"runner"`
}

type RunnerTokenInput struct {
	RunnerID string `json:"runner_id"`
}

func (s *Service) AuthenticateRunnerToken(ctx context.Context, token string) (auth.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	runner, err := s.runners.GetRunnerByTokenHash(ctx, runnerTokenHash(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       runner.ID,
		Email:    "",
		Name:     runner.Name,
		Roles:    []string{domain.RoleRunner},
		Provider: domain.PrincipalRunner,
	}, nil
}

var runnerEnrollmentIDPattern = regexp.MustCompile(`^runner_[a-z0-9][a-z0-9_-]{2,62}$`)
var enrollmentConsumeIDPattern = regexp.MustCompile(`^enroll_consume_[0-9a-f]{32}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var enrollmentTokenPattern = regexp.MustCompile(`^nce_[0-9a-f]{64}$`)

func (s *Service) CreateRunnerEnrollment(ctx context.Context, input RunnerEnrollmentInput) (CreatedRunnerEnrollment, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	if !isRunnerAdmin(principal) {
		return CreatedRunnerEnrollment{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if !runnerEnrollmentIDPattern.MatchString(runnerID) {
		return CreatedRunnerEnrollment{}, errors.New("runner_id is invalid")
	}
	name := strings.TrimSpace(input.RunnerName)
	if name == "" || len(name) > 128 {
		return CreatedRunnerEnrollment{}, errors.New("runner_name is invalid")
	}
	tags := normalizeTags(input.Tags)
	capabilities := normalizeTags(input.Capabilities)
	if len(tags) > 32 || len(capabilities) == 0 || len(capabilities) > 32 {
		return CreatedRunnerEnrollment{}, errors.New("runner tags or capabilities are invalid")
	}
	for _, value := range append(append([]string(nil), tags...), capabilities...) {
		if len(value) > 64 {
			return CreatedRunnerEnrollment{}, errors.New("runner tags or capabilities are invalid")
		}
	}
	ttl := 10 * time.Minute
	if input.TTLSeconds != 0 {
		ttl = time.Duration(input.TTLSeconds) * time.Second
	}
	if ttl < time.Minute || ttl > time.Hour {
		return CreatedRunnerEnrollment{}, errors.New("ttl_seconds is invalid")
	}
	id, err := prefixedID("enroll")
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	token, tokenHash, err := newEnrollmentToken()
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	now := time.Now().UTC()
	audit, err := s.auditEvent(ctx, principal.ID, "runner.enrollment.create", id, map[string]any{"enrollment_id": id, "runner_id": runnerID, "expires_in_seconds": int(ttl.Seconds())})
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	enrollment, err := s.runners.CreateRunnerEnrollment(ctx, domain.RunnerEnrollment{ID: id, TokenHash: tokenHash, RunnerID: runnerID, RunnerName: name, Tags: tags, Capabilities: capabilities, CreatedBy: principal.ID, CreatedAt: now, ExpiresAt: now.Add(ttl)}, audit)
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	return CreatedRunnerEnrollment{Enrollment: enrollment, Token: token}, nil
}

func (s *Service) RevokeRunnerEnrollment(ctx context.Context, input RunnerEnrollmentRevokeInput) (domain.RunnerEnrollment, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if !isRunnerAdmin(principal) {
		return domain.RunnerEnrollment{}, auth.ErrForbidden
	}
	id := strings.TrimSpace(input.EnrollmentID)
	if id == "" {
		return domain.RunnerEnrollment{}, errors.New("enrollment_id is required")
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.enrollment.revoke", id, map[string]any{"enrollment_id": id})
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return s.runners.RevokeRunnerEnrollment(ctx, id, audit)
}

func (s *Service) ConsumeRunnerEnrollment(ctx context.Context, token string, input RunnerEnrollmentConsumeInput) (ConsumedRunnerEnrollment, error) {
	token = strings.TrimSpace(token)
	if !enrollmentTokenPattern.MatchString(token) {
		return ConsumedRunnerEnrollment{}, auth.ErrUnauthenticated
	}
	requestID := strings.TrimSpace(input.RequestID)
	credentialHash := strings.TrimSpace(input.CredentialHash)
	if !enrollmentConsumeIDPattern.MatchString(requestID) || !sha256HexPattern.MatchString(credentialHash) {
		return ConsumedRunnerEnrollment{}, errors.New("request_id or credential_hash is invalid")
	}
	auditID, err := prefixedID("aud")
	if err != nil {
		return ConsumedRunnerEnrollment{}, err
	}
	runner, err := s.runners.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: enrollmentTokenHash(token), RequestID: requestID, CredentialHash: credentialHash}, domain.AuditEvent{ID: auditID, Action: "runner.enrollment.consume", CreatedAt: time.Now().UTC()})
	if err != nil {
		return ConsumedRunnerEnrollment{}, err
	}
	return ConsumedRunnerEnrollment{Runner: runner}, nil
}

func (s *Service) RegisterRunner(ctx context.Context, input RunnerInput) (RegisteredRunner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return RegisteredRunner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.register.denied", strings.TrimSpace(input.ID), map[string]any{"name": input.Name})
		return RegisteredRunner{}, auth.ErrForbidden
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id, err = prefixedID("runner")
		if err != nil {
			return RegisteredRunner{}, err
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	capabilities := normalizeTags(input.Capabilities)
	if len(capabilities) == 0 {
		return RegisteredRunner{}, errors.New("capabilities are required")
	}
	token, tokenHash, err := newRunnerToken()
	if err != nil {
		return RegisteredRunner{}, err
	}
	now := time.Now().UTC()
	runner, err := s.runners.RegisterRunner(ctx, domain.Runner{ID: id, Name: name, Tags: normalizeTags(input.Tags), Capabilities: capabilities, TokenHash: tokenHash, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now})
	if err != nil {
		return RegisteredRunner{}, err
	}
	return RegisteredRunner{Runner: runner, Token: token}, s.writeAudit(ctx, principal.ID, "runner.register", runner.ID, map[string]any{"name": runner.Name})
}

func (s *Service) RotateRunnerToken(ctx context.Context, input RunnerTokenInput) (RegisteredRunner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return RegisteredRunner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.token.rotate.denied", strings.TrimSpace(input.RunnerID), nil)
		return RegisteredRunner{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if runnerID == "" {
		return RegisteredRunner{}, errors.New("runner_id is required")
	}
	token, tokenHash, err := newRunnerToken()
	if err != nil {
		return RegisteredRunner{}, err
	}
	runner, err := s.runners.UpdateRunnerToken(ctx, runnerID, tokenHash, domain.RunnerActive, time.Now().UTC())
	if err != nil {
		return RegisteredRunner{}, err
	}
	return RegisteredRunner{Runner: runner, Token: token}, s.writeAudit(ctx, principal.ID, "runner.token.rotate", runner.ID, map[string]any{"runner_id": runner.ID})
}

func (s *Service) RevokeRunnerToken(ctx context.Context, input RunnerTokenInput) (domain.Runner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.token.revoke.denied", strings.TrimSpace(input.RunnerID), nil)
		return domain.Runner{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if runnerID == "" {
		return domain.Runner{}, errors.New("runner_id is required")
	}
	runner, err := s.runners.UpdateRunnerToken(ctx, runnerID, "", domain.RunnerRevoked, time.Now().UTC())
	if err != nil {
		return domain.Runner{}, err
	}
	return runner, s.writeAudit(ctx, principal.ID, "runner.token.revoke", runner.ID, map[string]any{"runner_id": runner.ID})
}

func (s *Service) HeartbeatRunner(ctx context.Context) (domain.Runner, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	runner, err := s.runners.HeartbeatRunner(ctx, principal.ID, time.Now().UTC())
	if err != nil {
		return domain.Runner{}, err
	}
	return runner, s.writeAudit(ctx, runner.ID, "runner.heartbeat", runner.ID, nil)
}

func (s *Service) ClaimRun(ctx context.Context) (domain.ClaimedRun, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim, err := s.runners.ClaimRun(ctx, principal.ID, time.Now().UTC(), s.leaseTTL)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim.Run = s.ensureWorkflowState(claim.Run)
	claim.Run, err = s.markWorkflowStepRunning(ctx, claim.Run, time.Now().UTC())
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	executableRun := s.executableRunForWorkflowStep(claim.Run)
	plan, err := s.registry.BuildPlan(executableRun)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim.PrimitivePlan = plan
	_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: claim.Run.ID, Sequence: 2, Stream: domain.LogSystem, Message: "Run leased to runner " + claim.Lease.RunnerID, CreatedAt: time.Now().UTC()})
	return claim, s.writeAudit(ctx, principal.ID, "runner.claim", claim.Run.ID, map[string]any{"runner_id": claim.Lease.RunnerID, "lease_id": claim.Lease.ID})
}

func (s *Service) RenewLease(ctx context.Context, leaseID, fence string, attempt int) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	if leaseID == "" || fence == "" || attempt <= 0 {
		return domain.RunLease{}, errors.New("lease_id, attempt, and fence are required")
	}
	return s.runners.RenewLease(ctx, principal.ID, leaseID, fence, attempt, time.Now().UTC(), s.leaseTTL)
}

func (s *Service) ReapExpiredLeases(ctx context.Context) error {
	return s.runners.ExpireLeases(ctx, time.Now().UTC())
}

func (s *Service) CompleteLease(ctx context.Context, leaseID string, status string, attempt int, fence string, completionKey ...string) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	status = strings.TrimSpace(status)
	switch status {
	case domain.RunSucceeded, domain.RunFailed, domain.RunCanceled:
	default:
		return domain.RunLease{}, errors.New("lease completion status is invalid")
	}
	leaseID = strings.TrimSpace(leaseID)
	key := ""
	if len(completionKey) > 0 {
		key = strings.TrimSpace(completionKey[0])
	}
	if key == "" {
		return domain.RunLease{}, errors.New("completion_key is required")
	}
	lease, err := s.runners.GetLeaseForCompletion(ctx, leaseID, principal.ID, attempt, fence)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.RunLease{}, auth.ErrForbidden
		}
		return domain.RunLease{}, err
	}
	if attempt <= 0 || fence == "" || lease.Attempt != attempt || lease.Fence != fence {
		return domain.RunLease{}, auth.ErrForbidden
	}
	if lease.CompletionKey != "" {
		if lease.CompletionKey == key && lease.Status == status {
			return lease, nil
		}
		return domain.RunLease{}, store.ErrConflict
	}
	run, err := s.runByID(ctx, lease.RunID)
	if err != nil {
		return domain.RunLease{}, err
	}
	now := time.Now().UTC()
	runStatus, finishedAt, workflowState, queueNext, err := completionRunState(run, status, now)
	if err != nil {
		return domain.RunLease{}, err
	}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.RunLease{}, err
	}
	logs := []domain.RunLog{{ID: logID, RunID: lease.RunID, Sequence: 3, Stream: domain.LogSystem, Message: "Runner completed lease with status " + status, CreatedAt: now}}
	if queueNext {
		nextLogID, err := prefixedID("log")
		if err != nil {
			return domain.RunLease{}, err
		}
		logs = append(logs, domain.RunLog{ID: nextLogID, RunID: lease.RunID, Sequence: 3, Stream: domain.LogSystem, Message: "Workflow queued next step", CreatedAt: now})
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.complete", lease.RunID, map[string]any{"lease_id": lease.ID, "status": status})
	if err != nil {
		return domain.RunLease{}, err
	}
	return s.runners.CompleteLeaseRequest(ctx, leaseID, principal.ID, status, attempt, fence, key, now, runStatus, finishedAt, workflowState, logs, audit)
}

func (s *Service) RunnerLease(ctx context.Context, leaseID string, attempt int, fence string) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" || attempt <= 0 || fence == "" {
		return domain.RunLease{}, errors.New("lease_id, attempt, and fence are required")
	}
	lease, err := s.runners.GetLeaseForRunner(ctx, leaseID, principal.ID)
	if err != nil {
		return domain.RunLease{}, err
	}
	if lease.Attempt != attempt || lease.Fence != fence {
		return domain.RunLease{}, auth.ErrForbidden
	}
	return lease, nil
}

type SecretAccessInput struct {
	AccessID string `json:"access_id"`
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	Binding  string `json:"binding"`
	Provider string `json:"provider"`
	Version  string `json:"version"`
}

func (s *Service) AuthorizeSecretAccess(ctx context.Context, input SecretAccessInput) (domain.SecretAccessGrant, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	request := domain.SecretAccessRequest{
		AccessID: strings.TrimSpace(input.AccessID), RunnerID: principal.ID,
		RunID: strings.TrimSpace(input.RunID), LeaseID: strings.TrimSpace(input.LeaseID),
		Attempt: input.Attempt, Fence: strings.TrimSpace(input.Fence), Binding: strings.TrimSpace(input.Binding),
		Provider: strings.ToLower(strings.TrimSpace(input.Provider)), Version: strings.TrimSpace(input.Version), RequestedAt: time.Now().UTC(),
	}
	if request.AccessID == "" || request.RunID == "" || request.LeaseID == "" || request.Attempt <= 0 || request.Fence == "" || request.Binding == "" || request.Provider == "" {
		return domain.SecretAccessGrant{}, errors.New("access_id, run_id, lease_id, attempt, fence, binding, and provider are required")
	}
	if err := runner.ValidateSecretAccessMetadata(request.AccessID, request.Binding, request.Provider, request.Version); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	return s.runners.AuthorizeSecretAccess(ctx, request)
}

func (s *Service) CreateTemplate(ctx context.Context, input TemplateInput) (domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(input.ProjectID), domain.RoleMaintainer); err != nil {
		return domain.TaskTemplate{}, err
	}
	template, err := s.templateFromInput("", input)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	id, err := prefixedID("tpl")
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	template.ID = id
	template, err = s.templates.CreateTemplate(ctx, template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return template, s.writeAudit(ctx, principal.ID, "template.create", template.ID, map[string]any{"project_id": template.ProjectID, "kind": template.Kind})
}

func (s *Service) UpdateTemplate(ctx context.Context, id string, input TemplateInput) (domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	existing, err := s.templates.GetTemplate(ctx, id)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := s.requireProjectRole(ctx, principal, existing.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskTemplate{}, err
	}
	if input.ProjectID == "" {
		input.ProjectID = existing.ProjectID
	}
	if strings.TrimSpace(input.ProjectID) != existing.ProjectID {
		return domain.TaskTemplate{}, errors.New("template project_id cannot be changed")
	}
	template, err := s.templateFromInput(id, input)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	template, err = s.templates.UpdateTemplate(ctx, template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return template, s.writeAudit(ctx, principal.ID, "template.update", template.ID, map[string]any{"project_id": template.ProjectID, "kind": template.Kind})
}

func (s *Service) RequestRun(ctx context.Context, templateID string) (domain.TaskRun, error) {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return domain.TaskRun{}, errors.New("template_id is required")
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	template, err := s.templates.GetTemplate(ctx, templateID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if err := s.requireProjectRole(ctx, principal, template.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	status := domain.RunQueued
	if template.RequiresAck {
		status = domain.RunWaitingApproval
	}
	now := time.Now().UTC()
	runID, err := prefixedID("run")
	if err != nil {
		return domain.TaskRun{}, err
	}
	run := domain.TaskRun{ID: runID, ProjectID: template.ProjectID, TemplateID: &template.ID, RunSpec: template.RunSpec, Workflow: template.Workflow, WorkflowState: initialWorkflowState(template.Workflow), RunnerTags: template.RunnerTags, Status: status, RequestedBy: principal.ID, StartedAt: now}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	log := domain.RunLog{ID: logID, RunID: run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "Run requested", CreatedAt: now}
	var approval *domain.Approval
	if template.RequiresAck {
		approvalID, err := prefixedID("apr")
		if err != nil {
			return domain.TaskRun{}, err
		}
		approval = &domain.Approval{ID: approvalID, RunID: run.ID, Status: domain.ApprovalPending, RequestedBy: principal.ID, CreatedAt: now}
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.request", run.ID, map[string]any{"template_id": template.ID, "status": run.Status})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runs.CreateRunRequest(ctx, run, log, approval, audit)
}

type RunRequestInput struct {
	ProjectID   string          `json:"project_id"`
	TemplateID  string          `json:"template_id"`
	RunSpec     domain.RunSpec  `json:"run_spec"`
	Workflow    domain.Workflow `json:"workflow"`
	RunnerTags  []string        `json:"runner_tags"`
	RequiresAck bool            `json:"requires_ack"`
}

func (s *Service) RequestRunWithSpec(ctx context.Context, input RunRequestInput) (domain.TaskRun, error) {
	if strings.TrimSpace(input.TemplateID) != "" {
		return s.RequestRun(ctx, input.TemplateID)
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		return domain.TaskRun{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	runSpec, err := s.normalizeRunSpec(input.RunSpec, "")
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := s.registry.BuildPlan(domain.TaskRun{ID: "validation", ProjectID: projectID, RunSpec: runSpec}); err != nil {
		return domain.TaskRun{}, err
	}
	status := domain.RunQueued
	if input.RequiresAck {
		status = domain.RunWaitingApproval
	}
	now := time.Now().UTC()
	runID, err := prefixedID("run")
	if err != nil {
		return domain.TaskRun{}, err
	}
	workflow, err := s.normalizeWorkflow(input.Workflow)
	if err != nil {
		return domain.TaskRun{}, err
	}
	run := domain.TaskRun{ID: runID, ProjectID: projectID, RunSpec: runSpec, Workflow: workflow, WorkflowState: initialWorkflowState(workflow), RunnerTags: normalizeTags(input.RunnerTags), Status: status, RequestedBy: principal.ID, StartedAt: now}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	log := domain.RunLog{ID: logID, RunID: run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "Ad-hoc run requested", CreatedAt: now}
	var approval *domain.Approval
	if input.RequiresAck {
		approvalID, err := prefixedID("apr")
		if err != nil {
			return domain.TaskRun{}, err
		}
		approval = &domain.Approval{ID: approvalID, RunID: run.ID, Status: domain.ApprovalPending, RequestedBy: principal.ID, CreatedAt: now}
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.request", run.ID, map[string]any{"run_type": run.RunSpec.Type, "status": run.Status})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runs.CreateRunRequest(ctx, run, log, approval, audit)
}

func (s *Service) ApproveRun(ctx context.Context, runID string) (domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	run, err := s.runByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.approvals.ApproveRun(ctx, strings.TrimSpace(runID), principal.ID, time.Now().UTC())
	if err != nil {
		return domain.Approval{}, err
	}
	return approval, s.writeAudit(ctx, principal.ID, "run.approve", runID, map[string]any{"approval_id": approval.ID})
}

func (s *Service) RejectRun(ctx context.Context, runID string) (domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	run, err := s.runByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.approvals.RejectRun(ctx, strings.TrimSpace(runID), principal.ID, time.Now().UTC())
	if err != nil {
		return domain.Approval{}, err
	}
	_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: approval.RunID, Sequence: 2, Stream: domain.LogSystem, Message: "Run rejected by approver", CreatedAt: time.Now().UTC()})
	return approval, s.writeAudit(ctx, principal.ID, "run.reject", runID, map[string]any{"approval_id": approval.ID})
}

func (s *Service) CancelRun(ctx context.Context, runID string) (domain.TaskRun, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	runID = strings.TrimSpace(runID)
	targetRun, err := s.runByID(ctx, runID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if err := s.requireProjectRole(ctx, principal, targetRun.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	if domain.IsTerminalRunStatus(targetRun.Status) {
		return domain.TaskRun{}, errors.New("run is already terminal")
	}
	now := time.Now().UTC()
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.cancel", runID, map[string]any{"status": domain.RunCanceled})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runners.CancelRunRequest(ctx, runID, now, domain.RunLog{ID: logID, RunID: runID, Sequence: 2, Stream: domain.LogSystem, Message: "Run canceled by user", CreatedAt: now}, audit)
}

func (s *Service) RunnerPrimitivePlan(ctx context.Context, runID string) (domain.RunnerPrimitivePlan, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	return s.registry.BuildPlan(run)
}

func (s *Service) Capabilities() []domain.Capability {
	return s.registry.Capabilities()
}

func newSessionToken() (string, string, error) {
	tokenHex, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token := "ncd_" + tokenHex
	return token, sessionTokenHash(token), nil
}

func newRunnerToken() (string, string, error) {
	tokenHex, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token := "ncr_" + tokenHex
	return token, runnerTokenHash(token), nil
}

func newEnrollmentToken() (string, string, error) {
	tokenHex, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token := "nce_" + tokenHex
	return token, enrollmentTokenHash(token), nil
}

func newAPITokenSecret() (string, string, error) {
	tokenHex, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token := "nca_" + tokenHex
	return token, apiTokenHash(token), nil
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func runnerTokenHash(token string) string {
	return sessionTokenHash(token)
}

func enrollmentTokenHash(token string) string { return sessionTokenHash(token) }

func apiTokenHash(token string) string {
	return sessionTokenHash(token)
}

func normalizeAPITokenRoles(roles []string) []string {
	normalized := make([]string, 0, len(roles))
	seen := map[string]bool{}
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if !isAllowedGlobalRole(role) || seen[role] {
			continue
		}
		seen[role] = true
		normalized = append(normalized, role)
	}
	return normalized
}

func verifyPassword(password string, passwordHash string) bool {
	if !strings.HasPrefix(passwordHash, "sha256:") {
		return false
	}
	sum := sha256.Sum256([]byte(password))
	return "sha256:"+hex.EncodeToString(sum[:]) == passwordHash
}

func (s *Service) templateFromInput(id string, input TemplateInput) (domain.TaskTemplate, error) {
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	kind := strings.TrimSpace(input.Kind)
	if projectID == "" {
		return domain.TaskTemplate{}, errors.New("project_id is required")
	}
	if name == "" {
		return domain.TaskTemplate{}, errors.New("name is required")
	}
	runSpec, err := s.normalizeRunSpec(input.RunSpec, kind)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if kind == "" {
		kind = runSpec.Type
	}
	if _, err := s.registry.BuildPlan(domain.TaskRun{ID: "validation", ProjectID: projectID, RunSpec: runSpec}); err != nil {
		return domain.TaskTemplate{}, err
	}
	workflow, err := s.normalizeWorkflow(input.Workflow)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return domain.TaskTemplate{ID: id, ProjectID: projectID, Name: name, Kind: kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: normalizeTags(input.RunnerTags), RequiresAck: input.RequiresAck}, nil
}

func (s *Service) normalizeRunSpec(runSpec domain.RunSpec, fallbackType string) (domain.RunSpec, error) {
	runType := strings.TrimSpace(runSpec.Type)
	if runType == "" {
		runType = strings.TrimSpace(fallbackType)
	}
	if runType == "" {
		return domain.RunSpec{}, errors.New("run_spec.type is required")
	}
	if !s.registry.Supports(runType) {
		return domain.RunSpec{}, errors.New("run_spec.type is not registered")
	}
	if runSpec.Inputs == nil {
		runSpec.Inputs = map[string]any{}
	}
	if runSpec.Repository != nil && strings.TrimSpace(runSpec.Repository.URL) != "" {
		if err := source.ValidateRepositoryURL(runSpec.Repository.URL); err != nil {
			return domain.RunSpec{}, fmt.Errorf("run_spec.repository.url: %w", err)
		}
	}
	if runSpec.Process != nil && len(runSpec.Process.Command) == 0 {
		return domain.RunSpec{}, errors.New("run_spec.process.command is required")
	}
	for _, artifact := range runSpec.Artifacts {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Path) == "" {
			return domain.RunSpec{}, errors.New("run_spec.artifacts require name and path")
		}
	}
	secretNames := make(map[string]struct{}, len(runSpec.Secrets))
	secretTargets := make(map[string]struct{}, len(runSpec.Secrets))
	for index := range runSpec.Secrets {
		secret := &runSpec.Secrets[index]
		secret.Name = strings.TrimSpace(secret.Name)
		secret.Provider = strings.ToLower(strings.TrimSpace(secret.Provider))
		secret.Reference = strings.TrimSpace(secret.Reference)
		secret.Target = strings.TrimSpace(secret.Target)
		secret.Version = strings.TrimSpace(secret.Version)
		secret.Fingerprint = strings.TrimSpace(secret.Fingerprint)
		secret.Classification = strings.ToLower(strings.TrimSpace(secret.Classification))
		for encodingIndex := range secret.RedactEncodings {
			secret.RedactEncodings[encodingIndex] = strings.ToLower(strings.TrimSpace(secret.RedactEncodings[encodingIndex]))
		}
		if err := runner.ValidateSecretBinding(*secret); err != nil {
			return domain.RunSpec{}, fmt.Errorf("invalid run_spec.secrets: %w", err)
		}
		if _, exists := secretNames[secret.Name]; exists {
			return domain.RunSpec{}, errors.New("invalid run_spec.secrets: binding names must be unique")
		}
		if _, exists := secretTargets[secret.Target]; exists {
			return domain.RunSpec{}, errors.New("invalid run_spec.secrets: binding targets must be unique")
		}
		secretNames[secret.Name] = struct{}{}
		secretTargets[secret.Target] = struct{}{}
	}
	if runSpec.Workflow != nil {
		workflow, err := s.normalizeWorkflow(*runSpec.Workflow)
		if err != nil {
			return domain.RunSpec{}, err
		}
		runSpec.Workflow = &workflow
	}
	runSpec.Type = runType
	return runSpec, nil
}

func (s *Service) normalizeWorkflow(workflow domain.Workflow) (domain.Workflow, error) {
	seen := map[string]struct{}{}
	for i, step := range workflow.Steps {
		step.ID = strings.TrimSpace(step.ID)
		step.Name = strings.TrimSpace(step.Name)
		if step.ID == "" {
			return domain.Workflow{}, errors.New("workflow step id is required")
		}
		if _, ok := seen[step.ID]; ok {
			return domain.Workflow{}, errors.New("workflow step id must be unique")
		}
		seen[step.ID] = struct{}{}
		if step.Name == "" {
			step.Name = step.ID
		}
		runSpec, err := s.normalizeRunSpec(step.RunSpec, "")
		if err != nil {
			return domain.Workflow{}, err
		}
		step.RunSpec = runSpec
		for _, dep := range step.DependsOn {
			if strings.TrimSpace(dep) == "" {
				return domain.Workflow{}, errors.New("workflow dependency id is required")
			}
		}
		workflow.Steps[i] = step
	}
	return workflow, nil
}

func initialWorkflowState(workflow domain.Workflow) domain.WorkflowState {
	state := domain.WorkflowState{Steps: make([]domain.WorkflowStepState, 0, len(workflow.Steps))}
	for _, step := range workflow.Steps {
		state.Steps = append(state.Steps, domain.WorkflowStepState{ID: step.ID, Name: step.Name, Status: domain.WorkflowPending})
	}
	return state
}

func (s *Service) ensureWorkflowState(run domain.TaskRun) domain.TaskRun {
	if len(run.Workflow.Steps) == 0 {
		return run
	}
	if len(run.WorkflowState.Steps) == len(run.Workflow.Steps) {
		return run
	}
	run.WorkflowState = initialWorkflowState(run.Workflow)
	return run
}

func (s *Service) markWorkflowStepRunning(ctx context.Context, run domain.TaskRun, startedAt time.Time) (domain.TaskRun, error) {
	if len(run.Workflow.Steps) == 0 {
		return run, nil
	}
	index := nextWorkflowStepIndex(run)
	if index < 0 {
		return run, nil
	}
	run.WorkflowState.CurrentStepID = run.Workflow.Steps[index].ID
	run.WorkflowState.Steps[index].Status = domain.WorkflowRunning
	run.WorkflowState.Steps[index].StartedAt = &startedAt
	run.WorkflowState.Steps[index].FinishedAt = nil
	updated, err := s.runs.UpdateRunWorkflowState(ctx, run.ID, run.WorkflowState)
	if err != nil {
		return domain.TaskRun{}, err
	}
	_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: run.ID, Sequence: 2, Stream: domain.LogSystem, Message: "Workflow step started: " + run.Workflow.Steps[index].ID, CreatedAt: startedAt})
	return updated, nil
}

func (s *Service) executableRunForWorkflowStep(run domain.TaskRun) domain.TaskRun {
	if len(run.Workflow.Steps) == 0 || strings.TrimSpace(run.WorkflowState.CurrentStepID) == "" {
		return run
	}
	for _, step := range run.Workflow.Steps {
		if step.ID == run.WorkflowState.CurrentStepID {
			run.RunSpec = step.RunSpec
			return run
		}
	}
	return run
}

func (s *Service) advanceWorkflowAfterLease(ctx context.Context, runID string, status string) error {
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return err
	}
	if len(run.Workflow.Steps) == 0 || strings.TrimSpace(run.WorkflowState.CurrentStepID) == "" {
		return nil
	}
	now := time.Now().UTC()
	for i, stepState := range run.WorkflowState.Steps {
		if stepState.ID != run.WorkflowState.CurrentStepID {
			continue
		}
		run.WorkflowState.Steps[i].Status = status
		run.WorkflowState.Steps[i].FinishedAt = &now
		break
	}
	run.WorkflowState.CurrentStepID = ""
	if _, err := s.runs.UpdateRunWorkflowState(ctx, run.ID, run.WorkflowState); err != nil {
		return err
	}
	if status != domain.RunSucceeded {
		return nil
	}
	if nextWorkflowStepIndex(run) >= 0 {
		if _, err := s.runs.UpdateRunStatus(ctx, run.ID, domain.RunQueued, nil); err != nil {
			return err
		}
		_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: run.ID, Sequence: 3, Stream: domain.LogSystem, Message: "Workflow queued next step", CreatedAt: now})
	}
	return nil
}

func completionRunState(run domain.TaskRun, status string, completedAt time.Time) (string, *time.Time, *domain.WorkflowState, bool, error) {
	runStatus := status
	finishedAt := &completedAt
	if len(run.Workflow.Steps) == 0 || strings.TrimSpace(run.WorkflowState.CurrentStepID) == "" {
		return runStatus, finishedAt, nil, false, nil
	}
	workflowState := run.WorkflowState
	for i, stepState := range workflowState.Steps {
		if stepState.ID != workflowState.CurrentStepID {
			continue
		}
		workflowState.Steps[i].Status = status
		workflowState.Steps[i].FinishedAt = &completedAt
		break
	}
	workflowState.CurrentStepID = ""
	run.WorkflowState = workflowState
	if status == domain.RunSucceeded && nextWorkflowStepIndex(run) >= 0 {
		return domain.RunQueued, nil, &workflowState, true, nil
	}
	return runStatus, finishedAt, &workflowState, false, nil
}

func nextWorkflowStepIndex(run domain.TaskRun) int {
	statusByID := map[string]string{}
	for _, step := range run.WorkflowState.Steps {
		statusByID[step.ID] = step.Status
	}
	for i, step := range run.Workflow.Steps {
		status := statusByID[step.ID]
		if status == "" {
			status = domain.WorkflowPending
		}
		if status != domain.WorkflowPending {
			continue
		}
		dependenciesReady := true
		for _, dependency := range step.DependsOn {
			if statusByID[dependency] != domain.RunSucceeded {
				dependenciesReady = false
				break
			}
		}
		if dependenciesReady {
			return i
		}
	}
	return -1
}

func normalizeTags(values []string) []string {
	tags := make([]string, 0, len(values))
	for _, tag := range values {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (s *Service) requireProjectRole(ctx context.Context, principal auth.Principal, projectID string, minimumRole string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project_id is required")
	}
	if isSystemAdmin(principal) {
		return nil
	}
	members, err := s.members.ListProjectMembers(ctx, projectID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.UserID == principal.ID && roleRank(member.Role) >= roleRank(minimumRole) {
			return nil
		}
	}
	return auth.ErrForbidden
}

func (s *Service) allowedProjects(ctx context.Context, userID string) (map[string]string, error) {
	members, err := s.members.ListProjectMembers(ctx, "")
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, member := range members {
		if member.UserID == userID {
			allowed[member.ProjectID] = member.Role
		}
	}
	return allowed, nil
}

func (s *Service) filterTemplatesForPrincipal(ctx context.Context, principal auth.Principal, templates []domain.TaskTemplate) ([]domain.TaskTemplate, error) {
	if isSystemAdmin(principal) {
		return templates, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.TaskTemplate, 0, len(templates))
	for _, template := range templates {
		if _, ok := allowed[template.ProjectID]; ok {
			out = append(out, template)
		}
	}
	return out, nil
}

func (s *Service) filterRepositoriesForPrincipal(ctx context.Context, principal auth.Principal, repositories []domain.Repository) ([]domain.Repository, error) {
	if isSystemAdmin(principal) {
		return repositories, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Repository, 0, len(repositories))
	for _, repository := range repositories {
		if _, ok := allowed[repository.ProjectID]; ok {
			out = append(out, repository)
		}
	}
	return out, nil
}

func (s *Service) filterAccessKeysForPrincipal(ctx context.Context, principal auth.Principal, keys []domain.AccessKey) ([]domain.AccessKey, error) {
	if isSystemAdmin(principal) {
		return keys, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccessKey, 0, len(keys))
	for _, key := range keys {
		if _, ok := allowed[key.ProjectID]; ok {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *Service) filterInventoriesForPrincipal(ctx context.Context, principal auth.Principal, inventories []domain.Inventory) ([]domain.Inventory, error) {
	if isSystemAdmin(principal) {
		return inventories, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Inventory, 0, len(inventories))
	for _, inventory := range inventories {
		if _, ok := allowed[inventory.ProjectID]; ok {
			out = append(out, inventory)
		}
	}
	return out, nil
}

func (s *Service) filterRunsForPrincipal(ctx context.Context, principal auth.Principal, runs []domain.TaskRun) ([]domain.TaskRun, error) {
	if isSystemAdmin(principal) {
		return runs, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.TaskRun, 0, len(runs))
	for _, run := range runs {
		if _, ok := allowed[run.ProjectID]; ok {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *Service) runByID(ctx context.Context, runID string) (domain.TaskRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.TaskRun{}, errors.New("run_id is required")
	}
	runs, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return domain.TaskRun{}, err
	}
	for _, run := range runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return domain.TaskRun{}, store.ErrNotFound
}

func (s *Service) templateByID(ctx context.Context, templateID string) (domain.TaskTemplate, error) {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return domain.TaskTemplate{}, store.ErrNotFound
	}
	return s.templates.GetTemplate(ctx, templateID)
}

func (s *Service) repositoryByID(ctx context.Context, repositoryID string) (domain.Repository, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" {
		return domain.Repository{}, store.ErrNotFound
	}
	repositories, err := s.sources.ListRepositories(ctx, "")
	if err != nil {
		return domain.Repository{}, err
	}
	for _, repository := range repositories {
		if repository.ID == repositoryID {
			return repository, nil
		}
	}
	return domain.Repository{}, store.ErrNotFound
}

func (s *Service) auditProjectID(ctx context.Context, event domain.AuditEvent) (string, bool) {
	if projectID, ok := event.Metadata["project_id"].(string); ok && strings.TrimSpace(projectID) != "" {
		return strings.TrimSpace(projectID), true
	}
	switch {
	case strings.HasPrefix(event.TargetID, "proj_"):
		return event.TargetID, true
	case strings.HasPrefix(event.TargetID, "run_"):
		run, err := s.runByID(ctx, event.TargetID)
		if err != nil {
			return "", false
		}
		return run.ProjectID, true
	case strings.HasPrefix(event.TargetID, "tpl_"):
		template, err := s.templateByID(ctx, event.TargetID)
		if err != nil {
			return "", false
		}
		return template.ProjectID, true
	case strings.HasPrefix(event.TargetID, "repo_"):
		repository, err := s.repositoryByID(ctx, event.TargetID)
		if err != nil {
			return "", false
		}
		return repository.ProjectID, true
	default:
		return "", false
	}
}

func isSystemAdmin(principal auth.Principal) bool {
	for _, role := range principal.Roles {
		if role == domain.RoleSystemAdmin {
			return true
		}
	}
	return false
}

func isRunnerAdmin(principal auth.Principal) bool {
	if isSystemAdmin(principal) {
		return true
	}
	for _, role := range principal.Roles {
		if role == domain.RoleRunnerAdmin {
			return true
		}
	}
	return false
}

func isAllowedGlobalRole(role string) bool {
	return role == domain.RoleSystemAdmin || role == domain.RoleRunnerAdmin
}

func globalPrincipalRoles(globalRole string) []string {
	if isAllowedGlobalRole(globalRole) {
		return []string{globalRole}
	}
	return []string{}
}

func (s *Service) requireRunnerPrincipal(ctx context.Context) (auth.Principal, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	if principal.Provider != domain.PrincipalRunner {
		return auth.Principal{}, auth.ErrForbidden
	}
	return principal, nil
}

func roleRank(role string) int {
	switch role {
	case domain.RoleSystemAdmin:
		return 4
	case domain.RoleOwner:
		return 3
	case domain.RoleMaintainer:
		return 2
	case domain.RoleViewer:
		return 1
	default:
		return 0
	}
}

func projectRole(projectID string, role string) domain.ProjectRole {
	rank := roleRank(role)
	return domain.ProjectRole{
		ProjectID: projectID,
		Role:      role,
		CanView:   rank >= roleRank(domain.RoleViewer),
		CanRun:    rank >= roleRank(domain.RoleMaintainer),
		CanAdmin:  rank >= roleRank(domain.RoleOwner),
	}
}

func (s *Service) writeAudit(ctx context.Context, actorID string, action string, targetID string, metadata map[string]any) error {
	event, err := s.auditEvent(ctx, actorID, action, targetID, metadata)
	if err != nil {
		return err
	}
	return s.audit.CreateAuditEvent(ctx, event)
}

func (s *Service) auditEvent(ctx context.Context, actorID string, action string, targetID string, metadata map[string]any) (domain.AuditEvent, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && requestID != "" {
		metadata["request_id"] = requestID
	}
	id, err := prefixedID("aud")
	if err != nil {
		return domain.AuditEvent{}, err
	}
	return domain.AuditEvent{ID: id, ActorID: actorID, Action: action, TargetID: targetID, Metadata: metadata, CreatedAt: time.Now().UTC()}, nil
}

func (s *Service) writeDeniedAudit(ctx context.Context, principal auth.Principal, action string, targetID string, metadata map[string]any) {
	if strings.TrimSpace(targetID) == "" {
		targetID = principal.ID
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["provider"] = principal.Provider
	metadata["roles"] = principal.Roles
	_ = s.writeAudit(ctx, principal.ID, action, targetID, metadata)
}

func paginateForService[T any](items []T, page store.Page) store.PageResult[T] {
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
	return store.PageResult[T]{Items: items[offset:end], Limit: limit, Offset: offset, Total: total}
}

func prefixedID(prefix string) (string, error) {
	value, err := randomHex(8)
	if err != nil {
		return "", err
	}
	return prefix + "_" + value, nil
}

func mustPrefixedID(prefix string) string {
	id, err := prefixedID(prefix)
	if err != nil {
		return prefix + "_unavailable"
	}
	return id
}

func randomHex(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
