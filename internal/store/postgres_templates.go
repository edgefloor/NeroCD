package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListTemplates implements the corresponding repository operation.
func (s *PostgresStore) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	rows, err := s.queries.ListTemplates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list templates query: %w", err)
	}
	templates := make([]domain.TaskTemplate, 0, len(rows))
	for _, row := range rows {
		template, err := taskTemplateFromSQLC(row)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	return templates, nil
}

// GetTemplate implements the corresponding repository operation.
func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (domain.TaskTemplate, error) {
	template, err := s.queries.GetTemplate(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("get template query: %w", err)
	}
	return taskTemplateFromSQLC(template)
}

// CreateTemplate implements the corresponding repository operation.
func (s *PostgresStore) CreateTemplate(ctx context.Context, template domain.TaskTemplate, opts ...MutationOption) (domain.TaskTemplate, error) {
	audit := resolveMutationOptions(opts)
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("encode template: %w", err)
	}
	if audit == nil {
		inserted, err := s.queries.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
		if err != nil {
			return domain.TaskTemplate{}, fmt.Errorf("create template query: %w", err)
		}
		return taskTemplateFromSQLC(inserted)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("create template query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("commit transaction: %w", err)
	}
	return taskTemplateFromSQLC(row)
}

// UpdateTemplate implements the corresponding repository operation.
func (s *PostgresStore) UpdateTemplate(ctx context.Context, template domain.TaskTemplate, opts ...MutationOption) (domain.TaskTemplate, error) {
	audit := resolveMutationOptions(opts)
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("encode template: %w", err)
	}
	if audit == nil {
		updated, err := s.queries.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
		if err == pgx.ErrNoRows {
			return domain.TaskTemplate{}, ErrNotFound
		}
		if err != nil {
			return domain.TaskTemplate{}, fmt.Errorf("update template query: %w", err)
		}
		return taskTemplateFromSQLC(updated)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("update template query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, fmt.Errorf("commit transaction: %w", err)
	}
	return taskTemplateFromSQLC(row)
}
