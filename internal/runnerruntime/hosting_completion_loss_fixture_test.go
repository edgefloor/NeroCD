package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
)

const hostingCompletionFixtureBodyLimit = 1 << 20

type hostingCompletionLossProof struct {
	Request     domain.HostingCompleteRequest
	RequestHash string
	Receipt     domain.HostingReceipt
	Committed   bool
	Suppressed  int
	Failure     string
}

// hostingCompletionLossFixture calls the real completion handler once while
// enabled. Only retries with the same canonical full request lose their reply.
// Its mutex also serializes concurrent retries behind the first real handler.
type hostingCompletionLossFixture struct {
	mu        sync.Mutex
	original  http.Handler
	enabled   bool
	attempted bool
	canonical []byte
	proof     hostingCompletionLossProof
}

func (f *hostingCompletionLossFixture) enable()  { f.mu.Lock(); defer f.mu.Unlock(); f.enabled = true }
func (f *hostingCompletionLossFixture) disable() { f.mu.Lock(); defer f.mu.Unlock(); f.enabled = false }
func (f *hostingCompletionLossFixture) snapshot() hostingCompletionLossProof {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proof
}
func (f *hostingCompletionLossFixture) fail(w http.ResponseWriter, status int, message string) {
	f.proof.Failure = message
	http.Error(w, message, status)
}
func (f *hostingCompletionLossFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if !f.enabled {
		f.mu.Unlock()
		f.original.ServeHTTP(w, r)
		return
	}
	defer f.mu.Unlock()
	if f.proof.Failure != "" {
		http.Error(w, f.proof.Failure, http.StatusConflict)
		return
	}
	if r.Method != http.MethodPost {
		f.fail(w, http.StatusBadRequest, "completion fixture requires POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, hostingCompletionFixtureBodyLimit+1))
	if err != nil {
		f.fail(w, http.StatusBadRequest, "completion fixture could not read bounded body")
		return
	}
	if len(body) > hostingCompletionFixtureBodyLimit {
		f.fail(w, http.StatusRequestEntityTooLarge, "completion fixture body exceeds limit")
		return
	}
	var request domain.HostingCompleteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		f.fail(w, http.StatusBadRequest, "completion fixture requires a typed request")
		return
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		f.fail(w, http.StatusBadRequest, "completion fixture requires one document")
		return
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "completion fixture cannot canonicalize request")
		return
	}
	if f.attempted {
		if !f.proof.Committed || !bytes.Equal(canonical, f.canonical) {
			f.fail(w, http.StatusConflict, "completion fixture retry identity changed")
			return
		}
		f.proof.Suppressed++
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	f.attempted = true
	f.canonical = canonical
	f.proof.Request = request
	f.proof.RequestHash = domain.HostingDocumentHash(request)
	// Keep the caller's context. The first handler must really commit successfully;
	// canceled later retries never re-enter it and cannot fabricate commit proof.
	r.Body = io.NopCloser(bytes.NewReader(body))
	recorded := httptest.NewRecorder()
	f.original.ServeHTTP(recorded, r)
	if recorded.Code != http.StatusOK {
		f.proof.Failure = "completion fixture first handler did not return 200"
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
		return
	}
	var op domain.HostingOperation
	if recorded.Body.Len() > hostingCompletionFixtureBodyLimit || json.Unmarshal(recorded.Body.Bytes(), &op) != nil || op.ID != request.OperationID || op.RunID != request.RunID || op.State != "succeeded" || op.Result == nil || *op.Result != request.Result || op.CompleteReceipt == nil || op.CompleteReceipt.ID != request.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(request.Result) {
		f.fail(w, http.StatusBadGateway, "completion fixture success proof did not match request")
		return
	}
	f.proof.Receipt = *op.CompleteReceipt
	f.proof.Committed = true
	f.proof.Suppressed++
	w.WriteHeader(http.StatusServiceUnavailable)
}

func fixtureCompletionRequest() domain.HostingCompleteRequest {
	return domain.HostingCompleteRequest{HostingAuthority: domain.HostingAuthority{OperationID: "operation", RunID: "run_operation", LeaseID: "lease_operation", Attempt: 2, Fence: "fence_operation"}, ReceiptID: "result_receipt", Result: domain.HostingResult{Outcome: "succeeded", ContainerID: strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "daemon", PlanHash: "sha256:" + strings.Repeat("c", 64), Ready: true}}
}
func fixtureCompletionSuccess(w http.ResponseWriter, in domain.HostingCompleteRequest) {
	op := domain.HostingOperation{ID: in.OperationID, RunID: in.RunID, State: "succeeded", Result: &in.Result, CompleteReceipt: &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Result)}}
	_ = json.NewEncoder(w).Encode(op)
}
func fixtureCompletionCall(f *hostingCompletionLossFixture, in domain.HostingCompleteRequest, canceled bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(in)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runners/hosting/complete", bytes.NewReader(body))
	if canceled {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		r = r.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w
}
func TestHostingCompletionLossFixtureRejectsFirstFailure(t *testing.T) {
	var calls int
	f := &hostingCompletionLossFixture{original: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "real first failure", http.StatusInternalServerError)
	})}
	f.enable()
	in := fixtureCompletionRequest()
	first := fixtureCompletionCall(f, in, false)
	if first.Code != 500 || !strings.Contains(first.Body.String(), "real first failure") {
		t.Fatal("first failure was hidden", first.Code)
	}
	if retry := fixtureCompletionCall(f, in, true); retry.Code != 409 {
		t.Fatal("failed first handler became lost success", retry.Code)
	}
	proof := f.snapshot()
	if calls != 1 || proof.Committed || proof.Failure == "" || proof.Suppressed != 0 {
		t.Fatalf("fabricated proof %+v calls%d", proof, calls)
	}
}
func TestHostingCompletionLossFixtureSuppressesOnlyCommittedIdenticalRetries(t *testing.T) {
	in := fixtureCompletionRequest()
	var calls atomic.Int32
	f := &hostingCompletionLossFixture{original: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Context().Err() != nil {
			http.Error(w, "canceled real handler", 500)
			return
		}
		fixtureCompletionSuccess(w, in)
	})}
	f.enable()
	if w := fixtureCompletionCall(f, in, false); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() {
			if w := fixtureCompletionCall(f, in, true); w.Code != 503 {
				t.Errorf("identical canceled retry got%d", w.Code)
			}
		})
	}
	group.Wait()
	proof := f.snapshot()
	if calls.Load() != 1 || !proof.Committed || proof.Suppressed != 9 || proof.Failure != "" || proof.Request != in || proof.RequestHash != domain.HostingDocumentHash(in) {
		t.Fatalf("proof %+v calls%d", proof, calls.Load())
	}
	f.disable()
	if w := fixtureCompletionCall(f, in, false); w.Code != 200 {
		t.Fatal("exact replay did not reach real handler", w.Code)
	}
	if calls.Load() != 2 {
		t.Fatal("replay handler count", calls.Load())
	}
}
func TestHostingCompletionLossFixtureRejectsChangedIdentity(t *testing.T) {
	for _, entry := range []struct {
		name   string
		change func(*domain.HostingCompleteRequest)
	}{
		{"operation", func(in *domain.HostingCompleteRequest) { in.OperationID = "other" }}, {"run", func(in *domain.HostingCompleteRequest) { in.RunID = "other" }}, {"lease", func(in *domain.HostingCompleteRequest) { in.LeaseID = "other" }}, {"attempt", func(in *domain.HostingCompleteRequest) { in.Attempt++ }}, {"fence", func(in *domain.HostingCompleteRequest) { in.Fence = "other" }}, {"receipt", func(in *domain.HostingCompleteRequest) { in.ReceiptID = "other" }}, {"result", func(in *domain.HostingCompleteRequest) { in.Result.ImageID = "other" }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			in := fixtureCompletionRequest()
			calls := 0
			f := &hostingCompletionLossFixture{original: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; fixtureCompletionSuccess(w, in) })}
			f.enable()
			if w := fixtureCompletionCall(f, in, false); w.Code != 503 {
				t.Fatal(w.Code)
			}
			entry.change(&in)
			w := fixtureCompletionCall(f, in, true)
			if w.Code != 409 || f.snapshot().Failure == "" || calls != 1 {
				t.Fatal("changed retry hidden", w.Code, calls)
			}
		})
	}
}
func TestHostingCompletionLossFixtureBoundsAndRequiresSuccessProof(t *testing.T) {
	for _, body := range []string{strings.Repeat(" ", hostingCompletionFixtureBodyLimit+1), `{} {}`, `{"unexpected":"field"}`} {
		calls := 0
		f := &hostingCompletionLossFixture{original: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })}
		f.enable()
		w := httptest.NewRecorder()
		f.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/complete", strings.NewReader(body)))
		if w.Code == 503 || calls != 0 || f.snapshot().Failure == "" {
			t.Fatal("invalid body entered loss path", w.Code, calls)
		}
	}
	f := &hostingCompletionLossFixture{original: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })}
	f.enable()
	if w := fixtureCompletionCall(f, fixtureCompletionRequest(), false); w.Code != 502 || f.snapshot().Committed {
		t.Fatal("unproven200 hidden", w.Code)
	}
}
