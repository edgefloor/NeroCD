package runnerclient

import (
	"encoding/json"
	"fmt"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedSwitchIdentityOldAgentDenied(t *testing.T) {
	for _, tc := range []struct{ flag, capability, valid bool }{{false, false, true}, {true, true, true}, {true, false, false}, {false, true, false}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			caps := []string{domain.RunTypeProxyApply}
			if tc.capability {
				caps = append(caps, domain.HostingProxyVerifiedSwitchCapability)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps})
		}))
		_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Proxy: true, ProxyVerifiedSwitch: tc.flag})
		server.Close()
		if (err == nil) != tc.valid {
			t.Fatalf("flag=%v capability=%v: %v", tc.flag, tc.capability, err)
		}
	}
}

func TestRuntimeExecutionIdentityRequiresExactLocalConfiguration(t *testing.T) {
	for _, tc := range []struct {
		caps                       []string
		proxy, build, local, valid bool
	}{
		{[]string{domain.RunTypeLocalBuildDeploy}, false, false, true, true},
		{[]string{domain.HostingBuildTaskType, domain.HostingPrivateGitCapability}, false, true, false, false},
		{[]string{domain.HostingPrivateGitCapability}, false, false, false, false},
		{[]string{domain.RunTypeLocalBuildDeploy, domain.RunTypeLocalBuildDeploy, "OPAQUE"}, false, false, true, true},
		{[]string{domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.HostingBuildTaskType}, true, true, true, true},
		{[]string{domain.RunTypeLocalBuildDeploy}, false, false, false, false},
		{[]string{"LOCAL_BUILD_DEPLOY_V1"}, false, false, true, false},
		{[]string{domain.RunTypeLocalBuildDeploy, "shell"}, false, false, true, false},
		{nil, false, false, false, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": tc.caps})
		}))
		client := New(server.URL, "fixture")
		_, err := client.RuntimeExecutionIdentity(t.Context(), tc.proxy, tc.build, tc.local)
		if (err == nil) != tc.valid {
			t.Fatal("local identity/config mismatch")
		}
		if tc.local && strings.Contains(strings.Join(tc.caps, ","), domain.RunTypeLocalBuildDeploy) {
			if _, err = client.RuntimeCapabilitiesIdentity(t.Context(), tc.proxy, tc.build); err == nil {
				t.Fatal("legacy wrapper accepted local capability")
			}
		}
		server.Close()
	}
}

func TestRuntimeIdentityPreservesOrdinaryIdentityContract(t *testing.T) {
	for _, test := range []struct {
		name, id     string
		capabilities []string
	}{
		{"legacy punctuation", "runner.legacy", []string{"shell"}},
		{"custom capability", "runner_legacy", []string{"shell", "CUSTOM"}},
		{"unicode identity and capability", "runner_árvíz", []string{"Saját képesség"}},
		{"duplicate capabilities", "runner_legacy", []string{"shell", "shell"}},
		{"long identity", "runner_" + strings.Repeat("a", 200), []string{"shell"}},
		{"long capability", "runner_legacy", []string{strings.Repeat("C", 200)}},
		{"many capabilities", "runner_legacy", strings.Split(strings.Repeat("CUSTOM,", 199)+"CUSTOM", ",")},
		{"bounded larger legacy response", "runner_legacy", []string{strings.Repeat("C", 70<<10)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"runner_id": test.id, "capabilities": test.capabilities})
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer runner_credential" {
					t.Error("identity made a nonidentity or unauthenticated request")
				}
				if _, err := w.Write(body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(server.URL, "runner_credential")
			oldID, oldErr := client.Identity(t.Context())
			id, err := client.RuntimeIdentity(t.Context(), false)
			if oldErr != nil || err != nil || oldID != test.id || id != oldID || requests != 2 {
				t.Fatalf("old id=%q err=%v runtime id=%q err=%v requests=%d", oldID, oldErr, id, err, requests)
			}
		})
	}
}

