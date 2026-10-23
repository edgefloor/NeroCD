package hostingproxy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestVerifiedSwitchPlan(t *testing.T) {
	routes := []Route{{"one", "app.example.test", "/api", 12000}}
	legacy, err := Prepare(testIdentity(), testSettings(), 2, 1, routes)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Hash() != "9cffee96230e38954d791c399d4d3232fca232640e39e8d17b3a9c59057407c3" || legacy.StaticHash() != "692bfb830ae0de4882aca2429ba27675418b1493606bf4bf001460185e8ef712" {
		t.Fatal("legacy serialized hash contract changed")
	}
	ids := []string{"one"}
	strict, err := PrepareVerifiedSwitch(testIdentity(), testSettings(), 2, 1, routes, ids)
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = "other"
	routes[0].HostPort = 13000
	if !strict.required("one") || strict.required("other") || strict.Routes()[0].HostPort != 12000 || strict.StaticHash() != legacy.StaticHash() || strict.Hash() == legacy.Hash() {
		t.Fatal("strict input ownership or hash separation failed")
	}
	for _, ids := range [][]string{nil, {""}, {"missing"}, {"one", "one"}} {
		if _, err = PrepareVerifiedSwitch(testIdentity(), testSettings(), 2, 1, legacy.Routes(), ids); err == nil {
			t.Fatalf("accepted invalid IDs %q", ids)
		}
	}
	if _, err = PrepareVerifiedSwitch(testIdentity(), testSettings(), 1, 0, legacy.Routes(), []string{"one"}); err == nil {
		t.Fatal("accepted absent predecessor")
	}
	routes = append(legacy.Routes(), Route{"two", "other.example.test", "/", 12001})
	a, _ := PrepareVerifiedSwitch(testIdentity(), testSettings(), 3, 2, routes, []string{"two", "one"})
	b, _ := PrepareVerifiedSwitch(testIdentity(), testSettings(), 3, 2, routes, []string{"one", "two"})
	c, _ := PrepareVerifiedSwitch(testIdentity(), testSettings(), 3, 2, routes, []string{"one"})
	if a.Hash() != b.Hash() || a.Hash() == c.Hash() || !reflect.DeepEqual(a.verification.RequiredRouteIDs, []string{"one", "two"}) {
		t.Fatal("required IDs are not sorted and hash-bound")
	}
}

