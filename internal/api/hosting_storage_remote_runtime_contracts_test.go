package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
)

// Local wire structs keep this independently compilable before the product
// commit. The contract test must compare them with production on integration.
type hostingStorageRemoteRuntimeTransfer struct {
	ID                 string                            `json:"id"`
	Mode               string                            `json:"mode"`
	State              string                            `json:"state"`
	BackupID           string                            `json:"backup_id"`
	RestoreOperationID string                            `json:"restore_operation_id"`
	TargetID           string                            `json:"target_id"`
	TargetRevision     int64                             `json:"target_revision"`
	FailureCode        string                            `json:"failure_code"`
	Blob               *domain.HostingStorageArchiveBlob `json:"blob"`
}

type hostingStorageRemoteRuntimeWire struct {
	mu               sync.Mutex
	backup           string
	first            bool
	body, response   [32]byte
	replays          int
	entered, release chan struct{}
}

// Only a successful real upload receipt can trigger response loss. A retry
// must send the identical body and receive the identical committed response.
func (f *hostingStorageRemoteRuntimeWire) serve(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/storage-backup-transfer/mutation" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		w.WriteHeader(400)
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var phase struct {
		Phase string `json:"phase"`
	}
	if json.Unmarshal(raw, &phase) != nil || phase.Phase != "complete" {
		next.ServeHTTP(w, r)
		return true
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r)
	var transfer hostingStorageRemoteRuntimeTransfer
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &transfer) != nil || transfer.Mode != "upload" || transfer.BackupID != f.backup || transfer.State != "succeeded" {
		hostingStorageRemoteRuntimeCopy(w, rec)
		return true
	}
	body, response := sha256.Sum256(raw), sha256.Sum256(rec.Body.Bytes())
	f.mu.Lock()
	if !f.first {
		f.first = true
		f.body = body
		f.response = response
		close(f.entered)
		f.mu.Unlock()
		select {
		case <-f.release:
		case <-r.Context().Done():
		}
		return true
	}
	same := f.body == body && f.response == response
	if same {
		f.replays++
	}
	f.mu.Unlock()
	if !same {
		w.WriteHeader(409)
		return true
	}
	hostingStorageRemoteRuntimeCopy(w, rec)
	return true
}
func hostingStorageRemoteRuntimeCopy(w http.ResponseWriter, r *httptest.ResponseRecorder) {
	for key, values := range r.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(r.Code)
	_, _ = w.Write(r.Body.Bytes())
}

// The proxy forwards real signed S3 traffic. Faults alter only object GET bytes;
// policy denial and successful writes are decided by the pinned Silo server.
type hostingStorageRemoteRuntimeObservation struct {
	mu                  sync.Mutex
	object              string
	fault               string
	gets, puts          int
	retentionDeletes    map[string]int
	unconditionedDelete bool
}

func (o *hostingStorageRemoteRuntimeObservation) observe(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/nerocd/storage/") && !strings.Contains(r.URL.Path, "retention-probes/") {
		if o.retentionDeletes == nil {
			o.retentionDeletes = map[string]int{}
		}
		o.retentionDeletes[r.URL.Path]++
		if r.Header.Get("If-Match") == "" || r.Header.Get("If-Match") == "*" {
			o.unconditionedDelete = true
		}
	}
	if r.URL.Path == o.object {
		if r.Method == "GET" {
			o.gets++
		}
		if r.Method == "PUT" {
			o.puts++
		}
	}
}
func (o *hostingStorageRemoteRuntimeObservation) modify(r *http.Response) error {
	o.mu.Lock()
	mode := o.fault
	match := r.Request.URL.Path == o.object
	o.mu.Unlock()
	if !match || r.Request.Method != "GET" || r.StatusCode != 200 || mode == "" {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	closeErr := r.Body.Close()
	if err != nil || closeErr != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return fmt.Errorf("bounded S3 fault response unavailable")
	}
	switch mode {
	case "corrupt":
		raw[0] ^= 0xff
	case "truncated":
		raw = raw[:len(raw)/2]
	default:
		return fmt.Errorf("unknown S3 fault")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.Header.Set("Content-Length", fmt.Sprint(len(raw)))
	return nil
}
func (o *hostingStorageRemoteRuntimeObservation) set(object, fault string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.object = object
	o.fault = fault
}
func (o *hostingStorageRemoteRuntimeObservation) counts() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gets, o.puts
}

