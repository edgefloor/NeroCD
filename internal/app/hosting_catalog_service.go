package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// HostingCatalog lists catalog templates visible to the project viewer.
func (s *Service) HostingCatalog(ctx context.Context, project string) ([]domain.HostingCatalogTemplate, error) {
	if _, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer); err != nil {
		return nil, err
	}
	return domain.HostingCatalogTemplates(), nil
}

// InstallHostingCatalog installs a catalog template and its declared managed credentials.
func (s *Service) InstallHostingCatalog(ctx context.Context, in domain.HostingCatalogInstallInput) (domain.HostingCatalogInstallation, error) {
	var zero domain.HostingCatalogInstallation
	actor, err := s.authorizeHostingProject(ctx, in.ProjectID, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if err = in.ValidateShape(); err != nil {
		return zero, InvalidInput(err)
	}
	if _, err = s.hostingEnvironmentInProject(ctx, in.ProjectID, in.ProjectEnvironmentID); err != nil {
		return zero, err
	}
	repo, ok := s.hosting.(store.HostingCatalogRepository)
	if !ok {
		return zero, store.ErrConflict
	}
	template, available := domain.LookupHostingCatalogTemplate(in.TemplateID, in.ImmutableVersion)
	if available {
		if err = in.Validate(); err != nil {
			return zero, InvalidInput(err)
		}
	}
	if len(template.Definition.CredentialNames) > 0 && s.secretKeyring == nil {
		return zero, ErrManagedSecretsUnavailable
	}
	resource := domain.HostingCatalogResourceID(in.ProjectID, in.RequestID)
	now := s.clock().UTC()
	records := make([]store.HostingCatalogCredentialRecord, 0, len(template.Definition.CredentialNames))
	for _, name := range template.Definition.CredentialNames {
		var entropy [32]byte
		if _, err = rand.Read(entropy[:]); err != nil {
			return zero, errors.New("catalog credential generation failed")
		}
		value := base64.RawURLEncoding.EncodeToString(entropy[:])
		if name == "LD_SUPERUSER_NAME" {
			value = "ncd_" + base64.RawURLEncoding.EncodeToString(entropy[:12])
		}
		if name == "SERVICE_USER_WORDPRESS" {
			value = "ncd_" + hex.EncodeToString(entropy[:8])
		}
		id := domain.HostingCatalogSecretID(resource, name)
		plaintext := []byte(value)
		envelope, sealErr := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: in.ProjectID, SecretID: id, Version: 1}, plaintext)
		clear(plaintext)
		clear(entropy[:])
		if sealErr != nil {
			return zero, errors.New("catalog credential encryption failed")
		}
		records = append(records, store.HostingCatalogCredentialRecord{Name: name, Record: store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: id, Version: 1, CreatedBy: actor.ID, CreatedAt: now}, Envelope: envelope}})
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.catalog.installed", resource, nil)
	if err != nil {
		return zero, err
	}
	return repo.InstallHostingCatalog(ctx, actor.ID, in, records, event)
}

// GetHostingCatalogInstallation returns an authorized catalog installation view.
func (s *Service) GetHostingCatalogInstallation(ctx context.Context, resource string) (domain.HostingCatalogInstallation, error) {
	var zero domain.HostingCatalogInstallation
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return zero, err
	}
	repo, ok := s.hosting.(store.HostingCatalogRepository)
	if !ok {
		return zero, store.ErrNotFound
	}
	return repo.GetHostingCatalogInstallation(ctx, actor.ID, r.ID)
}

// HostingCatalogDestinationOptions qualifies a page without admitting an installation.
func (s *Service) HostingCatalogDestinationOptions(ctx context.Context, project, id, version string, page store.Page) (domain.HostingCatalogDestinationOptions, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
	if err != nil {
		return domain.HostingCatalogDestinationOptions{}, err
	}
	template, ok := domain.LookupHostingCatalogTemplate(id, version)
	if !ok {
		return domain.HostingCatalogDestinationOptions{}, store.ErrNotFound
	}
	repo, ok := s.hosting.(store.HostingCatalogOptionsRepository)
	if !ok {
		return domain.HostingCatalogDestinationOptions{}, store.ErrConflict
	}
	return repo.HostingCatalogDestinationOptions(ctx, actor.ID, project, template, page)
}
