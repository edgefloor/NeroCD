package runnerclient

import (
	"context"
	"errors"
	"io"
	"nerocd/internal/domain"
)

type databaseImportResponse[T any] struct{ value T }

func (r *databaseImportResponse[T]) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidDatabaseImport
	}
	return nil
}

// HostingDatabaseImportStagePlan fetches exact fenced inspection authority.
func (c Client) HostingDatabaseImportStagePlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error) {
	var r databaseImportResponse[domain.HostingDatabaseImportStagePlan]
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-import-stage/plan", a, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("external inspection authority unavailable")
	}
	p := r.value
	if p.Validate() != nil || a.OperationID != p.Stage.ID || a.RunID != "db_import_stage_"+p.Stage.ID {
		return p, domain.ErrInvalidDatabaseImport
	}
	return p, nil
}

// HostingDatabaseImportPlan fetches only admitted local stage consumption authority.
func (c Client) HostingDatabaseImportPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseImportPlan, error) {
	var r databaseImportResponse[domain.HostingDatabaseImportPlan]
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-import/plan", a, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("external import authority unavailable")
	}
	p := r.value
	if p.Validate() != nil || a.OperationID != p.Operation.ID || a.RunID != "db_import_"+p.Operation.ID {
		return p, domain.ErrInvalidDatabaseImport
	}
	return p, nil
}

// HostingDatabaseImportMutation verifies the exact receipt family and returned state.
func (c Client) HostingDatabaseImportMutation(ctx context.Context, in domain.HostingDatabaseImportMutation) (domain.HostingDatabaseImportReceiptResult, error) {
	var r databaseImportResponse[domain.HostingDatabaseImportReceiptResult]
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidDatabaseImport
	}
	path := "/api/v1/runners/hosting/database-import/mutation"
	stage := in.RunID == "db_import_stage_"+in.OperationID
	if stage {
		path = "/api/v1/runners/hosting/database-import-stage/mutation"
	}
	if e := c.post(databaseBackupProtectedContext(ctx), path, in, &r); e != nil {
		if ctx.Err() != nil {
			return r.value, ctx.Err()
		}
		return r.value, errors.New("external import receipt unavailable")
	}
	out := r.value
	if stage {
		expected := map[string]string{"stage_intent": "running", "ready": "ready", "stage_failed": "failed"}[in.Phase]
		if out.Stage == nil || out.Import != nil || out.Stage.Validate() != nil || out.Stage.ID != in.OperationID || out.Stage.State != expected || out.Stage.FailureCode != in.FailureCode || in.Ready != nil && out.Stage.StageHash != in.Ready.Hash {
			return out, domain.ErrInvalidDatabaseImport
		}
	} else {
		expected := map[string]string{"import_intent": "running", "complete": "succeeded", "failed": "failed", "uncertain": "uncertain"}[in.Phase]
		if out.Import == nil || out.Stage != nil || out.Import.Validate() != nil || out.Import.ID != in.OperationID || out.Import.State != expected || out.Import.FailureCode != in.FailureCode || out.Import.WritersResumed != in.WritersResumed {
			return out, domain.ErrInvalidDatabaseImport
		}
	}
	return out, nil
}

// HostingDatabaseImportStageCleanup obtains atomic permission to delete an exact expired stage.
func (c Client) HostingDatabaseImportStageCleanup(ctx context.Context, in domain.HostingDatabaseImportStageCleanup) (domain.HostingDatabaseImportStageCleanupResult, error) {
	var r databaseImportResponse[domain.HostingDatabaseImportStageCleanupResult]
	if in.Validate() != nil {
		return r.value, domain.ErrInvalidDatabaseImport
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-import-stage/cleanup", in, &r); e != nil {
		return r.value, e
	}
	if r.value.HostingDatabaseImportStageCleanup != in {
		return r.value, domain.ErrInvalidDatabaseImport
	}
	return r.value, nil
}
