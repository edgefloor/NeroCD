package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"time"
)

// ErrHostingDatabaseNetworkCaptureUnavailable fails closed without the capture schema.
var ErrHostingDatabaseNetworkCaptureUnavailable = errors.New("database network capture requires compatible PostgreSQL metadata")

// HostingDatabaseNetworkCaptureRepository uses durable tasks/leases exclusively
// for read-only origin attestation. It does not enable a runtime adapter.
type HostingDatabaseNetworkCaptureRepository interface {
	RequestHostingDatabaseNetworkCapture(context.Context, string, string, domain.AuditEvent) (domain.HostingDatabaseNetworkCapture, error)
	ClaimHostingDatabaseNetworkCapture(context.Context, string, string, string, time.Duration) (domain.HostingDatabaseNetworkCaptureClaim, error)
	HostingDatabaseNetworkCapturePlan(context.Context, string, string, domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error)
	RecordHostingDatabaseNetworkOrigin(context.Context, string, string, domain.HostingDatabaseNetworkOriginRequest) (domain.HostingDatabaseNetworkOriginReceipt, error)
}

var _ HostingDatabaseNetworkCaptureRepository = (*PostgresStore)(nil)

// HostingDatabaseNetworkCaptureClaimRepository selects existing queued captures
// without requiring the runner to know their controller-assigned IDs.
type HostingDatabaseNetworkCaptureClaimRepository interface {
	ClaimNextHostingDatabaseNetworkCapture(context.Context, string, string, time.Duration) (domain.HostingDatabaseNetworkCaptureClaim, error)
}

var _ HostingDatabaseNetworkCaptureClaimRepository = (*PostgresStore)(nil)
