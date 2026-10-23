package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/domain"
)

func TestStorageMaterializationRoutesRequireDedicatedRunnerAndClosedJSON(t *testing.T) {
	s, mem := newTestServer()
	user := createTestSession(t, s)
	token := "runner_storage_protocol"
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_storage_protocol", Name: "storage", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id, volume := domain.HostingStorageIdentity("binding")
	ref := domain.HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}
	a := domain.HostingAuthority{OperationID: "operation", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	planHash := "sha256:" + strings.Repeat("b", 64)
	nonce := strings.Repeat("c", 32)
	create := domain.HostingStorageCreateRequest{HostingAuthority: a, StorageRef: ref, PlanHash: planHash, CreationNonce: nonce}
	labels := domain.ExpectedHostingStorageLabels(domain.HostingStorage{ID: id, Hash: ref.Hash, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination"}, domain.HostingStorageMaterialization{CreationOperationID: a.OperationID, CreationPlanHash: planHash, CreationNonce: nonce, DaemonID: "daemon"})
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "receipt", PlanHash: planHash, StorageRef: ref, CreationOperationID: a.OperationID, CreationPlanHash: planHash, CreationNonce: nonce, DaemonID: "daemon", VolumeName: volume, Driver: "local", Scope: "local", Labels: labels}
	for path, input := range map[string]any{"/api/v1/runners/hosting/storage-create-begin": create, "/api/v1/runners/hosting/storage-materialized": proof} {
		call := func(credential, body string, status int) {
			t.Helper()
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if credential != "" {
				r.Header.Set("Authorization", "Bearer "+credential)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: status=%d want=%d cache=%s body=%s", path, w.Code, status, w.Header().Get("Cache-Control"), w.Body.String())
			}
		}
		call("", `{}`, 401)
		call(user, `{}`, 401)
		valid, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		call(token, string(valid), 503)
		call(token, `{}`, 400)
		for _, raw := range []string{`null`, `[]`, `{"attempt":1,"attempt":2}`, `{"attempt":1,"Attempt":2}`, `{"host_path":"/tmp"}`, `{}` + `{}`} {
			call(token, raw, 400)
		}
		call(token, `{"padding":"`+strings.Repeat("x", domain.HostingStorageProtocolJSONLimit+1)+`"}`, 413)
	}
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/runners/hosting/storage-create-begin", "/api/v1/runners/hosting/storage-materialized"} {
		if document.Paths.Find(path) == nil || document.Paths.Find(path).Post == nil {
			t.Fatalf("registered storage route missing from contract: %s", path)
		}
	}
}
