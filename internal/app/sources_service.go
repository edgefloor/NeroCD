package app

import (
	"context"
	"strings"

	"nerocd/internal/domain"
)

func (s *Service) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.templates.ListTemplates(ctx, projectID)
	}
	templates, err := s.templates.ListTemplates(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterTemplatesForPrincipal(ctx, principal, templates)
}

func (s *Service) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListRepositories(ctx, projectID)
	}
	repositories, err := s.sources.ListRepositories(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterRepositoriesForPrincipal(ctx, principal, repositories)
}

func (s *Service) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListAccessKeys(ctx, projectID)
	}
	keys, err := s.sources.ListAccessKeys(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterAccessKeysForPrincipal(ctx, principal, keys)
}

func (s *Service) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.sources.ListInventories(ctx, projectID)
	}
	inventories, err := s.sources.ListInventories(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.filterInventoriesForPrincipal(ctx, principal, inventories)
}
