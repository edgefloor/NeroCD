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

func TestRunRunnerRejectsNonPositivePollIntervalBeforeNetworkRequests(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	tests := []struct {
		name     string
		interval string
	}{
		{name: "zero", interval: "0s"},
		{name: "negative", interval: "-1s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			err := runRunner([]string{
				"--server", server.URL,
				"--token", "privileged",
				"--poll-interval", tc.interval,
			})
			if err == nil || !strings.Contains(err.Error(), "--poll-interval must be greater than zero") {
				t.Errorf("runRunner() error = %v, want poll interval validation error", err)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("network requests = %d, want 0", got)
			}
		})
	}
}

func TestRunRunnerAcceptsDefaultAndPositivePollIntervalWithOnce(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "positive", args: []string{"--poll-interval", "10ms"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if err := runRunner(args); err != nil {
				t.Fatalf("runRunner() error = %v", err)
			}
		})
	}
}
