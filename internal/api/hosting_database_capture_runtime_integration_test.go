package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
// one origin response after committed 200, plus one automatic claim response.
// The automatic replay barrier holds a real empty poll only after origin replay.
type databaseCaptureRuntimeWire struct {
	mu                               sync.Mutex
	journalPath                      string
	expectedCapture, expectedRunner  string
	claimResponse                    databaseCaptureClaimDiagnostic
	identity, claim, plan, forbidden int
	suppressed200                    bool
	bodies, acks                     [][]byte
	lost, invalid                    bool
	automatic, claimLost             bool
	claims                           []domain.HostingDatabaseNetworkCaptureClaim
	replayReached, replayJoined      chan struct{}
	replayPaused                     bool
}

func (c *databaseCaptureRuntimeWire) serve(w http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/runners/identity":
		c.identity++
		return false
	case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry":
		if !c.automatic {
			c.forbidden++
		}
		return false
	case "/api/v1/runners/database-network-captures/claim", "/api/v1/runners/database-network-captures/claim-next":
		c.claim++
		next := r.URL.Path == "/api/v1/runners/database-network-captures/claim-next"
		if next != c.automatic {
			c.invalid = true
		}
		if next {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
			if err != nil || len(raw) > 4096 || !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
				c.invalid = true
				w.WriteHeader(http.StatusBadRequest)
				return true
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		c.claimResponse = databaseCaptureObserveClaim(rec.Code, rec.Body.Bytes(), c.expectedCapture, c.expectedRunner, time.Now())
		if next && rec.Code == http.StatusOK {
			var claim domain.HostingDatabaseNetworkCaptureClaim
			if len(c.claims) >= 2 || !c.claimResponse.ClientShape || domain.DecodeHostingStorageProtocolJSON(rec.Body.Bytes(), &claim) != nil {
				c.invalid = true
			} else {
				c.claims = append(c.claims, claim)
				if !c.claimLost {
					c.claimLost = true
					w.Header().Set("Cache-Control", "no-store")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"code":"unavailable","detail":"capture_claim_ack_loss_canary"}}`)
					return true
				}
			}
		}
		if next && len(c.bodies) == 2 && c.replayReached != nil {
			// The actual controller is called first. Hold only its post-replay
			// empty response so the fixture can prove durable retirement and join
			// before ordinary work would perform its separate Docker preflight.
			if rec.Code != http.StatusNotFound || c.replayPaused {
				c.invalid = true
			} else {
				c.replayPaused = true
				close(c.replayReached)
				c.mu.Unlock()
				select {
				case <-r.Context().Done():
				case <-time.After(20 * time.Second):
					c.mu.Lock()
					c.invalid = true
					c.mu.Unlock()
				}
				c.mu.Lock()
				close(c.replayJoined)
				return true
			}
		}
		response := rec.Result()
		for key, values := range response.Header {
			w.Header()[key] = slices.Clone(values)
		}
		if response.Body.Close() != nil {
			c.invalid = true
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return true
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
		c.suppressed200 = rec.Code == http.StatusOK
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

func (c *databaseCaptureRuntimeWire) automaticClaimLost() (domain.HostingDatabaseNetworkCaptureClaim, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.automatic || !c.claimLost || c.invalid || c.claim != 1 || len(c.claims) != 1 || c.plan != 0 || len(c.bodies) != 0 || c.forbidden != 0 {
		return domain.HostingDatabaseNetworkCaptureClaim{}, false
	}
	return c.claims[0], true
}

func (c *databaseCaptureRuntimeWire) automaticClaimReused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.automatic || !c.claimLost || c.invalid || len(c.claims) != 2 {
		return false
	}
	a, b := c.claims[0], c.claims[1]
	return a.Capture.ID == b.Capture.ID && a.Capture.RunID == b.Capture.RunID && a.Capture.Plan.Hash == b.Capture.Plan.Hash && a.Lease.ID == b.Lease.ID && a.Lease.RunID == b.Lease.RunID && a.Lease.RunnerID == b.Lease.RunnerID && a.Lease.Attempt == b.Lease.Attempt && a.Lease.Fence == b.Lease.Fence && a.Lease.CreatedAt.Equal(b.Lease.CreatedAt) && a.Lease.ExpiresAt.Equal(b.Lease.ExpiresAt)
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

func databaseAutomaticCaptureArguments(f *databaseRunnerFixture, endpoint string) []string {
	args := databaseRunnerArguments(f)
	index := slices.Index(args, "--image-docker-endpoint")
	args[index+1] = endpoint
	return append(args, "--hosting-database-network-capture", "--once")
}

func databaseCaptureJournalRetired(path string) bool {
	raw, err := os.ReadFile(path)
	defer clear(raw)
	var disk struct {
		Version int                                    `json:"version"`
		Capture []runner.JournalDatabaseNetworkCapture `json:"database_network_capture"`
	}
	return err == nil && len(raw) <= 8<<20 && json.Unmarshal(raw, &disk) == nil && disk.Version > 0 && disk.Version <= 14 && len(disk.Capture) == 0
}

func (f *databaseRunnerFixture) awaitCaptureExit(success bool, checkpoint string, cursor int, wire *databaseCaptureRuntimeWire) string {
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
	output := f.logs.snapshot()
	if cursor < 0 || cursor > len(output) {
		cursor = len(output)
	}
	diagnostic := databaseCaptureDiagnostic(checkpoint, p.command.Process.Pid, exit, filepath.Join(f.root, "journal", "journal.json"), wire, output[cursor:])
	clear(output)
	f.t.Logf("capture checkpoint %s", diagnostic)
	if !exit.Exited || success && (exit.Kind != "success" || exit.Code != 0) || !success && (exit.Kind != "process_exit" || exit.Code <= 0) {
		f.t.Fatalf("capture process exit mismatch success=%t %s", success, diagnostic)
	}
	return diagnostic
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

func (f *databaseRunnerFixture) captureClaimIdentity(capture domain.HostingDatabaseNetworkCapture, a domain.HostingAuthority, recorded bool) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var lease domain.RunLease
	var leases, claims, origins int
	if f.database.QueryRow(ctx, `SELECT l.id,l.run_id,l.runner_id,l.attempt,l.fence,(SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$2 AND action='hosting.database_network_capture.claimed'),(SELECT count(*) FROM audit_events WHERE target_id=$2 AND action='hosting.database_network_capture.recorded') FROM run_leases l WHERE l.run_id=$1`, capture.RunID, capture.ID).Scan(&lease.ID, &lease.RunID, &lease.RunnerID, &lease.Attempt, &lease.Fence, &leases, &claims, &origins) != nil || leases != 1 || claims != 1 || origins != map[bool]int{false: 0, true: 1}[recorded] || lease.RunnerID != capture.Plan.RunnerID || a != (domain.HostingAuthority{OperationID: capture.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}) {
		f.t.Fatal("automatic capture duplicated or changed retained lease/audit authority")
	}
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

// captureWithRecovery reuses the actual compiled capture commit/lost-ACK/replay
// sequence. Its caller has already joined the ordinary source runner.
func (f *databaseRunnerFixture) captureWithRecovery(source domain.HostingOperation) (domain.HostingDatabaseNetworkCapture, databaseCaptureStored, map[string]map[string]databaseCaptureTreeEntry) {
	f.t.Helper()
	return f.captureWithRecoveryMode(source, false)
}

func (f *databaseRunnerFixture) captureWithRecoveryMode(source domain.HostingOperation, automatic bool) (domain.HostingDatabaseNetworkCapture, databaseCaptureStored, map[string]map[string]databaseCaptureTreeEntry) {
	f.t.Helper()
	before, err := f.operation(f.t.Context(), source.ID)
	if err != nil || before.State != "succeeded" || before.Result == nil || before.CompleteReceipt == nil {
		f.t.Fatal("capture source not settled")
	}
	capture, err := f.service.RequestHostingDatabaseNetworkCapture(f.owner, source.ID)
	if err != nil {
		f.t.Fatal("authenticated owner capture request failed")
	}
	physical := f.capturePhysical(source, capture.Plan)
	trees := f.captureTrees()
	inventory := f.replayInventory([]domain.HostingOperation{source}, source)
	completeCount := f.completionCount(source.ID)
	genericClaims, applies, deliveries := f.claimRequests.Load(), f.applyRequests.Load(), f.deliveryRequests.Load()
	wire := &databaseCaptureRuntimeWire{journalPath: filepath.Join(f.root, "journal", "journal.json"), expectedCapture: capture.ID, expectedRunner: capture.Plan.RunnerID, automatic: automatic}
	if automatic {
		wire.replayReached, wire.replayJoined = make(chan struct{}), make(chan struct{})
	}
	if !f.capture.CompareAndSwap(nil, wire) {
		f.t.Fatal("capture wire hook already active")
	}
	args := databaseCaptureArguments(f, capture.ID, "unix:///var/run/docker.sock")
	if automatic {
		args = databaseAutomaticCaptureArguments(f, "unix:///var/run/docker.sock")
		cursor := f.captureLogCursor()
		f.startRunner(args)
		f.awaitCaptureExit(false, "automatic_claim_loss", cursor, wire)
		claim, lost := wire.automaticClaimLost()
		if !lost || len(f.captureChain()) != 0 {
			f.t.Fatal("automatic claim response loss did not retain exactly one unobserved server lease")
		}
		a := domain.HostingAuthority{OperationID: capture.ID, RunID: claim.Lease.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		f.captureClaimIdentity(capture, a, false)
		if !reflect.DeepEqual(trees, f.captureTrees()) {
			f.t.Fatal("automatic claim loss changed protected workload state")
		}
	}
	cursor := f.captureLogCursor()
	f.startRunner(args)
	diagnostic := f.awaitCaptureExit(false, "initial", cursor, wire)
	if !wire.committedACKLost() {
		f.t.Fatalf("initial capture did not satisfy the committed ACK-loss boundary: %s", diagnostic)
	}
	chain := f.captureChain()
	if stage := databaseCaptureChainStage(chain); stage != "origin_pending_ack" {
		f.t.Fatalf("compiled ACK loss journal stage=%s %s", stage, diagnostic)
	}
	original := *chain[2].Origin
	stored := f.captureStored(capture.ID)
	if !reflect.DeepEqual(stored.Request, original) || stored.Authority != original.HostingAuthority || stored.Hash != domain.HostingDocumentHash(original) || stored.Receipt.BodyHash != stored.Hash || stored.Receipt.ID != original.ReceiptID || stored.Receipt.Origin != physical || stored.Receipt.OriginHash != physical.DocumentHash() || !stored.Receipt.ObservedAt.Equal(original.ObservedAt) || stored.Receipt.PlanHash != capture.Plan.Hash {
		f.t.Fatal("committed capture differs from real origin and exact durable request")
	}
	if !reflect.DeepEqual(trees, f.captureTrees()) {
		f.t.Fatal("initial capture observation changed protected trees")
	}
	// Ordinary mode must not poll around unresolved capture state.
	claimCount, planCount, originCount, forbidden := wire.counts()
	wantClaims := 1
	if automatic {
		wantClaims = 2
		if !wire.automaticClaimReused() {
			f.t.Fatal("automatic claim restart changed original lease/attempt/fence")
		}
		f.captureClaimIdentity(capture, original.HostingAuthority, true)
	}
	if claimCount != wantClaims || planCount == 0 || originCount != 1 || forbidden != 0 {
		f.t.Fatal("dedicated capture used unexpected API authority")
	}
	cursor = f.captureLogCursor()
	f.start()
	f.awaitCaptureExit(false, "ordinary_hold", cursor, wire)
	if !reflect.DeepEqual(chain, f.captureChain()) {
		f.t.Fatal("ordinary mode discarded unresolved capture state")
	}
	if c, p, o, b := wire.counts(); c != claimCount || p != planCount || o != originCount || b != 0 {
		f.t.Fatal("ordinary mode bypassed unresolved capture hold")
	}
	// Keep all original arguments except the socket. An absent fixture-owned
	// socket proves persisted-origin replay does not construct the host observer.
	absent := filepath.Join(f.root, "absent-capture.sock")
	if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal("replay absence proof socket already exists")
	}
	replayArgs := databaseCaptureArguments(f, capture.ID, "unix://"+absent)
	if automatic {
		replayArgs = databaseAutomaticCaptureArguments(f, "unix://"+absent)
	}
	cursor = f.captureLogCursor()
	f.startRunner(replayArgs)
	if automatic {
		f.t.Cleanup(func() {
			f.stop()
			select {
			case <-wire.replayReached:
				select {
				case <-wire.replayJoined:
				case <-time.After(5 * time.Second):
					f.t.Error("automatic post-replay barrier did not join during cleanup")
				}
			default:
			}
		})
		select {
		case <-wire.replayReached:
		case <-f.process.done:
			f.t.Fatal("automatic replay process exited before post-ACK phase barrier")
		case <-time.After(15 * time.Second):
			f.t.Fatal("automatic replay did not reach post-ACK phase barrier")
		}
		if !wire.exactReplay() || !databaseCaptureJournalRetired(filepath.Join(f.root, "journal", "journal.json")) {
			f.t.Fatal("automatic replay did not prove historical ACK and durable retirement before cancellation")
		}
		f.stop()
		select {
		case <-wire.replayJoined:
		case <-time.After(5 * time.Second):
			f.t.Fatal("automatic post-replay request did not join")
		}
		f.t.Log("automatic replay ACK and journal retirement proved before cancellation; whole runtime success not asserted")
	} else {
		f.awaitCaptureExit(true, "historical_replay", cursor, wire)
	}
	if len(f.captureChain()) != 0 || !wire.exactReplay() {
		f.t.Fatal("compiled historical replay changed wire body/ACK or failed durable retirement")
	}
	postReplayClaims := claimCount
	if automatic {
		postReplayClaims++ // One real, empty poll held after successful reconciliation.
		f.captureClaimIdentity(capture, original.HostingAuthority, true)
	}
	if c, p, o, b := wire.counts(); c != postReplayClaims || p != planCount || o != originCount+1 || b != 0 {
		f.t.Fatal("historical replay reclaimed, refreshed plan or used generic runner APIs")
	}
	if !reflect.DeepEqual(stored, f.captureStored(capture.ID)) || !reflect.DeepEqual(trees, f.captureTrees()) {
		f.t.Fatal("capture replay changed immutable receipt or protected trees")
	}
	afterInventory := f.replayInventory([]domain.HostingOperation{source}, source)
	if databaseRunnerReplayInventoryMatches(inventory, afterInventory, []domain.HostingOperation{source}, source) != nil || f.completionCount(source.ID) != completeCount || f.claimRequests.Load() != genericClaims || f.applyRequests.Load() != applies || f.deliveryRequests.Load() != deliveries {
		f.t.Fatal("capture changed workload residue or repeated database execution/delivery")
	}
	f.capturePhysical(source, capture.Plan)
	after, err := f.operation(f.t.Context(), source.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		f.t.Fatal("capture changed source operation/result")
	}
	if !f.capture.CompareAndSwap(wire, nil) {
		f.t.Fatal("capture wire hook identity changed")
	}
	credential, err := os.ReadFile(filepath.Join(f.root, "credential"))
	if err != nil {
		f.t.Fatal("historical plan probe credential unavailable")
	}
	client := runnerclient.New(f.server.URL, strings.TrimSpace(string(credential)))
	clear(credential)
	client.HTTP = f.client
	if _, err := client.HostingDatabaseNetworkCapturePlan(f.t.Context(), capture.Plan.RunnerID, original.HostingAuthority); err == nil {
		f.t.Fatal("historical capture completion regained current plan authority")
	}
	f.assertRedacted()
	output := f.logs.snapshot()
	for _, private := range []string{original.Fence, original.LeaseID, "capture_ack_loss_canary", "capture_claim_ack_loss_canary"} {
		if bytes.Contains(output, []byte(private)) {
			f.t.Fatal("capture output exposed private authority or response diagnostics")
		}
	}
	clear(output)
	return capture, stored, trees
}

func databaseCaptureCreateInput(engine string, automatic bool, environment, destination string) domain.HostingDatabaseCreateInput {
	version, requestID := domain.HostingDatabasePostgresRecipe, strings.Repeat("c", 32)
	if automatic {
		requestID = strings.Repeat("c2", 16)
	}
	if engine == "redis" {
		version, requestID = domain.HostingDatabaseRedisRecipe, strings.Repeat("d", 32)
		if automatic {
			requestID = strings.Repeat("d2", 16)
		}
	}
	return domain.HostingDatabaseCreateInput{RequestID: requestID, ProjectID: "proj_platform", ProjectEnvironmentID: environment, Name: "capture physical " + engine, DestinationID: destination, Engine: engine, RecipeVersion: version}
}

func TestHostingDatabaseCaptureCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("run reviewed acceptance/runtime-hosting-database-runner/gate.sh after exact physical authorization")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	if os.Getenv("NEROCD_TEST_DATABASE_URL") == "" {
		t.Fatal("physical capture control database configuration missing")
	}
	for _, automatic := range []bool{false, true} {
		mode := "exact_id"
		if automatic {
			mode = "automatic"
		}
		t.Run(mode, func(t *testing.T) {
			f := newDatabaseRunnerFixtureWithCapture(t, true)
			for index, engine := range []string{"postgresql", "redis"} {
				if index > 0 {
					f.start()
				}
				view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", databaseCaptureCreateInput(engine, automatic, f.environment, f.destination), 201)
				source := f.await(f.deploy(view, "capture_source").ID)
				marker := "capture_physical_" + engine + "_before"
				f.marker(view, source, marker, true)
				f.awaitSourceJournalRetired()
				f.stop()
				capture, stored, trees := f.captureWithRecoveryMode(source, automatic)
				f.marker(view, source, marker, false)
				freshMarker := "capture_physical_" + engine + "_after"
				f.marker(view, source, freshMarker, true)
				f.marker(view, source, freshMarker, false)
				if !reflect.DeepEqual(trees, f.captureTrees()) {
					t.Fatal("post-capture application access changed protected roots")
				}
				f.assertRedacted()
				t.Logf("physical capture automatic=%t engine=%s source=%s capture=%s original_receipt=%s container=%s volume=%s network=%s lazy_replay_absent_socket=true", automatic, engine, source.ID, capture.ID, stored.Receipt.ID, source.Result.ContainerID, source.Plan.Storage.VolumeName, stored.Receipt.Origin.NetworkID)
			}
		})
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

// The diagnostic vocabulary is closed. Journal content and captured output only
// select fixed labels or booleans; they never become output strings.
func databaseCapturePhase(phase string) string {
	switch phase {
	case "claim", "plan", "origin", "ack":
		return phase
	}
	return "unknown"
}

func databaseCaptureBound(n int) int {
	if n < 0 {
		return 0
	}
	if n > 9999 {
		return 9999
	}
	return n
}

func databaseCaptureLogCategories(raw []byte) string {
	// These labels report observed complete lines, not inferred root causes.
	markers := []struct{ line, category string }{
		{"capture persistent configuration unavailable", "persistent_configuration"},
		{"capture credential unavailable", "credential"},
		{"capture runner identity not confirmed", "identity"},
		{"capture requires verified HTTPS", "verified_transport"},
		{"database network capture requires verified HTTPS", "verified_transport"},
		{"capture requires one exact ID and execution without generic completion", "flags"},
		{"capture cannot share registration or other execution options", "flags"},
		{"capture requires explicit credential, journal and persistent root flags", "flags"},
		{"capture requires explicit credential and journal", "flags"},
		{"database network capture requires a separate exact-ID execution mode", "mode"},
		{"database network capture remains unresolved", "unresolved"},
		{"retained database network capture requires explicit exact-ID capture mode", "ordinary_hold"},
		{"context canceled", "canceled"},
		{"context deadline exceeded", "deadline"},
	}
	var categories []string
	for _, marker := range markers {
		for line := range bytes.SplitSeq(raw, []byte("\n")) {
			if bytes.Equal(line, []byte(marker.line)) && !slices.Contains(categories, marker.category) {
				categories = append(categories, marker.category)
				break
			}
		}
	}
	if len(categories) == 0 {
		return "unclassified"
	}
	return strings.Join(categories, ",")
}

func databaseCaptureJournalSummary(path string) string {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "state=absent entries=0"
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return "state=unavailable entries=0"
	}
	raw, err := os.ReadFile(path)
	defer clear(raw)
	if err != nil || len(raw) > 8<<20 {
		return "state=unavailable entries=0"
	}
	var disk struct {
		Version int `json:"version"`
		Entries []struct {
			Phase                    string `json:"phase"`
			Claim, Plan, Origin, Ack json.RawMessage
		} `json:"database_network_capture"`
	}
	if json.Unmarshal(raw, &disk) != nil {
		return "state=malformed entries=0"
	}
	version := "other_version"
	if disk.Version == 13 {
		version = "v13"
	}
	var entries []string
	present := func(raw json.RawMessage) bool {
		return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	for i, entry := range disk.Entries {
		if i == 4 {
			break
		}
		entries = append(entries, fmt.Sprintf("%s:%t/%t/%t/%t", databaseCapturePhase(entry.Phase), present(entry.Claim), present(entry.Plan), present(entry.Origin), present(entry.Ack)))
	}
	return fmt.Sprintf("state=%s entries=%d phase_payloads=[%s]", version, databaseCaptureBound(len(disk.Entries)), strings.Join(entries, ","))
}

func (c *databaseCaptureRuntimeWire) committedACKLost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost && c.suppressed200 && !c.invalid && len(c.bodies) == 1 && len(c.acks) == 1
}

func databaseCaptureDiagnostic(checkpoint string, pid int, exit databaseRunnerExit, path string, wire *databaseCaptureRuntimeWire, output []byte) string {
	switch checkpoint {
	case "initial", "ordinary_hold", "historical_replay", "automatic_claim_loss":
	default:
		checkpoint = "unknown"
	}
	kind := "unknown"
	switch exit.Kind {
	case "running", "success", "process_exit", "wait_error":
		kind = exit.Kind
	}
	code := exit.Code
	if code < -1 || code > 255 {
		code = -1
	}
	if pid < 1 {
		pid = -1
	}
	wire.mu.Lock()
	counts := fmt.Sprintf("identity=%d claim=%d plan=%d origin=%d forbidden=%d committed200=%d suppressed200=%t invalid=%t", databaseCaptureBound(wire.identity), databaseCaptureBound(wire.claim), databaseCaptureBound(wire.plan), databaseCaptureBound(len(wire.bodies)), databaseCaptureBound(wire.forbidden), databaseCaptureBound(len(wire.acks)), wire.suppressed200, wire.invalid)
	claim := wire.claimResponse.summary()
	wire.mu.Unlock()
	return fmt.Sprintf("process=%s pid=%d exited=%t exit_kind=%s exit_code=%d journal=[%s] wire_totals=[%s] claim_response=[%s] log_categories=[%s]", checkpoint, pid, exit.Exited, kind, code, databaseCaptureJournalSummary(path), counts, claim, databaseCaptureLogCategories(output))
}

func (f *databaseRunnerFixture) captureLogCursor() int {
	raw := f.logs.snapshot()
	defer clear(raw)
	return len(raw)
}

func databaseCaptureChainStage(chain []runner.JournalDatabaseNetworkCapture) string {
	if len(chain) == 0 {
		return "before_claim"
	}
	if len(chain) > 3 {
		return "unexpected_depth"
	}
	if chain[0].Phase != "claim" {
		return "claim_phase_mismatch"
	}
	if chain[0].Claim == nil {
		return "claim_payload_missing"
	}
	if len(chain) == 1 {
		return "before_plan"
	}
	if chain[1].Phase != "plan" {
		return "plan_phase_mismatch"
	}
	if chain[1].Plan == nil {
		return "plan_payload_missing"
	}
	if len(chain) == 2 {
		return "before_origin"
	}
	if chain[2].Phase != "origin" {
		return "origin_phase_mismatch"
	}
	if chain[2].Origin == nil {
		return "origin_payload_missing"
	}
	return "origin_pending_ack"
}

func TestDatabaseCaptureRuntimeDiagnosticSeparatesEarlyExitFromACKLoss(t *testing.T) {
	wire := &databaseCaptureRuntimeWire{}
	if wire.committedACKLost() {
		t.Fatal("early exit classified as committed ACK loss")
	}
	if stage := databaseCaptureChainStage(nil); stage != "before_claim" {
		t.Fatal("empty chain lost stage")
	}
	chain := []runner.JournalDatabaseNetworkCapture{{Phase: "claim", Claim: &domain.HostingDatabaseNetworkCaptureClaim{}}}
	if stage := databaseCaptureChainStage(chain); stage != "before_plan" {
		t.Fatal("claim-only chain lost stage")
	}
	chain = append(chain, runner.JournalDatabaseNetworkCapture{Phase: "plan", Plan: &domain.HostingDatabaseNetworkCapture{}})
	if stage := databaseCaptureChainStage(chain); stage != "before_origin" {
		t.Fatal("plan-only chain lost stage")
	}
	chain = append(chain, runner.JournalDatabaseNetworkCapture{Phase: "origin", Origin: &domain.HostingDatabaseNetworkOriginRequest{}})
	if stage := databaseCaptureChainStage(chain); stage != "origin_pending_ack" {
		t.Fatal("retained origin lost stage")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"committed"}`)
	})
	rec := httptest.NewRecorder()
	wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/origin", strings.NewReader(`{}`)), handler)
	if rec.Code != 503 || !wire.committedACKLost() {
		t.Fatal("committed suppressed200 not distinguished from early exit")
	}
	rejected := &databaseCaptureRuntimeWire{}
	rejected.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/origin", strings.NewReader(`{}`)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(409) }))
	if rejected.committedACKLost() {
		t.Fatal("rejected origin classified as committed ACK loss")
	}
}

