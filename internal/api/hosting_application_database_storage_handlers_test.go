package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func (r *connectionHTTPRepository) HostingApplicationDatabaseStorageWriter(_ context.Context, runner, credential string, in domain.HostingApplicationDatabaseStorageWriterRequest) (domain.HostingOperation, error) {
	r.recordRunner("storage/writer", runner, credential, in.HostingAuthority)
	return domain.HostingOperation{ID: in.OperationID}, r.err
}
func (r *connectionHTTPRepository) AuthorizeHostingApplicationDatabaseStorageEffect(_ context.Context, runner, credential string, in domain.HostingApplicationDatabaseStorageEffectRequest) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error) {
	r.recordRunner("storage/authorize", runner, credential, in.HostingAuthority)
	return domain.HostingApplicationDatabaseStorageEffectAuthorization{}, r.err
}
func (r *connectionHTTPRepository) HostingApplicationDatabaseStorageProxyPlan(_ context.Context, runner, credential string, in domain.HostingAuthority) (domain.HostingApplicationDatabaseStorageProxyPlan, error) {
	r.recordRunner("storage/proxy-plan", runner, credential, in)
	return domain.HostingApplicationDatabaseStorageProxyPlan{}, r.err
}

func TestApplicationDatabaseStorageHTTPRunnerBoundary(t *testing.T) {
	s, service, repo, session := connectionHTTPFixture(t, true)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	registered, err := service.RegisterRunner(admin, app.RunnerInput{ID: "storage_runner", Name: "storage", Capabilities: []string{domain.HostingApplicationDatabaseCapability, domain.HostingApplicationDatabaseStorageCapability}})
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: "hop_connection", RunID: "run_connection", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	hash := "sha256:" + strings.Repeat("b", 64)
	storageID, _ := domain.HostingStorageIdentity("binding_connection")
	receipt := domain.HostingApplicationDatabaseStorageWriterReceipt{Version: domain.HostingApplicationDatabaseStorageVersion, ReceiptID: "stop_intent", Phase: "stop_intent", PlanHash: hash, StorageRef: domain.HostingStorageRef{StorageID: storageID, Generation: 1, Hash: hash}, PredecessorHash: hash}
	receipt.Hash = receipt.DocumentHash()
	values := map[string]any{"storage/writer": domain.HostingApplicationDatabaseStorageWriterRequest{HostingAuthority: a, Receipt: receipt}, "storage/authorize": domain.HostingApplicationDatabaseStorageEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("a", 32), Action: "stop_predecessor", StateHash: hash}, "storage/proxy-plan": a}
	sum := sha256.Sum256([]byte(registered.Token))
	for method, value := range values {
		t.Run(method, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			path := "/api/v1/runners/hosting/database-binding/" + method
			for _, token := range []string{"", session, "invalid_bearer"} {
				count := len(repo.calls)
				w := connectionCall(t, s, "POST", path, token, body)
				if w.Code != 401 || len(repo.calls) != count {
					t.Fatalf("untrusted bearer status=%d", w.Code)
				}
			}
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(createBrowserSession(t, s))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != 401 {
				t.Fatal("browser cookie admitted", w.Code)
			}
			for _, bad := range []string{strings.TrimSuffix(body, "}") + `,"unexpected":true}`, `{"operation_id":"hop_connection"}`, strings.Replace(body, `"attempt":1`, `"attempt":0`, 1)} {
				count := len(repo.calls)
				w := connectionCall(t, s, "POST", path, registered.Token, bad)
				if w.Code != 400 || len(repo.calls) != count {
					t.Fatalf("invalid body status=%d %s", w.Code, w.Body.String())
				}
			}
			count := len(repo.calls)
			w = connectionCall(t, s, "POST", path, registered.Token, body)
			if w.Code != 200 || len(repo.calls) != count+1 || repo.calls[count] != method || repo.authority != a || repo.runner != registered.Runner.ID || repo.credential != hex.EncodeToString(sum[:]) {
				t.Fatalf("runner identity/routing status=%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), registered.Token) {
				t.Fatal("bearer reflected")
			}
		})
	}
}
