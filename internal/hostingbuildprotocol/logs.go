package hostingbuildprotocol

import (
	"errors"
	"math"

	"nerocd/internal/domain"
)

// ValidateLogAcknowledgment binds every ordered receipt to canonical submitted
// content. Server sequence allocation is positive, bounded and strictly ordered.
func ValidateLogAcknowledgment(batch domain.HostingBuildLogBatch, ack domain.HostingBuildLogAck) error {
	canonical, err := domain.CanonicalHostingBuildLogs(batch)
	if err != nil || ValidateAuthority(batch.Authority) != nil || ack.Authority != batch.Authority || len(ack.Receipts) != len(batch.Events) {
		return errors.New("invalid build log acknowledgment")
	}
	previous := 0
	for i, event := range canonical.Events {
		r := ack.Receipts[i]
		if r.EventKey != event.EventKey || r.RequestedSequence != event.Sequence || r.BodyHash != domain.HostingBuildLogBodyHash(canonical.Authority, event) || r.Sequence <= previous || r.Sequence > math.MaxInt32 {
			return errors.New("build log receipt mismatch")
		}
		previous = r.Sequence
	}
	return nil
}
