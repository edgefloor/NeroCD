package store

import (
	"context"

	"nerocd/internal/domain"
)

// ServerReadinessRepository revalidates dedicated authority under ordered locks.
// Credential hashes are private and never included in diagnostic DTOs.
type ServerReadinessRepository interface {
	IssueServerReadiness(context.Context, string, string) (domain.ServerReadinessChallenge, error)
	ReportServerReadiness(context.Context, string, string, domain.ServerReadinessReport) (domain.ServerReadinessAck, error)
	GetServerReadiness(context.Context, string) (domain.ServerReadiness, error)
}

var _ ServerReadinessRepository = (*PostgresStore)(nil)
