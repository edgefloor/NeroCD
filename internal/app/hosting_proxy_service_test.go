package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type proxyViewRepository struct {
	store.RunRepository
	store.HostingProxyRepository
	op     domain.HostingProxyOperation
	routes map[string]store.HostingProxyRouteRecord
}

func (r *proxyViewRepository) GetHostingProxyOperation(context.Context, string) (domain.HostingProxyOperation, error) {
	return r.op, nil
}

func (r *proxyViewRepository) GetHostingRoute(_ context.Context, id string) (store.HostingProxyRouteRecord, error) {
	route, ok := r.routes[id]
	if !ok {
		return route, store.ErrNotFound
	}
	return route, nil
}

func TestProxyOperationProjectProjectionDoesNotExposeOtherProjects(t *testing.T) {
	service := newTestService()
	repository := &proxyViewRepository{RunRepository: service.runs, op: domain.HostingProxyOperation{
		ID: "pop_shared", ProxyID: "proxy_private", ProjectID: "proj_platform", State: "applying", RunID: "proxy_pop_shared", MutationHold: true,
		Snapshot: domain.HostingProxySnapshot{ProxyID: "proxy_private", ServerID: "srv_private", DestinationID: "dest_private", RunnerID: "runner_private", Routes: []domain.HostingProxyRouteTarget{
			{RouteID: "route_own", ProjectID: "proj_security"},
			{RouteID: "route_foreign", ProjectID: "proj_platform"},
		}},
	}, routes: map[string]store.HostingProxyRouteRecord{
		"route_own":     {ProjectID: "proj_security", Route: domain.HostingRoute{ID: "route_own", ResourceID: "res_own", Hostname: "own.example.com"}},
		"route_foreign": {ProjectID: "proj_platform", Route: domain.HostingRoute{ID: "route_foreign", ResourceID: "res_foreign", Hostname: "foreign.example.com"}},
	}}
	service.runs = repository
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	result, err := service.ProxyOperation(viewer, repository.op.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := result.(domain.HostingProxyOperationView)
	if !ok || len(view.Routes) != 1 || view.Routes[0].ID != "route_own" || !view.MutationHold {
		t.Fatalf("unexpected projection: %+v", result)
	}
	_, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// The ordinary project endpoint preserves the proxy's applied state while
	// keeping the engine result private, even after successful completion.
	repository.op.State = "applied"
	repository.op.MutationHold = false
	repository.op.Result = &domain.HostingProxyResult{Outcome: "applied"}
	result, err = service.ProxyOperation(viewer, repository.op.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	if string(public["id"]) != `"pop_shared"` || string(public["state"]) != `"applied"` {
		t.Fatalf("ordinary operation public contract: %s", encoded)
	}
	for _, private := range []string{"engine_outcome", "result", "snapshot"} {
		if _, exposed := public[private]; exposed {
			t.Fatalf("ordinary project view exposes %s", private)
		}
	}
	t.Logf("ordinary proxy operation marshaled project contract: %s", encoded)
	for _, forbidden := range []string{"proxy_private", "srv_private", "dest_private", "runner_private", "route_foreign", "res_foreign", "foreign.example.com", "proj_platform", "snapshot", "plan", "run_id"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("project projection contains %q: %s", forbidden, encoded)
		}
	}
	if _, err := service.CancelProxyOperation(viewer, repository.op.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("project viewer acquired whole proxy cancellation authority: %v", err)
	}
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if detail, err := service.ProxyOperation(admin, repository.op.ID); err != nil {
		t.Fatal(err)
	} else if _, ok := detail.(domain.HostingProxyOperation); !ok {
		t.Fatalf("administrator detail type %T", detail)
	}
}
