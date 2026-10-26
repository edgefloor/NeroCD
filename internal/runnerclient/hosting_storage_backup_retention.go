package runnerclient

import (
	"context"
	"errors"
	"io"

	"nerocd/internal/domain"
)

type storageRetentionPlanResponse struct {
	value domain.HostingStorageBackupRetentionPlan
}

func (r *storageRetentionPlanResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return domain.ErrInvalidStorageBackupRetention
	}
	return domain.DecodeHostingStorageProtocolJSON(raw, &r.value)
}

type storageRetentionMutationResponse struct {
	value domain.HostingStorageBackupRetentionExecution
}

func (r *storageRetentionMutationResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 65537))
	if e != nil || len(raw) > 65536 {
		return domain.ErrInvalidStorageBackupRetention
	}
	return domain.DecodeHostingStorageProtocolJSON(raw, &r.value)
}

// HostingStorageBackupRetentionPlan fetches and validates a storage backup retention plan.
func (c Client) HostingStorageBackupRetentionPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingStorageBackupRetentionPlan, error) {
	var r storageRetentionPlanResponse
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-retention/plan", a, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("backup retention authority unavailable")
	}
	if r.value.Validate() != nil || r.value.Operation.ID != a.OperationID || a.RunID != "storage_retention_"+a.OperationID {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	return r.value, nil
}

// HostingStorageBackupRetentionMutation submits a receipt and validates the retained item state.
func (c Client) HostingStorageBackupRetentionMutation(ctx context.Context, in domain.HostingStorageBackupRetentionMutation) (domain.HostingStorageBackupRetentionExecution, error) {
	var r storageRetentionMutationResponse
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-retention/mutation", in, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("backup retention receipt unavailable")
	}
	if r.value.ID != in.OperationID || !domain.ValidStorageBackupDeletionOperation(r.value) {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	matched := false
	for _, v := range r.value.Items {
		if v.CopyID == in.CopyID {
			matched = v.State == in.Phase && v.FailureCode == in.FailureCode
			if in.ProbeOnly {
				matched = v.ReservationHeld && v.State != "deleted" && v.State != "absent" && v.State != "failed"
			}
		}
	}
	if !matched {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	return r.value, nil
}

type storageRetentionRetirementResponse struct {
	value domain.HostingStorageBackupRetentionRetirement
}

func (r *storageRetentionRetirementResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 4097))
	if e != nil || len(raw) > 4096 {
		return domain.ErrInvalidStorageBackupRetention
	}
	return domain.DecodeHostingStorageProtocolJSON(raw, &r.value)
}

// HostingStorageBackupRetentionRetirement requests and validates a replay-only continuation.
func (c Client) HostingStorageBackupRetentionRetirement(ctx context.Context, in domain.HostingStorageBackupRetentionRetirementInput) (domain.HostingStorageBackupRetentionRetirement, error) {
	var r storageRetentionRetirementResponse
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-retention/retirement", in, &r); e != nil {
		return r.value, errors.New("backup retention continuation unavailable")
	}
	if r.value.HostingAuthority != in.HostingAuthority || r.value.PlanHash != in.PlanHash || r.value.NextAttempt <= in.Attempt || !r.value.ReplayOnly {
		return r.value, domain.ErrInvalidStorageBackupRetention
	}
	return r.value, nil
}
