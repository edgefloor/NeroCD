package store

import (
	"context"
	"reflect"
	"strings"
	"time"

	"nerocd/internal/domain"
)

func (s *MemoryStore) ListServices(_ context.Context, projectID string) ([]domain.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Service
	for _, v := range s.services {
		if projectID == "" || v.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateService(_ context.Context, v domain.Service) (domain.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.services {
		if x.ID == v.ID || (x.ProjectID == v.ProjectID && x.Name == v.Name) {
			return domain.Service{}, ErrConflict
		}
	}
	s.services = append(s.services, v)
	if s.serviceByID == nil {
		s.serviceByID = map[string]int{}
	}
	s.serviceByID[v.ID] = len(s.services) - 1
	return v, nil
}
func (s *MemoryStore) CreateServiceWithAudit(_ context.Context, v domain.Service, audit domain.AuditEvent) (domain.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.services {
		if x.ID == v.ID || (x.ProjectID == v.ProjectID && x.Name == v.Name) {
			return domain.Service{}, ErrConflict
		}
	}
	s.services = append(s.services, v)
	if s.serviceByID == nil {
		s.serviceByID = map[string]int{}
	}
	s.serviceByID[v.ID] = len(s.services) - 1
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}
func (s *MemoryStore) GetService(_ context.Context, id string) (domain.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.serviceByID[id]
	if !ok || index < 0 || index >= len(s.services) || s.services[index].ID != id {
		return domain.Service{}, ErrNotFound
	}
	return s.services[index], nil
}
func (s *MemoryStore) ListEnvironments(_ context.Context, serviceID string) ([]domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Environment
	for _, v := range s.environments {
		if serviceID == "" || v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateEnvironment(_ context.Context, v domain.Environment) (domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.environments {
		if x.ID == v.ID || (x.ServiceID == v.ServiceID && x.Name == v.Name) {
			return domain.Environment{}, ErrConflict
		}
	}
	s.environments = append(s.environments, v)
	if s.environmentByID == nil {
		s.environmentByID = map[string]int{}
	}
	s.environmentByID[v.ID] = len(s.environments) - 1
	return v, nil
}
func (s *MemoryStore) CreateEnvironmentWithAudit(_ context.Context, v domain.Environment, audit domain.AuditEvent) (domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.environments {
		if x.ID == v.ID || (x.ServiceID == v.ServiceID && x.Name == v.Name) {
			return domain.Environment{}, ErrConflict
		}
	}
	s.environments = append(s.environments, v)
	if s.environmentByID == nil {
		s.environmentByID = map[string]int{}
	}
	s.environmentByID[v.ID] = len(s.environments) - 1
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}
func (s *MemoryStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.environmentByID[id]
	if !ok || index < 0 || index >= len(s.environments) || s.environments[index].ID != id {
		return domain.Environment{}, ErrNotFound
	}
	return s.environments[index], nil
}
func (s *MemoryStore) ListRevisions(_ context.Context, serviceID string) ([]domain.Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Revision
	for _, v := range s.revisions {
		if serviceID == "" || v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateRevision(_ context.Context, v domain.Revision) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.revisions {
		if x.ID == v.ID || (v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity) {
			if v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity {
				return x, nil
			}
			return domain.Revision{}, ErrConflict
		}
	}
	// Direct repository callers (fixtures/imports) may supply complete evidence;
	// application creates deliberately leave it pending for the runner.
	if v.GitCommit != "" && v.ComposeHash != "" {
		v.ProvenanceResolved = true
		v.ProvenanceState = "legacy_unverified"
		now := time.Now().UTC()
		v.ResolvedAt = &now
	}
	if v.ProvenanceState == "" {
		v.ProvenanceState = "pending"
	}
	s.revisions = append(s.revisions, v)
	return v, nil
}
func (s *MemoryStore) CreateRevisionWithAudit(_ context.Context, v domain.Revision, audit domain.AuditEvent) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.revisions {
		if x.ID == v.ID || (v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity) {
			if v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity {
				return x, nil
			}
			return domain.Revision{}, ErrConflict
		}
	}
	s.revisions = append(s.revisions, v)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}

func (s *MemoryStore) DeploymentPlan(_ context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence string) (domain.DeploymentPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	var deployment domain.Deployment
	for _, d := range s.deployments {
		if d.ID == deploymentID && d.TaskRunID != nil && *d.TaskRunID == runID {
			deployment = d
			break
		}
	}
	if deployment.ID == "" {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	valid := false
	for _, l := range s.leases {
		if l.ID == leaseID && l.RunID == runID && l.RunnerID == runnerID && l.Attempt == attempt && l.Fence == fence && l.Status == domain.LeaseActive && l.ExpiresAt.After(now) {
			valid = true
		}
	}
	if !valid {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	var env domain.Environment
	for _, x := range s.environments {
		if x.ID == deployment.EnvironmentID {
			env = x
			break
		}
	}
	var service domain.Service
	for _, x := range s.services {
		if x.ID == env.ServiceID {
			service = x
			break
		}
	}
	var revision domain.Revision
	for _, x := range s.revisions {
		if x.ID == deployment.DesiredRevisionID {
			revision = x
			break
		}
	}
	var repository domain.Repository
	for _, x := range s.repositories {
		if x.ID == service.RepositoryID {
			repository = x
			break
		}
	}
	if env.ID == "" || service.ID == "" || revision.ID == "" || repository.ID == "" {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	requestedRef := revision.RequestedRef
	// A resolved revision is durable provenance. Replays and rollback children
	// must use that commit even if the original requested branch has advanced.
	if immutableGitCommit(revision.GitCommit) {
		requestedRef = revision.GitCommit
	}
	var cancellationRequestID *string
	for key, receipt := range s.deploymentCancels {
		if strings.HasPrefix(key, deploymentID+"\x00") {
			value := receipt.RequestID
			cancellationRequestID = &value
			break
		}
	}
	return domain.DeploymentPlan{DeploymentID: deployment.ID, Status: deployment.Status, RunID: runID, LeaseID: leaseID, Attempt: attempt, Fence: fence, ProjectID: service.ProjectID, ServiceID: service.ID, EnvironmentID: env.ID, RepositoryID: repository.ID, RepositoryURL: repository.URL, RepositoryPolicy: repository.Policy, RequestedRef: requestedRef, ComposePath: service.ComposePath, Profiles: append([]string(nil), service.Profiles...), ComposeProject: env.ComposeProject, TimeoutSeconds: env.TimeoutSeconds, HealthPolicy: env.HealthPolicy, SecretBindings: append([]domain.SecretBinding(nil), env.SecretBindings...), RollbackSafe: env.RollbackSafe, PreviousHealthyRevisionID: deployment.PreviousHealthyRevisionID, RollbackOfID: deployment.RollbackOfID, CancellationRequestID: cancellationRequestID}, nil
}

func (s *MemoryStore) ResolveRevisionProvenance(_ context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence, resolutionID, commit, hash string, digests []string, audit domain.AuditEvent) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deploymentID + "\x00" + resolutionID
	if replay, ok := s.provenanceReplays[key]; ok {
		if replay.runID == runID && replay.leaseID == leaseID && replay.runnerID == runnerID && replay.attempt == attempt && replay.fence == fence && replay.commit == commit && replay.hash == hash && reflect.DeepEqual(replay.digests, digests) {
			return replay.revision, nil
		}
		return domain.Revision{}, ErrConflict
	}
	now := time.Now().UTC()
	valid := false
	for _, da := range s.deploymentAttempts {
		if da.DeploymentID == deploymentID && da.RunID == runID && da.LeaseID == leaseID && da.RunnerID == runnerID && da.Attempt == attempt && da.Fence == fence {
			valid = true
			break
		}
	}
	if !valid {
		return domain.Revision{}, ErrNotFound
	}
	valid = false
	for _, l := range s.leases {
		if l.ID == leaseID && l.RunID == runID && l.RunnerID == runnerID && l.Attempt == attempt && l.Fence == fence && l.Status == domain.LeaseActive && l.ExpiresAt.After(now) {
			valid = true
		}
	}
	if !valid {
		return domain.Revision{}, ErrNotFound
	}
	for _, d := range s.deployments {
		if d.ID == deploymentID && d.TaskRunID != nil && *d.TaskRunID == runID {
			if d.Status != domain.DeploymentAssigned && d.Status != domain.DeploymentPreparing && d.Status != domain.DeploymentApplying && d.Status != domain.DeploymentVerifying {
				return domain.Revision{}, ErrNotFound
			}
			for i := range s.revisions {
				r := &s.revisions[i]
				if r.ID != d.DesiredRevisionID {
					continue
				}
				if r.ProvenanceResolved {
					// Rollback children reuse an already attested revision but still
					// need a per-attempt provenance receipt under their new fence.
					if r.GitCommit != commit || r.ComposeHash != hash || !reflect.DeepEqual(r.ImageDigests, digests) {
						return domain.Revision{}, ErrConflict
					}
				} else {
					if r.ProvenanceState != "pending" {
						return domain.Revision{}, ErrConflict
					}
					r.GitCommit, r.ComposeHash, r.ImageDigests = commit, hash, append([]string(nil), digests...)
					r.ContentIdentity = commit + ":" + hash
					r.ProvenanceResolved, r.ProvenanceState = true, "resolved"
					r.ResolvedAt = &now
				}
				if audit.ID != "" {
					audit.CreatedAt = now
					s.auditEvents = append(s.auditEvents, audit)
				}
				s.provenanceReplays[key] = memoryProvenanceReplay{deploymentID: deploymentID, resolutionID: resolutionID, runID: runID, leaseID: leaseID, runnerID: runnerID, attempt: attempt, fence: fence, commit: commit, hash: hash, digests: append([]string(nil), digests...), revision: *r}
				return *r, nil
			}
		}
	}
	return domain.Revision{}, ErrNotFound
}
func (s *MemoryStore) ListDeployments(_ context.Context, environmentID string) ([]domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Deployment
	for _, v := range s.deployments {
		if environmentID == "" || v.EnvironmentID == environmentID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *MemoryStore) deploymentBackedRunLocked(runID string) bool {
	for _, deployment := range s.deployments {
		if deployment.TaskRunID != nil && *deployment.TaskRunID == runID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) CreateDeploymentRequest(_ context.Context, v domain.Deployment, run domain.TaskRun, a domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.RollbackOfID != nil {
		return domain.Deployment{}, ErrConflict
	}
	var environment domain.Environment
	for _, candidate := range s.environments {
		if candidate.ID == v.EnvironmentID {
			environment = candidate
			break
		}
	}
	if environment.ID == "" {
		return domain.Deployment{}, ErrNotFound
	}
	validRevision := false
	for _, revision := range s.revisions {
		if revision.ID == v.DesiredRevisionID && revision.ServiceID == environment.ServiceID {
			validRevision = true
			break
		}
	}
	if !validRevision {
		return domain.Deployment{}, ErrNotFound
	}
	for _, existing := range s.deployments {
		if existing.EnvironmentID == v.EnvironmentID && existing.IdempotencyKey == v.IdempotencyKey {
			if existing.DesiredRevisionID == v.DesiredRevisionID {
				return existing, nil
			}
			return domain.Deployment{}, ErrConflict
		}
		if existing.EnvironmentID == v.EnvironmentID && !domain.IsTerminalDeploymentStatus(existing.Status) {
			return domain.Deployment{}, ErrConflict
		}
	}
	for _, existing := range s.runs {
		if existing.ID == run.ID {
			return domain.Deployment{}, ErrConflict
		}
	}
	v.TaskRunID = stringPointer(run.ID)
	v.PreviousHealthyRevisionID = environment.CurrentHealthyRevisionID
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = map[string]time.Time{}
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.deployments = append(s.deployments, v)
	if s.deploymentByID == nil {
		s.deploymentByID = map[string]int{}
	}
	s.deploymentByID[v.ID] = len(s.deployments) - 1
	s.auditEvents = append([]domain.AuditEvent{a}, s.auditEvents...)
	return v, nil
}
func deploymentTransitionAllowed(from, to domain.DeploymentStatus) bool {
	return domain.DeploymentTransitionAllowed(from, to)
}

func (s *MemoryStore) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.deploymentByID[id]
	if !ok || index < 0 || index >= len(s.deployments) || s.deployments[index].ID != id {
		return domain.Deployment{}, ErrNotFound
	}
	return s.deployments[index], nil
}

func (s *MemoryStore) ConfirmDeployment(_ context.Context, id, confirmedBy string, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, deployment := range s.deployments {
		if deployment.ID != id {
			continue
		}
		if deployment.Status != domain.DeploymentWaitingConfirmation || deployment.TaskRunID == nil {
			return domain.Deployment{}, ErrConflict
		}
		now := time.Now().UTC()
		deployment.Status = domain.DeploymentAssigned
		deployment.ConfirmedBy = stringPointer(confirmedBy)
		deployment.UpdatedAt = now
		s.deployments[i] = deployment
		for j, run := range s.runs {
			if run.ID == *deployment.TaskRunID && run.Status == domain.RunWaitingApproval {
				run.Status = domain.RunQueued
				s.runs[j] = run
				s.claimOrderByRun[run.ID] = now
			}
		}
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// FailPreAssignmentDeployment records a maintainer-side validation failure
// before any runner owns a deployment. Assigned work must use the fenced
// runner transition protocol instead.
func (s *MemoryStore) FailPreAssignmentDeployment(_ context.Context, id, failureCode string, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, deployment := range s.deployments {
		if deployment.ID != id {
			continue
		}
		if deployment.Status == domain.DeploymentFailed {
			if deployment.FailureCode == failureCode {
				return deployment, nil
			}
			return domain.Deployment{}, ErrConflict
		}
		if (deployment.Status != domain.DeploymentQueued && deployment.Status != domain.DeploymentWaitingConfirmation) || deployment.TaskRunID == nil {
			return domain.Deployment{}, ErrConflict
		}
		now := time.Now().UTC()
		runIndex := -1
		for j, run := range s.runs {
			if run.ID == *deployment.TaskRunID && (run.Status == domain.RunQueued || run.Status == domain.RunWaitingApproval) {
				runIndex = j
				break
			}
		}
		if runIndex == -1 {
			return domain.Deployment{}, ErrConflict
		}
		deployment.Status = domain.DeploymentFailed
		deployment.FailureCode = failureCode
		deployment.UpdatedAt = now
		deployment.FinishedAt = &now
		run := s.runs[runIndex]
		run.Status = domain.RunFailed
		run.RunnerID = nil
		run.FinishedAt = &now
		s.runs[runIndex] = run
		s.deployments[i] = deployment
		audit.CreatedAt = now
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

func sameDeploymentTransition(left, right domain.DeploymentTransitionRequest) bool {
	return left.DeploymentID == right.DeploymentID && left.RunID == right.RunID && left.LeaseID == right.LeaseID && left.RunnerID == right.RunnerID && left.Attempt == right.Attempt && left.Fence == right.Fence && left.ExpectedStatus == right.ExpectedStatus && left.TargetStatus == right.TargetStatus && left.FailureCode == right.FailureCode && ((left.HealthPassed == nil && right.HealthPassed == nil) || (left.HealthPassed != nil && right.HealthPassed != nil && *left.HealthPassed == *right.HealthPassed)) && reflect.DeepEqual(left.Metadata, right.Metadata)
}

func (s *MemoryStore) TransitionDeploymentAttempt(_ context.Context, request domain.DeploymentTransitionRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentTransitions == nil {
		s.deploymentTransitions = map[string]domain.DeploymentTransitionRequest{}
	}
	var lease domain.RunLease
	leaseIndex := -1
	foundLease := false
	for i, candidate := range s.leases {
		if candidate.ID == request.LeaseID && candidate.RunID == request.RunID && candidate.RunnerID == request.RunnerID && candidate.Attempt == request.Attempt && candidate.Fence == request.Fence {
			lease = candidate
			foundLease = true
			leaseIndex = i
			break
		}
	}
	if !foundLease {
		return domain.Deployment{}, ErrNotFound
	}
	attemptIndex := -1
	for i, candidate := range s.deploymentAttempts {
		if candidate.DeploymentID == request.DeploymentID && candidate.RunID == request.RunID && candidate.LeaseID == request.LeaseID && candidate.RunnerID == request.RunnerID && candidate.Attempt == request.Attempt && candidate.Fence == request.Fence {
			attemptIndex = i
			break
		}
	}
	if attemptIndex == -1 {
		return domain.Deployment{}, ErrNotFound
	}
	key := request.DeploymentID + "\x00" + request.TransitionKey
	if replay, ok := s.deploymentTransitions[key]; ok {
		if !sameDeploymentTransition(replay, request) {
			return domain.Deployment{}, ErrConflict
		}
		for _, deployment := range s.deployments {
			if deployment.ID == request.DeploymentID {
				return deployment, nil
			}
		}
		return domain.Deployment{}, ErrNotFound
	}
	now := time.Now().UTC()
	if lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(now) {
		return domain.Deployment{}, ErrNotFound
	}
	for i, deployment := range s.deployments {
		if deployment.ID != request.DeploymentID {
			continue
		}
		resolved := false
		for _, revision := range s.revisions {
			if revision.ID == deployment.DesiredRevisionID {
				resolved = revision.ProvenanceResolved
				break
			}
		}
		if deployment.TaskRunID == nil || *deployment.TaskRunID != request.RunID || deployment.Status != request.ExpectedStatus || !domain.DeploymentRoleTransitionAllowed(deployment.RollbackOfID != nil, deployment.Status, request.TargetStatus) || (request.TargetStatus == domain.DeploymentApplying && !resolved) || ((request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && deployment.RollbackOfID != nil)) && (request.HealthPassed == nil || !*request.HealthPassed)) {
			return domain.Deployment{}, ErrConflict
		}
		deployment.Status = request.TargetStatus
		deployment.HealthPassed = request.HealthPassed
		deployment.FailureCode = request.FailureCode
		deployment.UpdatedAt = now
		attemptStatus, leaseStatus, runStatus, terminal := deploymentTerminalOutcome(request.TargetStatus)
		if terminal {
			if s.deploymentAttempts[attemptIndex].Status != "active" {
				return domain.Deployment{}, ErrConflict
			}
			runIndex := -1
			for j, run := range s.runs {
				if run.ID == request.RunID && run.RunnerID != nil && *run.RunnerID == request.RunnerID && run.Status == domain.RunRunning {
					runIndex = j
					break
				}
			}
			if runIndex == -1 {
				return domain.Deployment{}, ErrConflict
			}
			lease.Status = leaseStatus
			lease.CompletedAt = &now
			s.leases[leaseIndex] = lease
			run := s.runs[runIndex]
			run.Status = runStatus
			run.RunnerID = nil
			run.FinishedAt = &now
			s.runs[runIndex] = run
			deployment.FinishedAt = &now
			s.deploymentAttempts[attemptIndex].Status = attemptStatus
			s.deploymentAttempts[attemptIndex].FinishedAt = &now
		}
		if request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && deployment.RollbackOfID != nil) {
			for j := range s.environments {
				if s.environments[j].ID == deployment.EnvironmentID {
					s.environments[j].CurrentHealthyRevisionID = stringPointer(deployment.DesiredRevisionID)
				}
			}
		}
		if deployment.RollbackOfID != nil && (request.TargetStatus == domain.DeploymentRolledBack || request.TargetStatus == domain.DeploymentRollbackFailed) {
			for j := range s.deployments {
				if s.deployments[j].ID == *deployment.RollbackOfID && s.deployments[j].Status == domain.DeploymentRollingBack {
					s.deployments[j].Status = request.TargetStatus
					s.deployments[j].UpdatedAt, s.deployments[j].FinishedAt = now, &now
					break
				}
			}
		}
		s.deployments[i] = deployment
		s.deploymentTransitions[key] = request
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// CancelDeploymentRequest is deliberately not implemented in terms of the
// runner transition API: pre-apply cancellation has no runner fence yet, and
// post-apply cancellation must retain that fence until the runner reconciles
// the target into rollback or manual intervention.
func (s *MemoryStore) CancelDeploymentRequest(_ context.Context, req domain.DeploymentCancelRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentCancels == nil {
		s.deploymentCancels = map[string]domain.DeploymentCancelRequest{}
	}
	key := req.DeploymentID + "\x00" + req.RequestID
	if prior, ok := s.deploymentCancels[key]; ok {
		if prior.ActorID != req.ActorID {
			return domain.Deployment{}, ErrConflict
		}
		for _, d := range s.deployments {
			if d.ID == req.DeploymentID {
				return d, nil
			}
		}
		return domain.Deployment{}, ErrNotFound
	}
	for priorKey := range s.deploymentCancels {
		if strings.HasPrefix(priorKey, req.DeploymentID+"\x00") {
			return domain.Deployment{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	for i := range s.deployments {
		d := s.deployments[i]
		if d.ID != req.DeploymentID {
			continue
		}
		if d.RollbackOfID != nil || domain.IsTerminalDeploymentStatus(d.Status) {
			return domain.Deployment{}, ErrConflict
		}
		switch d.Status {
		case domain.DeploymentQueued, domain.DeploymentWaitingConfirmation, domain.DeploymentAssigned, domain.DeploymentPreparing:
			d.Status, d.UpdatedAt, d.FinishedAt = domain.DeploymentCanceled, now, &now
			if d.TaskRunID != nil {
				for j := range s.runs {
					if s.runs[j].ID == *d.TaskRunID {
						s.runs[j].Status = domain.RunCanceled
						s.runs[j].RunnerID = nil
						s.runs[j].FinishedAt = &now
					}
				}
				for j := range s.leases {
					if s.leases[j].RunID == *d.TaskRunID && s.leases[j].Status == domain.LeaseActive {
						s.leases[j].Status = domain.RunCanceled
						s.leases[j].CompletedAt = &now
					}
				}
				for j := range s.deploymentAttempts {
					if s.deploymentAttempts[j].DeploymentID == d.ID && s.deploymentAttempts[j].Status == "active" {
						s.deploymentAttempts[j].Status = "canceled"
						s.deploymentAttempts[j].FinishedAt = &now
					}
				}
			}
		case domain.DeploymentApplying, domain.DeploymentVerifying:
			d.Status, d.UpdatedAt = domain.DeploymentCancelRequested, now
		default:
			return domain.Deployment{}, ErrConflict
		}
		s.deployments[i] = d
		s.deploymentCancels[key] = req
		audit.CreatedAt = now
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return d, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// FailDeploymentAndCreateRollback is intentionally not expressed as two
// calls to TransitionDeploymentAttempt/CreateDeploymentRequest.  Between
// those calls another deployment could acquire the environment.  Holding the
// store lock makes the source terminalization, lease settlement and rollback
// queue insertion one all-or-nothing fenced mutation.
func (s *MemoryStore) FailDeploymentAndCreateRollback(_ context.Context, req domain.DeploymentFailureRollbackRequest, failedAudit, rollbackAudit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		receipt, ok := s.deploymentCancels[req.DeploymentID+"\x00"+req.CancellationRequestID]
		if !ok || req.CancellationRequestID == "" || receipt.RequestID != req.CancellationRequestID || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	now := time.Now().UTC()
	key := "rollback:" + req.DeploymentID + ":" + req.RequestID
	for _, existing := range s.deployments {
		if existing.IdempotencyKey == key {
			if existing.ID != rollbackDeploymentID || existing.RollbackOfID == nil || *existing.RollbackOfID != req.DeploymentID || existing.TaskRunID == nil || *existing.TaskRunID != rollbackRunID {
				return domain.DeploymentFailureRollbackResult{}, ErrConflict
			}
			for _, source := range s.deployments {
				if source.ID == req.DeploymentID {
					if source.TaskRunID == nil || *source.TaskRunID != req.RunID {
						return domain.DeploymentFailureRollbackResult{}, ErrConflict
					}
					for _, attempt := range s.deploymentAttempts {
						if attempt.DeploymentID == req.DeploymentID && attempt.RunID == req.RunID && attempt.LeaseID == req.LeaseID && attempt.RunnerID == req.RunnerID && attempt.Attempt == req.Attempt && attempt.Fence == req.Fence {
							stored, ok := s.deploymentTransitions[source.ID+"\x00failure:"+req.RequestID]
							want := domain.DeploymentTransitionRequest{DeploymentID: req.DeploymentID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: req.Attempt, Fence: req.Fence, TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: req.Metadata}
							if attempt.Status == "failed" && ok && sameDeploymentTransition(stored, want) {
								return domain.DeploymentFailureRollbackResult{Failed: source, Rollback: existing}, nil
							}
							return domain.DeploymentFailureRollbackResult{}, ErrConflict
						}
					}
					return domain.DeploymentFailureRollbackResult{}, ErrNotFound
				}
			}
			return domain.DeploymentFailureRollbackResult{}, ErrNotFound
		}
	}
	leaseIndex, attemptIndex, depIndex, runIndex := -1, -1, -1, -1
	for i, l := range s.leases {
		if l.ID == req.LeaseID && l.RunID == req.RunID && l.RunnerID == req.RunnerID && l.Attempt == req.Attempt && l.Fence == req.Fence {
			leaseIndex = i
			break
		}
	}
	if leaseIndex < 0 || s.leases[leaseIndex].Status != domain.LeaseActive || !s.leases[leaseIndex].ExpiresAt.After(now) {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	for i, a := range s.deploymentAttempts {
		if a.DeploymentID == req.DeploymentID && a.RunID == req.RunID && a.LeaseID == req.LeaseID && a.RunnerID == req.RunnerID && a.Attempt == req.Attempt && a.Fence == req.Fence && a.Status == "active" {
			attemptIndex = i
			break
		}
	}
	for i, d := range s.deployments {
		if d.ID == req.DeploymentID && d.TaskRunID != nil && *d.TaskRunID == req.RunID {
			depIndex = i
			break
		}
	}
	for i, r := range s.runs {
		if r.ID == req.RunID && r.Status == domain.RunRunning && r.RunnerID != nil && *r.RunnerID == req.RunnerID {
			runIndex = i
			break
		}
	}
	if attemptIndex < 0 || depIndex < 0 || runIndex < 0 {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	source := s.deployments[depIndex]
	if source.Status != req.ExpectedStatus || (source.Status != domain.DeploymentApplying && source.Status != domain.DeploymentVerifying && source.Status != domain.DeploymentCancelRequested) || source.PreviousHealthyRevisionID == nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		if req.CancellationRequestID == "" || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
		if receipt, ok := s.deploymentCancels[req.DeploymentID+"\x00"+req.CancellationRequestID]; !ok || receipt.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	var env domain.Environment
	for _, e := range s.environments {
		if e.ID == source.EnvironmentID {
			env = e
			break
		}
	}
	if env.ID == "" || !env.RollbackSafe {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	// Ensure the recorded previous revision remains the last verified target.
	if env.CurrentHealthyRevisionID == nil || *env.CurrentHealthyRevisionID != *source.PreviousHealthyRevisionID {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	for _, d := range s.deployments {
		if d.EnvironmentID == source.EnvironmentID && !domain.IsTerminalDeploymentStatus(d.Status) && d.ID != source.ID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	// The root remains the active environment owner until its linked child
	// proves rollback health or fails loudly; only its execution attempt ends.
	source.Status, source.FailureCode, source.UpdatedAt, source.FinishedAt = domain.DeploymentRollingBack, req.FailureCode, now, nil
	s.deployments[depIndex] = source
	lease := s.leases[leaseIndex]
	lease.Status, lease.CompletedAt = domain.RunFailed, &now
	s.leases[leaseIndex] = lease
	run := s.runs[runIndex]
	run.Status, run.RunnerID, run.FinishedAt = domain.RunFailed, nil, &now
	s.runs[runIndex] = run
	s.deploymentAttempts[attemptIndex].Status, s.deploymentAttempts[attemptIndex].FinishedAt = "failed", &now
	if s.deploymentTransitions == nil {
		s.deploymentTransitions = map[string]domain.DeploymentTransitionRequest{}
	}
	s.deploymentTransitions[source.ID+"\x00failure:"+req.RequestID] = domain.DeploymentTransitionRequest{DeploymentID: source.ID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: req.Attempt, Fence: req.Fence, TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: req.Metadata}
	rollback := domain.Deployment{ID: rollbackDeploymentID, EnvironmentID: source.EnvironmentID, DesiredRevisionID: *source.PreviousHealthyRevisionID, PreviousHealthyRevisionID: source.PreviousHealthyRevisionID, IdempotencyKey: "rollback:" + source.ID + ":" + req.RequestID, Status: domain.DeploymentQueued, RequestedBy: req.RunnerID, CreatedAt: now, UpdatedAt: now, RollbackOfID: &source.ID, FenceRequired: true, TaskRunID: stringPointer(rollbackRunID)}
	rollbackRun := domain.TaskRun{ID: rollbackRunID, ProjectID: "", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": rollbackDeploymentID, "rollback_of_id": source.ID, "desired_revision_id": *source.PreviousHealthyRevisionID}, Secrets: append([]domain.SecretBinding(nil), run.RunSpec.Secrets...)}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: req.RunnerID, StartedAt: now}
	for _, service := range s.services {
		if service.ID == env.ServiceID {
			rollbackRun.ProjectID = service.ProjectID
			break
		}
	}
	if rollback.ID == "" || rollbackRun.ID == "" || rollbackRun.ProjectID == "" {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	s.runs = append([]domain.TaskRun{rollbackRun}, s.runs...)
	s.claimOrderByRun[rollbackRun.ID] = now
	s.deployments = append(s.deployments, rollback)
	if s.deploymentByID == nil {
		s.deploymentByID = map[string]int{}
	}
	s.deploymentByID[rollback.ID] = len(s.deployments) - 1
	failedAudit.CreatedAt, rollbackAudit.CreatedAt = now, now.Add(time.Microsecond)
	s.auditEvents = append([]domain.AuditEvent{rollbackAudit, failedAudit}, s.auditEvents...)
	return domain.DeploymentFailureRollbackResult{Failed: source, Rollback: rollback}, nil
}
