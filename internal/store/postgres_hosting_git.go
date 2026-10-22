package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func gitReference(src domain.HostingGitSource) domain.ResourceSource {
	return domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: src.ID, Generation: src.Generation}}
}
func expandGitResource(ctx context.Context, q *sqlcgen.Queries, item domain.Resource) (domain.Resource, error) {
	if item.Source.Kind != "git" {
		return item, nil
	}
	if item.Source.Git == nil {
		return item, ErrConflict
	}
	raw, err := q.GetExactResourceGitSource(ctx, sqlcgen.GetExactResourceGitSourceParams{SourceID: item.Source.Git.SourceID, Generation: item.Source.Git.Generation, ResourceID: item.ID, ProjectID: item.ProjectID})
	if err != nil {
		return item, hostingError(err)
	}
	var src domain.HostingGitSource
	if err = json.Unmarshal(raw, &src); err != nil {
		return item, err
	}
	if domain.ValidateHostingGitSource(src) != nil {
		return item, ErrConflict
	}
	item.Source.Git = &domain.GitResourceSource{SourceID: src.ID, Generation: src.Generation, GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: src.RepositoryURL, RequestedRef: src.RequestedRef, CredentialKind: src.CredentialKind, Settings: src.Settings}}
	return item, nil
}
func (s *PostgresStore) mutateHostingResource(ctx context.Context, item domain.Resource, expected int64, create bool, opts []MutationOption) (domain.Resource, error) {
	if validateResource(item) != nil || (!create && (expected < 1 || expected == math.MaxInt64)) {
		return domain.Resource{}, ErrConflict
	}
	options := resourceMutationOptions(opts)
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.Resource, error) {
		q := s.queries.WithTx(tx)
		actor := item.CreatedBy
		if options.audit != nil {
			actor = options.audit.ActorID
		}
		if options.audit != nil || item.Source.Kind == "git" {
			if err := authorizeBuildActor(ctx, tx, actor, item.ProjectID, "maintainer"); err != nil {
				return domain.Resource{}, err
			}
		}
		if !create {
			row, err := q.LockResourceConfiguration(ctx, item.ID)
			if err != nil {
				return domain.Resource{}, err
			}
			if row.Generation != expected || row.ProjectID != item.ProjectID || row.ProjectEnvironmentID != item.ProjectEnvironmentID || row.Kind != item.Kind || row.CreatedBy != item.CreatedBy || !row.CreatedAt.Equal(item.CreatedAt) {
				return domain.Resource{}, ErrConflict
			}
			old, err := resourceFromHostingSQL(row)
			if err != nil {
				return domain.Resource{}, err
			}
			if old.Source.Kind != item.Source.Kind {
				return domain.Resource{}, ErrConflict
			}
			if item.Source.Kind == "git" && (old.Source.Git == nil || item.Source.Git == nil || old.Source.Git.SourceID != item.Source.Git.SourceID || old.Source.Git.Generation != item.Source.Git.Generation) {
				return domain.Resource{}, ErrConflict
			}
		}
		if _, err := q.GetProjectEnvironment(ctx, item.ProjectEnvironmentID); err != nil {
			return domain.Resource{}, ErrConflict
		}
		var src *domain.HostingGitSource
		if item.Source.Kind == "git" {
			if options.audit == nil {
				return domain.Resource{}, ErrConflict
			}
			if options.gitSource != nil {
				copySource := *options.gitSource
				src = &copySource
				if src.ResourceID != item.ID || src.ProjectID != item.ProjectID || src.ID != item.Source.Git.SourceID || domain.ValidateHostingGitSource(*src) != nil {
					return domain.Resource{}, ErrConflict
				}
				if create {
					if src.Generation != 1 {
						return domain.Resource{}, ErrConflict
					}
				} else {
					old, err := readBuildSource(ctx, tx, item.ID)
					if err != nil || old.ID != src.ID || old.Generation != src.Generation {
						return domain.Resource{}, ErrConflict
					}
					if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(*src) {
						src.Generation++
						if src.Generation < 1 {
							return domain.Resource{}, ErrConflict
						}
					} else {
						src = nil
					}
				}
			} else if create {
				return domain.Resource{}, ErrConflict
			}
			if src != nil {
				item.Source = gitReference(*src)
			} else {
				item.Source = domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: item.Source.Git.SourceID, Generation: item.Source.Git.Generation}}
			}
		} else if options.gitSource != nil {
			return domain.Resource{}, ErrConflict
		}
		raw, err := json.Marshal(item.Source)
		if item.Source.Kind == "git" {
			raw, err = json.Marshal(map[string]any{"kind": "git", "git": map[string]any{"source_id": item.Source.Git.SourceID, "generation": item.Source.Git.Generation}})
		}
		if err != nil {
			return domain.Resource{}, err
		}
		var row sqlcgen.Resource
		if create {
			row, err = q.CreateResource(ctx, sqlcgen.CreateResourceParams{ID: item.ID, ProjectID: item.ProjectID, ProjectEnvironmentID: item.ProjectEnvironmentID, Name: item.Name, Kind: item.Kind, Source: raw, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
		} else {
			row, err = q.UpdateResource(ctx, sqlcgen.UpdateResourceParams{ID: item.ID, Name: item.Name, Source: raw, UpdatedAt: item.UpdatedAt, Generation: expected, ProjectID: item.ProjectID, ProjectEnvironmentID: item.ProjectEnvironmentID, Kind: item.Kind, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt})
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = ErrConflict
			}
			return domain.Resource{}, err
		}
		if src != nil {
			if create {
				err = q.CreateHostingBuildSource(ctx, sqlcgen.CreateHostingBuildSourceParams{ID: src.ID, ResourceID: item.ID, ProjectID: item.ProjectID})
			} else {
				err = casBuild(q.AdvanceHostingBuildSource(ctx, sqlcgen.AdvanceHostingBuildSourceParams{ID: src.ID, Generation: src.Generation - 1}))
			}
			if err != nil {
				return domain.Resource{}, err
			}
			if err = q.AppendHostingBuildSourceVersion(ctx, sqlcgen.AppendHostingBuildSourceVersionParams{SourceID: src.ID, Generation: src.Generation, Body: buildJSON(src), BodyHash: domain.HostingDocumentHash(src), CreatedBy: actor}); err != nil {
				return domain.Resource{}, err
			}
		}
		if options.audit != nil {
			audit := *options.audit
			audit.TargetID = item.ID
			if create {
				audit.Action = "resource.created"
			} else {
				audit.Action = "resource.updated"
			}
			audit.Metadata = map[string]any{"project_id": item.ProjectID, "project_environment_id": item.ProjectEnvironmentID, "generation": row.Generation}
			if err = createAuditWithQueries(ctx, q, audit); err != nil {
				return domain.Resource{}, err
			}
		}
		result, err := resourceFromHostingSQL(row)
		if err != nil {
			return result, err
		}
		result, err = expandGitResource(ctx, q, result)
		if err != nil {
			return result, err
		}
		if options.audit != nil || item.Source.Kind == "git" {
			return result, authorizeBuildActor(ctx, tx, actor, item.ProjectID, "maintainer")
		}
		return result, nil
	})
}
func exactBuildResourceSource(raw []byte, src domain.HostingGitSource) bool {
	var ref domain.ResourceSource
	return json.Unmarshal(raw, &ref) == nil && ref.Kind == "git" && ref.Git != nil && ref.Git.SourceID == src.ID && ref.Git.Generation == src.Generation
}
