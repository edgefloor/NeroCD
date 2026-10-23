package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// CreateHostingDatabase generates distinct credentials and seals them before
// the repository atomically commits desired metadata. It never starts an engine.
func (s *Service) CreateHostingDatabase(ctx context.Context, in domain.HostingDatabaseCreateInput) (domain.HostingDatabase, error) {
	var zero domain.HostingDatabase
	actor, err := s.authorizeHostingProject(ctx, in.ProjectID, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	if _, err = s.hostingEnvironmentInProject(ctx, in.ProjectID, in.ProjectEnvironmentID); err != nil {
		return zero, err
	}
	repo, ok := s.hosting.(store.HostingDatabaseRepository)
	if !ok {
		return zero, store.ErrHostingDatabaseUnavailable
	}
	if s.secretKeyring == nil {
		return zero, ErrManagedSecretsUnavailable
	}
	recipe, err := domain.HostingDatabaseRecipeFor(in.Engine, in.RecipeVersion)
	if err != nil {
		return zero, InvalidInput(err)
	}
	resource := domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID)
	now := s.clock().UTC()
	credentials := make([]store.HostingDatabaseCredentialRecord, 0, len(recipe.Credentials))
	for _, ref := range recipe.Credentials {
		ref.SecretID, ref.Version = domain.HostingDatabaseSecretID(resource, ref.Role), 1
		var entropy [32]byte
		if _, err = rand.Read(entropy[:]); err != nil {
			return zero, errors.New("database credential generation failed")
		}
		plaintext := []byte(base64.RawURLEncoding.EncodeToString(entropy[:]))
		envelope, sealErr := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: in.ProjectID, SecretID: ref.SecretID, Version: 1}, plaintext)
		clear(plaintext)
		clear(entropy[:])
		if sealErr != nil {
			return zero, errors.New("database credential encryption failed")
		}
		credentials = append(credentials, store.HostingDatabaseCredentialRecord{Reference: ref, Record: store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: ref.SecretID, Version: 1, CreatedBy: actor.ID, CreatedAt: now}, Envelope: envelope}})
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database.created", resource, nil)
	if err != nil {
		return zero, err
	}
	view, err := repo.CreateHostingDatabase(ctx, actor.ID, in, credentials, event)
	if err == nil && (view.Resource.ID != resource || view.Resource.ProjectID != in.ProjectID || view.Resource.Kind != "database" || view.Engine != in.Engine || view.RecipeVersion != in.RecipeVersion || view.RuntimeState != domain.HostingDatabaseUnqualified) {
		return zero, store.ErrConflict
	}
	return view, err
}

// GetHostingDatabase returns nonsecret metadata after persisted viewer checks.
func (s *Service) GetHostingDatabase(ctx context.Context, resource string) (domain.HostingDatabase, error) {
	var zero domain.HostingDatabase
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return zero, err
	}
	if r.Kind != "database" {
		return zero, store.ErrNotFound
	}
	repo, ok := s.hosting.(store.HostingDatabaseRepository)
	if !ok {
		return zero, store.ErrHostingDatabaseUnavailable
	}
	view, err := repo.GetHostingDatabase(ctx, actor.ID, r.ID)
	if err == nil && (view.Resource.ID != r.ID || view.Resource.ProjectID != r.ProjectID || view.RuntimeState != domain.HostingDatabaseUnqualified) {
		return zero, store.ErrConflict
	}
	return view, err
}
