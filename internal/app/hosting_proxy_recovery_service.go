package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) proxyRecoveryAdmin(ctx context.Context, operation string) (store.HostingProxyRecoveryRepository, string, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return nil, "", err
	}
	if err = validateDestinationID(operation, "operation_id"); err != nil {
		return nil, "", err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, "", err
	}
	op, err := repository.GetHostingProxyOperation(ctx, operation)
	if err != nil {
		return nil, "", err
	}
	p, err := repository.GetHostingProxy(ctx, op.ProxyID)
	if err != nil {
		return nil, "", err
	}
	if _, err = s.authorizeHostingProject(ctx, p.ControlProjectID, domain.RoleOwner); err != nil {
		return nil, "", err
	}
	recovery, ok := repository.(store.HostingProxyRecoveryRepository)
	if !ok {
		return nil, "", hostingjobs.ErrUnavailable
	}
	return recovery, principal.ID, nil
}

// HostingProxyRecovery explains only completed metadata conflicts to proxy administrators.
func (s *Service) HostingProxyRecovery(ctx context.Context, operation string) (domain.HostingProxyRecoveryStatus, error) {
	r, actor, err := s.proxyRecoveryAdmin(ctx, operation)
	if err != nil {
		return domain.HostingProxyRecoveryStatus{}, err
	}
	return r.HostingProxyRecovery(ctx, operation, actor)
}

// PlanHostingProxyRecovery creates an immutable review with a fixed 30-second expiry.
func (s *Service) PlanHostingProxyRecovery(ctx context.Context, operation string, in domain.HostingProxyRecoveryInput) (domain.HostingProxyRecoveryPlan, error) {
	r, actor, err := s.proxyRecoveryAdmin(ctx, operation)
	if err != nil {
		return domain.HostingProxyRecoveryPlan{}, err
	}
	if err = domain.ValidateHostingProxyRecoveryInput(in); err != nil {
		return domain.HostingProxyRecoveryPlan{}, InvalidInput(err)
	}
	id, err := prefixedID("prp")
	if err != nil {
		return domain.HostingProxyRecoveryPlan{}, err
	}
	audit, err := s.auditEvent(ctx, actor, "hosting.proxy_recovery_planned", operation, nil)
	if err != nil {
		return domain.HostingProxyRecoveryPlan{}, err
	}
	return r.PlanHostingProxyRecovery(ctx, operation, id, actor, in, audit)
}

// ApplyHostingProxyRecovery repairs reviewed metadata and queues authorized convergence.
func (s *Service) ApplyHostingProxyRecovery(ctx context.Context, operation, plan string, in domain.HostingProxyRecoveryApplyInput) (domain.HostingProxyRecoveryResolution, error) {
	r, actor, err := s.proxyRecoveryAdmin(ctx, operation)
	if err != nil {
		return domain.HostingProxyRecoveryResolution{}, err
	}
	if err = validateDestinationID(plan, "plan_id"); err != nil {
		return domain.HostingProxyRecoveryResolution{}, err
	}
	if err = domain.ValidateHostingProxyRecoveryApplyInput(in); err != nil {
		return domain.HostingProxyRecoveryResolution{}, InvalidInput(err)
	}
	id, err := prefixedID("prr")
	if err != nil {
		return domain.HostingProxyRecoveryResolution{}, err
	}
	audit, err := s.auditEvent(ctx, actor, "hosting.proxy_metadata_recovered", operation, nil)
	if err != nil {
		return domain.HostingProxyRecoveryResolution{}, err
	}
	return r.ApplyHostingProxyRecovery(ctx, operation, plan, id, actor, in, audit)
}