func TestProxyIdentityRequiresAuthenticatedCapabilityAndExplicitConfig(t *testing.T) {
	for _, test := range []struct {
		name, body        string
		configured, valid bool
	}{
		{"configured", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}`, true, true},
		{"missing_capability", `{"runner_id":"runner_one","capabilities":["shell"]}`, true, false},
		{"legacy_configured", `{"runner_id":"runner_one"}`, true, false},
		{"legacy_default", `{"runner_id":"runner_one"}`, false, true},
		{"unconfigured_proxy", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}`, false, false},
		{"duplicate", `{"runner_id":"runner_one","capabilities":["proxy_apply","proxy_apply"]}`, true, true},
		{"duplicate_unconfigured", `{"runner_id":"runner_one","capabilities":["proxy_apply","proxy_apply"]}`, false, false},
		{"wrong_case_configured", `{"runner_id":"runner_one","capabilities":["PROXY_APPLY"]}`, true, false},
		{"wrong_case_default", `{"runner_id":"runner_one","capabilities":["PROXY_APPLY"]}`, false, true},
		{"untrimmed_capability_configured", `{"runner_id":"runner_one","capabilities":[" proxy_apply "]}`, true, false},
		{"untrimmed_capability_default", `{"runner_id":"runner_one","capabilities":[" proxy_apply "]}`, false, true},
		{"null_capabilities_default", `{"runner_id":"runner_one","capabilities":null}`, false, true},
		{"null_capabilities_configured", `{"runner_id":"runner_one","capabilities":null}`, true, false},
		{"empty_capabilities_default", `{"runner_id":"runner_one","capabilities":[]}`, false, true},
		{"empty_capabilities_configured", `{"runner_id":"runner_one","capabilities":[]}`, true, false},
		{"additional_capabilities", `{"runner_id":"runner_one","capabilities":["CUSTOM","proxy_apply","other capability"]}`, true, true},
		{"concatenated", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}{}`, true, false},
		{"oversized", `{"runner_id":"runner_one","padding":"` + strings.Repeat("x", maxIdentityResponseBytes) + `"}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer runner_credential" {
					t.Error("proxy identity performed a nonidentity or unauthenticated request")
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			id, err := New(server.URL, "runner_credential").RuntimeIdentity(t.Context(), test.configured)
			if (err == nil) != test.valid || test.valid && id != "runner_one" || requests != 1 {
				t.Fatalf("identity=%q err=%v requests=%d", id, err, requests)
			}
		})
	}
}

func TestRuntimeIdentityRejectsMalformedOrUnauthenticatedResponsesWithinLegacyBound(t *testing.T) {
	valid := `{"runner_id":"runner.legacy","capabilities":["CUSTOM"]}`
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"exact legacy size bound", valid + strings.Repeat(" ", maxIdentityResponseBytes-len(valid)), 200, true},
		{"over legacy size bound", valid + strings.Repeat(" ", maxIdentityResponseBytes-len(valid)+1), 200, false},
		{"unknown fields", `{"runner_id":"runner.legacy","capabilities":["CUSTOM"],"future_field":{"value":true}}`, 200, true},
		{"surrounding document whitespace", " \t\r\n" + valid + " \t\r\n", 200, true},
		{"missing ID", `{"capabilities":[]}`, 200, false},
		{"null ID", `{"runner_id":null}`, 200, false},
		{"empty ID", `{"runner_id":""}`, 200, false},
		{"untrimmed ID", `{"runner_id":" runner.legacy "}`, 200, false},
		{"wrong ID type", `{"runner_id":123}`, 200, false},
		{"wrong capability type", `{"runner_id":"runner.legacy","capabilities":"proxy_apply"}`, 200, false},
		{"wrong capability element", `{"runner_id":"runner.legacy","capabilities":[123]}`, 200, false},
		{"malformed JSON", `{"runner_id":`, 200, false},
		{"null document", `null`, 200, false},
		{"array document", `[]`, 200, false},
		{"concatenated documents", valid + `{}`, 200, false},
		{"trailing garbage", valid + ` identity-secret-sentinel`, 200, false},
		{"unauthenticated", valid, 401, false},
		{"wrong actor credential", valid, 403, false},
		{"unsupported identity endpoint", valid, 404, false},
		{"server failure", valid, 500, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer dedicated_credential" {
					t.Error("unexpected identity authority or request")
				}
				w.WriteHeader(test.status)
				if _, err := w.Write([]byte(test.body)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			id, err := New(server.URL, "dedicated_credential").RuntimeIdentity(t.Context(), false)
			if (err == nil) != test.valid || test.valid && id != "runner.legacy" || !test.valid && id != "" || requests != 1 {
				t.Fatalf("id=%q error=%v requests=%d", id, err, requests)
			}
			if err != nil && strings.Contains(err.Error(), "identity-secret-sentinel") {
				t.Fatal("identity error reflected response contents")
			}
		})
	}
}

func TestRuntimeIdentityDecodeStopsBeforePublishingInvalidDocument(t *testing.T) {
	valid := `{"runner_id":"runner.legacy","capabilities":["proxy_apply"]}`
	reader := &countedIdentityReader{Reader: strings.NewReader(valid + strings.Repeat(" ", 2*maxIdentityResponseBytes))}
	var response proxyIdentityResponse
	if response.decodeResponse(reader) == nil || reader.bytesRead != maxIdentityResponseBytes+1 || response.RunnerID != "" || response.Capabilities != nil {
		t.Fatalf("oversized decoder published identity or exceeded bounded read: bytes=%d response=%+v", reader.bytesRead, response)
	}
	if response.decodeResponse(strings.NewReader(valid+` {}`)) == nil || response.RunnerID != "" || response.Capabilities != nil {
		t.Fatal("incomplete document published identity before whole-response validation")
	}
}