func TestVerifiedSwitchScope(t *testing.T) {
	routes := []Route{{"a", "app.example.test", "/", 12000}, {"b", "app.example.test", "/api", 12001}}
	old := receipt{Generation: 1, Hash: digest("previous"), Routes: routes, Phase: "committed"}
	renamedRoutes := append([]Route(nil), routes...)
	renamedRoutes[0].ID = "renamed"
	renamed, err := PrepareVerifiedSwitch(testIdentity(), testSettings(), 2, 1, renamedRoutes, []string{"renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if err = renamed.switchPredecessor(old, false); err == nil {
		t.Fatal("selected route identity changed")
	}
	for _, tc := range []struct {
		name    string
		mutate  func([]Route) []Route
		allowed bool
	}{
		{"selected port", func(r []Route) []Route { r[0].HostPort++; return r }, true},
		{"unselected port", func(r []Route) []Route { r[1].HostPort++; return r }, false},
		{"hostname", func(r []Route) []Route { r[0].Hostname = "other.example.test"; return r }, false},
		{"path", func(r []Route) []Route { r[0].PathPrefix = "/new"; return r }, false},
		{"removed", func(r []Route) []Route { return r[:1] }, false},
		{"added", func(r []Route) []Route { return append(r, Route{"c", "other.example.test", "/", 12002}) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PrepareVerifiedSwitch(testIdentity(), testSettings(), 2, 1, tc.mutate(append([]Route(nil), routes...)), []string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			if err = p.switchPredecessor(old, false); (err == nil) != tc.allowed {
				t.Fatalf("scope check: %v", err)
			}
			if err = p.switchPredecessor(old, true); err == nil {
				t.Fatal("install accepted")
			}
			for _, invalid := range []receipt{{}, {Generation: 2, Phase: "committed", Routes: routes}, {Generation: 1, Phase: "intent", Routes: routes}} {
				if err = p.switchPredecessor(invalid, false); err == nil {
					t.Fatal("invalid predecessor accepted")
				}
			}
		})
	}
}

func testSettings() Settings {
	return Settings{Directory: "/tmp/nerocd-test", DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Email: "operator@example.test", CA: LetsEncryptStaging, ManagementPort: 18080}
}
func testIdentity() Identity { return Identity{"proxy", "server", "destination", "runner", "daemon"} }
func TestPrepareValidation(t *testing.T) {
	good := Route{"one", "app.example.test", "/api", 12000}
	for _, tc := range []struct {
		name  string
		route Route
	}{
		{"rule injection", Route{"one", "a.test`) || Host(`evil.test", "/", 12000}},
		{"wildcard", Route{"one", "*.example.test", "/", 12000}},
		{"URL target", Route{"one", "example.test", "http://evil.test", 12000}},
		{"escape path", Route{"one", "example.test", "/a/../api", 12000}},
		{"encoded slash", Route{"one", "example.test", "/api%2fadmin", 12000}},
		{"acme override", Route{"one", "example.test", "/.well-known/acme-challenge", 12000}},
		{"privileged upstream", Route{"one", "example.test", "/", 80}},
		{"management upstream", Route{"one", "example.test", "/", 18080}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Prepare(testIdentity(), testSettings(), 1, 0, []Route{tc.route}); err == nil {
				t.Fatal("accepted unsafe route")
			}
		})
	}
	for _, routes := range [][]Route{{good, good}, {good, {"two", good.Hostname, good.PathPrefix, 13000}}} {
		if _, err := Prepare(testIdentity(), testSettings(), 1, 0, routes); err == nil {
			t.Fatal("accepted duplicate")
		}
	}
	s := testSettings()
	s.CA = CA("https://evil.test/directory")
	if _, err := Prepare(testIdentity(), s, 1, 0, nil); err == nil {
		t.Fatal("accepted arbitrary CA")
	}
}
func TestPlanImmutableDeterministic(t *testing.T) {
	r := []Route{{"b", "app.example.test", "/api", 12000}, {"a", "app.example.test", "/", 13000}}
	p, err := Prepare(testIdentity(), testSettings(), 2, 1, r)
	if err != nil {
		t.Fatal(err)
	}
	r[0].HostPort = 14000
	copy := p.Routes()
	copy[0].Hostname = "evil.test"
	q, err := Prepare(testIdentity(), testSettings(), 2, 1, []Route{{"a", "app.example.test", "/", 13000}, {"b", "app.example.test", "/api", 12000}})
	if err != nil || q.Hash() != p.Hash() {
		t.Fatal("plan is mutable or nondeterministic")
	}
	var config struct {
		HTTP struct {
			Routers map[string]struct {
				Rule     string
				Priority int
			}
			Services map[string]struct {
				LoadBalancer struct{ Servers []struct{ URL string } }
			}
		}
	}
	if err = json.Unmarshal(p.dynamic(), &config); err != nil {
		t.Fatal(err)
	}
	router := config.HTTP.Routers[p.prefix()+"-b-http"]
	if router.Rule != "Host(`app.example.test`) && (Path(`/api`) || PathPrefix(`/api/`))" {
		t.Fatal(router.Rule)
	}
	if router.Priority <= config.HTTP.Routers[p.prefix()+"-a-http"].Priority {
		t.Fatal("longest prefix lacks priority")
	}
	for _, s := range config.HTTP.Services {
		if len(s.LoadBalancer.Servers) != 1 {
			t.Fatal("server count")
		}
		if s.LoadBalancer.Servers[0].URL != "http://127.0.0.1:12000" && s.LoadBalancer.Servers[0].URL != "http://127.0.0.1:13000" {
			t.Fatal("non-loopback target")
		}
	}
}

func TestEmptyGenerationOmitsStandaloneServicesAndKeepsMarker(t *testing.T) {
	for _, previous := range []uint64{0, 7} {
		plan, err := Prepare(testIdentity(), testSettings(), previous+1, previous, nil)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]map[string]json.RawMessage
		if err = json.Unmarshal(plan.dynamic(), &document); err != nil {
			t.Fatal(err)
		}
		http := document["http"]
		if _, present := http["services"]; present {
			t.Fatal("Traefik rejects standalone empty services element")
		}
		var routers, middlewares map[string]json.RawMessage
		if err = json.Unmarshal(http["routers"], &routers); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(http["middlewares"], &middlewares); err != nil {
			t.Fatal(err)
		}
		if len(routers) != 1 || len(middlewares) != 1 || routers[plan.prefix()+"-generation"] == nil || middlewares[plan.prefix()] == nil {
			t.Fatal("empty generation lost its loaded generation proof")
		}
	}
}
