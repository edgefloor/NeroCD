package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/riverqueue/river"
	"strings"
	"testing"
)

type pushTestHandler struct {
	run func(context.Context, string) error
}

func (h pushTestHandler) PrepareGitHubPush(ctx context.Context, id string) error {
	return h.run(ctx, id)
}
func TestGitHubPushOpaqueProtocolAndClosedWorkerErrors(t *testing.T) {
	args := GitHubPushArgs{IntentID: "gpd_0123456789abcdef0123456789abcdef"}
	raw, err := json.Marshal(args)
	if err != nil || string(raw) != `{"intent_id":"gpd_0123456789abcdef0123456789abcdef"}` || args.Kind() != GitHubPushKind {
		t.Fatal("nonopaque job protocol")
	}
	for _, mode := range []string{"ok", "error", "panic", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			w := githubPushWorker{handler: pushTestHandler{run: func(context.Context, string) error {
				calls++
				if mode == "panic" {
					panic("private diagnostic")
				}
				if mode == "error" {
					return errors.New("private diagnostic")
				}
				return nil
			}}}
			in := args
			if mode == "invalid" {
				in.IntentID = "../foreign"
			}
			err := w.Work(t.Context(), &river.Job[GitHubPushArgs]{Args: in})
			if mode == "ok" && (err != nil || calls != 1) || mode != "ok" && (err == nil || strings.Contains(err.Error(), "private diagnostic")) || mode == "invalid" && calls != 0 {
				t.Fatal("worker boundary", err, calls)
			}
		})
	}
	if _, err = (&Client{}).EnqueueGitHubPushTx(t.Context(), nil, args); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
