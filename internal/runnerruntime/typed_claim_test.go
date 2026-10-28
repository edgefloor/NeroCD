package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func composeExecutionTestClaim() domain.ClaimedRun {
	return domain.ClaimedRun{
		Run:           domain.TaskRun{ID: "run_compose", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": "deployment"}}},
		Lease:         domain.RunLease{ID: "lease", RunID: "run_compose", RunnerID: "runner", Attempt: 1, Fence: "fence", Status: domain.LeaseActive, ExpiresAt: time.Now().Add(time.Minute)},
		PrimitivePlan: domain.RunnerPrimitivePlan{RunID: "run_compose"},
	}
}

func TestTypedClaimRejectsGenericInstructionsBeforeAnySideEffects(t *testing.T) {
	cases := map[string]func(*domain.ClaimedRun){
		"noncanonical type": func(c *domain.ClaimedRun) { c.Run.RunSpec.Type = " " + domain.RunTypeComposeDeploy },
		"primitive process": func(c *domain.ClaimedRun) {
			c.PrimitivePlan.Process = &domain.ProcessSpec{Command: []string{"touch", "should-not-exist"}}
		},
		"primitive checkout":  func(c *domain.ClaimedRun) { c.PrimitivePlan.Checkout = &domain.CheckoutPlan{} },
		"primitive artifacts": func(c *domain.ClaimedRun) { c.PrimitivePlan.Artifacts = []domain.ArtifactSpec{{Path: "secret"}} },
		"spec process":        func(c *domain.ClaimedRun) { c.Run.RunSpec.Process = &domain.ProcessSpec{} },
		"spec repository":     func(c *domain.ClaimedRun) { c.Run.RunSpec.Repository = &domain.RepositoryRef{} },
		"spec artifacts":      func(c *domain.ClaimedRun) { c.Run.RunSpec.Artifacts = []domain.ArtifactSpec{{Path: "secret"}} },
		"spec workflow":       func(c *domain.ClaimedRun) { c.Run.RunSpec.Workflow = &domain.Workflow{} },
		"run workflow":        func(c *domain.ClaimedRun) { c.Run.Workflow.Steps = []domain.WorkflowStep{{}} },
		"command input":       func(c *domain.ClaimedRun) { c.Run.RunSpec.Inputs["command"] = "touch marker" },
		"missing deployment":  func(c *domain.ClaimedRun) { delete(c.Run.RunSpec.Inputs, "deployment_id") },
		"missing fence":       func(c *domain.ClaimedRun) { c.Lease.Fence = "" },
		"missing lease":       func(c *domain.ClaimedRun) { c.Lease.ID = "" },
		"wrong run":           func(c *domain.ClaimedRun) { c.Lease.RunID = "another" },
		"wrong plan":          func(c *domain.ClaimedRun) { c.PrimitivePlan.RunID = "another" },
		"missing attempt":     func(c *domain.ClaimedRun) { c.Lease.Attempt = 0 },
		"expired":             func(c *domain.ClaimedRun) { c.Lease.ExpiresAt = time.Now().Add(-time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claim := composeExecutionTestClaim()
			mutate(&claim)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			root := t.TempDir()
			err := executeClaimWithOptions(server.URL, "token", claim, filepath.Join(root, "work"), time.Millisecond, nil, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{})
			if err == nil || requests.Load() != 0 {
				t.Fatalf("rejected claim touched API: error=%v requests=%d", err, requests.Load())
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatal("rejected claim created workspace")
			}
		})
	}
}

func TestComposeDispatchRequiresMatchingFencedPlan(t *testing.T) {
	claim := composeExecutionTestClaim()
	// Server-created Compose tasks can carry secret binding metadata. Secret
	// resolution must still use the separately authorized deployment plan.
	claim.Run.RunSpec.Secrets = []domain.SecretBinding{{Name: "application"}}
	claim.PrimitivePlan.Secrets = claim.Run.RunSpec.Secrets
	var planRequests atomic.Int32
	var unexpected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/renew", "/api/v1/runners/lease":
			_ = json.NewEncoder(w).Encode(claim.Lease)
		case "/api/v1/runners/deployments/plan":
			planRequests.Add(1)
			if r.URL.Query().Get("deployment_id") != "deployment" || r.URL.Query().Get("fence") != claim.Lease.Fence {
				t.Error("missing fenced deployment request")
			}
			_ = json.NewEncoder(w).Encode(domain.DeploymentPlan{DeploymentID: "deployment", RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: "wrong-fence", Status: domain.DeploymentAssigned})
		default:
			unexpected.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	journal := openReplayTestJournal(t)
	defer func() { _ = journal.Close() }()
	root := t.TempDir()
	err := executeClaimWithOptions(server.URL, "token", claim, root, time.Hour, journal, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{})
	if err == nil || planRequests.Load() != 1 || unexpected.Load() != 0 || journal.Depth() != 0 {
		t.Fatalf("plan fence rejection: %v plans=%d unexpected=%d depth=%d", err, planRequests.Load(), unexpected.Load(), journal.Depth())
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("mismatched plan created workspace")
	}
}
