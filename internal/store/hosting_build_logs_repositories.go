package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingBuildLogRepository binds durable nonsecret output to typed build authority.
type HostingBuildLogRepository interface {
	AppendHostingBuildLogs(context.Context, string, domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error)
	ListHostingBuildLogs(context.Context, string, string, int, int) (domain.HostingBuildLogPage, error)
}

var _ HostingBuildLogRepository = (*PostgresStore)(nil)
var _ HostingBuildLogRepository = (*MemoryStore)(nil)
