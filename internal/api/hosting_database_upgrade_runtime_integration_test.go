package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
)

// This fault withholds only the first successful real Complete response. The
// original request/ACK stay private so restart must prove exact historical replay.
type databaseUpgradeCompleteWire struct {
	mu                          sync.Mutex
	operationID                 string
	entered, finished, replayed chan struct{}
	request                     domain.HostingCompleteRequest
	bodies, acks                [][]byte
	committed, invalid          bool
}

func newDatabaseUpgradeCompleteWire(id string) *databaseUpgradeCompleteWire {
	return &databaseUpgradeCompleteWire{operationID: id, entered: make(chan struct{}), finished: make(chan struct{}), replayed: make(chan struct{})}
}

func (c *databaseUpgradeCompleteWire) serve(w http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	if r.URL.Path != "/api/v1/runners/hosting/complete" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	var request domain.HostingCompleteRequest
	if err != nil || len(raw) > 8192 || domain.DecodeHostingStorageProtocolJSON(raw, &request) != nil {
		c.mu.Lock()
		c.invalid = true
		c.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if request.OperationID != c.operationID {
		return false
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	c.mu.Lock()
	c.bodies = append(c.bodies, bytes.Clone(raw))
	if rec.Code != http.StatusOK {
		c.invalid = true
	} else {
		c.acks = append(c.acks, bytes.Clone(rec.Body.Bytes()))
	}
	first := rec.Code == http.StatusOK && !c.committed
	if first {
		c.committed = true
		c.request = request
	}
	replay := rec.Code == http.StatusOK && len(c.acks) == 2
	c.mu.Unlock()
	if first {
		defer close(c.finished)
		close(c.entered)
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			c.mu.Lock()
			c.invalid = true
			c.mu.Unlock()
		}
		return true
	}
	for key, values := range rec.Header() {
		w.Header()[key] = slices.Clone(values)
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
	if replay {
		close(c.replayed)
	}
	return true
}

func (c *databaseUpgradeCompleteWire) original() (domain.HostingCompleteRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request, c.committed && !c.invalid && len(c.bodies) == 1 && len(c.acks) == 1
}

func (c *databaseUpgradeCompleteWire) exactReplay() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.committed && !c.invalid && len(c.bodies) == 2 && len(c.acks) == 2 && bytes.Equal(c.bodies[0], c.bodies[1]) && bytes.Equal(c.acks[0], c.acks[1])
}

func databaseRunnerValidatePlan(op domain.HostingOperation) error {
	if op.Plan == nil {
		return errors.New("database fixture plan missing")
	}
	if op.DatabaseUpgrade != nil {
		return domain.ValidateHostingDatabaseUpgradeOperation(op)
	}
	return domain.ValidateHostingDatabasePlan(*op.Plan)
}

func databaseUpgradeClientEndpoint(op domain.HostingOperation, address string) (string, string, error) {
	invalid := errors.New("database fixture alias invalid")
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.String() != address || op.Plan == nil || op.Plan.Database == nil || op.Plan.Database.Network == nil || op.Plan.Database.Network.Alias != domain.HostingDatabaseBindingAlias(op.BindingID) {
		return "", "", invalid
	}
	return op.Plan.Database.Network.Alias, address, nil
}

func (f *databaseRunnerFixture) upgradeJournal() runner.JournalSnapshot {
	f.t.Helper()
	if f.process != nil {
		f.t.Fatal("upgrade journal requires joined process")
	}
	journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		f.t.Fatal("upgrade journal unavailable")
	}
	snapshot := journal.Snapshot()
	if journal.Close() != nil {
		f.t.Fatal("upgrade journal close failed")
	}
	return snapshot
}

