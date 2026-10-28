package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testAlias = "ncdb-0123456789abcdef0123456789abcdef"

func TestHTTPDatabaseEnvironmentProfiles(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			env := map[string]string{"FIXTURE_DATABASE_ENGINE": engine, "FIXTURE_DATABASE_NAMESPACE": "isolated_a", "PGHOST": testAlias, "PGPORT": "5432", "PGDATABASE": "app", "PGUSER": "ncd_app", "PGPASSWORD": strings.Repeat("a", 43), "REDIS_HOST": testAlias, "REDIS_PORT": "6379", "REDIS_USERNAME": "ncd_app", "REDIS_PASSWORD": strings.Repeat("a", 43), "MYSQL_HOST": testAlias, "MYSQL_PORT": "3306", "MYSQL_DATABASE": "app", "MYSQL_USER": "ncd_app", "MYSQL_PASSWORD": strings.Repeat("a", 43)}
			// A canonical 32-byte credential, delivered through runtime configuration.
			env["MYSQL_PASSWORD"] = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			env["PGPASSWORD"], env["REDIS_PASSWORD"] = base64.RawURLEncoding.EncodeToString(make([]byte, 32)), base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			if _, err := httpDatabaseEnvironment(func(k string) string { return env[k] }); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"FIXTURE_DATABASE_NAMESPACE", "FIXTURE_DATABASE_ENGINE", map[string]string{"postgresql": "PGHOST", "redis": "REDIS_HOST", "mysql": "MYSQL_HOST"}[engine], map[string]string{"postgresql": "PGPASSWORD", "redis": "REDIS_PASSWORD", "mysql": "MYSQL_PASSWORD"}[engine]} {
				saved := env[key]
				env[key] = "SENSITIVE-invalid"
				if _, err := httpDatabaseEnvironment(func(k string) string { return env[k] }); err == nil || strings.Contains(err.Error(), "SENSITIVE") {
					t.Fatal("invalid environment accepted or reflected")
				}
				env[key] = saved
			}
		})
	}
}

func TestMySQLHTTPEnvironmentRequiresExactBindingProfile(t *testing.T) {
	env := map[string]string{"FIXTURE_DATABASE_ENGINE": "mysql", "FIXTURE_DATABASE_NAMESPACE": "connected_mysql", "MYSQL_HOST": testAlias, "MYSQL_PORT": "3306", "MYSQL_DATABASE": "app", "MYSQL_USER": "ncd_app", "MYSQL_PASSWORD": base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	for _, key := range []string{"MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER"} {
		saved := env[key]
		env[key] = "unexpected"
		if _, err := httpDatabaseEnvironment(func(key string) string { return env[key] }); err == nil {
			t.Fatal("unexpected MySQL binding profile accepted")
		}
		env[key] = saved
	}
}

func TestHTTPDatabaseHandlerRequestsAndBounds(t *testing.T) {
	records := map[string]string{}
	calls := 0
	handler := httpDatabaseHandler(func(ctx context.Context, action, key, value string) (string, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("missing bounded request context")
		}
		if action == "ready" {
			return "ready", nil
		}
		if action == "put" {
			records[key] = value
		}
		return records[key], nil
	})
	for _, test := range []struct {
		method, path, body, want string
		status                   int
	}{
		{"GET", "/ready", "", "ready", 200}, {"PUT", "/records/a", "retained_a", "retained_a", 200}, {"GET", "/records/a", "", "retained_a", 200},
		{"PUT", "/records/a", strings.Repeat("a", 4097), "invalid request\n", 400}, {"PUT", "/records/a", "", "invalid request\n", 400},
		{"GET", "/records/a?password=SENSITIVE", "", "invalid request\n", 400}, {"GET", "/records/../a", "", "invalid request\n", 400}, {"POST", "/records/a", "", "not found\n", 404},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if response.Code != test.status || response.Body.String() != test.want || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("request mismatch method=%s status=%d", test.method, response.Code)
		}
	}
	if calls != 3 {
		t.Fatal("invalid requests reached database")
	}
	response := httptest.NewRecorder()
	httpDatabaseHandler(func(context.Context, string, string, string) (string, error) {
		return "SENSITIVE", errors.New("SENSITIVE")
	}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != 503 || response.Body.String() != "database unavailable\n" {
		t.Fatal("database diagnostic exposed")
	}
}

