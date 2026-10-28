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
	if src.Kind == "dockerfile" {
		return domain.ResourceSource{Kind: "dockerfile", Dockerfile: &domain.DockerfileResourceSource{SourceID: src.ID, Generation: src.Generation}}
	}
	return domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: src.ID, Generation: src.Generation}}
}
func expandGitResource(ctx context.Context, q *sqlcgen.Queries, item domain.Resource) (domain.Resource, error) {
	if item.Source.Kind != "git" && item.Source.Kind != "dockerfile" {
		return item, nil
	}
	sourceID, sourceGeneration := item.Source.GitIdentity()
	if sourceID == "" {
		return item, ErrConflict
	}
	raw, err := q.GetExactResourceGitSource(ctx, sqlcgen.GetExactResourceGitSourceParams{SourceID: sourceID, Generation: sourceGeneration, ResourceID: item.ID, ProjectID: item.ProjectID})
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
	if src.Kind == "dockerfile" {
		item.Source = domain.ResourceSource{Kind: "dockerfile", Dockerfile: &domain.DockerfileResourceSource{SourceID: src.ID, Generation: src.Generation, ContentHash: src.ContentHash, DockerfileResourceConfiguration: domain.DockerfileResourceConfiguration{Content: src.Content, Settings: src.Settings.Clone()}}}
		return item, nil
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
		if options.audit != nil || (item.Source.Kind == "git" || item.Source.Kind == "dockerfile") {
			if err := authorizeBuildActor(ctx, tx, actor, item.ProjectID, "maintainer"); err != nil {
				return domain.Resource{}, err
			}
		}
		if create && item.Source.Kind == "compose" {
			if options.audit == nil || options.composeCreate == nil || !domain.ValidHostingRuntimeDeliveryID(options.composeCreate.RequestID) {
				return domain.Resource{}, ErrConflict
			}
			request := options.composeCreate
			count, err := q.ClaimHostingComposeCreate(ctx, sqlcgen.ClaimHostingComposeCreateParams{ProjectID: item.ProjectID, RequestID: request.RequestID, ResourceID: item.ID, RequestHash: request.RequestHash})
			if err != nil {
				return domain.Resource{}, err
			}
			if count == 0 {
				receipt, err := q.GetHostingComposeCreate(ctx, sqlcgen.GetHostingComposeCreateParams{ProjectID: item.ProjectID, RequestID: request.RequestID})
				if err != nil {
					return domain.Resource{}, err
				}
				if receipt.RequestHash != request.RequestHash || receipt.ResourceID != item.ID {
					return domain.Resource{}, ErrConflict
				}
				row, err := q.GetResource(ctx, item.ID)
				if err != nil {
					return domain.Resource{}, err
				}
				return resourceFromHostingSQL(row)
			}
		} else if options.composeCreate != nil {
			return domain.Resource{}, ErrConflict
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
			oldID, oldGeneration := old.Source.GitIdentity()
			newID, newGeneration := item.Source.GitIdentity()
			if (item.Source.Kind == "git" || item.Source.Kind == "dockerfile") && (oldID == "" || oldID != newID || oldGeneration != newGeneration) {
				return domain.Resource{}, ErrConflict
			}
		}
		if _, err := q.GetProjectEnvironment(ctx, item.ProjectEnvironmentID); err != nil {
			return domain.Resource{}, ErrConflict
		}
		var src *domain.HostingGitSource
		if item.Source.Kind == "git" || item.Source.Kind == "dockerfile" {
			if options.audit == nil {
				return domain.Resource{}, ErrConflict
			}
			if options.gitSource != nil {
				copySource := *options.gitSource
				src = &copySource
				sourceID, _ := item.Source.GitIdentity()
				if src.ResourceID != item.ID || src.ProjectID != item.ProjectID || src.ID != sourceID || domain.ValidateHostingGitSource(*src) != nil {
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
				id, generation := item.Source.GitIdentity()
				kind := ""
				if item.Source.Kind == "dockerfile" {
					kind = "dockerfile"
				}
				item.Source = gitReference(domain.HostingGitSource{Kind: kind, ID: id, Generation: generation})
			}
		} else if options.gitSource != nil {
			return domain.Resource{}, ErrConflict
		}
		raw, err := json.Marshal(item.Source)
		if item.Source.Kind == "git" || item.Source.Kind == "dockerfile" {
			id, generation := item.Source.GitIdentity()
			raw, err = json.Marshal(map[string]any{"kind": item.Source.Kind, item.Source.Kind: map[string]any{"source_id": id, "generation": generation}})
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
		if item.Source.Kind == "compose" {
			definition, parseErr := domain.ParseHostingStack(*item.Source.Compose)
			if parseErr != nil {
				return domain.Resource{}, ErrConflict
			}
			if err = q.AppendHostingComposeSourceVersion(ctx, sqlcgen.AppendHostingComposeSourceVersionParams{ResourceID: item.ID, Generation: row.Generation, Original: item.Source.Compose.Original, OriginalHash: definition.OriginalHash, Definition: buildJSON(definition), DefinitionHash: domain.HostingDocumentHash(definition)}); err != nil {
				return domain.Resource{}, err
			}
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
		if options.audit != nil || (item.Source.Kind == "git" || item.Source.Kind == "dockerfile") {
			return result, authorizeBuildActor(ctx, tx, actor, item.ProjectID, "maintainer")
		}
		return result, nil
	})
}
func exactBuildResourceSource(raw []byte, src domain.HostingGitSource) bool {
	var ref domain.ResourceSource
	if json.Unmarshal(raw, &ref) != nil {
		return false
	}
	id, generation := ref.GitIdentity()
	kind := "git"
	if src.Kind == "dockerfile" {
		kind = "dockerfile"
	}
	return ref.Kind == kind && id == src.ID && generation == src.Generation
}
