package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) hostingStorageMaterializer(ctx context.Context, validate func() error) (store.HostingStorageMaterializationRepository, string, error) {
	identity, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return nil, "", err
	}
	if err = validate(); err != nil {
		return nil, "", InvalidInput(err)
	}
	repo, ok := s.runs.(store.HostingStorageMaterializationRepository)
	if !ok {
		return nil, "", hostingjobs.ErrUnavailable
	}
	return repo, identity.RunnerID, nil
}

// RunnerHostingStorageCreateBegin commits creation admission for the current runner.
func (s *Service) RunnerHostingStorageCreateBegin(ctx context.Context, in domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	repo, id, err := s.hostingStorageMaterializer(ctx, in.Validate)
	if err != nil {
		return domain.HostingStorageCreateAck{}, err
	}
	return repo.HostingStorageCreateBegin(ctx, id, in)
}

// RunnerHostingStorageMaterialized acknowledges exact evidence for the current runner.
func (s *Service) RunnerHostingStorageMaterialized(ctx context.Context, in domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	repo, id, err := s.hostingStorageMaterializer(ctx, in.Validate)
	if err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	return repo.HostingStorageMaterialized(ctx, id, in)
}
