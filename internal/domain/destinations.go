package domain

import "time"

// Server gives one enrolled runner a stable hosting identity. It does not reserve scheduler capacity.
type Server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RunnerID  string    `json:"runner_id"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Destination identifies one immutable runtime target on a server.
type Destination struct {
	ID          string    `json:"id"`
	ServerID    string    `json:"server_id"`
	Name        string    `json:"name"`
	RuntimeKind string    `json:"runtime_kind"`
	CreatedAt   time.Time `json:"created_at"`
}

// DestinationProjectGrant records explicit project permission. Revocation retains existing bindings.
type DestinationProjectGrant struct {
	DestinationID string     `json:"destination_id"`
	ProjectID     string     `json:"project_id"`
	GrantedBy     string     `json:"granted_by"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// ResourceBinding preserves a resource's target and platform-generated runtime namespace.
type ResourceBinding struct {
	ID               string    `json:"id"`
	ResourceID       string    `json:"resource_id"`
	DestinationID    string    `json:"destination_id"`
	RuntimeNamespace string    `json:"runtime_namespace"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

// ResourceDestination resolves persisted identities, including current grant and runner evidence.
// It is not an execution grant: callers must check current authorization and runner eligibility before mutation.
type ResourceDestination struct {
	Binding     ResourceBinding         `json:"binding"`
	Destination Destination             `json:"destination"`
	Server      Server                  `json:"server"`
	Runner      Runner                  `json:"runner"`
	Grant       DestinationProjectGrant `json:"grant"`
}
