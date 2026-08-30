package app

import (
	"context"
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/store"
	"regexp"
	"strings"
	"time"
)

var immutableImageReferencePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

// ServiceInput supplies service registration attributes.
type ServiceInput struct {
	ProjectID    string   `json:"project_id"`
	Name         string   `json:"name"`
	RepositoryID string   `json:"repository_id"`
	ComposePath  string   `json:"compose_path"`
	Profiles     []string `json:"profiles"`
}

// EnvironmentInput supplies deployment-environment configuration.
type EnvironmentInput struct {
	ServiceID            string                 `json:"service_id"`
	Name                 string                 `json:"name"`
	RunnerSelector       []string               `json:"runner_selector"`
	ComposeProject       string                 `json:"compose_project"`
	HealthPolicy         domain.HealthPolicy    `json:"health_policy"`
	ConfirmationRequired bool                   `json:"confirmation_required"`
	TimeoutSeconds       int                    `json:"timeout_seconds"`
	SecretBindings       []domain.SecretBinding `json:"secret_bindings"`
	RollbackSafe         bool                   `json:"rollback_safe"`
}

// RevisionInput supplies an immutable service revision request.
type RevisionInput struct {
	ServiceID    string `json:"service_id"`
	RequestedRef string `json:"requested_ref"`
}

// DeploymentInput supplies an authorized deployment request.
type DeploymentInput struct {
	EnvironmentID     string  `json:"environment_id"`
	DesiredRevisionID string  `json:"desired_revision_id"`
	IdempotencyKey    string  `json:"idempotency_key"`
	TaskRunID         *string `json:"task_run_id,omitempty"`
	RollbackOfID      *string `json:"rollback_of_id,omitempty"`
}

// DeploymentCancelInput identifies an authorized deployment cancellation.
type DeploymentCancelInput struct {
	DeploymentID string `json:"deployment_id"`
	RequestID    string `json:"request_id"`
}

func (s *Service) deploymentRepo() (store.DeploymentRepository, error) {
	if s.deployments == nil {
		return nil, errors.New("deployment control plane is unavailable")
	}
	return s.deployments, nil
}

// ListServices lists authorized services.
func (s *Service) ListServices(ctx context.Context, projectID string) ([]domain.Service, error) {
	p, e := s.CurrentPrincipal(ctx)
	if e != nil {
		return nil, e
	}
	if projectID != "" {
		if e = s.requireProjectRole(ctx, p, projectID, domain.RoleViewer); e != nil {
			return nil, e
		}
	}
	r, e := s.deploymentRepo()
	if e != nil {
		return nil, e
	}
	services, e := r.ListServices(ctx, projectID)
	if e != nil || projectID != "" || isSystemAdmin(p) {
		return services, e
	}
	allowed, e := s.allowedProjects(ctx, p.ID)
	if e != nil {
		return nil, e
	}
	out := make([]domain.Service, 0, len(services))
	for _, service := range services {
		if _, ok := allowed[service.ProjectID]; ok {
			out = append(out, service)
		}
	}
	return out, nil
}