func TestRuntimeCapabilitiesIdentityChecksProxyAndBuildIndependently(t *testing.T) {
	for _, serverProxy := range []bool{false, true} {
		for _, serverBuild := range []bool{false, true} {
			for _, localProxy := range []bool{false, true} {
				for _, localBuild := range []bool{false, true} {
					name := fmt.Sprintf("persisted_%t_%t_local_%t_%t", serverProxy, serverBuild, localProxy, localBuild)
					t.Run(name, func(t *testing.T) {
						caps := []string{"CUSTOM", "CUSTOM", " hosting_build_v1 ", "HOSTING_BUILD_V1"}
						if serverProxy {
							caps = append(caps, "proxy_apply", "proxy_apply")
						}
						if serverBuild {
							caps = append(caps, "hosting_build_v1", "hosting_build_v1")
						}
						requests := 0
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests++
							if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer token" {
								t.Error("identity performed an unexpected request")
							}
							if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner.legacy", "capabilities": caps}); err != nil {
								t.Error(err)
							}
						}))
						defer server.Close()
						id, err := New(server.URL, "token").RuntimeCapabilitiesIdentity(t.Context(), localProxy, localBuild)
						valid := serverProxy == localProxy && serverBuild == localBuild
						if (err == nil) != valid || valid && id != "runner.legacy" || !valid && id != "" || requests != 1 {
							t.Fatalf("id=%q err=%v requests=%d", id, err, requests)
						}
					})
				}
			}
		}
	}
}

