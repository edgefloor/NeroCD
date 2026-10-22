package hostingproxy

import (
	"encoding/json"
	"testing"
)

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
