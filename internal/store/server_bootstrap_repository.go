package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
)

// ServerBootstrapSecretGrant authorizes only this principal and pinned target.
// It is independent of ordinary managed-secret reveal grants.
type ServerBootstrapSecretGrant struct {
	OperationID   string
	PrincipalID   string
	TargetHash    string
	SecretID      string
	SecretVersion int64
	ExpiresAt     time.Time
}

// ServerBootstrapDelivery must never be serialized or logged. Callers own and
// discard returned byte slices after the bounded transport call. Go does not guarantee erasure.
type ServerBootstrapDelivery struct {
	Operation       domain.ServerBootstrapOperation `json:"-"`
	Stage           domain.ServerBootstrapStage     `json:"-"`
	SSHPrivateKey   []byte                          `json:"-"`
	EnrollmentToken []byte                          `json:"-"`
}

// ServerBootstrapRepository is durable control only; it performs no remote I/O.
type ServerBootstrapRepository interface {
	CreateServerBootstrap(context.Context, string, string, domain.ServerBootstrapInput, domain.AuditEvent) (domain.ServerBootstrapOperation, error)
	GetServerBootstrap(context.Context, string, string) (domain.ServerBootstrapOperation, error)
	RequestServerBootstrapCommand(context.Context, string, string, domain.ServerBootstrapCommand, domain.AuditEvent) (domain.ServerBootstrapCommand, error)
	ClaimServerBootstrapStage(context.Context, string, string, string) (domain.ServerBootstrapStage, error)
	RenewServerBootstrapStage(context.Context, string, string, domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error)
	AuthorizeServerBootstrapDelivery(context.Context, string, string, domain.ServerBootstrapStage, *secretcrypto.Keyring, domain.AuditEvent) (ServerBootstrapDelivery, error)
	CompleteServerBootstrapStage(context.Context, string, string, domain.ServerBootstrapReceipt, domain.AuditEvent) (domain.ServerBootstrapOperation, error)
}
