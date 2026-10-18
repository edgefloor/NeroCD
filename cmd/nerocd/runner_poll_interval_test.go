package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
)

func TestRunRunnerValidatesPollIntervalsBeforeNetworkRequests(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantNoHTTP bool
	}{
		{name: "zero poll interval", args: []string{"--poll-interval", "0s"}, wantErr: "--poll-interval must be greater than zero", wantNoHTTP: true},
		{name: "negative poll interval", args: []string{"--poll-interval", "-1s"}, wantErr: "--poll-interval must be greater than zero", wantNoHTTP: true},
		{name: "zero cancel poll interval", args: []string{"--cancel-poll-interval", "0s"}, wantErr: "--cancel-poll-interval must be greater than zero", wantNoHTTP: true},
		{name: "negative cancel poll interval", args: []string{"--cancel-poll-interval", "-1s"}, wantErr: "--cancel-poll-interval must be greater than zero", wantNoHTTP: true},
		{name: "default intervals"},
		{name: "positive poll interval", args: []string{"--poll-interval", "10ms"}},
		{name: "positive cancel poll interval", args: []string{"--cancel-poll-interval", "10ms"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/api/v1/runners/register":
					_ = json.NewEncoder(w).Encode(runnerRegisterResponse{Token: "runner-token"})
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner_local", Status: domain.RunnerActive})
				case "/api/v1/runners/telemetry":
					w.WriteHeader(http.StatusNoContent)
				case "/api/v1/runners/claim":
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			args := []string{
				"--server", server.URL,
				"--token", "privileged",
				"--work-dir", t.TempDir(),
				"--once",
			}
			args = append(args, tc.args...)
			err := runRunner(args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("runRunner() error = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("runRunner() error = %v", err)
			}
			if got := requests.Load(); tc.wantNoHTTP && got != 0 {
				t.Fatalf("network requests = %d, want 0", got)
			}
		})
	}
}
