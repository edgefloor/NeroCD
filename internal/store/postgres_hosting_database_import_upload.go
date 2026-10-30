package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ErrDatabaseImportUploadHash rejects a body before dispatch or consumption.
var ErrDatabaseImportUploadHash = errors.New("upload hash mismatch")

// RequestDatabaseImportUploadStage persists target-bound metadata without dispatching work.
func (s *PostgresStore) RequestDatabaseImportUploadStage(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseImportUploadInput) (domain.HostingDatabaseImportStage, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseImportStage{}, domain.ErrInvalidDatabaseImport
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportStage, error) {
		var zero domain.HostingDatabaseImportStage
		r, err := externalImportResourceAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Resource string
			Input           domain.HostingDatabaseImportUploadInput
		}{actor, resource, in})
		prior, err := q.FindDatabaseImportStageRequest(ctx, sqlcgen.FindDatabaseImportStageRequestParams{TargetResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if prior.ActorID != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			return externalImportStageView(prior.Body)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		h, operation, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, in.Component, domain.RoleOwner)
		if err != nil || operation.Plan == nil {
			return zero, ErrConflict
		}
		if generation != in.ExpectedResourceGeneration || h.OperationID != in.ExpectedDeployedOperationID {
			return zero, ErrConflict
		}
		if _, err = domain.HostingMySQLComponent(h, *operation.Plan, in.Component); err != nil {
			return zero, ErrConflict
		}
		now := time.Now().UTC()
		deadline := now.Add(time.Hour)
		view := domain.HostingDatabaseImportStage{ID: id, Version: domain.HostingDatabaseImportUploadVersion, TargetResourceID: resource, Component: in.Component, ExpectedDeployedOperationID: h.OperationID, ExpectedResourceGeneration: generation, UploadSource: &domain.HostingDatabaseImportUploadSource{Filename: in.Filename, Bytes: in.SourceBytes}, UploadDeadline: &deadline, State: "queued", CreatedAt: now, UpdatedAt: now}
		p := domain.HostingDatabaseImportStagePlan{Stage: view, ActorID: actor, ProjectID: r.ProjectID, RunnerID: r.RunnerID, Target: h, TargetPlan: *operation.Plan, MaxBytes: domain.HostingDatabaseImportMaxBytes, TimeoutSeconds: 3600}
		p.Stage.UploadHash = p.UploadIdentityHash()
		view.UploadHash = p.Stage.UploadHash
		p.Hash = domain.HostingDocumentHash(p)
		if p.Validate() != nil {
			return zero, ErrConflict
		}
		if err = externalImportSourceAuthority(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = externalImportRunner(ctx, tx, r.RunnerID, "upload"); err != nil {
			return zero, err
		}
		if err = q.CreateDatabaseImportStage(ctx, sqlcgen.CreateDatabaseImportStageParams{ID: id, ProjectID: r.ProjectID, TargetResourceID: resource, TargetBindingID: r.BindingID, ActorID: actor, RequestID: in.RequestID, RequestHash: hash, RunnerID: r.RunnerID, DaemonID: h.Release.DaemonID, Plan: buildJSON(p), PlanHash: p.Hash, Body: buildJSON(view)}); err != nil {
			return zero, hostingError(err)
		}
		return view, externalImportAudit(ctx, tx, actor, r.ProjectID, id, "stage_requested")
	})
}

// DatabaseImportUpload authorizes metadata or atomically admits the only body attempt.
// The returned bool reports a previous attempt, never permission to forward bytes.
func (s *PostgresStore) DatabaseImportUpload(ctx context.Context, actor, id, hash string, begin bool) (domain.HostingDatabaseImportStagePlan, domain.HostingDatabaseImportStage, bool, error) {
	type result struct {
		plan      domain.HostingDatabaseImportStagePlan
		view      domain.HostingDatabaseImportStage
		attempted bool
	}
	out, err := buildTransaction(ctx, s, func(tx pgx.Tx) (result, error) {
		var out result
		q := sqlcgen.New(tx)
		row, err := q.ReadDatabaseImportStage(ctx, id)
		if err != nil {
			return out, hostingError(err)
		}
		if _, err = externalImportResourceAuthority(ctx, tx, actor, row.TargetResourceID, domain.RoleOwner); err != nil {
			return out, err
		}
		row, err = q.LockDatabaseImportStage(ctx, id)
		if err != nil {
			return out, hostingError(err)
		}
		out.view, err = externalImportStageView(row.Body)
		if err != nil || json.Unmarshal(row.Plan, &out.plan) != nil || out.plan.Validate() != nil || out.plan.Hash != row.PlanHash || out.plan.Stage.Version != domain.HostingDatabaseImportUploadVersion || out.plan.ActorID != actor {
			return out, ErrConflict
		}
		if out.plan.Stage.UploadHash != hash {
			return out, ErrDatabaseImportUploadHash
		}
		out.attempted = row.RunID != nil
		if out.attempted {
			return out, nil
		}
		if out.view.State != "queued" || !time.Now().Before(*out.plan.Stage.UploadDeadline) {
			return out, ErrConflict
		}
		if err = externalImportCurrent(ctx, tx, out.plan, "stage"); err != nil {
			return out, err
		}
		if begin {
			err = dispatchExternalImport(ctx, tx, id, row.ProjectID, actor, "stage")
		}
		return out, err
	})
	return out.plan, out.view, out.attempted, err
}

// RevalidateDatabaseImportUpload retains live target and actor authority during a body.
func (s *PostgresStore) RevalidateDatabaseImportUpload(ctx context.Context, actor string, p domain.HostingDatabaseImportStagePlan) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		if actor != p.ActorID || p.Stage.Version != domain.HostingDatabaseImportUploadVersion || p.Validate() != nil || !time.Now().Before(*p.Stage.UploadDeadline) {
			return false, ErrConflict
		}
		if _, e := externalImportResourceAuthority(ctx, tx, actor, p.Stage.TargetResourceID, domain.RoleOwner); e != nil {
			return false, e
		}
		row, e := sqlcgen.New(tx).LockDatabaseImportStage(ctx, p.Stage.ID)
		if e != nil || row.PlanHash != p.Hash || row.RunID == nil {
			return false, ErrConflict
		}
		return true, externalImportCurrent(ctx, tx, p, "stage")
	})
	return err
}
