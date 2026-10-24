package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"nerocd/internal/store/sqlcgen"
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

// Lease IDs are public correlation fields in printRunnerAuthorityEvent. The
// retained fence remains private, including inside an otherwise valid event or
// full authority tuple. Existing credential scans remain independently active.
func databaseUpgradeAuthorityLogCheck(output []byte, authority domain.HostingAuthority, protected [][]byte) error {
	if authority.LeaseID == "" || authority.Fence == "" {
		return errors.New("upgrade retained authority incomplete")
	}
	if bytes.Contains(output, []byte(authority.Fence)) || bytes.Contains(output, []byte(base64.StdEncoding.EncodeToString([]byte(authority.Fence)))) {
		return errors.New("upgrade output exposed retained fence")
	}
	for _, value := range protected {
		if len(value) > 0 && (bytes.Contains(output, value) || bytes.Contains(output, []byte(base64.StdEncoding.EncodeToString(value)))) {
			return errors.New("upgrade output exposed protected credential")
		}
	}
	return nil
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

// The original materialization pointer is stable. Later deployments each add
// their own immutable inspection receipt without becoming the creation origin.
type databaseUpgradeStorageReceipt struct {
	Receipt   sqlcgen.HostingStorageMaterializedReceipt
	Admission sqlcgen.HostingStorageOperationAdmission
	Authority domain.HostingAuthority
	RunnerID  string
}

type databaseUpgradeStorageSnapshot struct {
	Origin   sqlcgen.HostingStorageMaterialization
	Receipts []databaseUpgradeStorageReceipt
}

func databaseUpgradeStorageContract(c domain.HostingDatabaseNetworkCapturePlan, expected []domain.HostingOperation, snapshot databaseUpgradeStorageSnapshot) error {
	invalid := errors.New("upgrade storage receipt contract invalid")
	origin := snapshot.Origin
	m := c.Materialization
	if c.Storage.Validate() != nil || m.Validate(c.Storage) != nil || m.State != "materialized" || m.MaterializedReceipt == nil || origin.StorageID != c.Storage.ID || origin.OriginOperationID != m.CreationOperationID || origin.OriginPlanHash != m.CreationPlanHash || origin.MaterializedOperationID == nil || *origin.MaterializedOperationID != m.CreationOperationID || origin.MaterializedReceiptID == nil || *origin.MaterializedReceiptID != m.MaterializedReceipt.ID || origin.CreatedAt.IsZero() {
		return invalid
	}
	var admitted domain.HostingStorageMaterialization
	if domain.DecodeHostingStorageProtocolJSON(origin.Origin, &admitted) != nil || admitted.State != "create_admitted" || admitted.MaterializedReceipt != nil || domain.HostingDocumentHash(admitted) != origin.OriginHash || admitted.Validate(c.Storage) != nil {
		return invalid
	}
	admitted.State, admitted.MaterializedReceipt = "materialized", m.MaterializedReceipt
	if domain.HostingDocumentHash(admitted) != domain.HostingDocumentHash(m) || len(expected) < 1 || len(expected) > 3 || len(snapshot.Receipts) != len(expected) {
		return invalid
	}
	operations := make(map[string]domain.HostingOperation, len(expected))
	for _, op := range expected {
		if _, duplicate := operations[op.ID]; duplicate || op.ID == "" || op.RunID == "" || op.Plan == nil || op.Plan.Storage == nil || op.ProjectID != c.Storage.ProjectID || op.ResourceID != c.Storage.ResourceID || op.BindingID != c.Storage.BindingID || op.Snapshot.RunnerID != c.RunnerID || op.Snapshot.Storage != m.StorageRef || op.Plan.Storage.Reference != m.StorageRef {
			return invalid
		}
		operations[op.ID] = op
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range snapshot.Receipts {
		r, a, authority := entry.Receipt, entry.Admission, entry.Authority
		op, found := operations[r.OperationID]
		var body domain.HostingStorageMaterializedRequest
		var receiptAuthority, admissionAuthority domain.HostingAuthority
		if !found || seen[r.OperationID] || r.StorageID != c.Storage.ID || a.StorageID != r.StorageID || a.OperationID != op.ID || a.PlanHash != op.Plan.Hash || authority.OperationID != op.ID || authority.RunID != op.RunID || authority.LeaseID == "" || authority.Fence == "" || authority.Attempt < 1 || entry.RunnerID != c.RunnerID || r.CreatedAt.IsZero() || a.CreatedAt.IsZero() || r.CreatedAt.Before(a.CreatedAt) || r.AuditID != "aud_storage_materialized_"+op.ID || domain.DecodeHostingStorageProtocolJSON(r.Authority, &receiptAuthority) != nil || domain.DecodeHostingStorageProtocolJSON(a.Authority, &admissionAuthority) != nil || receiptAuthority != authority || admissionAuthority != authority || domain.DecodeHostingStorageProtocolJSON(r.Body, &body) != nil || body.HostingAuthority != authority || body.ReceiptID != r.ReceiptID || body.PlanHash != op.Plan.Hash || body.Matches(c.Storage, m) != nil || r.BodyHash != domain.HostingDocumentHash(body) {
			return invalid
		}
		if r.OperationID == m.CreationOperationID && domain.HostingDocumentHash(domain.HostingReceipt{ID: r.ReceiptID, BodyHash: r.BodyHash, CreatedAt: r.CreatedAt}) != domain.HostingDocumentHash(*m.MaterializedReceipt) {
			return invalid
		}
		seen[r.OperationID] = true
	}
	if !seen[m.CreationOperationID] {
		return invalid
	}
	return nil
}

// A later operation may add its own receipt, but can never rewrite the original
// row, source receipt, admission or any already committed inspection receipt.
func databaseUpgradeStoragePreserved(before, after databaseUpgradeStorageSnapshot) bool {
	if domain.HostingDocumentHash(before.Origin) != domain.HostingDocumentHash(after.Origin) || len(after.Receipts) < len(before.Receipts) {
		return false
	}
	retained := make(map[string]string, len(after.Receipts))
	for _, entry := range after.Receipts {
		id := entry.Receipt.OperationID
		if _, duplicate := retained[id]; duplicate {
			return false
		}
		retained[id] = domain.HostingDocumentHash(entry)
	}
	for _, entry := range before.Receipts {
		if retained[entry.Receipt.OperationID] != domain.HostingDocumentHash(entry) {
			return false
		}
	}
	return true
}

func (f *databaseRunnerFixture) upgradeStorageIdentity(c domain.HostingDatabaseNetworkCapturePlan, expected []domain.HostingOperation) databaseUpgradeStorageSnapshot {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var snapshot databaseUpgradeStorageSnapshot
	var raw []byte
	if f.database.QueryRow(ctx, `SELECT to_jsonb(m)::text FROM hosting_storage_materializations m WHERE storage_id=$1`, c.Storage.ID).Scan(&raw) != nil || len(raw) > domain.HostingStorageProtocolJSONLimit || json.Unmarshal(raw, &snapshot.Origin) != nil {
		f.t.Fatal("upgrade original materialization unavailable")
	}
	// Each row is one operation's inspection and admission. Joining the exact
	// recorded lease pins its full authority independently of the supplied body.
	rows, err := f.database.Query(ctx, `SELECT to_jsonb(r)::text,to_jsonb(a)::text,jsonb_build_object('operation_id',r.operation_id,'run_id',l.run_id,'lease_id',l.id,'attempt',l.attempt,'fence',l.fence)::text,l.runner_id FROM hosting_storage_materialized_receipts r LEFT JOIN hosting_storage_operation_admissions a ON a.operation_id=r.operation_id AND a.storage_id=r.storage_id LEFT JOIN run_leases l ON l.id=r.authority->>'lease_id' WHERE r.storage_id=$1 ORDER BY r.operation_id`, c.Storage.ID)
	if err != nil {
		f.t.Fatal("upgrade materialization receipts unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var receipt, admission, authority []byte
		var entry databaseUpgradeStorageReceipt
		if len(snapshot.Receipts) >= 3 || rows.Scan(&receipt, &admission, &authority, &entry.RunnerID) != nil || len(receipt) > domain.HostingStorageProtocolJSONLimit || len(admission) > domain.HostingStorageProtocolJSONLimit || len(authority) > 2048 || json.Unmarshal(receipt, &entry.Receipt) != nil || json.Unmarshal(admission, &entry.Admission) != nil || domain.DecodeHostingStorageProtocolJSON(authority, &entry.Authority) != nil {
			f.t.Fatal("upgrade materialization receipt set invalid")
		}
		snapshot.Receipts = append(snapshot.Receipts, entry)
	}
	if rows.Err() != nil || databaseUpgradeStorageContract(c, expected, snapshot) != nil {
		f.t.Fatal("upgrade materialization receipt contract invalid")
	}
	return snapshot
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
		storage := f.upgradeStorageIdentity(capture.Plan, []domain.HostingOperation{source})
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
		firstStorage := f.upgradeStorageIdentity(capture.Plan, []domain.HostingOperation{source, first})
		if f.volumeProof(first) != volume || !databaseUpgradeStoragePreserved(storage, firstStorage) || !reflect.DeepEqual(credentials, f.upgradeCredentialIdentity(view)) {
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
		maintenanceStorage := f.upgradeStorageIdentity(capture.Plan, []domain.HostingOperation{source, first, maintenance})
		if !databaseUpgradeStoragePreserved(firstStorage, maintenanceStorage) {
			t.Fatal("maintenance rewrote original or earlier inspection receipts")
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
		replayedStorage := f.upgradeStorageIdentity(capture.Plan, []domain.HostingOperation{source, first, replayed})
		if domain.HostingDocumentHash(maintenanceStorage) != domain.HostingDocumentHash(replayedStorage) {
			t.Fatal("Complete replay added or rewrote materialization receipts")
		}
		if f.volumeProof(replayed) != volume || !databaseUpgradeStoragePreserved(storage, replayedStorage) || !reflect.DeepEqual(credentials, f.upgradeCredentialIdentity(view)) || domain.HostingDocumentHash(stored) != domain.HostingDocumentHash(f.captureStored(capture.ID)) {
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
		if err := databaseUpgradeAuthorityLogCheck(output, original.HostingAuthority, f.protected); err != nil {
			t.Fatal(err)
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

func databaseUpgradeReceiptJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("storage receipt fixture encoding failed")
	}
	return raw
}

// These are local contract documents, not physical evidence or retained data
// from either failed run. Each observation must pass the production validator.
func databaseUpgradeStorageContractFixture(t *testing.T, engine string) (domain.HostingDatabaseNetworkCapturePlan, []domain.HostingOperation, databaseUpgradeStorageSnapshot) {
	t.Helper()
	recipe := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		recipe = domain.HostingDatabaseRedisRecipe
	}
	r, err := domain.HostingDatabaseRecipeFor(engine, recipe)
	if err != nil {
		t.Fatal("receipt fixture recipe unavailable")
	}
	id, volume := domain.HostingStorageIdentity("binding_upgrade_receipts")
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	storage := domain.HostingStorage{ID: id, ProjectID: "proj_platform", ResourceID: "resource_receipts", BindingID: "binding_upgrade_receipts", DestinationID: "destination_receipts", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: r.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner_fixture", CreatedAt: now}
	storage.Hash = storage.DocumentHash()
	m := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: "hop_source", CreationPlanHash: domain.HostingDocumentHash("hop_source"), DaemonID: "daemon_receipts"}
	m.CreationNonce = domain.HostingStorageCreationNonce(m.StorageRef, m.CreationOperationID, m.DaemonID)
	m.Labels = domain.ExpectedHostingStorageLabels(storage, m)
	admitted := m
	c := domain.HostingDatabaseNetworkCapturePlan{Storage: storage, RunnerID: "runner_receipts", SourceOperationID: m.CreationOperationID}
	var operations []domain.HostingOperation
	var snapshot databaseUpgradeStorageSnapshot
	for index, name := range []string{"hop_source", "hop_first", "hop_maintenance"} {
		op := domain.HostingOperation{ID: name, RunID: "run_" + name, ProjectID: storage.ProjectID, ResourceID: storage.ResourceID, BindingID: storage.BindingID, Snapshot: domain.HostingSnapshot{Storage: storage.Reference(), RunnerID: c.RunnerID}, Plan: &domain.HostingApplyPlan{Hash: domain.HostingDocumentHash(name), Storage: &domain.HostingStoragePlan{Reference: storage.Reference()}}}
		a := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease_" + name, Attempt: 1, Fence: "fence_" + name}
		body := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "receipt_" + name, PlanHash: op.Plan.Hash, StorageRef: m.StorageRef, CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}
		if body.Validate() != nil || body.Matches(storage, m) != nil {
			t.Fatal("inspection receipt failed actual domain contract")
		}
		created := now.Add(time.Duration(index+1) * time.Second)
		entry := databaseUpgradeStorageReceipt{Receipt: sqlcgen.HostingStorageMaterializedReceipt{OperationID: name, StorageID: id, ReceiptID: body.ReceiptID, Authority: databaseUpgradeReceiptJSON(t, a), Body: databaseUpgradeReceiptJSON(t, body), BodyHash: domain.HostingDocumentHash(body), AuditID: "aud_storage_materialized_" + name, CreatedAt: created}, Admission: sqlcgen.HostingStorageOperationAdmission{OperationID: name, StorageID: id, PlanHash: op.Plan.Hash, Authority: databaseUpgradeReceiptJSON(t, a), CreatedAt: now}, Authority: a, RunnerID: c.RunnerID}
		if index == 0 {
			m.State = "materialized"
			m.MaterializedReceipt = &domain.HostingReceipt{ID: entry.Receipt.ReceiptID, BodyHash: entry.Receipt.BodyHash, CreatedAt: created}
		}
		operations = append(operations, op)
		snapshot.Receipts = append(snapshot.Receipts, entry)
	}
	c.Materialization = m
	snapshot.Origin = sqlcgen.HostingStorageMaterialization{StorageID: id, OriginOperationID: m.CreationOperationID, OriginPlanHash: m.CreationPlanHash, Origin: databaseUpgradeReceiptJSON(t, admitted), OriginHash: domain.HostingDocumentHash(admitted), MaterializedOperationID: &m.CreationOperationID, MaterializedReceiptID: &m.MaterializedReceipt.ID, CreatedAt: now}
	return c, operations, snapshot
}

func TestDatabaseUpgradeRuntimeStorageInspectionReceipts(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			c, ops, all := databaseUpgradeStorageContractFixture(t, engine)
			before := all
			before.Receipts = slices.Clone(all.Receipts[:1])
			for count := 1; count <= 3; count++ {
				after := all
				after.Receipts = slices.Clone(all.Receipts[:count])
				if databaseUpgradeStorageContract(c, ops[:count], after) != nil || !databaseUpgradeStoragePreserved(before, after) {
					t.Fatal("legitimate per-operation inspection receipt changed original creation identity")
				}
				before = after
			}
			// A byte-identical replay preserves the exact bounded set, including every
			// receipt, admission, audit identity, timestamp and independent lease tuple.
			var replay databaseUpgradeStorageSnapshot
			if json.Unmarshal(databaseUpgradeReceiptJSON(t, all), &replay) != nil || databaseUpgradeStorageContract(c, ops, replay) != nil || !databaseUpgradeStoragePreserved(all, replay) || domain.HostingDocumentHash(all) != domain.HostingDocumentHash(replay) {
				t.Fatal("identical inspection replay changed retained evidence")
			}
			for name, change := range map[string]func(*databaseUpgradeStorageSnapshot){
				"original_hash":    func(s *databaseUpgradeStorageSnapshot) { s.Origin.OriginHash = domain.HostingDocumentHash("foreign") },
				"original_pointer": func(s *databaseUpgradeStorageSnapshot) { s.Origin.MaterializedOperationID = &ops[1].ID },
				"source_receipt": func(s *databaseUpgradeStorageSnapshot) {
					s.Receipts[0].Receipt.BodyHash = domain.HostingDocumentHash("rewritten")
				},
				"unexpected_operation": func(s *databaseUpgradeStorageSnapshot) { s.Receipts[1].Receipt.OperationID = "hop_unexpected" },
				"missing_receipt":      func(s *databaseUpgradeStorageSnapshot) { s.Receipts = s.Receipts[:2] },
				"duplicate_receipt":    func(s *databaseUpgradeStorageSnapshot) { s.Receipts[2] = s.Receipts[1] },
				"plan": func(s *databaseUpgradeStorageSnapshot) {
					s.Receipts[1].Admission.PlanHash = domain.HostingDocumentHash("foreign")
				},
				"authority": func(s *databaseUpgradeStorageSnapshot) { s.Receipts[1].Authority.Fence = "private_authority_canary" },
				"runner":    func(s *databaseUpgradeStorageSnapshot) { s.Receipts[1].RunnerID = "private_authority_canary" },
				"body_hash": func(s *databaseUpgradeStorageSnapshot) {
					s.Receipts[1].Receipt.BodyHash = domain.HostingDocumentHash("foreign")
				},
				"audit": func(s *databaseUpgradeStorageSnapshot) { s.Receipts[1].Receipt.AuditID = "private_authority_canary" },
			} {
				t.Run(name, func(t *testing.T) {
					var changed databaseUpgradeStorageSnapshot
					if json.Unmarshal(databaseUpgradeReceiptJSON(t, all), &changed) != nil {
						t.Fatal("receipt fixture clone failed")
					}
					change(&changed)
					err := databaseUpgradeStorageContract(c, ops, changed)
					if err == nil || strings.Contains(err.Error(), "private_authority_canary") {
						t.Fatal("changed or unexpected inspection evidence accepted or disclosed")
					}
				})
			}
			// Even a shape-valid rewrite is forbidden once a receipt or original row
			// has been observed. Merely revalidating hashes would miss these changes.
			for _, change := range []func(*databaseUpgradeStorageSnapshot){
				func(s *databaseUpgradeStorageSnapshot) { s.Origin.CreatedAt = s.Origin.CreatedAt.Add(time.Nanosecond) },
				func(s *databaseUpgradeStorageSnapshot) {
					s.Receipts[1].Receipt.CreatedAt = s.Receipts[1].Receipt.CreatedAt.Add(time.Nanosecond)
				},
				func(s *databaseUpgradeStorageSnapshot) {
					entry := &s.Receipts[1]
					var body domain.HostingStorageMaterializedRequest
					if json.Unmarshal(entry.Receipt.Body, &body) != nil {
						t.Fatal("receipt body clone failed")
					}
					body.ReceiptID = "receipt_rewritten"
					entry.Receipt.ReceiptID = body.ReceiptID
					entry.Receipt.Body = databaseUpgradeReceiptJSON(t, body)
					entry.Receipt.BodyHash = domain.HostingDocumentHash(body)
				},
			} {
				var changed databaseUpgradeStorageSnapshot
				if json.Unmarshal(databaseUpgradeReceiptJSON(t, all), &changed) != nil {
					t.Fatal("receipt fixture clone failed")
				}
				change(&changed)
				if databaseUpgradeStorageContract(c, ops, changed) != nil || databaseUpgradeStoragePreserved(all, changed) {
					t.Fatal("shape-valid immutable evidence rewrite escaped snapshot comparison")
				}
			}
		})
	}
}

func TestDatabaseUpgradeRuntimeAuthorityLogContract(t *testing.T) {
	authority := domain.HostingAuthority{OperationID: "hop_log_fixture", RunID: "run_log_fixture", LeaseID: "lease_log_correlation", Attempt: 1, Fence: "fence_private_canary"}
	credential := []byte("ncr_credential_private_canary")
	// Match the closed event fields of printRunnerAuthorityEvent. Its public
	// lease correlation is distinct from a full fenced authority document.
	claimed := databaseUpgradeReceiptJSON(t, struct {
		Event   string `json:"event"`
		RunID   string `json:"run_id"`
		LeaseID string `json:"lease_id"`
		Attempt int    `json:"attempt"`
		Status  string `json:"status,omitempty"`
	}{"claimed_run", authority.RunID, authority.LeaseID, authority.Attempt, "active"})
	for name, tc := range map[string]struct {
		output  []byte
		missing string
		want    string
	}{
		"public_claimed_run": {output: claimed},
		"fence":              {output: []byte(authority.Fence), want: "upgrade output exposed retained fence"},
		"encoded_fence":      {output: []byte(base64.StdEncoding.EncodeToString([]byte(authority.Fence))), want: "upgrade output exposed retained fence"},
		"full_authority":     {output: databaseUpgradeReceiptJSON(t, authority), want: "upgrade output exposed retained fence"},
		"credential":         {output: append(bytes.Clone(claimed), credential...), want: "upgrade output exposed protected credential"},
		"encoded_credential": {output: []byte(base64.StdEncoding.EncodeToString(credential)), want: "upgrade output exposed protected credential"},
		"missing_lease":      {output: claimed, missing: "lease", want: "upgrade retained authority incomplete"},
		"missing_fence":      {output: claimed, missing: "fence", want: "upgrade retained authority incomplete"},
		"missing_authority":  {output: nil, missing: "all", want: "upgrade retained authority incomplete"},
	} {
		t.Run(name, func(t *testing.T) {
			a := authority
			switch tc.missing {
			case "lease":
				a.LeaseID = ""
			case "fence":
				a.Fence = ""
			case "all":
				a = domain.HostingAuthority{}
			}
			err := databaseUpgradeAuthorityLogCheck(tc.output, a, [][]byte{credential})
			if tc.want == "" {
				if err != nil {
					t.Fatal("supported public correlation event rejected")
				}
				return
			}
			if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), authority.Fence) || strings.Contains(err.Error(), string(credential)) || strings.Contains(err.Error(), authority.LeaseID) {
				t.Fatal("private exposure or missing authority did not fail with a closed diagnostic")
			}
		})
	}
}
