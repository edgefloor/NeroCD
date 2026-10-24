package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestDatabaseRuntimeCannotCompleteGenericClaim(t *testing.T) {
	completions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner"})
		case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v1/runners/claim":
			_ = json.NewEncoder(w).Encode(domain.ClaimedRun{Run: domain.TaskRun{ID: "run_database", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy}}})
		case "/api/v1/runners/complete":
			completions++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	err := Run(Config{Server: server.URL, Token: "fixture", RunnerID: "runner", WorkDir: t.TempDir(), JournalDir: t.TempDir(), CompleteStatus: "succeeded", Once: true, PollInterval: time.Second, CancelPollInterval: time.Second})
	if err == nil || completions != 0 {
		t.Fatal("database reached generic completion", err)
	}
}
func TestDatabaseRuntimeConfigRejectsCompletionBeforeRequests(t *testing.T) {
	for _, execute := range []bool{false, true} {
		if err := validateDatabaseExecutionConfig(Config{Execute: execute, CompleteStatus: "succeeded", Database: &DatabaseExecutionConfig{}}); err == nil {
			t.Fatal("database generic completion configuration accepted")
		}
	}
	if err := validateDatabaseExecutionConfig(Config{}); err != nil {
		t.Fatal("default database-off changed", err)
	}
}