func TestDatabaseCaptureRuntimeDiagnosticRedactsCanaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private_journal_path_canary")
	raw := []byte(`{"version":13,"database_network_capture":[{"phase":"private_fence_canary","claim":{"secret":"ncr_bearer_canary"}},{"phase":"plan","plan":{}},{"phase":"origin","origin":{"url":"https://private.invalid/?token=canary"}}]}`)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("diagnostic fixture write failed")
	}
	output := []byte("capture persistent configuration unavailable\ncapture persistent configuration unavailable ncr_bearer_canary\nprivate log canary\nhttps://private.invalid/?token=canary\n")
	got := databaseCaptureDiagnostic("private_checkpoint_canary", 42, databaseRunnerExit{Exited: true, Kind: "private_exit_canary", Code: 1000}, path, &databaseCaptureRuntimeWire{identity: 10000}, output)
	for _, secret := range []string{root, path, "private_", "ncr_", "https://", "?token=", "canary"} {
		if strings.Contains(got, secret) {
			t.Fatal("diagnostic exposed untrusted source text")
		}
	}
	for _, expected := range []string{"process=unknown pid=42", "exit_kind=unknown exit_code=-1", "state=v13 entries=3", "unknown:true/false/false/false", "plan:false/true/false/false", "origin:false/false/true/false", "identity=9999", "log_categories=[persistent_configuration]"} {
		if !strings.Contains(got, expected) {
			t.Fatal("diagnostic omitted bounded stage evidence")
		}
	}
	if category := databaseCaptureLogCategories([]byte("capture credential unavailable: private_canary\nprivate_canary\n")); category != "unclassified" {
		t.Fatal("diagnostic accepted partial error match")
	}
}

