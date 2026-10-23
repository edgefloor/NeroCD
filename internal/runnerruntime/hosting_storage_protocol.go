package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// journalHostingStorageCreate retains the intent even after an admission ACK.
// A returned fresh permission lives only in the caller's current stack.
func journalHostingStorageCreate(ctx context.Context, journal *runner.AttemptJournal, client runnerclient.Client, entry runner.JournalHosting) (domain.HostingStorageCreateAck, error) {
	if entry.Phase != "storage_create_intent" || entry.StorageCreate == nil {
		return domain.HostingStorageCreateAck{}, errors.New("storage creation requires durable intent")
	}
	if _, err := journal.AppendHosting(entry); err != nil {
		return domain.HostingStorageCreateAck{}, err
	}
	return client.HostingStorageCreateBegin(ctx, *entry.StorageCreate)
}

// journalHostingStorageMaterialized appends evidence before sending. Keep its
// exact body through workload settlement, including nil versus empty options,
// so a restart never reconstructs a different acknowledged receipt.
func journalHostingStorageMaterialized(ctx context.Context, journal *runner.AttemptJournal, client runnerclient.Client, entry runner.JournalHosting) (domain.HostingStorageMaterializedAck, error) {
	if entry.Phase != "storage_materialized" || entry.StorageMaterialized == nil {
		return domain.HostingStorageMaterializedAck{}, errors.New("storage materialization requires durable evidence")
	}
	if _, err := journal.AppendHosting(entry); err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	ack, err := client.HostingStorageMaterialized(ctx, *entry.StorageMaterialized)
	if err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	if err = ctx.Err(); err != nil {
		return domain.HostingStorageMaterializedAck{}, err
	}
	return ack, nil
}
