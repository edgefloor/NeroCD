package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func catalogLoad(ctx context.Context, tx pgx.Tx, project, request, resource string) (domain.HostingCatalogInstallation, error) {
	var v domain.HostingCatalogInstallation
	row, err := sqlcgen.New(tx).ReadHostingCatalogInstallation(ctx, sqlcgen.ReadHostingCatalogInstallationParams{ProjectID: project, RequestID: request, ResourceID: resource})
	body, hash := row.Body, row.BodyHash
	if err != nil {
		return v, hostingError(err)
	}
	if json.Unmarshal(body, &v) != nil || domain.HostingDocumentHash(v) != hash || v.Resource.ID != domain.HostingCatalogResourceID(v.Request.ProjectID, v.Request.RequestID) {
		return domain.HostingCatalogInstallation{}, ErrConflict
	}
	return v, nil
}

// InstallHostingCatalog commits the ordinary resource, placement, retained volume,
// encrypted values, runtime references and immutable template copy atomically.
func (s *PostgresStore) InstallHostingCatalog(ctx context.Context, actor string, in domain.HostingCatalogInstallInput, credentials []HostingCatalogCredentialRecord, event domain.AuditEvent) (domain.HostingCatalogInstallation, error) {
	var zero domain.HostingCatalogInstallation
	if in.ValidateShape() != nil {
		return zero, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer storageDeploymentRollback(ctx, tx)
	q := sqlcgen.New(tx)
	if _, err = q.LockHostingDestination(ctx, in.DestinationID); err != nil {
		return zero, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, in.ProjectID, domain.RoleOwner, false); err != nil {
		return zero, err
	}
	if _, err = q.LockHostingStorageGrant(ctx, sqlcgen.LockHostingStorageGrantParams{DestinationID: in.DestinationID, ProjectID: in.ProjectID}); errors.Is(err, pgx.ErrNoRows) {
		return zero, auth.ErrForbidden
	} else if err != nil {
		return zero, err
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
		Input domain.HostingCatalogInstallInput
	}{actor, in})
	receipt, err := q.ReadHostingCatalogInstallReceipt(ctx, sqlcgen.ReadHostingCatalogInstallReceiptParams{ProjectID: in.ProjectID, RequestID: in.RequestID})
	storedHash, storedActor := receipt.RequestHash, receipt.ActorID
	if err == nil {
		if storedHash != requestHash || storedActor != actor {
			return zero, ErrConflict
		}
		view, e := catalogLoad(ctx, tx, in.ProjectID, in.RequestID, "")
		if e != nil {
			return zero, e
		}
		_, refs, e := domain.CanonicalHostingRuntimeDocument(view.Credentials)
		if e != nil {
			return zero, ErrConflict
		}
		if e = validateRuntimeReferences(ctx, q, in.ProjectID, refs); e != nil {
			return zero, e
		}
		if e = proxyCommitMutation(ctx, tx, actor); e != nil {
			return zero, e
		}
		return view, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	template := domain.HostingCatalogTemplates()[0]
	resource := domain.HostingCatalogResourceID(in.ProjectID, in.RequestID)
	binding := "binding_catalog_" + resource[len("res_catalog_"):]
	now := time.Now().UTC()
	if len(credentials) != len(template.Definition.CredentialNames) {
		return zero, ErrConflict
	}
	for i, c := range credentials {
		m := c.Record.Metadata
		if c.Name != template.Definition.CredentialNames[i] || !validManagedRecord(c.Record) || m.SecretID != domain.HostingCatalogSecretID(resource, c.Name) || m.Version != 1 || m.CreatedBy != actor || m.CreatedAt.IsZero() {
			return zero, ErrConflict
		}
	}
	row, err := q.CreateResource(ctx, sqlcgen.CreateResourceParams{ID: resource, ProjectID: in.ProjectID, ProjectEnvironmentID: in.ProjectEnvironmentID, Name: in.Name, Kind: "application", Source: buildJSON(domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: template.Definition.ImageReference}}), CreatedBy: actor, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return zero, hostingError(err)
	}
	namespace, err := newLeaseToken("ncd")
	if err != nil {
		return zero, err
	}
	bound, err := q.CreateResourceBinding(ctx, sqlcgen.CreateResourceBindingParams{ID: binding, ResourceID: resource, DestinationID: in.DestinationID, RuntimeNamespace: namespace, CreatedBy: actor, CreatedAt: now})
	if err != nil {
		return zero, hostingError(err)
	}
	storageID, volume := domain.HostingStorageIdentity(binding)
	storage := domain.HostingStorage{ID: storageID, ProjectID: in.ProjectID, ResourceID: resource, BindingID: binding, DestinationID: in.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: template.Definition.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: actor, CreatedAt: now}
	storage.Hash = storage.DocumentHash()
	if _, err = q.InsertHostingStorage(ctx, sqlcgen.InsertHostingStorageParams{ID: storage.ID, ProjectID: in.ProjectID, ResourceID: resource, BindingID: binding, DestinationID: in.DestinationID, Kind: storage.Kind, Driver: storage.Driver, VolumeName: volume, MountPath: storage.MountPath, AccessMode: storage.AccessMode, Retention: storage.Retention, BodyHash: storage.Hash, CreatedBy: actor}); err != nil {
		return zero, hostingError(err)
	}
	entries := make([]domain.HostingRuntimeEntry, 0, len(credentials))
	for _, c := range credentials {
		m := c.Record.Metadata
		secret, err := q.CreateManagedSecret(ctx, sqlcgen.CreateManagedSecretParams{ID: m.SecretID, ProjectID: in.ProjectID, Name: "catalog_" + resource + "_" + c.Name, CreatedBy: actor, CreatedAt: m.CreatedAt})
		if err != nil {
			return zero, hostingError(err)
		}
		if err = appendManagedRecord(ctx, q, c.Record); err != nil {
			return zero, err
		}
		if err = commitManagedAudit(ctx, q, domain.AuditEvent{ID: "aud_catalog_secret_" + m.SecretID, ActorID: actor, CreatedAt: now}, "managed_secret.created", managedSecretFromSQL(secret), 1); err != nil {
			return zero, err
		}
		entries = append(entries, domain.HostingRuntimeEntry{Name: c.Name, Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: m.SecretID, Version: 1}})
	}
	doc, refs, err := domain.CanonicalHostingRuntimeDocument(entries)
	if err != nil {
		return zero, err
	}
	if err = q.CreateHostingRuntimeConfiguration(ctx, sqlcgen.CreateHostingRuntimeConfigurationParams{ResourceID: resource, ProjectID: in.ProjectID}); err != nil {
		return zero, err
	}
	if _, err = q.InsertHostingRuntimeVersion(ctx, sqlcgen.InsertHostingRuntimeVersionParams{ResourceID: resource, ProjectID: in.ProjectID, Generation: 1, Body: buildJSON(doc), BodyHash: domain.HostingDocumentHash(doc), CreatedBy: actor}); err != nil {
		return zero, err
	}
	for _, ref := range refs {
		if err = q.InsertHostingRuntimeSecretReference(ctx, sqlcgen.InsertHostingRuntimeSecretReferenceParams{ResourceID: resource, ProjectID: in.ProjectID, Generation: 1, SecretID: ref.SecretID, SecretVersion: 1}); err != nil {
			return zero, err
		}
	}
	r, err := resourceFromHostingSQL(row)
	if err != nil {
		return zero, err
	}
	view := domain.HostingCatalogInstallation{ID: resource, Request: in, Template: template, Resource: r, Binding: bindingFromDestinationSQL(bound), StorageRef: storage.Reference(), RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: domain.HostingDocumentHash(doc)}, Credentials: doc.Entries, CreatedBy: actor, CreatedAt: now}
	storageAudit := domain.AuditEvent{ID: domain.HostingStorageAuditID(resource, in.RequestID), ActorID: actor, Action: "hosting.storage.created", TargetID: storage.ID, CreatedAt: now, Metadata: map[string]any{"project_id": in.ProjectID, "resource_id": resource, "binding_id": binding, "destination_id": in.DestinationID, "storage_generation": int64(1), "storage_hash": storage.Hash}}
	if err = createAuditWithQueries(ctx, q, storageAudit); err != nil {
		return zero, err
	}
	if err = q.InsertHostingStorageReceipt(ctx, sqlcgen.InsertHostingStorageReceiptParams{ResourceID: resource, RequestID: in.RequestID, StorageID: storage.ID, ActorID: actor, RequestHash: requestHash, ExpectedResourceGeneration: 1, AuditID: storageAudit.ID}); err != nil {
		return zero, err
	}
	runtimeAudit := domain.AuditEvent{ID: domain.HostingRuntimeAuditID(resource, in.RequestID), ActorID: actor, Action: "hosting.runtime_configuration.updated", TargetID: resource, CreatedAt: now, Metadata: map[string]any{"project_id": in.ProjectID, "resource_id": resource, "runtime_generation": int64(1), "entry_count": len(doc.Entries), "secret_reference_count": len(refs)}}
	if err = createAuditWithQueries(ctx, q, runtimeAudit); err != nil {
		return zero, err
	}
	runtimeInput := domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, RequestID: in.RequestID, Entries: doc.Entries}
	runtimeHash := domain.HostingDocumentHash(struct {
		Resource, Actor                       string
		ResourceGeneration, RuntimeGeneration int64
		Document                              domain.HostingRuntimeDocument
	}{resource, actor, 1, 0, doc})
	if err = q.InsertHostingRuntimeReceipt(ctx, sqlcgen.InsertHostingRuntimeReceiptParams{ResourceID: resource, ProjectID: in.ProjectID, RequestID: runtimeInput.RequestID, ActorID: actor, RequestHash: runtimeHash, ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, RuntimeGeneration: 1, AuditID: runtimeAudit.ID}); err != nil {
		return zero, err
	}
	event.ID, event.ActorID, event.Action, event.TargetID, event.CreatedAt = "aud_catalog_"+resource, actor, "hosting.catalog.installed", resource, now
	event.Metadata = map[string]any{"project_id": in.ProjectID, "resource_id": resource, "template_id": template.ID, "immutable_version": template.Version, "original_hash": template.OriginalHash, "definition_hash": template.DefinitionHash, "storage_id": storage.ID, "runtime_generation": 1}
	if err = createAuditWithQueries(ctx, q, event); err != nil {
		return zero, err
	}
	if err = q.InsertHostingCatalogInstallation(ctx, sqlcgen.InsertHostingCatalogInstallationParams{ProjectID: in.ProjectID, RequestID: in.RequestID, ResourceID: resource, ActorID: actor, RequestHash: requestHash, Body: buildJSON(view), BodyHash: domain.HostingDocumentHash(view)}); err != nil {
		return zero, hostingError(err)
	}
	if err = proxyCommitMutation(ctx, tx, actor); err != nil {
		return zero, err
	}
	return view, nil
}

func (s *PostgresStore) GetHostingCatalogInstallation(ctx context.Context, actor, resource string) (domain.HostingCatalogInstallation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingCatalogInstallation, error) {
		var zero domain.HostingCatalogInstallation
		r, b, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleViewer)
		if err != nil {
			return zero, err
		}
		v, err := catalogLoad(ctx, tx, "", "", resource)
		if err != nil {
			return zero, err
		}
		if v.Resource.ProjectID != r.ProjectID || !catalogBindingEqual(v.Binding, b) {
			return zero, ErrConflict
		}
		return v, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

func catalogBindingEqual(left, right domain.ResourceBinding) bool {
	// JSON restores UTC timestamps with time.UTC, while pgx can return the
	// same instant with time.Local. Compare the instant before aligning its
	// representation; retain exact equality for every other binding field.
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return false
	}
	left.CreatedAt = right.CreatedAt
	return left == right
}
