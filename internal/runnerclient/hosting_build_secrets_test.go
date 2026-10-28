package runnerclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestBuildSecretsProtectedTransport(t *testing.T) {
	request := domain.HostingBuildSecretsRequest{Authority: domain.HostingBuildAuthority{BuildID: "build-test", RunID: "build_run_build-test", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32)}
	for _, mode := range []string{"valid", "redirect", "missing no-store", "expired", "unknown", "missing value"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/runners/builds/secrets" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer bearer" {
					t.Error("wrong protected endpoint request")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/other", http.StatusFound)
					return
				}
				if mode != "missing no-store" {
					w.Header().Set("Cache-Control", "no-store")
				}
				if mode == "unknown" {
					_, _ = w.Write([]byte(`{"unknown":true}`))
					return
				}
				if mode == "missing value" {
					_, _ = w.Write([]byte(`{"secrets":[{"name":"TOKEN"}],"use_until":"2030-01-01T00:00:00Z"}`))
					return
				}
				expiry := time.Now().Add(30 * time.Second)
				if mode == "expired" {
					expiry = time.Now().Add(-time.Second)
				}
				if err := domain.WriteHostingBuildSecretsDelivery(w, &domain.HostingBuildSecretsDelivery{UseUntil: expiry, Secrets: []domain.HostingBuildSecretValue{{Name: "TOKEN", Value: "protected"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(server.URL, "bearer")
			client.HTTP = server.Client()
			material, err := client.BuildSecrets(t.Context(), "runner", request, SourceCredentialTransport{})
			if (err == nil) != (mode == "valid") || calls != 1 {
				t.Fatal("protected transport boundary changed", calls, err)
			}
			if material != nil {
				material.Close()
			}
		})
	}
	client := New("http://127.0.0.1:1", "bearer")
	if material, err := client.BuildSecrets(t.Context(), "runner", request, SourceCredentialTransport{}); err == nil {
		material.Close()
		t.Fatal("untrusted cleartext protected delivery accepted")
	}
}
