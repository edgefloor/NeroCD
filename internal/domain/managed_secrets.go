package domain

import "time"

// ManagedSecret is project-owned metadata. Values and encryption details are never metadata.
type ManagedSecret struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	Name           string     `json:"name"`
	CurrentVersion int64      `json:"current_version"`
	Status         string     `json:"status"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// ManagedSecretVersion describes an immutable version without its encrypted value.
type ManagedSecretVersion struct {
	SecretID  string    `json:"secret_id"`
	Version   int64     `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}