// CreateService creates an authorized service.
func (s *Service) CreateService(ctx context.Context, in ServiceInput) (domain.Service, error) {
	p, e := s.CurrentPrincipal(ctx)
	if e != nil {
		return domain.Service{}, e
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if e = s.requireProjectRole(ctx, p, in.ProjectID, domain.RoleMaintainer); e != nil {
		return domain.Service{}, e
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.RepositoryID) == "" || strings.TrimSpace(in.ComposePath) == "" {
		return domain.Service{}, errors.New("name, repository_id, and compose_path are required")
	}
	repos, e := s.sources.ListRepositories(ctx, in.ProjectID)
	if e != nil {
		return domain.Service{}, e
	}
	ok := false
	for _, x := range repos {
		if x.ID == in.RepositoryID {
			ok = true
		}
	}
	if !ok {
		return domain.Service{}, auth.ErrForbidden
	}
	r, e := s.deploymentRepo()
	if e != nil {
		return domain.Service{}, e
	}
	id, e := prefixedID("svc")
	if e != nil {
		return domain.Service{}, e
	}
	profiles := append([]string{}, in.Profiles...)
	v := domain.Service{ID: id, ProjectID: in.ProjectID, Name: strings.TrimSpace(in.Name), RepositoryID: in.RepositoryID, ComposePath: strings.TrimSpace(in.ComposePath), Profiles: profiles, OwnerID: p.ID, CreatedAt: time.Now().UTC()}
	audit, e := s.auditEvent(ctx, p.ID, "service.create", v.ID, map[string]any{"project_id": v.ProjectID})
	if e != nil {
		return domain.Service{}, e
	}
	return r.CreateService(ctx, v, store.WithAudit(audit))
}

// ListEnvironments lists authorized environments.
func (s *Service) ListEnvironments(ctx context.Context, serviceID string) ([]domain.Environment, error) {
	r, e := s.deploymentRepo()
	if e != nil {
		return nil, e
	}
	v, e := r.ListEnvironments(ctx, serviceID)
	if e != nil {
		return nil, e
	}
	p, e := s.CurrentPrincipal(ctx)
	if e != nil {
		return nil, e
	}
	if serviceID != "" {
		if e = s.requireServiceView(ctx, serviceID); e != nil {
			return nil, e
		}
		return v, nil
	}
	if isSystemAdmin(p) {
		return v, nil
	}
	allowed, e := s.allowedProjects(ctx, p.ID)
	if e != nil {
		return nil, e
	}
	services, e := r.ListServices(ctx, "")
	if e != nil {
		return nil, e
	}
	projects := map[string]string{}
	for _, service := range services {
		projects[service.ID] = service.ProjectID
	}
	out := make([]domain.Environment, 0, len(v))
	for _, environment := range v {
		if _, ok := allowed[projects[environment.ServiceID]]; ok {
			out = append(out, environment)
		}
	}
	return out, nil
}
func (s *Service) requireServiceRole(ctx context.Context, id string, minimumRole string) error {
	r, e := s.deploymentRepo()
	if e != nil {
		return e
	}
	service, e := r.GetService(ctx, id)
	if e != nil {
		return store.ErrNotFound
	}
	p, e := s.CurrentPrincipal(ctx)
	if e != nil {
		return e
	}
	return s.requireProjectRole(ctx, p, service.ProjectID, minimumRole)
}
func (s *Service) requireServiceView(ctx context.Context, id string) error {
	return s.requireServiceRole(ctx, id, domain.RoleViewer)
}
func (s *Service) requireServiceMutation(ctx context.Context, id string) error {
	return s.requireServiceRole(ctx, id, domain.RoleMaintainer)
}

// CreateEnvironment creates an authorized environment.
func (s *Service) CreateEnvironment(ctx context.Context, in EnvironmentInput) (domain.Environment, error) {
	r, e := s.deploymentRepo()
	if e != nil {
		return domain.Environment{}, e
	}
	if e = s.requireServiceMutation(ctx, in.ServiceID); e != nil {
		return domain.Environment{}, e
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.ComposeProject) == "" || in.TimeoutSeconds <= 0 {
		return domain.Environment{}, errors.New("name, compose_project, and positive timeout_seconds are required")
	}
	for _, binding := range in.SecretBindings {
		if err := runner.ValidateSecretBinding(binding); err != nil {
			return domain.Environment{}, errors.New("invalid environment secret binding")
		}
	}
	service, e := r.GetService(ctx, in.ServiceID)
	if e != nil {
		return domain.Environment{}, e
	}
	repositories, e := s.sources.ListRepositories(ctx, service.ProjectID)
	if e != nil {
		return domain.Environment{}, e
	}
	for _, repository := range repositories {
		if repository.ID != service.RepositoryID {
			continue
		}
		credentialReference := strings.TrimSpace(repository.Policy.CredentialReferenceID)
		for _, binding := range in.SecretBindings {
			if credentialReference != "" && strings.EqualFold(strings.TrimSpace(binding.Provider), domain.ProviderRunnerFile) && strings.TrimSpace(binding.Reference) == credentialReference && strings.HasPrefix(strings.TrimSpace(binding.Target), "file:") {
				return domain.Environment{}, errors.New("repository credential binding must use an env target")
			}
		}
		break
	}
	id, e := prefixedID("env")
	if e != nil {
		return domain.Environment{}, e
	}
	p, _ := s.CurrentPrincipal(ctx)
	v := domain.Environment{ID: id, ServiceID: in.ServiceID, Name: strings.TrimSpace(in.Name), RunnerSelector: in.RunnerSelector, ComposeProject: strings.TrimSpace(in.ComposeProject), HealthPolicy: in.HealthPolicy, ConfirmationRequired: in.ConfirmationRequired, TimeoutSeconds: in.TimeoutSeconds, SecretBindings: in.SecretBindings, RollbackSafe: in.RollbackSafe, CreatedAt: time.Now().UTC()}
	audit, e := s.auditEvent(ctx, p.ID, "environment.create", v.ID, nil)
	if e != nil {
		return domain.Environment{}, e
	}
	return r.CreateEnvironment(ctx, v, store.WithAudit(audit))
}

// ListRevisions lists authorized revisions.
func (s *Service) ListRevisions(ctx context.Context, serviceID string) ([]domain.Revision, error) {
	if e := s.requireServiceView(ctx, serviceID); e != nil {
		return nil, e
	}
	r, e := s.deploymentRepo()
	if e != nil {
		return nil, e
	}
	return r.ListRevisions(ctx, serviceID)
}

// CreateRevision creates an authorized revision.
func (s *Service) CreateRevision(ctx context.Context, in RevisionInput) (domain.Revision, error) {
	if e := s.requireServiceMutation(ctx, in.ServiceID); e != nil {
		return domain.Revision{}, e
	}
	if strings.TrimSpace(in.RequestedRef) == "" {
		return domain.Revision{}, errors.New("requested_ref is required")
	}
	r, e := s.deploymentRepo()
	if e != nil {
		return domain.Revision{}, e
	}
	id, e := prefixedID("rev")
	if e != nil {
		return domain.Revision{}, e
	}
	p, _ := s.CurrentPrincipal(ctx)
	// A maintainer requests a ref; the runner binds the immutable commit,
	// normalized compose hash and image digests under an active attempt.
	v := domain.Revision{ID: id, ServiceID: in.ServiceID, RequestedRef: strings.TrimSpace(in.RequestedRef), ProvenanceState: "pending", CreatedBy: p.ID, CreatedAt: time.Now().UTC()}
	audit, e := s.auditEvent(ctx, p.ID, "revision.create", v.ID, nil)
	if e != nil {
		return domain.Revision{}, e
	}
	return r.CreateRevision(ctx, v, store.WithAudit(audit))
}

// ListDeployments lists authorized deployments.
func (s *Service) ListDeployments(ctx context.Context, eid string) ([]domain.Deployment, error) {
	r, e := s.deploymentRepo()
	if e != nil {
		return nil, e
	}
	es, e := r.ListEnvironments(ctx, "")
	if e != nil {
		return nil, e
	}
	for _, x := range es {
		if x.ID == eid {
			if e = s.requireServiceView(ctx, x.ServiceID); e != nil {
				return nil, e
			}
		}
	}
	return r.ListDeployments(ctx, eid)
}

// GetDeployment resolves one public control-plane record by its stable ID.
// The lookup is direct rather than an unbounded deployment list scan. Access
// denial is normalized to not-found, preventing cross-project enumeration.
// GetDeployment returns the authorized deployment.
func (s *Service) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	r, err := s.deploymentRepo()
	if err != nil {
		return domain.Deployment{}, err
	}
	deployment, err := r.GetDeployment(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Deployment{}, err
	}
	environment, err := r.GetEnvironment(ctx, deployment.EnvironmentID)
	if err != nil {
		return domain.Deployment{}, store.ErrNotFound
	}
	if err := s.requireServiceView(ctx, environment.ServiceID); err != nil {
		return domain.Deployment{}, store.ErrNotFound
	}
	return deployment, nil
}

