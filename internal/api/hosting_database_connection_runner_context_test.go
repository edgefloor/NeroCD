package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type connectionContextHTTPRepository struct {
	*store.MemoryStore
	store.HostingExecutionRepository
	fingerprint string
	rotate      bool
	calls       int
	accepted    int
}

func (r *connectionContextHTTPRepository) authorize(ctx context.Context, runner string) error {
	r.calls++
	// Rebinding to a foreign fingerprint must fail. If the context is absent,
	// rebinding would succeed and expose a missing HTTP bridge at this boundary.
	if _, err := store.BindHostingDatabaseConnectionRunnerContext(ctx, runner, strings.Repeat("0", 64)); err == nil {
		return store.ErrConflict
	}
	if _, err := store.BindHostingDatabaseConnectionRunnerContext(ctx, runner, r.fingerprint); err != nil {
		return err
	}
	if r.rotate {
		if _, err := r.UpdateRunnerToken(ctx, runner, strings.Repeat("2", 64), domain.RunnerActive, time.Now()); err != nil {
			return err
		}
	}
	current, err := r.GetRunnerByID(ctx, runner)
	if err != nil {
		return err
	}
	if _, err = store.BindHostingDatabaseConnectionRunnerContext(ctx, runner, current.TokenHash); err != nil {
		return err
	}
	r.accepted++
	return nil
}

func (r *connectionContextHTTPRepository) operation(ctx context.Context, runner string) (domain.HostingOperation, error) {
	err := r.authorize(ctx, runner)
	return domain.HostingOperation{ID: "connection_operation", ApplicationDatabase: &domain.HostingApplicationDatabaseExecutionProof{}}, err
}

func (r *connectionContextHTTPRepository) ClaimRun(ctx context.Context, runner string, _ time.Time, _ time.Duration, _ ...store.MutationOption) (domain.ClaimedRun, error) {
	if err := r.authorize(ctx, runner); err != nil {
		return domain.ClaimedRun{}, err
	}
	return domain.ClaimedRun{}, store.ErrNotFound
}

func (r *connectionContextHTTPRepository) HostingRunnerPlan(ctx context.Context, runner string, _ domain.HostingAuthority) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingPrepared(ctx context.Context, runner string, _ domain.HostingPreparedRequest) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingApplyBegin(ctx context.Context, runner string, _ domain.HostingAuthority) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingStatus(ctx context.Context, runner string, _ domain.HostingStatusRequest) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) ClaimHostingObservation(ctx context.Context, runner string, _ domain.HostingAuthority, _ time.Duration) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingComplete(ctx context.Context, runner string, _ domain.HostingCompleteRequest) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingReconciliation(ctx context.Context, runner string, _ domain.HostingObservation) (domain.HostingOperation, error) {
	return r.operation(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingStorageCreateBegin(ctx context.Context, runner string, _ domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	return domain.HostingStorageCreateAck{}, r.authorize(ctx, runner)
}
func (r *connectionContextHTTPRepository) HostingStorageMaterialized(ctx context.Context, runner string, _ domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	return domain.HostingStorageMaterializedAck{}, r.authorize(ctx, runner)
}

func connectionContextHTTPFixture(t *testing.T) (*Server, *connectionContextHTTPRepository, string, *bytes.Buffer) {
	t.Helper()
	token := "runner-original-connection-bearer"
	hash := sha256.Sum256([]byte(token))
	mem := newSeededTestStore()
	repo := &connectionContextHTTPRepository{MemoryStore: mem, fingerprint: hex.EncodeToString(hash[:])}
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_connection_context", Name: "connection", TokenHash: repo.fingerprint, Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: mem, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: repo, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	logs := new(bytes.Buffer)
	return NewServer(service, slog.New(slog.NewTextHandler(logs, nil)), web.Static()), repo, token, logs
}

func connectionContextHTTPBodies() map[string]any {
	a := domain.HostingAuthority{OperationID: "connection_operation", RunID: "run_connection_operation", LeaseID: "connection_lease", Attempt: 1, Fence: "connection_fence"}
	id, volume := domain.HostingStorageIdentity("binding")
	ref := domain.HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}
	planHash, nonce := "sha256:"+strings.Repeat("b", 64), strings.Repeat("c", 32)
	labels := domain.ExpectedHostingStorageLabels(domain.HostingStorage{ID: id, Hash: ref.Hash, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination"}, domain.HostingStorageMaterialization{CreationOperationID: a.OperationID, CreationPlanHash: planHash, CreationNonce: nonce, DaemonID: "daemon"})
	return map[string]any{
		"claim":                        struct{}{},
		"hosting/plan":                 a,
		"hosting/prepared":             domain.HostingPreparedRequest{HostingAuthority: a},
		"hosting/apply-begin":          a,
		"hosting/status":               domain.HostingStatusRequest{HostingAuthority: a},
		"hosting/complete":             domain.HostingCompleteRequest{HostingAuthority: a},
		"hosting/reconciliation":       domain.HostingObservation{HostingAuthority: a},
		"hosting/storage-create-begin": domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: planHash, StorageRef: ref, CreationNonce: nonce},
		"hosting/storage-materialized": domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "receipt", PlanHash: planHash, StorageRef: ref, CreationOperationID: a.OperationID, CreationPlanHash: planHash, CreationNonce: nonce, DaemonID: "daemon", VolumeName: volume, Driver: "local", Scope: "local", Labels: labels},
	}
}

func TestDatabaseConnectionCommonHTTPPinsOriginalRunnerCredential(t *testing.T) {
	bodies := connectionContextHTTPBodies()
	bodies["hosting/status#claim_observation"] = domain.HostingStatusRequest{HostingAuthority: domain.HostingAuthority{OperationID: "connection_operation", RunID: "run_connection_operation", LeaseID: "connection_lease", Attempt: 1, Fence: "connection_fence"}, Phase: "claim_observation"}
	for path, body := range bodies {
		for _, mode := range []string{"original", "missing", "rotated_before", "rotated_at_authority"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				s, repo, token, logs := connectionContextHTTPFixture(t)
				if err := repo.authorize(t.Context(), "runner_connection_context"); err == nil {
					t.Fatal("new-family boundary accepted missing private context")
				}
				repo.calls = 0
				if mode == "rotated_before" {
					if _, err := repo.UpdateRunnerToken(t.Context(), "runner_connection_context", strings.Repeat("2", 64), domain.RunnerActive, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				repo.rotate = mode == "rotated_at_authority"
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest("POST", "/api/v1/runners/"+strings.Split(path, "#")[0], bytes.NewReader(raw))
				request.Header.Set("Content-Type", "application/json")
				if mode != "missing" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				response := httptest.NewRecorder()
				s.ServeHTTP(response, request)
				want, calls, accepted := 200, 1, 1
				if path == "claim" {
					want = 404
				}
				switch mode {
				case "missing", "rotated_before":
					want, calls, accepted = 401, 0, 0
				case "rotated_at_authority":
					want, accepted = 409, 0
				}
				if response.Code != want || repo.calls != calls || repo.accepted != accepted {
					t.Fatalf("status=%d want=%d calls=%d accepted=%d body=%s", response.Code, want, repo.calls, repo.accepted, response.Body.String())
				}
				if strings.Contains(logs.String(), token) || strings.Contains(response.Body.String(), token) || strings.Contains(logs.String(), repo.fingerprint) {
					t.Fatal("HTTP bridge exposed bearer or fingerprint")
				}
			})
		}
	}
}
