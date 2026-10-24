package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// This hook observes the compiled process's real HTTP requests. It drops exactly
// one response only after the real origin handler returns its committed 200.
type databaseCaptureRuntimeWire struct {
	mu                     sync.Mutex
	journalPath            string
	claim, plan, forbidden int
	bodies, acks           [][]byte
	lost, invalid          bool
}

func (c *databaseCaptureRuntimeWire) serve(w http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/runners/identity":
		return false
	case "/api/v1/runners/database-network-captures/claim":
		c.claim++
		return false
	case "/api/v1/runners/database-network-captures/plan":
		c.plan++
		return false
	case "/api/v1/runners/database-network-captures/origin":
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/runners/") {
			c.forbidden++
		}
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingDatabaseBindingJSONLimit+1))
	if err != nil || len(raw) > domain.HostingDatabaseBindingJSONLimit {
		c.invalid = true
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	c.bodies = append(c.bodies, bytes.Clone(raw))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if !c.lost && c.journalPath != "" {
		var disk struct {
			Version int                                    `json:"version"`
			Capture []runner.JournalDatabaseNetworkCapture `json:"database_network_capture"`
		}
		var in domain.HostingDatabaseNetworkOriginRequest
		data, readErr := os.ReadFile(c.journalPath)
		if readErr != nil || len(data) > 8<<20 || json.Unmarshal(data, &disk) != nil || disk.Version != 13 || len(disk.Capture) != 3 || disk.Capture[0].Phase != "claim" || disk.Capture[1].Phase != "plan" || disk.Capture[2].Phase != "origin" || json.Unmarshal(raw, &in) != nil || !reflect.DeepEqual(disk.Capture[2].Origin, &in) {
			c.invalid = true
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		c.invalid = true
	} else {
		c.acks = append(c.acks, bytes.Clone(rec.Body.Bytes()))
	}
	if !c.lost {
		c.lost = true
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"unavailable","detail":"capture_ack_loss_canary"}}`)
		return true
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
	return true
}

func (c *databaseCaptureRuntimeWire) counts() (int, int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claim, c.plan, len(c.bodies), c.forbidden
}

func (c *databaseCaptureRuntimeWire) exactReplay() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.invalid && c.lost && len(c.bodies) == 2 && bytes.Equal(c.bodies[0], c.bodies[1]) && len(c.acks) == 2 && bytes.Equal(c.acks[0], c.acks[1])
}

// Hash protected files without retaining their plaintext in fixture snapshots.
type databaseCaptureTreeEntry struct {
	Mode fs.FileMode
	Hash [sha256.Size]byte
}

func databaseCaptureTree(root string) (map[string]databaseCaptureTreeEntry, error) {
	entries := map[string]databaseCaptureTreeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("protected tree unavailable")
		}
		info, err := d.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) || info.Size() > 1<<20 {
			return errors.New("protected tree entry invalid")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("protected tree path invalid")
		}
		entry := databaseCaptureTreeEntry{Mode: info.Mode()}
		if !info.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return errors.New("protected tree read failed")
			}
			entry.Hash = sha256.Sum256(raw)
			clear(raw)
		}
		entries[relative] = entry
		return nil
	})
	return entries, err
}

func (f *databaseRunnerFixture) captureTrees() map[string]map[string]databaseCaptureTreeEntry {
	f.t.Helper()
	trees := map[string]map[string]databaseCaptureTreeEntry{}
	for _, name := range []string{"credential", "state", "secrets"} {
		entries, err := databaseCaptureTree(filepath.Join(f.root, name))
		if err != nil {
			f.t.Fatal("capture protected tree snapshot failed")
		}
		trees[name] = entries
	}
	return trees
}

func databaseCaptureArguments(f *databaseRunnerFixture, id, endpoint string) []string {
	return []string{"runner", "--server", f.server.URL, "--credential-file", filepath.Join(f.root, "credential"), "--database-network-capture-id", id, "--journal-dir", filepath.Join(f.root, "journal"), "--database-state-root", filepath.Join(f.root, "state"), "--database-secret-root", filepath.Join(f.root, "secrets"), "--image-docker-endpoint", endpoint}
}

func (f *databaseRunnerFixture) awaitCaptureExit(success bool) {
	f.t.Helper()
	p := f.process
	if p == nil {
		f.t.Fatal("capture process missing")
	}
	select {
	case <-p.done:
	case <-f.t.Context().Done():
		f.t.Fatal("capture process canceled before completion")
	case <-time.After(45 * time.Second):
		f.t.Fatal("capture process did not exit")
	}
	exit := p.status()
	f.stop()
	if !exit.Exited || success && (exit.Kind != "success" || exit.Code != 0) || !success && (exit.Kind != "process_exit" || exit.Code <= 0) {
		f.t.Fatalf("capture process exit mismatch success=%t exited=%t kind=%s code=%d", success, exit.Exited, exit.Kind, exit.Code)
	}
}

func (f *databaseRunnerFixture) captureChain() []runner.JournalDatabaseNetworkCapture {
	f.t.Helper()
	if f.process != nil {
		f.t.Fatal("capture journal requires joined runner")
	}
	j, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		f.t.Fatal("capture journal unavailable")
	}
	snapshot := j.Snapshot()
	if j.Close() != nil {
		f.t.Fatal("capture journal did not close")
	}
	return snapshot.DatabaseNetworkCapture
}

func (f *databaseRunnerFixture) awaitSourceJournalRetired() {
	f.t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		// The journal replaces this file atomically. Its exclusive writer stays
		// active until the source ACK has retired; the fixture only reads bytes.
		raw, err := os.ReadFile(filepath.Join(f.root, "journal", "journal.json"))
		var pending runner.JournalSnapshot
		if err == nil && len(raw) <= 8<<20 && json.Unmarshal(raw, &pending) == nil && len(pending.Hosting) == 0 && len(pending.Events) == 0 && len(pending.Completions) == 0 && len(pending.Provenance) == 0 {
			return
		}
		if f.process == nil || f.process.status().Exited {
			f.t.Fatal("source runner exited before journal retirement")
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("source journal retirement canceled")
		case <-deadline.C:
			f.t.Fatal("source journal did not retire before capture")
		case <-ticker.C:
		}
	}
}

type databaseCaptureStored struct {
	Request   domain.HostingDatabaseNetworkOriginRequest
	Authority domain.HostingAuthority
	Receipt   domain.HostingDatabaseNetworkOriginReceipt
	Hash      string
}

func (f *databaseRunnerFixture) captureStored(id string) databaseCaptureStored {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var body, authority, receipt []byte
	var stored databaseCaptureStored
	var count int
	if f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_database_network_origin_receipts WHERE capture_id=$1`, id).Scan(&count) != nil || count != 1 {
		f.t.Fatal("capture did not commit exactly one origin receipt")
	}
	if f.database.QueryRow(ctx, `SELECT body,authority,receipt,body_hash FROM hosting_database_network_origin_receipts WHERE capture_id=$1`, id).Scan(&body, &authority, &receipt, &stored.Hash) != nil || json.Unmarshal(body, &stored.Request) != nil || json.Unmarshal(authority, &stored.Authority) != nil || json.Unmarshal(receipt, &stored.Receipt) != nil {
		f.t.Fatal("capture receipt documents invalid")
	}
	var task, lease, key string
	var completed bool
	var leases int
	if f.database.QueryRow(ctx, `SELECT t.status,l.status,l.completion_key,l.completed_at IS NOT NULL,(SELECT count(*) FROM run_leases WHERE run_id=t.id) FROM task_runs t JOIN run_leases l ON l.run_id=t.id WHERE t.id=$1 AND l.id=$2`, stored.Authority.RunID, stored.Authority.LeaseID).Scan(&task, &lease, &key, &completed, &leases) != nil || task != "succeeded" || lease != "succeeded" || !completed || key != stored.Request.ReceiptID || leases != 1 {
		f.t.Fatal("capture original task/lease completion invalid")
	}
	return stored
}

