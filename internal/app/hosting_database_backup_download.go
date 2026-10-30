package app

import (
	"context"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) databaseDownloadRepository() (store.HostingDatabaseBackupDownloadRepository, error) {
	r, ok := s.runs.(store.HostingDatabaseBackupDownloadRepository)
	if !ok {
		return nil, store.ErrDatabaseDownloadUnavailable
	}
	return r, nil
}

// DatabaseBackupDownloadOptions requires current owner and destination authority.
func (s *Service) DatabaseBackupDownloadOptions(ctx context.Context, id string) (domain.HostingDatabaseBackupDownloadOptions, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupDownloadOptions{}, err
	}
	if err = validateDestinationID(id, "backup_id"); err != nil {
		return domain.HostingDatabaseBackupDownloadOptions{}, err
	}
	r, err := s.databaseDownloadRepository()
	if err != nil {
		return domain.HostingDatabaseBackupDownloadOptions{}, err
	}
	return r.DatabaseBackupDownloadOptions(ctx, actor.ID, id)
}

// CreateDatabaseBackupDownload admits a fresh one-use read, never a restore.
func (s *Service) CreateDatabaseBackupDownload(ctx context.Context, id, backup string, expires time.Time) (domain.HostingDatabaseBackupDownloadPlan, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	if err = validateDestinationID(backup, "backup_id"); err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	r, err := s.databaseDownloadRepository()
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	return r.CreateDatabaseBackupDownload(ctx, id, actor.ID, backup, expires)
}

// RunnerDatabaseBackupDownloadPlan binds one read to the current full lease.
func (s *Service) RunnerDatabaseBackupDownloadPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	if (domain.HostingDatabaseBackupDownloadStream{HostingAuthority: a, PlanHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}).Validate() != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, InvalidInput(domain.ErrInvalidDatabaseBackupDownload)
	}
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	r, err := s.databaseDownloadRepository()
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	return r.DatabaseBackupDownloadPlan(ctx, runner.ID, a)
}

// BeginDatabaseBackupDownload consumes stream authority exactly once.
func (s *Service) BeginDatabaseBackupDownload(ctx context.Context, in domain.HostingDatabaseBackupDownloadStream) (domain.HostingDatabaseBackupDownloadPlan, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, InvalidInput(domain.ErrInvalidDatabaseBackupDownload)
	}
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	r, err := s.databaseDownloadRepository()
	if err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, err
	}
	return r.BeginDatabaseBackupDownload(ctx, runner.ID, in)
}

// CloseDatabaseBackupDownload closes metadata for an admitted controller-owned read.
func (s *Service) CloseDatabaseBackupDownload(ctx context.Context, id string, success bool) error {
	r, err := s.databaseDownloadRepository()
	if err != nil {
		return err
	}
	return r.CloseDatabaseBackupDownload(ctx, id, success)
}
