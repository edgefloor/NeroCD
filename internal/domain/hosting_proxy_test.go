package domain

import "testing"

func TestHostingProxyHostnameAndPathBoundaries(t *testing.T) {
	if host, err := NormalizeHostingHostname("BÜCHER.example"); err != nil || host != "xn--bcher-kva.example" {
		t.Fatalf("IDNA %q %v", host, err)
	}
	for _, host := range []string{"*.example.com", "https://example.com", "example.com:80", " example.com", "example.com.", "localhost", "example.com\n"} {
		if _, err := NormalizeHostingHostname(host); err == nil {
			t.Fatalf("unsafe host accepted %q", host)
		}
	}
	for _, path := range []string{"/", "/api", "/api/v2"} {
		if err := ValidateHostingRoutePath(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"//api", "/api/", "/api/../admin", "/api?x=1", "/api%2Fother", "/.well-known/acme-challenge", "/_nerocd", "/dashboard"} {
		if ValidateHostingRoutePath(path) == nil {
			t.Fatalf("unsafe path accepted %q", path)
		}
	}
	if HostingHostnameGranted("example.com", true, "example.com") || !HostingHostnameGranted("example.com", true, "nested.app.example.com") {
		t.Fatal("wildcard delegation confused apex or descendants")
	}
	if !HostingHostnameGrantsOverlap("example.com", true, "app.example.com", false) || HostingHostnameGrantsOverlap("example.com", false, "other.example.com", false) {
		t.Fatal("hostname authority overlap incorrect")
	}
}

func TestHostingProxyTaskCannotEnterPublicWorkflow(t *testing.T) {
	run := TaskRun{ID: "proxy_pop_one", RunSpec: RunSpec{Type: RunTypeProxyApply, ProxyOperationID: "pop_one", ProxyMode: "apply"}}
	if err := ValidateProxyApplyTask(run); err != nil {
		t.Fatal(err)
	}
	if !ContainsHostingExecution(run.RunSpec, Workflow{}) {
		t.Fatal("proxy task became public")
	}
	for _, spec := range []RunSpec{{Type: "shell", ProxyOperationID: "pop_one"}, {Type: "shell", ProxyMode: "apply"}, {Type: "shell", Workflow: &Workflow{Steps: []WorkflowStep{{ID: "hidden", RunSpec: run.RunSpec}}}}} {
		if !ContainsHostingExecution(spec, Workflow{}) {
			t.Fatal("nested proxy authority became public")
		}
	}
	run.RunSpec.Inputs = map[string]any{"url": "http://arbitrary.invalid"}
	if ValidateProxyApplyTask(run) == nil {
		t.Fatal("internal primitive input accepted")
	}
}

func TestHostingProxyPlanFreezesCanonicalIdentityAndRoutes(t *testing.T) {
	snapshot := HostingProxySnapshot{ProxyID: "proxy_one", ServerID: "srv_one", DestinationID: "dest_one", RunnerID: "runner_one", Generation: 2, Configuration: HostingProxyConfiguration{Email: "operator@example.com", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []HostingProxyRouteTarget{
		{RouteID: "route_b", ProjectID: "proj_one", ResourceID: "res_b", BindingID: "bind_b", ReleaseOperationID: "hop_b", Hostname: "b.example.com", PathPrefix: "/api", HostPort: 18081},
		{RouteID: "route_a", ProjectID: "proj_one", ResourceID: "res_a", BindingID: "bind_a", ReleaseOperationID: "hop_a", Hostname: "a.example.com", PathPrefix: "/", HostPort: 18080},
	}}
	plan, err := CompileHostingProxyPlan(snapshot, 1, "daemon_one")
	if err != nil || ValidateHostingProxyPlan(plan) != nil {
		t.Fatalf("canonical plan: %+v %v", plan, err)
	}
	if snapshot.Routes[0].RouteID != "route_b" || plan.Snapshot.Routes[0].RouteID != "route_a" {
		t.Fatal("compiler modified input or did not sort routes")
	}
	snapshot.Routes[1].HostPort = 19000
	if plan.Snapshot.Routes[0].HostPort != 18080 {
		t.Fatal("plan aliases caller route storage")
	}
	for _, mutate := range []func(*HostingProxyPlan){
		func(p *HostingProxyPlan) { p.CompilerVersion = "old.v0" },
		func(p *HostingProxyPlan) { p.Hash = "wrong" },
		func(p *HostingProxyPlan) { p.Snapshot.Routes[0].HostPort++ },
		func(p *HostingProxyPlan) { p.Snapshot.ProxyID = "../foreign" },
		func(p *HostingProxyPlan) { p.DaemonID = "daemon\nforeign" },
		func(p *HostingProxyPlan) { p.Snapshot.Routes[1].RouteID = p.Snapshot.Routes[0].RouteID },
	} {
		changed := plan
		changed.Snapshot.Routes = append([]HostingProxyRouteTarget(nil), plan.Snapshot.Routes...)
		mutate(&changed)
		if ValidateHostingProxyPlan(changed) == nil {
			t.Fatalf("invalid plan accepted: %+v", changed)
		}
	}
}
