package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingCatalogCredentialRecord associates a template credential name with its encrypted managed
// record.
type HostingCatalogCredentialRecord struct {
	Name   string
	Record ManagedSecretRecord
}

// HostingCatalogRepository defines persistence operations for catalog.
type HostingCatalogRepository interface {
	InstallHostingCatalog(context.Context, string, domain.HostingCatalogInstallInput, []HostingCatalogCredentialRecord, domain.AuditEvent) (domain.HostingCatalogInstallation, error)
	GetHostingCatalogInstallation(context.Context, string, string) (domain.HostingCatalogInstallation, error)
}

// HostingCatalogOptionsRepository returns current authorized placement qualifications.
type HostingCatalogOptionsRepository interface {
	HostingCatalogDestinationOptions(context.Context, string, string, domain.HostingCatalogTemplate, Page) (domain.HostingCatalogDestinationOptions, error)
}
