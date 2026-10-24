package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrHostingDatabaseCredentialsUnavailable suppresses credential and repository diagnostics.
var ErrHostingDatabaseCredentialsUnavailable = errors.New("hosting_database_credentials_unavailable")

// HostingDatabaseCredentialDelivery decrypts only committed, scoped authorization.
type HostingDatabaseCredentialDelivery struct {
	auth       auth.Provider
	runners    HostingRuntimeRunnerIdentity
	repository store.HostingDatabaseCredentialRepository
	keys       *secretcrypto.Keyring
	clock      func() time.Time
}

// NewHostingDatabaseCredentialDelivery accepts immutable keys and a trusted clock.
func NewHostingDatabaseCredentialDelivery(provider auth.Provider, runners HostingRuntimeRunnerIdentity, repository store.HostingDatabaseCredentialRepository, keys *secretcrypto.Keyring, clock func() time.Time) (*HostingDatabaseCredentialDelivery, error) {
	if provider == nil || runners == nil || repository == nil || clock == nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	return &HostingDatabaseCredentialDelivery{provider, runners, repository, keys, clock}, nil
}

func (s *HostingDatabaseCredentialDelivery) currentCredential(ctx context.Context, token, runner string) error {
	if token == "" {
		return ErrHostingDatabaseCredentialsUnavailable
	}
	identity, err := s.runners.CurrentRunnerIdentity(ctx)
	if err != nil || identity.RunnerID != runner {
		return ErrHostingDatabaseCredentialsUnavailable
	}
	for _, capability := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
		if !slices.Contains(identity.Capabilities, capability) {
			return ErrHostingDatabaseCredentialsUnavailable
		}
	}
	p, err := s.runners.AuthenticateRunnerToken(ctx, token)
	if err != nil || p.Provider != domain.PrincipalRunner || p.ID != runner {
		return ErrHostingDatabaseCredentialsUnavailable
	}
	return nil
}

// Deliver retains the original bearer and deadline across decryption and a fresh
// database authority transaction. Returned bytes cannot subsequently be recalled.
func (s *HostingDatabaseCredentialDelivery) Deliver(ctx context.Context, in domain.HostingDatabaseCredentialRequest, token string) (*hostingdatabaseprotocol.Credentials, error) {
	if in.Validate() != nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	token = strings.TrimSpace(token)
	p, err := s.auth.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Provider != domain.PrincipalRunner || p.ID == "" {
		return nil, auth.ErrForbidden
	}
	if s.currentCredential(ctx, token, p.ID) != nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	a, err := s.repository.AuthorizeHostingDatabaseCredentials(ctx, p.ID, runnerTokenHash(token), in)
	if err != nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	defer a.Close()
	m := a.Metadata
	if hostingdatabaseprotocol.ValidateMetadata(m) != nil || a.ProjectID == "" || m.RunnerID != p.ID || m.Authority != in.Authority || m.DeliveryID != in.DeliveryID || m.Mode != in.Mode || m.PlanHash != in.PlanHash || m.RequestHash != hostingdatabaseprotocol.RequestHash(p.ID, in) || m.Purpose != hostingdatabaseprotocol.Purpose || !s.clock().Before(m.UseUntil) || m.UseUntil.After(s.clock().Add(time.Minute)) || len(a.Records) != 2 || len(m.References) != 2 {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	if s.currentCredential(ctx, token, p.ID) != nil || ctx.Err() != nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	entries := make([]hostingdatabaseprotocol.Credential, 0, len(m.References))
	defer func() {
		for _, e := range entries {
			clear(e.Value)
		}
	}()
	for _, ref := range m.References {
		r, ok := a.Records[ref]
		if !ok || s.keys == nil || r.Metadata.SecretID != ref.SecretID || r.Metadata.Version != ref.Version {
			return nil, ErrHostingDatabaseCredentialsUnavailable
		}
		value, e := s.keys.Open(secretcrypto.Binding{ProjectID: a.ProjectID, SecretID: ref.SecretID, Version: ref.Version}, r.Envelope)
		if e != nil {
			clear(value)
			return nil, ErrHostingDatabaseCredentialsUnavailable
		}
		entries = append(entries, hostingdatabaseprotocol.Credential{Reference: ref, Value: value})
	}
	out, err := hostingdatabaseprotocol.NewCredentials(m, entries)
	if err != nil {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	// The fresh transaction rechecks operation, actor, bearer, secret status,
	// capabilities, mode, lease/fence and the unchanged committed deadline.
	if s.currentCredential(ctx, token, p.ID) != nil || s.repository.RevalidateHostingDatabaseCredentials(ctx, p.ID, runnerTokenHash(token), in, m) != nil || ctx.Err() != nil || !s.clock().Before(m.UseUntil) {
		out.Close()
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	return out, nil
}

// RunnerHostingDatabaseCredentials connects dedicated runner authentication to delivery.
func (s *Service) RunnerHostingDatabaseCredentials(ctx context.Context, in domain.HostingDatabaseCredentialRequest, token string) (*hostingdatabaseprotocol.Credentials, error) {
	if in.Validate() != nil {
		return nil, InvalidInput(hostingdatabaseprotocol.ErrInvalidMessage)
	}
	repo, ok := s.hosting.(store.HostingDatabaseCredentialRepository)
	if !ok {
		return nil, ErrHostingDatabaseCredentialsUnavailable
	}
	delivery, err := NewHostingDatabaseCredentialDelivery(s.auth, s, repo, s.secretKeyring, s.clock)
	if err != nil {
		return nil, err
	}
	return delivery.Deliver(ctx, in, token)
}
