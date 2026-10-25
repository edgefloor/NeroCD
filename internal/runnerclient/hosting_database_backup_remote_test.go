package runnerclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestBackupRemoteClientPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := New("https://backup-controller.example", "synthetic-token-canary")
	a := domain.HostingAuthority{OperationID: "transfer", RunID: "db_transfer_transfer", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	if _, err := c.HostingDatabaseBackupTransferPlan(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatal("plan lost caller cancellation")
	}
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "receipt", PlanHash: "sha256:" + strings.Repeat("a", 64), Phase: "intent", DaemonID: "daemon"}
	if _, err := c.HostingDatabaseBackupTransferMutation(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal("receipt lost caller cancellation")
	}
}
