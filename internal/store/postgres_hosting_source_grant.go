package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func publicSourceGrant(g domain.HostingSourceGrant, generation int64) domain.PublicHostingSourceGrant {
	return domain.PublicHostingSourceGrant{ID: g.ID, Version: g.Version, SourceID: g.SourceID, SourceGeneration: generation, ResourceID: g.ResourceID, Kind: g.Kind, Revoked: g.Revoked}
}

// RequestHostingSourceGrant serializes exact request replay with current owner authority.
func (s *PostgresStore) RequestHostingSourceGrant(ctx context.Context, actor, resource string, in domain.HostingSourceGrantRequest, event domain.AuditEvent) (domain.PublicHostingSourceGrant, error) {
	var zero domain.PublicHostingSourceGrant
	if !domain.ValidHostingSourceDeliveryID(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.ExpectedSourceGeneration < 1 || in.SecretID == "" || len(in.SecretID) > 128 || in.SecretVersion < 1 {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.PublicHostingSourceGrant, error) {
		q := sqlcgen.New(tx)
		r, err := q.LockHostingBuildResource(ctx, resource)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner"); err != nil {
			return zero, err
		}
		src, err := readBuildSource(ctx, tx, resource)
		if err != nil {
			return zero, err
		}
		if r.Generation != in.ExpectedResourceGeneration || src.Generation != in.ExpectedSourceGeneration || (src.CredentialKind != "https_basic" && src.CredentialKind != "ssh_private_key") {
			return zero, ErrConflict
		}
		if _, err = q.LockHostingBuildSecretVersion(ctx, sqlcgen.LockHostingBuildSecretVersionParams{ID: in.SecretID, ProjectID: r.ProjectID, Version: in.SecretVersion}); err != nil {
			return zero, ErrConflict
		}
		id := domain.HostingSourceGrantID(resource, in.RequestID)
		hash := domain.HostingDocumentHash(in)
		old, err := q.GetHostingSourceGrant(ctx, id)
		if err == nil {
			audit, e := q.GetHostingSourceDeliveryAudit(ctx, "aud_"+id)
			var metadata struct {
				RequestHash      string `json:"request_hash"`
				SourceGeneration int64  `json:"source_generation"`
			}
			if e != nil || json.Unmarshal(audit.Metadata, &metadata) != nil || audit.Action != "hosting.source_grant.created" || audit.TargetID != id || audit.ActorID != actor || metadata.RequestHash != hash || metadata.SourceGeneration != src.Generation {
				return zero, ErrConflict
			}
			g := grantDomain(old)
			if g.Revoked || g.ResourceID != resource || g.ProjectID != r.ProjectID || g.SourceID != src.ID || g.RepositoryURL != src.RepositoryURL || g.PolicyHash != domain.HostingDocumentHash(src.Policy) || g.SecretID != in.SecretID || g.SecretVersion != in.SecretVersion || g.Kind != src.CredentialKind {
				return zero, ErrConflict
			}
			return publicSourceGrant(g, src.Generation), authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		g := domain.HostingSourceGrant{ID: id, Version: 1, SourceID: src.ID, ResourceID: resource, ProjectID: r.ProjectID, RepositoryURL: src.RepositoryURL, PolicyHash: domain.HostingDocumentHash(src.Policy), SecretID: in.SecretID, SecretVersion: in.SecretVersion, Kind: src.CredentialKind, CreatedBy: actor}
		if err = q.CreateHostingSourceGrant(ctx, sqlcgen.CreateHostingSourceGrantParams{ID: id, SourceID: g.SourceID, ResourceID: resource, ProjectID: g.ProjectID, SecretID: g.SecretID, SecretVersion: g.SecretVersion, Kind: g.Kind, RepositoryUrl: g.RepositoryURL, PolicyHash: g.PolicyHash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		event.ID = "aud_" + id
		event.ActorID = actor
		event.Action = "hosting.source_grant.created"
		event.TargetID = id
		event.Metadata = map[string]any{"request_hash": hash, "source_generation": src.Generation}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		return publicSourceGrant(g, src.Generation), authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner")
	})
}

// ListHostingSourceGrants excludes secret references and bounds history under current authority.
func (s *PostgresStore) ListHostingSourceGrants(ctx context.Context, actor, resource string, page Page) (PageResult[domain.PublicHostingSourceGrant], error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (PageResult[domain.PublicHostingSourceGrant], error) {
		var out PageResult[domain.PublicHostingSourceGrant]
		q := sqlcgen.New(tx)
		r, err := q.LockHostingBuildResource(ctx, resource)
		if err != nil {
			return out, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "maintainer"); err != nil {
			return out, err
		}
		count, err := q.CountResourceHostingSourceGrants(ctx, resource)
		if err != nil {
			return out, err
		}
		page = hostingPage(page, int(count))
		out = PageResult[domain.PublicHostingSourceGrant]{Items: []domain.PublicHostingSourceGrant{}, Limit: page.Limit, Offset: page.Offset, Total: int(count)}
		rows, err := q.ListResourceHostingSourceGrants(ctx, sqlcgen.ListResourceHostingSourceGrantsParams{ResourceID: resource, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
		if err != nil {
			return out, err
		}
		for _, raw := range rows {
			var g domain.PublicHostingSourceGrant
			if json.Unmarshal(raw, &g) != nil {
				return out, ErrConflict
			}
			out.Items = append(out.Items, g)
		}
		return out, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "maintainer")
	})
}

// RevokeResourceHostingSourceGrant checks immutable resource binding before owner revocation.
func (s *PostgresStore) RevokeResourceHostingSourceGrant(ctx context.Context, actor, resource, id string, event domain.AuditEvent) error {
	g, err := sqlcgen.New(s.pool).GetHostingSourceGrant(ctx, id)
	if err != nil {
		return hostingError(err)
	}
	if g.ResourceID != resource {
		return ErrConflict
	}
	return s.RevokeHostingSourceGrant(ctx, actor, id, event)
}

var _ HostingSourceGrantRepository = (*PostgresStore)(nil)
