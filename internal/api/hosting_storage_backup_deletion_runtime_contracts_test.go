package api

import (
	"bytes"
	"encoding/json"
	"io"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The real browser receives one lost execution ACK after the production handler
// commits. Capture only safe typed request/response metadata for exact replay.
type hostingStorageDeletionRuntimeWire struct {
	mu                sync.Mutex
	resource          string
	backup            string
	previewRequest    domain.HostingStorageBackupDeletionPreviewInput
	preview           domain.HostingStorageBackupRetentionPreview
	executionRequest  domain.HostingStorageBackupDeletionInput
	execution         domain.HostingStorageBackupRetentionExecution
	executionRequests int
	failed            bool
}

func (f *hostingStorageDeletionRuntimeWire) serve(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	base := "/api/v1/resources/" + f.resource + "/storage-backups/" + f.backup
	if r.Method != "POST" || (r.URL.Path != base+"/deletion-previews" && r.URL.Path != base+"/deletions") {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(raw) > 8192 {
		w.WriteHeader(400)
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	response := httptest.NewRecorder()
	next.ServeHTTP(response, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == base+"/deletion-previews" && response.Code == 200 {
		if json.Unmarshal(raw, &f.previewRequest) != nil || json.Unmarshal(response.Body.Bytes(), &f.preview) != nil {
			f.failed = true
		}
	}
	if r.URL.Path == base+"/deletions" && response.Code == 202 {
		var request domain.HostingStorageBackupDeletionInput
		var execution domain.HostingStorageBackupRetentionExecution
		if json.Unmarshal(raw, &request) != nil || json.Unmarshal(response.Body.Bytes(), &execution) != nil {
			f.failed = true
		}
		f.executionRequests++
		if f.executionRequests == 1 {
			f.executionRequest, f.execution = request, execution
			http.Error(w, "fixture lost committed deletion response", http.StatusServiceUnavailable)
			return true
		}
		if request != f.executionRequest || domain.HostingDocumentHash(execution) != domain.HostingDocumentHash(f.execution) {
			f.failed = true
		}
	}
	hostingStorageRemoteRuntimeCopy(w, response)
	return true
}
func TestHostingStorageDeletionBrowserExecutionACKContract(t *testing.T) {
	wire := &hostingStorageDeletionRuntimeWire{resource: "resource", backup: "backup"}
	request := domain.HostingStorageBackupDeletionInput{RequestID: "request", PreviewID: "preview", ExpectedPlanHash: "sha256:" + strings.Repeat("a", 64)}
	execution := domain.HostingStorageBackupRetentionExecution{ID: "execution", PreviewID: "preview", PlanHash: request.ExpectedPlanHash}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(execution)
	})
	for i, want := range []int{503, 202} {
		raw, _ := json.Marshal(request)
		out := httptest.NewRecorder()
		if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/resources/resource/storage-backups/backup/deletions", bytes.NewReader(raw)), handler) || out.Code != want {
			t.Fatalf("browser ACK round %d status %d", i, out.Code)
		}
	}
	if wire.failed || wire.executionRequests != 2 || wire.executionRequest != request || wire.execution.ID != "execution" {
		t.Fatal("browser retry did not preserve exact batch")
	}
	if wire.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/another/storage-backups/backup/deletions", nil), handler) {
		t.Fatal("fault escaped exact resource")
	}
	if wire.serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/resources/resource/storage-backups/backup/deletions", nil), handler) {
		t.Fatal("fault intercepted history")
	}
}
