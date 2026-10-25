package runnerclient

import (
	"context"
	"errors"
	"io"

	"nerocd/internal/domain"
)

type backupRetentionPlanResponse struct {
	value domain.HostingDatabaseBackupRetentionPlan
}

func (r *backupRetentionPlanResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return domain.ErrInvalidDatabaseBackupRetention
	}
	return domain.DecodeHostingDatabaseBackupJSON(raw, &r.value)
}

type backupRetentionMutationResponse struct {
	value domain.HostingDatabaseBackupRetentionExecution
}

func (r *backupRetentionMutationResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 65537))
	if e != nil || len(raw) > 65536 {
		return domain.ErrInvalidDatabaseBackupRetention
	}
	return domain.DecodeHostingDatabaseBackupJSON(raw, &r.value)
}
func (c Client) HostingDatabaseBackupRetentionPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupRetentionPlan, error) {
	var r backupRetentionPlanResponse
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-retention/plan", a, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("backup retention authority unavailable")
	}
	if r.value.Validate() != nil || r.value.Operation.ID != a.OperationID || a.RunID != "db_retention_"+a.OperationID {
		return r.value, domain.ErrInvalidDatabaseBackupRetention
	}
	return r.value, nil
}
func (c Client) HostingDatabaseBackupRetentionMutation(ctx context.Context, in domain.HostingDatabaseBackupRetentionMutation) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var r backupRetentionMutationResponse
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidDatabaseBackupRetention
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-retention/mutation", in, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("backup retention receipt unavailable")
	}
	if r.value.ID != in.OperationID || r.value.Version != domain.HostingDatabaseBackupRetentionVersion {
		return r.value, domain.ErrInvalidDatabaseBackupRetention
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
		return r.value, domain.ErrInvalidDatabaseBackupRetention
	}
	return r.value, nil
}

type backupRetentionRetirementResponse struct {
	value domain.HostingDatabaseBackupRetentionRetirement
}

func (r *backupRetentionRetirementResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 4097))
	if e != nil || len(raw) > 4096 {
		return domain.ErrInvalidDatabaseBackupRetention
	}
	return domain.DecodeHostingDatabaseBackupJSON(raw, &r.value)
}
func (c Client) HostingDatabaseBackupRetentionRetirement(ctx context.Context, in domain.HostingDatabaseBackupRetentionRetirementInput) (domain.HostingDatabaseBackupRetentionRetirement, error) {
	var r backupRetentionRetirementResponse
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidDatabaseBackupRetention
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-retention/retirement", in, &r); e != nil {
		return r.value, errors.New("backup retention continuation unavailable")
	}
	if r.value.HostingAuthority != in.HostingAuthority || r.value.PlanHash != in.PlanHash || r.value.NextAttempt <= in.Attempt || !r.value.ReplayOnly {
		return r.value, domain.ErrInvalidDatabaseBackupRetention
	}
	return r.value, nil
}
