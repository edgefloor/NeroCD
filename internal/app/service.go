package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/observability"
	"nerocd/internal/runner"
	"nerocd/internal/source"
	"nerocd/internal/store"
)

type requestIDContextKey struct{}

// WithRequestID returns ctx carrying a normalized request identifier.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// Service orchestrates authorized application workflows across injected dependencies.
type Service struct {
	auth                 auth.Provider
	users                store.UserRepository
	sessions             store.SessionRepository
	apiTokens            store.APITokenRepository
	projects             store.ProjectRepository
	members              store.ProjectMemberRepository
	templates            store.TemplateRepository
	sources              store.SourceRepository
	runs                 store.RunRepository
	runners              store.RunnerRepository
	approvals            store.ApprovalRepository
	audit                store.AuditRepository
	deployments          store.DeploymentRepository
	operationalSnapshot  store.OperationalSnapshotRepository
	operationalWriter    store.OperationalObservationWriter
	operationalReader    store.OperationalObservationReader
	retention            store.RunLogRetentionRepository
	registry             runner.Registry
	leaseTTL             time.Duration
	allowLegacyPasswords bool
	clock                func() time.Time
	loginLimiter         *auth.LoginLimiter
}

// Dependencies names every collaborator of the application service. All
// fields are required: NewService fails instead of probing the runner or run
// repositories for optional capabilities, so wiring mistakes surface at
// construction rather than as missing behavior at request time.
// Dependencies supplies the repositories and services used by Service.
type Dependencies struct {
	Auth              auth.Provider
	Users             store.UserRepository
	Sessions          store.SessionRepository
	APITokens         store.APITokenRepository
	Projects          store.ProjectRepository
	Members           store.ProjectMemberRepository
	Templates         store.TemplateRepository
	Sources           store.SourceRepository
	Runs              store.RunRepository
	Runners           store.RunnerRepository
	Approvals         store.ApprovalRepository
	Audit             store.AuditRepository
	Deployments       store.DeploymentRepository
	Observability     store.OperationalSnapshotRepository
	ObservationWriter store.OperationalObservationWriter
	ObservationReader store.OperationalObservationReader
	Retention         store.RunLogRetentionRepository
}

// NewService constructs the application service. It reports every missing
// dependency by name so wiring errors are actionable at startup.
func NewService(deps Dependencies) (*Service, error) {
	var missing []string
	if deps.Auth == nil {
		missing = append(missing, "Auth")
	}
	if deps.Users == nil {
		missing = append(missing, "Users")
	}
	if deps.Sessions == nil {
		missing = append(missing, "Sessions")
	}
	if deps.APITokens == nil {
		missing = append(missing, "APITokens")
	}
	if deps.Projects == nil {
		missing = append(missing, "Projects")
	}
	if deps.Members == nil {
		missing = append(missing, "Members")
	}
	if deps.Templates == nil {
		missing = append(missing, "Templates")
	}
	if deps.Sources == nil {
		missing = append(missing, "Sources")
	}
	if deps.Runs == nil {
		missing = append(missing, "Runs")
	}
	if deps.Runners == nil {
		missing = append(missing, "Runners")
	}
	if deps.Approvals == nil {
		missing = append(missing, "Approvals")
	}
	if deps.Audit == nil {
		missing = append(missing, "Audit")
	}
	if deps.Deployments == nil {
		missing = append(missing, "Deployments")
	}
	if deps.Observability == nil {
		missing = append(missing, "Observability")
	}
	if deps.ObservationWriter == nil {
		missing = append(missing, "ObservationWriter")
	}
	if deps.ObservationReader == nil {
		missing = append(missing, "ObservationReader")
	}
	if deps.Retention == nil {
		missing = append(missing, "Retention")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("app.NewService: missing required dependencies: %s", strings.Join(missing, ", "))
	}
	return &Service{
		auth:                 deps.Auth,
		users:                deps.Users,
		sessions:             deps.Sessions,
		apiTokens:            deps.APITokens,
		projects:             deps.Projects,
		members:              deps.Members,
		templates:            deps.Templates,
		sources:              deps.Sources,
		runs:                 deps.Runs,
		runners:              deps.Runners,
		approvals:            deps.Approvals,
		audit:                deps.Audit,
		deployments:          deps.Deployments,
		operationalSnapshot:  deps.Observability,
		operationalWriter:    deps.ObservationWriter,
		operationalReader:    deps.ObservationReader,
		retention:            deps.Retention,
		registry:             runner.NewRegistry(),
		leaseTTL:             2 * time.Minute,
		allowLegacyPasswords: true,
		clock:                time.Now,
		loginLimiter:         auth.NewLoginLimiter(time.Now, 5, time.Minute, 10_000),
	}, nil
}

// OperationalSnapshot returns the current operational snapshot.
func (s *Service) OperationalSnapshot(ctx context.Context) (observability.Snapshot, error) {
	if s.operationalSnapshot == nil {
		return observability.Snapshot{}, errors.New("operational snapshot is unavailable")
	}
	return s.operationalSnapshot.OperationalSnapshot(ctx)
}

// SetLoginLimiter is an explicit test/configuration seam. A nil limiter is
// rejected so production login never silently loses its brute-force boundary.
// SetLoginLimiter configures login-attempt limiting.
func (s *Service) SetLoginLimiter(limiter *auth.LoginLimiter) {
	if limiter == nil {
		panic("login limiter is required")
	}
	s.loginLimiter = limiter
}

// SetClock configures the clock used by service operations.
func (s *Service) SetClock(clock func() time.Time) {
	if clock == nil {
		panic("clock is required")
	}
	s.clock = clock
}

// SetAllowLegacyPasswordVerification is only used by the explicit development
// mode to perform a one-time compare-and-swap style upgrade of retired hashes.
// SetAllowLegacyPasswordVerification configures legacy password verification.
func (s *Service) SetAllowLegacyPasswordVerification(value bool) { s.allowLegacyPasswords = value }

// Capabilities returns the service capability inventory.
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
		deps := make([]string, 0, len(step.DependsOn))
		seenDeps := map[string]struct{}{}
		for _, dep := range step.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				return domain.Workflow{}, errors.New("workflow dependency id is required")
			}
			if dep == step.ID {
				return domain.Workflow{}, errors.New("workflow step cannot depend on itself")
			}
			if _, duplicate := seenDeps[dep]; duplicate {
				return domain.Workflow{}, errors.New("workflow dependency id must be unique per step")
			}
			seenDeps[dep] = struct{}{}
			deps = append(deps, dep)
		}
		step.DependsOn = deps
		workflow.Steps[i] = step
	}
	for _, step := range workflow.Steps {
		for _, dep := range step.DependsOn {
			if _, ok := seen[dep]; !ok {
				return domain.Workflow{}, errors.New("workflow dependency must reference a step")
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	byID := map[string]domain.WorkflowStep{}
	for _, step := range workflow.Steps {
		byID[step.ID] = step
	}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if !visit(dep) {
				return false
			}
		}
		visiting[id] = false
		visited[id] = true
		return true
	}
	for _, step := range workflow.Steps {
		if !visit(step.ID) {
			return domain.Workflow{}, errors.New("workflow must be acyclic")
		}
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
	if limit > store.MaxPageLimit {
		limit = store.MaxPageLimit
	}
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > store.MaxPageOffset {
		offset = store.MaxPageOffset
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
