package app

import (
	"context"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// RunnerHostingBuildRegistryCredential delivers only the selected live build credential.
func (s *Service) RunnerHostingBuildRegistryCredential(ctx context.Context, in domain.HostingBuildRegistryCredentialRequest, token string) (*domain.HostingRegistryCredentialDelivery, error) {
	if hostingbuildprotocol.ValidateAuthority(in.Authority) != nil || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) || token == "" || s.secretKeyring == nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	current := func() (string, error) {
		p, err := s.auth.CurrentPrincipal(ctx)
		if err != nil || p.Provider != domain.PrincipalRunner {
			return "", ErrHostingRegistryCredentialUnavailable
		}
		r, err := s.CurrentRunnerIdentity(ctx)
		if err != nil || r.RunnerID != p.ID || !slices.Contains(r.Capabilities, domain.HostingBuildRegistryCapability) {
			return "", ErrHostingRegistryCredentialUnavailable
		}
		original, err := s.AuthenticateRunnerToken(ctx, token)
		if err != nil || original.ID != p.ID || original.Provider != domain.PrincipalRunner {
			return "", ErrHostingRegistryCredentialUnavailable
		}
		return p.ID, nil
	}
	runner, err := current()
	if err != nil {
		return nil, err
	}
	repo, ok := s.runs.(store.HostingBuildRegistryRepository)
	if !ok {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	authorized, err := repo.AuthorizeHostingBuildRegistryCredential(ctx, runner, in)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	defer clear(authorized.Record.Envelope.Data)
	if authorized.Window.DeliveryID != in.DeliveryID || authorized.Window.RequestHash != domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingBuildRegistryCredentialRequest
	}{runner, in}) || authorized.Record.Metadata.SecretID != authorized.Ref.SecretID || authorized.Record.Metadata.Version != authorized.Ref.Version {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	if !s.clock().Before(authorized.Window.UseUntil) || authorized.Window.UseUntil.After(s.clock().Add(time.Minute)) || ctx.Err() != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	raw, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: authorized.ProjectID, SecretID: authorized.Ref.SecretID, Version: authorized.Ref.Version}, authorized.Record.Envelope)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	defer clear(raw)
	credential, err := domain.DecodeHostingRegistryCredential(raw)
	if err != nil {
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	if _, err = current(); err != nil || ctx.Err() != nil || !s.clock().Before(authorized.Window.UseUntil) {
		credential.Close()
		return nil, ErrHostingRegistryCredentialUnavailable
	}
	return &domain.HostingRegistryCredentialDelivery{Credential: credential, UseUntil: authorized.Window.UseUntil}, nil
}
