package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalImportUploadExactBytesAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		declared    int64
		ok          bool
	}{{"exact", "SELECT 1;", 9, true}, {"short", "SELECT 1;", 10, false}, {"extra", "SELECT 1;", 8, false}, {"zero", "", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			err := copyImportUpload(&dst, strings.NewReader(tc.input), tc.declared)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected byte-count admission: %v", err)
			}
			if !tc.ok && dst.Len() != 0 {
				t.Fatal("unverified final bytes forwarded")
			}
		})
	}
	var broker databaseImportUploadBroker
	plan := func(id string) domain.HostingDatabaseImportStagePlan {
		return domain.HostingDatabaseImportStagePlan{Stage: domain.HostingDatabaseImportStage{ID: id}, Hash: "hash_" + id}
	}
	s, ok := broker.reserve("actor1", plan("one"))
	if !ok {
		t.Fatal("first denied")
	}
	if _, ok = broker.reserve("actor1", plan("two")); ok {
		t.Fatal("actor capacity bypass")
	}
	if _, ok = broker.reserve("actor2", plan("one")); ok {
		t.Fatal("stage overwritten")
	}
	if _, ok = broker.reserve("actor2", plan("two")); !ok {
		t.Fatal("second denied")
	}
	if _, ok = broker.reserve("actor3", plan("three")); ok {
		t.Fatal("process capacity bypass")
	}
	if _, ok = broker.claim("one", "wrong"); ok {
		t.Fatal("wrong plan claimed")
	}
	if _, ok = broker.claim("one", "hash_one"); !ok {
		t.Fatal("exact plan denied")
	}
	if _, ok = broker.claim("one", "hash_one"); ok {
		t.Fatal("second body claimed")
	}
	readDone := make(chan error, 1)
	go func() { _, err := s.reader.Read(make([]byte, 1)); readDone <- err }()
	broker.remove("one")
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("close failed")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked read survived removal")
	}
	broker.remove("two")
	if _, ok = broker.reserve("actor1", plan("one")); !ok {
		t.Fatal("capacity leaked")
	}
	broker.remove("one")
}

func TestLocalImportHTTPDeadlineOverrideAndCancellation(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := uploadDeadlines(w, time.Now().Add(time.Second)); err != nil {
			t.Error(err)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("complete"))
	}))
	server.Config.ReadTimeout = 30 * time.Millisecond
	server.Config.WriteTimeout = 30 * time.Millisecond
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		time.Sleep(80 * time.Millisecond)
		_, _ = writer.Write([]byte("SQL"))
		_ = writer.Close()
	}()
	response, err := server.Client().Post(server.URL, "application/octet-stream", reader)
	if err != nil {
		t.Fatal("scoped stream deadline did not override server timeout:", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	<-sent
	if err != nil || string(body) != "complete" {
		t.Fatal("bounded stream incomplete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	blocked, output := io.Pipe()
	done := make(chan error, 1)
	stop := watchImportUpload(ctx, func(err error) { _ = blocked.CloseWithError(err); _ = output.CloseWithError(err) }, func() error { return nil })
	go func() { _, err := io.Copy(io.Discard, blocked); done <- err }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel left blocked stream")
	}
	stop()
}

type localImportHTTPRepository struct {
	*databaseImportHTTPRepository
	mu         sync.Mutex
	plan       domain.HostingDatabaseImportStagePlan
	attempted  bool
	dispatched chan struct{}
}

func (r *localImportHTTPRepository) RequestDatabaseImportUploadStage(ctx context.Context, _, _, actor string, in domain.HostingDatabaseImportUploadInput) (domain.HostingDatabaseImportStage, error) {
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return domain.HostingDatabaseImportStage{}, err
	}
	return r.plan.Stage, nil
}
func (r *localImportHTTPRepository) DatabaseImportUpload(ctx context.Context, actor, _, hash string, begin bool) (domain.HostingDatabaseImportStagePlan, domain.HostingDatabaseImportStage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return r.plan, r.plan.Stage, false, err
	}
	if hash != r.plan.Stage.UploadHash {
		return r.plan, r.plan.Stage, false, store.ErrDatabaseImportUploadHash
	}
	attempted := r.attempted
	if begin && !attempted {
		r.attempted = true
		close(r.dispatched)
	}
	return r.plan, r.plan.Stage, attempted, nil
}
func (r *localImportHTTPRepository) RevalidateDatabaseImportUpload(ctx context.Context, actor string, _ domain.HostingDatabaseImportStagePlan) error {
	return r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *localImportHTTPRepository) DatabaseImportStagePlan(_ context.Context, runner string, in domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error) {
	if runner != "runner_archive" || in.OperationID != r.plan.Stage.ID || in.RunID != "db_import_stage_"+r.plan.Stage.ID {
		return r.plan, store.ErrConflict
	}
	return r.plan, nil
}

