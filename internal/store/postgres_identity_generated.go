package store

import (
	"database/sql"
	"time"

	"github.com/lib/pq"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
	bobqueries "nerocd/internal/store/bobgen/queries"
)

func userFromGenerated(user *bobmodels.User) domain.User {
	return domain.User{
		ID:           user.ID,
		Email:        user.Email,
		Name:         user.Name,
		Status:       user.Status,
		GlobalRole:   user.GlobalRole,
		PasswordHash: user.PasswordHash,
		CreatedAt:    user.CreatedAt,
	}
}

func userFromPrincipalRow(row bobqueries.PrincipalBySessionTokenHashRow) domain.User {
	return domain.User{
		ID:           row.ID,
		Email:        row.Email,
		Name:         row.Name,
		Status:       row.Status,
		GlobalRole:   row.GlobalRole,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
	}
}

func sessionSetter(session domain.Session, tokenHash string) *bobmodels.SessionSetter {
	return &bobmodels.SessionSetter{
		ID:        &session.ID,
		UserID:    &session.UserID,
		TokenHash: &tokenHash,
		ExpiresAt: &session.ExpiresAt,
		CreatedAt: &session.CreatedAt,
	}
}

func apiTokenSetter(token domain.APIToken) *bobmodels.APITokenSetter {
	roles := pq.StringArray(token.Roles)
	expiresAt := nullTime(token.ExpiresAt)
	return &bobmodels.APITokenSetter{
		ID:        &token.ID,
		Name:      &token.Name,
		Kind:      &token.Kind,
		TokenHash: &token.TokenHash,
		Roles:     &roles,
		Status:    &token.Status,
		CreatedBy: &token.CreatedBy,
		CreatedAt: &token.CreatedAt,
		ExpiresAt: &expiresAt,
	}
}

func apiTokenFromGenerated(token *bobmodels.APIToken) domain.APIToken {
	return domain.APIToken{
		ID:         token.ID,
		Name:       token.Name,
		Kind:       token.Kind,
		TokenHash:  token.TokenHash,
		Roles:      append([]string(nil), token.Roles...),
		Status:     token.Status,
		CreatedBy:  token.CreatedBy,
		CreatedAt:  token.CreatedAt,
		ExpiresAt:  timePtr(token.ExpiresAt),
		LastUsedAt: timePtr(token.LastUsedAt),
		RevokedAt:  timePtr(token.RevokedAt),
	}
}

func nullTime(value *time.Time) sql.Null[time.Time] {
	if value == nil {
		return sql.Null[time.Time]{}
	}
	return sql.Null[time.Time]{V: *value, Valid: true}
}

func timePtr(value sql.Null[time.Time]) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.V
}
