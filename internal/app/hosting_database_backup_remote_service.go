package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type BackupTargetCredentials struct{ access, secret []byte }

func NewBackupTargetCredentials(access, secret []byte) (*BackupTargetCredentials, error) {
	if len(access) < 1 || len(access) > 256 || len(secret) < 1 || len(secret) > 4096 {
		return nil, domain.ErrInvalidDatabaseBackupRemote
	}
	return &BackupTargetCredentials{access: append([]byte(nil), access...), secret: append([]byte(nil), secret...)}, nil
}
func (c *BackupTargetCredentials) Close() {
	if c != nil {
		clear(c.access)
		clear(c.secret)
		c.access = nil
		c.secret = nil
	}
}
func (*BackupTargetCredentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[write-only backup target credentials]")
}
func (*BackupTargetCredentials) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func (s *Service) databaseBackupRemoteRepository() (store.HostingDatabaseBackupRemoteRepository, error) {
	r, ok := s.hosting.(store.HostingDatabaseBackupRemoteRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

func (s *Service) SaveDatabaseBackupTarget(ctx context.Context, project, existing string, in domain.HostingDatabaseBackupTargetInput, credentials *BackupTargetCredentials) (domain.HostingDatabaseBackupTarget, error) {
	var zero domain.HostingDatabaseBackupTarget
	if in.Validate() != nil || existing == "" && credentials == nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackupRemote)
	}
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbt")
	if err != nil {
		return zero, err
	}
	var records *store.BackupRemoteCredentialRecords
	var compare store.BackupRemoteCredentialCompare
	if credentials != nil {
		if s.secretKeyring == nil || len(credentials.access) < 1 || len(credentials.secret) < 1 {
			return zero, ErrManagedSecretsUnavailable
		}
		pair := store.BackupRemoteCredentialRecords{}
		for _, item := range []struct {
			value  []byte
			record *store.ManagedSecretRecord
		}{{credentials.access, &pair.Access}, {credentials.secret, &pair.Secret}} {
			ref, e := prefixedID("dbk")
			if e != nil {
				return zero, e
			}
			envelope, e := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: project, SecretID: ref, Version: 1}, item.value)
			if e != nil {
				return zero, errors.New("backup target encryption unavailable")
			}
			*item.record = store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: ref, Version: 1, CreatedBy: actor.ID, CreatedAt: s.clock().UTC()}, Envelope: envelope}
		}
		records = &pair
		compare = func(saved store.BackupRemoteCredentialRecords) error {
			for _, item := range []struct {
				want   []byte
				record store.ManagedSecretRecord
			}{{credentials.access, saved.Access}, {credentials.secret, saved.Secret}} {
				value, e := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: project, SecretID: item.record.Metadata.SecretID, Version: item.record.Metadata.Version}, item.record.Envelope)
				match := subtle.ConstantTimeCompare(value, item.want)
				clear(value)
				if e != nil || match != 1 {
					return store.ErrConflict
				}
			}
			return nil
		}
	}
	return repo.SaveDatabaseBackupTarget(ctx, id, project, actor.ID, existing, in, records, compare)
}

func (s *Service) ListDatabaseBackupTargets(ctx context.Context, project string) ([]domain.HostingDatabaseBackupTarget, error) {
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListDatabaseBackupTargets(ctx, actor.ID, project)
}
func (s *Service) ValidateDatabaseBackupTarget(ctx context.Context, project, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingDatabaseBackupTransfer, error) {
	var zero domain.HostingDatabaseBackupTransfer
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackupRemote)
	}
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbx")
	if err != nil {
		return zero, err
	}
	return repo.ValidateDatabaseBackupTarget(ctx, id, project, actor.ID, target, in)
}
func (s *Service) ListDatabaseBackupTransfers(ctx context.Context, id string, validation bool) ([]domain.HostingDatabaseBackupTransfer, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListDatabaseBackupTransfers(ctx, actor.ID, id, validation)
}
func (s *Service) GetDatabaseBackupTransfer(ctx context.Context, id string) (domain.HostingDatabaseBackupTransfer, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupTransfer{}, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return domain.HostingDatabaseBackupTransfer{}, err
	}
	return repo.GetDatabaseBackupTransfer(ctx, actor.ID, id)
}
func (s *Service) RetryDatabaseBackupTransfer(ctx context.Context, backup string, in domain.HostingDatabaseBackupTransferRetryInput) (domain.HostingDatabaseBackupTransfer, error) {
	var zero domain.HostingDatabaseBackupTransfer
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackupRemote)
	}
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbx")
	if err != nil {
		return zero, err
	}
	return repo.RetryDatabaseBackupTransfer(ctx, id, actor.ID, backup, in)
}
func (s *Service) RunnerDatabaseBackupTransferPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingDatabaseBackupTransferPlan, error) {
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupTransferPlan{}, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return domain.HostingDatabaseBackupTransferPlan{}, err
	}
	return repo.DatabaseBackupTransferPlan(ctx, runner.ID, in)
}
func (s *Service) RunnerDatabaseBackupTransferMutation(ctx context.Context, in domain.HostingDatabaseBackupTransferMutation) (domain.HostingDatabaseBackupTransfer, error) {
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupTransfer{}, err
	}
	repo, err := s.databaseBackupRemoteRepository()
	if err != nil {
		return domain.HostingDatabaseBackupTransfer{}, err
	}
	return repo.DatabaseBackupTransferMutation(ctx, runner.ID, in)
}
