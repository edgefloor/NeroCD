package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"nerocd/internal/domain"
)

type hostingOperationResponse domain.HostingOperation

func (r *hostingOperationResponse) decodeResponse(body io.Reader) error {
	contents, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(contents) > 1<<20 {
		return errors.New("hosting response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err = decoder.Decode(r); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("hosting response requires one JSON document")
	}
	op := domain.HostingOperation(*r)
	if op.ApplicationDatabase != nil || op.DatabaseMaintenance != nil || op.Snapshot.ApplicationDatabase != (domain.HostingApplicationDatabaseRef{}) || op.Plan != nil && op.Plan.ApplicationDatabase != nil {
		var closed domain.HostingOperation
		if decodeConnectionResponse(contents, &closed) != nil || !connectionOperationValid(closed) {
			return errDatabaseConnection
		}
		*r = hostingOperationResponse(closed)
		return nil
	}
	return domain.ValidateHostingDatabaseUpgradeOperation(op)
}
func (c Client) hostingPost(ctx context.Context, path string, in any) (domain.HostingOperation, error) {
	var response hostingOperationResponse
	if err := c.post(ctx, "/api/v1/runners/hosting/"+path, in, &response); err != nil {
		return domain.HostingOperation{}, err
	}
	return domain.HostingOperation(response), nil
}

// HostingPlan requests a fenced immutable execution plan.
func (c Client) HostingPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "plan", in)
}

// HostingPrepared submits a stable preparation receipt.
func (c Client) HostingPrepared(ctx context.Context, in domain.HostingPreparedRequest) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "prepared", in)
}

// HostingApplyBegin requests durable admission before workload mutation.
func (c Client) HostingApplyBegin(ctx context.Context, in domain.HostingAuthority) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "apply-begin", in)
}

// HostingComplete submits a stable durable execution result.
func (c Client) HostingComplete(ctx context.Context, in domain.HostingCompleteRequest) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "complete", in)
}

// HostingStatus reads fenced state or requests observation authority.
func (c Client) HostingStatus(ctx context.Context, in domain.HostingStatusRequest) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "status", in)
}

// HostingReconciliation submits exact-target read-only evidence.
func (c Client) HostingReconciliation(ctx context.Context, in domain.HostingObservation) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "reconciliation", in)
}

// HostingStackVolumes acknowledges nonsecret creation provenance before stack writers start.
func (c Client) HostingStackVolumes(ctx context.Context, in domain.HostingStackVolumeRequest) (domain.HostingOperation, error) {
	return c.hostingPost(ctx, "stack-volumes", in)
}
