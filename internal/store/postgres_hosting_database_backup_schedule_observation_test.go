package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/auth"
)

func TestDatabaseBackupScheduleControllerObservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		class string
	}{
		{"unknown", errors.New("password=CANARY_PRIVATE\nforged log"), "unavailable"},
		{"wrapped_constraint", fmt.Errorf("CANARY_PRIVATE: %w", &pgconn.PgError{Code: "23514", Message: "CANARY_PRIVATE", Detail: "CANARY_PRIVATE"}), "database_constraint"},
		{"serialization", &pgconn.PgError{Code: "40001", Message: "CANARY_PRIVATE"}, "transaction_conflict"},
		{"deadline", context.DeadlineExceeded, "deadline"},
		{"revocation", auth.ErrForbidden, "authority_denied"},
		{"foreign_sqlstate", &pgconn.PgError{Code: "CANARY_PRIVATE", Message: "CANARY_PRIVATE"}, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			var observer databaseBackupScheduleObserver
			now := time.Unix(1000, 0)
			calls := 0
			pass := func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("controller attempt lost bounded context")
				}
				return tc.err
			}
			observer.run(t.Context(), now, logger, pass)
			var event map[string]any
			if json.Unmarshal(output.Bytes(), &event) != nil || event["error_class"] != tc.class || event["level"] != "WARN" || len(event) != 4 || strings.Contains(output.String(), "CANARY_PRIVATE") {
				t.Fatal("unsafe or missing finite controller diagnostic")
			}
			first := output.String()
			observer.run(t.Context(), now.Add(5*time.Second), logger, pass)
			if calls != 2 || output.String() != first {
				t.Fatal("report limiting skipped work or emitted repeatedly")
			}
			observer.run(t.Context(), now.Add(time.Minute), logger, pass)
			if calls != 3 || strings.Count(output.String(), "controller pass failed") != 2 {
				t.Fatal("later failed pass not visible")
			}
		})
	}
}

func TestDatabaseBackupScheduleControllerObservationSuppressesShutdown(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	var observer databaseBackupScheduleObserver
	ctx, cancel := context.WithCancel(t.Context())
	observer.run(ctx, time.Now(), logger, func(context.Context) error { cancel(); return errors.New("CANARY_PRIVATE") })
	observer.run(t.Context(), time.Now(), logger, func(context.Context) error { return nil })
	if output.Len() != 0 {
		t.Fatal("normal shutdown or success emitted failure")
	}
}
