package runnerclient

import (
	"encoding/json"

	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostingStackVolumeRequestUsesFencedHostingEndpoint(t *testing.T) {
	input := domain.HostingStackVolumeRequest{HostingAuthority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: "plan", Volumes: []domain.HostingStackVolume{}}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var got domain.HostingStackVolumeRequest
		if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/stack-volumes" || r.Header.Get("Authorization") != "Bearer token" || json.NewDecoder(r.Body).Decode(&got) != nil || domain.HostingDocumentHash(got) != domain.HostingDocumentHash(input) {
			t.Error("changed volume authority endpoint or body")
		}
		_ = json.NewEncoder(w).Encode(domain.HostingOperation{ID: "hop"})
	}))
	defer server.Close()
	client := New(server.URL, "token")
	client.HTTP = server.Client()
	if _, err := client.HostingStackVolumes(t.Context(), input); err != nil || calls != 1 {
		t.Fatal("volume request failed", err)
	}
}
