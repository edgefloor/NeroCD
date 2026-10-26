package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/riverqueue/river"
	"strings"
	"testing"
)

type notificationHandlerFunc func(context.Context, string) error

func (f notificationHandlerFunc) SendBackupNotification(ctx context.Context, id string) error {
	return f(ctx, id)
}
func TestBackupNotificationOpaqueJobAndSafeErrors(t *testing.T) {
	args := BackupNotificationArgs{DeliveryID: "ntd_stable"}
	raw, _ := json.Marshal(args)
	if string(raw) != `{"delivery_id":"ntd_stable"}` || args.InsertOpts().MaxAttempts != 5 {
		t.Fatal("job payload or attempt bounds changed")
	}
	w := backupNotificationWorker{handler: notificationHandlerFunc(func(ctx context.Context, id string) error {
		if id != "ntd_stable" {
			t.Fatal("identity changed")
		}
		return errors.New("canary_receiver_error")
	})}
	err := w.Work(t.Context(), &river.Job[BackupNotificationArgs]{Args: args})
	if err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("raw receiver failure exposed")
	}
	if err = w.Work(t.Context(), &river.Job[BackupNotificationArgs]{Args: BackupNotificationArgs{DeliveryID: "../bad"}}); err == nil {
		t.Fatal("invalid delivery identity accepted")
	}
}
