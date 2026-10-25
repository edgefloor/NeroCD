package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

func backupRemoteTargetSnapshot(ctx context.Context, tx pgx.Tx, id string, revision int64) (domain.HostingDatabaseBackupTargetSnapshot, error) {
	var raw []byte
	var out domain.HostingDatabaseBackupTargetSnapshot
	if err := tx.QueryRow(ctx, `SELECT snapshot FROM hosting_database_backup_target_versions WHERE target_id=$1 AND revision=$2`, id, revision).Scan(&raw); err != nil {
		return out, hostingError(err)
	}
	if json.Unmarshal(raw, &out) != nil || out.Validate() != nil || out.Target.ID != id || out.Target.Revision != revision {
		return out, ErrConflict
	}
	return out, nil
}

func backupRemoteCredentialRecord(ctx context.Context, tx pgx.Tx, project string, ref domain.BackupRemoteCredentialRef, active bool) (ManagedSecretRecord, error) {
	var out ManagedSecretRecord
	var status string
	var version int
	out.Metadata.SecretID = ref.SecretID
	out.Metadata.Version = ref.Version
	err := tx.QueryRow(ctx, `SELECT status,envelope_version,key_id,ciphertext,created_by,created_at FROM hosting_database_backup_remote_credentials WHERE id=$1 AND project_id=$2 AND version=$3 FOR SHARE`, ref.SecretID, project, ref.Version).Scan(&status, &version, &out.Envelope.KeyID, &out.Envelope.Data, &out.Metadata.CreatedBy, &out.Metadata.CreatedAt)
	out.Envelope.Version = version
	if err != nil {
		return out, hostingError(err)
	}
	if active && status != "active" || !validManagedRecord(out) {
		return out, ErrConflict
	}
	return out, nil
}

func insertBackupRemoteCredential(ctx context.Context, tx pgx.Tx, project, actor string, record ManagedSecretRecord) error {
	if !validManagedRecord(record) || record.Metadata.Version != 1 || record.Metadata.CreatedBy != actor {
		return ErrConflict
	}
	_, err := tx.Exec(ctx, `INSERT INTO hosting_database_backup_remote_credentials(id,project_id,version,status,envelope_version,key_id,ciphertext,created_by,created_at) VALUES($1,$2,1,'active',$3,$4,$5,$6,$7)`, record.Metadata.SecretID, project, record.Envelope.Version, record.Envelope.KeyID, record.Envelope.Data, actor, record.Metadata.CreatedAt)
	return hostingError(err)
}

