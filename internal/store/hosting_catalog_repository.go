package store

import (
	"context"

	"nerocd/internal/domain"
)

type HostingCatalogCredentialRecord struct {
	Name   string
	Record ManagedSecretRecord
}

type HostingCatalogRepository interface {
	InstallHostingCatalog(context.Context, string, domain.HostingCatalogInstallInput, []HostingCatalogCredentialRecord, domain.AuditEvent) (domain.HostingCatalogInstallation, error)
	GetHostingCatalogInstallation(context.Context, string, string) (domain.HostingCatalogInstallation, error)
}
