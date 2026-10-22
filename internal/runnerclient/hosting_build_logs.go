package runnerclient

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

// BuildEvents submits a canonical bounded batch through the dedicated build
// endpoint. It never retries or accepts a partial or mismatched acknowledgment.
func (c Client) BuildEvents(ctx context.Context, in domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	var zero domain.HostingBuildLogAck
	canonical, err := domain.CanonicalHostingBuildLogs(in)
	if err != nil {
		return zero, err
	}
	if err = hostingbuildprotocol.ValidateAuthority(canonical.Authority); err != nil {
		return zero, err
	}
	ack, err := buildPost[domain.HostingBuildLogAck](ctx, c, "events", canonical)
	if err != nil {
		if errors.Is(err, errBuildResponse) {
			return zero, errBuildResponse
		}
		return zero, err
	}
	if hostingbuildprotocol.ValidateLogAcknowledgment(canonical, ack) != nil {
		return zero, errBuildResponse
	}
	return ack, nil
}
