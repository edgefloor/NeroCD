package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrHostingRegistryCredentialUnavailable excludes credential and transport diagnostics.
var ErrHostingRegistryCredentialUnavailable = errors.New("hosting_registry_credential_unavailable")

// RunnerHostingRegistryCredential authorizes the selected pre-pull secret under live runner authority.
func (s *Service) RunnerHostingRegistryCredential(ctx context.Context, in domain.HostingRegistryCredentialRequest, token string) (*domain.HostingRegistryCredentialDelivery, error) {
	if in.Validate() != nil || token == "" || s.secretKeyring == nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	current := func() error {
		p, err := s.auth.CurrentPrincipal(ctx)
		if err != nil || p.Provider != domain.PrincipalRunner {
			return ErrHostingRegistryCredentialUnavailable
		}
		r, err := s.CurrentRunnerIdentity(ctx)
		if err != nil || r.RunnerID != p.ID || !slices.Contains(r.Capabilities, domain.HostingRegistryPullCapability) {
			return ErrHostingRegistryCredentialUnavailable
		}
		original, err := s.AuthenticateRunnerToken(ctx, token)
		if err != nil || original.ID != p.ID || original.Provider != domain.PrincipalRunner {
			return ErrHostingRegistryCredentialUnavailable
		}
		return nil
	}
	if err := current(); err != nil {
		return nil, err
	}
	p, _ := s.auth.CurrentPrincipal(ctx)
	repo, ok := s.hosting.(store.HostingRegistryRepository)
	if !ok {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	verified, err := repo.AuthorizeHostingRegistryCredential(ctx, p.ID, runnerTokenHash(token), in)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	defer verified.Close()
	if !s.clock().Before(verified.UseUntil) || verified.UseUntil.After(s.clock().Add(time.Minute)) || current() != nil || ctx.Err() != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	raw, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: verified.ProjectID, SecretID: verified.Ref.SecretID, Version: verified.Ref.Version}, verified.Record.Envelope)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	defer clear(raw)
	credential, err := domain.DecodeHostingRegistryCredential(raw)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	if credential.Registry != verified.Registry || current() != nil || ctx.Err() != nil || !s.clock().Before(verified.UseUntil) {
		credential.Close()
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	return &domain.HostingRegistryCredentialDelivery{Credential: credential, UseUntil: verified.UseUntil}, nil
}
