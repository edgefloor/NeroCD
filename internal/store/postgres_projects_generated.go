package store

import (
	"database/sql"
	"time"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
)

func projectSetter(project domain.Project) *bobmodels.ProjectSetter {
	return &bobmodels.ProjectSetter{
		ID:          &project.ID,
		Name:        &project.Name,
		Description: &project.Description,
		CreatedAt:   &project.CreatedAt,
	}
}

func projectFromGeneratedModel(project *bobmodels.Project) domain.Project {
	return projectFromGeneratedRow(project.ID, project.Name, project.Description, project.CreatedAt, project.ArchivedAt)
}

func projectFromGeneratedRow(id, name, description string, createdAt time.Time, archivedAt sql.Null[time.Time]) domain.Project {
	project := domain.Project{
		ID:          id,
		Name:        name,
		Description: description,
		CreatedAt:   createdAt,
	}
	if archivedAt.Valid {
		project.ArchivedAt = &archivedAt.V
	}
	return project
}