// Only closed scalar observations leave the private claim decoder. Shape flags
// mirror the referenced client/coordinator/journal checks; they do not prove a
// journal append succeeds or authorize a claim that production rejected.
type databaseCaptureClaimDiagnostic struct {
	Status            int  `json:"status"`
	BodyBytes         int  `json:"body_bytes"`
	BodyWithinLimit   bool `json:"body_within_limit"`
	Decoded           bool `json:"strict_decode"`
	PlanValid         bool `json:"plan_valid"`
	CaptureMatch      bool `json:"capture_match"`
	OperationMatch    bool `json:"operation_match"`
	RunMatch          bool `json:"run_match"`
	RunnerMatch       bool `json:"runner_match"`
	CreatorPresent    bool `json:"creator_present"`
	CreatorClientID   bool `json:"creator_client_id"`
	CreatorJournalID  bool `json:"creator_journal_id"`
	CaptureTimes      bool `json:"capture_times"`
	RunningNoReceipt  bool `json:"running_no_receipt"`
	LeaseAuthority    bool `json:"lease_authority"`
	LeaseActive       bool `json:"lease_active"`
	CompletionAbsent  bool `json:"completion_absent"`
	LeaseCreated      bool `json:"lease_created"`
	LeaseOrdering     bool `json:"lease_ordering"`
	LeaseDuration     bool `json:"lease_duration"`
	LeaseLive         bool `json:"lease_live"`
	ClientShape       bool `json:"client_shape"`
	TargetShape       bool `json:"target_shape"`
	JournalClaimShape bool `json:"journal_claim_shape"`
}

var databaseCaptureClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var databaseCaptureJournalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func databaseCaptureObserveClaim(status int, raw []byte, id, runnerID string, now time.Time) databaseCaptureClaimDiagnostic {
	d := databaseCaptureClaimDiagnostic{Status: status, BodyBytes: len(raw), BodyWithinLimit: len(raw) <= domain.HostingStorageProtocolJSONLimit}
	if d.Status < 100 || d.Status > 599 {
		d.Status = 0
	}
	if d.BodyBytes > domain.HostingStorageProtocolJSONLimit+1 {
		d.BodyBytes = domain.HostingStorageProtocolJSONLimit + 1
	}
	var claim domain.HostingDatabaseNetworkCaptureClaim
	if status != http.StatusOK || !d.BodyWithinLimit || domain.DecodeHostingStorageProtocolJSON(raw, &claim) != nil {
		return d
	}
	d.Decoded = true
	return databaseCaptureClaimShape(d, claim, id, runnerID, now)
}

func databaseCaptureClaimShape(d databaseCaptureClaimDiagnostic, claim domain.HostingDatabaseNetworkCaptureClaim, id, runnerID string, now time.Time) databaseCaptureClaimDiagnostic {
	c, l := claim.Capture, claim.Lease
	d.PlanValid = c.Plan.Validate() == nil
	d.CaptureMatch = c.ID == id
	d.OperationMatch = c.Plan.OperationID == id
	d.RunMatch = c.RunID == "capture_"+id && l.RunID == c.RunID
	d.RunnerMatch = c.Plan.RunnerID == runnerID && l.RunnerID == runnerID
	d.CreatorPresent = c.CreatedBy != ""
	d.CreatorClientID = databaseCaptureClientID.MatchString(c.CreatedBy)
	d.CreatorJournalID = databaseCaptureJournalID.MatchString(c.CreatedBy)
	d.CaptureTimes = !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt)
	d.RunningNoReceipt = c.State == "running" && c.Receipt == nil
	d.LeaseAuthority = databaseCaptureClientID.MatchString(l.ID) && databaseCaptureClientID.MatchString(l.Fence) && databaseCaptureClientID.MatchString(l.RunID) && l.Attempt > 0 && l.Attempt <= 2147483647
	d.LeaseActive = l.Status == domain.LeaseActive
	d.CompletionAbsent = l.CompletedAt == nil && l.CompletionKey == ""
	d.LeaseCreated = !l.CreatedAt.IsZero() && !l.CreatedAt.After(now.Add(30*time.Second))
	d.LeaseOrdering = l.ExpiresAt.After(l.CreatedAt)
	d.LeaseDuration = l.ExpiresAt.Sub(l.CreatedAt) <= 10*time.Minute
	d.LeaseLive = l.ExpiresAt.After(now)
	// The client checks completed_at but does not require completion_key to be
	// empty. The journal requires both completion fields to be absent.
	d.ClientShape = d.PlanValid && d.CaptureMatch && d.OperationMatch && d.RunMatch && d.RunnerMatch && databaseCaptureClientID.MatchString(runnerID) && d.CreatorClientID && d.CaptureTimes && d.RunningNoReceipt && d.LeaseAuthority && d.LeaseActive && l.CompletedAt == nil && d.LeaseCreated && d.LeaseOrdering && d.LeaseDuration && d.LeaseLive
	d.TargetShape = d.PlanValid && d.CaptureMatch && d.OperationMatch && c.RunID == "capture_"+id && c.Plan.RunnerID == runnerID && d.CreatorPresent && d.CaptureTimes && d.RunningNoReceipt
	d.JournalClaimShape = d.PlanValid && c.ID == c.Plan.OperationID && c.RunID == "capture_"+c.ID && d.CreatorJournalID && d.CaptureTimes && d.RunningNoReceipt && l.RunID == c.RunID && l.RunnerID == c.Plan.RunnerID && l.Attempt > 0 && l.Attempt <= 2147483647 && databaseCaptureJournalID.MatchString(l.ID) && databaseCaptureJournalID.MatchString(l.Fence) && d.LeaseActive && d.CompletionAbsent && !l.CreatedAt.IsZero() && d.LeaseOrdering && d.LeaseDuration
	return d
}

