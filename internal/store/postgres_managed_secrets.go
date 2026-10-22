package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

func managedSecretFromSQL(v sqlcgen.ProjectManagedSecret) domain.ManagedSecret {
	return domain.ManagedSecret{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, CurrentVersion: v.CurrentVersion, Status: v.Status, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, RevokedAt: v.RevokedAt}
}

// ListManagedSecrets returns a bounded page of project metadata.
func (s *PostgresStore) ListManagedSecrets(ctx context.Context, project string, page Page) (PageResult[domain.ManagedSecret], error) {
	total, err := s.queries.CountManagedSecrets(ctx, project)
	if err != nil {
		return PageResult[domain.ManagedSecret]{}, managedSecretError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListManagedSecrets(ctx, sqlcgen.ListManagedSecretsParams{ProjectID: project, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.ManagedSecret]{}, managedSecretError(err)
	}
	items := make([]domain.ManagedSecret, 0, len(rows))
	for _, row := range rows {
		items = append(items, managedSecretFromSQL(row))
	}
	return PageResult[domain.ManagedSecret]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// GetManagedSecret retrieves metadata only.
func (s *PostgresStore) GetManagedSecret(ctx context.Context, id string) (domain.ManagedSecret, error) {
	row, err := s.queries.GetManagedSecret(ctx, id)
	if err != nil {
		return domain.ManagedSecret{}, managedSecretError(err)
	}
	return managedSecretFromSQL(row), nil
}

// ListManagedSecretVersions never queries encrypted columns.
func (s *PostgresStore) ListManagedSecretVersions(ctx context.Context, id string, page Page) (PageResult[domain.ManagedSecretVersion], error) {
	total, err := s.queries.CountManagedSecretVersions(ctx, id)
	if err != nil {
		return PageResult[domain.ManagedSecretVersion]{}, managedSecretError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListManagedSecretVersions(ctx, sqlcgen.ListManagedSecretVersionsParams{SecretID: id, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.ManagedSecretVersion]{}, managedSecretError(err)
	}
	items := make([]domain.ManagedSecretVersion, 0, len(rows))
	for _, v := range rows {
		items = append(items, domain.ManagedSecretVersion{SecretID: v.SecretID, Version: v.Version, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
	}
	return PageResult[domain.ManagedSecretVersion]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// GetManagedSecretRecord returns encrypted storage data for internal decryption, without a reveal grant.
func (s *PostgresStore) GetManagedSecretRecord(ctx context.Context, id string, version int64) (ManagedSecretRecord, error) {
	v, err := s.queries.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: id, Version: version})
	if err != nil {
		return ManagedSecretRecord{}, managedSecretError(err)
	}
	return ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: v.SecretID, Version: v.Version, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(v.EnvelopeVersion), KeyID: v.KeyID, Data: v.Ciphertext}}, nil
}
func appendManagedRecord(ctx context.Context, q *sqlcgen.Queries, v ManagedSecretRecord) error {
	return q.AppendManagedSecretVersion(ctx, sqlcgen.AppendManagedSecretVersionParams{SecretID: v.Metadata.SecretID, Version: v.Metadata.Version, CreatedBy: v.Metadata.CreatedBy, CreatedAt: v.Metadata.CreatedAt, EnvelopeVersion: int32(v.Envelope.Version), KeyID: v.Envelope.KeyID, Ciphertext: v.Envelope.Data})
}
func commitManagedAudit(ctx context.Context, q *sqlcgen.Queries, event domain.AuditEvent, action string, item domain.ManagedSecret, version int64) error {
	event, err := managedSecretAudit(event, action, item, version)
	if err != nil {
		return err
	}
	return createAuditWithQueries(ctx, q, event)
}

// CreateManagedSecret commits metadata, encrypted version one, and audit together.
func (s *PostgresStore) CreateManagedSecret(ctx context.Context, item domain.ManagedSecret, record ManagedSecretRecord, event domain.AuditEvent) (domain.ManagedSecret, error) {
	if item.ID == "" || domain.ValidateHostingName(item.Name) != nil || !validManagedRecord(record) || record.Metadata.SecretID != item.ID || record.Metadata.Version != 1 || record.Metadata.CreatedBy != item.CreatedBy {
		return domain.ManagedSecret{}, ErrConflict
	}
	return managedSecretMutation(ctx, s, func(q *sqlcgen.Queries) (domain.ManagedSecret, error) {
		row, err := q.CreateManagedSecret(ctx, sqlcgen.CreateManagedSecretParams{ID: item.ID, ProjectID: item.ProjectID, Name: item.Name, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt})
		if err != nil {
			return domain.ManagedSecret{}, err
		}
		if err = appendManagedRecord(ctx, q, record); err != nil {
			return domain.ManagedSecret{}, err
		}
		result := managedSecretFromSQL(row)
		if err = commitManagedAudit(ctx, q, event, "managed_secret.created", result, 1); err != nil {
			return domain.ManagedSecret{}, err
		}
		return result, nil
	})
}

// RotateManagedSecret locks current state before comparing and appending a version.
func (s *PostgresStore) RotateManagedSecret(ctx context.Context, id string, expected int64, record ManagedSecretRecord, event domain.AuditEvent) (domain.ManagedSecret, error) {
	if !validManagedExpected(expected) || !validManagedRecord(record) || record.Metadata.SecretID != id || record.Metadata.Version != expected+1 {
		return domain.ManagedSecret{}, ErrConflict
	}
	return managedSecretMutation(ctx, s, func(q *sqlcgen.Queries) (domain.ManagedSecret, error) {
		old, err := q.LockManagedSecret(ctx, id)
		if err != nil {
			return domain.ManagedSecret{}, err
		}
		if old.Status != "active" || old.CurrentVersion != expected {
			return domain.ManagedSecret{}, ErrConflict
		}
		if err = appendManagedRecord(ctx, q, record); err != nil {
			return domain.ManagedSecret{}, err
		}
		row, err := q.AdvanceManagedSecret(ctx, sqlcgen.AdvanceManagedSecretParams{ID: id, CurrentVersion: expected, UpdatedAt: record.Metadata.CreatedAt})
		if err != nil {
			return domain.ManagedSecret{}, err
		}
		result := managedSecretFromSQL(row)
		if err = commitManagedAudit(ctx, q, event, "managed_secret.rotated", result, expected+1); err != nil {
			return domain.ManagedSecret{}, err
		}
		return result, nil
	})
}

// RevokeManagedSecret locks the same metadata row as reveal and cannot be reversed.
func (s *PostgresStore) RevokeManagedSecret(ctx context.Context, id string, expected int64, event domain.AuditEvent) (domain.ManagedSecret, error) {
	if expected < 1 {
		return domain.ManagedSecret{}, ErrConflict
	}
	return managedSecretMutation(ctx, s, func(q *sqlcgen.Queries) (domain.ManagedSecret, error) {
		old, err := q.LockManagedSecret(ctx, id)
		if err != nil {
			return domain.ManagedSecret{}, err
		}
		if old.Status != "active" || old.CurrentVersion != expected {
			return domain.ManagedSecret{}, ErrConflict
		}
		row, err := q.RevokeManagedSecret(ctx, sqlcgen.RevokeManagedSecretParams{ID: id, CurrentVersion: expected, RevokedAt: &event.CreatedAt})
		if err != nil {
			return domain.ManagedSecret{}, err
		}
		result := managedSecretFromSQL(row)
		if err = commitManagedAudit(ctx, q, event, "managed_secret.revoked", result, expected); err != nil {
			return domain.ManagedSecret{}, err
		}
		return result, nil
	})
}

// AuthorizeManagedSecretReveal commits a mandatory fresh audit after checking active state and version under lock.
func (s *PostgresStore) AuthorizeManagedSecretReveal(ctx context.Context, id string, version int64, event domain.AuditEvent) error {
	_, err := managedSecretMutation(ctx, s, func(q *sqlcgen.Queries) (bool, error) {
		row, err := q.LockManagedSecret(ctx, id)
		if err != nil {
			return false, err
		}
		if row.Status != "active" {
			return false, ErrConflict
		}
		if _, err = q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: id, Version: version}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, ErrNotFound
			}
			return false, err
		}
		if err = commitManagedAudit(ctx, q, event, "managed_secret.revealed", managedSecretFromSQL(row), version); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

// Persistence failures must never expose database detail that could include an encrypted row.
func managedSecretError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514", "22003":
			return ErrConflict
		}
	}
	return errors.New("managed secret persistence failed")
}
func managedSecretMutation[T any](ctx context.Context, s *PostgresStore, mutate func(*sqlcgen.Queries) (T, error)) (T, error) {
	var zero T
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, managedSecretError(err)
	}
	defer rollbackTransaction(ctx, tx)
	result, err := mutate(s.queries.WithTx(tx))
	if err != nil {
		return zero, managedSecretError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, managedSecretError(err)
	}
	return result, nil
}
