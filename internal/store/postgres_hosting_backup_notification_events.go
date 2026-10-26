package store

import (
	"context"
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