// Verify that controller provenance came from this actual database deployment,
// retained volume creation and initialized on-disk record, not a test attestation.
func (f *databaseRunnerFixture) capturePhysical(source domain.HostingOperation, p domain.HostingDatabaseNetworkCapturePlan) domain.HostingDatabaseNetworkOrigin {
	f.t.Helper()
	container := f.inspect(source)
	if p.Validate() != nil || p.SourceOperationID != source.ID || p.SourceHealthy.ContainerID != container.ID || p.SourcePlan.Hash != source.Plan.Hash || !reflect.DeepEqual(p.SourcePlan, *source.Plan) || p.SourceHealthy.Release != *source.Release {
		f.t.Fatal("capture source provenance differs from actual deployment")
	}
	var volume struct {
		Name, Driver, Scope, CreatedAt string
		Labels, Options                map[string]string
	}
	if json.Unmarshal([]byte(f.volumeProof(source)), &volume) != nil || volume.Name != p.Creation.VolumeName || volume.Driver != p.Creation.Driver || volume.Scope != p.Creation.Scope || !maps.Equal(volume.Labels, p.Creation.Labels) || !maps.Equal(volume.Options, p.Creation.Options) {
		f.t.Fatal("capture volume creation identity differs")
	}
	var creation domain.HostingStorageMaterializedRequest
	var body []byte
	var hash string
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	if f.database.QueryRow(ctx, `SELECT body,body_hash FROM hosting_storage_materialized_receipts WHERE operation_id=$1 AND receipt_id=$2`, p.Materialization.CreationOperationID, p.Creation.Receipt.ID).Scan(&body, &hash) != nil || json.Unmarshal(body, &creation) != nil || domain.HostingDocumentHash(creation) != hash || hash != p.Creation.Receipt.BodyHash || creation.VolumeName != volume.Name || creation.Driver != volume.Driver || creation.Scope != volume.Scope || !maps.Equal(creation.Labels, volume.Labels) || !maps.Equal(creation.Options, volume.Options) || creation.DaemonID != p.Materialization.DaemonID || creation.CreationOperationID != p.Materialization.CreationOperationID || creation.CreationPlanHash != p.Materialization.CreationPlanHash || creation.CreationNonce != p.Materialization.CreationNonce {
		f.t.Fatal("capture materialization did not derive from committed physical receipt")
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "state", source.Plan.Database.NetworkName+".json"))
	var origin domain.HostingDatabaseNetworkOrigin
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &origin) != nil || p.ValidateOrigin(origin) != nil || origin.VolumeCreatedAt != volume.CreatedAt || origin.NetworkID != container.NetworkSettings.Networks[source.Plan.Database.NetworkName].NetworkID {
		f.t.Fatal("capture retained origin differs from physical identities")
	}
	daemon := strings.TrimSpace(string(f.docker("info", "--format", "{{json .ID}}")))
	var daemonID string
	if json.Unmarshal([]byte(daemon), &daemonID) != nil || daemonID != origin.Daemon || source.Release.DaemonID != origin.Daemon {
		f.t.Fatal("capture daemon identity differs")
	}
	for _, filter := range []string{"volume=" + volume.Name, "network=" + origin.NetworkID} {
		users := strings.Fields(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", filter)))
		if len(users) != 1 || users[0] != container.ID {
			f.t.Fatal("capture physical identity has extra or missing all-state users")
		}
	}
	return origin
}

func TestHostingDatabaseCaptureCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("run reviewed acceptance/runtime-hosting-database-runner/gate.sh after exact physical authorization")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	f := newDatabaseRunnerFixtureWithCapture(t, true)
	for index, engine := range []string{"postgresql", "redis"} {
		version := domain.HostingDatabasePostgresRecipe
		if engine == "redis" {
			version = domain.HostingDatabaseRedisRecipe
		}
		if index > 0 {
			f.start()
		}
		view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(string(rune('c'+index)), 32), ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "capture physical " + engine, DestinationID: f.destination, Engine: engine, RecipeVersion: version}, 201)
		source := f.await(f.deploy(view, "capture_source").ID)
		marker := "capture_physical_" + engine + "_before"
		f.marker(view, source, marker, true)
		f.awaitSourceJournalRetired()
		f.stop()
		before, err := f.operation(t.Context(), source.ID)
		if err != nil || before.State != "succeeded" || before.Result == nil || before.CompleteReceipt == nil {
			t.Fatal("capture source not settled")
		}
		capture, err := f.service.RequestHostingDatabaseNetworkCapture(f.owner, source.ID)
		if err != nil {
			t.Fatal("authenticated owner capture request failed")
		}
		physical := f.capturePhysical(source, capture.Plan)
		trees := f.captureTrees()
		inventory := f.replayInventory([]domain.HostingOperation{source}, source)
		completeCount := f.completionCount(source.ID)
		genericClaims, applies, deliveries := f.claimRequests.Load(), f.applyRequests.Load(), f.deliveryRequests.Load()
		wire := &databaseCaptureRuntimeWire{journalPath: filepath.Join(f.root, "journal", "journal.json")}
		if !f.capture.CompareAndSwap(nil, wire) {
			t.Fatal("capture wire hook already active")
		}
		args := databaseCaptureArguments(f, capture.ID, "unix:///var/run/docker.sock")
		f.startRunner(args)
		f.awaitCaptureExit(false)
		chain := f.captureChain()
		if len(chain) != 3 || chain[0].Phase != "claim" || chain[1].Phase != "plan" || chain[2].Phase != "origin" || chain[2].Origin == nil || chain[0].Claim == nil {
			t.Fatal("compiled ACK loss did not retain original three-phase journal")
		}
		original := *chain[2].Origin
		stored := f.captureStored(capture.ID)
		if !reflect.DeepEqual(stored.Request, original) || stored.Authority != original.HostingAuthority || stored.Hash != domain.HostingDocumentHash(original) || stored.Receipt.BodyHash != stored.Hash || stored.Receipt.ID != original.ReceiptID || stored.Receipt.Origin != physical || stored.Receipt.OriginHash != physical.DocumentHash() || !stored.Receipt.ObservedAt.Equal(original.ObservedAt) || stored.Receipt.PlanHash != capture.Plan.Hash {
			t.Fatal("committed capture differs from real origin and exact durable request")
		}
		if !reflect.DeepEqual(trees, f.captureTrees()) {
			t.Fatal("initial capture observation changed protected trees")
		}
		// Ordinary mode must not poll around unresolved capture state.
		claimCount, planCount, originCount, forbidden := wire.counts()
		if claimCount != 1 || planCount == 0 || originCount != 1 || forbidden != 0 {
			t.Fatal("dedicated capture used unexpected API authority")
		}
		f.start()
		f.awaitCaptureExit(false)
		if !reflect.DeepEqual(chain, f.captureChain()) {
			t.Fatal("ordinary mode discarded unresolved capture state")
		}
		if c, p, o, b := wire.counts(); c != claimCount || p != planCount || o != originCount || b != 0 {
			t.Fatal("ordinary mode bypassed unresolved capture hold")
		}
		// Keep all original arguments except the socket. An absent fixture-owned
		// socket proves persisted-origin replay does not construct the host observer.
		absent := filepath.Join(f.root, "absent-capture.sock")
		if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("replay absence proof socket already exists")
		}
		replayArgs := databaseCaptureArguments(f, capture.ID, "unix://"+absent)
		f.startRunner(replayArgs)
		f.awaitCaptureExit(true)
		if len(f.captureChain()) != 0 || !wire.exactReplay() {
			t.Fatal("compiled historical replay changed wire body/ACK or failed durable retirement")
		}
		if c, p, o, b := wire.counts(); c != claimCount || p != planCount || o != originCount+1 || b != 0 {
			t.Fatal("historical replay reclaimed, refreshed plan or used generic runner APIs")
		}
		if !reflect.DeepEqual(stored, f.captureStored(capture.ID)) || !reflect.DeepEqual(trees, f.captureTrees()) {
			t.Fatal("capture replay changed immutable receipt or protected trees")
		}
		afterInventory := f.replayInventory([]domain.HostingOperation{source}, source)
		if databaseRunnerReplayInventoryMatches(inventory, afterInventory, []domain.HostingOperation{source}, source) != nil || f.completionCount(source.ID) != completeCount || f.claimRequests.Load() != genericClaims || f.applyRequests.Load() != applies || f.deliveryRequests.Load() != deliveries {
			t.Fatal("capture changed workload residue or repeated database execution/delivery")
		}
		f.capturePhysical(source, capture.Plan)
		after, err := f.operation(t.Context(), source.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("capture changed source operation/result")
		}
		if !f.capture.CompareAndSwap(wire, nil) {
			t.Fatal("capture wire hook identity changed")
		}
		credential, err := os.ReadFile(filepath.Join(f.root, "credential"))
		if err != nil {
			t.Fatal("historical plan probe credential unavailable")
		}
		client := runnerclient.New(f.server.URL, strings.TrimSpace(string(credential)))
		clear(credential)
		client.HTTP = f.client
		if _, err := client.HostingDatabaseNetworkCapturePlan(t.Context(), capture.Plan.RunnerID, original.HostingAuthority); err == nil {
			t.Fatal("historical capture completion regained current plan authority")
		}
		f.marker(view, source, marker, false)
		freshMarker := "capture_physical_" + engine + "_after"
		f.marker(view, source, freshMarker, true)
		f.marker(view, source, freshMarker, false)
		if !reflect.DeepEqual(trees, f.captureTrees()) {
			t.Fatal("post-capture application access changed protected roots")
		}
		f.assertRedacted()
		output := f.logs.snapshot()
		for _, private := range []string{original.Fence, original.LeaseID, "capture_ack_loss_canary"} {
			if bytes.Contains(output, []byte(private)) {
				t.Fatal("capture output exposed private authority or response diagnostics")
			}
		}
		clear(output)
		t.Logf("physical capture engine=%s source=%s capture=%s original_receipt=%s container=%s volume=%s network=%s lazy_replay_absent_socket=true", engine, source.ID, capture.ID, stored.Receipt.ID, source.Result.ContainerID, source.Plan.Storage.VolumeName, physical.NetworkID)
	}
}

