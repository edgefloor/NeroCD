package hostingjobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riverqueue/river"
)

type proxyTestHandler struct{ called string }

func (h *proxyTestHandler) PrepareProxy(_ context.Context, id string) error {
	h.called = id
	return nil
}

func TestProxyDispatchUsesOnlyOpaqueOperationIdentity(t *testing.T) {
	args := PrepareProxyArgs{OperationID: "pop_one"}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != `{"operation_id":"pop_one"}` || args.Kind() != "hosting.prepare_proxy.v1" {
		t.Fatalf("job protocol %s %v", encoded, err)
	}
	handler := &proxyTestHandler{}
	worker := prepareProxyWorker{handler: handler}
	if err := worker.Work(t.Context(), &river.Job[PrepareProxyArgs]{Args: args}); err != nil || handler.called != args.OperationID {
		t.Fatalf("real handler was not invoked: %q %v", handler.called, err)
	}
	handler.called = ""
	if err := worker.Work(t.Context(), &river.Job[PrepareProxyArgs]{Args: PrepareProxyArgs{OperationID: "../foreign"}}); err == nil || handler.called != "" {
		t.Fatalf("invalid identity reached handler: %q %v", handler.called, err)
	}
	if _, err := (&Client{}).EnqueueProxyTx(t.Context(), nil, args); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("client without real proxy handler queued work: %v", err)
	}
}
