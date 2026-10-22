package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrHostingRuntimeEnvironmentUnavailable excludes secret and storage diagnostics.
var ErrHostingRuntimeEnvironmentUnavailable = errors.New("hosting_runtime_environment_unavailable")

// HostingRuntimeEnvironmentDelivery decrypts only after the repository commits authorization.
type HostingRuntimeEnvironmentDelivery struct {
	auth       auth.Provider
	runners    HostingSourceRunnerIdentity
	repository store.HostingRuntimeEnvironmentRepository
	keys       *secretcrypto.Keyring
	clock      func() time.Time
}

// NewHostingRuntimeEnvironmentDelivery accepts immutable keys and a trusted clock.
func NewHostingRuntimeEnvironmentDelivery(provider auth.Provider, runners HostingSourceRunnerIdentity, repository store.HostingRuntimeEnvironmentRepository, keys *secretcrypto.Keyring, clock func() time.Time) (*HostingRuntimeEnvironmentDelivery, error) {
	if provider == nil || runners == nil || repository == nil || clock == nil {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	return &HostingRuntimeEnvironmentDelivery{provider, runners, repository, keys, clock}, nil
}

// Deliver verifies current authority and consumes only committed authorization.
func (s *HostingRuntimeEnvironmentDelivery) Deliver(ctx context.Context, in domain.HostingRuntimeEnvironmentRequest) (*hostingruntimeprotocol.Environment, error) {
	if hostingruntimeprotocol.ValidateRequest(in) != nil {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	principal, err := s.auth.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if principal.Provider != domain.PrincipalRunner || principal.ID == "" {
		return nil, auth.ErrForbidden
	}
	runner, err := s.runners.CurrentRunnerIdentity(ctx)
	if err != nil || runner.RunnerID != principal.ID || !slices.Contains(runner.Capabilities, domain.HostingRuntimeEnvironmentCapability) {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	verified, err := s.repository.AuthorizeHostingRuntimeEnvironment(ctx, runner.RunnerID, in)
	if err != nil {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	defer verified.Close()
	hash := hostingruntimeprotocol.RuntimeEnvironmentRequestHash(runner.RunnerID, in)
	if verified.ProjectID == "" || verified.Window.DeliveryID != in.DeliveryID || verified.Window.RequestHash != hash || !s.clock().Before(verified.Window.UseUntil) {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	doc, refs, err := domain.CanonicalHostingRuntimeDocument(verified.Document.Entries)
	if err != nil || domain.HostingDocumentHash(doc) != in.RuntimeConfiguration.Hash || len(refs) != len(verified.Records) {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	entries := make([]hostingruntimeprotocol.Entry, 0, len(doc.Entries))
	defer func() {
		for _, entry := range entries {
			clear(entry.Value)
		}
	}()
	for _, entry := range doc.Entries {
		var value []byte
		if entry.Kind == "literal" {
			value = []byte(*entry.Value)
		} else {
			record, ok := verified.Records[*entry.Secret]
			if !ok || s.keys == nil || record.Metadata.SecretID != entry.Secret.SecretID || record.Metadata.Version != entry.Secret.Version {
				return nil, ErrHostingRuntimeEnvironmentUnavailable
			}
			value, err = s.keys.Open(secretcrypto.Binding{ProjectID: verified.ProjectID, SecretID: entry.Secret.SecretID, Version: entry.Secret.Version}, record.Envelope)
			if err != nil {
				clear(value)
				return nil, ErrHostingRuntimeEnvironmentUnavailable
			}
		}
		resolved := hostingruntimeprotocol.Entry{Name: entry.Name, Value: value}
		if hostingruntimeprotocol.ValidateEntries([]hostingruntimeprotocol.Entry{resolved}) != nil {
			clear(value)
			return nil, ErrHostingRuntimeEnvironmentUnavailable
		}
		entries = append(entries, resolved)
	}
	current, err := s.runners.CurrentRunnerIdentity(ctx)
	if err != nil || current.RunnerID != runner.RunnerID || !slices.Contains(current.Capabilities, domain.HostingRuntimeEnvironmentCapability) || ctx.Err() != nil || !s.clock().Before(verified.Window.UseUntil) {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	out, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: in.Authority, DeliveryID: in.DeliveryID, PlanHash: in.PlanHash, RuntimeConfiguration: in.RuntimeConfiguration, RequestHash: hash, Purpose: hostingruntimeprotocol.Purpose, UseUntil: verified.Window.UseUntil}, entries)
	if err != nil {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	return out, nil
}

// RunnerHostingRuntimeEnvironment connects dedicated runner authentication to delivery.
func (s *Service) RunnerHostingRuntimeEnvironment(ctx context.Context, in domain.HostingRuntimeEnvironmentRequest) (*hostingruntimeprotocol.Environment, error) {
	if hostingruntimeprotocol.ValidateRequest(in) != nil {
		return nil, InvalidInput(hostingruntimeprotocol.ErrInvalidMessage)
	}
	repository, ok := s.hosting.(store.HostingRuntimeEnvironmentRepository)
	if !ok {
		return nil, ErrHostingRuntimeEnvironmentUnavailable
	}
	delivery, err := NewHostingRuntimeEnvironmentDelivery(s.auth, s, repository, s.secretKeyring, s.clock)
	if err != nil {
		return nil, err
	}
	return delivery.Deliver(ctx, in)
}
