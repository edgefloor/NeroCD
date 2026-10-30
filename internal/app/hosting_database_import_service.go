package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) databaseImportRepository() (store.HostingDatabaseImportRepository, error) {
	repo, ok := s.hosting.(store.HostingDatabaseImportRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return repo, nil
}

// RequestDatabaseImportStage admits external S3 inspection for an explicitly selected target.
func (s *Service) RequestDatabaseImportStage(ctx context.Context, resource string, in domain.HostingDatabaseImportStageInput) (domain.HostingDatabaseImportStage, error) {
	var zero domain.HostingDatabaseImportStage
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseImport)
	}
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseImportRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbis")
	if err != nil {
		return zero, err
	}
	return repo.RequestDatabaseImportStage(ctx, id, resource, actor.ID, in)
}

// GetDatabaseImportStage reads immutable inspection facts under current viewer authority.
func (s *Service) GetDatabaseImportStage(ctx context.Context, id string) (domain.HostingDatabaseImportStage, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseImportStage{}, err
	}
	repo, err := s.databaseImportRepository()
	if err != nil {
		return domain.HostingDatabaseImportStage{}, err
	}
	return repo.GetDatabaseImportStage(ctx, actor.ID, id)
}

// RequestDatabaseImport confirms one stage; it never invents a generated backup source.
func (s *Service) RequestDatabaseImport(ctx context.Context, resource string, in domain.HostingDatabaseImportInput) (domain.HostingDatabaseImportOperation, error) {
	var zero domain.HostingDatabaseImportOperation
	if in.Validate() != nil || in.ComponentTarget.ResourceID != resource {
		return zero, InvalidInput(domain.ErrInvalidDatabaseImport)
	}
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseImportRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbim")
	if err != nil {
		return zero, err
	}
	return repo.RequestDatabaseImport(ctx, id, resource, actor.ID, in)
}

// GetDatabaseImport reads the original operation without replaying its SQL.
func (s *Service) GetDatabaseImport(ctx context.Context, id string) (domain.HostingDatabaseImportOperation, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseImportOperation{}, err
	}
	repo, err := s.databaseImportRepository()
	if err != nil {
		return domain.HostingDatabaseImportOperation{}, err
	}
	return repo.GetDatabaseImport(ctx, actor.ID, id)
}

// RunnerDatabaseImportStagePlan returns only the authenticated runner's fenced inspection plan.
func (s *Service) RunnerDatabaseImportStagePlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error) {
	r, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return domain.HostingDatabaseImportStagePlan{}, e
	}
	repo, ok := s.hosting.(store.HostingDatabaseImportRepository)
	if !ok {
		return domain.HostingDatabaseImportStagePlan{}, store.ErrConflict
	}
	return repo.DatabaseImportStagePlan(ctx, r.ID, a)
}

// RunnerDatabaseImportPlan returns the admitted immutable stage consumption plan.
func (s *Service) RunnerDatabaseImportPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseImportPlan, error) {
	r, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return domain.HostingDatabaseImportPlan{}, e
	}
	repo, ok := s.hosting.(store.HostingDatabaseImportRepository)
	if !ok {
		return domain.HostingDatabaseImportPlan{}, store.ErrConflict
	}
	return repo.DatabaseImportPlan(ctx, r.ID, a)
}

// RunnerDatabaseImportMutation records a fenced stage or SQL result.
func (s *Service) RunnerDatabaseImportMutation(ctx context.Context, in domain.HostingDatabaseImportMutation) (domain.HostingDatabaseImportReceiptResult, error) {
	r, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return domain.HostingDatabaseImportReceiptResult{}, e
	}
	repo, ok := s.hosting.(store.HostingDatabaseImportRepository)
	if !ok {
		return domain.HostingDatabaseImportReceiptResult{}, store.ErrConflict
	}
	return repo.DatabaseImportMutation(ctx, r.ID, in)
}
