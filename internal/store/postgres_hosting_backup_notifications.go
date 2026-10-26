package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func notificationChannelView(v sqlcgen.ReadNotificationChannelRow) domain.NotificationChannel {
	events := append([]string{}, v.Events...)
	return domain.NotificationChannel{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Type: v.Type, Enabled: v.Enabled, Generation: v.Generation, Events: events, Configured: v.Configured, ConsecutiveFailures: int(v.ConsecutiveFailures), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func notificationDeliveryView(v sqlcgen.ReadNotificationDeliveryRow) domain.NotificationDelivery {
	return domain.NotificationDelivery{ID: v.ID, EventID: v.EventID, ProjectID: v.ProjectID, ChannelID: v.ChannelID, ChannelGeneration: v.ChannelGeneration, Event: v.Event, SourceKind: v.SourceKind, SourceID: v.SourceID, SourceState: v.SourceState, State: v.State, AttemptCount: int(v.AttemptCount), MaxAttempts: int(v.MaxAttempts), FailureCode: v.FailureCode, LocalCopyAvailable: v.LocalCopyAvailable, RecoveryURL: v.RecoveryUrl, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func notificationReadSecret(ctx context.Context, tx pgx.Tx, project, id string, version int64, active bool) (NotificationSecret, error) {
	r := NotificationSecret{ID: id, Version: version}
	row, err := sqlcgen.New(tx).ReadNotificationSecret(ctx, sqlcgen.ReadNotificationSecretParams{ProjectID: project, ID: id, Version: version})
	if err != nil {
		return r, hostingError(err)
	}
	if active && row.Status != "active" {
		return r, ErrConflict
	}
	r.Envelope.Version = int(row.EnvelopeVersion)
	r.Envelope.KeyID = row.KeyID
	r.Envelope.Data = row.Ciphertext
	return r, nil
}
func notificationProjectLock(ctx context.Context, tx pgx.Tx, project, actor string) error {
	if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, false); err != nil {
		return err
	}
	_, err := sqlcgen.New(tx).LockNotificationProject(ctx, project)
	return hostingError(err)
}
func notificationMembers(ctx context.Context, tx pgx.Tx, project string, ids []string) ([]string, error) {
	emails, err := sqlcgen.New(tx).ListNotificationMembers(ctx, sqlcgen.ListNotificationMembersParams{ProjectID: project, Column2: ids})
	if err != nil {
		return nil, err
	}
	for _, email := range emails {
		if !domain.NotificationEmail(email) {
			return nil, ErrConflict
		}
	}
	return emails, nil
}

func (s *PostgresStore) SaveNotificationChannel(ctx context.Context, id, project, actor, existing string, in domain.NotificationChannelInput, secret *NotificationSecret, recipientIDs []string, compare NotificationSecretCompare) (domain.NotificationChannel, error) {
	if in.Validate() != nil {
		return domain.NotificationChannel{}, domain.ErrInvalidNotification
	}
	if !s.backupNotificationsAvailable() {
		return domain.NotificationChannel{}, ErrBackupNotificationsUnavailable
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.NotificationChannel, error) {
		var zero domain.NotificationChannel
		if err := notificationProjectLock(ctx, tx, project, actor); err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Existing string
			Input           domain.NotificationChannelInput
			Configuration   bool
		}{actor, existing, in, secret != nil})
		old, err := q.ReadNotificationSaveRequest(ctx, sqlcgen.ReadNotificationSaveRequestParams{ProjectID: project, RequestID: in.RequestID})
		if err == nil {
			if hash != old.RequestHash || old.Operation != "save" || json.Unmarshal(old.Response, &zero) != nil {
				return zero, ErrConflict
			}
			if secret != nil {
				saved, e := notificationReadSecret(ctx, tx, project, old.SecretID, old.SecretVersion, false)
				if e != nil {
					return zero, e
				}
				if compare == nil || compare(saved) != nil {
					return zero, ErrConflict
				}
			}
			return zero, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if s.notificationKeyring == nil || s.notificationSender == nil {
			return zero, ErrHostingDatabaseUnavailable
		}
		var ref string
		var version int64
		if existing != "" {
			v, e := q.LockNotificationChannel(ctx, sqlcgen.LockNotificationChannelParams{ID: existing, ProjectID: project})
			if e != nil {
				return zero, hostingError(e)
			}
			if v.Generation != in.ExpectedGeneration || v.Type != in.Type {
				return zero, ErrConflict
			}
			id = existing
			current, e := q.ReadNotificationChannelSecret(ctx, id)
			if e != nil {
				return zero, e
			}
			ref, version = current.SecretID, current.SecretVersion
		} else if in.ExpectedGeneration != 0 || secret == nil {
			return zero, ErrConflict
		}
		if secret != nil {
			if secret.ID == "" || secret.Version != 1 || secret.Envelope.Version != 1 || len(secret.Envelope.Data) < 29 {
				return zero, ErrConflict
			}
			if in.Type == "email" {
				members, e := notificationMembers(ctx, tx, project, recipientIDs)
				if e != nil {
					return zero, e
				}
				if len(members) != len(recipientIDs) || len(members) == 0 {
					return zero, ErrConflict
				}
			}
			ref, version = secret.ID, secret.Version
			if err = q.InsertNotificationSecret(ctx, sqlcgen.InsertNotificationSecretParams{ID: ref, Version: version, ProjectID: project, EnvelopeVersion: int32(secret.Envelope.Version), KeyID: secret.Envelope.KeyID, Ciphertext: secret.Envelope.Data}); err != nil {
				return zero, err
			}
		} else if _, err = notificationReadSecret(ctx, tx, project, ref, version, true); err != nil {
			return zero, err
		}
		if existing == "" {
			count, e := q.CountNotificationChannels(ctx, project)
			if e != nil {
				return zero, e
			}
			if count >= 20 {
				return zero, ErrConflict
			}
			err = q.CreateNotificationChannel(ctx, sqlcgen.CreateNotificationChannelParams{ID: id, ProjectID: project, Name: in.Name, Type: in.Type, Enabled: in.Enabled, Events: in.Events, SecretID: ref, SecretVersion: version})
		} else {
			err = q.UpdateNotificationChannel(ctx, sqlcgen.UpdateNotificationChannelParams{ID: id, Name: in.Name, Enabled: in.Enabled, Events: in.Events, SecretID: ref, SecretVersion: version})
		}
		if err != nil {
			return zero, err
		}
		row, err := q.ReadNotificationChannel(ctx, id)
		if err != nil {
			return zero, err
		}
		v := notificationChannelView(row)
		if err = q.InsertNotificationSaveRequest(ctx, sqlcgen.InsertNotificationSaveRequestParams{ProjectID: project, RequestID: in.RequestID, ActorID: actor, RequestHash: hash, Response: buildJSON(v), SecretID: ref, SecretVersion: version}); err != nil {
			return zero, err
		}
		if err = databaseBackupAudit(ctx, tx, actor, id+"_"+strconv.FormatInt(v.Generation, 10), "notification_channel_saved", project); err != nil {
			return zero, err
		}
		return v, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListNotificationChannels(ctx context.Context, actor, project string) ([]domain.NotificationChannel, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.NotificationChannel, error) {
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		rows, err := sqlcgen.New(tx).ListNotificationChannels(ctx, project)
		if err != nil {
			return nil, err
		}
		out := make([]domain.NotificationChannel, 0, len(rows))
		for _, row := range rows {
			out = append(out, notificationChannelView(sqlcgen.ReadNotificationChannelRow(row)))
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListNotificationDeliveries(ctx context.Context, actor, project string) ([]domain.NotificationDelivery, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.NotificationDelivery, error) {
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		rows, err := sqlcgen.New(tx).ListNotificationDeliveries(ctx, project)
		if err != nil {
			return nil, err
		}
		out := make([]domain.NotificationDelivery, 0, len(rows))
		for _, row := range rows {
			out = append(out, notificationDeliveryView(sqlcgen.ReadNotificationDeliveryRow(row)))
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) GetNotificationDelivery(ctx context.Context, actor, project, id string) (domain.NotificationDelivery, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.NotificationDelivery, error) {
		var zero domain.NotificationDelivery
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		row, err := q.ReadNotificationDelivery(ctx, sqlcgen.ReadNotificationDeliveryParams{ProjectID: project, ID: id})
		if err != nil {
			return zero, hostingError(err)
		}
		v := notificationDeliveryView(row)
		attempts, err := q.ListNotificationAttempts(ctx, id)
		if err != nil {
			return zero, err
		}
		for _, a := range attempts {
			v.Attempts = append(v.Attempts, domain.NotificationAttempt{Number: int(a.Number), State: a.State, FailureCode: a.FailureCode, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt})
		}
		return v, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) TestNotificationChannel(ctx context.Context, id, project, actor, channel string, in domain.NotificationTestInput) (domain.NotificationDelivery, error) {
	if in.Validate() != nil {
		return domain.NotificationDelivery{}, domain.ErrInvalidNotification
	}
	if !s.backupNotificationsAvailable() {
		return domain.NotificationDelivery{}, ErrBackupNotificationsUnavailable
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.NotificationDelivery, error) {
		var zero domain.NotificationDelivery
		if err := notificationProjectLock(ctx, tx, project, actor); err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Channel string
			Input          domain.NotificationTestInput
		}{actor, channel, in})
		old, err := q.ReadNotificationTestRequest(ctx, sqlcgen.ReadNotificationTestRequestParams{ProjectID: project, RequestID: in.RequestID})
		if err == nil {
			if old.RequestHash != hash || old.Operation != "test" || json.Unmarshal(old.Response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		v, err := q.LockNotificationChannel(ctx, sqlcgen.LockNotificationChannelParams{ID: channel, ProjectID: project})
		if err != nil {
			return zero, hostingError(err)
		}
		if v.Generation != in.ExpectedGeneration || !v.Enabled || s.notificationSender == nil || s.notificationKeyring == nil {
			return zero, ErrConflict
		}
		recent, err := q.RecentNotificationTest(ctx, channel)
		if err != nil {
			return zero, err
		}
		if recent {
			return zero, ErrConflict
		}
		if err = notificationInsertEvent(ctx, tx, notificationEvent{ID: id, Project: project, Event: "test", SourceKind: "test", SourceID: id, SourceState: "test", Terminal: "test", Receipt: in.RequestID, Recovery: "/projects?project_id=" + project}, channel); err != nil {
			return zero, err
		}
		row, err := q.ReadNotificationDeliveryByEvent(ctx, id)
		if err != nil {
			return zero, err
		}
		out := notificationDeliveryView(sqlcgen.ReadNotificationDeliveryRow(row))
		if err = q.InsertNotificationTestRequest(ctx, sqlcgen.InsertNotificationTestRequestParams{ProjectID: project, RequestID: in.RequestID, ActorID: actor, RequestHash: hash, Response: buildJSON(out)}); err != nil {
			return zero, err
		}
		if err = databaseBackupAudit(ctx, tx, actor, id, "notification_test_requested", project); err != nil {
			return zero, err
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

type notificationEvent struct {
	ID, Project, Event, SourceKind, SourceID, SourceState, Terminal, Receipt, Failure, Recovery string
	Local                                                                                       bool
}

func notificationStableID(prefix string, v any) string {
	return prefix + strings.TrimPrefix(domain.HostingDocumentHash(v), "sha256:")
}
func notificationInsertEvent(ctx context.Context, tx pgx.Tx, e notificationEvent, onlyChannel string) error {
	// Persist selection with the source transaction. Recovery publishes opaque
	// delivery IDs after commit; there is no network or River dependency here.
	if e.ID == "" {
		e.ID = notificationStableID("nte_", []string{e.SourceKind, e.SourceID, e.Terminal})
	}
	q := sqlcgen.New(tx)
	_, err := q.InsertNotificationEvent(ctx, sqlcgen.InsertNotificationEventParams{ID: e.ID, ProjectID: e.Project, Event: e.Event, SourceKind: e.SourceKind, SourceID: e.SourceID, SourceState: e.SourceState, TerminalEvent: e.Terminal, SourceReceipt: e.Receipt, FailureCode: e.Failure, LocalCopyAvailable: e.Local, RecoveryUrl: e.Recovery})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	channels, err := q.SelectNotificationChannels(ctx, sqlcgen.SelectNotificationChannelsParams{ProjectID: e.Project, Column2: e.Event, Column3: onlyChannel})
	if err != nil {
		return err
	}
	for _, c := range channels {
		id := notificationStableID("ntd_", []any{e.ID, c.ID, c.Generation})
		if err = q.InsertNotificationDelivery(ctx, sqlcgen.InsertNotificationDeliveryParams{ID: id, EventID: e.ID, ProjectID: e.Project, ChannelID: c.ID, ChannelGeneration: c.Generation, SecretID: c.SecretID, SecretVersion: c.SecretVersion}); err != nil {
			return err
		}
	}
	return databaseBackupAudit(ctx, tx, "notification_controller", e.ID, "notification_event", e.Project)
}
