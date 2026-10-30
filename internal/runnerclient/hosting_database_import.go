package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"nerocd/internal/domain"
	"net/http"
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

// HostingDatabaseImportUploadStream opens the once-only bounded browser body.
// Closing the returned body also releases its dedicated protected transport.
func (c Client) HostingDatabaseImportUploadStream(ctx context.Context, in domain.HostingDatabaseImportUploadStream) (io.ReadCloser, error) {
	if in.Validate() != nil {
		return nil, domain.ErrInvalidDatabaseImport
	}
	policy, _ := databaseBackupProtectedContext(ctx).Value(databaseTransportKey{}).(DatabaseTransport)
	secured, closeTransport, err := c.serverReadinessHTTP(ServerReadinessTransport(policy))
	if err != nil {
		return nil, err
	}
	secured.HTTP.Timeout = 0
	raw, err := json.Marshal(in)
	if err != nil {
		closeTransport()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/database-import-stage/upload-stream", bytes.NewReader(raw))
	if err != nil {
		closeTransport()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	response, err := secured.HTTP.Do(req)
	if err != nil {
		closeTransport()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("local upload stream unavailable")
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/octet-stream" {
		_ = response.Body.Close()
		closeTransport()
		return nil, errors.New("local upload stream unavailable")
	}
	return &databaseImportUploadBody{ReadCloser: response.Body, release: closeTransport}, nil
}

type databaseImportUploadBody struct {
	io.ReadCloser
	release func()
}

func (b *databaseImportUploadBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