func (d databaseCaptureClaimDiagnostic) summary() string {
	raw, err := json.Marshal(d)
	if err != nil {
		return "unavailable"
	}
	return string(raw)
}

func TestDatabaseCaptureRuntimeClaimRecorderForwardsUnchanged(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict} {
		wire := &databaseCaptureRuntimeWire{expectedCapture: "hnc_private_id_canary", expectedRunner: "runner_private_canary"}
		body := []byte(`{"private":"ncr_bearer_canary https://private.invalid/?fence=canary"}`)
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Add("X-Fixture", "first")
			w.Header().Add("X-Fixture", "second")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		})
		rec := httptest.NewRecorder()
		if !wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/claim", strings.NewReader(`{"capture_id":"private"}`)), handler) || rec.Code != status || !bytes.Equal(rec.Body.Bytes(), body) || !reflect.DeepEqual(rec.Header().Values("X-Fixture"), []string{"first", "second"}) || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("claim recorder changed response status, headers or bytes")
		}
		if wire.claimResponse.Status != status || wire.claimResponse.BodyBytes != len(body) || wire.claimResponse.Decoded || wire.claim != 1 || wire.lost || len(wire.acks) != 0 {
			t.Fatal("claim recorder confused rejected/malformed response with capture ACK fault")
		}
		summary := wire.claimResponse.summary()
		for _, secret := range []string{"private", "canary", "ncr_", "https://", "fence"} {
			if strings.Contains(summary, secret) {
				t.Fatal("claim diagnostic exposed response or expected identity")
			}
		}
	}
}

