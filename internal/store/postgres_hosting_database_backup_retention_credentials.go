package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
)

type HostingDatabaseBackupRetentionCredentialRepository interface {
	AuthorizeBackupRetentionCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateBackupRetentionCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

func backupRetentionDeliveryAuthority(ctx context.Context, tx pgx.Tx, runner, token string, in domain.BackupRemoteCredentialRequest) (backupRetentionRow, domain.BackupRetentionCopy, time.Time, error) {
	var c domain.BackupRetentionCopy
	var expiry time.Time
	if in.Validate() != nil || in.RetentionCopyID == "" {
		return backupRetentionRow{}, c, expiry, ErrConflict
	}
	r, e := backupRetentionAuthority(ctx, tx, runner, in.Authority, true)
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
	if c.Target == nil || c.Target.Target.ID != in.TargetID || c.Target.Target.Revision != in.TargetRevision || c.DaemonID != in.DaemonID {
		return r, c, expiry, ErrConflict
	}
	if e = backupRetentionCurrent(ctx, tx, r.plan); e != nil {
		return r, c, expiry, e
	}
	e = tx.QueryRow(ctx, `SELECT least(l.expires_at,clock_timestamp()+interval '1 minute') FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r`, in.Authority.LeaseID, runner, token).Scan(&expiry)
	return r, c, expiry, hostingError(e)
}
func (s *PostgresStore) AuthorizeBackupRetentionCredentials(ctx context.Context, runner, token string, in domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (BackupRemoteCredentialAuthorization, error) {
		var out BackupRemoteCredentialAuthorization
		r, c, expiry, e := backupRetentionDeliveryAuthority(ctx, tx, runner, token, in)
		if e != nil {
			return out, e
		}
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.BackupRemoteCredentialRequest
		}{runner, in})
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT metadata FROM hosting_database_backup_retention_deliveries WHERE delivery_id=$1`, in.DeliveryID).Scan(&raw)
		if e == nil {
			if json.Unmarshal(raw, &out.Metadata) != nil || out.Metadata.RequestHash != hash || out.Metadata.Request != in || out.Metadata.Validate() != nil {
				return out, ErrConflict
			}
		} else if errors.Is(e, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseBackupRetentionCredentialPurpose, Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_retention_deliveries(delivery_id,execution_id,copy_id,metadata,use_until) VALUES($1,$2,$3,$4,$5)`, in.DeliveryID, in.Authority.OperationID, in.RetentionCopyID, buildJSON(out.Metadata), expiry); e != nil {
				return out, e
			}
			if e = databaseBackupAudit(ctx, tx, runner, r.view.ID, "retention_credentials_"+in.DeliveryID, r.view.ProjectID); e != nil {
				return out, e
			}
		} else {
			return out, e
		}
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz`, out.Metadata.UseUntil, expiry).Scan(&valid); e != nil {
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
func (s *PostgresStore) RevalidateBackupRetentionCredentials(ctx context.Context, runner, token string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		_, _, expiry, e := backupRetentionDeliveryAuthority(ctx, tx, runner, token, in)
		if e != nil {
			return false, e
		}
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT metadata FROM hosting_database_backup_retention_deliveries WHERE delivery_id=$1 AND execution_id=$2 AND copy_id=$3 AND use_until>clock_timestamp() AND use_until<=$4`, in.DeliveryID, in.Authority.OperationID, in.RetentionCopyID, expiry).Scan(&raw)
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
