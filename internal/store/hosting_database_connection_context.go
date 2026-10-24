package store

import (
	"context"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/store/sqlcgen"
)

type databaseConnectionCredentialKey struct{}
type databaseConnectionCredential struct {
	runner, hash string
}

// BindHostingDatabaseConnectionRunnerContext is an internal trust bridge for an
// already authenticated service request. Never call it with public input or a
// plaintext bearer. Only the runner and SHA-256 fingerprint live in the context.
func BindHostingDatabaseConnectionRunnerContext(ctx context.Context, runner, fingerprint string) (context.Context, error) {
	decoded, err := hex.DecodeString(fingerprint)
	if ctx == nil || runner == "" || err != nil || len(decoded) != 32 {
		return nil, ErrConflict
	}
	want := databaseConnectionCredential{runner: runner, hash: fingerprint}
	if old, ok := ctx.Value(databaseConnectionCredentialKey{}).(databaseConnectionCredential); ok {
		if old != want {
			return nil, ErrConflict // A request cannot refresh its original credential.
		}
		return ctx, nil
	}
	return context.WithValue(ctx, databaseConnectionCredentialKey{}, want), nil
}

func databaseConnectionContextCredential(ctx context.Context, runner string) (string, error) {
	if ctx == nil {
		return "", ErrConflict
	}
	credential, ok := ctx.Value(databaseConnectionCredentialKey{}).(databaseConnectionCredential)
	if !ok || credential.runner != runner || credential.hash == "" {
		return "", ErrConflict
	}
	return credential.hash, nil
}

func checkDatabaseConnectionRunnerContext(ctx context.Context, tx pgx.Tx, runner string) error {
	fingerprint, err := databaseConnectionContextCredential(ctx, runner)
	if err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	current, err := q.LockObservationRunner(ctx, runner)
	if err != nil || current.Status != "active" || current.TokenHash != fingerprint {
		return ErrConflict
	}
	_, err = q.LockObservationEnrollment(ctx, sqlcgen.LockObservationEnrollmentParams{RunnerID: runner, CredentialHash: &fingerprint})
	return hostingError(err)
}