// CreateDeployment creates an authorized deployment.
func (s *Service) CreateDeployment(ctx context.Context, in DeploymentInput) (domain.Deployment, error) {
	r, e := s.deploymentRepo()
	if e != nil {
		return domain.Deployment{}, e
	}
	es, e := r.ListEnvironments(ctx, "")
	if e != nil {
		return domain.Deployment{}, e
	}
	var env domain.Environment
	for _, x := range es {
		if x.ID == in.EnvironmentID {
			env = x
		}
	}
	if env.ID == "" {
		return domain.Deployment{}, store.ErrNotFound
	}
	if e = s.requireServiceMutation(ctx, env.ServiceID); e != nil {
		return domain.Deployment{}, e
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || strings.TrimSpace(in.DesiredRevisionID) == "" {
		return domain.Deployment{}, errors.New("idempotency_key and desired_revision_id are required")
	}
	if in.TaskRunID != nil {
		return domain.Deployment{}, errors.New("task_run_id is server assigned")
	}
	if in.RollbackOfID != nil {
		return domain.Deployment{}, store.ErrConflict
	}
	revisions, e := r.ListRevisions(ctx, env.ServiceID)
	if e != nil {
		return domain.Deployment{}, e
	}
	validRevision := false
	for _, revision := range revisions {
		if revision.ID == in.DesiredRevisionID {
			validRevision = true
			break
		}
	}
	if !validRevision {
		return domain.Deployment{}, store.ErrNotFound
	}
	eligible := false
	runners, e := s.runners.ListRunners(ctx)
	if e != nil {
		return domain.Deployment{}, e
	}
	for _, runner := range runners {
		if runner.Status == domain.RunnerActive && hasDeploymentRunnerEligibility(runner.Tags, env.RunnerSelector, runner.Capabilities) {
			eligible = true
			break
		}
	}
	if !eligible {
		return domain.Deployment{}, errors.New("no active selected runner advertises compose-deploy")
	}
	p, _ := s.CurrentPrincipal(ctx)
	id, e := prefixedID("dep")
	if e != nil {
		return domain.Deployment{}, e
	}
	status := domain.DeploymentQueued
	runStatus := domain.RunQueued
	if env.ConfirmationRequired {
		status = domain.DeploymentWaitingConfirmation
		runStatus = domain.RunWaitingApproval
	}
	services, e := r.ListServices(ctx, "")
	if e != nil {
		return domain.Deployment{}, e
	}
	projectID := ""
	for _, service := range services {
		if service.ID == env.ServiceID {
			projectID = service.ProjectID
			break
		}
	}
	if projectID == "" {
		return domain.Deployment{}, store.ErrNotFound
	}
	// The server-owned compose run spec is the authority used later by the
	// fenced secret-access endpoint. Bind only the environment's declared
	// runner_file credential matching this repository policy; never let a
	// runner turn an arbitrary policy reference into a readable secret.
	var service domain.Service
	for _, candidate := range services {
		if candidate.ID == env.ServiceID {
			service = candidate
			break
		}
	}
	repositories, e := s.sources.ListRepositories(ctx, projectID)
	if e != nil {
		return domain.Deployment{}, e
	}
	var repository domain.Repository
	for _, candidate := range repositories {
		if candidate.ID == service.RepositoryID {
			repository = candidate
			break
		}
	}
	credentialReference := strings.TrimSpace(repository.Policy.CredentialReferenceID)
	if credentialReference != "" {
		matches := 0
		for _, binding := range env.SecretBindings {
			if strings.TrimSpace(binding.Reference) == credentialReference && strings.EqualFold(strings.TrimSpace(binding.Provider), domain.ProviderRunnerFile) {
				matches++
			}
		}
		if matches != 1 {
			return domain.Deployment{}, errors.New("compose repository credential must match exactly one environment runner_file binding")
		}
	}
	runID, e := prefixedID("run")
	if e != nil {
		return domain.Deployment{}, e
	}
	now := time.Now().UTC()
	// compose-deploy is a typed, intentionally non-shell plan. Slice 12 owns
	// its adapter; this slice only creates the fenced execution authority.
	run := domain.TaskRun{ID: runID, ProjectID: projectID, RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": id, "environment_id": env.ID, "desired_revision_id": in.DesiredRevisionID}, Secrets: append([]domain.SecretBinding(nil), env.SecretBindings...)}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{}}, WorkflowState: domain.WorkflowState{Steps: []domain.WorkflowStepState{}}, RunnerTags: env.RunnerSelector, Status: runStatus, RequestedBy: p.ID, StartedAt: now}
	return r.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: env.ID, DesiredRevisionID: in.DesiredRevisionID, IdempotencyKey: in.IdempotencyKey, Status: status, RequestedBy: p.ID, FenceRequired: true, CreatedAt: now, UpdatedAt: now}, run, domain.AuditEvent{ID: mustID("audit"), ActorID: p.ID, Action: "deployment.create", TargetID: id, Metadata: map[string]any{"idempotency_key": in.IdempotencyKey, "run_id": runID, "run_type": domain.RunTypeComposeDeploy}})
}

