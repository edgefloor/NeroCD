package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"
)

type connectionHandlerFixture func(context.Context, string) error

func (h connectionHandlerFixture) PrepareHostingDatabaseConnection(ctx context.Context, id string) error {
	return h(ctx, id)
}

func TestDatabaseConnectionContinuationBoundary(t *testing.T) {
	args := DatabaseConnectionArgs{ConnectionID: "hcn_owned"}
	raw, err := json.Marshal(args)
	if err != nil || string(raw) != `{"connection_id":"hcn_owned"}` || args.Kind() != DatabaseConnectionKind {
		t.Fatal("continuation leaked inputs or lost identity", err)
	}
	for _, mode := range []string{"ok", "waiting", "invalid", "failure", "panic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			worker := databaseConnectionWorker{handler: connectionHandlerFixture(func(ctx context.Context, id string) error {
				calls++
				if ctx != t.Context() || id != args.ConnectionID {
					t.Fatal("changed request context or child identity")
				}
				switch mode {
				case "waiting":
					return ErrDatabaseConnectionWaiting
				case "failure":
					return errors.New("private diagnostic canary")
				case "panic":
					panic("private diagnostic canary")
				}
				return nil
			})}
			input := args
			if mode == "invalid" {
				input.ConnectionID = "../foreign"
			}
			err := worker.Work(t.Context(), &river.Job[DatabaseConnectionArgs]{Args: input})
			if mode == "ok" && (err != nil || calls != 1) || mode != "ok" && (err == nil || strings.Contains(err.Error(), "private diagnostic canary")) || mode == "invalid" && calls != 0 {
				t.Fatal("worker failure boundary", err, calls)
			}
			if mode == "waiting" && err.Error() != river.JobSnooze(1_000_000_000).Error() {
				t.Fatal("waiting consumed an ordinary failure retry", err)
			}
		})
	}
	if _, err = (&Client{}).EnqueueDatabaseConnectionTx(t.Context(), nil, args); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsupported backend reported success", err)
	}
}
