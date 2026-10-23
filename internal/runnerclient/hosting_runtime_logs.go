package runnerclient

import (
	"context"
	"errors"
	"io"
	"math"
	"nerocd/internal/domain"
)

// runtimeLogResponse keeps the log response contract independent of observation
// DTOs while enforcing the same closed JSON fields as the log API boundary.
type runtimeLogResponse[T domain.HostingRuntimeLogClaims | domain.HostingRuntimeLogAck] struct {
	value T
	limit int
}

func (r *runtimeLogResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, int64(r.limit)+1))
	if err != nil {
		return err
	}
	if len(raw) > r.limit {
		return errors.New("runtime log response exceeds limit")
	}
	return domain.DecodeHostingRuntimeLogsJSON(raw, &r.value)
}

// ClaimRuntimeLogSessions renews only process-local collection leases, never demand.
func (c Client) ClaimRuntimeLogSessions(ctx context.Context, in domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingRuntimeLogClaims{}, err
	}
	response := runtimeLogResponse[domain.HostingRuntimeLogClaims]{limit: 64 << 10}
	err := c.post(ctx, "/api/v1/runners/runtime-log-sessions/claim", in, &response)
	if err != nil {
		return domain.HostingRuntimeLogClaims{}, err
	}
	if len(response.value.Targets) > in.Limit {
		return domain.HostingRuntimeLogClaims{}, errors.New("runtime log claim exceeds limit")
	}
	for _, target := range response.value.Targets {
		if target.InstanceID != in.InstanceID || target.SessionID == "" || target.CollectionEpoch == "" || target.EpochExpiresAt.IsZero() || target.NextSequence < 1 || target.NextBatchSequence < 1 || target.NextBatchSequence > math.MaxInt32 || target.Target.Validate() != nil || target.EpochExpiresAt.After(target.DemandExpiresAt) {
			return domain.HostingRuntimeLogClaims{}, errors.New("runtime log claim identity mismatch")
		}
	}
	return response.value, nil
}

// AppendRuntimeLogs verifies an acknowledgment against canonical submitted content.
func (c Client) AppendRuntimeLogs(ctx context.Context, in domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
	in, err := in.Canonical()
	if err != nil {
		return domain.HostingRuntimeLogAck{}, err
	}
	response := runtimeLogResponse[domain.HostingRuntimeLogAck]{limit: 8 << 10}
	if err = c.post(ctx, "/api/v1/runners/runtime-log-sessions/events", in, &response); err != nil {
		return domain.HostingRuntimeLogAck{}, err
	}
	ack := response.value
	first := 0
	if len(in.Events) > 0 {
		first = in.ExpectedSequence + 1
	}
	if ack.SessionID != in.SessionID || ack.InstanceID != in.InstanceID || ack.CollectionEpoch != in.CollectionEpoch || ack.BatchID != in.BatchID || ack.BatchSequence != in.BatchSequence || ack.BodyHash != domain.HostingDocumentHash(in) || ack.FirstSequence != first || ack.LastSequence != in.ExpectedSequence+len(in.Events) || ack.ReceivedAt.IsZero() {
		return domain.HostingRuntimeLogAck{}, errors.New("runtime log acknowledgment mismatch")
	}
	return ack, nil
}