func TestDatabaseCaptureRuntimeClaimShapeUsesActualIdentityAndTime(t *testing.T) {
	now := time.Now().UTC()
	id, runnerID := "hnc_"+strings.Repeat("a", 32), "runner_fixture"
	claim := domain.HostingDatabaseNetworkCaptureClaim{Capture: domain.HostingDatabaseNetworkCapture{ID: id, RunID: "capture_" + id, State: "running", CreatedBy: "_client_only", CreatedAt: now, UpdatedAt: now, Plan: domain.HostingDatabaseNetworkCapturePlan{OperationID: id, RunnerID: runnerID}}, Lease: domain.RunLease{ID: "lease_fixture", RunID: "capture_" + id, RunnerID: runnerID, Fence: "private_fence_canary", Attempt: 1, Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	d := databaseCaptureClaimShape(databaseCaptureClaimDiagnostic{}, claim, id, runnerID, now)
	if !d.CreatorClientID || d.CreatorJournalID || !d.CaptureMatch || !d.OperationMatch || !d.RunMatch || !d.RunnerMatch || !d.CaptureTimes || !d.CompletionAbsent || !d.LeaseOrdering || !d.LeaseDuration || !d.LeaseLive {
		t.Fatal("claim diagnostic omitted actual identity-profile or time distinction")
	}
	claim.Capture.CreatedBy = "usr_fixture"
	claim.Lease.CompletionKey = "private_completion_canary"
	claim.Lease.ExpiresAt = now.Add(-time.Second)
	d = databaseCaptureClaimShape(databaseCaptureClaimDiagnostic{}, claim, id, runnerID, now)
	if !d.CreatorJournalID || d.CompletionAbsent || d.LeaseLive || d.LeaseOrdering {
		t.Fatal("claim diagnostic missed completion or expired lease")
	}
	raw := []byte(`{"capture":{},"capture":{},"lease":{}}`)
	if databaseCaptureObserveClaim(200, raw, id, runnerID, now).Decoded {
		t.Fatal("claim diagnostic bypassed strict duplicate-key decoder")
	}
	oversized := bytes.Repeat([]byte("x"), domain.HostingStorageProtocolJSONLimit+20)
	bounded := databaseCaptureObserveClaim(900, oversized, id, runnerID, now)
	if bounded.Status != 0 || bounded.BodyBytes != domain.HostingStorageProtocolJSONLimit+1 || bounded.BodyWithinLimit || bounded.Decoded {
		t.Fatal("claim response diagnostics exceeded fixed bounds")
	}
}

func databaseCaptureAutomaticClaimFixture(t *testing.T) domain.HostingDatabaseNetworkCaptureClaim {
	t.Helper()
	op := databaseOperationResponse(t, "postgresql")
	created := time.Now().UTC().Add(-time.Hour)
	storage := domain.HostingStorage{ID: op.Snapshot.Storage.StorageID, ProjectID: op.ProjectID, ResourceID: op.ResourceID, BindingID: op.BindingID, DestinationID: op.Snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: op.Plan.Storage.VolumeName, MountPath: op.Plan.Storage.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: op.CreatedBy, CreatedAt: created}
	storage.Hash = storage.DocumentHash()
	snapshot := op.Snapshot
	snapshot.Storage = storage.Reference()
	db := *op.Plan.Database
	var err error
	snapshot.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(snapshot, db.Credentials)
	if err != nil {
		t.Fatal("automatic claim configuration fixture failed")
	}
	db.Reference = snapshot.Database
	sp := *op.Plan.Storage
	sp.Reference = storage.Reference()
	sp.CreationNonce = domain.HostingStorageCreationNonce(sp.Reference, sp.CreationOperationID, sp.DaemonID)
	plan, err := domain.CompileHostingDatabasePlan(snapshot, *op.Release, nil, sp, db)
	if err != nil {
		t.Fatal("automatic claim source fixture failed")
	}
	receipt := domain.HostingReceipt{ID: "receipt_fixture", BodyHash: domain.HostingDocumentHash("receipt_fixture"), CreatedAt: created}
	m := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "materialized", CreationOperationID: op.ID, CreationPlanHash: plan.Hash, CreationNonce: sp.CreationNonce, DaemonID: op.Release.DaemonID, MaterializedReceipt: &receipt}
	m.Labels = domain.ExpectedHostingStorageLabels(storage, m)
	healthy := domain.HostingHealthy{Snapshot: snapshot, OperationID: op.ID, ContainerID: strings.Repeat("a", 64), Release: *op.Release, PlanHash: plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: sp, CreationPlanHash: plan.Hash}}
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(op.ID), SourceOperationID: op.ID, ProjectID: op.ProjectID, ResourceID: op.ResourceID, BindingID: op.BindingID, DestinationID: snapshot.DestinationID, ServerID: snapshot.ServerID, RunnerID: snapshot.RunnerID, SourcePlan: plan, SourceHealthy: healthy, Storage: storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil {
		t.Fatal("automatic claim capture fixture invalid")
	}
	now := time.Now().UTC()
	c := domain.HostingDatabaseNetworkCapture{ID: p.OperationID, RunID: "capture_" + p.OperationID, State: "running", Plan: p, CreatedBy: op.CreatedBy, CreatedAt: now, UpdatedAt: now}
	return domain.HostingDatabaseNetworkCaptureClaim{Capture: c, Lease: domain.RunLease{ID: "lease_fixture", RunID: c.RunID, RunnerID: p.RunnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "fence_private_canary", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
}

func TestDatabaseCaptureRuntimeAutomaticClaimLossAndIdentity(t *testing.T) {
	claim := databaseCaptureAutomaticClaimFixture(t)
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal("automatic claim fixture encoding failed")
	}
	wire := &databaseCaptureRuntimeWire{automatic: true, expectedCapture: claim.Capture.ID, expectedRunner: claim.Lease.RunnerID}
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, e := io.ReadAll(r.Body)
		if e != nil || string(body) != "{}" {
			t.Error("automatic wire changed strict empty request")
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	})
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		rec := httptest.NewRecorder()
		if !wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/claim-next", strings.NewReader("{}")), handler) || rec.Code != status {
			t.Fatal("automatic wire did not lose exactly one committed claim response")
		}
		if status == http.StatusServiceUnavailable {
			if _, ok := wire.automaticClaimLost(); !ok {
				t.Fatal("automatic claim loss not qualified")
			}
		} else if !bytes.Equal(rec.Body.Bytes(), raw) {
			t.Fatal("claim recovery changed real envelope bytes")
		}
	}
	if calls != 2 || !wire.automaticClaimReused() {
		t.Fatal("automatic claim recovery changed original authority")
	}
	wire.claims[1].Lease.Fence = "changed_private_canary"
	if wire.automaticClaimReused() {
		t.Fatal("automatic fixture accepted changed original fence")
	}
	for _, bad := range []string{`{"capture_id":"private_canary"}`, `{} {}`, `null`, strings.Repeat(" ", 4095) + "{}"} {
		rec := httptest.NewRecorder()
		before := calls
		if !wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/claim-next", strings.NewReader(bad)), handler) || rec.Code != http.StatusBadRequest || calls != before {
			t.Fatal("automatic fixture forwarded nonempty or unbounded selection input")
		}
	}
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusForbidden} {
		rejected := &databaseCaptureRuntimeWire{automatic: true}
		rec := httptest.NewRecorder()
		rejected.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/claim-next", strings.NewReader("{}")), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		if _, ok := rejected.automaticClaimLost(); ok || rec.Code != status {
			t.Fatal("server rejection qualified as committed automatic claim loss")
		}
	}
}

