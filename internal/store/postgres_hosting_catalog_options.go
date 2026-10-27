package store

import (
	"context"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func catalogDestinationOption(row sqlcgen.CatalogDestinationOptionsRow, requirements domain.HostingCatalogRequirements) domain.HostingCatalogDestinationOption {
	v := domain.HostingCatalogDestinationOption{Destination: domain.Destination{ID: row.ID, ServerID: row.ServerID, Name: row.Name, RuntimeKind: row.RuntimeKind, CreatedAt: row.CreatedAt}, ServerName: row.ServerName, MissingCapabilities: []string{}}
	for _, capability := range requirements.RequiredCapabilities {
		if !slices.Contains(row.Capabilities, capability) {
			v.MissingCapabilities = append(v.MissingCapabilities, capability)
		}
	}
	switch {
	case row.RuntimeKind != "docker_compose":
		v.ReasonCode = "unsupported_runtime"
	case row.RunnerStatus != "active":
		v.ReasonCode = "runner_unavailable"
	case !row.Credentialed || slices.Contains(row.Capabilities, "shell"):
		v.ReasonCode = "runner_ineligible"
	case len(v.MissingCapabilities) > 0:
		v.ReasonCode = "missing_capabilities"
	default:
		v.Available = true
	}
	return v
}

// HostingCatalogDestinationOptions reads one project-authorized snapshot without reserving placement.
func (s *PostgresStore) HostingCatalogDestinationOptions(ctx context.Context, actor, project string, template domain.HostingCatalogTemplate, page Page) (domain.HostingCatalogDestinationOptions, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v := domain.HostingCatalogDestinationOptions{TemplateID: template.ID, ImmutableVersion: template.Version, Requirements: domain.CatalogRequirements(template), Items: []domain.HostingCatalogDestinationOption{}}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer storageDeploymentRollback(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
		return v, err
	}
	q := sqlcgen.New(tx)
	total, err := q.CountProjectDestinations(ctx, project)
	if err != nil {
		return v, err
	}
	page = hostingPage(page, int(total))
	v.Total = int(total)
	v.Limit = page.Limit
	v.Offset = page.Offset
	rows, err := q.CatalogDestinationOptions(ctx, sqlcgen.CatalogDestinationOptionsParams{ProjectID: project, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
	if err != nil {
		return v, err
	}
	for _, row := range rows {
		v.Items = append(v.Items, catalogDestinationOption(row, v.Requirements))
	}
	if err = proxyCheckLegacyToken(ctx, tx, actor); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}
