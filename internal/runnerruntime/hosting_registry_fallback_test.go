//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestRegistryPullConnectedRunnerOrdinaryFallback(t *testing.T) {
	j, _ := openStorageCoordinatorJournal(t)
	lease := domain.RunLease{ID: "registry_lease", RunID: "run_registry", RunnerID: "runner", Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	supervisor := newAttemptSupervisorFrom(ctx, lease)
	defer supervisor.Close()
	snapshot := imageTestPlan(t).Snapshot
	snapshot.RunnerID = "runner"
	snapshot.RegistryCredential = domain.HostingRegistryCredentialRef{SecretID: "secret", Version: 1}
	op := domain.HostingOperation{ID: "registry", RunID: lease.RunID, BindingID: snapshot.BindingID, Snapshot: snapshot, State: "preparing", TaskMode: "deploy"}
	var credentialCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/plan":
			_ = json.NewEncoder(w).Encode(op)
		case "/api/v1/runners/hosting/registry-credential":
			credentialCalls.Add(1)
			supervisor.cancel()
			w.WriteHeader(503)
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	reporter := startAttemptReporter(supervisor.Context(), supervisor, j, server.URL, "token", lease.RunID, lease)
	defer reporter.Stop()
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: lease.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: op.ID, HostingMode: "deploy"}}, Lease: lease}
	claim.PrimitivePlan.RunID = lease.RunID
	err := executeConnectedImageClaim(supervisor, j, reporter, func(context.Context) error { return nil }, server.URL, "token", claim, "", false, nil, &DatabaseExecutionConfig{ApplicationDatabase: true}, &RegistryPullConfig{TrustedLoopbackHTTP: true})
	if credentialCalls.Load() != 1 || err == nil || strings.Contains(err.Error(), "explicit configuration") {
		t.Fatal("ordinary private image lost registry configuration in connected dispatcher", credentialCalls.Load(), err)
	}
}
