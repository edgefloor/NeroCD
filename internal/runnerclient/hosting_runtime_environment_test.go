package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func runtimeEnvironmentRequest() domain.HostingRuntimeEnvironmentRequest {
	return domain.HostingRuntimeEnvironmentRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: "sha256:" + strings.Repeat("a", 64), RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("b", 64)}, DeliveryID: strings.Repeat("c", 32)}
}
func TestHostingRuntimeEnvironmentTransport(t *testing.T) {
	for _, mode := range []string{"valid", "authority", "plan", "reference", "delivery", "request", "purpose", "expired", "future", "unknown", "duplicate", "cache", "redirect", "status", "invalid-value"} {
		t.Run(mode, func(t *testing.T) {
			in := runtimeEnvironmentRequest()
			sentinel := "fixture-secret-do-not-print"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/hosting/runtime-environment" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong request authority")
				}
				var got domain.HostingRuntimeEnvironmentRequest
				if json.NewDecoder(r.Body).Decode(&got) != nil || got != in {
					t.Error("request changed")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/elsewhere", http.StatusFound)
					return
				}
				if mode == "status" {
					w.WriteHeader(503)
					return
				}
				m := hostingruntimeprotocol.Metadata{Authority: in.Authority, PlanHash: in.PlanHash, RuntimeConfiguration: in.RuntimeConfiguration, DeliveryID: in.DeliveryID, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash("runner", in), Purpose: hostingruntimeprotocol.Purpose, UseUntil: time.Now().Add(30 * time.Second)}
				switch mode {
				case "authority":
					m.Authority.Fence = "other"
				case "plan":
					m.PlanHash = "sha256:" + strings.Repeat("d", 64)
				case "reference":
					m.RuntimeConfiguration.Generation++
				case "delivery":
					m.DeliveryID = strings.Repeat("d", 32)
				case "request":
					m.RequestHash = "sha256:" + strings.Repeat("d", 64)
				case "expired":
					m.UseUntil = time.Now().Add(-time.Second)
				case "future":
					m.UseUntil = time.Now().Add(2 * time.Minute)
				}
				env, err := hostingruntimeprotocol.NewEnvironment(m, []hostingruntimeprotocol.Entry{{Name: "APP_SECRET", Value: []byte(sentinel)}, {Name: "APP_EMPTY", Value: []byte{}}})
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				defer env.Close()
				var out bytes.Buffer
				if hostingruntimeprotocol.WriteEnvironment(&out, env) != nil {
					t.Error("encode failed")
				}
				body := out.String()
				switch mode {
				case "purpose":
					body = strings.Replace(body, hostingruntimeprotocol.Purpose, "other", 1)
				case "unknown":
					body = strings.Replace(body, `"entries":`, `"unknown":true,"entries":`, 1)
				case "duplicate":
					body = strings.Replace(body, `"entries":`, `"purpose":"other","entries":`, 1)
				case "invalid-value":
					body = strings.Replace(body, `"name":"APP_SECRET"`, `"name":"NEROCD_INVALID"`, 1)
				}
				if mode != "cache" {
					w.Header().Set("Cache-Control", "no-store")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			c := New(server.URL, "token")
			c.HTTP = server.Client()
			env, err := c.HostingRuntimeEnvironment(t.Context(), "runner", in, RuntimeEnvironmentTransport{})
			if mode != "valid" {
				if err == nil {
					env.Close()
					t.Fatal("invalid protected response accepted")
				}
				if strings.Contains(fmt.Sprint(err), sentinel) {
					t.Fatal("secret in diagnostic")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer env.Close()
			if err = env.WithValues(func(values []hostingruntimeprotocol.Entry) error {
				if len(values) != 2 || !bytes.Equal(values[0].Value, []byte(sentinel)) {
					t.Error("value changed")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fmt.Sprintf("%+v", env), sentinel) {
				t.Fatal("format leaked")
			}
		})
	}
}
func TestRuntimeEnvironmentServerPolicy(t *testing.T) {
	for _, url := range []string{"http://localhost:123", "http://example.com", "https://user:pass@example.com", "https://example.com?x=1"} {
		if ValidateRuntimeEnvironmentServer(url, RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true}) == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	if ValidateRuntimeEnvironmentServer("http://127.0.0.1:123", RuntimeEnvironmentTransport{}) == nil {
		t.Fatal("implicit HTTP accepted")
	}
	if ValidateRuntimeEnvironmentServer("http://127.0.0.1:123", RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true}) != nil {
		t.Fatal("explicit fixture rejected")
	}
}
func TestRuntimeEnvironmentIdentityOptions(t *testing.T) {
	for _, caps := range [][]string{{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability}, {domain.RunTypeImageDeploy, domain.HostingRuntimeEnvironmentCapability}, {"shell", domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability}, {domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: caps})
		}))
		c := New(server.URL, "token")
		_, err := c.RuntimeExecutionIdentityWithOptions(context.Background(), RuntimeExecutionOptions{RuntimeEnvironment: true})
		valid := len(caps) == 3 && caps[1] == domain.HostingReleasePortsCapability
		if (err == nil) != valid {
			t.Fatal("identity did not enforce exact environment modes")
		}
		if valid {
			if _, err = c.RuntimeExecutionIdentity(t.Context(), false, false, false); err == nil {
				t.Fatal("legacy wrapper accepted envcap")
			}
		}
		server.Close()
	}
}
