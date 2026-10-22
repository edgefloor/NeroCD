package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) hostingExecutor() (store.HostingExecutionRepository, error) {
	executor, ok := s.runs.(store.HostingExecutionRepository)
	if !ok {
		return nil, hostingjobs.ErrUnavailable
	}
	return executor, nil
}

// RequestHostingDeployment authorizes the persisted resource project and submits
// an immutable operation through a real PostgreSQL/River transaction.
func (s *Service) RequestHostingDeployment(ctx context.Context, resourceID string, in domain.HostingDeploymentInput) (domain.HostingOperation, error) {
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if err = domain.ValidateHostingDeployment(in); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	executor, err := s.hostingExecutor()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.deployment_requested", id, map[string]any{"resource_id": resource.ID, "expected_generation": in.ExpectedGeneration})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return executor.RequestHostingDeployment(ctx, id, resource.ID, principal.ID, in, audit)
}

func (s *Service) hostingOperationForRole(ctx context.Context, id, role string) (store.HostingExecutionRepository, domain.HostingOperation, error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return nil, domain.HostingOperation{}, err
	}
	if err := validateDestinationID(id, "operation_id"); err != nil {
		return nil, domain.HostingOperation{}, err
	}
	executor, err := s.hostingExecutor()
	if err != nil {
		return nil, domain.HostingOperation{}, err
	}
	op, err := executor.GetHostingOperation(ctx, id)
	if err != nil {
		return nil, op, err
	}
	if _, err = s.authorizeHostingProject(ctx, op.ProjectID, role); err != nil {
		return nil, domain.HostingOperation{}, err
	}
	return executor, op, nil
}

// HostingOperation returns an operation after checking persisted project viewer access.
func (s *Service) HostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	_, op, err := s.hostingOperationForRole(ctx, id, domain.RoleViewer)
	return op, err
}

// CancelHostingOperation requests cancellation with current maintainer authority.
func (s *Service) CancelHostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	executor, op, err := s.hostingOperationForRole(ctx, id, domain.RoleMaintainer)
	if err != nil {
		return op, err
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.cancel_requested", id, map[string]any{"binding_id": op.BindingID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return executor.CancelHostingOperation(ctx, id, audit)
}

// ReconcileHostingOperation requests an exact-target observation with current owner authority.
func (s *Service) ReconcileHostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	executor, op, err := s.hostingOperationForRole(ctx, id, domain.RoleOwner)
	if err != nil {
		return op, err
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.reconciliation_requested", id, map[string]any{"binding_id": op.BindingID, "runner_id": op.Snapshot.RunnerID, "destination_id": op.Snapshot.DestinationID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return executor.RequestHostingReconciliation(ctx, id, audit)
}

func (s *Service) withHostingRunner(ctx context.Context, call func(store.HostingExecutionRepository, string) (domain.HostingOperation, error)) (domain.HostingOperation, error) {
	identity, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	executor, err := s.hostingExecutor()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return call(executor, identity.RunnerID)
}

// RunnerHostingPlan loads the immutable plan for the authenticated runner lease.
func (s *Service) RunnerHostingPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		return repo.HostingRunnerPlan(ctx, id, in)
	})
}

// RunnerHostingPrepared commits or replays the authenticated preparation receipt.
func (s *Service) RunnerHostingPrepared(ctx context.Context, in domain.HostingPreparedRequest) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		return repo.HostingPrepared(ctx, id, in)
	})
}

// RunnerHostingApplyBegin acquires the independent mutation hold before external effects.
func (s *Service) RunnerHostingApplyBegin(ctx context.Context, in domain.HostingAuthority) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		return repo.HostingApplyBegin(ctx, id, in)
	})
}

// RunnerHostingComplete settles durable execution evidence for the authenticated attempt.
func (s *Service) RunnerHostingComplete(ctx context.Context, in domain.HostingCompleteRequest) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		return repo.HostingComplete(ctx, id, in)
	})
}

// RunnerHostingStatus reads fenced status or claims an owner-requested observation.
func (s *Service) RunnerHostingStatus(ctx context.Context, in domain.HostingStatusRequest) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		if in.Phase == "claim_observation" {
			return repo.ClaimHostingObservation(ctx, id, in.HostingAuthority, s.leaseTTL)
		}
		return repo.HostingStatus(ctx, id, in)
	})
}

// RunnerHostingReconciliation submits read-only evidence under an observation lease.
func (s *Service) RunnerHostingReconciliation(ctx context.Context, in domain.HostingObservation) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, id string) (domain.HostingOperation, error) {
		return repo.HostingReconciliation(ctx, id, in)
	})
}

// ListHostingDeployments authorizes the resource's persisted project before pagination.
func (s *Service) ListHostingDeployments(ctx context.Context, id string, page store.Page) (store.PageResult[domain.HostingOperation], error) {
	resource, _, err := s.resourceForDestinationRole(ctx, id, domain.RoleViewer)
	if err != nil {
		return store.PageResult[domain.HostingOperation]{}, err
	}
	repo, err := s.hostingExecutor()
	if err != nil {
		return store.PageResult[domain.HostingOperation]{}, err
	}
	return repo.ListHostingOperations(ctx, resource.ID, page)
}
