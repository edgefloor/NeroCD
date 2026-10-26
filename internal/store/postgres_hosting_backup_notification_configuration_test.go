package store

import (
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/notificationtransport"
	"nerocd/internal/secretcrypto"
	"strings"
	"testing"
)

func TestNotificationRequiresAbsoluteHTTPSRecoveryOrigin(t *testing.T) {
	ring, err := secretcrypto.New("fixture", map[string][]byte{"fixture": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := notificationtransport.New(notificationtransport.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s := &PostgresStore{}
	if err = s.ConfigureBackupNotifications(ring, sender, ""); err != nil {
		t.Fatal("empty notification origin affected startup", err)
	}
	// No pool exists. These calls must reject before transaction or network I/O.
	if err = s.SendBackupNotification(t.Context(), "delivery"); !errors.Is(err, ErrBackupNotificationsUnavailable) {
		t.Fatal("missing origin allowed send", err)
	}
	if _, err = s.SaveNotificationChannel(t.Context(), "channel", "project", "owner", "", domain.NotificationChannelInput{RequestID: "request", Name: "alerts", Type: "webhook", Events: []string{}}, nil, nil, nil); !errors.Is(err, ErrBackupNotificationsUnavailable) {
		t.Fatal("missing origin admitted channel", err)
	}
	if _, err = s.TestNotificationChannel(t.Context(), "test", "project", "owner", "channel", domain.NotificationTestInput{RequestID: "request", ExpectedGeneration: 1}); !errors.Is(err, ErrBackupNotificationsUnavailable) {
		t.Fatal("missing origin admitted test", err)
	}
	for _, origin := range []string{"http://nerocd.example", "https://user:password@nerocd.example", "https://nerocd.example/path", "https://nerocd.example?token=x", "https://nerocd.example:0"} {
		if err = s.ConfigureBackupNotifications(ring, sender, origin); !errors.Is(err, ErrBackupNotificationsUnavailable) {
			t.Fatal("invalid origin accepted", origin, err)
		}
	}
	if err = s.ConfigureBackupNotifications(ring, sender, "https://nerocd.example"); err != nil || !s.backupNotificationsAvailable() {
		t.Fatal("valid protected origin unavailable", err)
	}
}
