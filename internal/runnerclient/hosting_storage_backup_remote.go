package runnerclient

import (
	"context"
	"errors"
	"io"

	"nerocd/internal/domain"
)

type storageRemotePlanResponse struct {
	value domain.HostingStorageBackupTransferPlan
}

func (r *storageRemotePlanResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 65537))
	if e != nil || len(raw) > 65536 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidStorageBackupRemote
	}
	return nil
}

type storageRemoteMutationResponse struct {
	value domain.HostingStorageBackupTransfer
}

func (r *storageRemoteMutationResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 16385))
	if e != nil || len(raw) > 16384 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidStorageBackupRemote
	}
	return nil
}
// HostingStorageBackupTransferPlan fetches and validates a storage backup transfer plan.
func (c Client) HostingStorageBackupTransferPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingStorageBackupTransferPlan, error) {
	var response storageRemotePlanResponse
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-transfer/plan", a, &response); e != nil {
		if ctx.Err() != nil {
			return response.value, ctx.Err()
		}
		return response.value, errors.New("remote backup authority unavailable")
	}
	p := response.value
	if p.Validate() != nil || p.Operation.ID != a.OperationID || a.RunID != "storage_transfer_"+a.OperationID {
		return p, domain.ErrInvalidStorageBackupRemote
	}
	return p, nil
}
// HostingStorageBackupTransferMutation submits a receipt and validates the transfer outcome.
func (c Client) HostingStorageBackupTransferMutation(ctx context.Context, in domain.HostingStorageBackupTransferMutation) (domain.HostingStorageBackupTransfer, error) {
	var response storageRemoteMutationResponse
	if in.Validate() != nil {
		return response.value, domain.ErrInvalidStorageBackupRemote
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-transfer/mutation", in, &response); e != nil {
		if ctx.Err() != nil {
			return response.value, ctx.Err()
		}
		return response.value, errors.New("remote backup receipt request failed")
	}
	out := response.value
	expected := map[string]string{"intent": "applying", "complete": "succeeded", "failed": "failed", "uncertain": "uncertain"}[in.Phase]
	if out.Validate() != nil || out.ID != in.OperationID || out.State != expected || out.FailureCode != in.FailureCode {
		return out, domain.ErrInvalidStorageBackupRemote
	}
	return out, nil
}
