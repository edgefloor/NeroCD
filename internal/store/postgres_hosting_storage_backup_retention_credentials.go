package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type HostingStorageBackupRetentionCredentialRepository interface {
	AuthorizeStorageBackupRetentionCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateStorageBackupRetentionCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

func storageRetentionDeliveryAuthority(ctx context.Context, tx pgx.Tx, runner, token string, in domain.BackupRemoteCredentialRequest) (storageRetentionRow, domain.StorageBackupRetentionCopy, time.Time, error) {
	var c domain.StorageBackupRetentionCopy
	var expiry time.Time
	if in.Validate() != nil || in.RetentionCopyID == "" {
		return storageRetentionRow{}, c, expiry, ErrConflict
	}
	r, e := storageRetentionAuthority(ctx, tx, runner, in.Authority, true)
	if e != nil {
		return r, c, expiry, e
	}
	if r.plan.Hash != in.PlanHash || r.view.State != "dispatched" && r.view.State != "applying" {
		return r, c, expiry, ErrConflict
	}
	for _, v := range r.plan.Copies {
		if v.Item.CopyID == in.RetentionCopyID {
			c = v
		}
	}
	held := false
	for _, item := range r.view.Items {
		if item.CopyID == in.RetentionCopyID {
			held = item.ReservationHeld
		}
	}
	if !held || c.Target == nil || c.Target.Target.ID != in.TargetID || c.Target.Target.Revision != in.TargetRevision || c.DaemonID != in.DaemonID {
		return r, c, expiry, ErrConflict
	}
	if e = storageRetentionCurrent(ctx, tx, r.plan); e != nil {
		return r, c, expiry, e
	}
	expiry, e = sqlcgen.New(tx).StorageRetentionCredentialDeadline(ctx, sqlcgen.StorageRetentionCredentialDeadlineParams{ID: in.Authority.LeaseID, ID_2: runner, TokenHash: token})
	return r, c, expiry, hostingError(e)
}
func (s *PostgresStore) AuthorizeStorageBackupRetentionCredentials(ctx context.Context, runner, token string, in domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (BackupRemoteCredentialAuthorization, error) {
		var out BackupRemoteCredentialAuthorization
		r, c, expiry, e := storageRetentionDeliveryAuthority(ctx, tx, runner, token, in)
		if e != nil {
			return out, e
		}
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.BackupRemoteCredentialRequest
		}{runner, in})
		var raw []byte
		raw, e = sqlcgen.New(tx).StorageRetentionReadDelivery(ctx, in.DeliveryID)
		if e == nil {
			if json.Unmarshal(raw, &out.Metadata) != nil || out.Metadata.RequestHash != hash || out.Metadata.Request != in || out.Metadata.Validate() != nil {
				return out, ErrConflict
			}
		} else if errors.Is(e, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingStorageBackupRetentionCredentialPurpose, Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			if e = sqlcgen.New(tx).StorageRetentionInsertDelivery(ctx, sqlcgen.StorageRetentionInsertDeliveryParams{DeliveryID: in.DeliveryID, ExecutionID: in.Authority.OperationID, CopyID: in.RetentionCopyID, Metadata: buildJSON(out.Metadata), UseUntil: expiry}); e != nil {
				return out, e
			}
			if e = storageRetentionAudit(ctx, tx, runner, r.view.ID, "retention_credentials_"+in.DeliveryID, r.view.ProjectID); e != nil {
				return out, e
			}
		} else {
			return out, e
		}
		var valid bool
		if valid, e = sqlcgen.New(tx).StorageRetentionDeliveryLive(ctx, sqlcgen.StorageRetentionDeliveryLiveParams{Column1: out.Metadata.UseUntil, Column2: expiry}); e != nil {
			return out, e
		}
		if !valid {
			return out, ErrConflict
		}
		out.ProjectID = r.view.ProjectID
		out.Records.Access, e = backupRemoteCredentialRecord(ctx, tx, out.ProjectID, c.Target.Access, true)
		if e != nil {
			return out, e
		}
		out.Records.Secret, e = backupRemoteCredentialRecord(ctx, tx, out.ProjectID, c.Target.Secret, true)
		return out, e
	})
}
func (s *PostgresStore) RevalidateStorageBackupRetentionCredentials(ctx context.Context, runner, token string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		_, _, expiry, e := storageRetentionDeliveryAuthority(ctx, tx, runner, token, in)
		if e != nil {
			return false, e
		}
		var raw []byte
		raw, e = sqlcgen.New(tx).StorageRetentionReadLiveDelivery(ctx, sqlcgen.StorageRetentionReadLiveDeliveryParams{DeliveryID: in.DeliveryID, ExecutionID: in.Authority.OperationID, CopyID: in.RetentionCopyID, UseUntil: expiry})
		if e != nil {
			return false, hostingError(e)
		}
		var saved domain.BackupRemoteCredentialMetadata
		if json.Unmarshal(raw, &saved) != nil || m.Validate() != nil {
			return false, ErrConflict
		}
		comp := saved
		comp.UseUntil = m.UseUntil
		if comp != m || !saved.UseUntil.Equal(m.UseUntil) {
			return false, ErrConflict
		}
		return true, nil
	})
	return e
}
