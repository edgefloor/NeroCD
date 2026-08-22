package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	rows, err := s.queries.ListTemplates(ctx, projectID)
	if err != nil {
		return nil, err
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

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (domain.TaskTemplate, error) {
	template, err := s.queries.GetTemplate(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(template)
}

func (s *PostgresStore) CreateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	inserted, err := s.queries.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(inserted)
}
func (s *PostgresStore) CreateTemplateWithAudit(ctx context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(row)
}

func (s *PostgresStore) UpdateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	updated, err := s.queries.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(updated)
}
func (s *PostgresStore) UpdateTemplateWithAudit(ctx context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(row)
}
