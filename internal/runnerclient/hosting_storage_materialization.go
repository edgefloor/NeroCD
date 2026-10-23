package runnerclient

import (
	"context"
	"errors"
	"io"
	"maps"

	"nerocd/internal/domain"
)

type storageProtocolResponse[T any] struct{ value T }

func (r *storageProtocolResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, domain.HostingStorageProtocolJSONLimit+1))
	if err != nil {
		return err
	}
	return domain.DecodeHostingStorageProtocolJSON(raw, &r.value)
}

// HostingStorageCreateBegin requests durable admission. CreatePermitted is
// ephemeral and must never be saved as permission for a later create attempt.
func (c Client) HostingStorageCreateBegin(ctx context.Context, in domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	var out storageProtocolResponse[domain.HostingStorageCreateAck]
	if err := in.Validate(); err != nil {
		return out.value, err
	}
	if err := c.post(ctx, "/api/v1/runners/hosting/storage-create-begin", in, &out); err != nil {
		return domain.HostingStorageCreateAck{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.HostingStorageCreateAck{}, err
	}
	ack, m := out.value, out.value.Materialization
	if ack.Storage.Validate() != nil || m.Validate(ack.Storage) != nil || ack.Storage.Reference() != in.StorageRef || m.CreationNonce != in.CreationNonce || m.CreationOperationID == in.OperationID && m.CreationPlanHash != in.PlanHash || ack.CreatePermitted && (m.State != "create_admitted" || m.CreationOperationID != in.OperationID || m.CreationPlanHash != in.PlanHash) {
		return domain.HostingStorageCreateAck{}, errors.New("storage admission acknowledgment identity mismatch")
	}
	return ack, nil
}

// HostingStorageMaterialized sends stable nonsecret evidence and checks its
// committed receipt. This acknowledgment alone grants no application authority.
func (c Client) HostingStorageMaterialized(ctx context.Context, in domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	var out storageProtocolResponse[domain.HostingStorageMaterializedAck]
	if err := in.Validate(); err != nil {
		return out.value, err
	}
	if err := c.post(ctx, "/api/v1/runners/hosting/storage-materialized", in, &out); err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	ack, m := out.value, out.value.Materialization
	if m.State != "materialized" || m.MaterializedReceipt == nil || m.MaterializedReceipt.ID == "" || m.MaterializedReceipt.BodyHash == "" || m.MaterializedReceipt.CreatedAt.IsZero() || m.StorageRef != in.StorageRef || m.CreationOperationID != in.CreationOperationID || m.CreationPlanHash != in.CreationPlanHash || m.CreationNonce != in.CreationNonce || m.DaemonID != in.DaemonID || !maps.Equal(m.Labels, in.Labels) || ack.Receipt.ID != in.ReceiptID || ack.Receipt.BodyHash != domain.HostingDocumentHash(in) || ack.Receipt.CreatedAt.IsZero() {
		return domain.HostingStorageMaterializedAck{}, errors.New("storage materialization acknowledgment identity mismatch")
	}
	return ack, nil
}
