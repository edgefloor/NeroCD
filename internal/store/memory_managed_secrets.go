package store

import (
	"context"
	"sort"

	"nerocd/internal/domain"
)

func (s *MemoryStore) managedSecretIndexLocked(id string) int {
	for i, v := range s.managedSecrets {
		if v.ID == id {
			return i
		}
	}
	return -1
}
func (s *MemoryStore) managedAuditAvailableLocked(event domain.AuditEvent) bool {
	return s.auditIDAvailableLocked(event.ID) && s.hostingPrincipalExistsLocked(event.ActorID)
}

// ListManagedSecrets returns only metadata scoped to the explicit project.
func (s *MemoryStore) ListManagedSecrets(_ context.Context, project string, page Page) (PageResult[domain.ManagedSecret], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ManagedSecret, 0)
	for _, item := range s.managedSecrets {
		if item.ProjectID == project {
			items = append(items, copyManagedSecret(item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return paginateSlice(items, page), nil
}

// GetManagedSecret returns metadata without encryption details.
func (s *MemoryStore) GetManagedSecret(_ context.Context, id string) (domain.ManagedSecret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.managedSecretIndexLocked(id)
	if i < 0 {
		return domain.ManagedSecret{}, ErrNotFound
	}
	return copyManagedSecret(s.managedSecrets[i]), nil
}

// ListManagedSecretVersions returns immutable version metadata in descending version order.
func (s *MemoryStore) ListManagedSecretVersions(_ context.Context, id string, page Page) (PageResult[domain.ManagedSecretVersion], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ManagedSecretVersion, 0)
	for _, v := range s.managedSecretVersions {
		if v.Metadata.SecretID == id {
			items = append(items, v.Metadata)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version > items[j].Version })
	return paginateSlice(items, page), nil
}

// GetManagedSecretRecord returns a defensive copy for internal decryption only.
func (s *MemoryStore) GetManagedSecretRecord(_ context.Context, id string, version int64) (ManagedSecretRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.managedSecretVersions {
		if v.Metadata.SecretID == id && v.Metadata.Version == version {
			return copyManagedRecord(v), nil
		}
	}
	return ManagedSecretRecord{}, ErrNotFound
}

// CreateManagedSecret atomically creates metadata, version one, and a mandatory audit.
func (s *MemoryStore) CreateManagedSecret(_ context.Context, item domain.ManagedSecret, record ManagedSecretRecord, event domain.AuditEvent) (domain.ManagedSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" || domain.ValidateHostingName(item.Name) != nil || !s.hostingProjectExistsLocked(item.ProjectID) || !s.hostingPrincipalExistsLocked(item.CreatedBy) || !validManagedRecord(record) || record.Metadata.SecretID != item.ID || record.Metadata.Version != 1 || record.Metadata.CreatedBy != item.CreatedBy {
		return domain.ManagedSecret{}, ErrConflict
	}
	for _, old := range s.managedSecrets {
		if old.ID == item.ID || old.ProjectID == item.ProjectID && old.Name == item.Name {
			return domain.ManagedSecret{}, ErrConflict
		}
	}
	event, err := managedSecretAudit(event, "managed_secret.created", item, 1)
	if err != nil || !s.managedAuditAvailableLocked(event) {
		return domain.ManagedSecret{}, ErrConflict
	}
	item.CurrentVersion = 1
	item.Status = "active"
	item.RevokedAt = nil
	s.managedSecrets = append(s.managedSecrets, copyManagedSecret(item))
	s.managedSecretVersions = append(s.managedSecretVersions, copyManagedRecord(record))
	s.appendHostingAuditLocked(&event)
	return item, nil
}

// RotateManagedSecret serializes compare-and-swap, immutable append, and audit.
func (s *MemoryStore) RotateManagedSecret(_ context.Context, id string, expected int64, record ManagedSecretRecord, event domain.AuditEvent) (domain.ManagedSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.managedSecretIndexLocked(id)
	if i < 0 {
		return domain.ManagedSecret{}, ErrNotFound
	}
	item := s.managedSecrets[i]
	if !validManagedExpected(expected) || item.Status != "active" || item.CurrentVersion != expected || !validManagedRecord(record) || record.Metadata.SecretID != id || record.Metadata.Version != expected+1 || !s.hostingPrincipalExistsLocked(record.Metadata.CreatedBy) {
		return domain.ManagedSecret{}, ErrConflict
	}
	event, err := managedSecretAudit(event, "managed_secret.rotated", item, expected+1)
	if err != nil || !s.managedAuditAvailableLocked(event) {
		return domain.ManagedSecret{}, ErrConflict
	}
	item.CurrentVersion++
	item.UpdatedAt = record.Metadata.CreatedAt
	s.managedSecrets[i] = item
	s.managedSecretVersions = append(s.managedSecretVersions, copyManagedRecord(record))
	s.appendHostingAuditLocked(&event)
	return copyManagedSecret(item), nil
}

// RevokeManagedSecret irreversibly revokes under the same lock used for reveal authorization.
func (s *MemoryStore) RevokeManagedSecret(_ context.Context, id string, expected int64, event domain.AuditEvent) (domain.ManagedSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.managedSecretIndexLocked(id)
	if i < 0 {
		return domain.ManagedSecret{}, ErrNotFound
	}
	item := s.managedSecrets[i]
	if expected < 1 || item.CurrentVersion != expected || item.Status != "active" {
		return domain.ManagedSecret{}, ErrConflict
	}
	event, err := managedSecretAudit(event, "managed_secret.revoked", item, expected)
	if err != nil || !s.managedAuditAvailableLocked(event) {
		return domain.ManagedSecret{}, ErrConflict
	}
	item.Status = "revoked"
	item.UpdatedAt = event.CreatedAt
	at := event.CreatedAt
	item.RevokedAt = &at
	s.managedSecrets[i] = item
	s.appendHostingAuditLocked(&event)
	return copyManagedSecret(item), nil
}

// AuthorizeManagedSecretReveal commits a fresh audit only while the selected version remains active.
func (s *MemoryStore) AuthorizeManagedSecretReveal(_ context.Context, id string, version int64, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.managedSecretIndexLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	item := s.managedSecrets[i]
	if item.Status != "active" {
		return ErrConflict
	}
	found := false
	for _, record := range s.managedSecretVersions {
		if record.Metadata.SecretID == id && record.Metadata.Version == version {
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	event, err := managedSecretAudit(event, "managed_secret.revealed", item, version)
	if err != nil || !s.managedAuditAvailableLocked(event) {
		return ErrConflict
	}
	s.appendHostingAuditLocked(&event)
	return nil
}
