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

func backupRemoteTargetSnapshot(ctx context.Context, tx pgx.Tx, id string, revision int64) (domain.HostingDatabaseBackupTargetSnapshot, error) {
	var raw []byte
	var out domain.HostingDatabaseBackupTargetSnapshot
	raw, err := sqlcgen.New(tx).ReadDatabaseBackupTargetSnapshot(ctx, sqlcgen.ReadDatabaseBackupTargetSnapshotParams{TargetID: id, Revision: int64(revision)})
	if err != nil {
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
	lockDatabaseBackupRemoteCredentialRow, err := sqlcgen.New(tx).LockDatabaseBackupRemoteCredential(ctx, sqlcgen.LockDatabaseBackupRemoteCredentialParams{ID: ref.SecretID, ProjectID: project, Version: int64(ref.Version)})
	status = lockDatabaseBackupRemoteCredentialRow.Status
	version = int(lockDatabaseBackupRemoteCredentialRow.EnvelopeVersion)
	out.Envelope.KeyID = lockDatabaseBackupRemoteCredentialRow.KeyID
	out.Envelope.Data = lockDatabaseBackupRemoteCredentialRow.Ciphertext
	out.Metadata.CreatedBy = lockDatabaseBackupRemoteCredentialRow.CreatedBy
	out.Metadata.CreatedAt = lockDatabaseBackupRemoteCredentialRow.CreatedAt
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
	err := sqlcgen.New(tx).CreateDatabaseBackupRemoteCredential(ctx, sqlcgen.CreateDatabaseBackupRemoteCredentialParams{ID: record.Metadata.SecretID, ProjectID: project, EnvelopeVersion: int32(record.Envelope.Version), KeyID: record.Envelope.KeyID, Ciphertext: record.Envelope.Data, CreatedBy: actor, CreatedAt: record.Metadata.CreatedAt})
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
		_, err := sqlcgen.New(tx).LockDatabaseBackupTargetProject(ctx, project)
		if err != nil {
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
		readDatabaseBackupTargetRequestRow, err := sqlcgen.New(tx).ReadDatabaseBackupTargetRequest(ctx, sqlcgen.ReadDatabaseBackupTargetRequestParams{ProjectID: project, RequestID: in.RequestID})
		savedHash = readDatabaseBackupTargetRequestRow.RequestHash
		savedActor = readDatabaseBackupTargetRequestRow.ActorID
		target = readDatabaseBackupTargetRequestRow.TargetID
		revision = readDatabaseBackupTargetRequestRow.Revision
		supplied = readDatabaseBackupTargetRequestRow.CredentialsSupplied
		response = readDatabaseBackupTargetRequestRow.Response
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
		now, err = sqlcgen.New(tx).ReadDatabaseBackupTargetClock(ctx)
		if err != nil {
			return zero, err
		}
		out := domain.HostingDatabaseBackupTarget{ID: id, ProjectID: project, Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: in.Name, Endpoint: in.Endpoint, Bucket: in.Bucket, Region: in.Region, Enabled: in.Enabled, CreatedAt: now, UpdatedAt: now}
		var snapshot domain.HostingDatabaseBackupTargetSnapshot
		if existing != "" {
			var current int64
			var savedProject string
			lockDatabaseBackupTargetRow, err := sqlcgen.New(tx).LockDatabaseBackupTarget(ctx, existing)
			savedProject = lockDatabaseBackupTargetRow.ProjectID
			current = lockDatabaseBackupTargetRow.Revision
			out.CreatedAt = lockDatabaseBackupTargetRow.CreatedAt
			if err != nil {
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
			var count int64
			count, err = sqlcgen.New(tx).CountDatabaseBackupTargets(ctx, project)
			if err != nil {
				return zero, err
			}
			if count >= 100 {
				return zero, ErrConflict
			}
			err = sqlcgen.New(tx).CreateDatabaseBackupTarget(ctx, sqlcgen.CreateDatabaseBackupTargetParams{ID: out.ID, ProjectID: project, Enabled: out.Enabled, CreatedAt: now})
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
				if err = sqlcgen.New(tx).RevokeDatabaseBackupRemoteCredentials(ctx, []string{snapshot.Access.SecretID, snapshot.Secret.SecretID}); err != nil {
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
		err = sqlcgen.New(tx).CreateDatabaseBackupTargetVersion(ctx, sqlcgen.CreateDatabaseBackupTargetVersionParams{TargetID: out.ID, Revision: int64(out.Revision), ActorID: actor, Snapshot: buildJSON(snapshot), AccessID: snapshot.Access.SecretID, SecretID: snapshot.Secret.SecretID})
		if err != nil {
			return zero, hostingError(err)
		}
		if existing != "" {
			if err = sqlcgen.New(tx).UpdateDatabaseBackupTarget(ctx, sqlcgen.UpdateDatabaseBackupTargetParams{ID: out.ID, Revision: int64(out.Revision), Enabled: out.Enabled, UpdatedAt: now}); err != nil {
				return zero, err
			}
		}
		err = sqlcgen.New(tx).CreateDatabaseBackupTargetRequest(ctx, sqlcgen.CreateDatabaseBackupTargetRequestParams{ProjectID: project, RequestID: in.RequestID, ActorID: actor, RequestHash: hash, TargetID: out.ID, Revision: int64(out.Revision), CredentialsSupplied: records != nil, Response: buildJSON(out)})
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
		snapshots, err := sqlcgen.New(tx).ListDatabaseBackupTargetSnapshots(ctx, project)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupTarget, 0, len(snapshots))
		for _, raw := range snapshots {
			var snap domain.HostingDatabaseBackupTargetSnapshot
			if json.Unmarshal(raw, &snap) != nil || snap.Validate() != nil {
				return nil, ErrConflict
			}
			out = append(out, snap.Target)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
