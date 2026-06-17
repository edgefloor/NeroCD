package store

import (
	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
)

func repositorySetter(repository domain.Repository) *bobmodels.RepositorySetter {
	return &bobmodels.RepositorySetter{
		ID:         &repository.ID,
		ProjectID:  &repository.ProjectID,
		Name:       &repository.Name,
		URL:        &repository.URL,
		Provider:   &repository.Provider,
		DefaultRef: &repository.DefaultRef,
		CreatedAt:  &repository.CreatedAt,
	}
}

func repositoryFromGenerated(repository *bobmodels.Repository) domain.Repository {
	return domain.Repository{
		ID:         repository.ID,
		ProjectID:  repository.ProjectID,
		Name:       repository.Name,
		URL:        repository.URL,
		Provider:   repository.Provider,
		DefaultRef: repository.DefaultRef,
		CreatedAt:  repository.CreatedAt,
	}
}

func accessKeySetter(key domain.AccessKey) *bobmodels.AccessKeySetter {
	return &bobmodels.AccessKeySetter{
		ID:          &key.ID,
		ProjectID:   &key.ProjectID,
		Name:        &key.Name,
		Kind:        &key.Kind,
		Fingerprint: &key.Fingerprint,
		CreatedAt:   &key.CreatedAt,
	}
}

func accessKeyFromGenerated(key *bobmodels.AccessKey) domain.AccessKey {
	return domain.AccessKey{
		ID:          key.ID,
		ProjectID:   key.ProjectID,
		Name:        key.Name,
		Kind:        key.Kind,
		Fingerprint: key.Fingerprint,
		CreatedAt:   key.CreatedAt,
	}
}

func inventorySetter(inventory domain.Inventory) *bobmodels.InventorySetter {
	return &bobmodels.InventorySetter{
		ID:        &inventory.ID,
		ProjectID: &inventory.ProjectID,
		Name:      &inventory.Name,
		Kind:      &inventory.Kind,
		Source:    &inventory.Source,
		CreatedAt: &inventory.CreatedAt,
	}
}

func inventoryFromGenerated(inventory *bobmodels.Inventory) domain.Inventory {
	return domain.Inventory{
		ID:        inventory.ID,
		ProjectID: inventory.ProjectID,
		Name:      inventory.Name,
		Kind:      inventory.Kind,
		Source:    inventory.Source,
		CreatedAt: inventory.CreatedAt,
	}
}