func TestLocalImportRegisteredCookieRoutesAndStream(t *testing.T) {
	server, base, owner, viewer, runner := archiveHTTPFixture(t)
	common := &databaseImportHTTPRepository{archiveHTTPRepository: base}
	setDatabaseImportHTTPRepository(t, server, common)
	cookie := createBrowserSession(t, server)
	server.publicOrigin = "https://127.0.0.1:25443"
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	sql := "SELECT 'local bounded body';"
	stage := domain.HostingDatabaseImportStage{ID: "local_stage", Version: domain.HostingDatabaseImportUploadVersion, TargetResourceID: base.resource.ID, Component: "mysql", ExpectedDeployedOperationID: "deployed", ExpectedResourceGeneration: 1, State: "queued", CreatedAt: now, UpdatedAt: now, UploadSource: &domain.HostingDatabaseImportUploadSource{Filename: "display.sql", Bytes: int64(len(sql))}, UploadHash: "sha256:" + strings.Repeat("a", 64), UploadDeadline: &deadline}
	common.stage = stage
	repo := &localImportHTTPRepository{databaseImportHTTPRepository: common, plan: domain.HostingDatabaseImportStagePlan{Stage: stage, ActorID: "usr_bootstrap", Hash: "sha256:" + strings.Repeat("b", 64)}, dispatched: make(chan struct{})}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	metadata := `{"request_id":"local_request","component":"mysql","expected_deployed_operation_id":"deployed","expected_resource_generation":1,"filename":"display.sql","source_bytes":27}`
	postPath := "/api/v1/resources/" + base.resource.ID + "/database-import-upload-stages"
	archiveHTTPCall(t, server, "POST", postPath, viewer, metadata, "application/json", 403)
	archiveHTTPCall(t, server, "POST", postPath, owner, metadata, "application/json", 202)
	for _, bad := range []string{strings.Replace(metadata, `display.sql`, `../display.sql`, 1), strings.Replace(metadata, `display.sql`, `\ud800.sql`, 1), strings.TrimSuffix(metadata, "}") + `,"path":"private"}`, strings.Replace(metadata, `:27`, `:0`, 1)} {
		archiveHTTPCall(t, server, "POST", postPath, owner, bad, "application/json", 400)
	}
	putPath := "/api/v1/database-import-stages/local_stage/content"
	for _, route := range []struct{ method, path, body, media string }{{"POST", postPath, metadata, "application/json"}, {"PUT", putPath, sql, "application/octet-stream"}} {
		for _, headers := range []struct{ csrf, origin string }{{"", ""}, {"1", ""}, {"1", "https://wrong.invalid"}, {"", server.publicOrigin}} {
			body := &uploadReadProbe{Reader: strings.NewReader(route.body)}
			request := httptest.NewRequest(route.method, route.path, body)
			request.AddCookie(cookie)
			request.Header.Set("Content-Type", route.media)
			request.Header.Set("X-NeroCD-CSRF", headers.csrf)
			request.Header.Set("Origin", headers.origin)
			request.Header.Set("If-Match", `"`+stage.UploadHash+`"`)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != 403 || route.method == "PUT" && body.read {
				t.Fatal("cookie authority failure consumed body", response.Code)
			}
		}
	}
	callPUT := func(hash string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("PUT", putPath, strings.NewReader(sql))
		request.AddCookie(cookie)
		request.Header.Set("X-NeroCD-CSRF", "1")
		request.Header.Set("Origin", server.publicOrigin)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("If-Match", `"`+hash+`"`)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	if response := callPUT("sha256:" + strings.Repeat("c", 64)); response.Code != 412 || repo.attempted {
		t.Fatal("wrong hash admitted", response.Code)
	}
	runnerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-repo.dispatched
		request := httptest.NewRequest("POST", "/api/v1/runners/hosting/database-import-stage/upload-stream", strings.NewReader(`{"operation_id":"local_stage","run_id":"db_import_stage_local_stage","lease_id":"lease","attempt":1,"fence":"fence","plan_hash":"`+repo.plan.Hash+`"}`))
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Authorization", "Bearer "+runner)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		runnerDone <- response
	}()
	response := callPUT(stage.UploadHash)
	stream := <-runnerDone
	if response.Code != 202 || stream.Code != 200 || stream.Body.String() != sql {
		t.Fatal("registered stream failed", response.Code, stream.Code, response.Body.String(), stream.Body.String())
	}
	if response = callPUT(stage.UploadHash); response.Code != 409 {
		t.Fatal("duplicate body accepted", response.Code)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("upload result was cacheable")
	}
}

type uploadReadProbe struct {
	io.Reader
	read bool
}

func (p *uploadReadProbe) Read(b []byte) (int, error) { p.read = true; return p.Reader.Read(b) }

func TestLocalImportStalledSocketRevocationReleasesCapacity(t *testing.T) {
	var broker databaseImportUploadBroker
	active := make(chan struct{})
	revoke := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		session, ok := broker.reserve("actor", domain.HostingDatabaseImportStagePlan{Stage: domain.HostingDatabaseImportStage{ID: "stalled"}})
		if !ok {
			t.Error("capacity unavailable")
			return
		}
		defer broker.remove("stalled")
		deadline := time.Now().Add(10 * time.Second)
		if err := uploadDeadlines(w, deadline); err != nil {
			t.Error(err)
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		fail := func(err error) {
			cancel()
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			_ = session.writer.CloseWithError(err)
			_ = session.reader.CloseWithError(err)
			_ = r.Body.Close()
		}
		stop := watchImportUpload(ctx, fail, func() error {
			select {
			case <-revoke:
				return store.ErrConflict
			default:
				return nil
			}
		})
		defer stop()
		close(active)
		_ = copyImportUpload(session.writer, r.Body, 100)
	}))
	defer server.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	_, err = fmt.Fprintf(connection, "PUT / HTTP/1.1\r\nHost: test\r\nContent-Type: application/octet-stream\r\nContent-Length: 100\r\n\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	<-active
	start := time.Now()
	close(revoke)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("stalled real socket prevented watcher/handler join")
	}
	if time.Since(start) >= 3*time.Second {
		t.Fatal("revocation waited for original read deadline")
	}
	broker.mu.Lock()
	remaining := len(broker.sessions)
	broker.mu.Unlock()
	if remaining != 0 {
		t.Fatal("stalled upload leaked capacity")
	}
}
