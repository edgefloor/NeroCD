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

// BackupRemoteCredentialAuthorization contains delivery metadata and encrypted credential
// records.
type BackupRemoteCredentialAuthorization struct {
	ProjectID string
	Metadata  domain.BackupRemoteCredentialMetadata
	Records   BackupRemoteCredentialRecords
}

// HostingDatabaseBackupRemoteCredentialRepository defines persistence operations for database
// backup remote credential.
type HostingDatabaseBackupRemoteCredentialRepository interface {
	AuthorizeBackupRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateBackupRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

func backupRemoteDeliveryAuthority(ctx context.Context, tx pgx.Tx, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (backupRemoteRow, time.Time, error) {
	var expiry time.Time
	if in.Validate() != nil || in.RetentionCopyID != "" {
		return backupRemoteRow{}, expiry, ErrConflict
	}
	r, err := backupRemoteAuthority(ctx, tx, runner, in.Authority, true)
	if err != nil {
		return r, expiry, err
	}
	if r.plan.Hash != in.PlanHash || r.view.TargetID != in.TargetID || r.view.TargetRevision != in.TargetRevision || r.plan.DaemonID != "" && r.plan.DaemonID != in.DaemonID || r.daemon != "" && r.daemon != in.DaemonID || r.view.State != "dispatched" && r.view.State != "applying" {
		return r, expiry, ErrConflict
	}
	if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
		return r, expiry, err
	}
	expiry, err = sqlcgen.New(tx).ReadDatabaseBackupRemoteCredentialExpiry(ctx, sqlcgen.ReadDatabaseBackupRemoteCredentialExpiryParams{ID: in.Authority.LeaseID, ID_2: runner, TokenHash: tokenHash})
	return r, expiry, hostingError(err)
}

// AuthorizeBackupRemoteCredentials authorizes and records a remote transfer credential delivery.
func (s *PostgresStore) AuthorizeBackupRemoteCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (BackupRemoteCredentialAuthorization, error) {
		var out BackupRemoteCredentialAuthorization
		r, expiry, err := backupRemoteDeliveryAuthority(ctx, tx, runner, tokenHash, in)
		if err != nil {
			return out, err
		}
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.BackupRemoteCredentialRequest
		}{runner, in})
		var raw []byte
		raw, err = sqlcgen.New(tx).ReadDatabaseBackupRemoteDelivery(ctx, in.DeliveryID)
		if err == nil {
			if json.Unmarshal(raw, &out.Metadata) != nil || out.Metadata.Validate() != nil || out.Metadata.RequestHash != hash || out.Metadata.Request != in || out.Metadata.Purpose != domain.HostingDatabaseBackupRemoteCredentialPurposeFor(r.view.Version) {
				return out, ErrConflict
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseBackupRemoteCredentialPurposeFor(r.view.Version), Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			err = sqlcgen.New(tx).CreateDatabaseBackupRemoteDelivery(ctx, sqlcgen.CreateDatabaseBackupRemoteDeliveryParams{DeliveryID: in.DeliveryID, OperationID: in.Authority.OperationID, RunID: in.Authority.RunID, LeaseID: in.Authority.LeaseID, Attempt: int32(in.Authority.Attempt), Fence: in.Authority.Fence, RunnerID: runner, RequestHash: hash, Metadata: buildJSON(out.Metadata), UseUntil: expiry})
			if err != nil {
				return out, hostingError(err)
			}
			if err = databaseBackupAudit(ctx, tx, runner, r.view.ID, "remote_credentials_"+in.DeliveryID, r.view.ProjectID); err != nil {
				return out, err
			}
		} else {
			return out, err
		}
		var valid bool
		valid, err = sqlcgen.New(tx).DatabaseBackupRemoteDeliveryValid(ctx, sqlcgen.DatabaseBackupRemoteDeliveryValidParams{Column1: out.Metadata.UseUntil, Column2: expiry})
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

// RevalidateBackupRemoteCredentials rechecks a saved remote transfer credential grant before use.
func (s *PostgresStore) RevalidateBackupRemoteCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		operation, expiry, e := backupRemoteDeliveryAuthority(ctx, tx, runner, tokenHash, in)
		if e != nil {
			return false, e
		}
		var raw []byte
		raw, e = sqlcgen.New(tx).ReadValidDatabaseBackupRemoteDelivery(ctx, sqlcgen.ReadValidDatabaseBackupRemoteDeliveryParams{DeliveryID: in.DeliveryID, OperationID: in.Authority.OperationID, RunnerID: runner, UseUntil: expiry})
		if e != nil {
			return false, hostingError(e)
		}
		var saved domain.BackupRemoteCredentialMetadata
		if json.Unmarshal(raw, &saved) != nil || m.Validate() != nil || m.Purpose != domain.HostingDatabaseBackupRemoteCredentialPurposeFor(operation.view.Version) {
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
