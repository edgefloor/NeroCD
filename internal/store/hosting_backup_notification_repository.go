package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
)

// ErrBackupNotificationsUnavailable reports missing notification origin or managed secret
// configuration.
var ErrBackupNotificationsUnavailable = errors.New("backup notifications require configured HTTPS public origin and managed secrets")

// NotificationSecret contains the encrypted notification configuration and its metadata.
type NotificationSecret struct {
	ID       string
	Version  int64
	Envelope secretcrypto.Envelope
}

// NotificationSecretCompare checks whether a replayed secret matches the supplied notification
// configuration.
type NotificationSecretCompare func(NotificationSecret) error

// HostingNotificationRepository defines persistence operations for notification.
type HostingNotificationRepository interface {
	SaveNotificationChannel(context.Context, string, string, string, string, domain.NotificationChannelInput, *NotificationSecret, []string, NotificationSecretCompare) (domain.NotificationChannel, error)
	ListNotificationChannels(context.Context, string, string) ([]domain.NotificationChannel, error)
	TestNotificationChannel(context.Context, string, string, string, string, domain.NotificationTestInput) (domain.NotificationDelivery, error)
	ListNotificationDeliveries(context.Context, string, string) ([]domain.NotificationDelivery, error)
	GetNotificationDelivery(context.Context, string, string, string) (domain.NotificationDelivery, error)
}
