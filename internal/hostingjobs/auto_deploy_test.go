package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"
)

type automaticHandlerFixture struct {
	f func(context.Context, string) error
}

func (h automaticHandlerFixture) PrepareHostingAutoDeploy(ctx context.Context, id string) error {
	return h.f(ctx, id)
}
func TestAutomaticDeploymentWakeup(t *testing.T) {
	args := AutoDeployArgs{ResourceID: "res_owned"}
	raw, err := json.Marshal(args)
	if err != nil || string(raw) != `{"resource_id":"res_owned"}` || args.Kind() != AutoDeployKind || args.InsertOpts().MaxAttempts != 3 {
		t.Fatal("opaque bounded args", string(raw), err)
	}
	for _, mode := range []string{"ok", "invalid", "failure", "panic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			w := autoDeployWorker{handler: automaticHandlerFixture{f: func(_ context.Context, id string) error {
				calls++
				if id != args.ResourceID {
					t.Fatal("changed identity")
				}
				if mode == "failure" {
					return errors.New("private diagnostic")
				}
				if mode == "panic" {
					panic("private diagnostic")
				}
				return nil
			}}}
			in := args
			if mode == "invalid" {
				in.ResourceID = "../other"
			}
			err := w.Work(t.Context(), &river.Job[AutoDeployArgs]{Args: in})
			if mode == "ok" && (err != nil || calls != 1) || mode != "ok" && (err == nil || strings.Contains(err.Error(), "private diagnostic")) || mode == "invalid" && calls != 0 {
				t.Fatal("worker boundary", err, calls)
			}
		})
	}
	if _, err = (&Client{}).EnqueueAutoDeployTx(t.Context(), nil, args); !errors.Is(err, ErrUnavailable) {
		t.Fatal("absent supplied controller", err)
	}
}