func TestHTTPDatabaseInstanceIdentity(t *testing.T) {
	for _, instance := range []string{"", "foreign-name", strings.Repeat("a", 64), "abcdef01234g"} {
		if _, err := httpDatabaseInstanceHandler(instance, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
			t.Fatal("unqualified container identity accepted")
		}
	}
	for _, instance := range []string{"abcdef012345", "012345abcdef"} {
		handler, err := httpDatabaseInstanceHandler(instance, httpDatabaseHandler(func(context.Context, string, string, string) (string, error) { return "retained_a", nil }))
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/records/a", nil))
		if response.Code != 200 || response.Body.String() != "retained_a" || response.Header().Get("X-NeroCD-Fixture-Instance") != instance {
			t.Fatal("instance identity changed application response")
		}
	}
}

func TestUnrelatedHTTPFixtureRejectsDatabaseAccessAndWrites(t *testing.T) {
	getenv := func(name string) string {
		switch name {
		case "FIXTURE_DATABASE_ENGINE":
			return "unrelated"
		case "FIXTURE_DATABASE_NAMESPACE":
			return "route_isolation"
		default:
			t.Fatal("unrelated fixture read database configuration")
			return ""
		}
	}
	record, err := httpFixtureRecord(getenv)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		action, key, want string
		ok                bool
	}{{"ready", "", "ready", true}, {"get", "unrelated", "unrelated_route", true}, {"put", "unrelated", "", false}, {"get", "foreign", "", false}} {
		got, err := record(t.Context(), test.action, test.key, "attempted_write")
		if got != test.want || (err == nil) != test.ok {
			t.Fatal("unrelated fixture escaped its read-only scope")
		}
	}
	if _, err := httpFixtureRecord(func(name string) string {
		if name == "FIXTURE_DATABASE_ENGINE" {
			return "unrelated"
		}
		return "foreign"
	}); err == nil {
		t.Fatal("unrelated namespace accepted")
	}
}

func TestHTTPDatabaseHandlerCancellationAndResponseBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response := httptest.NewRecorder()
	httpDatabaseHandler(func(ctx context.Context, _, _, _ string) (string, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("request cancellation lost")
		}
		return "", ctx.Err()
	}).ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/ready", nil))
	if response.Code != 503 || response.Body.String() != "database unavailable\n" {
		t.Fatal("canceled request did not sanitize failure")
	}
	for _, size := range []int{4096, 4097} {
		response = httptest.NewRecorder()
		httpDatabaseHandler(func(context.Context, string, string, string) (string, error) { return strings.Repeat("a", size), nil }).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records/a", nil))
		if size == 4096 && (response.Code != 200 || response.Body.Len() != size) || size == 4097 && (response.Code != 503 || response.Body.String() != "database unavailable\n") {
			t.Fatal("database response bound not enforced")
		}
	}
}

func TestPrivateEndpointIPCompatibility(t *testing.T) {
	for _, host := range []string{"10.0.0.2", "172.18.0.3", "192.168.2.4", "::ffff:172.18.0.3"} {
		t.Run(host, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), host, "", func(context.Context, string) ([]string, error) {
				t.Fatal("IP-only mode must not resolve DNS")
				return nil, nil
			})
			if err != nil || got != host {
				t.Fatalf("IP-only endpoint changed: got %q, error %v", got, err)
			}
		})
	}
}

func TestPrivateEndpointAliasPinsInspectedIPv4(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	calls := 0
	got, err := privateEndpoint(ctx, testAlias, "172.18.0.3", func(received context.Context, host string) ([]string, error) {
		calls++
		if received != ctx || host != testAlias {
			t.Fatal("lookup must receive the exact alias and shared deadline context")
		}
		return []string{"172.18.0.3"}, nil
	})
	if err != nil || got != "172.18.0.3" || calls != 1 {
		t.Fatalf("alias did not produce the inspected IPv4: got %q, calls %d, error %v", got, calls, err)
	}
}