func (f *databaseRunnerFixture) upgradeStorageIdentity(source domain.HostingOperation) [sha256.Size]byte {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var raw []byte
	var receipts, foreignReceipts int
	if f.database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_storage_materialized_receipts WHERE storage_id=$1),(SELECT count(*) FROM hosting_storage_materialized_receipts WHERE storage_id=$1 AND operation_id<>$2),jsonb_build_object('materialization',m,'receipt',(SELECT to_jsonb(r) FROM hosting_storage_materialized_receipts r WHERE r.storage_id=$1))::text FROM hosting_storage_materializations m WHERE storage_id=$1`, source.Plan.Storage.Reference.StorageID, source.ID).Scan(&receipts, &foreignReceipts, &raw) != nil || receipts != 1 || foreignReceipts != 0 {
		f.t.Fatal("upgrade changed original materialization identity")
	}
	return sha256.Sum256(raw)
}

func (f *databaseRunnerFixture) upgradeCredentialIdentity(view domain.HostingDatabase) map[string][sha256.Size]byte {
	f.t.Helper()
	result := map[string][sha256.Size]byte{}
	for _, ref := range view.Credentials {
		value := f.reveal(ref)
		result[ref.Role] = sha256.Sum256(value)
		clear(value)
	}
	return result
}

func (f *databaseRunnerFixture) upgradeInventory(op domain.HostingOperation) databaseRunnerReplayInventory {
	f.t.Helper()
	inventory := f.replayInventory([]domain.HostingOperation{op}, op)
	if len(inventory.BindingContainers) != 1 || len(inventory.VolumeUsers) != 1 {
		f.t.Fatal("upgrade retained a stopped predecessor or extra volume user")
	}
	users := strings.Fields(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "network="+op.DatabaseUpgrade.OriginReceipt.Origin.NetworkID)))
	if len(users) != 1 || users[0] != op.Result.ContainerID {
		f.t.Fatal("upgrade private bridge has residual users")
	}
	container := f.inspect(op)
	if container.NetworkSettings.Networks[op.Plan.Database.NetworkName].NetworkID != op.DatabaseUpgrade.OriginReceipt.Origin.NetworkID {
		f.t.Fatal("upgrade changed actual private bridge identity")
	}
	return inventory
}

func (f *databaseRunnerFixture) requestUpgrade(view domain.HostingDatabase, capture domain.HostingDatabaseNetworkCapture, receipt domain.HostingDatabaseNetworkOriginReceipt, key string) domain.HostingOperation {
	f.t.Helper()
	op, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.owner, view.Resource.ID, domain.HostingDatabaseUpgradeInput{ExpectedGeneration: view.Resource.Generation, CaptureID: capture.ID, ExpectedOriginHash: receipt.OriginHash, IdempotencyKey: key})
	if err != nil || op.DatabaseUpgrade == nil || op.Plan != nil || op.Release != nil || domain.ValidateHostingDatabaseUpgradeOperation(op) != nil || domain.HostingDocumentHash(op.DatabaseUpgrade.CapturePlan) != domain.HostingDocumentHash(capture.Plan) || domain.HostingDocumentHash(op.DatabaseUpgrade.OriginReceipt) != domain.HostingDocumentHash(receipt) {
		f.t.Fatal("authenticated owner upgrade did not retain exact controller proof")
	}
	return op
}

func databaseUpgradeMatches(source, previous, current domain.HostingOperation, receipt domain.HostingDatabaseNetworkOriginReceipt) bool {
	if databaseRunnerValidatePlan(current) != nil || current.DatabaseUpgrade == nil || previous.Plan == nil || previous.Result == nil || current.Result == nil || current.Release == nil || source.Plan == nil || source.Release == nil {
		return false
	}
	p, n := current.DatabaseUpgrade, current.Plan.Database.Network
	return p.PreviousHealthy.OperationID == previous.ID && p.PreviousHealthy.ContainerID == previous.Result.ContainerID && domain.HostingDocumentHash(p.PreviousPlan) == domain.HostingDocumentHash(*previous.Plan) && current.Snapshot == previous.Snapshot && *current.Release == *source.Release && *current.Plan.Storage == *source.Plan.Storage && reflect.DeepEqual(current.Plan.Database.Credentials, source.Plan.Database.Credentials) && n.Members != nil && len(n.Members) == 0 && n.MembershipRevision == 1 && n.OriginHash == receipt.OriginHash && n.NetworkID == receipt.Origin.NetworkID && n.Predecessor.PlanHash == previous.Plan.Hash && current.Result.ContainerID != previous.Result.ContainerID && current.Result.PreviousContainerID == previous.Result.ContainerID
}

func databaseUpgradeCreateInput(engine, environment, destination string) domain.HostingDatabaseCreateInput {
	version, requestID := domain.HostingDatabasePostgresRecipe, strings.Repeat("e", 32)
	if engine == "redis" {
		version, requestID = domain.HostingDatabaseRedisRecipe, strings.Repeat("f", 32)
	}
	return domain.HostingDatabaseCreateInput{RequestID: requestID, ProjectID: "proj_platform", ProjectEnvironmentID: environment, Name: "upgrade physical " + engine, DestinationID: destination, Engine: engine, RecipeVersion: version}
}

func TestHostingDatabaseUpgradeCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("run reviewed three-test compiled database gate after exact physical authorization")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	f := newDatabaseRunnerFixtureWithProfile(t, true, true)
	for index, engine := range []string{"postgresql", "redis"} {
		if index > 0 {
			f.start()
		}
		view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", databaseUpgradeCreateInput(engine, f.environment, f.destination), 201)
		source := f.await(f.deploy(view, "upgrade_source").ID)
		marker := "upgrade_physical_" + engine + "_before"
		f.marker(view, source, marker, true)
		f.awaitSourceJournalRetired()
		f.stop()
		capture, stored, trees := f.captureWithRecovery(source)
		volume := f.volumeProof(source)
		storage := f.upgradeStorageIdentity(source)
		credentials := f.upgradeCredentialIdentity(view)
		sourceDocument := domain.HostingDocumentHash(source)
		firstRequest := f.requestUpgrade(view, capture, stored.Receipt, "upgrade_empty_first")
		f.start()
		first := f.await(firstRequest.ID)
		f.awaitSourceJournalRetired()
		f.stop()
		if !databaseUpgradeMatches(source, source, first, stored.Receipt) || domain.HostingDocumentHash(firstRequest.DatabaseUpgrade) != domain.HostingDocumentHash(first.DatabaseUpgrade) {
			t.Fatal("initial upgrade changed controller proof or predecessor")
		}
		f.upgradeInventory(first)
		if f.volumeProof(first) != volume || f.upgradeStorageIdentity(source) != storage || !reflect.DeepEqual(credentials, f.upgradeCredentialIdentity(view)) {
			t.Fatal("initial upgrade changed retained volume/materialization/credentials")
		}
		f.markerAt(view, first, marker, false, true)
		marker = "upgrade_physical_" + engine + "_after_first"
		f.markerAt(view, first, marker, true, true)
		f.markerAt(view, first, marker, false, true)
		maintenanceRequest := f.requestUpgrade(view, capture, stored.Receipt, "upgrade_empty_maintenance")
		wire := newDatabaseUpgradeCompleteWire(maintenanceRequest.ID)
		if !f.upgradeComplete.CompareAndSwap(nil, wire) {
			t.Fatal("upgrade completion hook already active")
		}
		t.Cleanup(func() {
			f.stop()
			select {
			case <-wire.entered:
				select {
				case <-wire.finished:
				case <-time.After(10 * time.Second):
					t.Error("upgrade completion fault did not join during cleanup")
				}
			default:
			}
		})
		f.start()
		select {
		case <-wire.entered:
		case <-f.process.done:
			t.Fatal("upgrade runner exited before committed completion")
		case <-time.After(180 * time.Second):
			t.Fatal("upgrade committed completion did not enter")
		}
		f.stop()
		select {
		case <-wire.finished:
		case <-time.After(10 * time.Second):
			t.Fatal("upgrade committed completion handler did not join")
		}
		original, committed := wire.original()
		if !committed {
			t.Fatal("upgrade fault did not withhold exactly one committed 200")
		}
		maintenance := f.await(maintenanceRequest.ID)
		if !databaseUpgradeMatches(source, first, maintenance, stored.Receipt) || domain.HostingDocumentHash(maintenanceRequest.DatabaseUpgrade) != domain.HostingDocumentHash(maintenance.DatabaseUpgrade) {
			t.Fatal("maintenance changed exact empty predecessor proof")
		}
		pending := false
		for _, entry := range f.upgradeJournal().Hosting {
			if entry.OperationID == maintenance.ID && entry.Phase == "result" && entry.Database && entry.Result != nil && *entry.Result == original && domain.HostingDocumentHash(entry.DatabaseUpgrade) == domain.HostingDocumentHash(maintenance.DatabaseUpgrade) {
				pending = true
			}
		}
		if !pending {
			t.Fatal("upgrade committed ACK loss did not retain exact durable result and proof")
		}
		beforeInventory := f.upgradeInventory(maintenance)
		beforeTrees := f.captureTrees()
		beforeApply, beforeDelivery := f.applyRequests.Load(), f.deliveryRequests.Load()
		f.start()
		select {
		case <-wire.replayed:
		case <-f.process.done:
			t.Fatal("upgrade replay process exited before historical ACK")
		case <-time.After(30 * time.Second):
			t.Fatal("upgrade restart did not replay exact Complete")
		}
		f.awaitSourceJournalRetired()
		f.stop()
		replayed, err := f.operation(t.Context(), maintenance.ID)
		if err != nil || domain.HostingDocumentHash(replayed) != domain.HostingDocumentHash(maintenance) || !wire.exactReplay() {
			t.Fatal("upgrade restart changed receipt/result or historical wire bytes")
		}
		if f.applyRequests.Load() != beforeApply || f.deliveryRequests.Load() != beforeDelivery || !reflect.DeepEqual(beforeTrees, f.captureTrees()) {
			t.Fatal("upgrade replay repeated effects, credentials or protected-file writes")
		}
		afterInventory := f.upgradeInventory(replayed)
		if databaseRunnerReplayInventoryMatches(beforeInventory, afterInventory, []domain.HostingOperation{replayed}, replayed) != nil {
			t.Fatal("upgrade replay changed all-state writer or volume identities")
		}
		for _, entry := range f.upgradeJournal().Hosting {
			if entry.OperationID == maintenance.ID {
				t.Fatal("upgrade historical ACK failed durable retirement")
			}
		}
		if !f.upgradeComplete.CompareAndSwap(wire, nil) {
			t.Fatal("upgrade completion hook identity changed")
		}
		if f.volumeProof(replayed) != volume || f.upgradeStorageIdentity(source) != storage || !reflect.DeepEqual(credentials, f.upgradeCredentialIdentity(view)) || domain.HostingDocumentHash(stored) != domain.HostingDocumentHash(f.captureStored(capture.ID)) {
			t.Fatal("maintenance/replay changed original retained evidence")
		}
		retained := f.captureTrees()
		if !reflect.DeepEqual(trees["credential"], retained["credential"]) || trees["state"][source.Plan.Database.NetworkName+".json"] != retained["state"][source.Plan.Database.NetworkName+".json"] {
			t.Fatal("upgrade changed original credential or initialized network record")
		}
		f.markerAt(view, replayed, marker, false, true)
		marker = "upgrade_physical_" + engine + "_after_maintenance"
		f.markerAt(view, replayed, marker, true, true)
		f.markerAt(view, replayed, marker, false, true)
		f.upgradeInventory(replayed)
		afterSource, err := f.operation(t.Context(), source.ID)
		if err != nil || domain.HostingDocumentHash(afterSource) != sourceDocument {
			t.Fatal("upgrade changed historical source operation/result")
		}
		f.assertRedacted()
		output := f.logs.snapshot()
		if bytes.Contains(output, []byte(original.Fence)) || bytes.Contains(output, []byte(original.LeaseID)) {
			t.Fatal("upgrade output exposed retained attempt authority")
		}
		clear(output)
		t.Logf("physical empty upgrade engine=%s first=true maintenance=true retained_origin=true alias_read_write=true exact_complete_replay=true single_writer=true", engine)
	}
}

func TestDatabaseUpgradeRuntimeArgumentsAndAliasTarget(t *testing.T) {
	f := &databaseRunnerFixture{root: "/fixture/owned", server: &httptest.Server{URL: "https://fixture.invalid"}, upgrade: true}
	if !slices.Contains(databaseRunnerArguments(f), "--hosting-database-network-upgrade") || slices.Contains(databaseCaptureArguments(f, "hnc_"+strings.Repeat("a", 32), "unix:///fixture/absent.sock"), "--hosting-database-network-upgrade") {
		t.Fatal("fixture mixed explicit upgrade and exact capture execution modes")
	}
	f.upgrade = false
	if slices.Contains(databaseRunnerArguments(f), "--hosting-database-network-upgrade") {
		t.Fatal("ordinary fixture silently enabled upgrade")
	}
	op := domain.HostingOperation{BindingID: "binding_fixture", Plan: &domain.HostingApplyPlan{Database: &domain.HostingDatabasePlan{Network: &domain.HostingDatabaseNetworkPlan{Alias: domain.HostingDatabaseBindingAlias("binding_fixture")}}}}
	host, address, err := databaseUpgradeClientEndpoint(op, "172.19.0.4")
	if err != nil || host != op.Plan.Database.Network.Alias || address != "172.19.0.4" {
		t.Fatal("alias client lost inspected endpoint")
	}
	for _, ip := range []string{"127.0.0.1", "8.8.8.8", "172.019.0.4", "private-canary", "::1"} {
		if _, _, err := databaseUpgradeClientEndpoint(op, ip); err == nil || strings.Contains(err.Error(), ip) {
			t.Fatal("alias endpoint accepted unqualified address or exposed input")
		}
	}
	op.Plan.Database.Network.Alias = "private-canary"
	if _, _, err := databaseUpgradeClientEndpoint(op, "172.19.0.4"); err == nil {
		t.Fatal("alias endpoint accepted foreign binding")
	}
}

func TestDatabaseUpgradeRuntimeCompleteFaultAndReplay(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			wire := newDatabaseUpgradeCompleteWire("hop_fixture")
			request := domain.HostingCompleteRequest{HostingAuthority: domain.HostingAuthority{OperationID: "hop_fixture"}, ReceiptID: "receipt_fixture"}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal("wire fixture encoding failed")
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"id":"private-canary"}`)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			rec := httptest.NewRecorder()
			go func() {
				defer close(done)
				wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/hosting/complete", bytes.NewReader(raw)).WithContext(ctx), handler)
			}()
			if status == http.StatusOK {
				select {
				case <-wire.entered:
				case <-time.After(time.Second):
					t.Fatal("committed fault did not enter")
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("fault handler did not join")
			}
			if status != http.StatusOK {
				if rec.Code != status || rec.Header().Get("Cache-Control") != "no-store" || rec.Body.String() != `{"id":"private-canary"}` {
					t.Fatal("noncommitted response changed")
				}
				if _, ok := wire.original(); ok {
					t.Fatal("server rejection qualified as committed ACK loss")
				}
				return
			}
			if _, ok := wire.original(); !ok {
				t.Fatal("committed fault missing original request")
			}
			replay := httptest.NewRecorder()
			if !wire.serve(replay, httptest.NewRequest("POST", "/api/v1/runners/hosting/complete", bytes.NewReader(raw)), handler) || replay.Code != http.StatusOK || !wire.exactReplay() {
				t.Fatal("wire failed exact historical replay")
			}
		})
	}
}