func hasDeploymentRunnerEligibility(tags, selector, capabilities []string) bool {
	for _, required := range selector {
		found := false
		for _, tag := range tags {
			if tag == required {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, capability := range capabilities {
		if capability == domain.RunTypeComposeDeploy {
			return true
		}
	}
	return false
}

// ConfirmDeployment confirms an authorized deployment request.
func (s *Service) ConfirmDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.Deployment{}, err
	}
	deployments, err := repo.ListDeployments(ctx, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	var deployment domain.Deployment
	for _, candidate := range deployments {
		if candidate.ID == strings.TrimSpace(id) {
			deployment = candidate
			break
		}
	}
	if deployment.ID == "" {
		return domain.Deployment{}, store.ErrNotFound
	}
	environments, err := repo.ListEnvironments(ctx, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	for _, environment := range environments {
		if environment.ID == deployment.EnvironmentID {
			if err = s.requireServiceMutation(ctx, environment.ServiceID); err != nil {
				return domain.Deployment{}, err
			}
			break
		}
	}
	p, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	audit, err := s.auditEvent(ctx, p.ID, "deployment.confirm", deployment.ID, map[string]any{"run_id": deployment.TaskRunID})
	if err != nil {
		return domain.Deployment{}, err
	}
	return repo.ConfirmDeployment(ctx, deployment.ID, p.ID, audit)
}

// CancelDeployment is the only maintainer cancellation entry point for a
// deployment-owned run.  Generic /runs/cancel remains intentionally rejected
// for these runs because it cannot preserve the environment lock lifecycle.
// CancelDeployment requests cancellation of an authorized deployment.
func (s *Service) CancelDeployment(ctx context.Context, in DeploymentCancelInput) (domain.Deployment, error) {
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.Deployment{}, err
	}
	id, requestID := strings.TrimSpace(in.DeploymentID), strings.TrimSpace(in.RequestID)
	if id == "" || requestID == "" {
		return domain.Deployment{}, errors.New("deployment_id and request_id are required")
	}
	deployment, err := repo.GetDeployment(ctx, id)
	if err != nil {
		return domain.Deployment{}, err
	}
	environments, err := repo.ListEnvironments(ctx, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	var env domain.Environment
	for _, candidate := range environments {
		if candidate.ID == deployment.EnvironmentID {
			env = candidate
			break
		}
	}
	if env.ID == "" {
		return domain.Deployment{}, store.ErrNotFound
	}
	if err = s.requireServiceMutation(ctx, env.ServiceID); err != nil {
		return domain.Deployment{}, err
	}
	p, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	audit, err := s.auditEvent(ctx, p.ID, "deployment.cancel", id, map[string]any{"cancellation_request_id": requestID})
	if err != nil {
		return domain.Deployment{}, err
	}
	return repo.CancelDeploymentRequest(ctx, domain.DeploymentCancelRequest{DeploymentID: id, RequestID: requestID, ActorID: p.ID}, audit)
}

// PreAssignmentFailureInput records a deployment failure before assignment.
type PreAssignmentFailureInput struct {
	ID          string `json:"id"`
	FailureCode string `json:"failure_code"`
}

// FailPreAssignmentDeployment records a deployment failure before runner assignment.
func (s *Service) FailPreAssignmentDeployment(ctx context.Context, in PreAssignmentFailureInput) (domain.Deployment, error) {
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.Deployment{}, err
	}
	id, failureCode := strings.TrimSpace(in.ID), strings.TrimSpace(in.FailureCode)
	if id == "" || failureCode == "" {
		return domain.Deployment{}, errors.New("id and failure_code are required")
	}
	deployments, err := repo.ListDeployments(ctx, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	var deployment domain.Deployment
	for _, candidate := range deployments {
		if candidate.ID == id {
			deployment = candidate
			break
		}
	}
	if deployment.ID == "" {
		return domain.Deployment{}, store.ErrNotFound
	}
	environments, err := repo.ListEnvironments(ctx, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	for _, environment := range environments {
		if environment.ID == deployment.EnvironmentID {
			if err = s.requireServiceMutation(ctx, environment.ServiceID); err != nil {
				return domain.Deployment{}, err
			}
			break
		}
	}
	p, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	audit, err := s.auditEvent(ctx, p.ID, "deployment.preassignment_fail", deployment.ID, map[string]any{"failure_code": failureCode, "run_id": deployment.TaskRunID})
	if err != nil {
		return domain.Deployment{}, err
	}
	return repo.FailPreAssignmentDeployment(ctx, deployment.ID, failureCode, audit)
}
func mustID(prefix string) string { id, _ := prefixedID(prefix); return id }

// DeploymentTransitionInput supplies an authenticated deployment state transition.
type DeploymentTransitionInput struct {
	DeploymentID   string                  `json:"deployment_id"`
	RunID          string                  `json:"run_id"`
	LeaseID        string                  `json:"lease_id"`
	Attempt        int                     `json:"attempt"`
	Fence          string                  `json:"fence"`
	TransitionKey  string                  `json:"transition_key"`
	ExpectedStatus domain.DeploymentStatus `json:"expected_status"`
	TargetStatus   domain.DeploymentStatus `json:"target_status"`
	HealthPassed   *bool                   `json:"health_passed"`
	FailureCode    string                  `json:"failure_code"`
	Metadata       map[string]any          `json:"metadata"`
}

// DeploymentFailureRollbackInput supplies failure details for rollback creation.
type DeploymentFailureRollbackInput struct {
	DeploymentID          string                  `json:"deployment_id"`
	RunID                 string                  `json:"run_id"`
	LeaseID               string                  `json:"lease_id"`
	Attempt               int                     `json:"attempt"`
	Fence                 string                  `json:"fence"`
	RequestID             string                  `json:"request_id"`
	ExpectedStatus        domain.DeploymentStatus `json:"expected_status"`
	CancellationRequestID string                  `json:"cancellation_request_id,omitempty"`
	FailureCode           string                  `json:"failure_code"`
	Metadata              map[string]any          `json:"metadata"`
}

// FailDeploymentAndCreateRollback records failure and creates a rollback request.
func (s *Service) FailDeploymentAndCreateRollback(ctx context.Context, in DeploymentFailureRollbackInput) (domain.DeploymentFailureRollbackResult, error) {
	p, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	req := domain.DeploymentFailureRollbackRequest{DeploymentID: strings.TrimSpace(in.DeploymentID), RunID: strings.TrimSpace(in.RunID), LeaseID: strings.TrimSpace(in.LeaseID), RunnerID: p.ID, Attempt: in.Attempt, Fence: strings.TrimSpace(in.Fence), RequestID: strings.TrimSpace(in.RequestID), ExpectedStatus: strings.TrimSpace(in.ExpectedStatus), CancellationRequestID: strings.TrimSpace(in.CancellationRequestID), FailureCode: strings.TrimSpace(in.FailureCode), Metadata: in.Metadata}
	if req.DeploymentID == "" || req.RunID == "" || req.LeaseID == "" || req.Attempt <= 0 || req.Fence == "" || req.RequestID == "" || req.FailureCode == "" || (req.ExpectedStatus != domain.DeploymentApplying && req.ExpectedStatus != domain.DeploymentVerifying && req.ExpectedStatus != domain.DeploymentCancelRequested) || (req.ExpectedStatus == domain.DeploymentCancelRequested && req.CancellationRequestID == "") {
		return domain.DeploymentFailureRollbackResult{}, errors.New("deployment_id, run_id, lease_id, attempt, fence, request_id, expected_status, and failure_code are required")
	}
	// The runner cannot select either recovery object. Stable server-derived IDs
	// make a response-loss retry replay the same durable rollback receipt.
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	action := "runner.deployment.failed"
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		action = "runner.deployment.cancellation_rollback"
	}
	failedAudit, err := s.auditEvent(ctx, p.ID, action, req.DeploymentID, map[string]any{"run_id": req.RunID, "lease_id": req.LeaseID, "attempt": req.Attempt, "failure_code": req.FailureCode, "cancellation_request_id": req.CancellationRequestID})
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	rollbackAudit, err := s.auditEvent(ctx, p.ID, "runner.deployment.rollback_queued", rollbackDeploymentID, map[string]any{"source_deployment_id": req.DeploymentID, "rollback_run_id": rollbackRunID})
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	// PostgreSQL stores timestamps at microsecond precision. Preserve the
	// causal order even when the in-process clock advanced by less than one
	// stored tick: the random IDs are not an ordering mechanism.
	rollbackAudit.CreatedAt = failedAudit.CreatedAt.Add(time.Microsecond)
	result, err := repo.FailDeploymentAndCreateRollback(ctx, req, failedAudit, rollbackAudit)
	if errors.Is(err, store.ErrNotFound) {
		return domain.DeploymentFailureRollbackResult{}, auth.ErrForbidden
	}
	return result, err
}

// TransitionDeploymentAttempt applies an authorized deployment lifecycle transition.
func (s *Service) TransitionDeploymentAttempt(ctx context.Context, in DeploymentTransitionInput) (domain.Deployment, error) {
	p, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.Deployment{}, err
	}
	req := domain.DeploymentTransitionRequest{DeploymentID: strings.TrimSpace(in.DeploymentID), RunID: strings.TrimSpace(in.RunID), LeaseID: strings.TrimSpace(in.LeaseID), RunnerID: p.ID, Attempt: in.Attempt, Fence: strings.TrimSpace(in.Fence), TransitionKey: strings.TrimSpace(in.TransitionKey), ExpectedStatus: strings.TrimSpace(in.ExpectedStatus), TargetStatus: strings.TrimSpace(in.TargetStatus), HealthPassed: in.HealthPassed, FailureCode: strings.TrimSpace(in.FailureCode), Metadata: in.Metadata}
	if req.DeploymentID == "" || req.RunID == "" || req.LeaseID == "" || req.Attempt <= 0 || req.Fence == "" || req.TransitionKey == "" || req.ExpectedStatus == "" || req.TargetStatus == "" {
		return domain.Deployment{}, errors.New("deployment_id, run_id, lease_id, attempt, fence, transition_key, expected_status, and target_status are required")
	}
	deployment, err := repo.GetDeployment(ctx, req.DeploymentID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Deployment{}, auth.ErrForbidden
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if !fencedDeploymentTransitionAllowed(deployment.RollbackOfID != nil, req.ExpectedStatus, req.TargetStatus) || (req.TargetStatus == domain.DeploymentSucceeded && (req.HealthPassed == nil || !*req.HealthPassed)) {
		return domain.Deployment{}, store.ErrConflict
	}
	audit, err := s.auditEvent(ctx, p.ID, "runner.deployment.transition", req.DeploymentID, map[string]any{"run_id": req.RunID, "lease_id": req.LeaseID, "attempt": req.Attempt, "expected_status": req.ExpectedStatus, "target_status": req.TargetStatus, "failure_code": req.FailureCode})
	if err != nil {
		return domain.Deployment{}, err
	}
	deployed, err := repo.TransitionDeploymentAttempt(ctx, req, audit)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Deployment{}, auth.ErrForbidden
	}
	return deployed, err
}

// DeploymentPlanInput identifies the runner lease requesting a deployment plan.
type DeploymentPlanInput struct {
	DeploymentID string `json:"deployment_id"`
	RunID        string `json:"run_id"`
	LeaseID      string `json:"lease_id"`
	Attempt      int    `json:"attempt"`
	Fence        string `json:"fence"`
}

// RunnerDeploymentPlan returns no user credentials or secret material.  The
// store verifies all lease identity fields against its database clock before
// returning the plan.
// RunnerDeploymentPlan returns the authorized deployment plan.
func (s *Service) RunnerDeploymentPlan(ctx context.Context, in DeploymentPlanInput) (domain.DeploymentPlan, error) {
	p, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.DeploymentPlan{}, err
	}
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.DeploymentPlan{}, err
	}
	plan, err := repo.DeploymentPlan(ctx, strings.TrimSpace(in.DeploymentID), strings.TrimSpace(in.RunID), strings.TrimSpace(in.LeaseID), p.ID, in.Attempt, strings.TrimSpace(in.Fence))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.DeploymentPlan{}, auth.ErrForbidden
		}
		return domain.DeploymentPlan{}, err
	}
	if p.ID == "" {
		return domain.DeploymentPlan{}, auth.ErrForbidden
	}
	return plan, nil
}

