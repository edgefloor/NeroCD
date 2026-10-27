package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
)

// ErrRuntimeLogsExpired marks logical retention expiry independently of physical purge.
var ErrRuntimeLogsExpired = errors.New("runtime log session expired")

// RuntimeLogScope exposes no plaintext or collection authority.
type RuntimeLogScope struct{ ActorID, ResourceID string }

// HostingRuntimeLogsRepository owns bounded demand and immutable-target log sessions.
type HostingRuntimeLogsRepository interface {
	RuntimeLogTargets(context.Context, string, string) (domain.HostingRuntimeLogTargets, error)
	RuntimeLogSessionScope(context.Context, string, string) (RuntimeLogScope, error)
	OpenRuntimeLogSession(context.Context, string, string, domain.HostingRuntimeLogOpen) (domain.HostingRuntimeLogSession, error)
	RenewRuntimeLogSession(context.Context, string, string) (domain.HostingRuntimeLogSession, error)
	CloseRuntimeLogSession(context.Context, string, string) (domain.HostingRuntimeLogSession, error)
	RuntimeLogPage(context.Context, string, string, int, int) (domain.HostingRuntimeLogPage, error)
	ClaimRuntimeLogSessions(context.Context, string, string, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error)
	AppendRuntimeLogs(context.Context, string, string, string, domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error)
	PurgeRuntimeLogs(context.Context) (int, error)
}

var _ HostingRuntimeLogsRepository = (*PostgresStore)(nil)
