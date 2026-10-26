package runnerclient

import (
	"context"
	"errors"
	"io"
	"strings"

	"nerocd/internal/domain"
)

type databaseBackupPlanResponse struct {
	value domain.HostingDatabaseBackupPlan
}

func (r *databaseBackupPlanResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return errors.New("invalid database backup plan response")
	}
	return nil
}

type databaseBackupMutationResponse struct {
	value domain.HostingDatabaseBackupOperation
}

func (r *databaseBackupMutationResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, 16385))
	if err != nil || len(raw) > 16384 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return errors.New("invalid database backup receipt response")
	}
	return nil
}
func databaseBackupProtectedContext(ctx context.Context) context.Context {
	if _, ok := ctx.Value(databaseTransportKey{}).(DatabaseTransport); !ok {
		return WithDatabaseTransport(ctx, DatabaseTransport{})
	}
	return ctx
}

// HostingDatabaseBackupPlan fetches and validates a plan for the supplied authority.
func (c Client) HostingDatabaseBackupPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error) {
	var p domain.HostingDatabaseBackupPlan
	if (domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "plan", PlanHash: "sha256:" + strings.Repeat("0", 64), Phase: "intent"}).Validate() != nil {
		return p, domain.ErrInvalidDatabaseBackup
	}
	var response databaseBackupPlanResponse
	ctx = databaseBackupProtectedContext(ctx)
	if err := c.post(ctx, "/api/v1/runners/hosting/database-backup/plan", a, &response); err != nil {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		return p, errors.New("database backup authority unavailable")
	}
	p = response.value
	if p.Validate() != nil || p.Operation.ID != a.OperationID {
		return p, errors.New("invalid database backup plan response")
	}
	return p, nil
}

// HostingDatabaseBackupMutation submits a receipt and validates the returned backup state.
func (c Client) HostingDatabaseBackupMutation(ctx context.Context, in domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error) {
	var out domain.HostingDatabaseBackupOperation
	if in.Validate() != nil {
		return out, domain.ErrInvalidDatabaseBackup
	}
	var response databaseBackupMutationResponse
	ctx = databaseBackupProtectedContext(ctx)
	if err := c.post(ctx, "/api/v1/runners/hosting/database-backup/mutation", in, &response); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("database backup receipt request failed")
	}
	out = response.value
	state := map[string]string{"intent": "applying", "complete": "succeeded", "failed": "failed", "uncertain": "uncertain"}[in.Phase]
	if out.Validate() != nil || out.ID != in.OperationID || out.Version != domain.HostingDatabaseBackupVersion || out.State != state || in.Phase == "complete" && (out.Blob == nil || domain.HostingDocumentHash(out.Blob) != domain.HostingDocumentHash(in.Blob)) {
		return out, errors.New("invalid database backup receipt response")
	}
	return out, nil
}
