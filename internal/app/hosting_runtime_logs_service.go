package app

import (
	"context"
	"errors"
	"math"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) runtimeLogs() (store.HostingRuntimeLogsRepository, error) {
	repo, ok := s.runs.(store.HostingRuntimeLogsRepository)
	if !ok {
		return nil, hostingjobs.ErrUnavailable
	}
	return repo, nil
}

// OpenRuntimeLogSession admits bounded demand under the current resource viewer.
// Revoked cookies deny new HTTP requests. Already admitted background demand may
// remain for at most thirty seconds and does not promise instant cookie revocation.
func (s *Service) OpenRuntimeLogSession(ctx context.Context, resource string, in domain.HostingRuntimeLogOpen) (domain.HostingRuntimeLogSession, error) {
	item, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.HostingRuntimeLogSession{}, err
	}
	in, err = in.Canonical()
	if err != nil {
		return domain.HostingRuntimeLogSession{}, InvalidInput(err)
	}
	repo, err := s.runtimeLogs()
	if err != nil {
		return domain.HostingRuntimeLogSession{}, err
	}
	return repo.OpenRuntimeLogSession(ctx, actor.ID, item.ID, in)
}

func (s *Service) runtimeLogSessionActor(ctx context.Context, id string) (store.HostingRuntimeLogsRepository, string, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, "", err
	}
	if err = validateDestinationID(id, "session_id"); err != nil {
		return nil, "", err
	}
	repo, err := s.runtimeLogs()
	if err != nil {
		return nil, "", err
	}
	scope, err := repo.RuntimeLogSessionScope(ctx, actor.ID, id)
	if err != nil {
		return nil, "", err
	}
	if scope.ActorID != actor.ID {
		return nil, "", auth.ErrForbidden
	}
	if _, _, err = s.resourceForDestinationRole(ctx, scope.ResourceID, domain.RoleViewer); err != nil {
		return nil, "", err
	}
	return repo, actor.ID, nil
}

// RenewRuntimeLogSession extends only live actor-owned demand.
func (s *Service) RenewRuntimeLogSession(ctx context.Context, id string) (domain.HostingRuntimeLogSession, error) {
	repo, actor, err := s.runtimeLogSessionActor(ctx, id)
	if err != nil {
		return domain.HostingRuntimeLogSession{}, err
	}
	return repo.RenewRuntimeLogSession(ctx, actor, id)
}

// CloseRuntimeLogSession ends actor-owned demand immediately.
func (s *Service) CloseRuntimeLogSession(ctx context.Context, id string) (domain.HostingRuntimeLogSession, error) {
	repo, actor, err := s.runtimeLogSessionActor(ctx, id)
	if err != nil {
		return domain.HostingRuntimeLogSession{}, err
	}
	return repo.CloseRuntimeLogSession(ctx, actor, id)
}

// RuntimeLogPage returns explicitly sensitive cached output under current access.
func (s *Service) RuntimeLogPage(ctx context.Context, id string, after, limit int) (domain.HostingRuntimeLogPage, error) {
	if after < 0 || after > math.MaxInt32 || limit < 1 || limit > domain.HostingRuntimeLogReadLimit {
		return domain.HostingRuntimeLogPage{}, InvalidInput(errors.New("invalid runtime log pagination"))
	}
	repo, actor, err := s.runtimeLogSessionActor(ctx, id)
	if err != nil {
		return domain.HostingRuntimeLogPage{}, err
	}
	return repo.RuntimeLogPage(ctx, actor, id, after, limit)
}

// ClaimRuntimeLogSessions binds collection to the actual authenticated runner bearer.
func (s *Service) ClaimRuntimeLogSessions(ctx context.Context, token string, in domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return domain.HostingRuntimeLogClaims{}, err
	}
	if err = in.Validate(); err != nil {
		return domain.HostingRuntimeLogClaims{}, InvalidInput(err)
	}
	repo, err := s.runtimeLogs()
	if err != nil {
		return domain.HostingRuntimeLogClaims{}, err
	}
	return repo.ClaimRuntimeLogSessions(ctx, runner, hash, in)
}

// AppendRuntimeLogs passes the known bearer only for bounded pre-storage redaction.
// Arbitrary application secrets can remain in this sensitive log output.
func (s *Service) AppendRuntimeLogs(ctx context.Context, token string, in domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return domain.HostingRuntimeLogAck{}, err
	}
	in, err = in.Canonical()
	if err != nil {
		return domain.HostingRuntimeLogAck{}, InvalidInput(err)
	}
	repo, err := s.runtimeLogs()
	if err != nil {
		return domain.HostingRuntimeLogAck{}, err
	}
	return repo.AppendRuntimeLogs(ctx, runner, hash, token, in)
}

// RuntimeLogTargets authorizes current component discovery without collection demand.
func (s *Service) RuntimeLogTargets(ctx context.Context, resource string) (domain.HostingRuntimeLogTargets, error) {
	item, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.HostingRuntimeLogTargets{}, err
	}
	repo, err := s.runtimeLogs()
	if err != nil {
		return domain.HostingRuntimeLogTargets{}, err
	}
	return repo.RuntimeLogTargets(ctx, actor.ID, item.ID)
}
