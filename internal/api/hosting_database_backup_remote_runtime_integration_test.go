package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

const databaseRemoteSiloImage = "pgsty/silo@sha256:87585bb783b7822318b3e1bcc4497c8427c98e2b021826d20b5f888713299f95"

// Keep the fixture wire local so this opt-in test compiles before the separately
// reviewed remote implementation is integrated. It still calls only real APIs.
type databaseRemoteTransfer struct {
	ID                 string                            `json:"id"`
	Mode               string                            `json:"mode"`
	State              string                            `json:"state"`
	BackupID           string                            `json:"backup_id"`
	RestoreOperationID string                            `json:"restore_operation_id"`
	FailureCode        string                            `json:"failure_code"`
	TargetID           string                            `json:"target_id"`
	TargetRevision     int64                             `json:"target_revision"`
	Blob               *domain.HostingDatabaseBackupBlob `json:"blob"`
}

type databaseRemoteBackupWire struct {
	mu               sync.Mutex
	backup           string
	first            bool
	body, response   [32]byte
	replays          int
	entered, release chan struct{}
}

// Drop the first real committed upload receipt response, then require byte-exact
// replay after the runner restarts. Validation receipts never enter this fault.
func (w *databaseRemoteBackupWire) serve(out http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/database-backup-transfer/mutation" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		out.WriteHeader(400)
		return true
	}
	defer clear(raw)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var in struct {
		Phase string `json:"phase"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Phase != "complete" {
		handler.ServeHTTP(out, r)
		return true
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	var transfer databaseRemoteTransfer
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &transfer) != nil || transfer.Mode != "upload" || transfer.BackupID != w.backup {
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	body, response := sha256.Sum256(raw), sha256.Sum256(rec.Body.Bytes())
	w.mu.Lock()
	if !w.first {
		w.first = true
		w.body, w.response = body, response
		close(w.entered)
		w.mu.Unlock()
		select {
		case <-w.release:
		case <-r.Context().Done():
		}
		return true
	}
	if w.body != body || w.response != response {
		w.mu.Unlock()
		out.WriteHeader(409)
		return true
	}
	w.replays++
	w.mu.Unlock()
	databaseRemoteCopyResponse(out, rec)
	return true
}
func databaseRemoteCopyResponse(out http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		out.Header()[k] = v
	}
	out.WriteHeader(rec.Code)
	_, _ = out.Write(rec.Body.Bytes())
}

func databaseRemoteAwait(f *databaseRunnerFixture, backup, mode, state string) databaseRemoteTransfer {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			f.t.Fatalf("remote %s did not reach %s", mode, state)
		case <-ticker.C:
			values := databaseRunnerJSON[[]databaseRemoteTransfer](f, f.token, "GET", "/api/v1/database-backups/"+backup+"/transfers", nil, 200)
			for _, v := range values {
				if v.Mode == mode && v.State == state {
					return v
				}
			}
		}
	}
}

// All Silo administration runs in the exact owned container. Secrets are
// generated per test, enter through a private env file, and are never evidence.
type databaseRemoteSilo struct {
	f                          *databaseRunnerFixture
	id, volume, access, secret string
}

func databaseRemoteRandom(t *testing.T) string {
	t.Helper()
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		t.Fatal("remote fixture random failed")
	}
	return hex.EncodeToString(b[:])
}
func startDatabaseRemoteSilo(f *databaseRunnerFixture) *databaseRemoteSilo {
	f.t.Helper()
	s := &databaseRemoteSilo{f: f, volume: databaseRunnerOwner + "-silo-" + databaseRemoteRandom(f.t), access: databaseRemoteRandom(f.t)[:20], secret: databaseRemoteRandom(f.t)}
	// The gate pulls the exact digest, and this fixture rejects another platform.
	var image []struct{ Architecture, OS string }
	if json.Unmarshal(f.docker("image", "inspect", databaseRemoteSiloImage), &image) != nil || len(image) != 1 || image[0].Architecture != "arm64" || image[0].OS != "linux" {
		f.t.Fatal("pinned native Silo image required")
	}
	owner := databaseRemoteRandom(f.t)
	f.docker("volume", "create", "--label", "io.nerocd.remote-fixture.owner="+owner, s.volume)
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if s.id != "" {
			raw, e := databaseRunnerDocker(ctx, nil, "container", "inspect", "--format", "{{ index .Config.Labels \"io.nerocd.remote-fixture.owner\" }}", s.id)
			if e != nil || strings.TrimSpace(string(raw)) != owner {
				f.t.Error("Silo cleanup ownership unresolved")
				return
			}
			if _, e = databaseRunnerDocker(ctx, nil, "container", "rm", "--force", s.id); e != nil {
				f.t.Error("Silo cleanup failed")
				return
			}
		}
		raw, e := databaseRunnerDocker(ctx, nil, "volume", "inspect", "--format", "{{ index .Labels \"io.nerocd.remote-fixture.owner\" }}", s.volume)
		if e != nil || strings.TrimSpace(string(raw)) != owner {
			f.t.Error("Silo volume cleanup ownership unresolved")
			return
		}
		if _, e = databaseRunnerDocker(ctx, nil, "volume", "rm", s.volume); e != nil {
			f.t.Error("Silo volume cleanup failed")
		}
	})
	env := []byte("MINIO_ROOT_USER=" + databaseRemoteRandom(f.t)[:20] + "\nMINIO_ROOT_PASSWORD=" + databaseRemoteRandom(f.t) + "\nFIXTURE_ACCESS=" + s.access + "\nFIXTURE_SECRET=" + s.secret + "\nMINIO_BROWSER=off\n")
	path := filepath.Join(f.root, "silo.env")
	if os.WriteFile(path, env, 0600) != nil {
		f.t.Fatal("private Silo environment failed")
	}
	clear(env)
	s.id = strings.TrimSpace(string(f.docker("run", "--detach", "--name", s.volume, "--label", "io.nerocd.remote-fixture.owner="+owner, "--network", "host", "--cpus", "1", "--memory", "512m", "--pids-limit", "128", "--mount", "type=volume,src="+s.volume+",dst=/data", "--env-file", path, databaseRemoteSiloImage, "server", "/data", "--address", "127.0.0.1:9000")))
	if !backupRuntimeValidExecID(s.id) {
		f.t.Fatal("Silo container identity invalid")
	}
	s.command(`for n in $(seq 1 30); do
  if mc alias set fixture http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 && mc ready fixture >/dev/null 2>&1; then break; fi
  sleep 1
done
mc ready fixture >/dev/null
mc mb fixture/nerocd-runtime >/dev/null
mc admin user add fixture "$FIXTURE_ACCESS" "$FIXTURE_SECRET" >/dev/null
`)
	s.policy(false)
	f.protected = append(f.protected, []byte(s.access), []byte(s.secret))
	return s
}
func (s *databaseRemoteSilo) command(script string) []byte {
	s.f.t.Helper()
	raw, e := databaseRunnerDocker(s.f.t.Context(), []byte("set -eu\n"+script), "exec", "--interactive", s.id, "sh", "-s")
	if e != nil {
		s.f.t.Fatal("owned Silo administration failed")
	}
	return raw
}
func (s *databaseRemoteSilo) policy(put bool) {
	actions := []string{"s3:GetObject"}
	if put {
		actions = append(actions, "s3:PutObject")
	}
	policy := map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"s3:ListBucket"}, "Resource": []string{"arn:aws:s3:::nerocd-runtime"}}, map[string]any{"Effect": "Allow", "Action": actions, "Resource": []string{"arn:aws:s3:::nerocd-runtime/nerocd/postgresql/*"}}}}
	raw, _ := json.Marshal(policy)
	attach := ""
	if !put {
		attach = "mc admin policy attach fixture fixture-backup --user \"$FIXTURE_ACCESS\" >/dev/null\n"
	}
	s.command("cat > /tmp/fixture-policy.json <<'POLICY'\n" + string(raw) + "\nPOLICY\nmc admin policy create fixture fixture-backup /tmp/fixture-policy.json >/dev/null\n" + attach)
}
func (s *databaseRemoteSilo) verify(backup domain.HostingDatabaseBackupOperation) {
	s.f.t.Helper()
	if backup.Blob == nil {
		s.f.t.Fatal("remote blob absent")
	}
	key := "nerocd/postgresql/" + backup.ID + "/" + strings.TrimPrefix(backup.Blob.Digest, "sha256:") + ".dump"
	// Both identifiers derive from validated server documents, never shell input.
	if backup.Validate() != nil {
		s.f.t.Fatal("remote backup identity invalid")
	}
	raw := s.command("mc cat fixture/nerocd-runtime/" + key + " | sha256sum\n")
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != strings.TrimPrefix(backup.Blob.Digest, "sha256:") {
		s.f.t.Fatal("remote object SHA mismatch")
	}
}

func TestHostingDatabaseBackupRemoteCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact authorized remote backup runtime gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	silo := startDatabaseRemoteSilo(f)
	source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", backupRuntimeCreateInput(t, f.environment, f.destination), 201)
	f.registerRouteDatabaseCleanup(source.Resource.ID)
	deployed := f.deploy(source, "remote_source")
	f.registerRouteOperationCleanup(deployed.ID)
	f.await(deployed.ID)
	application := backupRuntimeApplication(f, source, "remote_source")
	f.appRecord(application, "PUT", "before", "remote_snapshot_origin")
	target := databaseRunnerJSON[struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}](f, f.token, "POST", "/api/v1/projects/"+source.Resource.ProjectID+"/database-backup-targets", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": 0, "name": "Runtime S3", "endpoint": "http://127.0.0.1:9000", "bucket": "nerocd-runtime", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": silo.access, "secret_key": silo.secret}}, 200)
	validation := databaseRunnerJSON[databaseRemoteTransfer](f, f.token, "POST", "/api/v1/projects/"+source.Resource.ProjectID+"/database-backup-targets/"+target.ID+"/validations", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": target.Revision, "destination_id": f.destination}, 202)
	databaseRemoteAwaitValidation(f, validation)
	browser := startDatabaseBackupBrowserProfile(f, source, "backup_remote")
	id := browser.await("backup")
	diagnosticID = id
	backup := backupRuntimeAwait(f, id)
	failed := databaseRemoteAwait(f, id, "upload", "failed")
	if failed.FailureCode != "access_denied" || failed.Blob == nil || *failed.Blob != *backup.Blob || failed.TargetID != target.ID || failed.TargetRevision != target.Revision {
		t.Fatal("Put denial did not preserve local archive and target identity")
	}
	f.appRecord(application, "PUT", "after_snapshot", "source_stays_writable")
	backupRuntimeRestartController(f)
	f.stop()
	f.start()
	survived := backupRuntimeAwait(f, id)
	if *survived.Blob != *backup.Blob {
		t.Fatal("restart changed local dump")
	}
	browser.setPhase("denied", id)
	// Browser acknowledges that the warning survived a complete page reload.
	// Only then grant PutObject, preserving credentials and target revision.
	databaseRemoteAwaitBrowserPhase(f, browser, "warning_seen")
	dumpPlans := databaseRemoteDumpPlanCount(&f.apiCounts)
	silo.policy(true)
	wire := &databaseRemoteBackupWire{backup: id, entered: make(chan struct{}), release: make(chan struct{})}
	f.remoteBackupWire.Store(wire)
	browser.setPhase("retry", id)
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("upload committed receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	ctxReplay, cancelReplay := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelReplay()
	for {
		wire.mu.Lock()
		count := wire.replays
		wire.mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-ctxReplay.Done():
			t.Fatal("remote committed receipt did not replay")
		case <-time.After(100 * time.Millisecond):
		}
	}
	f.remoteBackupWire.Store(nil)
	uploaded := databaseRemoteAwait(f, id, "upload", "succeeded")
	if uploaded.ID == failed.ID || uploaded.Blob == nil || *uploaded.Blob != *backup.Blob || uploaded.TargetRevision != failed.TargetRevision {
		t.Fatal("retry changed archive or target")
	}
	if databaseRemoteDumpPlanCount(&f.apiCounts) != dumpPlans {
		t.Fatal("upload retry requested another dump plan")
	}
	silo.verify(backup)
	// Withhold only the exact fixture-owned CAS file. Metadata and staging remain,
	// so successful remote restore must perform a real GET and re-publish the CAS.
	cas := filepath.Join(f.root, "state", "logical-backups", strings.TrimPrefix(backup.Blob.Digest, "sha256:"))
	raw, e := os.ReadFile(cas)
	if e != nil || int64(len(raw)) != backup.Blob.Bytes {
		t.Fatal("local CAS absent before remote restore")
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != backup.Blob.Digest {
		t.Fatal("local CAS digest mismatch")
	}
	if os.Rename(cas, cas+".fixture-withheld") != nil {
		t.Fatal("fixture CAS withholding failed")
	}
	browser.setPhase("restore", id)
	restoreID := browser.await("restore")
	diagnosticID = restoreID
	restored := backupRuntimeAwait(f, restoreID)
	download := databaseRemoteAwait(f, id, "download", "succeeded")
	if download.RestoreOperationID != restoreID || download.Blob == nil || *download.Blob != *backup.Blob || restored.TargetResourceID == source.Resource.ID {
		t.Fatal("remote restore identity mismatch")
	}
	downloaded, e := os.ReadFile(cas)
	if e != nil {
		t.Fatal("remote GET did not republish CAS")
	}
	got := sha256.Sum256(downloaded)
	clear(downloaded)
	if got != digest {
		t.Fatal("remote GET SHA mismatch")
	}
	fresh := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	f.registerRouteDatabaseCleanup(fresh.Resource.ID)
	if fresh.Binding.ID == source.Binding.ID || fresh.StorageRef == source.StorageRef || fresh.Credentials[0].SecretID == source.Credentials[0].SecretID {
		t.Fatal("remote restore reused binding volume or credential")
	}
	distinct := backupRuntimeApplicationObserved(f, fresh, "remote_restored", browser.recordProgress)
	if distinct.ResourceID == application.ResourceID {
		t.Fatal("remote restore reused application")
	}
	f.appRecord(distinct, "GET", "before", "remote_snapshot_origin")
	f.appRecord(distinct, "PUT", "new", "remote_new_write")
	f.appRecord(distinct, "GET", "new", "remote_new_write")
	f.appRecord(application, "GET", "after_snapshot", "source_stays_writable")
	// A retry and recovery may transfer again, but must never dump again.
	history := databaseRunnerJSON[[]domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/resources/"+source.Resource.ID+"/database-backups", nil, 200)
	dumps := 0
	for _, op := range history {
		if op.Mode == "backup" {
			dumps++
			if op.ID != id || op.Blob == nil || *op.Blob != *backup.Blob {
				t.Fatal("retry redumped source")
			}
		}
	}
	if dumps != 1 {
		t.Fatal("retry admitted another dump")
	}
	browser.setPhase("complete", restoreID)
	browser.finished()
	f.assertRedacted()
	fmt.Println("DATABASE_BACKUP_REMOTE_PASS postgresql")
	fmt.Println("DATABASE_BACKUP_REMOTE_REPLAY_PASS postgresql")
	fmt.Println("DATABASE_BACKUP_REMOTE_GET_RESTORE_APP_PASS postgresql")
}

func databaseRemoteAwaitBrowserPhase(f *databaseRunnerFixture, b *databaseBackupBrowserFixture, want string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	for {
		b.mu.Lock()
		phase := b.phase
		b.mu.Unlock()
		if phase == want {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("remote browser warning acknowledgement missing")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestDatabaseRemoteBackupWireDropsOnlyCommittedUploadAndRequiresExactReplay(t *testing.T) {
	for _, tc := range []struct {
		name, request, mode, backup string
		status                      int
		fault                       bool
	}{
		{"upload", `{"phase":"complete","receipt_id":"same"}`, "upload", "backup", 200, true},
		{"validation", `{"phase":"complete"}`, "validate", "", 200, false},
		{"download", `{"phase":"complete"}`, "download", "backup", 200, false},
		{"other_backup", `{"phase":"complete"}`, "upload", "other", 200, false},
		{"intent", `{"phase":"intent"}`, "upload", "backup", 200, false},
		{"denied", `{"phase":"complete"}`, "upload", "backup", 403, false},
		{"invalid", `{`, "upload", "backup", 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &databaseRemoteBackupWire{backup: "backup", entered: make(chan struct{}), release: make(chan struct{})}
			close(wire.release)
			calls := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, e := io.ReadAll(r.Body)
				if e != nil || string(raw) != tc.request {
					t.Error("forwarded receipt body changed")
				}
				w.Header().Set("X-Receipt", "real")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(databaseRemoteTransfer{ID: "transfer", Mode: tc.mode, BackupID: tc.backup, State: "succeeded"})
			})
			send := func(body string) *httptest.ResponseRecorder {
				out := httptest.NewRecorder()
				if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-transfer/mutation", strings.NewReader(body)), handler) {
					t.Fatal("receipt not forwarded")
				}
				return out
			}
			first := send(tc.request)
			if !tc.fault {
				if first.Code != tc.status || first.Header().Get("X-Receipt") != "real" || wire.first {
					t.Fatal("unrelated response faulted")
				}
				return
			}
			select {
			case <-wire.entered:
			default:
				t.Fatal("committed upload fault absent")
			}
			if first.Body.Len() != 0 || first.Header().Get("X-Receipt") != "" {
				t.Fatal("lost response escaped")
			}
			replay := send(tc.request)
			if replay.Code != 200 || replay.Header().Get("X-Receipt") != "real" || wire.replays != 1 || calls != 2 {
				t.Fatal("real receipt replay not preserved")
			}
			// A different payload is forwarded to the real server, but never accepted as
			// evidence of the original receipt replay, even if its response is identical.
			changed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(databaseRemoteTransfer{ID: "transfer", Mode: "upload", BackupID: "backup", State: "succeeded"})
			})
			out := httptest.NewRecorder()
			wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-transfer/mutation", strings.NewReader(`{"phase":"complete","receipt_id":"different"}`)), changed)
			if out.Code != 409 || wire.replays != 1 {
				t.Fatal("changed receipt accepted as exact replay")
			}
		})
	}
}

func databaseRemoteDumpPlanCount(counts *databaseRunnerAPICounts) uint32 {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	var total uint32
	for i, route := range databaseRunnerDiagnosticRoutes {
		if route.method == "POST" && route.path == "/api/v1/runners/hosting/database-backup/plan" {
			for _, n := range counts.counts[i] {
				total += n
			}
		}
	}
	return total
}

// Reuse the runner-aware waiter so an exited runner cancels a poll and retains
// the last successfully observed transfer. Unknown values never become output.
func waitDatabaseRemoteValidation(ctx context.Context, process *databaseRunnerProcess, last databaseRemoteTransfer, fetch func(context.Context) (databaseRemoteTransfer, error)) (databaseRemoteTransfer, string) {
	_, reason := waitDatabaseRunnerOperation(ctx, process, func(ctx context.Context) (domain.HostingOperation, error) {
		next, err := fetch(ctx)
		if err != nil {
			return domain.HostingOperation{}, err
		}
		last = next
		state := next.State
		if state == "uncertain" {
			state = "reconciliation_required"
		}
		return domain.HostingOperation{State: state}, nil
	})
	return last, reason
}

func databaseRemoteAwaitValidation(f *databaseRunnerFixture, initial databaseRemoteTransfer) (last databaseRemoteTransfer) {
	f.t.Helper()
	last = initial
	reason := "unknown"
	defer func() {
		if f.t.Failed() {
			f.reportDatabaseRemoteValidationFailure(last, reason)
		}
	}()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	f.processMu.Lock()
	process := f.process
	f.processMu.Unlock()
	last, reason = waitDatabaseRemoteValidation(ctx, process, initial, func(ctx context.Context) (databaseRemoteTransfer, error) {
		var value databaseRemoteTransfer
		invalid := errors.New("remote validation poll unavailable")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+"/api/v1/database-backup-transfers/"+initial.ID, nil)
		if err != nil {
			return value, invalid
		}
		req.Header.Set("Authorization", "Bearer "+f.token)
		response, err := f.client.Do(req)
		if err != nil {
			return value, invalid
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
		defer clear(raw)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || len(raw) > 16384 || response.StatusCode != http.StatusOK || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || json.Unmarshal(raw, &value) != nil || value.ID != initial.ID || value.Mode != "validate" {
			return databaseRemoteTransfer{}, invalid
		}
		return value, nil
	})
	if reason != "" {
		f.t.Fatal("real ListObjects validation did not succeed")
	}
	return last
}

type databaseRemoteValidationEvidence struct {
	LastState, StoredState, FailureCode, Reason, RunStatus, LeaseStatus string
	RunLinked, LeaseLinked, IntentReceipt, TerminalReceipt              string
	TerminalClass, Requests                                             string
	RunnerExited                                                        bool
	RunnerCode                                                          int
}

func databaseRemoteValidationProjection(last, stored, failure, reason, run, lease string, observed, linked, leased, intent, terminal bool, raw []byte, exit databaseRunnerExit, counts *databaseRunnerAPICounts) databaseRemoteValidationEvidence {
	out := databaseRemoteValidationEvidence{LastState: "unknown", StoredState: "unknown", FailureCode: "unknown", Reason: "unknown", RunStatus: "unknown", LeaseStatus: "unknown", RunLinked: "unknown", LeaseLinked: "unknown", IntentReceipt: "unknown", TerminalReceipt: "unknown", TerminalClass: databaseBackupTerminalClass(raw), Requests: counts.summary(), RunnerExited: exit.Exited, RunnerCode: -1}
	state := func(v string) string {
		switch v {
		case "queued", "dispatched", "applying", "succeeded", "failed", "uncertain":
			return v
		}
		return "unknown"
	}
	out.LastState = state(last)
	switch reason {
	case "runner_exited", "deadline", "canceled", "operation_poll", "terminal_operation":
		out.Reason = reason
	}
	if exit.Exited && exit.Code >= 0 && exit.Code <= 255 {
		out.RunnerCode = exit.Code
	}
	if !observed {
		return out
	}
	out.StoredState = state(stored)
	switch failure {
	case "", "access_denied", "network_denied", "remote_unavailable", "object_mismatch", "archive_unavailable", "interrupted", "authority_denied":
		out.FailureCode = failure
	}
	switch run {
	case "none", "queued", "running", "succeeded", "failed", "canceled", "waiting_approval":
		out.RunStatus = run
	}
	switch lease {
	case "none", "active", "succeeded", "failed", "canceled", "expired":
		out.LeaseStatus = lease
	}
	out.RunLinked, out.LeaseLinked = fmt.Sprint(linked), fmt.Sprint(leased)
	out.IntentReceipt, out.TerminalReceipt = "absent", "absent"
	if intent {
		out.IntentReceipt = "present"
	}
	if terminal {
		out.TerminalReceipt = "present"
	}
	return out
}

// Read only the exact validation's linked task and latest matching runner lease.
// Receipt presence also matches that lease's complete authority, never another
// operation or an earlier lease. Raw rows and identifiers stay private.
const databaseRemoteValidationQuery = `SELECT b.state,b.failure_code,COALESCE(t.status,'none'),
 COALESCE((CASE WHEN l.status='active' AND l.expires_at<=clock_timestamp() THEN 'expired' ELSE l.status END),'none'),t.id IS NOT NULL,l.id IS NOT NULL,
 EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts r WHERE r.operation_id=b.id AND r.phase='intent' AND r.authority=jsonb_build_object('operation_id',b.id,'run_id',t.id,'lease_id',l.id,'attempt',l.attempt,'fence',l.fence)),
 EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts r WHERE r.operation_id=b.id AND r.phase IN ('complete','failed','uncertain') AND r.authority=jsonb_build_object('operation_id',b.id,'run_id',t.id,'lease_id',l.id,'attempt',l.attempt,'fence',l.fence))
 FROM hosting_database_backup_transfers b
 LEFT JOIN task_runs t ON t.id=b.run_id AND t.id='db_transfer_'||b.id AND t.run_spec->>'type'='database_backup_transfer' AND t.run_spec->>'hosting_operation_id'=b.id AND t.run_spec->>'hosting_mode'=b.mode
 LEFT JOIN LATERAL (SELECT x.* FROM run_leases x WHERE x.run_id=t.id AND x.runner_id=b.runner_id ORDER BY x.attempt DESC LIMIT 1) l ON true
 WHERE b.id=$1 AND b.mode='validate'`

func (f *databaseRunnerFixture) reportDatabaseRemoteValidationFailure(last databaseRemoteTransfer, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var state, failure, run, lease string
	var linked, leased, intent, terminal bool
	observed := false
	if f.database != nil && last.ID != "" {
		observed = f.database.QueryRow(ctx, databaseRemoteValidationQuery, last.ID).Scan(&state, &failure, &run, &lease, &linked, &leased, &intent, &terminal) == nil
	}
	raw := f.logs.snapshot()
	defer clear(raw)
	evidence := databaseRemoteValidationProjection(last.State, state, failure, reason, run, lease, observed, linked, leased, intent, terminal, raw, f.failureRunnerStatus(), &f.apiCounts)
	output := f.failureOutput
	if output == nil {
		output = os.Stdout
	}
	encoded, err := json.Marshal(evidence)
	if err == nil {
		_, _ = fmt.Fprintf(output, "DATABASE_BACKUP_REMOTE_FAILURE_EVIDENCE %s\n", encoded)
	}
}
