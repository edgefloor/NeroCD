package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
)

var ErrBackupNotificationsUnavailable = errors.New("backup notifications require configured HTTPS public origin and managed secrets")

type NotificationSecret struct {
	ID       string
	Version  int64
	Envelope secretcrypto.Envelope
}
type NotificationSecretCompare func(NotificationSecret) error
type HostingNotificationRepository interface {
	SaveNotificationChannel(context.Context, string, string, string, string, domain.NotificationChannelInput, *NotificationSecret, []string, NotificationSecretCompare) (domain.NotificationChannel, error)
	ListNotificationChannels(context.Context, string, string) ([]domain.NotificationChannel, error)
	TestNotificationChannel(context.Context, string, string, string, string, domain.NotificationTestInput) (domain.NotificationDelivery, error)
	ListNotificationDeliveries(context.Context, string, string) ([]domain.NotificationDelivery, error)
	GetNotificationDelivery(context.Context, string, string, string) (domain.NotificationDelivery, error)
}
