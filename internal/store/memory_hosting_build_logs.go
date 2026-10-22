package store

import (
	"context"
	"nerocd/internal/domain"
)

// AppendHostingBuildLogs requires durable PostgreSQL build authority.
func (s *MemoryStore) AppendHostingBuildLogs(context.Context, string, domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	return domain.HostingBuildLogAck{}, ErrHostingBuildUnavailable
}

// ListHostingBuildLogs requires durable PostgreSQL sequence history.
func (s *MemoryStore) ListHostingBuildLogs(context.Context, string, string, int, int) (domain.HostingBuildLogPage, error) {
	return domain.HostingBuildLogPage{}, ErrHostingBuildUnavailable
}
func (s *MemoryStore) buildLogRunLocked(id string) bool {
	for _, r := range s.runs {
		if r.ID == id {
			return domain.ContainsHostingBuildExecution(r.RunSpec, r.Workflow)
		}
	}
	return false
}