func TestDatabaseCaptureRuntimeAutomaticReplayBarrierJoins(t *testing.T) {
	wire := &databaseCaptureRuntimeWire{automatic: true, bodies: [][]byte{[]byte("origin"), []byte("origin")}, replayReached: make(chan struct{}), replayJoined: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	var called bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNotFound) })
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/database-network-captures/claim-next", strings.NewReader("{}")).WithContext(ctx), handler)
	}()
	select {
	case <-wire.replayReached:
	case <-time.After(time.Second):
		t.Fatal("post-replay barrier did not enter")
	}
	if !called || rec.Body.Len() != 0 {
		t.Fatal("post-replay barrier did not call real handler before holding response")
	}
	counts := make(chan struct{})
	go func() { wire.counts(); close(counts) }()
	select {
	case <-counts:
	case <-time.After(time.Second):
		t.Fatal("post-replay barrier retained diagnostic mutex")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("post-replay barrier did not cancel and join")
	}
	select {
	case <-wire.replayJoined:
	default:
		t.Fatal("post-replay barrier join not published")
	}
	if wire.invalid || !wire.replayPaused {
		t.Fatal("canceled post-replay barrier lost phase evidence")
	}
}

func TestDatabaseCaptureRuntimeAutomaticArgumentsAndRetirement(t *testing.T) {
	f := &databaseRunnerFixture{root: "/fixture/owned", server: &httptest.Server{URL: "https://fixture.invalid"}}
	args := databaseAutomaticCaptureArguments(f, "unix:///fixture/owned/absent.sock")
	for _, required := range []string{"--hosting-database-network-capture", "--hosting-database", "--once", "--execute", "--credential-file", "--journal-dir", "--database-state-root", "--database-secret-root"} {
		if !slices.Contains(args, required) {
			t.Fatal("automatic arguments lost explicit protected configuration")
		}
	}
	for _, forbidden := range []string{"--database-network-capture-id", "--register", "--capabilities", "--complete-status"} {
		if slices.Contains(args, forbidden) {
			t.Fatal("automatic arguments supplied identity or generic completion")
		}
	}
	index := slices.Index(args, "--image-docker-endpoint")
	if index < 0 || args[index+1] != "unix:///fixture/owned/absent.sock" {
		t.Fatal("automatic replay arguments lost absent socket")
	}
	path := filepath.Join(t.TempDir(), "journal.json")
	for _, tc := range []struct {
		body    string
		retired bool
	}{{`{"version":1,"events":[],"completions":[],"provenance":[]}`, true}, {`{"version":13,"database_network_capture":[{}]}`, false}, {`{"version":0}`, false}, {`{"version":1`, false}} {
		if os.WriteFile(path, []byte(tc.body), 0600) != nil {
			t.Fatal("retirement fixture write failed")
		}
		if databaseCaptureJournalRetired(path) != tc.retired {
			t.Fatal("retirement phase proof accepted unresolved or malformed journal")
		}
	}
}

