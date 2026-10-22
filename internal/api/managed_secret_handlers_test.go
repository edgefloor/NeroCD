package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

func newManagedTestServer(t *testing.T, enabled bool) (*Server, *store.MemoryStore) {
	t.Helper()
	mem := newSeededTestStore()
	var ring *secretcrypto.Keyring
	var err error
	if enabled {
		ring, err = secretcrypto.New("test", map[string][]byte{"test": bytes.Repeat([]byte{8}, 32)})
		if err != nil {
			t.Fatal(err)
		}
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Projects: mem, Hosting: mem, Destinations: mem, ManagedSecrets: mem, SecretKeyring: ring, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static()), mem
}
func TestManagedSecretAPI(t *testing.T) {
	server, mem := newManagedTestServer(t, true)
	token := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	if _, err := mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "managed_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, credential string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s status=%d want=%d cache=%q body=%s", method, path, rec.Code, want, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	const root = "/api/v1/managed-secrets"
	request("GET", root+"?project_id=proj_platform", "", "", 401)
	request("GET", root, "", token, 400)
	request("GET", root+"?project_id=missing", "", token, 404)
	request("GET", root+"?limit=-1", "", token, 400)
	create := `{"project_id":"proj_platform","name":"test","value":"  private\n"}`
	var item domain.ManagedSecret
	if err := json.Unmarshal(request("POST", root, create, token, 201), &item); err != nil {
		t.Fatal(err)
	}
	path := root + "/" + item.ID
	request("POST", root, create, token, 409)
	request("POST", root, create, viewer, 403)
	metadata := request("GET", path, "", viewer, 200)
	if bytes.Contains(metadata, []byte("private")) {
		t.Fatal("metadata contains value")
	}
	request("POST", path+"/reveal", `{"version":1}`, viewer, 403)
	request("POST", path+"/revoke", `{"expected_version":1}`, viewer, 403)
	request("POST", path+"/versions", `{"expected_version":1,"value":"new"}`, viewer, 403)
	for _, body := range []string{`{}`, `{"expected_version":1,"value":null}`, `{"expected_version":1,"value":""}`, `{"expected_version":1,"value":"x","project_id":"proj_security"}`, `{"expected_version":1,"value":"\ud800"}`, "{\"expected_version\":1,\"value\":\"" + string([]byte{255}) + "\"}"} {
		request("POST", path+"/versions", body, token, 400)
	}
	request("POST", path+"/versions", `{"expected_version":1,"value":"new"}`, token, 201)
	request("POST", path+"/versions", `{"expected_version":1,"value":"stale"}`, token, 409)
	request("GET", path+"/versions?limit=1&offset=0", "", viewer, 200)
	var reveal app.ManagedSecretReveal
	if err := json.Unmarshal(request("POST", path+"/reveal", `{"version":1}`, token, 200), &reveal); err != nil || reveal.Value != "  private\n" {
		t.Fatalf("reveal: %v", err)
	}
	request("POST", path+"/reveal", `{}`, token, 400)
	request("POST", path+"/reveal", `{"version":99}`, token, 404)
	request("POST", path+"/revoke", `{"expected_version":1}`, token, 409)
	request("POST", path+"/revoke", `{"expected_version":2}`, token, 200)
	request("POST", path+"/reveal", `{"version":1}`, token, 409)
	request("POST", path+"/versions", `{"expected_version":2,"value":"new"}`, token, 409)
	request("GET", path, "", token, 200)
	request("GET", path+"/versions", "", token, 200)
	disabled, _ := newManagedTestServer(t, false)
	disabledToken := createTestSession(t, disabled)
	req := httptest.NewRequest(http.MethodPost, root, strings.NewReader(create))
	req.Header.Set("Authorization", "Bearer "+disabledToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	disabled.ServeHTTP(rec, req)
	if rec.Code != 503 || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("disabled create status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestManagedJSONUnicode(t *testing.T) {
	for _, value := range []string{`{"value":"\ud83d\ude00"}`, `{"value":"\\ud800"}`, `{"value":"árvíz"}`} {
		if !validManagedJSONUnicode([]byte(value)) {
			t.Fatalf("valid Unicode rejected: %s", value)
		}
	}
	for _, value := range []string{`{"value":"\ud800"}`, `{"value":"\udc00"}`, `{"value":"\ud800\u0041"}`} {
		if validManagedJSONUnicode([]byte(value)) {
			t.Fatal("invalid Unicode accepted")
		}
	}
}
