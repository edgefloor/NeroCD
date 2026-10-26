package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageBackupDeletionRepository() (store.HostingStorageBackupDeletionRepository, error) {
	r, ok := s.hosting.(store.HostingStorageBackupDeletionRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// PreviewStorageBackupDeletion previews deletion of the selected backup copy.
func (s *Service) PreviewStorageBackupDeletion(ctx context.Context, resource, backup string, in domain.HostingStorageBackupDeletionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	var z domain.HostingStorageBackupRetentionPreview
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidStorageBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupDeletionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("sbdp")
	if e != nil {
		return z, e
	}
	return r.PreviewStorageBackupDeletion(ctx, id, a.ID, resource, backup, in)
}

// ExecuteStorageBackupDeletion admits manual deletion from a saved backup preview.
func (s *Service) ExecuteStorageBackupDeletion(ctx context.Context, resource, backup string, in domain.HostingStorageBackupDeletionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	var z domain.HostingStorageBackupRetentionExecution
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidStorageBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupDeletionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("sbde")
	if e != nil {
		return z, e
	}
	return r.ExecuteStorageBackupDeletion(ctx, id, a.ID, resource, backup, in)
}

// ListStorageBackupDeletions lists manual deletion executions for the selected backup.
func (s *Service) ListStorageBackupDeletions(ctx context.Context, resource, backup string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return nil, e
	}
	r, e := s.storageBackupDeletionRepository()
	if e != nil {
		return nil, e
	}
	return r.ListStorageBackupDeletions(ctx, a.ID, resource, backup)
}

// GetStorageBackupDeletion returns an authorized manual backup deletion execution.
func (s *Service) GetStorageBackupDeletion(ctx context.Context, resource, backup, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	var z domain.HostingStorageBackupRetentionExecution
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupDeletionRepository()
	if e != nil {
		return z, e
	}
	return r.GetStorageBackupDeletion(ctx, a.ID, resource, backup, id)
}
