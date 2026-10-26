package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) databaseBackupRetentionRepository() (store.HostingDatabaseBackupRetentionRepository, error) {
	r, ok := s.hosting.(store.HostingDatabaseBackupRetentionRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// GetDatabaseBackupRetention returns the authorized database backup retention policy.
func (s *Service) GetDatabaseBackupRetention(ctx context.Context, resource string) (*domain.HostingDatabaseBackupRetentionPolicy, error) {
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return nil, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return nil, e
	}
	return r.GetDatabaseBackupRetention(ctx, a.ID, resource)
}

// SaveDatabaseBackupRetention updates the authorized database backup retention policy.
func (s *Service) SaveDatabaseBackupRetention(ctx context.Context, resource string, in domain.HostingDatabaseBackupRetentionInput) (domain.HostingDatabaseBackupRetentionPolicy, error) {
	var z domain.HostingDatabaseBackupRetentionPolicy
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidDatabaseBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.SaveDatabaseBackupRetention(ctx, a.ID, resource, in)
}

// PreviewDatabaseBackupRetention records a retention preview for database backup copies.
func (s *Service) PreviewDatabaseBackupRetention(ctx context.Context, resource string, in domain.HostingDatabaseBackupRetentionPreviewInput) (domain.HostingDatabaseBackupRetentionPreview, error) {
	var z domain.HostingDatabaseBackupRetentionPreview
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidDatabaseBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("dbrp")
	if e != nil {
		return z, e
	}
	return r.PreviewDatabaseBackupRetention(ctx, id, a.ID, resource, in)
}

// ExecuteDatabaseBackupRetention admits deletion of the database copies selected by a saved
// preview.
func (s *Service) ExecuteDatabaseBackupRetention(ctx context.Context, resource string, in domain.HostingDatabaseBackupRetentionExecutionInput) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var z domain.HostingDatabaseBackupRetentionExecution
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidDatabaseBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("dbre")
	if e != nil {
		return z, e
	}
	return r.ExecuteDatabaseBackupRetention(ctx, id, a.ID, resource, in)
}

// ListDatabaseBackupRetentionExecutions lists authorized database backup retention executions.
func (s *Service) ListDatabaseBackupRetentionExecutions(ctx context.Context, resource string) ([]domain.HostingDatabaseBackupRetentionExecution, error) {
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return nil, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return nil, e
	}
	return r.ListDatabaseBackupRetentionExecutions(ctx, a.ID, resource)
}

// GetDatabaseBackupRetentionExecution returns an authorized database backup retention execution.
func (s *Service) GetDatabaseBackupRetentionExecution(ctx context.Context, resource, id string) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var z domain.HostingDatabaseBackupRetentionExecution
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.GetDatabaseBackupRetentionExecution(ctx, a.ID, resource, id)
}

// RunnerDatabaseBackupRetentionPlan returns a database retention plan for the authenticated
// runner.
func (s *Service) RunnerDatabaseBackupRetentionPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupRetentionPlan, error) {
	var z domain.HostingDatabaseBackupRetentionPlan
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.DatabaseBackupRetentionPlan(ctx, p.ID, a)
}

// RunnerDatabaseBackupRetentionMutation records a database retention mutation from the
// authenticated runner.
func (s *Service) RunnerDatabaseBackupRetentionMutation(ctx context.Context, in domain.HostingDatabaseBackupRetentionMutation) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var z domain.HostingDatabaseBackupRetentionExecution
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.DatabaseBackupRetentionMutation(ctx, p.ID, in)
}

// RunnerDatabaseBackupRetentionRetirement checks retirement of a database retention lease for the
// authenticated runner.
func (s *Service) RunnerDatabaseBackupRetentionRetirement(ctx context.Context, in domain.HostingDatabaseBackupRetentionRetirementInput) (domain.HostingDatabaseBackupRetentionRetirement, error) {
	var z domain.HostingDatabaseBackupRetentionRetirement
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.databaseBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.DatabaseBackupRetentionRetirement(ctx, p.ID, in)
}
