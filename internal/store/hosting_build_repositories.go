package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

// ErrHostingBuildUnavailable means no reviewed DB-only controller callback exists.
var ErrHostingBuildUnavailable = errors.New("hosting build controller unavailable")

// HostingBuildEnqueueTx must enqueue real work in this transaction, with no
// external effects. This package provides no River kind or consumer.
type HostingBuildEnqueueTx func(context.Context, pgx.Tx, domain.HostingBuild) error

// HostingBuildDispatchTx creates the canonical new internal task in this same
// transaction and returns its ID. The repository verifies task body and target
// before committing. It must not attach an existing public run or do external IO.
type HostingBuildDispatchTx func(context.Context, pgx.Tx, domain.HostingBuild) (string, error)

// HostingBuildCompletion records fixed metadata; failure codes never contain stderr.
type HostingBuildCompletion struct {
	State       string                       `json:"state"`
	FailureCode string                       `json:"failure_code,omitempty"`
	Artifact    *domain.HostingBuildArtifact `json:"artifact,omitempty"`
}

// HostingBuildRepository is an inert persistence seam until controllers, public
// authorization, typed runner admission, and executors are independently wired.
type HostingBuildRepository interface {
	GetHostingBuildSource(context.Context, string, string) (domain.HostingGitSource, error)
	CreateHostingSourceGrant(context.Context, string, domain.HostingSourceGrant, domain.AuditEvent) (domain.HostingSourceGrant, error)
	RevokeHostingSourceGrant(context.Context, string, string, domain.AuditEvent) error
	RequestHostingBuild(context.Context, string, string, string, domain.HostingBuildInput, domain.HostingBuildPolicy, HostingBuildEnqueueTx, domain.AuditEvent) (domain.HostingBuild, error)
	DispatchHostingBuild(context.Context, string, HostingBuildDispatchTx, domain.AuditEvent) (domain.HostingBuild, error)
	GetHostingBuild(context.Context, string, string) (domain.HostingBuild, error)
	CancelHostingBuild(context.Context, string, string, int64, domain.AuditEvent) (domain.HostingBuild, error)
	BeginHostingBuildSource(context.Context, string, domain.HostingBuildAuthority, int64) (domain.HostingBuild, error)
	CommitHostingBuildSource(context.Context, string, domain.HostingBuildAuthority, string, domain.HostingBuildSourceReceipt) (domain.HostingBuild, error)
	PrepareHostingBuild(context.Context, string, domain.HostingBuildAuthority, int64, string) (domain.HostingBuild, error)
	CompleteHostingBuild(context.Context, string, domain.HostingBuildAuthority, string, int64, HostingBuildCompletion) (domain.HostingBuild, error)
	ResolveCurrentHostingBuildArtifact(context.Context, string, string, string) (domain.HostingBuildArtifact, error)
}

var _ HostingBuildRepository = (*PostgresStore)(nil)
