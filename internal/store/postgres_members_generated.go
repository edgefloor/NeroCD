package store

import (
	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
	bobqueries "nerocd/internal/store/bobgen/queries"
)

func projectMemberSetter(member domain.ProjectMember) *bobmodels.ProjectMemberSetter {
	return &bobmodels.ProjectMemberSetter{
		ID:        &member.ID,
		ProjectID: &member.ProjectID,
		UserID:    &member.UserID,
		Role:      &member.Role,
		CreatedAt: &member.CreatedAt,
		UpdatedAt: &member.UpdatedAt,
	}
}

func projectMemberFromGeneratedModel(member *bobmodels.ProjectMember, user *bobmodels.User) domain.ProjectMember {
	return domain.ProjectMember{
		ID:        member.ID,
		ProjectID: member.ProjectID,
		UserID:    member.UserID,
		Email:     user.Email,
		Name:      user.Name,
		Role:      member.Role,
		CreatedAt: member.CreatedAt,
		UpdatedAt: member.UpdatedAt,
	}
}

func projectMemberFromGenerated(row bobqueries.ProjectMembersRow) domain.ProjectMember {
	return domain.ProjectMember{
		ID:        row.ID,
		ProjectID: row.ProjectID,
		UserID:    row.UserID,
		Email:     row.Email,
		Name:      row.Name,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func projectMemberFromGeneratedByProject(row bobqueries.ProjectMembersByProjectRow) domain.ProjectMember {
	return domain.ProjectMember{
		ID:        row.ID,
		ProjectID: row.ProjectID,
		UserID:    row.UserID,
		Email:     row.Email,
		Name:      row.Name,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
