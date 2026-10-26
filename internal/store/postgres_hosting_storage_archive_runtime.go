package store

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// archivePinnedRuntime reads immutable public metadata only. The resource and
// destination are already locked and authorized by restore admission.
func archivePinnedRuntime(ctx context.Context, q *sqlcgen.Queries, p domain.HostingStorageArchivePlan) (domain.HostingRuntimeDocument, []domain.HostingRuntimeSecretReference, error) {
	var zero domain.HostingRuntimeDocument
	snap := p.SourceHealthy.Snapshot
	if snap.RuntimeConfiguration == (domain.HostingRuntimeConfigurationRef{}) {
		return zero, nil, nil
	}
	if snap.PolicyVersion != domain.HostingOwnedStorageRuntimePolicyVersion || domain.ValidateHostingRuntimeConfigurationRef(snap) != nil {
		return zero, nil, ErrConflict
	}
	row, err := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: snap.ResourceID, Generation: snap.RuntimeConfiguration.Generation})
	if err != nil {
		return zero, nil, hostingError(err)
	}
	var doc domain.HostingRuntimeDocument
	decoder := json.NewDecoder(bytes.NewReader(row.Body))
	decoder.DisallowUnknownFields()
	if len(row.Body) > domain.HostingRuntimeDocumentLimit || row.ProjectID != p.ProjectID || row.ResourceID != snap.ResourceID || row.Generation != snap.RuntimeConfiguration.Generation || row.BodyHash != snap.RuntimeConfiguration.Hash || decoder.Decode(&doc) != nil {
		return zero, nil, ErrConflict
	}
	canonical, refs, err := domain.CanonicalHostingRuntimeDocument(doc.Entries)
	if err != nil || domain.HostingDocumentHash(doc) != row.BodyHash || domain.HostingDocumentHash(canonical) != row.BodyHash {
		return zero, nil, ErrConflict
	}
	if err := validateRuntimeReferences(ctx, q, p.ProjectID, refs); err != nil {
		return zero, nil, err
	}
	return canonical, refs, nil
}

// archiveCreateRuntime appends the target's first desired document inside the
// restore transaction. It copies references and public literals, never values
// from managed-secret storage or catalog installation identity.
func archiveCreateRuntime(ctx context.Context, q *sqlcgen.Queries, resource, project, actor, request string, doc domain.HostingRuntimeDocument, refs []domain.HostingRuntimeSecretReference, now time.Time) error {
	if err := q.CreateHostingRuntimeConfiguration(ctx, sqlcgen.CreateHostingRuntimeConfigurationParams{ResourceID: resource, ProjectID: project}); err != nil {
		return err
	}
	if _, err := q.InsertHostingRuntimeVersion(ctx, sqlcgen.InsertHostingRuntimeVersionParams{ResourceID: resource, ProjectID: project, Generation: 1, Body: buildJSON(doc), BodyHash: domain.HostingDocumentHash(doc), CreatedBy: actor}); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := q.InsertHostingRuntimeSecretReference(ctx, sqlcgen.InsertHostingRuntimeSecretReferenceParams{ResourceID: resource, ProjectID: project, Generation: 1, SecretID: ref.SecretID, SecretVersion: ref.Version}); err != nil {
			return err
		}
	}
	event := domain.AuditEvent{ID: domain.HostingRuntimeAuditID(resource, request), ActorID: actor, Action: "hosting.runtime_configuration.updated", TargetID: resource, CreatedAt: now, Metadata: map[string]any{"project_id": project, "resource_id": resource, "runtime_generation": int64(1), "entry_count": len(doc.Entries), "secret_reference_count": len(refs)}}
	if err := createAuditWithQueries(ctx, q, event); err != nil {
		return err
	}
	hash := domain.HostingDocumentHash(struct {
		Resource, Actor                       string
		ResourceGeneration, RuntimeGeneration int64
		Document                              domain.HostingRuntimeDocument
	}{resource, actor, 1, 0, doc})
	return q.InsertHostingRuntimeReceipt(ctx, sqlcgen.InsertHostingRuntimeReceiptParams{ResourceID: resource, ProjectID: project, RequestID: request, ActorID: actor, RequestHash: hash, ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, RuntimeGeneration: 1, AuditID: event.ID})
}