func TestHostingStorageRemoteReceiptFaultContract(t *testing.T) {
	for _, tc := range []struct {
		name, path, mode, state, backup string
		status                          int
		trigger                         bool
	}{
		{"exact successful upload", "storage-backup-transfer", "upload", "succeeded", "backup", 200, true},
		{"database family excluded", "database-backup-transfer", "upload", "succeeded", "backup", 200, false},
		{"validation excluded", "storage-backup-transfer", "validate", "succeeded", "backup", 200, false},
		{"download excluded", "storage-backup-transfer", "download", "succeeded", "backup", 200, false},
		{"failed excluded", "storage-backup-transfer", "upload", "failed", "backup", 200, false},
		{"another backup excluded", "storage-backup-transfer", "upload", "succeeded", "other", 200, false},
		{"HTTP failure excluded", "storage-backup-transfer", "upload", "succeeded", "backup", 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &hostingStorageRemoteRuntimeWire{backup: "backup", entered: make(chan struct{}), release: make(chan struct{})}
			close(wire.release)
			response := hostingStorageRemoteRuntimeTransfer{ID: "transfer", Mode: tc.mode, State: tc.state, BackupID: tc.backup}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != `{"phase":"complete","receipt_id":"stable"}` {
					t.Error("fault changed request")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(response)
			})
			body := `{"phase":"complete","receipt_id":"stable"}`
			invoke := func() *httptest.ResponseRecorder {
				out := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/api/v1/runners/hosting/"+tc.path+"/mutation", strings.NewReader(body))
				if !wire.serve(out, req, handler) {
					handler.ServeHTTP(out, req)
				}
				return out
			}
			first := invoke()
			if wire.first != tc.trigger {
				t.Fatalf("fault triggered=%t want=%t", wire.first, tc.trigger)
			}
			if !tc.trigger {
				if first.Code != tc.status {
					t.Fatal("bypass changed HTTP status")
				}
				return
			}
			replay := invoke()
			if replay.Code != 200 || wire.replays != 1 {
				t.Fatal("exact successful receipt replay failed")
			}
			body = `{"phase":"complete","receipt_id":"changed"}`
			// The real handler returns the same successful receipt, but changed request
			// identity must still be rejected by the response-loss test fixture.
			out := httptest.NewRecorder()
			wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/storage-backup-transfer/mutation", strings.NewReader(body)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
			if out.Code != 409 || wire.replays != 1 {
				t.Fatal("changed receipt body accepted")
			}
		})
	}
}

func TestHostingStorageRemoteObservationFaultContract(t *testing.T) {
	original := []byte("nerocd archive bytes remain in the local CAS")
	for _, mode := range []string{"", "corrupt", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			observation := &hostingStorageRemoteRuntimeObservation{}
			observation.set("/bucket/nerocd/storage/backup/blob.tar", mode)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(original)))
				_, _ = w.Write(original)
			}))
			defer upstream.Close()
			response, err := http.Get(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			response.Request = httptest.NewRequest("GET", "http://fixture/bucket/nerocd/storage/backup/blob.tar", nil)
			observation.observe(response.Request)
			if err := observation.modify(response); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if err != nil || closeErr != nil {
				t.Fatal("fault response read/close")
			}
			switch mode {
			case "":
				if !bytes.Equal(got, original) {
					t.Fatal("pass-through changed bytes")
				}
			case "corrupt":
				if len(got) != len(original) || bytes.Equal(got, original) {
					t.Fatal("corruption did not preserve size and change digest")
				}
			case "truncated":
				if len(got) != len(original)/2 {
					t.Fatal("truncation not observable")
				}
			}
			if response.ContentLength != int64(len(got)) {
				t.Fatal("response metadata does not match injected bytes")
			}
			observation.observe(httptest.NewRequest("PUT", "http://fixture/bucket/nerocd/storage/backup/blob.tar", nil))
			observation.observe(httptest.NewRequest("GET", "http://fixture/bucket/validation", nil))
			gets, puts := observation.counts()
			if gets != 1 || puts != 1 {
				t.Fatalf("object operation observation GET=%d PUT=%d", gets, puts)
			}
		})
	}
}