func TestDatabaseCaptureRuntimePhysicalIdentitiesDisjoint(t *testing.T) {
	const environment, destination = "penv_fixture", "dest_fixture"
	type createCase struct {
		name    string
		input   domain.HostingDatabaseCreateInput
		request string
	}
	var cases []createCase
	for index, engine := range []string{"postgresql", "redis"} {
		normal := databaseCaptureCreateInput(engine, false, environment, destination)
		normal.RequestID = strings.Repeat(string(rune('a'+index)), 32)
		cases = append(cases, createCase{"normal_" + engine, normal, normal.RequestID})
		upgrade := databaseUpgradeCreateInput(engine, environment, destination)
		cases = append(cases, createCase{"upgrade_" + engine, upgrade, strings.Repeat(string(rune('e'+index)), 32)})
	}
	for _, automatic := range []bool{false, true} {
		mode := "exact"
		if automatic {
			mode = "automatic"
		}
		for index, engine := range []string{"postgresql", "redis"} {
			request := strings.Repeat(string(rune('c'+index)), 32)
			if automatic {
				request = strings.Repeat(string(rune('c'+index))+"2", 16)
			}
			cases = append(cases, createCase{"capture_" + mode + "_" + engine, databaseCaptureCreateInput(engine, automatic, environment, destination), request})
		}
	}
	seen := map[string]string{}
	for _, tc := range cases {
		if tc.input.Validate() != nil {
			t.Fatalf("%s has invalid database-create fixture input", tc.name)
		}
		resource := domain.HostingDatabaseResourceID(tc.input.ProjectID, tc.input.RequestID)
		// This is the store's database-creation binding projection. The storage
		// and network names use their actual production identity functions.
		binding := "binding_db_" + strings.TrimPrefix(resource, "res_db_")
		storage, volume := domain.HostingStorageIdentity(binding)
		for _, identity := range []struct{ kind, value string }{{"resource", resource}, {"binding", binding}, {"storage", storage}, {"volume", volume}, {"network", domain.HostingDatabaseNetworkName(binding)}} {
			key := identity.kind + ":" + identity.value
			if prior, exists := seen[key]; exists {
				t.Fatalf("%s %s collides with %s", tc.name, identity.kind, prior)
			}
			seen[key] = tc.name
		}
		if tc.input.RequestID != tc.request {
			t.Fatalf("%s changed a frozen fixture request identity", tc.name)
		}
	}
}
