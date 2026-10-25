package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
)

func TestRuntimeStartupIdentityPropagatesApplicationDatabaseStorage(t *testing.T) {
	for _, tc := range []struct {
		name                                                     string
		database, configured, advertised, wrongRunner, wantError bool
	}{
		{name: "enabled_matching", database: true, configured: true, advertised: true},
		{name: "disabled_matching", database: true},
		{name: "database_absent"},
		{name: "advertised_without_opt_in", database: true, advertised: true, wantError: true},
		{name: "opt_in_without_capability", database: true, configured: true, wantError: true},
		{name: "capability_without_database", advertised: true, wantError: true},
		{name: "different_runner", database: true, configured: true, advertised: true, wrongRunner: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var capabilities []string
			config := Config{RunnerID: "runner_route_identity", Token: "fixture"}
			if tc.database {
				capabilities = []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability, domain.HostingApplicationDatabaseCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}
				config.Database = &DatabaseExecutionConfig{ApplicationDatabase: true, NetworkCapture: true, NetworkUpgrade: true, ApplicationStorage: tc.configured}
				config.RuntimeEnvironment = &RuntimeEnvironmentConfig{}
				config.Proxy = ProxyConfig{Directory: t.TempDir(), VerifiedSwitch: true}
			}
			if tc.advertised {
				capabilities = append(capabilities, domain.HostingApplicationDatabaseStorageCapability)
			}
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("startup identity reached a nonidentity endpoint or changed authentication")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_route_identity", "capabilities": capabilities}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			// Trust this fixture CA without relaxing HTTPS verification. The
			// runtime constructs its own client, as it does during RunContext.
			previous := http.DefaultTransport
			trusted := server.Client().Transport.(*http.Transport).Clone()
			http.DefaultTransport = trusted
			t.Cleanup(func() { http.DefaultTransport = previous; trusted.CloseIdleConnections() })
			config.Server = server.URL
			if tc.wrongRunner {
				config.RunnerID = "runner_other"
			}
			// RunContext uses this exact gate before opening its journal or
			// starting replay, heartbeat and claim work.
			err := validateLocalBuildRuntimeIdentity(t.Context(), config)
			if (err != nil) != tc.wantError || requests.Load() != 1 {
				t.Fatalf("startup identity error=%v requests=%d", err, requests.Load())
			}
		})
	}
}
