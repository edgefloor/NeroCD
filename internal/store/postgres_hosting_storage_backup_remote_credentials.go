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

func storageRemoteDeliveryAuthority(ctx context.Context, tx pgx.Tx, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (storageRemoteRow, time.Time, error) {
	var expiry time.Time
	if in.Validate() != nil || in.RetentionCopyID != "" {
		return storageRemoteRow{}, expiry, ErrConflict
	}
	r, err := storageRemoteAuthority(ctx, tx, runner, in.Authority, true)
	if err != nil {
		return r, expiry, err
	}
	if r.plan.Hash != in.PlanHash || r.view.TargetID != in.TargetID || r.view.TargetRevision != in.TargetRevision || r.plan.DaemonID != "" && r.plan.DaemonID != in.DaemonID || r.daemon != "" && r.daemon != in.DaemonID || r.view.State != "dispatched" && r.view.State != "applying" {
		return r, expiry, ErrConflict
	}
	if err = storageRemoteCurrent(ctx, tx, r.plan); err != nil {
		return r, expiry, err
	}
	expiry, err = sqlcgen.New(tx).StorageRemoteCredentialDeadline(ctx, sqlcgen.StorageRemoteCredentialDeadlineParams{ID: in.Authority.LeaseID, ID_2: runner, TokenHash: tokenHash})
	return r, expiry, hostingError(err)
}

// AuthorizeStorageRemoteCredentials authorizes and records a storage transfer credential
// delivery.
func (s *PostgresStore) AuthorizeStorageRemoteCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (BackupRemoteCredentialAuthorization, error) {
		var out BackupRemoteCredentialAuthorization
		r, expiry, err := storageRemoteDeliveryAuthority(ctx, tx, runner, tokenHash, in)
		if err != nil {
			return out, err
		}
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.BackupRemoteCredentialRequest
		}{runner, in})
		var raw []byte
		raw, err = sqlcgen.New(tx).ReadStorageRemoteDelivery(ctx, in.DeliveryID)
		if err == nil {
			if json.Unmarshal(raw, &out.Metadata) != nil || out.Metadata.Validate() != nil || out.Metadata.RequestHash != hash || out.Metadata.Request != in {
				return out, ErrConflict
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingStorageBackupRemoteCredentialPurpose, Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			err = sqlcgen.New(tx).InsertStorageRemoteDelivery(ctx, sqlcgen.InsertStorageRemoteDeliveryParams{DeliveryID: in.DeliveryID, OperationID: in.Authority.OperationID, RunID: in.Authority.RunID, LeaseID: in.Authority.LeaseID, Attempt: int32(in.Authority.Attempt), Fence: in.Authority.Fence, RunnerID: runner, RequestHash: hash, Metadata: buildJSON(out.Metadata), UseUntil: expiry})
			if err != nil {
				return out, hostingError(err)
			}
			if err = storageRemoteAudit(ctx, tx, runner, r.view.ID, "credentials_"+in.DeliveryID, r.view.ProjectID); err != nil {
				return out, err
			}
		} else {
			return out, err
		}
		var valid bool
		valid, err = sqlcgen.New(tx).StorageRemoteDeliveryLive(ctx, sqlcgen.StorageRemoteDeliveryLiveParams{Column1: out.Metadata.UseUntil, Column2: expiry})
		if err != nil {
			return out, err
		}
		if !valid {
			return out, ErrConflict
		}
		out.ProjectID = r.view.ProjectID
		out.Records.Access, err = backupRemoteCredentialRecord(ctx, tx, out.ProjectID, r.plan.Target.Access, true)
		if err != nil {
			return out, err
		}
		out.Records.Secret, err = backupRemoteCredentialRecord(ctx, tx, out.ProjectID, r.plan.Target.Secret, true)
		return out, err
	})
}

// RevalidateStorageRemoteCredentials rechecks a saved storage transfer credential grant before
// use.
func (s *PostgresStore) RevalidateStorageRemoteCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		_, expiry, e := storageRemoteDeliveryAuthority(ctx, tx, runner, tokenHash, in)
		if e != nil {
			return false, e
		}
		var raw []byte
		raw, e = sqlcgen.New(tx).ReadLiveStorageRemoteDelivery(ctx, sqlcgen.ReadLiveStorageRemoteDeliveryParams{DeliveryID: in.DeliveryID, OperationID: in.Authority.OperationID, RunnerID: runner, UseUntil: expiry})
		if e != nil {
			return false, hostingError(e)
		}
		var saved domain.BackupRemoteCredentialMetadata
		if json.Unmarshal(raw, &saved) != nil || m.Validate() != nil {
			return false, ErrConflict
		}
		// PostgreSQL and JSON can represent the same instant with different
		// process-local time zones. Keep every other metadata field exact.
		comparable := saved
		comparable.UseUntil = m.UseUntil
		if comparable != m || !saved.UseUntil.Equal(m.UseUntil) {
			return false, ErrConflict
		}
		return true, nil
	})
	return err
}
