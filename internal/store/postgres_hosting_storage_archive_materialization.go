package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageRestoreOriginPresent(ctx context.Context, tx pgx.Tx, storage string) (bool, error) {
	q := sqlcgen.New(tx)
	present, err := q.StorageArchiveSchemaPresent(ctx)
	if err != nil || !present {
		return false, err
	}
	_, err = q.ReadStorageRestoreOrigin(ctx, storage)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func readStorageOperationAdmission(ctx context.Context, tx pgx.Tx, id string) (sqlcgen.HostingStorageOperationAdmission, error) {
	q := sqlcgen.New(tx)
	row, err := q.ReadHostingStorageOperationAdmission(ctx, id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, err
	}
	present, e := q.StorageArchiveSchemaPresent(ctx)
	if e != nil {
		return row, e
	}
	if !present {
		return row, err
	}
	use, e := q.ReadStorageRestoreUseAdmission(ctx, id)
	if e != nil {
		return row, e
	}
	return sqlcgen.HostingStorageOperationAdmission(use), nil
}

func insertStorageOperationAdmission(ctx context.Context, tx pgx.Tx, in sqlcgen.InsertHostingStorageOperationAdmissionParams) error {
	q := sqlcgen.New(tx)
	restored, err := storageRestoreOriginPresent(ctx, tx, in.StorageID)
	if err != nil {
		return err
	}
	if restored {
		return q.InsertStorageRestoreUseAdmission(ctx, sqlcgen.InsertStorageRestoreUseAdmissionParams(in))
	}
	return q.InsertHostingStorageOperationAdmission(ctx, in)
}

func readStorageMaterializedReceipt(ctx context.Context, tx pgx.Tx, id string) (sqlcgen.HostingStorageMaterializedReceipt, error) {
	q := sqlcgen.New(tx)
	row, err := q.ReadHostingStorageMaterializedReceipt(ctx, id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, err
	}
	present, e := q.StorageArchiveSchemaPresent(ctx)
	if e != nil {
		return row, e
	}
	if !present {
		return row, err
	}
	use, e := q.ReadStorageRestoreUseReceipt(ctx, id)
	if e != nil {
		return row, e
	}
	return sqlcgen.HostingStorageMaterializedReceipt(use), nil
}

func insertStorageMaterializedReceipt(ctx context.Context, tx pgx.Tx, in sqlcgen.InsertHostingStorageMaterializedReceiptParams) (sqlcgen.HostingStorageMaterializedReceipt, error) {
	q := sqlcgen.New(tx)
	restored, err := storageRestoreOriginPresent(ctx, tx, in.StorageID)
	if err != nil {
		return sqlcgen.HostingStorageMaterializedReceipt{}, err
	}
	if !restored {
		return q.InsertHostingStorageMaterializedReceipt(ctx, in)
	}
	row, err := q.InsertStorageRestoreUseReceipt(ctx, sqlcgen.InsertStorageRestoreUseReceiptParams(in))
	return sqlcgen.HostingStorageMaterializedReceipt(row), err
}

func (s *PostgresStore) archiveStorageCreateBegin(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	var zero domain.HostingStorageCreateAck
	op, err := s.archiveAuthority(ctx, tx, runner, in.HostingAuthority, true)
	if err != nil {
		return zero, err
	}
	p := op.StorageArchive.Plan
	if p.Mode != "restore" || p.Target == nil || op.State != "prepared" || op.StorageArchive.PreparedReceipt == nil || in.PlanHash != p.Hash || in.StorageRef != p.Target.Storage.Reference() || in.CreationNonce != p.Target.Plan.CreationNonce {
		return zero, ErrConflict
	}
	q := sqlcgen.New(tx)
	st := p.Target.Storage
	if _, err = q.LockStorageArchiveStorage(ctx, st.ID); err != nil {
		return zero, err
	}
	prior, err := q.ReadStorageRestoreOrigin(ctx, st.ID)
	if err == nil {
		var a domain.HostingAuthority
		if json.Unmarshal(prior.Authority, &a) != nil || a != in.HostingAuthority {
			return zero, ErrConflict
		}
		m, e := readArchiveRestoreMaterialization(ctx, tx, st)
		if e != nil {
			return zero, e
		}
		return domain.HostingStorageCreateAck{Storage: st, Materialization: m, CreatePermitted: false}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	m := domain.HostingStorageMaterialization{StorageRef: st.Reference(), State: "create_admitted", CreationOperationID: op.ID, CreationPlanHash: p.Hash, CreationNonce: p.Target.Plan.CreationNonce, DaemonID: p.Helper.DaemonID}
	m.Labels = domain.ExpectedHostingStorageLabels(st, m)
	if m.Validate(st) != nil {
		return zero, ErrConflict
	}
	if err = q.InsertStorageRestoreOrigin(ctx, sqlcgen.InsertStorageRestoreOriginParams{StorageID: st.ID, OriginOperationID: op.ID, OriginPlanHash: p.Hash, Origin: buildJSON(m), OriginHash: domain.HostingDocumentHash(m), Authority: buildJSON(in.HostingAuthority)}); err != nil {
		return zero, err
	}
	if err = archiveAudit(ctx, tx, runner, "storage_create_admitted", op); err != nil {
		return zero, err
	}
	if _, err = s.archiveAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return zero, err
	}
	return domain.HostingStorageCreateAck{Storage: st, Materialization: m, CreatePermitted: true}, nil
}

func (s *PostgresStore) archiveStorageMaterialized(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	var zero domain.HostingStorageMaterializedAck
	op, err := s.archiveAuthority(ctx, tx, runner, in.HostingAuthority, false)
	if err != nil {
		return zero, err
	}
	p := op.StorageArchive.Plan
	if p.Mode != "restore" || p.Target == nil || in.PlanHash != p.Hash || in.StorageRef != p.Target.Storage.Reference() {
		return zero, ErrConflict
	}
	q := sqlcgen.New(tx)
	st := p.Target.Storage
	if _, err = q.LockStorageArchiveStorage(ctx, st.ID); err != nil {
		return zero, err
	}
	origin, err := q.ReadStorageRestoreOrigin(ctx, st.ID)
	if err != nil {
		return zero, err
	}
	var a domain.HostingAuthority
	if json.Unmarshal(origin.Authority, &a) != nil || a != in.HostingAuthority {
		return zero, ErrConflict
	}
	m, err := readArchiveRestoreMaterialization(ctx, tx, st)
	if err != nil || in.Matches(st, m) != nil {
		return zero, ErrConflict
	}
	prior, err := q.ReadStorageRestoreMaterialized(ctx, st.ID)
	if err == nil {
		if prior.OperationID != op.ID || prior.ReceiptID != in.ReceiptID || prior.BodyHash != domain.HostingDocumentHash(in) {
			return zero, ErrConflict
		}
		return domain.HostingStorageMaterializedAck{Materialization: m, Receipt: domain.HostingReceipt{ID: prior.ReceiptID, BodyHash: prior.BodyHash, CreatedAt: prior.CreatedAt}}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if _, err = s.archiveAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return zero, err
	}
	if op.State != "prepared" {
		return zero, ErrConflict
	}
	if err = archiveAudit(ctx, tx, runner, "storage_materialized", op); err != nil {
		return zero, err
	}
	row, err := q.InsertStorageRestoreMaterialized(ctx, sqlcgen.InsertStorageRestoreMaterializedParams{StorageID: st.ID, OperationID: op.ID, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), AuditID: "aud_archive_storage_materialized_" + op.ID})
	if err != nil {
		return zero, err
	}
	m, err = readArchiveRestoreMaterialization(ctx, tx, st)
	if err != nil {
		return zero, err
	}
	if _, err = s.archiveAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return zero, err
	}
	return domain.HostingStorageMaterializedAck{Materialization: m, Receipt: domain.HostingReceipt{ID: row.ReceiptID, BodyHash: row.BodyHash, CreatedAt: row.CreatedAt}}, nil
}
