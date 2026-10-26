package app

import (
	"context"
	"crypto/subtle"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func (s *Service) notificationRepository() (store.HostingNotificationRepository, error) {
	r, ok := s.hosting.(store.HostingNotificationRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// SaveNotificationChannel creates or revises a project notification channel.
func (s *Service) SaveNotificationChannel(ctx context.Context, project, existing string, in domain.NotificationChannelInput, c *domain.NotificationConfiguration) (domain.NotificationChannel, error) {
	var zero domain.NotificationChannel
	if in.Validate() != nil || existing == "" && in.ExpectedGeneration != 0 || existing != "" && in.ExpectedGeneration < 1 || existing == "" && c == nil || c != nil && c.Validate(in.Type) != nil {
		return zero, InvalidInput(domain.ErrInvalidNotification)
	}
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	r, err := s.notificationRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("ntc")
	if err != nil {
		return zero, err
	}
	var record *store.NotificationSecret
	var compare store.NotificationSecretCompare
	var recipients []string
	if c != nil {
		if s.secretKeyring == nil {
			return zero, ErrManagedSecretsUnavailable
		}
		raw, e := c.Encode()
		if e != nil {
			return zero, InvalidInput(domain.ErrInvalidNotification)
		}
		defer clear(raw)
		ref, e := prefixedID("ntk")
		if e != nil {
			return zero, e
		}
		envelope, e := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: project, SecretID: ref, Version: 1}, raw)
		if e != nil {
			return zero, errors.New("notification_encryption_unavailable")
		}
		record = &store.NotificationSecret{ID: ref, Version: 1, Envelope: envelope}
		recipients = append([]string(nil), c.RecipientUserIDs...)
		compare = func(saved store.NotificationSecret) error {
			prior, e := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: project, SecretID: saved.ID, Version: saved.Version}, saved.Envelope)
			defer clear(prior)
			if e != nil || subtle.ConstantTimeCompare(prior, raw) != 1 {
				return store.ErrConflict
			}
			return nil
		}
	}
	return r.SaveNotificationChannel(ctx, id, project, actor.ID, existing, in, record, recipients, compare)
}

// ListNotificationChannels lists notification channels visible to the project viewer.
func (s *Service) ListNotificationChannels(ctx context.Context, project string) ([]domain.NotificationChannel, error) {
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	r, err := s.notificationRepository()
	if err != nil {
		return nil, err
	}
	return r.ListNotificationChannels(ctx, actor.ID, project)
}

// TestNotificationChannel queues a test delivery for an authorized notification channel.
func (s *Service) TestNotificationChannel(ctx context.Context, project, channel string, in domain.NotificationTestInput) (domain.NotificationDelivery, error) {
	var zero domain.NotificationDelivery
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidNotification)
	}
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	r, err := s.notificationRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("ntt")
	if err != nil {
		return zero, err
	}
	return r.TestNotificationChannel(ctx, id, project, actor.ID, channel, in)
}

// ListNotificationDeliveries lists notification deliveries visible to the project viewer.
func (s *Service) ListNotificationDeliveries(ctx context.Context, project string) ([]domain.NotificationDelivery, error) {
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	r, err := s.notificationRepository()
	if err != nil {
		return nil, err
	}
	return r.ListNotificationDeliveries(ctx, actor.ID, project)
}

// GetNotificationDelivery returns an authorized notification delivery view.
func (s *Service) GetNotificationDelivery(ctx context.Context, project, id string) (domain.NotificationDelivery, error) {
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
	if err != nil {
		return domain.NotificationDelivery{}, err
	}
	r, err := s.notificationRepository()
	if err != nil {
		return domain.NotificationDelivery{}, err
	}
	return r.GetNotificationDelivery(ctx, actor.ID, project, id)
}
