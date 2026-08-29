package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListRepositories implements the corresponding repository operation.
func (s *PostgresStore) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	rows, err := s.queries.ListRepositories(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list repositories query: %w", err)
	}
	repositories := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repository, err := repositoryFromSQLC(row)
		if err != nil {
			return nil, fmt.Errorf("decode repository row: %w", err)
		}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

// CreateRepository implements the corresponding repository operation.
func (s *PostgresStore) CreateRepository(ctx context.Context, repository domain.Repository, opts ...MutationOption) (domain.Repository, error) {
	audit := resolveMutationOptions(opts)
	policy, err := json.Marshal(repository.Policy)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("encode repository policy: %w", err)
	}
	if audit == nil {
		inserted, err := s.queries.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
		if err != nil {
			return domain.Repository{}, fmt.Errorf("create repository query: %w", err)
		}
		return repositoryFromSQLC(inserted)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
	if err != nil {
		return domain.Repository{}, fmt.Errorf("create repository query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Repository{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, fmt.Errorf("commit transaction: %w", err)
	}
	return repositoryFromSQLC(row)
}

// ConfigureRepositoryPolicy implements the corresponding repository operation.
func (s *PostgresStore) ConfigureRepositoryPolicy(ctx context.Context, request RepositoryPolicyConfiguration) (domain.Repository, error) {
	raw, err := json.Marshal(request.Policy)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("encode repository policy: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	row, err := q.LockRepositoryForPolicyConfiguration(ctx, sqlcgen.LockRepositoryForPolicyConfigurationParams{ID: request.RepositoryID, ProjectID: request.ProjectID})
	if err == pgx.ErrNoRows {
		return domain.Repository{}, ErrNotFound
	}
	if err != nil {
		return domain.Repository{}, fmt.Errorf("lock repository for policy configuration query: %w", err)
	}
	repository, err := repositoryFromSQLC(row)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("decode repository row: %w", err)
	}
	authorized, err := q.RepositoryPolicyActorAuthorized(ctx, sqlcgen.RepositoryPolicyActorAuthorizedParams{ProjectID: request.ProjectID, ID: request.ActorID})
	if err != nil {
		return domain.Repository{}, fmt.Errorf("repository policy actor authorized query: %w", err)
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
			return domain.Repository{}, fmt.Errorf("commit transaction: %w", err)
		}
		return repository, nil
	}
	if err != pgx.ErrNoRows {
		return domain.Repository{}, fmt.Errorf("get repository policy configuration receipt query: %w", err)
	}
	if repository.Policy.State != "legacy_unverified" {
		return domain.Repository{}, ErrConflict
	}
	if err = q.SetRepositoryPolicyConfiguration(ctx, sqlcgen.SetRepositoryPolicyConfigurationParams{ID: request.RepositoryID, RepositoryPolicy: raw}); err != nil {
		return domain.Repository{}, fmt.Errorf("set repository policy configuration query: %w", err)
	}
	metadata, err := json.Marshal(request.Audit.Metadata)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("encode audit metadata: %w", err)
	}
	if err = q.CreateAuditEventAtDatabaseTime(ctx, sqlcgen.CreateAuditEventAtDatabaseTimeParams{ID: request.Audit.ID, ActorID: request.Audit.ActorID, Action: request.Audit.Action, TargetID: request.Audit.TargetID, Metadata: metadata}); err != nil {
		return domain.Repository{}, fmt.Errorf("create audit event query: %w", err)
	}
	if err = q.CreateRepositoryPolicyConfigurationReceipt(ctx, sqlcgen.CreateRepositoryPolicyConfigurationReceiptParams{RepositoryID: request.RepositoryID, ConfigurationID: request.ConfigurationID, ActorID: request.ActorID, PolicySha256: request.PolicyHash, AuditID: request.Audit.ID}); err != nil {
		return domain.Repository{}, fmt.Errorf("create repository policy configuration receipt query: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, fmt.Errorf("commit transaction: %w", err)
	}
	repository.Policy = request.Policy
	return repository, nil
}

// ListAccessKeys implements the corresponding repository operation.
func (s *PostgresStore) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	rows, err := s.queries.ListAccessKeys(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list access keys query: %w", err)
	}
	keys := make([]domain.AccessKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, accessKeyFromSQLC(row))
	}
	return keys, nil
}

// CreateAccessKey implements the corresponding repository operation.
func (s *PostgresStore) CreateAccessKey(ctx context.Context, key domain.AccessKey, opts ...MutationOption) (domain.AccessKey, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		inserted, err := s.queries.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
		if err != nil {
			return domain.AccessKey{}, fmt.Errorf("create access key query: %w", err)
		}
		return accessKeyFromSQLC(inserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AccessKey{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
	if err != nil {
		return domain.AccessKey{}, fmt.Errorf("create access key query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.AccessKey{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.AccessKey{}, fmt.Errorf("commit transaction: %w", err)
	}
	return accessKeyFromSQLC(row), nil
}

// ListInventories implements the corresponding repository operation.
func (s *PostgresStore) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	rows, err := s.queries.ListInventories(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list inventories query: %w", err)
	}
	inventories := make([]domain.Inventory, 0, len(rows))
	for _, row := range rows {
		inventories = append(inventories, inventoryFromSQLC(row))
	}
	return inventories, nil
}

// CreateInventory implements the corresponding repository operation.
func (s *PostgresStore) CreateInventory(ctx context.Context, inventory domain.Inventory, opts ...MutationOption) (domain.Inventory, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		inserted, err := s.queries.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
		if err != nil {
			return domain.Inventory{}, fmt.Errorf("create inventory query: %w", err)
		}
		return inventoryFromSQLC(inserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Inventory{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
	if err != nil {
		return domain.Inventory{}, fmt.Errorf("create inventory query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Inventory{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Inventory{}, fmt.Errorf("commit transaction: %w", err)
	}
	return inventoryFromSQLC(row), nil
}
