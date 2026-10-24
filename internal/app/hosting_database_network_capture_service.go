package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) databaseNetworkCaptureRepository() (store.HostingDatabaseNetworkCaptureRepository, error) {
	r, ok := s.runs.(store.HostingDatabaseNetworkCaptureRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseNetworkCaptureUnavailable
	}
	return r, nil
}

// RequestHostingDatabaseNetworkCapture requests internal read-only attestation.
// No public route or generic runner dispatch is registered for this family.
func (s *Service) RequestHostingDatabaseNetworkCapture(ctx context.Context, source string) (domain.HostingDatabaseNetworkCapture, error) {
	var zero domain.HostingDatabaseNetworkCapture
	_, op, err := s.hostingOperationForRole(ctx, source, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseNetworkCaptureRepository()
	if err != nil {
		return zero, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.database_network_capture.requested", op.ID, nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestHostingDatabaseNetworkCapture(ctx, op.ID, actor.ID, audit)
}

// RunnerClaimHostingDatabaseNetworkCapture binds a dedicated internal claim to the current bearer.
func (s *Service) RunnerClaimHostingDatabaseNetworkCapture(ctx context.Context, token, id string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	var zero domain.HostingDatabaseNetworkCaptureClaim
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseNetworkCaptureRepository()
	if err != nil {
		return zero, err
	}
	return repo.ClaimHostingDatabaseNetworkCapture(ctx, runner, hash, id, s.leaseTTL)
}

// RunnerClaimNextHostingDatabaseNetworkCapture selects work only for the current bearer.
func (s *Service) RunnerClaimNextHostingDatabaseNetworkCapture(ctx context.Context, token string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	var zero domain.HostingDatabaseNetworkCaptureClaim
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	repo, ok := s.runs.(store.HostingDatabaseNetworkCaptureClaimRepository)
	if !ok {
		return zero, store.ErrHostingDatabaseNetworkCaptureUnavailable
	}
	return repo.ClaimNextHostingDatabaseNetworkCapture(ctx, runner, hash, s.leaseTTL)
}

// RunnerHostingDatabaseNetworkCapturePlan loads the exact read-only target under fresh authority.
func (s *Service) RunnerHostingDatabaseNetworkCapturePlan(ctx context.Context, token string, a domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error) {
	var zero domain.HostingDatabaseNetworkCapture
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseNetworkCaptureRepository()
	if err != nil {
		return zero, err
	}
	return repo.HostingDatabaseNetworkCapturePlan(ctx, runner, hash, a)
}

// RunnerRecordHostingDatabaseNetworkOrigin submits full attestation under the current runner identity.
func (s *Service) RunnerRecordHostingDatabaseNetworkOrigin(ctx context.Context, token string, in domain.HostingDatabaseNetworkOriginRequest) (domain.HostingDatabaseNetworkOriginReceipt, error) {
	var zero domain.HostingDatabaseNetworkOriginReceipt
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidHostingDatabaseNetworkCapture)
	}
	repo, err := s.databaseNetworkCaptureRepository()
	if err != nil {
		return zero, err
	}
	return repo.RecordHostingDatabaseNetworkOrigin(ctx, runner, hash, in)
}
