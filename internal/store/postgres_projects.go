package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListProjects implements the corresponding repository operation.
func (s *PostgresStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects query: %w", err)
	}
	projects := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromSQLC(row))
	}
	return projects, nil
}

// CreateProject implements the corresponding repository operation.
func (s *PostgresStore) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	inserted, err := s.queries.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, fmt.Errorf("create project query: %w", err)
	}
	return projectFromSQLC(inserted), nil
}

// CreateProjectWithOwner implements the corresponding repository operation.
func (s *PostgresStore) CreateProjectWithOwner(ctx context.Context, project domain.Project, owner domain.ProjectMember, audit domain.AuditEvent) (domain.Project, error) {
	if owner.ProjectID != project.ID || owner.UserID == "" || owner.Role != domain.RoleOwner {
		return domain.Project{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	inserted, err := q.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, fmt.Errorf("create project query: %w", err)
	}
	if _, err = q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: owner.ID, ProjectID: owner.ProjectID, UserID: owner.UserID, Role: owner.Role, CreatedAt: owner.CreatedAt, UpdatedAt: owner.UpdatedAt}); err != nil {
		return domain.Project{}, fmt.Errorf("upsert project member query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Project{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit transaction: %w", err)
	}
	return projectFromSQLC(inserted), nil
}

// UpdateProject implements the corresponding repository operation.
func (s *PostgresStore) UpdateProject(ctx context.Context, project domain.Project, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		updated, err := s.queries.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
		if err == pgx.ErrNoRows {
			return domain.Project{}, ErrNotFound
		}
		if err != nil {
			return domain.Project{}, fmt.Errorf("update project query: %w", err)
		}
		return projectFromSQLC(updated), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	updated, err := q.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, fmt.Errorf("update project query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Project{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit transaction: %w", err)
	}
	return projectFromSQLC(updated), nil
}

// ArchiveProject implements the corresponding repository operation.
func (s *PostgresStore) ArchiveProject(ctx context.Context, id string, archivedAt time.Time, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		archived, err := s.queries.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
		if err == pgx.ErrNoRows {
			return domain.Project{}, ErrNotFound
		}
		if err != nil {
			return domain.Project{}, fmt.Errorf("archive project query: %w", err)
		}
		return projectFromSQLC(archived), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	archived, err := q.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, fmt.Errorf("archive project query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Project{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit transaction: %w", err)
	}
	return projectFromSQLC(archived), nil
}

// ListProjectMembers implements the corresponding repository operation.
func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.queries.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project members query: %w", err)
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, projectMemberListRowFromSQLC(row))
	}
	return members, nil
}

// UpsertProjectMember implements the corresponding repository operation.
func (s *PostgresStore) UpsertProjectMember(ctx context.Context, member domain.ProjectMember, opts ...MutationOption) (domain.ProjectMember, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		upserted, err := s.queries.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
		if err != nil {
			return domain.ProjectMember{}, fmt.Errorf("upsert project member query: %w", err)
		}
		user, err := s.queries.GetUserByID(ctx, upserted.UserID)
		if err == pgx.ErrNoRows {
			return domain.ProjectMember{}, ErrNotFound
		}
		if err != nil {
			return domain.ProjectMember{}, fmt.Errorf("get user by id query: %w", err)
		}
		return projectMemberFromSQLC(upserted, user), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ProjectMember{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	upserted, err := q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
	if err != nil {
		return domain.ProjectMember{}, fmt.Errorf("upsert project member query: %w", err)
	}
	user, err := q.GetUserByID(ctx, upserted.UserID)
	if err == pgx.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, fmt.Errorf("get user by id query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.ProjectMember{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProjectMember{}, fmt.Errorf("commit transaction: %w", err)
	}
	return projectMemberFromSQLC(upserted, user), nil
}
