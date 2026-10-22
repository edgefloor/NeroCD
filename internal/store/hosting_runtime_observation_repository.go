package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingRuntimeObservationRepository requires current target authority under locks.
// Credential hashes are private request authority, never public observation data.
type HostingRuntimeObservationRepository interface {
	HostingObservationTargets(context.Context, string, string, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error)
	ReportHostingObservation(context.Context, string, string, domain.HostingRuntimeObservation) (domain.HostingObservationAck, error)
	HostingRuntimeStatus(context.Context, string, string) (domain.HostingRuntimeStatus, error)
}

var _ HostingRuntimeObservationRepository = (*PostgresStore)(nil)