func TestRuntimeCapabilitiesIdentityKeepsLegacyWrappersFailClosed(t *testing.T) {
	for _, test := range []struct {
		name, body          string
		proxy, build, valid bool
	}{
		{"legacy disabled", `{"runner_id":"runner"}`, false, false, true},
		{"legacy build", `{"runner_id":"runner"}`, false, true, false},
		{"legacy proxy", `{"runner_id":"runner"}`, true, false, false},
		{"null disabled", `{"runner_id":"runner","capabilities":null}`, false, false, true},
		{"opaque names", `{"runner_id":"runner","capabilities":["HOSTING_BUILD_V1"," hosting_build_v1 ","hosting_build","CUSTOM","CUSTOM"]}`, false, false, true},
		{"opaque not build", `{"runner_id":"runner","capabilities":["HOSTING_BUILD_V1"]}`, false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			id, err := New(server.URL, "token").RuntimeCapabilitiesIdentity(t.Context(), test.proxy, test.build)
			if (err == nil) != test.valid || test.valid && id != "runner" {
				t.Fatal(id, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"runner_id":"runner","capabilities":["proxy_apply","hosting_build_v1"]}`))
	}))
	defer server.Close()
	client := New(server.URL, "token")
	if id, err := client.Identity(t.Context()); err != nil || id != "runner" {
		t.Fatal("legacy Identity changed", id, err)
	}
	if _, err := client.RuntimeIdentity(t.Context(), true); err == nil {
		t.Fatal("old runtime accepted unconfigured build capability")
	}
	if _, err := client.ProxyIdentity(t.Context()); err == nil {
		t.Fatal("proxy-only wrapper accepted build capability")
	}
	if _, err := client.RuntimeCapabilitiesIdentity(t.Context(), true, true); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeBuildIdentityRejectsPersistedShell(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/identity" {
			t.Error("capability validation made nonidentity request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_build", "capabilities": []string{"shell", domain.HostingBuildTaskType}})
	}))
	defer server.Close()
	if _, err := New(server.URL, "fixture").RuntimeCapabilitiesIdentity(t.Context(), false, true); err == nil {
		t.Fatal("persisted shell+build identity accepted")
	}
}

func TestRuntimePrivateGitIdentityExactOptIn(t *testing.T) {
	for _, tc := range []struct {
		caps                  []string
		build, private, valid bool
	}{
		{[]string{domain.HostingBuildTaskType, domain.HostingPrivateGitCapability}, true, true, true},
		{[]string{domain.HostingBuildTaskType, domain.HostingPrivateGitCapability, domain.HostingSSHGitCapability}, true, true, true},
		{[]string{domain.HostingBuildTaskType, domain.HostingSSHGitCapability}, true, false, false},
		{[]string{domain.HostingBuildTaskType}, true, true, false},
		{[]string{domain.HostingPrivateGitCapability}, false, true, false},
		{[]string{domain.HostingBuildTaskType, domain.HostingPrivateGitCapability, "shell"}, true, true, false},
		{[]string{domain.HostingBuildTaskType, "HOSTING_GIT_HTTPS_BASIC_V1"}, true, true, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": tc.caps})
		}))
		c := New(server.URL, "fixture")
		_, err := c.RuntimeExecutionIdentity(t.Context(), false, tc.build, false, tc.private)
		if (err == nil) != tc.valid {
			t.Fatal("private identity mismatch", err)
		}
		if tc.valid {
			if _, err = c.RuntimeCapabilitiesIdentity(t.Context(), false, true); err == nil {
				t.Fatal("legacy helper enabled private")
			}
		}
		server.Close()
	}
}

func TestRuntimeReadinessIdentityRequiresExactConfiguration(t *testing.T) {
	for _, tc := range []struct {
		caps              []string
		configured, valid bool
	}{
		{[]string{domain.ServerReadinessCapability}, true, true},
		{[]string{domain.ServerReadinessCapability}, false, false},
		{nil, true, false},
		{[]string{domain.ServerReadinessCapability, "shell"}, true, false},
		{[]string{"HOSTING_READINESS_V1"}, true, false},
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/runners/identity" {
				t.Error("wrong identity path")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": tc.caps})
		}))
		client := New(server.URL, "fixture")
		client.HTTP = server.Client()
		_, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Readiness: tc.configured})
		server.Close()
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}

func TestReadinessAndVerifiedSwitchIdentityCompose(t *testing.T) {
	for _, advertisedStrict := range []bool{false, true} {
		for _, advertisedReadiness := range []bool{false, true} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				caps := []string{domain.RunTypeProxyApply}
				if advertisedStrict {
					caps = append(caps, domain.HostingProxyVerifiedSwitchCapability)
				}
				if advertisedReadiness {
					caps = append(caps, domain.ServerReadinessCapability)
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_combined", "capabilities": caps}); err != nil {
					t.Error(err)
				}
			}))
			for _, configuredStrict := range []bool{false, true} {
				for _, configuredReadiness := range []bool{false, true} {
					_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Proxy: true, ProxyVerifiedSwitch: configuredStrict, Readiness: configuredReadiness, ReadinessTrustedLoopbackHTTP: true})
					valid := configuredStrict == advertisedStrict && configuredReadiness == advertisedReadiness
					if (err == nil) != valid {
						t.Fatalf("configured strict/readiness=%v/%v advertised=%v/%v err=%v", configuredStrict, configuredReadiness, advertisedStrict, advertisedReadiness, err)
					}
				}
			}
			server.Close()
		}
	}
}

func TestDatabaseExecutionIdentityRequiresExactConfigurationAndPrerequisites(t *testing.T) {
	for _, tc := range []struct {
		configured bool
		caps       []string
		valid      bool
	}{
		{false, nil, true},
		{false, []string{domain.HostingDatabaseCapability}, false},
		{false, []string{domain.RunTypeDatabaseDeploy}, false},
		{true, []string{domain.HostingDatabaseCapability}, false},
		{true, []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability}, false},
		{true, []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}, true},
		{true, []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, "shell"}, false},
		{true, nil, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": tc.caps})
		}))
		_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Database: tc.configured, DatabaseTrustedLoopbackHTTP: true})
		server.Close()
		if (err == nil) != tc.valid {
			t.Fatalf("database identity accepted mismatch configured=%v caps=%v err=%v", tc.configured, tc.caps, err)
		}
	}
}

func TestMySQLExecutionIdentityRequiresConfiguredAdapter(t *testing.T) {
	base := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	for _, tc := range []struct {
		name              string
		database, backups bool
		extra             []string
		valid             bool
	}{
		{"legacy PostgreSQL", true, false, nil, true},
		{"MySQL database", true, false, []string{domain.HostingDatabaseMySQLCapability}, true},
		{"unconfigured MySQL", false, false, []string{domain.HostingDatabaseMySQLCapability}, false},
		{"unconfigured backup", true, false, []string{domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability}, false},
		{"configured backup", true, true, []string{domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability, domain.HostingDatabaseBackupCapability, domain.RunTypeDatabaseBackup}, true},
		{"shell", true, false, []string{domain.HostingDatabaseMySQLCapability, "shell"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := append(append([]string(nil), base...), tc.extra...)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Database: tc.database, LogicalDatabaseBackups: tc.backups, DatabaseTrustedLoopbackHTTP: true})
			if (err == nil) != tc.valid {
				t.Fatal("MySQL adapter boundary", err)
			}
		})
	}
}
