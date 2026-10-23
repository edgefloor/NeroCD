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

type routeSwitchServiceRepository struct {
	*proxyViewRepository
	calls int
	input domain.HostingRouteSwitchInput
}

func (r *routeSwitchServiceRepository) RequestHostingRouteSwitch(_ context.Context, resource, operation, actor string, in domain.HostingRouteSwitchInput, _ domain.AuditEvent) (domain.HostingProxyOperation, error) {
	r.calls++
	r.input = in
	return r.op, nil
}

func TestRouteSwitchServiceProjectProjection(t *testing.T) {
	service, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	environment, err := service.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "switch"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "switch", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &routeSwitchServiceRepository{proxyViewRepository: &proxyViewRepository{RunRepository: service.runs, op: domain.HostingProxyOperation{ID: "pop_switch", ProjectID: "proj_platform", ProxyID: "proxy_private", RunID: "private_run", State: "queued", Snapshot: domain.HostingProxySnapshot{ServerID: "private_server", Routes: []domain.HostingProxyRouteTarget{{RouteID: "route_own", ProjectID: "proj_platform"}, {RouteID: "route_other", ProjectID: "proj_security"}}}}, routes: map[string]store.HostingProxyRouteRecord{}}}
	repository.routes["route_own"] = store.HostingProxyRouteRecord{ProjectID: "proj_platform", Route: domain.HostingRoute{ID: "route_own", ResourceID: resource.ID, Hostname: "own.example.com"}}
	repository.routes["route_other"] = store.HostingProxyRouteRecord{ProjectID: "proj_security", Route: domain.HostingRoute{ID: "route_other", ResourceID: "private_resource", Hostname: "other.example.com"}}
	service.runs = repository
	input := domain.HostingRouteSwitchInput{IdempotencyKey: "switch", ExpectedLoadedGeneration: 1, TargetReleaseOperationID: "hop_b", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_z", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_a"}, {RouteID: "route_a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_a"}}}
	view, err := service.RequestHostingRouteSwitch(admin, resource.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || repository.input.Routes[0].RouteID != "route_a" || input.Routes[0].RouteID != "route_z" {
		t.Fatal("service failed canonical copied scope")
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"proxy_private", "private_run", "private_server", "snapshot", "plan"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("unsafe response %s", encoded)
		}
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = service.RequestHostingRouteSwitch(viewer, resource.ID, input); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer switch: %v", err)
	}
	if repository.calls != 1 {
		t.Fatal("unauthorized request reached mutation")
	}
}
