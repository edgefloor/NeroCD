package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func hostingError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514", "22003":
			return ErrConflict
		}
	}
	return fmt.Errorf("hosting persistence: %w", err)
}
func environmentFromHostingSQL(row sqlcgen.ProjectEnvironment) domain.ProjectEnvironment {
	return domain.ProjectEnvironment{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, CreatedAt: row.CreatedAt}
}
func resourceFromHostingSQL(row sqlcgen.Resource) (domain.Resource, error) {
	var source domain.ResourceSource
	if err := json.Unmarshal(row.Source, &source); err != nil {
		return domain.Resource{}, fmt.Errorf("decode resource source: %w", err)
	}
	return domain.Resource{ID: row.ID, ProjectID: row.ProjectID, ProjectEnvironmentID: row.ProjectEnvironmentID, Name: row.Name, Kind: row.Kind, Source: source, Generation: row.Generation, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func hostingMutation[T any](ctx context.Context, s *PostgresStore, opts []MutationOption, mutate func(*sqlcgen.Queries) (T, error)) (T, error) {
	var zero T
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, hostingError(err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	result, err := mutate(q)
	if err != nil {
		return zero, hostingError(err)
	}
	if audit := resolveMutationOptions(opts); audit != nil {
		if err = createAuditWithQueries(ctx, q, *audit); err != nil {
			return zero, hostingError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, hostingError(err)
	}
	return result, nil
}

// ListProjectEnvironments returns a bounded page with its project total.
func (s *PostgresStore) ListProjectEnvironments(ctx context.Context, projectID string, page Page) (PageResult[domain.ProjectEnvironment], error) {
	total, err := s.queries.CountProjectEnvironments(ctx, projectID)
	if err != nil {
		return PageResult[domain.ProjectEnvironment]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListProjectEnvironments(ctx, sqlcgen.ListProjectEnvironmentsParams{ProjectID: projectID, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.ProjectEnvironment]{}, hostingError(err)
	}
	items := make([]domain.ProjectEnvironment, 0, len(rows))
	for _, row := range rows {
		items = append(items, environmentFromHostingSQL(row))
	}
	return PageResult[domain.ProjectEnvironment]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// GetProjectEnvironment returns one shared environment.
func (s *PostgresStore) GetProjectEnvironment(ctx context.Context, id string) (domain.ProjectEnvironment, error) {
	row, err := s.queries.GetProjectEnvironment(ctx, id)
	if err != nil {
		return domain.ProjectEnvironment{}, hostingError(err)
	}
	return environmentFromHostingSQL(row), nil
}

// CreateProjectEnvironment commits the environment and optional audit together.
func (s *PostgresStore) CreateProjectEnvironment(ctx context.Context, item domain.ProjectEnvironment, opts ...MutationOption) (domain.ProjectEnvironment, error) {
	if item.ID == "" || domain.ValidateHostingName(item.Name) != nil {
		return domain.ProjectEnvironment{}, ErrConflict
	}
	return hostingMutation(ctx, s, opts, func(q *sqlcgen.Queries) (domain.ProjectEnvironment, error) {
		row, err := q.CreateProjectEnvironment(ctx, sqlcgen.CreateProjectEnvironmentParams{ID: item.ID, ProjectID: item.ProjectID, Name: item.Name, CreatedAt: item.CreatedAt})
		if err != nil {
			return domain.ProjectEnvironment{}, err
		}
		return environmentFromHostingSQL(row), nil
	})
}

// ListResources returns desired configuration in stable creation order.
func (s *PostgresStore) ListResources(ctx context.Context, projectID, environmentID string, page Page) (PageResult[domain.Resource], error) {
	total, err := s.queries.CountResources(ctx, sqlcgen.CountResourcesParams{ProjectID: projectID, Column2: environmentID})
	if err != nil {
		return PageResult[domain.Resource]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListResources(ctx, sqlcgen.ListResourcesParams{ProjectID: projectID, Column2: environmentID, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.Resource]{}, hostingError(err)
	}
	items := make([]domain.Resource, 0, len(rows))
	for _, row := range rows {
		item, err := resourceFromHostingSQL(row)
		if err != nil {
			return PageResult[domain.Resource]{}, err
		}
		items = append(items, item)
	}
	return PageResult[domain.Resource]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// GetResource returns persisted scope and generation along with desired source.
func (s *PostgresStore) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	row, err := s.queries.GetResource(ctx, id)
	if err != nil {
		return domain.Resource{}, hostingError(err)
	}
	return resourceFromHostingSQL(row)
}

// CreateResource validates the source and commits configuration with its audit.
func (s *PostgresStore) CreateResource(ctx context.Context, item domain.Resource, opts ...MutationOption) (domain.Resource, error) {
	if err := validateResource(item); err != nil {
		return domain.Resource{}, err
	}
	source, err := json.Marshal(item.Source)
	if err != nil {
		return domain.Resource{}, fmt.Errorf("encode resource source: %w", err)
	}
	return hostingMutation(ctx, s, opts, func(q *sqlcgen.Queries) (domain.Resource, error) {
		row, err := q.CreateResource(ctx, sqlcgen.CreateResourceParams{ID: item.ID, ProjectID: item.ProjectID, ProjectEnvironmentID: item.ProjectEnvironmentID, Name: item.Name, Kind: item.Kind, Source: source, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
		if err != nil {
			return domain.Resource{}, err
		}
		return resourceFromHostingSQL(row)
	})
}

// UpdateResource uses a single compare-and-swap statement inside the audit transaction.
func (s *PostgresStore) UpdateResource(ctx context.Context, item domain.Resource, expectedGeneration int64, opts ...MutationOption) (domain.Resource, error) {
	if err := validateResource(item); err != nil {
		return domain.Resource{}, err
	}
	if expectedGeneration < 1 || expectedGeneration == 9223372036854775807 {
		return domain.Resource{}, ErrConflict
	}
	source, err := json.Marshal(item.Source)
	if err != nil {
		return domain.Resource{}, fmt.Errorf("encode resource source: %w", err)
	}
	return hostingMutation(ctx, s, opts, func(q *sqlcgen.Queries) (domain.Resource, error) {
		row, err := q.UpdateResource(ctx, sqlcgen.UpdateResourceParams{ID: item.ID, Name: item.Name, Source: source, UpdatedAt: item.UpdatedAt, Generation: expectedGeneration, ProjectID: item.ProjectID, ProjectEnvironmentID: item.ProjectEnvironmentID, Kind: item.Kind, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt})
		if errors.Is(err, pgx.ErrNoRows) {
			if _, getErr := q.GetResource(ctx, item.ID); getErr != nil {
				return domain.Resource{}, getErr
			}
			return domain.Resource{}, ErrConflict
		}
		if err != nil {
			return domain.Resource{}, err
		}
		return resourceFromHostingSQL(row)
	})
}
