package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

// ErrHostingBuildUnavailable means executable build admission is not configured.
var ErrHostingBuildUnavailable = errors.New("hosting build controller unavailable")

// HostingBuildEnqueueTx must enqueue real work in this transaction, with no
// external effects. The configured controller supplies the real River enqueuer.
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

// HostingBuildRepository persists immutable source/build intent and fenced receipts.
// Public admission uses the separately configured controller repository.
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

// HostingBuildControllerRepository is public admission and nonsecret runner status.
// Memory stores deliberately do not implement an executable controller.
type HostingBuildControllerRepository interface {
	HostingBuildRepository
	RequestConfiguredHostingBuild(context.Context, string, string, string, domain.HostingBuildRequest, domain.AuditEvent) (domain.HostingBuild, error)
	RequestPublicHostingBuild(context.Context, string, string, string, domain.HostingBuildRequest, domain.AuditEvent) (domain.HostingBuild, error)
	ListHostingBuilds(context.Context, string, string, Page) (PageResult[domain.HostingBuild], error)
	HostingBuildStatus(context.Context, string, domain.HostingBuildAuthority) (hostingbuildprotocol.StatusResponse, error)
}

// HostingSourceGrantRepository owns current authorization and atomic retry intent.
type HostingSourceGrantRepository interface {
	RequestHostingSourceGrant(context.Context, string, string, domain.HostingSourceGrantRequest, domain.AuditEvent) (domain.PublicHostingSourceGrant, error)
	ListHostingSourceGrants(context.Context, string, string, Page) (PageResult[domain.PublicHostingSourceGrant], error)
	RevokeResourceHostingSourceGrant(context.Context, string, string, string, domain.AuditEvent) error
}