func (s *PostgresStore) SaveDatabaseBackupTarget(ctx context.Context, id, project, actor, existing string, in domain.HostingDatabaseBackupTargetInput, records *BackupRemoteCredentialRecords, compare BackupRemoteCredentialCompare) (domain.HostingDatabaseBackupTarget, error) {
	if in.Validate() != nil || existing == "" && (in.ExpectedRevision != 0 || records == nil) || existing != "" && in.ExpectedRevision < 1 || records != nil && compare == nil {
		return domain.HostingDatabaseBackupTarget{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTarget, error) {
		var zero domain.HostingDatabaseBackupTarget
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		// Project serialization also closes request-ID races before any encrypted
		// credential comparison or target revision is accepted.
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project).Scan(&locked); err != nil {
			return zero, hostingError(err)
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Existing string
			Input           domain.HostingDatabaseBackupTargetInput
			Credentials     bool
		}{actor, existing, in, records != nil})
		var savedHash, savedActor, target string
		var revision int64
		var supplied bool
		var response []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,actor_id,target_id,revision,credentials_supplied,response FROM hosting_database_backup_target_requests WHERE project_id=$1 AND request_id=$2`, project, in.RequestID).Scan(&savedHash, &savedActor, &target, &revision, &supplied, &response)
		if err == nil {
			if savedHash != hash || savedActor != actor || supplied != (records != nil) || json.Unmarshal(response, &zero) != nil {
				return zero, ErrConflict
			}
			if supplied {
				snapshot, e := backupRemoteTargetSnapshot(ctx, tx, target, revision)
				if e != nil {
					return zero, e
				}
				a, e := backupRemoteCredentialRecord(ctx, tx, project, snapshot.Access, false)
				if e != nil {
					return zero, e
				}
				b, e := backupRemoteCredentialRecord(ctx, tx, project, snapshot.Secret, false)
				if e != nil {
					return zero, e
				}
				if compare(BackupRemoteCredentialRecords{Access: a, Secret: b}) != nil {
					return zero, ErrConflict
				}
			}
			return zero, databaseBackupFinalizeActor(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return zero, err
		}
		out := domain.HostingDatabaseBackupTarget{ID: id, ProjectID: project, Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: in.Name, Endpoint: in.Endpoint, Bucket: in.Bucket, Region: in.Region, Enabled: in.Enabled, CreatedAt: now, UpdatedAt: now}
		var snapshot domain.HostingDatabaseBackupTargetSnapshot
		if existing != "" {
			var current int64
			var savedProject string
			if err = tx.QueryRow(ctx, `SELECT project_id,revision,created_at FROM hosting_database_backup_targets WHERE id=$1 FOR UPDATE`, existing).Scan(&savedProject, &current, &out.CreatedAt); err != nil {
				return zero, hostingError(err)
			}
			if savedProject != project || current != in.ExpectedRevision || current == int64(^uint64(0)>>1) {
				return zero, ErrConflict
			}
			out.ID = existing
			out.Revision = current + 1
			if snapshot, err = backupRemoteTargetSnapshot(ctx, tx, existing, current); err != nil {
				return zero, err
			}
		} else {
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM hosting_database_backup_targets WHERE project_id=$1`, project).Scan(&count); err != nil {
				return zero, err
			}
			if count >= 100 {
				return zero, ErrConflict
			}
			_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_targets(id,project_id,revision,enabled,created_at,updated_at) VALUES($1,$2,1,$3,$4,$4)`, out.ID, project, out.Enabled, now)
			if err != nil {
				return zero, hostingError(err)
			}
		}
		if records != nil {
			if err = insertBackupRemoteCredential(ctx, tx, project, actor, records.Access); err != nil {
				return zero, err
			}
			if err = insertBackupRemoteCredential(ctx, tx, project, actor, records.Secret); err != nil {
				return zero, err
			}
			// Replacing credentials explicitly revokes the previous references;
			// editing only endpoint/name/bucket leaves historical keys untouched.
			if existing != "" {
				if _, err = tx.Exec(ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=ANY($1::text[]) AND status='active'`, []string{snapshot.Access.SecretID, snapshot.Secret.SecretID}); err != nil {
					return zero, err
				}
			}
			snapshot.Access = domain.BackupRemoteCredentialRef{SecretID: records.Access.Metadata.SecretID, Version: 1}
			snapshot.Secret = domain.BackupRemoteCredentialRef{SecretID: records.Secret.Metadata.SecretID, Version: 1}
		}
		snapshot.Target = out
		if snapshot.Validate() != nil {
			return zero, ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_target_versions(target_id,revision,actor_id,snapshot,access_id,secret_id) VALUES($1,$2,$3,$4,$5,$6)`, out.ID, out.Revision, actor, buildJSON(snapshot), snapshot.Access.SecretID, snapshot.Secret.SecretID)
		if err != nil {
			return zero, hostingError(err)
		}
		if existing != "" {
			if _, err = tx.Exec(ctx, `UPDATE hosting_database_backup_targets SET revision=$2,enabled=$3,updated_at=$4 WHERE id=$1`, out.ID, out.Revision, out.Enabled, now); err != nil {
				return zero, err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_target_requests(project_id,request_id,actor_id,request_hash,target_id,revision,credentials_supplied,response) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, project, in.RequestID, actor, hash, out.ID, out.Revision, records != nil, buildJSON(out))
		if err != nil {
			return zero, err
		}
		if err = databaseBackupAudit(ctx, tx, actor, out.ID, "remote_target_"+in.RequestID, project); err != nil {
			return zero, err
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

func (s *PostgresStore) ListDatabaseBackupTargets(ctx context.Context, actor, project string) ([]domain.HostingDatabaseBackupTarget, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupTarget, error) {
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT v.snapshot FROM hosting_database_backup_targets t JOIN hosting_database_backup_target_versions v ON v.target_id=t.id AND v.revision=t.revision WHERE t.project_id=$1 ORDER BY t.created_at,t.id LIMIT 100`, project)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]domain.HostingDatabaseBackupTarget, 0)
		for rows.Next() {
			var raw []byte
			var snap domain.HostingDatabaseBackupTargetSnapshot
			if rows.Scan(&raw) != nil || json.Unmarshal(raw, &snap) != nil || snap.Validate() != nil {
				return nil, ErrConflict
			}
			out = append(out, snap.Target)
		}
		if rows.Err() != nil {
			return nil, rows.Err()
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
