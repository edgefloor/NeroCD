package app

import (
	"context"
	"errors"
	"math"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) buildLogs() (store.HostingBuildLogRepository, error) {
	r, ok := s.runs.(store.HostingBuildLogRepository)
	if !ok {
		return nil, store.ErrHostingBuildUnavailable
	}
	return r, nil
}

// AppendHostingBuildLogs authenticates the persisted runner, never a caller ID.
func (s *Service) AppendHostingBuildLogs(ctx context.Context, in domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return domain.HostingBuildLogAck{}, err
	}
	in, err = domain.CanonicalHostingBuildLogs(in)
	if err != nil {
		return domain.HostingBuildLogAck{}, InvalidInput(err)
	}
	repo, err := s.buildLogs()
	if err != nil {
		return domain.HostingBuildLogAck{}, err
	}
	return repo.AppendHostingBuildLogs(ctx, p.RunnerID, in)
}

// HostingBuildLogs rechecks current viewer access on every reconnect read.
func (s *Service) HostingBuildLogs(ctx context.Context, id string, after, limit int) (domain.HostingBuildLogPage, error) {
	p, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingBuildLogPage{}, err
	}
	if err = validateDestinationID(id, "build_id"); err != nil {
		return domain.HostingBuildLogPage{}, err
	}
	if after < 0 || after > math.MaxInt32 || limit < 1 || limit > domain.HostingBuildLogReadLimit {
		return domain.HostingBuildLogPage{}, InvalidInput(errors.New("invalid build log cursor or limit"))
	}
	repo, err := s.buildLogs()
	if err != nil {
		return domain.HostingBuildLogPage{}, err
	}
	return repo.ListHostingBuildLogs(ctx, p.ID, id, after, limit)
}
