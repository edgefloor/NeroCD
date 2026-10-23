package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func hostingDatabaseCompatible(ctx context.Context, q *sqlcgen.Queries) error {
	compatible, err := q.HostingDatabaseSchemaCompatible(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !compatible {
		return ErrHostingDatabaseUnavailable
	}
	return nil
}

func hostingDatabaseLoad(ctx context.Context, q *sqlcgen.Queries, resource string) (domain.HostingDatabase, error) {
	var view domain.HostingDatabase
	row, err := q.HostingDatabaseRead(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	r, err := q.GetResource(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	view.Resource, err = resourceFromHostingSQL(r)
	if err != nil {
		return view, err
	}
	binding, err := q.GetResourceBinding(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	view.Binding = bindingFromDestinationSQL(binding)
	saved, err := q.ReadHostingStorage(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	storage, err := hostingStorageFromSQL(saved)
	if err != nil {
		return view, err
	}
	view.StorageRef = storage.Reference()
	view.Engine, view.RecipeVersion, view.RuntimeState = row.Engine, row.RecipeVersion, row.RuntimeState
	recipe, err := domain.HostingDatabaseRecipeFor(row.Engine, row.RecipeVersion)
	if err != nil || row.RuntimeState != domain.HostingDatabaseUnqualified || view.Resource.Kind != "database" || view.Resource.Source.Kind != "image" || view.Resource.Source.Image.Reference != recipe.ImageReference || row.ImageReference != recipe.ImageReference || row.ProjectID != r.ProjectID || row.BindingID != binding.ID || row.DestinationID != binding.DestinationID || row.StorageID != storage.ID || storage.MountPath != recipe.MountPath || storage.BindingID != binding.ID {
		return domain.HostingDatabase{}, ErrConflict
	}
	refs, err := q.HostingDatabaseCredentials(ctx, resource)
	if err != nil {
		return view, fmt.Errorf("read database credential references: %w", err)
	}
	if len(refs) != len(recipe.Credentials) {
		return domain.HostingDatabase{}, ErrConflict
	}
	for i, ref := range refs {
		identity := recipe.Credentials[i]
		if ref.Role != identity.Role || ref.Username != identity.Username || ref.SecretID != domain.HostingDatabaseSecretID(resource, ref.Role) || ref.Version != 1 {
			return domain.HostingDatabase{}, ErrConflict
		}
		view.Credentials = append(view.Credentials, domain.HostingDatabaseCredentialRef{Role: ref.Role, Username: ref.Username, SecretID: ref.SecretID, Version: ref.Version})
	}
	return view, nil
}

// CreateHostingDatabase commits connected desired metadata and encrypted
// credentials. It cannot queue deployment or claim engine qualification.
func (s *PostgresStore) CreateHostingDatabase(ctx context.Context, actor string, in domain.HostingDatabaseCreateInput, credentials []HostingDatabaseCredentialRecord, event domain.AuditEvent) (domain.HostingDatabase, error) {
	var zero domain.HostingDatabase
	if in.Validate() != nil || actor == "" {
		return zero, ErrConflict
	}
	recipe, err := domain.HostingDatabaseRecipeFor(in.Engine, in.RecipeVersion)
	if err != nil {
		return zero, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin database creation: %w", err)
	}
	defer storageDeploymentRollback(ctx, tx)
	q := sqlcgen.New(tx)
	if err = hostingDatabaseCompatible(ctx, q); err != nil {
		return zero, err
	}
	if _, err = q.LockHostingDestination(ctx, in.DestinationID); err != nil {
		return zero, hostingError(err)
	}
	// proxyAuthorize locks the active actor and project. The project lock also
	// serializes concurrent project-scoped request IDs without a separate bus.
	if err = proxyAuthorize(ctx, tx, actor, in.ProjectID, domain.RoleOwner, false); err != nil {
		return zero, err
	}
	if _, err = q.LockHostingStorageGrant(ctx, sqlcgen.LockHostingStorageGrantParams{DestinationID: in.DestinationID, ProjectID: in.ProjectID}); errors.Is(err, pgx.ErrNoRows) {
		return zero, auth.ErrForbidden
	} else if err != nil {
		return zero, fmt.Errorf("lock database destination grant: %w", err)
	}
	environment, err := q.GetProjectEnvironment(ctx, in.ProjectEnvironmentID)
	if err != nil {
		return zero, hostingError(err)
	}
	if environment.ProjectID != in.ProjectID {
		return zero, auth.ErrForbidden
	}
	requestHash := domain.HostingDocumentHash(struct {
		Actor string
		Input domain.HostingDatabaseCreateInput
	}{actor, in})
	receipt, err := q.HostingDatabaseRequest(ctx, sqlcgen.HostingDatabaseRequestParams{ProjectID: in.ProjectID, RequestID: in.RequestID})
	if err == nil {
		var accepted domain.HostingDatabaseCreateInput
		if receipt.ActorID != actor || receipt.RequestHash != requestHash || json.Unmarshal(receipt.AcceptedRequest, &accepted) != nil || accepted != in {
			return zero, ErrConflict
		}
		view, readErr := hostingDatabaseLoad(ctx, q, receipt.ResourceID)
		if readErr != nil {
			return zero, readErr
		}
		return view, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("read database creation receipt: %w", err)
	}
	resource := domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID)
	if len(credentials) != len(recipe.Credentials) {
		return zero, ErrConflict
	}
	for i, credential := range credentials {
		identity := recipe.Credentials[i]
		ref := credential.Reference
		record := credential.Record
		if ref.Role != identity.Role || ref.Username != identity.Username || ref.SecretID != domain.HostingDatabaseSecretID(resource, ref.Role) || ref.Version != 1 || !validManagedRecord(record) || record.Metadata.SecretID != ref.SecretID || record.Metadata.Version != 1 || record.Metadata.CreatedBy != actor || record.Metadata.CreatedAt.IsZero() {
			return zero, ErrConflict
		}
	}
	now := time.Now().UTC()
	source, err := json.Marshal(domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: recipe.ImageReference}})
	if err != nil {
		return zero, fmt.Errorf("encode database image source: %w", err)
	}
	if _, err = q.CreateResource(ctx, sqlcgen.CreateResourceParams{ID: resource, ProjectID: in.ProjectID, ProjectEnvironmentID: in.ProjectEnvironmentID, Name: in.Name, Kind: "database", Source: source, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}); err != nil {
		return zero, hostingError(err)
	}
	bindingID := "binding_db_" + resource[len("res_db_"):]
	namespace, err := newLeaseToken("ncd")
	if err != nil {
		return zero, fmt.Errorf("generate database namespace: %w", err)
	}
	if _, err = q.CreateResourceBinding(ctx, sqlcgen.CreateResourceBindingParams{ID: bindingID, ResourceID: resource, DestinationID: in.DestinationID, RuntimeNamespace: namespace, CreatedBy: actor, CreatedAt: now}); err != nil {
		return zero, hostingError(err)
	}
	for _, audit := range []domain.AuditEvent{
		{ID: "aud_database_resource_" + resource, ActorID: actor, Action: "resource.created", TargetID: resource, CreatedAt: now, Metadata: map[string]any{"project_id": in.ProjectID, "project_environment_id": in.ProjectEnvironmentID, "generation": int64(1)}},
		{ID: "aud_database_binding_" + resource, ActorID: actor, Action: "resource.destination_bound", TargetID: resource, CreatedAt: now, Metadata: map[string]any{"project_id": in.ProjectID, "destination_id": in.DestinationID, "binding_id": bindingID}},
	} {
		if err = createAuditWithQueries(ctx, q, audit); err != nil {
			return zero, fmt.Errorf("audit database resource placement: %w", err)
		}
	}
	storageID, volume := domain.HostingStorageIdentity(bindingID)
	storage := domain.HostingStorage{ID: storageID, ProjectID: in.ProjectID, ResourceID: resource, BindingID: bindingID, DestinationID: in.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: recipe.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: actor, CreatedAt: now}
	storage.Hash = storage.DocumentHash()
	if storage.Validate() != nil {
		return zero, ErrConflict
	}
	if _, err = q.InsertHostingStorage(ctx, sqlcgen.InsertHostingStorageParams{ID: storage.ID, ProjectID: storage.ProjectID, ResourceID: resource, BindingID: bindingID, DestinationID: in.DestinationID, Kind: storage.Kind, Driver: storage.Driver, VolumeName: storage.VolumeName, MountPath: storage.MountPath, AccessMode: storage.AccessMode, Retention: storage.Retention, BodyHash: storage.Hash, CreatedBy: actor}); err != nil {
		return zero, hostingError(err)
	}
	if err = q.HostingDatabaseInsert(ctx, sqlcgen.HostingDatabaseInsertParams{ResourceID: resource, ProjectID: in.ProjectID, BindingID: bindingID, DestinationID: in.DestinationID, StorageID: storage.ID, Engine: in.Engine, RecipeVersion: in.RecipeVersion, ImageReference: recipe.ImageReference, CreatedBy: actor}); err != nil {
		return zero, hostingError(err)
	}
	for _, credential := range credentials {
		ref, record := credential.Reference, credential.Record
		row, createErr := q.CreateManagedSecret(ctx, sqlcgen.CreateManagedSecretParams{ID: ref.SecretID, ProjectID: in.ProjectID, Name: "database_" + resource + "_" + ref.Role, CreatedBy: actor, CreatedAt: record.Metadata.CreatedAt})
		if createErr != nil {
			return zero, hostingError(createErr)
		}
		if err = appendManagedRecord(ctx, q, record); err != nil {
			return zero, fmt.Errorf("append database credential version: %w", err)
		}
		audit := domain.AuditEvent{ID: "aud_database_secret_" + ref.SecretID, ActorID: actor, CreatedAt: now}
		if err = commitManagedAudit(ctx, q, audit, "managed_secret.created", managedSecretFromSQL(row), 1); err != nil {
			return zero, fmt.Errorf("audit database credential creation: %w", err)
		}
		if err = q.HostingDatabaseCredentialInsert(ctx, sqlcgen.HostingDatabaseCredentialInsertParams{ResourceID: resource, Role: ref.Role, Username: ref.Username, SecretID: ref.SecretID}); err != nil {
			return zero, hostingError(err)
		}
	}
	storageAudit := domain.AuditEvent{ID: domain.HostingStorageAuditID(resource, in.RequestID), ActorID: actor, Action: "hosting.storage.created", TargetID: storage.ID, CreatedAt: now, Metadata: map[string]any{"project_id": in.ProjectID, "resource_id": resource, "binding_id": bindingID, "destination_id": in.DestinationID, "storage_generation": int64(1), "storage_hash": storage.Hash}}
	if err = createAuditWithQueries(ctx, q, storageAudit); err != nil {
		return zero, fmt.Errorf("audit database retained storage: %w", err)
	}
	if err = q.InsertHostingStorageReceipt(ctx, sqlcgen.InsertHostingStorageReceiptParams{ResourceID: resource, RequestID: in.RequestID, StorageID: storage.ID, ActorID: actor, RequestHash: requestHash, ExpectedResourceGeneration: 1, AuditID: storageAudit.ID}); err != nil {
		return zero, hostingError(err)
	}
	event.ID, event.ActorID, event.Action, event.TargetID, event.CreatedAt = "aud_database_"+resource, actor, "hosting.database.created", resource, now
	event.Metadata = map[string]any{"project_id": in.ProjectID, "project_environment_id": in.ProjectEnvironmentID, "binding_id": bindingID, "destination_id": in.DestinationID, "storage_id": storage.ID, "engine": in.Engine, "recipe_version": in.RecipeVersion}
	if err = createAuditWithQueries(ctx, q, event); err != nil {
		return zero, fmt.Errorf("audit database creation: %w", err)
	}
	accepted, err := json.Marshal(in)
	if err != nil {
		return zero, fmt.Errorf("encode accepted database request: %w", err)
	}
	if err = q.HostingDatabaseReceiptInsert(ctx, sqlcgen.HostingDatabaseReceiptInsertParams{ProjectID: in.ProjectID, RequestID: in.RequestID, ResourceID: resource, ActorID: actor, RequestHash: requestHash, AcceptedRequest: accepted, AuditID: event.ID}); err != nil {
		return zero, hostingError(err)
	}
	view, err := hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return zero, err
	}
	if err = proxyCommitMutation(ctx, tx, actor); err != nil {
		return zero, hostingError(err)
	}
	return view, nil
}

// GetHostingDatabase returns immutable metadata under current viewer and grant authority.
func (s *PostgresStore) GetHostingDatabase(ctx context.Context, actor, resource string) (domain.HostingDatabase, error) {
	var zero domain.HostingDatabase
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin database metadata read: %w", err)
	}
	defer storageDeploymentRollback(ctx, tx)
	q := sqlcgen.New(tx)
	if err = hostingDatabaseCompatible(ctx, q); err != nil {
		return zero, err
	}
	row, err := q.HostingDatabaseRead(ctx, resource)
	if err != nil {
		return zero, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, row.ProjectID, domain.RoleViewer, false); err != nil {
		return zero, err
	}
	if _, err = q.LockHostingStorageGrant(ctx, sqlcgen.LockHostingStorageGrantParams{DestinationID: row.DestinationID, ProjectID: row.ProjectID}); errors.Is(err, pgx.ErrNoRows) {
		return zero, auth.ErrForbidden
	} else if err != nil {
		return zero, fmt.Errorf("read database destination grant: %w", err)
	}
	view, err := hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return zero, err
	}
	return view, proxyCheckLegacyToken(ctx, tx, actor)
}
