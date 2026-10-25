package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) connectionRepository() (store.HostingDatabaseConnectionRepository, error) {
	repo, ok := s.runs.(store.HostingDatabaseConnectionRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseConnectionUnavailable
	}
	return repo, nil
}

// ConnectHostingApplicationDatabase captures one atomic initial owner request.
func (s *Service) ConnectHostingApplicationDatabase(ctx context.Context, application string, in domain.HostingDatabaseConnectionInput) (domain.HostingDatabaseConnection, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, application, domain.RoleOwner)
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	if resource.Kind != "application" || resource.Source.Kind != "image" || in.Validate() != nil {
		return domain.HostingDatabaseConnection{}, InvalidInput(domain.ErrInvalidHostingDatabaseConnection)
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	id, err := prefixedID("hcn")
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database_connection.requested", id, nil)
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	return repo.ConnectHostingApplicationDatabase(ctx, id, resource.ID, actor.ID, in, event)
}

// RequestHostingApplicationDatabaseDeployment replaces only a qualified bound family.
func (s *Service) RequestHostingApplicationDatabaseDeployment(ctx context.Context, application string, in domain.HostingDeploymentInput) (domain.HostingOperation, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, application, domain.RoleOwner)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if resource.Kind != "application" || resource.Source.Kind != "image" || domain.ValidateHostingDeployment(in) != nil || in.StorageRouteContinuity != nil {
		return domain.HostingOperation{}, InvalidInput(domain.ErrInvalidHostingDatabaseConnection)
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.application_database.requested", id, nil)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingApplicationDatabaseDeployment(ctx, id, resource.ID, actor.ID, in, event)
}

// RequestHostingDatabaseMaintenance preserves exact current membership evidence.
func (s *Service) RequestHostingDatabaseMaintenance(ctx context.Context, database string, in domain.HostingDatabaseMaintenanceInput) (domain.HostingOperation, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, database, domain.RoleOwner)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if resource.Kind != "database" || in.Validate() != nil {
		return domain.HostingOperation{}, InvalidInput(domain.ErrInvalidHostingDatabaseConnection)
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database_maintenance.requested", id, nil)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingDatabaseMaintenance(ctx, id, resource.ID, actor.ID, in, event)
}

// GetHostingDatabaseConnection returns durable progress under current viewer access.
func (s *Service) GetHostingDatabaseConnection(ctx context.Context, application string) (domain.HostingDatabaseConnection, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, application, domain.RoleViewer)
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return domain.HostingDatabaseConnection{}, err
	}
	return repo.GetHostingDatabaseConnection(ctx, actor.ID, resource.ID)
}

// ListEligibleHostingDatabases exposes only bounded, same-target eligible options.
func (s *Service) ListEligibleHostingDatabases(ctx context.Context, application string) ([]domain.HostingDatabaseConnectionOption, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, application, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	if resource.Kind != "application" || resource.Source.Kind != "image" {
		return nil, InvalidInput(domain.ErrInvalidHostingDatabaseConnection)
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListEligibleHostingDatabases(ctx, actor.ID, resource.ID)
}

// RequestHostingDatabaseConnectionRecovery authorizes fresh observation only.
func (s *Service) RequestHostingDatabaseConnectionRecovery(ctx context.Context, application string, in domain.HostingDatabaseConnectionRecoveryInput) (domain.HostingOperation, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, application, domain.RoleOwner)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if err = in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, err := s.connectionRepository()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hcr")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database_connection.observation_requested", in.OperationID, nil)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingDatabaseConnectionRecovery(ctx, id, resource.ID, actor.ID, in, event)
}

func (s *Service) connectionRunner(ctx context.Context, token string) (store.HostingApplicationDatabaseRunnerRepository, string, string, error) {
	runner, credential, err := s.observationRunner(ctx, token)
	if err != nil {
		return nil, "", "", err
	}
	repo, ok := s.runs.(store.HostingApplicationDatabaseRunnerRepository)
	if !ok {
		return nil, "", "", store.ErrHostingDatabaseConnectionUnavailable
	}
	return repo, runner, credential, nil
}

// RunnerHostingApplicationDatabaseStage commits an acknowledgement, not effect permission.
func (s *Service) RunnerHostingApplicationDatabaseStage(ctx context.Context, token string, in domain.HostingApplicationDatabaseStageRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.HostingApplicationDatabaseStage(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseRetirementPlan loads the exact prior target.
func (s *Service) RunnerHostingApplicationDatabaseRetirementPlan(ctx context.Context, token string, in domain.HostingAuthority) (domain.HostingDatabaseBindingEndpointPlan, error) {
	repo, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingDatabaseBindingEndpointPlan{}, err
	}
	return repo.HostingApplicationDatabaseRetirementPlan(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseRetirement records ordered retirement evidence.
func (s *Service) RunnerHostingApplicationDatabaseRetirement(ctx context.Context, token string, in domain.HostingApplicationDatabaseRetirementRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.HostingApplicationDatabaseRetirement(ctx, runner, credential, in)
}

// RunnerAuthorizeHostingApplicationDatabaseEffect requests explicit current permission.
func (s *Service) RunnerAuthorizeHostingApplicationDatabaseEffect(ctx context.Context, token string, in domain.HostingApplicationDatabaseEffectRequest) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingApplicationDatabaseEffectAuthorization{}, InvalidInput(err)
	}
	repo, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingApplicationDatabaseEffectAuthorization{}, err
	}
	return repo.AuthorizeHostingApplicationDatabaseEffect(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseState reads exact retained state without permission.
func (s *Service) RunnerHostingApplicationDatabaseState(ctx context.Context, token string, in domain.HostingAuthority) (domain.HostingApplicationDatabaseExecutionState, error) {
	repo, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingApplicationDatabaseExecutionState{}, err
	}
	return repo.GetHostingApplicationDatabaseState(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseYield records exact ready evidence and releases only the prepare claim.
func (s *Service) RunnerHostingApplicationDatabaseYield(ctx context.Context, token string, in domain.HostingApplicationDatabaseYieldRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	base, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	repo, ok := base.(store.HostingApplicationDatabaseYieldRepository)
	if !ok {
		return domain.HostingOperation{}, store.ErrHostingDatabaseConnectionUnavailable
	}
	return repo.HostingApplicationDatabaseYield(ctx, runner, credential, in)
}