func TestDatabaseCaptureRuntimeArguments(t *testing.T) {
	f := &databaseRunnerFixture{root: "/fixture/owned", server: &httptest.Server{URL: "https://fixture.invalid"}}
	args := databaseCaptureArguments(f, "hnc_"+strings.Repeat("a", 32), "unix:///fixture/owned/absent.sock")
	for _, flag := range []string{"--hosting-database", "--register", "--enrollment-token", "--capabilities", "--execute"} {
		if slices.Contains(args, flag) {
			t.Fatal("capture arguments included generic or enrollment mode")
		}
	}
	for flag, value := range map[string]string{"--server": f.server.URL, "--credential-file": "/fixture/owned/credential", "--journal-dir": "/fixture/owned/journal", "--database-state-root": "/fixture/owned/state", "--database-secret-root": "/fixture/owned/secrets", "--image-docker-endpoint": "unix:///fixture/owned/absent.sock"} {
		index := slices.Index(args, flag)
		if index < 0 || index+1 >= len(args) || args[index+1] != value {
			t.Fatal("capture arguments lost frozen protected path")
		}
	}
}

func TestDatabaseCaptureRuntimeTreeDetectsChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "protected")
	if os.WriteFile(path, []byte("protected_fixture_canary"), 0600) != nil {
		t.Fatal("tree fixture creation failed")
	}
	before, err := databaseCaptureTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(path, []byte("changed_fixture_canary"), 0600) != nil {
		t.Fatal("tree fixture mutation failed")
	}
	after, err := databaseCaptureTree(root)
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatal("protected tree failed to detect content change")
	}
	if os.Remove(path) != nil || os.Symlink("absent", path) != nil {
		t.Fatal("tree symlink fixture failed")
	}
	if _, err := databaseCaptureTree(root); err == nil {
		t.Fatal("protected tree followed unexpected symlink")
	}
}

func TestDatabaseCaptureRuntimeWireDropsOnlyFirstCommittedACK(t *testing.T) {
	wire := &databaseCaptureRuntimeWire{}
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, `{"id":"same_historical_ack"}`)
	})
	for _, want := range []int{503, 200} {
		rec := httptest.NewRecorder()
		if !wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/origin", strings.NewReader(`{"exact":"original"}`)), handler) || rec.Code != want {
			t.Fatal("wire did not model exact committed ACK loss/replay")
		}
	}
	if calls != 2 || !wire.exactReplay() {
		t.Fatal("wire changed repeated request or committed ACK")
	}
}
