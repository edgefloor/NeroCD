package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromSQLC(row))
	}
	return projects, nil
}

func (s *PostgresStore) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	inserted, err := s.queries.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(inserted), nil
}

func (s *PostgresStore) CreateProjectWithOwner(ctx context.Context, project domain.Project, owner domain.ProjectMember, audit domain.AuditEvent) (domain.Project, error) {
	if owner.ProjectID != project.ID || owner.UserID == "" || owner.Role != domain.RoleOwner {
		return domain.Project{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	inserted, err := q.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, err
	}
	if _, err = q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: owner.ID, ProjectID: owner.ProjectID, UserID: owner.UserID, Role: owner.Role, CreatedAt: owner.CreatedAt, UpdatedAt: owner.UpdatedAt}); err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(inserted), nil
}

func (s *PostgresStore) UpdateProject(ctx context.Context, project domain.Project, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		updated, err := s.queries.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
		if err == pgx.ErrNoRows {
			return domain.Project{}, ErrNotFound
		}
		if err != nil {
			return domain.Project{}, err
		}
		return projectFromSQLC(updated), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	updated, err := q.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(updated), nil
}

func (s *PostgresStore) ArchiveProject(ctx context.Context, id string, archivedAt time.Time, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		archived, err := s.queries.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
		if err == pgx.ErrNoRows {
			return domain.Project{}, ErrNotFound
		}
		if err != nil {
			return domain.Project{}, err
		}
		return projectFromSQLC(archived), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	archived, err := q.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(archived), nil
}

func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.queries.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, projectMemberListRowFromSQLC(row))
	}
	return members, nil
}

func (s *PostgresStore) UpsertProjectMember(ctx context.Context, member domain.ProjectMember, opts ...MutationOption) (domain.ProjectMember, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		upserted, err := s.queries.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
		if err != nil {
			return domain.ProjectMember{}, err
		}
		user, err := s.queries.GetUserByID(ctx, upserted.UserID)
		if err == pgx.ErrNoRows {
			return domain.ProjectMember{}, ErrNotFound
		}
		if err != nil {
			return domain.ProjectMember{}, err
		}
		return projectMemberFromSQLC(upserted, user), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	upserted, err := q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	user, err := q.GetUserByID(ctx, upserted.UserID)
	if err == pgx.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, err
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.ProjectMember{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProjectMember{}, err
	}
	return projectMemberFromSQLC(upserted, user), nil
}
