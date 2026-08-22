package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	rows, err := s.queries.ListRepositories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repositories := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repository, err := repositoryFromSQLC(row)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func (s *PostgresStore) CreateRepository(ctx context.Context, repository domain.Repository, opts ...MutationOption) (domain.Repository, error) {
	audit := resolveMutationOptions(opts)
	policy, err := json.Marshal(repository.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	if audit == nil {
		inserted, err := s.queries.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
		if err != nil {
			return domain.Repository{}, err
		}
		return repositoryFromSQLC(inserted)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
	if err != nil {
		return domain.Repository{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Repository{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, err
	}
	return repositoryFromSQLC(row)
}
func (s *PostgresStore) ConfigureRepositoryPolicy(ctx context.Context, request RepositoryPolicyConfiguration) (domain.Repository, error) {
	raw, err := json.Marshal(request.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	row, err := q.LockRepositoryForPolicyConfiguration(ctx, sqlcgen.LockRepositoryForPolicyConfigurationParams{ID: request.RepositoryID, ProjectID: request.ProjectID})
	if err == pgx.ErrNoRows {
		return domain.Repository{}, ErrNotFound
	}
	if err != nil {
		return domain.Repository{}, err
	}
	repository, err := repositoryFromSQLC(row)
	if err != nil {
		return domain.Repository{}, err
	}
	authorized, err := q.RepositoryPolicyActorAuthorized(ctx, sqlcgen.RepositoryPolicyActorAuthorizedParams{ProjectID: request.ProjectID, ID: request.ActorID})
	if err != nil {
		return domain.Repository{}, err
	}
	if !authorized {
		return domain.Repository{}, ErrNotFound
	}
	receipt, err := q.GetRepositoryPolicyConfigurationReceipt(ctx, sqlcgen.GetRepositoryPolicyConfigurationReceiptParams{RepositoryID: request.RepositoryID, ConfigurationID: request.ConfigurationID})
	if err == nil {
		if receipt.ActorID != request.ActorID || receipt.PolicySha256 != request.PolicyHash {
			return domain.Repository{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Repository{}, err
		}
		return repository, nil
	}
	if err != pgx.ErrNoRows {
		return domain.Repository{}, err
	}
	if repository.Policy.State != "legacy_unverified" {
		return domain.Repository{}, ErrConflict
	}
	if err = q.SetRepositoryPolicyConfiguration(ctx, sqlcgen.SetRepositoryPolicyConfigurationParams{ID: request.RepositoryID, RepositoryPolicy: raw}); err != nil {
		return domain.Repository{}, err
	}
	metadata, err := json.Marshal(request.Audit.Metadata)
	if err != nil {
		return domain.Repository{}, err
	}
	if err = q.CreateAuditEventAtDatabaseTime(ctx, sqlcgen.CreateAuditEventAtDatabaseTimeParams{ID: request.Audit.ID, ActorID: request.Audit.ActorID, Action: request.Audit.Action, TargetID: request.Audit.TargetID, Metadata: metadata}); err != nil {
		return domain.Repository{}, err
	}
	if err = q.CreateRepositoryPolicyConfigurationReceipt(ctx, sqlcgen.CreateRepositoryPolicyConfigurationReceiptParams{RepositoryID: request.RepositoryID, ConfigurationID: request.ConfigurationID, ActorID: request.ActorID, PolicySha256: request.PolicyHash, AuditID: request.Audit.ID}); err != nil {
		return domain.Repository{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, err
	}
	repository.Policy = request.Policy
	return repository, nil
}

func (s *PostgresStore) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	rows, err := s.queries.ListAccessKeys(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]domain.AccessKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, accessKeyFromSQLC(row))
	}
	return keys, nil
}

func (s *PostgresStore) CreateAccessKey(ctx context.Context, key domain.AccessKey, opts ...MutationOption) (domain.AccessKey, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		inserted, err := s.queries.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
		if err != nil {
			return domain.AccessKey{}, err
		}
		return accessKeyFromSQLC(inserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AccessKey{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
	if err != nil {
		return domain.AccessKey{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.AccessKey{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.AccessKey{}, err
	}
	return accessKeyFromSQLC(row), nil
}

func (s *PostgresStore) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	rows, err := s.queries.ListInventories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	inventories := make([]domain.Inventory, 0, len(rows))
	for _, row := range rows {
		inventories = append(inventories, inventoryFromSQLC(row))
	}
	return inventories, nil
}

func (s *PostgresStore) CreateInventory(ctx context.Context, inventory domain.Inventory, opts ...MutationOption) (domain.Inventory, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		inserted, err := s.queries.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
		if err != nil {
			return domain.Inventory{}, err
		}
		return inventoryFromSQLC(inserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Inventory{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
	if err != nil {
		return domain.Inventory{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Inventory{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Inventory{}, err
	}
	return inventoryFromSQLC(row), nil
}
