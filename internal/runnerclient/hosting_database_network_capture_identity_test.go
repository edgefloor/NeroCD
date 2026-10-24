package runnerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
)

func TestDatabaseNetworkCaptureIdentityProfiles(t *testing.T) {
	db := []string{domain.RunTypeComposeDeploy, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability}
	for _, tc := range []struct {
		name    string
		caps    []string
		options RuntimeExecutionOptions
		valid   bool
	}{
		{"capture only", []string{domain.HostingDatabaseNetworkCaptureCapability}, RuntimeExecutionOptions{DatabaseNetworkCapture: true}, true},
		{"source profile", db, RuntimeExecutionOptions{DatabaseNetworkCapture: true}, true},
		{"ordinary database with capture", db, RuntimeExecutionOptions{Database: true}, true},
		{"missing capture", db[:4], RuntimeExecutionOptions{DatabaseNetworkCapture: true}, false},
		{"shell", []string{domain.HostingDatabaseNetworkCaptureCapability, "shell"}, RuntimeExecutionOptions{DatabaseNetworkCapture: true}, false},
		{"mixed modes", db, RuntimeExecutionOptions{DatabaseNetworkCapture: true, Database: true}, false},
		{"incoherent database", []string{domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseCapability}, RuntimeExecutionOptions{DatabaseNetworkCapture: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer identity_canary" {
					t.Error("unexpected identity request")
				}
				if err := json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: tc.caps}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(server.URL, "identity_canary")
			client.HTTP = server.Client()
			id, err := client.RuntimeExecutionIdentityWithOptions(context.Background(), tc.options)
			if (err == nil) != tc.valid || err == nil && id != "runner" || calls != 1 {
				t.Fatalf("identity result id=%q error=%v calls=%d", id, err, calls)
			}
		})
	}
}

func TestDatabaseNetworkCaptureIdentityProtectsTransport(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	client := New(target.URL, "identity_canary")
	if _, err := client.RuntimeExecutionIdentityWithOptions(context.Background(), RuntimeExecutionOptions{DatabaseNetworkCapture: true}); err == nil {
		t.Fatal("HTTP accepted")
	}
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client = New(source.URL, "identity_canary")
	client.HTTP = source.Client()
	if _, err := client.RuntimeExecutionIdentityWithOptions(context.Background(), RuntimeExecutionOptions{DatabaseNetworkCapture: true}); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("identity bearer reached unprotected destination")
	}
}