func TestDatabaseUpgradeRuntimeCompleteForwardingAndBounds(t *testing.T) {
	wire := newDatabaseUpgradeCompleteWire("hop_expected")
	body := `{"operation_id":"hop_foreign","run_id":"run_private_canary","lease_id":"lease_private_canary","attempt":1,"fence":"fence_private_canary","receipt_id":"receipt_fixture","result":{"outcome":"succeeded","ready":true,"restored":false,"uncertain":false}}`
	r := httptest.NewRequest("POST", "/api/v1/runners/hosting/complete", strings.NewReader(body))
	called := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	if wire.serve(httptest.NewRecorder(), r, handler) || called {
		t.Fatal("completion hook intercepted a different operation")
	}
	forwarded, err := io.ReadAll(r.Body)
	if err != nil || string(forwarded) != body {
		t.Fatal("completion hook changed the forwarded original body")
	}
	for _, bad := range []string{strings.Repeat("x", 8193), `{"operation_id":"hop_expected","operation_id":"hop_expected"}`, `{"private-canary":null}`} {
		rec := httptest.NewRecorder()
		if !wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/hosting/complete", strings.NewReader(bad)), handler) || rec.Code != http.StatusBadRequest || rec.Body.Len() != 0 || called {
			t.Fatal("completion hook forwarded malformed or oversized authority")
		}
	}
	if _, ok := wire.original(); ok {
		t.Fatal("malformed request qualified as committed ACK loss")
	}
}

