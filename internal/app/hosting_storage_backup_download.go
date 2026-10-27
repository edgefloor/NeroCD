package app

import (
	"context"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageDownloadRepository() (store.HostingStorageBackupDownloadRepository, error) {
	r, ok := s.runs.(store.HostingStorageBackupDownloadRepository)
	if !ok {
		return nil, store.ErrStorageDownloadUnavailable
	}
	return r, nil
}

// StorageBackupDownloadOptions requires current owner and destination authority.
func (s *Service) StorageBackupDownloadOptions(ctx context.Context, id string) (domain.HostingStorageBackupDownloadOptions, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupDownloadOptions{}, err
	}
	if err = validateDestinationID(id, "backup_id"); err != nil {
		return domain.HostingStorageBackupDownloadOptions{}, err
	}
	r, err := s.storageDownloadRepository()
	if err != nil {
		return domain.HostingStorageBackupDownloadOptions{}, err
	}
	return r.StorageBackupDownloadOptions(ctx, actor.ID, id)
}

// CreateStorageBackupDownload admits a fresh one-use read, never a restore.
func (s *Service) CreateStorageBackupDownload(ctx context.Context, id, backup string, expires time.Time) (domain.HostingStorageBackupDownloadPlan, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	if err = validateDestinationID(backup, "backup_id"); err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	r, err := s.storageDownloadRepository()
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	return r.CreateStorageBackupDownload(ctx, id, actor.ID, backup, expires)
}

// RunnerStorageBackupDownloadPlan binds one read to the current full lease.
func (s *Service) RunnerStorageBackupDownloadPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error) {
	if (domain.HostingStorageBackupDownloadStream{HostingAuthority: a, PlanHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}).Validate() != nil {
		return domain.HostingStorageBackupDownloadPlan{}, InvalidInput(domain.ErrInvalidStorageBackupDownload)
	}
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	r, err := s.storageDownloadRepository()
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	return r.StorageBackupDownloadPlan(ctx, runner.ID, a)
}

// BeginStorageBackupDownload consumes stream authority exactly once.
func (s *Service) BeginStorageBackupDownload(ctx context.Context, in domain.HostingStorageBackupDownloadStream) (domain.HostingStorageBackupDownloadPlan, error) {
	if in.Validate() != nil {
		return domain.HostingStorageBackupDownloadPlan{}, InvalidInput(domain.ErrInvalidStorageBackupDownload)
	}
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	r, err := s.storageDownloadRepository()
	if err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, err
	}
	return r.BeginStorageBackupDownload(ctx, runner.ID, in)
}

// CloseStorageBackupDownload closes metadata for an admitted controller-owned read.
func (s *Service) CloseStorageBackupDownload(ctx context.Context, id string, success bool) error {
	r, err := s.storageDownloadRepository()
	if err != nil {
		return err
	}
	return r.CloseStorageBackupDownload(ctx, id, success)
}
