package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func (s *Service) RunnerBackupRemoteCredentials(ctx context.Context, in domain.BackupRemoteCredentialRequest, token string) (*domain.BackupRemoteCredentials, error) {
	return s.runnerBackupCredentials(ctx, in, token, false)
}
func (s *Service) RunnerBackupRetentionCredentials(ctx context.Context, in domain.BackupRemoteCredentialRequest, token string) (*domain.BackupRemoteCredentials, error) {
	return s.runnerBackupCredentials(ctx, in, token, true)
}
func (s *Service) runnerBackupCredentials(ctx context.Context, in domain.BackupRemoteCredentialRequest, token string, retention bool) (*domain.BackupRemoteCredentials, error) {
	if in.Validate() != nil || retention != (in.RetentionCopyID != "") {
		return nil, InvalidInput(domain.ErrBackupRemoteCredentials)
	}
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	storageFamily := in.Authority.RunID == "storage_transfer_"+in.Authority.OperationID
	token = strings.TrimSpace(token)
	current := func() bool {
		identity, e := s.CurrentRunnerIdentity(ctx)
		capability, kind := domain.HostingDatabaseBackupRemoteCapability, domain.RunTypeDatabaseBackupTransfer
		if storageFamily {
			capability, kind = domain.HostingStorageBackupRemoteCapability, domain.RunTypeStorageBackupTransfer
		}
		if e != nil || identity.RunnerID != principal.ID || !slices.Contains(identity.Capabilities, capability) || !slices.Contains(identity.Capabilities, kind) {
			return false
		}
		if retention && (!slices.Contains(identity.Capabilities, domain.HostingDatabaseBackupRetentionCapability) || !slices.Contains(identity.Capabilities, domain.RunTypeDatabaseBackupRetention)) {
			return false
		}
		p, e := s.AuthenticateRunnerToken(ctx, token)
		return e == nil && p.ID == principal.ID && p.Provider == domain.PrincipalRunner
	}
	repo, ok := s.hosting.(store.HostingDatabaseBackupRemoteCredentialRepository)
	if !ok || s.secretKeyring == nil || token == "" || !current() {
		return nil, domain.ErrBackupRemoteCredentials
	}
	var a store.BackupRemoteCredentialAuthorization
	if storageFamily {
		r, ok := s.hosting.(store.HostingStorageBackupRemoteCredentialRepository)
		if !ok {
			return nil, domain.ErrBackupRemoteCredentials
		}
		a, err = r.AuthorizeStorageRemoteCredentials(ctx, principal.ID, runnerTokenHash(token), in)
	} else if retention {
		r, ok := s.hosting.(store.HostingDatabaseBackupRetentionCredentialRepository)
		if !ok {
			return nil, domain.ErrBackupRemoteCredentials
		}
		a, err = r.AuthorizeBackupRetentionCredentials(ctx, principal.ID, runnerTokenHash(token), in)
	} else {
		a, err = repo.AuthorizeBackupRemoteCredentials(ctx, principal.ID, runnerTokenHash(token), in)
	}
	if err != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	if a.Metadata.Validate() != nil || a.Metadata.Request != in || a.Metadata.RunnerID != principal.ID || !s.clock().Before(a.Metadata.UseUntil) || a.Metadata.UseUntil.After(s.clock().Add(time.Minute)) {
		return nil, domain.ErrBackupRemoteCredentials
	}
	access, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: a.ProjectID, SecretID: a.Records.Access.Metadata.SecretID, Version: a.Records.Access.Metadata.Version}, a.Records.Access.Envelope)
	defer clear(access)
	if err != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	secret, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: a.ProjectID, SecretID: a.Records.Secret.Metadata.SecretID, Version: a.Records.Secret.Metadata.Version}, a.Records.Secret.Envelope)
	defer clear(secret)
	if err != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	out, err := domain.NewBackupRemoteCredentials(a.Metadata, access, secret)
	if err != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	validate := func() error {
		if storageFamily {
			return s.hosting.(store.HostingStorageBackupRemoteCredentialRepository).RevalidateStorageRemoteCredentials(ctx, principal.ID, runnerTokenHash(token), in, a.Metadata)
		}
		if retention {
			r := s.hosting.(store.HostingDatabaseBackupRetentionCredentialRepository)
			return r.RevalidateBackupRetentionCredentials(ctx, principal.ID, runnerTokenHash(token), in, a.Metadata)
		}
		return repo.RevalidateBackupRemoteCredentials(ctx, principal.ID, runnerTokenHash(token), in, a.Metadata)
	}
	if !current() || validate() != nil || ctx.Err() != nil || !s.clock().Before(a.Metadata.UseUntil) {
		out.Close()
		return nil, domain.ErrBackupRemoteCredentials
	}
	return out, nil
}