func TestDatabaseUpgradeRuntimeFixtureInputs(t *testing.T) {
	seen := map[string]bool{}
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			input := databaseUpgradeCreateInput(engine, "penv_fixture", "dest_fixture")
			if input.Validate() != nil {
				t.Fatal("generated database-create fixture input rejected by actual validator")
			}
			for _, reserved := range []string{"a", "b", "c", "d"} {
				if input.RequestID == strings.Repeat(reserved, 32) {
					t.Fatal("upgrade request identity overlaps existing compiled fixtures")
				}
			}
			resource := domain.HostingDatabaseResourceID(input.ProjectID, input.RequestID)
			if seen[resource] {
				t.Fatal("database-create fixture identities collide")
			}
			seen[resource] = true
			raw, err := json.Marshal(input)
			var decoded domain.HostingDatabaseCreateInput
			if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != input {
				t.Fatal("generated database-create fixture input rejected by strict wire decoder")
			}
			if (domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "upgrade_source"}).Validate() != nil {
				t.Fatal("fixture source deployment identity invalid")
			}
			for _, key := range []string{"upgrade_empty_first", "upgrade_empty_maintenance"} {
				upgrade := domain.HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: domain.HostingDatabaseNetworkCaptureID("hop_upgrade_source"), ExpectedOriginHash: domain.HostingDocumentHash("fixture_origin"), IdempotencyKey: key}
				if upgrade.Validate() != nil {
					t.Fatal("fixture upgrade request identity invalid")
				}
			}
		})
	}
}