// The real browser receives one lost execution ACK after the production handler
// commits. Capture only safe typed request/response metadata for exact replay.
type hostingStorageRetentionRuntimeWire struct {
	mu                sync.Mutex
	resource          string
	previewRequest    domain.HostingStorageBackupRetentionPreviewInput
	preview           domain.HostingStorageBackupRetentionPreview
	executionRequest  domain.HostingStorageBackupRetentionExecutionInput
	execution         domain.HostingStorageBackupRetentionExecution
	executionRequests int
	failed            bool
}

func (f *hostingStorageRetentionRuntimeWire) serve(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	base := "/api/v1/resources/" + f.resource + "/storage-backup-retention"
	if r.Method != "POST" || (r.URL.Path != base+"/previews" && r.URL.Path != base+"/executions") {
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
	if r.URL.Path == base+"/previews" && response.Code == 200 {
		if json.Unmarshal(raw, &f.previewRequest) != nil || json.Unmarshal(response.Body.Bytes(), &f.preview) != nil {
			f.failed = true
		}
	}
	if r.URL.Path == base+"/executions" && response.Code == 202 {
		var request domain.HostingStorageBackupRetentionExecutionInput
		var execution domain.HostingStorageBackupRetentionExecution
		if json.Unmarshal(raw, &request) != nil || json.Unmarshal(response.Body.Bytes(), &execution) != nil {
			f.failed = true
		}
		f.executionRequests++
		if f.executionRequests == 1 {
			f.executionRequest, f.execution = request, execution
			http.Error(w, "fixture lost committed retention response", 503)
			return true
		}
		if request != f.executionRequest || domain.HostingDocumentHash(execution) != domain.HostingDocumentHash(f.execution) {
			f.failed = true
		}
	}
	hostingStorageRemoteRuntimeCopy(w, response)
	return true
}
func TestHostingStorageRetentionBrowserExecutionACKContract(t *testing.T) {
	wire := &hostingStorageRetentionRuntimeWire{resource: "resource"}
	request := domain.HostingStorageBackupRetentionExecutionInput{RequestID: "request", PreviewID: "preview", ExpectedPolicyRevision: 1, ExpectedPlanHash: "sha256:" + strings.Repeat("a", 64)}
	execution := domain.HostingStorageBackupRetentionExecution{ID: "execution", PreviewID: "preview", PlanHash: request.ExpectedPlanHash}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(execution)
	})
	for i, want := range []int{503, 202} {
		raw, _ := json.Marshal(request)
		out := httptest.NewRecorder()
		if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/resources/resource/storage-backup-retention/executions", bytes.NewReader(raw)), handler) || out.Code != want {
			t.Fatalf("browser ACK round %d status %d", i, out.Code)
		}
	}
	if wire.failed || wire.executionRequests != 2 || wire.executionRequest != request || wire.execution.ID != "execution" {
		t.Fatal("browser retry did not preserve exact batch")
	}
	if wire.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/another/storage-backup-retention/executions", nil), handler) {
		t.Fatal("fault escaped exact resource")
	}
	if wire.serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/resources/resource/storage-backup-retention/executions", nil), handler) {
		t.Fatal("fault intercepted history")
	}
}
