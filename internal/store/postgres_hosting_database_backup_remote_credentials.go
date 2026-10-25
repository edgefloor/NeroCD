package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

type BackupRemoteCredentialAuthorization struct {
	ProjectID string
	Metadata  domain.BackupRemoteCredentialMetadata
	Records   BackupRemoteCredentialRecords
}
type HostingDatabaseBackupRemoteCredentialRepository interface {
	AuthorizeBackupRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateBackupRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

func backupRemoteDeliveryAuthority(ctx context.Context, tx pgx.Tx, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (backupRemoteRow, time.Time, error) {
	var expiry time.Time
	if in.Validate() != nil {
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
	err = tx.QueryRow(ctx, `SELECT least(l.expires_at,clock_timestamp()+interval '1 minute') FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r`, in.Authority.LeaseID, runner, tokenHash).Scan(&expiry)
	return r, expiry, hostingError(err)
}

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
		err = tx.QueryRow(ctx, `SELECT metadata FROM hosting_database_backup_remote_deliveries WHERE delivery_id=$1`, in.DeliveryID).Scan(&raw)
		if err == nil {
			if json.Unmarshal(raw, &out.Metadata) != nil || out.Metadata.Validate() != nil || out.Metadata.RequestHash != hash || out.Metadata.Request != in {
				return out, ErrConflict
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseBackupRemoteCredentialPurpose, Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_remote_deliveries(delivery_id,operation_id,run_id,lease_id,attempt,fence,runner_id,request_hash,metadata,use_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.DeliveryID, in.Authority.OperationID, in.Authority.RunID, in.Authority.LeaseID, in.Authority.Attempt, in.Authority.Fence, runner, hash, buildJSON(out.Metadata), expiry)
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
		if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz`, out.Metadata.UseUntil, expiry).Scan(&valid); err != nil {
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
func (s *PostgresStore) RevalidateBackupRemoteCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		_, expiry, e := backupRemoteDeliveryAuthority(ctx, tx, runner, tokenHash, in)
		if e != nil {
			return false, e
		}
		var raw []byte
		if e = tx.QueryRow(ctx, `SELECT metadata FROM hosting_database_backup_remote_deliveries WHERE delivery_id=$1 AND operation_id=$2 AND runner_id=$3 AND use_until>clock_timestamp() AND use_until<=$4`, in.DeliveryID, in.Authority.OperationID, runner, expiry).Scan(&raw); e != nil {
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
