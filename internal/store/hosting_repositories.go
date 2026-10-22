package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingRepository owns desired hosting configuration and atomic mutation audits.
type HostingRepository interface {
	ListProjectEnvironments(context.Context, string, Page) (PageResult[domain.ProjectEnvironment], error)
	GetProjectEnvironment(context.Context, string) (domain.ProjectEnvironment, error)
	CreateProjectEnvironment(context.Context, domain.ProjectEnvironment, ...MutationOption) (domain.ProjectEnvironment, error)
	ListResources(context.Context, string, string, Page) (PageResult[domain.Resource], error)
	GetResource(context.Context, string) (domain.Resource, error)
	CreateResource(context.Context, domain.Resource, ...MutationOption) (domain.Resource, error)
	UpdateResource(context.Context, domain.Resource, int64, ...MutationOption) (domain.Resource, error)
}

var _ HostingRepository = (*MemoryStore)(nil)
var _ HostingRepository = (*PostgresStore)(nil)

func hostingPage(page Page, total int) Page {
	if !page.Enabled {
		page.Limit = total
		page.Offset = 0
	} else if page.Limit == 0 && !page.LimitSpecified {
		page.Limit = total
	}
	if page.Limit > MaxPageLimit {
		page.Limit = MaxPageLimit
	}
	if page.Limit < 0 {
		page.Limit = 0
	}
	if page.Offset < 0 {
		page.Offset = 0
	}
	if page.Offset > MaxPageOffset {
		page.Offset = MaxPageOffset
	}
	if page.Offset > total {
		page.Offset = total
	}
	return page
}

func validateResource(resource domain.Resource) error {
	if resource.Kind != "application" || resource.ID == "" || resource.ProjectID == "" || resource.ProjectEnvironmentID == "" || resource.CreatedBy == "" {
		return ErrConflict
	}
	if domain.ValidateHostingName(resource.Name) != nil || domain.ValidateResourceSource(resource.Source) != nil {
		return ErrConflict
	}
	return nil
}
