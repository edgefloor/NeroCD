package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type storageRouteRecoveryHTTPRepository struct {
	*store.MemoryStore
	calls int
	err   error
}

func (r *storageRouteRecoveryHTTPRepository) RecoverHostingStorageRouteContinuity(_ context.Context, resource, operation, _ string, _ domain.HostingStorageRouteRecoveryInput, _ domain.AuditEvent) (domain.HostingStorageRouteContinuityStatus, error) {
	r.calls++
	return domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: "proxy_pending", SuccessorProxyOperationID: "pop_storage_" + operation, Reason: "pending"}, r.err
}

func TestHostingStorageRouteRecoveryHTTP(t *testing.T) {
	mem := newSeededTestStore()
	repo := &storageRouteRecoveryHTTPRepository{MemoryStore: mem}
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(s, slog.Default(), web.Static())
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage recovery API"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/resources/" + resource.ID + "/storage-route-continuity/hop_next/recover"
	in := domain.HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: "sha256:" + strings.Repeat("a", 64), ExpectedCompleteReceiptID: "complete_next", ExpectedCompleteReceiptHash: "sha256:" + strings.Repeat("b", 64)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	request := func(url, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, r)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("no-store missing: %+v", rec.Header())
		}
		return rec
	}
	if rec := request(path, "", string(raw)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body.String())
	}
	token := createTestSession(t, server)
	for _, body := range []string{`{}`, `null`, string(raw) + `{}`, strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`, strings.TrimSuffix(string(raw), "}") + `,"expected_complete_receipt_id":"other"}`} {
		if rec := request(path, token, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := request(path+"?actor=other", token, string(raw)); rec.Code != http.StatusBadRequest {
		t.Fatalf("query: %d", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatal("rejected request reached repository")
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer} {
		account, e := s.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: resource.ProjectID, Name: "recovery " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		want := http.StatusForbidden
		if role == domain.RoleMaintainer {
			want = http.StatusOK
		}
		if rec := request(path, account.Token, string(raw)); rec.Code != want {
			t.Fatalf("%s: %d %s", role, rec.Code, rec.Body.String())
		}
	}
	for _, mapping := range []struct {
		err    error
		status int
		code   string
	}{{nil, 200, ""}, {auth.ErrForbidden, 403, "forbidden"}, {store.ErrNotFound, 404, "not_found"}, {store.ErrConflict, 409, "conflict"}, {store.ErrHostingStorageUnavailable, 503, "hosting_storage_unavailable"}} {
		repo.err = mapping.err
		rec := request(path, token, string(raw))
		var body map[string]json.RawMessage
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if rec.Code != mapping.status {
			t.Fatalf("%v: %d %s", mapping.err, rec.Code, rec.Body.String())
		}
		if mapping.err != nil && string(body["code"]) != `"`+mapping.code+`"` {
			t.Fatal(rec.Body.String())
		}
		for _, private := range []string{"snapshot", "daemon_id", "storage", "configuration", "https_ready"} {
			if _, ok := body[private]; ok {
				t.Fatalf("private recovery response: %s", rec.Body.String())
			}
		}
	}
}
