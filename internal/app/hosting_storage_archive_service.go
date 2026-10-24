package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageArchiveRepository() (store.HostingStorageArchiveRepository, error) {
	r, ok := s.runs.(store.HostingStorageArchiveRepository)
	if !ok {
		return nil, store.ErrStorageArchiveUnavailable
	}
	return r, nil
}

// RequestStorageBackup authorizes current owner intent for one exact resource.
func (s *Service) RequestStorageBackup(ctx context.Context, resourceID string, in domain.HostingStorageBackupInput) (domain.HostingStorageBackup, error) {
	var zero domain.HostingStorageBackup
	r, actor, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if err = supportedHostingStorage(r); err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return zero, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.storage_archive.requested", id, nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestStorageBackup(ctx, id, resourceID, actor.ID, in, audit)
}

// ListStorageBackups requires current viewer membership and destination access.
func (s *Service) ListStorageBackups(ctx context.Context, resourceID string, page store.Page) (store.PageResult[domain.HostingStorageBackup], error) {
	var zero store.PageResult[domain.HostingStorageBackup]
	r, actor, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleViewer)
	if err != nil {
		return zero, err
	}
	if err = supportedHostingStorage(r); err != nil {
		return zero, err
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	return repo.ListStorageBackups(ctx, actor.ID, resourceID, page)
}

// GetStorageBackup reads metadata only under current persisted viewer authority.
func (s *Service) GetStorageBackup(ctx context.Context, id string) (domain.HostingStorageBackup, error) {
	var zero domain.HostingStorageBackup
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if err = validateDestinationID(id, "backup_id"); err != nil {
		return zero, err
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	return repo.GetStorageBackup(ctx, actor.ID, id)
}

// RequestStorageRestore creates new resource and binding identities internally.
// The repository atomically checks owner authority and persists the target hold.
func (s *Service) RequestStorageRestore(ctx context.Context, backupID string, in domain.HostingStorageRestoreInput) (domain.HostingStorageRestore, error) {
	var zero domain.HostingStorageRestore
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if err = validateDestinationID(backupID, "backup_id"); err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return zero, err
	}
	resource, err := prefixedID("res")
	if err != nil {
		return zero, err
	}
	binding, err := prefixedID("binding")
	if err != nil {
		return zero, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.storage_archive.restore_requested", id, nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestStorageRestore(ctx, id, backupID, actor.ID, resource, binding, in, audit)
}

// GetStorageRestore returns the held target and safe independent operation state.
func (s *Service) GetStorageRestore(ctx context.Context, id string) (domain.HostingStorageRestore, error) {
	var zero domain.HostingStorageRestore
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if err = validateDestinationID(id, "restore_id"); err != nil {
		return zero, err
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	return repo.GetStorageRestore(ctx, actor.ID, id)
}

// RequestStorageArchiveRecovery grants fresh narrow authority to verify/resume
// exact recorded writers or verify a held target, never repeat archive/extract.
func (s *Service) RequestStorageArchiveRecovery(ctx context.Context, operationID string, in domain.HostingStorageArchiveRecoveryInput) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if err = validateDestinationID(operationID, "operation_id"); err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return zero, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.storage_archive.recovery_requested", id, nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestStorageArchiveRecovery(ctx, id, operationID, actor.ID, in, audit)
}

func (s *Service) withStorageArchiveRunner(ctx context.Context, a domain.HostingAuthority, call func(store.HostingStorageArchiveRepository, string) (domain.HostingOperation, error)) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if err = validateDestinationID(a.OperationID, "operation_id"); err != nil {
		return zero, err
	}
	repo, err := s.storageArchiveRepository()
	if err != nil {
		return zero, err
	}
	return call(repo, runner.ID)
}

// RunnerStorageArchivePlan returns fenced family-specific immutable authority.
func (s *Service) RunnerStorageArchivePlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingOperation, error) {
	return s.withStorageArchiveRunner(ctx, in, func(r store.HostingStorageArchiveRepository, id string) (domain.HostingOperation, error) {
		return r.StorageArchivePlan(ctx, id, in)
	})
}

// RunnerStorageArchivePrepared confirms the configured local helper identity.
func (s *Service) RunnerStorageArchivePrepared(ctx context.Context, in domain.HostingStorageArchivePreparedRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	return s.withStorageArchiveRunner(ctx, in.HostingAuthority, func(r store.HostingStorageArchiveRepository, id string) (domain.HostingOperation, error) {
		return r.StorageArchivePrepared(ctx, id, in)
	})
}

// RunnerStorageArchiveApplyBegin requests durable authority before runtime effects.
func (s *Service) RunnerStorageArchiveApplyBegin(ctx context.Context, in domain.HostingStorageArchiveApplyBeginRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	return s.withStorageArchiveRunner(ctx, in.HostingAuthority, func(r store.HostingStorageArchiveRepository, id string) (domain.HostingOperation, error) {
		return r.StorageArchiveApplyBegin(ctx, id, in)
	})
}

// RunnerStorageArchiveProgress commits immutable safe progress before next effects.
func (s *Service) RunnerStorageArchiveProgress(ctx context.Context, in domain.HostingStorageArchiveProgress) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	return s.withStorageArchiveRunner(ctx, in.HostingAuthority, func(r store.HostingStorageArchiveRepository, id string) (domain.HostingOperation, error) {
		return r.StorageArchiveProgress(ctx, id, in)
	})
}

// RunnerStorageArchiveComplete settles independent archive and resume outcomes.
func (s *Service) RunnerStorageArchiveComplete(ctx context.Context, in domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	return s.withStorageArchiveRunner(ctx, in.HostingAuthority, func(r store.HostingStorageArchiveRepository, id string) (domain.HostingOperation, error) {
		return r.StorageArchiveComplete(ctx, id, in)
	})
}
