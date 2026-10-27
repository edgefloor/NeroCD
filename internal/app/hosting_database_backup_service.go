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

func (s *Service) databaseBackupRepository() (store.HostingDatabaseBackupRepository, error) {
	r, ok := s.hosting.(store.HostingDatabaseBackupRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// RequestDatabaseBackup admits a logical PostgreSQL backup under current source authority.
func (s *Service) RequestDatabaseBackup(ctx context.Context, resource string, in domain.HostingDatabaseBackupInput) (domain.HostingDatabaseBackupOperation, error) {
	var zero domain.HostingDatabaseBackupOperation
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackup)
	}
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if r.Kind != "database" && (in.Component == "" || r.Source.Kind != "compose") {
		return zero, store.ErrNotFound
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbb")
	if err != nil {
		return zero, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database_backup.requested", id, nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestDatabaseBackup(ctx, id, resource, actor.ID, in, event)
}

// GetDatabaseBackup returns an authorized database backup operation.
func (s *Service) GetDatabaseBackup(ctx context.Context, id string) (domain.HostingDatabaseBackupOperation, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupOperation{}, err
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return domain.HostingDatabaseBackupOperation{}, err
	}
	return repo.GetDatabaseBackup(ctx, actor.ID, id)
}

// ListDatabaseBackups lists authorized backup operations for a database resource.
func (s *Service) ListDatabaseBackups(ctx context.Context, resource string) ([]domain.HostingDatabaseBackupOperation, error) {
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListDatabaseBackups(ctx, actor.ID, resource)
}

// RequestDatabaseBackupRestore admits restoration into a new private database resource.
func (s *Service) RequestDatabaseBackupRestore(ctx context.Context, backup string, in domain.HostingDatabaseBackupRestoreInput) (domain.HostingDatabaseBackupOperation, error) {
	var zero domain.HostingDatabaseBackupOperation
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackup)
	}
	original, err := s.GetDatabaseBackup(ctx, backup)
	if err != nil {
		return zero, err
	}
	_, actor, err := s.resourceForDestinationRole(ctx, original.SourceResourceID, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if in.ComponentTarget != nil {
		if _, _, err = s.resourceForDestinationRole(ctx, in.ComponentTarget.ResourceID, domain.RoleOwner); err != nil {
			return zero, err
		}
		id, e := prefixedID("dbr")
		if e != nil {
			return zero, e
		}
		event, e := s.auditEvent(ctx, actor.ID, "hosting.database_backup.restore_requested", id, nil)
		if e != nil {
			return zero, e
		}
		repo, e := s.databaseBackupRepository()
		if e != nil {
			return zero, e
		}
		return repo.RequestDatabaseBackupRestore(ctx, id, backup, actor.ID, in, domain.HostingDatabaseCreateInput{}, nil, event)
	}
	source, err := s.GetHostingDatabase(ctx, original.SourceResourceID)
	if err != nil {
		return zero, err
	}
	if source.Engine != "postgresql" || s.secretKeyring == nil {
		return zero, store.ErrHostingDatabaseUnavailable
	}
	create := domain.HostingDatabaseCreateInput{RequestID: domain.HostingDocumentHash(struct{ Backup, Request string }{backup, in.RequestID})[7:39], ProjectID: source.Resource.ProjectID, ProjectEnvironmentID: source.Resource.ProjectEnvironmentID, Name: in.Name, DestinationID: source.Binding.DestinationID, Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe}
	resource := domain.HostingDatabaseResourceID(create.ProjectID, create.RequestID)
	recipe, _ := domain.HostingDatabaseRecipeFor(create.Engine, create.RecipeVersion)
	records := make([]store.HostingDatabaseCredentialRecord, 0, len(recipe.Credentials))
	for _, ref := range recipe.Credentials {
		ref.SecretID, ref.Version = domain.HostingDatabaseSecretID(resource, ref.Role), 1
		var entropy [32]byte
		if _, err = rand.Read(entropy[:]); err != nil {
			return zero, errors.New("restore credential generation failed")
		}
		plain := []byte(base64.RawURLEncoding.EncodeToString(entropy[:]))
		envelope, e := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: create.ProjectID, SecretID: ref.SecretID, Version: 1}, plain)
		clear(plain)
		clear(entropy[:])
		if e != nil {
			return zero, errors.New("restore credential encryption failed")
		}
		records = append(records, store.HostingDatabaseCredentialRecord{Reference: ref, Record: store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: ref.SecretID, Version: 1, CreatedBy: actor.ID, CreatedAt: s.clock().UTC()}, Envelope: envelope}})
	}
	id, err := prefixedID("dbr")
	if err != nil {
		return zero, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.database.created", resource, nil)
	if err != nil {
		return zero, err
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return zero, err
	}
	return repo.RequestDatabaseBackupRestore(ctx, id, backup, actor.ID, in, create, records, event)
}

// DatabaseBackupComponents reads settled eligibility without opening a backup operation.
func (s *Service) DatabaseBackupComponents(ctx context.Context, resource string) (domain.HostingDatabaseBackupComponents, error) {
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.HostingDatabaseBackupComponents{}, err
	}
	repo, ok := s.hosting.(store.HostingDatabaseBackupComponentRepository)
	if !ok {
		return domain.HostingDatabaseBackupComponents{}, store.ErrHostingDatabaseUnavailable
	}
	return repo.DatabaseBackupComponents(ctx, actor.ID, resource)
}

// RunnerDatabaseBackupPlan returns a backup plan for the authenticated runner.
func (s *Service) RunnerDatabaseBackupPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error) {
	r, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupPlan{}, err
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return domain.HostingDatabaseBackupPlan{}, err
	}
	return repo.DatabaseBackupPlan(ctx, r.ID, a)
}

// RunnerDatabaseBackupMutation records a backup mutation from the authenticated runner.
func (s *Service) RunnerDatabaseBackupMutation(ctx context.Context, in domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error) {
	r, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingDatabaseBackupOperation{}, err
	}
	repo, err := s.databaseBackupRepository()
	if err != nil {
		return domain.HostingDatabaseBackupOperation{}, err
	}
	return repo.DatabaseBackupMutation(ctx, r.ID, in)
}
