package store

import (
	"context"
	"errors"
	"net/url"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/store/sqlcgen"
)

// Source rows and their exact accepted receipts are locked by the caller.
// Restore, download, validation, retention, and discovery-only misses do not
// enter the notification stream.
func notificationBackupFailure(ctx context.Context, tx pgx.Tx, id, receipt string) error {
	b, err := sqlcgen.New(tx).ReadNotificationBackupSource(ctx, id)
	if err != nil {
		return err
	}
	if b.Mode != "backup" || (b.State != "failed" && b.State != "uncertain") {
		return nil
	}
	q := url.Values{"project_id": {b.ProjectID}, "environment_id": {b.ProjectEnvironmentID}, "resource_id": {b.SourceResourceID}, "backup_id": {id}}
	return notificationInsertEvent(ctx, tx, notificationEvent{Project: b.ProjectID, Event: "backup_failed", SourceKind: "backup", SourceID: id, SourceState: b.State, Terminal: "backup_failed", Receipt: receipt, Failure: b.FailureCode, Recovery: "/projects?" + q.Encode()}, "")
}
func notificationUploadFailure(ctx context.Context, tx pgx.Tx, id, receipt string) error {
	b, err := sqlcgen.New(tx).ReadNotificationUploadSource(ctx, id)
	if err != nil {
		return err
	}
	if b.Mode != "upload" || (b.State != "failed" && b.State != "uncertain") {
		return nil
	}
	q := url.Values{"project_id": {b.ProjectID}, "environment_id": {b.ProjectEnvironmentID}, "resource_id": {b.SourceResourceID}, "backup_id": {b.ID}, "transfer_id": {id}}
	return notificationInsertEvent(ctx, tx, notificationEvent{Project: b.ProjectID, Event: "upload_failed", SourceKind: "transfer", SourceID: id, SourceState: b.State, Terminal: "upload_failed", Receipt: receipt, Failure: b.FailureCode, Local: b.Exists, Recovery: "/projects?" + q.Encode()}, "")
}

// Direct controller transitions have no runner receipt. They retain an exact
// state snapshot in the same transaction rather than inventing lease authority.
func notificationDirectReceipt(ctx context.Context, tx pgx.Tx, kind, id, reason string) (string, error) {
	receipt := notificationStableID("ntr_", []string{kind, id, reason})
	q := sqlcgen.New(tx)
	if kind == "transfer" {
		return receipt, q.InsertNotificationUploadSourceReceipt(ctx, sqlcgen.InsertNotificationUploadSourceReceiptParams{ID: receipt, ID_2: id, Reason: reason})
	}
	return receipt, q.InsertNotificationBackupSourceReceipt(ctx, sqlcgen.InsertNotificationBackupSourceReceiptParams{ID: receipt, ID_2: id, Reason: reason})
}

// Storage has separate immutable plans and receipts. It only shares delivery
// policy and transport with database backups, never their execution authority.
func notificationStorageBackupFailure(ctx context.Context, tx pgx.Tx, id, receipt string) error {
	b, err := sqlcgen.New(tx).ReadNotificationStorageBackupSource(ctx, sqlcgen.ReadNotificationStorageBackupSourceParams{ID: id, ReceiptID: receipt})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state := b.State
	if state == "reconciliation_required" {
		state = "uncertain"
	}
	q := url.Values{"project_id": {b.ProjectID}, "environment_id": {b.ProjectEnvironmentID}, "resource_id": {b.SourceResourceID}, "backup_id": {b.BackupID}, "source_kind": {"storage_backup"}}
	return notificationInsertEvent(ctx, tx, notificationEvent{Project: b.ProjectID, Event: "backup_failed", SourceKind: "storage_backup", SourceID: id, SourceState: state, Terminal: "backup_failed", Receipt: receipt, Failure: b.FailureCode, Local: b.LocalCopyAvailable, Recovery: "/projects?" + q.Encode()}, "")
}
func notificationStorageUploadFailure(ctx context.Context, tx pgx.Tx, id, receipt string) error {
	b, err := sqlcgen.New(tx).ReadNotificationStorageUploadSource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	q := url.Values{"project_id": {b.ProjectID}, "environment_id": {b.ProjectEnvironmentID}, "resource_id": {b.ResourceID}, "backup_id": {b.BackupID}, "transfer_id": {id}, "source_kind": {"storage_transfer"}}
	return notificationInsertEvent(ctx, tx, notificationEvent{Project: b.ProjectID, Event: "upload_failed", SourceKind: "storage_transfer", SourceID: id, SourceState: b.State, Terminal: "upload_failed", Receipt: receipt, Failure: b.FailureCode, Local: b.LocalCopyAvailable, Recovery: "/projects?" + q.Encode()}, "")
}
func notificationStorageDirectReceipt(ctx context.Context, tx pgx.Tx, id, reason string) (string, error) {
	receipt := notificationStableID("ntr_", []string{"storage_transfer", id, reason})
	err := sqlcgen.New(tx).InsertNotificationStorageUploadSourceReceipt(ctx, sqlcgen.InsertNotificationStorageUploadSourceReceiptParams{ID: receipt, ID_2: id, Reason: reason})
	return receipt, err
}