// RunnerDeploymentStatus is the bounded, read-only cancellation watcher
// capability. It authenticates the complete live fence through DeploymentPlan
// and returns only authoritative status plus the server-issued cancel receipt.
// RunnerDeploymentStatus returns the authorized deployment status.
func (s *Service) RunnerDeploymentStatus(ctx context.Context, in DeploymentPlanInput) (domain.DeploymentPlan, error) {
	return s.RunnerDeploymentPlan(ctx, in)
}

// ProvenanceResolutionInput supplies verified revision provenance for a deployment.
type ProvenanceResolutionInput struct {
	DeploymentID    string   `json:"deployment_id"`
	RunID           string   `json:"run_id"`
	LeaseID         string   `json:"lease_id"`
	Attempt         int      `json:"attempt"`
	Fence           string   `json:"fence"`
	ResolutionID    string   `json:"resolution_id"`
	GitCommit       string   `json:"git_commit"`
	ComposeHash     string   `json:"compose_hash"`
	ImageDigests    []string `json:"image_digests"`
	ContentIdentity string   `json:"content_identity"`
}

func validCommit(v string) bool {
	if len(v) < 40 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validDigest(v string) bool {
	parts := strings.Split(v, ":")
	if len(parts) != 2 || parts[0] != "sha256" || len(parts[1]) != 64 {
		return false
	}
	for _, c := range parts[1] {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validImageReference(v string) bool {
	repository, digest, found := strings.Cut(v, "@sha256:")
	return found && immutableImageReferencePattern.MatchString(v) && strings.LastIndex(repository, ":") <= strings.LastIndex(repository, "/") && validDigest("sha256:"+digest)
}

// ResolveDeploymentProvenance records verified deployment provenance.
func (s *Service) ResolveDeploymentProvenance(ctx context.Context, in ProvenanceResolutionInput) (domain.Revision, error) {
	p, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.Revision{}, err
	}
	commit, hash := strings.TrimSpace(in.GitCommit), strings.TrimSpace(in.ComposeHash)
	if strings.TrimSpace(in.ResolutionID) == "" || strings.TrimSpace(in.ContentIdentity) != commit+":"+hash || !validCommit(commit) || !validDigest(hash) || len(in.ImageDigests) == 0 {
		return domain.Revision{}, errors.New("git_commit and compose_hash must be immutable sha256 values and image_digests must be non-empty repository digests")
	}
	digests := append([]string(nil), in.ImageDigests...)
	for i, d := range digests {
		digests[i] = strings.TrimSpace(d)
		if !validImageReference(digests[i]) {
			return domain.Revision{}, errors.New("image_digests must be repository@sha256 digests")
		}
	}
	for i := 1; i < len(digests); i++ {
		if digests[i-1] >= digests[i] {
			return domain.Revision{}, errors.New("image_digests must be strictly sorted")
		}
	}
	repo, err := s.deploymentRepo()
	if err != nil {
		return domain.Revision{}, err
	}
	audit, err := s.auditEvent(ctx, p.ID, "runner.deployment.provenance.resolve", strings.TrimSpace(in.DeploymentID), map[string]any{"run_id": in.RunID, "lease_id": in.LeaseID, "attempt": in.Attempt, "git_commit": commit, "compose_hash": hash, "image_digests": digests})
	if err != nil {
		return domain.Revision{}, err
	}
	v, err := repo.ResolveRevisionProvenance(ctx, strings.TrimSpace(in.DeploymentID), strings.TrimSpace(in.RunID), strings.TrimSpace(in.LeaseID), p.ID, in.Attempt, strings.TrimSpace(in.Fence), strings.TrimSpace(in.ResolutionID), commit, hash, digests, audit)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Revision{}, auth.ErrForbidden
	}
	return v, err
}

func fencedDeploymentTransitionAllowed(isRollbackChild bool, from, to domain.DeploymentStatus) bool {
	return domain.DeploymentRoleTransitionAllowed(isRollbackChild, from, to)
}
