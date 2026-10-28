package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func runtimeResourceSupported(kind string, source json.RawMessage) bool {
	var src struct {
		Kind string `json:"kind"`
	}
	return kind == "application" && json.Unmarshal(source, &src) == nil && (src.Kind == "image" || src.Kind == "git" || src.Kind == "compose" || src.Kind == "dockerfile")
}

func runtimeVersion(row sqlcgen.HostingRuntimeConfigurationVersion, resourceGeneration int64) (domain.HostingRuntimeConfiguration, error) {
	var doc domain.HostingRuntimeDocument
	if json.Unmarshal(row.Body, &doc) != nil {
		return domain.HostingRuntimeConfiguration{}, ErrConflict
	}
	canonical, _, err := domain.CanonicalHostingRuntimeDocument(doc.Entries)
	if err != nil || domain.HostingDocumentHash(canonical) != row.BodyHash {
		return domain.HostingRuntimeConfiguration{}, ErrConflict
	}
	return domain.HostingRuntimeConfiguration{ResourceID: row.ResourceID, ProjectID: row.ProjectID, ResourceGeneration: resourceGeneration, RuntimeGeneration: row.Generation, Entries: canonical.Entries, CreatedBy: row.CreatedBy, CreatedAt: &row.CreatedAt}, nil
}

// GetHostingRuntimeConfiguration reads saved desired metadata under current viewer authority.
// Revocation does not erase a previously saved reference or expand its value.
func (s *PostgresStore) GetHostingRuntimeConfiguration(ctx context.Context, actor, resource string) (domain.HostingRuntimeConfiguration, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeConfiguration, error) {
		var zero domain.HostingRuntimeConfiguration
		q := sqlcgen.New(tx)
		r, err := q.ReadHostingRuntimeResource(ctx, resource)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer"); err != nil {
			return zero, err
		}
		if !runtimeResourceSupported(r.Kind, r.Source) {
			return zero, ErrConflict
		}
		generation, err := q.ReadHostingRuntimeGeneration(ctx, resource)
		var result domain.HostingRuntimeConfiguration
		if errors.Is(err, pgx.ErrNoRows) {
			result = domain.HostingRuntimeConfiguration{ResourceID: r.ID, ProjectID: r.ProjectID, ResourceGeneration: r.Generation, Entries: []domain.HostingRuntimeEntry{}}
		} else if err != nil {
			return zero, err
		} else {
			row, e := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: resource, Generation: generation})
			if e != nil {
				return zero, e
			}
			result, err = runtimeVersion(row, r.Generation)
			if err != nil {
				return zero, err
			}
		}
		return result, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer")
	})
}

func validateRuntimeReferences(ctx context.Context, q *sqlcgen.Queries, project string, refs []domain.HostingRuntimeSecretReference) error {
	for _, ref := range refs {
		if _, err := q.ValidateHostingRuntimeSecretReference(ctx, sqlcgen.ValidateHostingRuntimeSecretReferenceParams{ID: ref.SecretID, ProjectID: project, Version: ref.Version}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
	}
	return nil
}

// PutHostingRuntimeConfiguration appends one immutable desired document and audit
// atomically. It never changes resource/source generations or schedules execution.
func (s *PostgresStore) PutHostingRuntimeConfiguration(ctx context.Context, actor, resource string, in domain.HostingRuntimeConfigurationInput, event domain.AuditEvent) (domain.HostingRuntimeConfiguration, error) {
	doc, refs, err := in.Validate()
	if err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	requestHash := domain.HostingDocumentHash(struct {
		Resource, Actor                       string
		ResourceGeneration, RuntimeGeneration int64
		Document                              domain.HostingRuntimeDocument
	}{resource, actor, in.ExpectedResourceGeneration, in.ExpectedRuntimeGeneration, doc})
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeConfiguration, error) {
		var zero domain.HostingRuntimeConfiguration
		q := sqlcgen.New(tx)
		r, err := q.LockHostingRuntimeResource(ctx, resource)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner"); err != nil {
			return zero, err
		}
		if !runtimeResourceSupported(r.Kind, r.Source) || r.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		generation, err := q.LockHostingRuntimeConfiguration(ctx, resource)
		if errors.Is(err, pgx.ErrNoRows) {
			generation = 0
		} else if err != nil {
			return zero, err
		}
		receipt, err := q.GetHostingRuntimeReceipt(ctx, sqlcgen.GetHostingRuntimeReceiptParams{ResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if receipt.ActorID != actor || receipt.RequestHash != requestHash {
				return zero, ErrConflict
			}
			if err = validateRuntimeReferences(ctx, q, r.ProjectID, refs); err != nil {
				return zero, err
			}
			row, e := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: resource, Generation: receipt.RuntimeGeneration})
			if e != nil {
				return zero, e
			}
			result, e := runtimeVersion(row, r.Generation)
			if e != nil {
				return zero, e
			}
			return result, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if err = databaseBindingPendingGate(ctx, tx, resource); err != nil {
			return zero, err
		}
		if generation != in.ExpectedRuntimeGeneration {
			return zero, ErrConflict
		}
		if err = validateRuntimeReferences(ctx, q, r.ProjectID, refs); err != nil {
			return zero, err
		}
		if generation == 0 {
			if err = q.CreateHostingRuntimeConfiguration(ctx, sqlcgen.CreateHostingRuntimeConfigurationParams{ResourceID: resource, ProjectID: r.ProjectID}); err != nil {
				return zero, err
			}
		}
		next := generation + 1
		row, err := q.InsertHostingRuntimeVersion(ctx, sqlcgen.InsertHostingRuntimeVersionParams{ResourceID: resource, ProjectID: r.ProjectID, Generation: next, Body: buildJSON(doc), BodyHash: domain.HostingDocumentHash(doc), CreatedBy: actor})
		if err != nil {
			return zero, err
		}
		for _, ref := range refs {
			if err = q.InsertHostingRuntimeSecretReference(ctx, sqlcgen.InsertHostingRuntimeSecretReferenceParams{ResourceID: resource, ProjectID: r.ProjectID, Generation: next, SecretID: ref.SecretID, SecretVersion: ref.Version}); err != nil {
				return zero, err
			}
		}
		if generation > 0 {
			if err = casBuild(q.AdvanceHostingRuntimeConfiguration(ctx, sqlcgen.AdvanceHostingRuntimeConfigurationParams{ResourceID: resource, Generation: generation})); err != nil {
				return zero, err
			}
		}
		event.ID = domain.HostingRuntimeAuditID(resource, in.RequestID)
		event.ActorID = actor
		event.Action = "hosting.runtime_configuration.updated"
		event.TargetID = resource
		event.Metadata = map[string]any{"project_id": r.ProjectID, "resource_id": resource, "runtime_generation": next, "entry_count": len(doc.Entries), "secret_reference_count": len(refs)}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertHostingRuntimeReceipt(ctx, sqlcgen.InsertHostingRuntimeReceiptParams{ResourceID: resource, ProjectID: r.ProjectID, RequestID: in.RequestID, ActorID: actor, RequestHash: requestHash, ExpectedResourceGeneration: in.ExpectedResourceGeneration, ExpectedRuntimeGeneration: in.ExpectedRuntimeGeneration, RuntimeGeneration: next, AuditID: event.ID}); err != nil {
			return zero, err
		}
		result, err := runtimeVersion(row, r.Generation)
		if err != nil {
			return zero, err
		}
		return result, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner")
	})
}
