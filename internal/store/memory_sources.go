package store

import (
	"context"

	"nerocd/internal/domain"
)

func (s *MemoryStore) ListRepositories(_ context.Context, projectID string) ([]domain.Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Repository, 0, len(s.repositories))
	for _, repository := range s.repositories {
		if projectID == "" || repository.ProjectID == projectID {
			out = append(out, repository)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateRepository(_ context.Context, repository domain.Repository) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = append(s.repositories, repository)
	return repository, nil
}
func (s *MemoryStore) CreateRepositoryWithAudit(_ context.Context, repository domain.Repository, audit domain.AuditEvent) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = append(s.repositories, repository)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return repository, nil
}
func (s *MemoryStore) ConfigureRepositoryPolicy(_ context.Context, request RepositoryPolicyConfiguration) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authorized := false
	for _, user := range s.users {
		if user.ID == request.ActorID && user.GlobalRole == domain.RoleSystemAdmin {
			authorized = true
			break
		}
	}
	if !authorized {
		for _, member := range s.projectMembers {
			if member.ProjectID == request.ProjectID && member.UserID == request.ActorID && (member.Role == domain.RoleOwner || member.Role == domain.RoleMaintainer) {
				authorized = true
				break
			}
		}
	}
	if !authorized {
		return domain.Repository{}, ErrNotFound
	}
	key := request.RepositoryID + "\x00" + request.ConfigurationID
	if receipt, ok := s.policyConfigurations[key]; ok {
		if receipt.actorID != request.ActorID || receipt.policyHash != request.PolicyHash {
			return domain.Repository{}, ErrConflict
		}
		for _, repository := range s.repositories {
			if repository.ID == request.RepositoryID && repository.ProjectID == request.ProjectID {
				return repository, nil
			}
		}
		return domain.Repository{}, ErrNotFound
	}
	for i := range s.repositories {
		if s.repositories[i].ID == request.RepositoryID && s.repositories[i].ProjectID == request.ProjectID {
			if s.repositories[i].Policy.State == "configured" {
				return domain.Repository{}, ErrConflict
			}
			s.repositories[i].Policy = request.Policy
			s.auditEvents = append(s.auditEvents, request.Audit)
			s.policyConfigurations[key] = memoryPolicyConfiguration{actorID: request.ActorID, policyHash: request.PolicyHash}
			return s.repositories[i], nil
		}
	}
	return domain.Repository{}, ErrNotFound
}

func (s *MemoryStore) ListAccessKeys(_ context.Context, projectID string) ([]domain.AccessKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AccessKey, 0, len(s.accessKeys))
	for _, key := range s.accessKeys {
		if projectID == "" || key.ProjectID == projectID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateAccessKey(_ context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKeys = append(s.accessKeys, key)
	return key, nil
}
func (s *MemoryStore) CreateAccessKeyWithAudit(_ context.Context, key domain.AccessKey, audit domain.AuditEvent) (domain.AccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKeys = append(s.accessKeys, key)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return key, nil
}

func (s *MemoryStore) ListInventories(_ context.Context, projectID string) ([]domain.Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Inventory, 0, len(s.inventories))
	for _, inventory := range s.inventories {
		if projectID == "" || inventory.ProjectID == projectID {
			out = append(out, inventory)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateInventory(_ context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventories = append(s.inventories, inventory)
	return inventory, nil
}
func (s *MemoryStore) CreateInventoryWithAudit(_ context.Context, inventory domain.Inventory, audit domain.AuditEvent) (domain.Inventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventories = append(s.inventories, inventory)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return inventory, nil
}