func TestPrivateEndpointRejectsUnboundedOrAmbiguousInputBeforeDNS(t *testing.T) {
	tests := []struct{ host, address string }{
		{"127.0.0.1", ""}, {"8.8.8.8", ""}, {"::1", ""}, {"fd00::1", ""}, {testAlias, ""},
		{"example.invalid", "172.18.0.3"}, {"172.18.0.3", "172.18.0.3"},
		{strings.ToUpper(testAlias), "172.18.0.3"}, {testAlias + ".", "172.18.0.3"},
		{testAlias + ".internal", "172.18.0.3"}, {testAlias[:36], "172.18.0.3"},
		{testAlias + "0", "172.18.0.3"}, {testAlias[:36] + "g", "172.18.0.3"},
		{testAlias, "8.8.8.8"}, {testAlias, "127.0.0.1"}, {testAlias, "169.254.1.2"},
		{testAlias, "fd00::1"}, {testAlias, "::ffff:172.18.0.3"},
		{testAlias, "172.018.0.3"}, {testAlias, "172.18.0.3:5432"},
	}
	for _, test := range tests {
		t.Run(test.host+"/"+test.address, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), test.host, test.address, func(context.Context, string) ([]string, error) {
				t.Fatal("invalid endpoint reached DNS")
				return nil, nil
			})
			if err == nil || got != "" {
				t.Fatal("invalid endpoint accepted")
			}
		})
	}
}

func TestPrivateEndpointRejectsDNSOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		addresses []string
		err       error
	}{
		{"empty", nil, nil},
		{"mismatched", []string{"172.18.0.4"}, nil},
		{"nonprivate", []string{"8.8.8.8"}, nil},
		{"ipv6", []string{"fd00::1"}, nil},
		{"mapped_ipv6", []string{"::ffff:172.18.0.3"}, nil},
		{"multiple", []string{"172.18.0.3", "172.18.0.4"}, nil},
		{"duplicate", []string{"172.18.0.3", "172.18.0.3"}, nil},
		{"mixed", []string{"172.18.0.3", "8.8.8.8"}, nil},
		{"partial_error", []string{"172.18.0.3"}, errors.New("sensitive resolver diagnostic")},
		{"error", nil, errors.New("sensitive resolver diagnostic")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), testAlias, "172.18.0.3", func(context.Context, string) ([]string, error) {
				return test.addresses, test.err
			})
			if got != "" || err == nil || err.Error() != "private alias qualification failed" {
				t.Fatalf("DNS result did not fail closed with generic error: got %q, error %v", got, err)
			}
		})
	}
}

func TestPrivateEndpointLookupDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	got, err := privateEndpoint(ctx, testAlias, "172.18.0.3", func(received context.Context, _ string) ([]string, error) {
		<-received.Done()
		return nil, received.Err()
	})
	if got != "" || err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("lookup did not honor the shared deadline")
	}
}

func TestClientFailureOutputHelper(t *testing.T) {
	if os.Getenv("NEROCD_MARKER_CLIENT_OUTPUT_TEST") == "1" {
		main()
	}
}

func TestClientFailureOutputAndInputBounds(t *testing.T) {
	password := []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	credential := input{Engine: "postgresql", Host: "example.invalid", Address: "172.18.0.3", Username: "ncd_app", Password: password, Marker: "private_marker_0123456789", Write: true}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{raw, append(bytes.Clone(raw), []byte(strings.Repeat(" ", 4097))...), []byte(`{"password":"parser_diagnostic_marker"`)} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClientFailureOutputHelper$")
		cmd.Env = append(os.Environ(), "NEROCD_MARKER_CLIENT_OUTPUT_TEST=1")
		cmd.Stdin = bytes.NewReader(raw)
		out, err := cmd.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || string(out) != "database marker client failed\n" {
			t.Fatalf("failure output or exit contract changed: %q, error %v", out, err)
		}
	}
}
