package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrHostingBuildSecretsUnavailable excludes secret and authorization diagnostics.
var ErrHostingBuildSecretsUnavailable = errors.New("hosting_build_secrets_unavailable")

// RunnerHostingBuildSecrets delivers only snapshot-selected live build secrets.
func (s *Service) RunnerHostingBuildSecrets(ctx context.Context, in domain.HostingBuildSecretsRequest, token string) (*domain.HostingBuildSecretsDelivery, error) {
	if hostingbuildprotocol.ValidateAuthority(in.Authority) != nil || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) || token == "" || s.secretKeyring == nil {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	current := func() (string, error) {
		p, err := s.auth.CurrentPrincipal(ctx)
		if err != nil || p.Provider != domain.PrincipalRunner {
			return "", ErrHostingBuildSecretsUnavailable
		}
		r, err := s.CurrentRunnerIdentity(ctx)
		if err != nil || r.RunnerID != p.ID || !slices.Contains(r.Capabilities, domain.HostingBuildSecretsCapability) {
			return "", ErrHostingBuildSecretsUnavailable
		}
		original, err := s.AuthenticateRunnerToken(ctx, token)
		if err != nil || original.ID != p.ID || original.Provider != domain.PrincipalRunner {
			return "", ErrHostingBuildSecretsUnavailable
		}
		return p.ID, nil
	}
	runner, err := current()
	if err != nil {
		return nil, err
	}
	repo, ok := s.runs.(store.HostingBuildSecretsRepository)
	if !ok {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	authorized, err := repo.AuthorizeHostingBuildSecrets(ctx, runner, in)
	if err != nil {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	defer func() {
		for _, secret := range authorized.Secrets {
			clear(secret.Record.Envelope.Data)
		}
	}()
	if authorized.ProjectID == "" || len(authorized.Secrets) == 0 || len(authorized.Secrets) > 64 || authorized.Window.DeliveryID != in.DeliveryID || authorized.Window.RequestHash != domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingBuildSecretsRequest
	}{runner, in}) {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	if !s.clock().Before(authorized.Window.UseUntil) || authorized.Window.UseUntil.After(s.clock().Add(time.Minute)) || ctx.Err() != nil {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	out := &domain.HostingBuildSecretsDelivery{UseUntil: authorized.Window.UseUntil}
	accepted := false
	defer func() {
		if !accepted {
			out.Close()
		}
	}()
	names := map[string]bool{}
	for _, item := range authorized.Secrets {
		ref := item.Ref
		if ref.Secret.Validate() != nil || names[ref.Name] || item.Record.Metadata.SecretID != ref.Secret.SecretID || item.Record.Metadata.Version != ref.Secret.Version {
			return nil, ErrHostingBuildSecretsUnavailable
		}
		names[ref.Name] = true
		raw, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: authorized.ProjectID, SecretID: ref.Secret.SecretID, Version: ref.Secret.Version}, item.Record.Envelope)
		if err != nil {
			return nil, ErrHostingBuildSecretsUnavailable
		}
		valid := domain.ValidHostingBuildSecretValue(string(raw))
		if valid {
			out.Secrets = append(out.Secrets, domain.HostingBuildSecretValue{Name: ref.Name, Value: string(raw)})
		}
		clear(raw)
		if !valid {
			return nil, ErrHostingBuildSecretsUnavailable
		}
	}
	if _, err = current(); err != nil || ctx.Err() != nil || !s.clock().Before(authorized.Window.UseUntil) {
		return nil, ErrHostingBuildSecretsUnavailable
	}
	accepted = true
	return out, nil
}
