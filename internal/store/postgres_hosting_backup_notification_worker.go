package store

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/notificationtransport"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

// ConfigureBackupNotifications freezes protected dependencies before queue
// startup. It starts no goroutine and performs no delivery.
func (s *PostgresStore) ConfigureBackupNotifications(keyring *secretcrypto.Keyring, sender *notificationtransport.Sender, publicOrigin string) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	if publicOrigin != "" {
		u, err := url.Parse(publicOrigin)
		if err != nil || len(publicOrigin) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return ErrBackupNotificationsUnavailable
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return ErrBackupNotificationsUnavailable
			}
		}
	}
	if (keyring == nil) != (sender == nil) {
		return domain.ErrInvalidNotification
	}
	s.notificationKeyring = keyring
	s.notificationSender = sender
	s.notificationPublicOrigin = publicOrigin
	return nil
}

func (s *PostgresStore) backupNotificationsAvailable() bool {
	return s.notificationSender != nil && s.notificationKeyring != nil && s.notificationPublicOrigin != ""
}

type notificationClaim struct {
	Delivery    domain.NotificationDelivery
	Payload     domain.NotificationPayload
	Secret      NotificationSecret
	Kind, Fence string
	Expires     time.Time
}

// A dedicated PostgreSQL session lock serializes sends across processes even
// when a lease expires. Expiry alone never permits a concurrent duplicate send.
// Losing the session is an authority failure; the sender's before-send callback
// probes that same session before each message. External delivery is at least
// once: a lost DATA/HTTP acknowledgement may cause a duplicate after recovery.
func (s *PostgresStore) SendBackupNotification(ctx context.Context, id string) error {
	if !s.backupNotificationsAvailable() {
		return ErrBackupNotificationsUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	locked := false
	defer func() {
		if locked {
			releaseCtx, stop := context.WithTimeout(context.Background(), time.Second)
			released, e := sqlcgen.New(conn).ReleaseNotificationSessionLock(releaseCtx, id)
			stop()
			if e != nil || !released {
				closeCtx, stopClose := context.WithTimeout(context.Background(), time.Second)
				_ = conn.Conn().Close(closeCtx)
				stopClose()
			}
		}
		conn.Release()
	}()
	if locked, err = sqlcgen.New(conn).AcquireNotificationSessionLock(ctx, id); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	claim, ok, err := s.claimBackupNotification(ctx, tx, id)
	if err != nil {
		return err
	}
	if !ok {
		return tx.Commit(ctx)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	raw, err := s.notificationKeyring.Open(secretcrypto.Binding{ProjectID: claim.Delivery.ProjectID, SecretID: claim.Secret.ID, Version: claim.Secret.Version}, claim.Secret.Envelope)
	result := notificationtransport.Result{Code: "configuration_unavailable", ConfigurationFailure: true}
	if err == nil {
		configuration, e := domain.DecodeNotificationConfiguration(raw)
		clear(raw)
		if e == nil {
			defer configuration.Close()
			if configuration.Validate(claim.Kind) == nil {
				authorize := func(sendCtx context.Context) ([]string, error) {
					probe, e := conn.Begin(sendCtx)
					if e != nil {
						return nil, e
					}
					defer rollbackTransaction(sendCtx, probe)
					if e = notificationClaimCurrent(sendCtx, probe, claim); e != nil {
						if errors.Is(e, ErrConflict) {
							return nil, notificationtransport.ErrAuthorityChanged
						}
						return nil, e
					}
					var emails []string
					if claim.Kind == "email" {
						emails, e = notificationMembers(sendCtx, probe, claim.Delivery.ProjectID, configuration.RecipientUserIDs)
						if e != nil {
							return nil, e
						}
						if len(emails) == 0 {
							return nil, notificationtransport.ErrAuthorityChanged
						}
					}
					if e = probe.Commit(sendCtx); e != nil {
						return nil, e
					}
					return emails, nil
				}
				result = s.notificationSender.Send(ctx, configuration, claim.Payload, authorize)
			}
		}
	}
	// Cancellation cannot erase an uncertain external attempt. A bounded cleanup
	// context records its result, and the immutable budget remains consumed.
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	finish, err := conn.Begin(finishCtx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(finishCtx, finish)
	if err = finishBackupNotification(finishCtx, finish, claim, result); err != nil {
		return err
	}
	return finish.Commit(finishCtx)
}

func (s *PostgresStore) claimBackupNotification(ctx context.Context, tx pgx.Tx, id string) (notificationClaim, bool, error) {
	var claim notificationClaim
	q := sqlcgen.New(tx)
	row, err := q.LockNotificationDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim, false, nil
	}
	if err != nil {
		return claim, false, err
	}
	v := notificationDeliveryView(sqlcgen.ReadNotificationDeliveryRow(row))
	claim.Delivery = v
	if v.State != "pending" && v.State != "sending" {
		return claim, false, nil
	}
	saved, err := q.ReadNotificationClaim(ctx, id)
	if err != nil {
		return claim, false, err
	}
	claim.Secret.ID, claim.Secret.Version = saved.SecretID, saved.SecretVersion
	now, err := q.NotificationClock(ctx)
	if err != nil {
		return claim, false, err
	}
	if v.State == "sending" && saved.ClaimExpiresAt != nil && saved.ClaimExpiresAt.After(now) {
		return claim, false, nil
	}
	if v.State == "pending" && saved.NextAttemptAt.After(now) {
		return claim, false, nil
	}
	if v.State == "sending" {
		if err = q.ExpireNotificationAttempt(ctx, sqlcgen.ExpireNotificationAttemptParams{DeliveryID: id, Number: int32(v.AttemptCount), Fence: saved.Fence}); err != nil {
			return claim, false, err
		}
	}
	if v.AttemptCount >= domain.NotificationMaxAttempts || !saved.DeadlineAt.After(now) {
		return claim, false, q.ExhaustNotificationBudget(ctx, id)
	}
	channel, err := q.ReadNotificationChannelForSend(ctx, sqlcgen.ReadNotificationChannelForSendParams{ID: v.ChannelID, ProjectID: v.ProjectID})
	if err != nil {
		return claim, false, err
	}
	claim.Kind = channel.Type
	if !channel.Enabled || channel.Generation != v.ChannelGeneration || channel.SecretID != claim.Secret.ID || channel.SecretVersion != claim.Secret.Version || !domain.NotificationSelected(channel.Events, v.Event) {
		return claim, false, q.SuppressChangedNotification(ctx, id)
	}
	claim.Secret, err = notificationReadSecret(ctx, tx, v.ProjectID, channel.SecretID, channel.SecretVersion, true)
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return claim, false, q.SuppressRevokedNotification(ctx, id)
		}
		return claim, false, err
	}
	claim.Fence = rand.Text()
	claim.Expires = now.Add(30 * time.Second)
	claim.Delivery.AttemptCount++
	if err = q.ClaimNotificationDelivery(ctx, sqlcgen.ClaimNotificationDeliveryParams{ID: id, Fence: claim.Fence, ClaimExpiresAt: &claim.Expires}); err != nil {
		return claim, false, err
	}
	if err = q.InsertNotificationAttempt(ctx, sqlcgen.InsertNotificationAttemptParams{DeliveryID: id, Number: int32(claim.Delivery.AttemptCount), Fence: claim.Fence}); err != nil {
		return claim, false, err
	}
	failure, err := q.ReadNotificationEventFailure(ctx, v.EventID)
	if err != nil {
		return claim, false, err
	}
	link := s.notificationPublicOrigin + v.RecoveryURL
	claim.Payload = domain.NotificationPayload{EventID: v.EventID, DeliveryID: v.ID, Event: v.Event, Message: domain.NotificationMessage(v.Event, v.SourceState), SourceKind: v.SourceKind, SourceID: v.SourceID, SourceState: v.SourceState, FailureCode: failure, LocalCopyAvailable: v.LocalCopyAvailable, RecoveryURL: link}
	return claim, true, nil
}
func notificationClaimCurrent(ctx context.Context, tx pgx.Tx, c notificationClaim) error {
	current, err := sqlcgen.New(tx).NotificationClaimCurrent(ctx, sqlcgen.NotificationClaimCurrentParams{ID: c.Delivery.ID, Fence: c.Fence, AttemptCount: int32(c.Delivery.AttemptCount)})
	if err != nil {
		return err
	}
	if !current {
		return ErrConflict
	}
	return nil
}
func finishBackupNotification(ctx context.Context, tx pgx.Tx, c notificationClaim, r notificationtransport.Result) error {
	q := sqlcgen.New(tx)
	saved, err := q.LockNotificationFinish(ctx, c.Delivery.ID)
	if err != nil {
		return err
	}
	if saved.Fence != c.Fence || saved.State != "sending" || int(saved.AttemptCount) != c.Delivery.AttemptCount {
		return ErrConflict
	}
	state, attempt := "delivered", "delivered"
	if r.Code != "" {
		state, attempt = "pending", "failed"
		if r.Uncertain {
			attempt = "uncertain"
		}
		if saved.AttemptCount >= domain.NotificationMaxAttempts || r.ConfigurationFailure || r.Permanent {
			state = "failed"
		}
		if r.Code == "authority_changed" {
			state, attempt = "suppressed", "suppressed"
		}
	}
	if err = q.FinishNotificationAttempt(ctx, sqlcgen.FinishNotificationAttemptParams{DeliveryID: c.Delivery.ID, Number: saved.AttemptCount, State: attempt, FailureCode: r.Code, Fence: c.Fence}); err != nil {
		return err
	}
	if err = q.FinishNotificationDelivery(ctx, sqlcgen.FinishNotificationDeliveryParams{ID: c.Delivery.ID, State: state, FailureCode: r.Code}); err != nil {
		return err
	}
	// Explicit tests never mutate the actual-event configuration failure counter.
	if c.Delivery.Event != "test" && (r.ConfigurationFailure || r.Code == "") {
		if r.Code == "" {
			return q.ResetNotificationFailureCount(ctx, sqlcgen.ResetNotificationFailureCountParams{ID: c.Delivery.ChannelID, Generation: c.Delivery.ChannelGeneration})
		}
		return q.IncrementNotificationFailureCount(ctx, sqlcgen.IncrementNotificationFailureCountParams{ID: c.Delivery.ChannelID, Generation: c.Delivery.ChannelGeneration})
	}
	return nil
}
func (s *PostgresStore) recoverBackupNotifications(ctx context.Context) error {
	if !s.backupNotificationsAvailable() || !s.hostingJobs.BackupNotificationEnabled() {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, err := sqlcgen.New(tx).RecoverNotificationDeliveries(ctx)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err = s.hostingJobs.EnqueueBackupNotificationTx(ctx, tx, hostingjobs.BackupNotificationArgs{DeliveryID: id}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}
func (h deploymentOnlyHandler) SendBackupNotification(ctx context.Context, id string) error {
	return h.store.SendBackupNotification(ctx, id)
}
