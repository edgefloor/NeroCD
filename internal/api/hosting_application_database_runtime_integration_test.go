package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func applicationDatabaseFixtureImage(reference string) error {
	repository, digest, ok := strings.Cut(reference, "@")
	host, name, present := strings.Cut(repository, "/")
	address, port, err := net.SplitHostPort(host)
	n, portErr := strconv.Atoi(port)
	if !ok || !present || address != "127.0.0.1" || err != nil || portErr != nil || n < 1024 || n > 65535 || name != "nerocd-http-app" || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") || strings.Trim(digest[7:], "0123456789abcdef") != "" || domain.ValidateImageReference(reference) != nil {
		return errors.New("HTTP fixture image must be the owned loopback registry digest")
	}
	return nil
}

func applicationDatabaseCreateInput(engine, environment, destination string) domain.HostingDatabaseCreateInput {
	input := databaseUpgradeCreateInput(engine, environment, destination)
	input.RequestID = strings.Repeat("01", 16)
	if engine == "redis" {
		input.RequestID = strings.Repeat("02", 16)
	}
	input.Name = "connected physical " + engine
	return input
}

// Complete hashes the immutable result body. The original authority and entire
// request remain independently pinned by the pending journal and wire replay.
func applicationDatabaseCompleteReceiptMatches(op domain.HostingOperation, original domain.HostingCompleteRequest) bool {
	return op.Result != nil && *op.Result == original.Result && op.CompleteReceipt != nil && op.CompleteReceipt.ID == original.ReceiptID && op.CompleteReceipt.BodyHash == domain.HostingDocumentHash(original.Result)
}

func applicationDatabaseCompleted(op domain.HostingOperation) bool {
	if op.State != "succeeded" || op.Plan == nil || op.Release == nil || op.Result == nil || !op.Result.Ready || op.Result.Uncertain || op.Result.ContainerID == "" || op.CompleteReceipt == nil || op.MutationHold || op.RestoreOwnershipHeld {
		return false
	}
	if op.Result.PlanHash != op.Plan.Hash || op.Result.ImageID != op.Release.ImageID || op.Result.DaemonID != op.Release.DaemonID {
		return false
	}
	if op.ApplicationDatabase != nil {
		return domain.ValidateHostingApplicationDatabaseOperation(op) == nil
	}
	return op.DatabaseMaintenance != nil && domain.ValidateHostingDatabaseMaintenanceOperation(op) == nil
}

func (f *databaseRunnerFixture) awaitConnectionOperation(id string) domain.HostingOperation {
	f.t.Helper()
	op, reason := waitDatabaseRunnerOperation(f.t.Context(), f.process, func(ctx context.Context) (domain.HostingOperation, error) { return f.operation(ctx, id) })
	if reason != "" || !applicationDatabaseCompleted(op) {
		f.t.Fatalf("connected operation lacks settlement reason=%s %s", reason, databaseRunnerDiagnostic(op, f.lastExit, &f.apiCounts))
	}
	return op
}

func (f *databaseRunnerFixture) connection(resource string) domain.HostingDatabaseConnection {
	return databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "GET", "/api/v1/resources/"+resource+"/database-connection", nil, 200)
}

func (f *databaseRunnerFixture) activeConnection(resource string) domain.HostingDatabaseConnection {
	f.t.Helper()
	deadline := time.NewTimer(180 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		c := f.connection(resource)
		if c.State == "active" && c.HoldOperationID == "" && c.MembershipRevision >= 2 {
			return c
		}
		if c.State == "held" || f.process.status().Exited {
			f.t.Fatal("connected workflow held or runner exited")
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("connected wait canceled")
		case <-deadline.C:
			f.t.Fatalf("connected workflow timed out state=%s reason=%s", c.State, c.ReasonCode)
		case <-tick.C:
		}
	}
}

// Inspect only public identity/network fields. Runtime secret environment values
// are deliberately excluded from the Docker response and fixture diagnostics.
type applicationDatabaseInspection struct {
	Container databaseRunnerContainer
	Endpoints map[string]struct{ NetworkID, EndpointID, IPAddress, MacAddress string }
	ProcessID int
	StartedAt string
}

func (f *databaseRunnerFixture) applicationContainer(op domain.HostingOperation) applicationDatabaseInspection {
	f.t.Helper()
	if op.ApplicationDatabase == nil || !applicationDatabaseCompleted(op) {
		f.t.Fatal("app inspection requires settled exact family evidence")
	}
	const format = `{"Id":{{json .Id}},"Image":{{json .Image}},"Config":{"Labels":{{json .Config.Labels}}},"State":{{json .State}},"HostConfig":{"NetworkMode":{{json .HostConfig.NetworkMode}},"PortBindings":{{json .HostConfig.PortBindings}},"Privileged":{{json .HostConfig.Privileged}}},"Mounts":{{json .Mounts}},"NetworkSettings":{{json .NetworkSettings}}}`
	var c databaseRunnerContainer
	rawInspection := f.docker("container", "inspect", op.Result.ContainerID, "--format", format)
	if json.Unmarshal(rawInspection, &c) != nil || c.ID != op.Result.ContainerID || c.Image != op.Release.ImageID || !c.State.Running || c.Config.Labels["io.nerocd.binding"] != op.BindingID || c.Config.Labels["io.nerocd.operation"] != op.ID || c.Config.Labels["io.nerocd.plan"] != op.Plan.Hash || c.Config.Labels["io.nerocd.release"] != op.Release.Hash || c.HostConfig.Privileged || len(c.Mounts) != 0 || len(c.NetworkSettings.Networks) != 2 {
		f.t.Fatal("connected app live identity mismatch")
	}
	private, ok := c.NetworkSettings.Networks[op.ApplicationDatabase.Binding.NetworkName]
	if !ok || private.NetworkID != op.ApplicationDatabase.Origin.NetworkID || private.IPAddress == "" || c.NetworkSettings.Networks["bridge"].NetworkID == "" {
		f.t.Fatal("connected app private/default network mismatch")
	}
	var bindings map[string][]struct{ HostIP, HostPort string }
	raw, _ := json.Marshal(c.HostConfig.PortBindings)
	if json.Unmarshal(raw, &bindings) != nil || len(bindings) != 1 || len(bindings["8080/tcp"]) != 1 || bindings["8080/tcp"][0].HostIP != "127.0.0.1" || bindings["8080/tcp"][0].HostPort != strconv.Itoa(op.Snapshot.HostPort) {
		f.t.Fatal("connected app is not the exact loopback HTTP endpoint")
	}
	var detail struct {
		State struct {
			Pid       int
			StartedAt string
		}
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID, EndpointID, IPAddress, MacAddress string }
		}
	}
	// This second decode uses the same selected nonsecret inspection document.
	if json.Unmarshal(rawInspection, &detail) != nil || detail.State.Pid < 1 || detail.State.StartedAt == "" {
		f.t.Fatal("app endpoint identity invalid")
	}
	for _, endpoint := range detail.NetworkSettings.Networks {
		if endpoint.EndpointID == "" {
			f.t.Fatal("app endpoint identity missing")
		}
	}
	return applicationDatabaseInspection{Container: c, Endpoints: detail.NetworkSettings.Networks, ProcessID: detail.State.Pid, StartedAt: detail.State.StartedAt}
}

func (f *databaseRunnerFixture) appRecord(op domain.HostingOperation, method, key, value string) {
	f.t.Helper()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(f.t.Context(), method, "http://127.0.0.1:"+strconv.Itoa(op.Snapshot.HostPort)+"/records/"+key, strings.NewReader(value))
	if err != nil {
		f.t.Fatal("HTTP fixture request invalid")
	}
	response, err := client.Do(req)
	if err != nil {
		f.t.Fatal("running application HTTP request failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	defer clear(raw)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != 200 || len(raw) > 4096 || string(raw) != value {
		f.t.Fatalf("application read/write failed method=%s key=%s status=%d", method, key, response.StatusCode)
	}
}

func (f *databaseRunnerFixture) exactMembers(application, database domain.HostingOperation) {
	f.t.Helper()
	for _, op := range []domain.HostingOperation{application, database} {
		ids := strings.Fields(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+op.BindingID)))
		if len(ids) != 1 || ids[0] != op.Result.ContainerID {
			f.t.Fatal("extra or missing app member/database writer")
		}
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(f.docker("network", "inspect", application.ApplicationDatabase.Origin.NetworkID, "--format", "{{json .Containers}}"), &members) != nil || len(members) != 2 || members[application.Result.ContainerID] == nil || members[database.Result.ContainerID] == nil {
		f.t.Fatal("private network endpoint set differs from exact app and database")
	}
}

func (f *databaseRunnerFixture) credentialMetadata(view domain.HostingDatabase) [32]byte {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var raw []byte
	// Hash retained encrypted versions and reference metadata without decrypting or
	// revealing any app credential to this fixture.
	if f.database.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_array(to_jsonb(c),to_jsonb(v)) ORDER BY c.role),'[]'::jsonb)::text FROM hosting_database_credentials c JOIN project_managed_secret_versions v ON v.secret_id=c.secret_id AND v.version=c.version WHERE c.resource_id=$1`, view.Resource.ID).Scan(&raw) != nil || len(raw) == 0 || len(raw) > 65536 {
		f.t.Fatal("credential metadata unavailable")
	}
	defer clear(raw)
	return sha256.Sum256(raw)
}

func (f *databaseRunnerFixture) connectionDurableCounts() string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	var counts string
	if f.database.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_apply_admissions),(SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT count(*) FROM hosting_runtime_configuration_receipts),(SELECT count(*) FROM hosting_application_database_effect_authorizations),(SELECT count(*) FROM hosting_application_database_stages),(SELECT count(*) FROM hosting_application_database_retirements),(SELECT count(*) FROM hosting_database_membership_receipts))::text`).Scan(&counts) != nil {
		f.t.Fatal("connected replay evidence counts unavailable")
	}
	return counts
}

func (f *databaseRunnerFixture) assertConnectionJournalRetired(id string) {
	f.t.Helper()
	snapshot := f.upgradeJournal()
	for _, e := range snapshot.Hosting {
		if e.OperationID == id {
			f.t.Fatal("connected completion journal not retired")
		}
	}
	for _, e := range snapshot.Events {
		if e.Attempt.RunID != "" {
			f.t.Fatal("connected event journal not retired")
		}
	}
}

func TestHostingApplicationDatabaseCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires explicit combined-candidate physical gate authorization")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	image := os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")
	if applicationDatabaseFixtureImage(image) != nil {
		t.Fatal("owned registry HTTP image digest required")
	}
	for i, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
			view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseCreateInput(engine, f.environment, f.destination), 201)
			source := f.await(f.deploy(view, "connected_source_v1").ID)
			if source.Plan.Database.Network != nil {
				t.Fatal("first Connect must start from ordinary V1 database")
			}
			volume := f.volumeProof(source)
			credentials := f.credentialMetadata(view)
			resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "http " + engine, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
			entries := []domain.HostingRuntimeEntry{}
			for _, literal := range [][2]string{{"FIXTURE_DATABASE_ENGINE", engine}, {"FIXTURE_DATABASE_NAMESPACE", "connected_" + engine}} {
				v := literal[1]
				entries = append(entries, domain.HostingRuntimeEntry{Name: literal[0], Kind: "literal", Value: &v})
			}
			config := databaseRunnerJSON[struct {
				Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
			}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat(string(rune('a'+i)), 32), Entries: entries}, 200).Configuration
			deployment := domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "connected_a", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 30}}
			in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: view.Resource.ID, ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: config.RuntimeGeneration, ExpectedDatabaseGeneration: view.Resource.Generation, RequestID: strings.Repeat(string(rune('c'+i)), 32)}, Deployment: deployment}
			path := "/api/v1/resources/" + resource.ID + "/database-connections"
			databaseRunnerJSON[json.RawMessage](f, "", "POST", path, in, 401)
			stale := in
			stale.Binding.ExpectedDatabaseGeneration++
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", path, stale, 409)
			// A distinct destination cannot borrow this database's private network.
			enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, f.token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_foreign_acceptance", RunnerName: "foreign metadata", Capabilities: []string{domain.RunTypeImageDeploy}}, 201)
			f.protected = append(f.protected, []byte(enrollment.Token), []byte("ncr_foreign_fixture_only"))
			foreignRunner := databaseRunnerJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", databaseRunnerEnrollmentConsumeInput("ncr_foreign_fixture_only"), 200)
			other := databaseRunnerJSON[app.ServerDetail](f, f.token, "POST", "/api/v1/servers", app.ServerInput{Name: "foreign destination", RunnerID: foreignRunner.Runner.ID}, 201)
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/destinations/"+other.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
			foreign := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "foreign app", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+foreign.ID+"/binding", app.ResourceBindingInput{DestinationID: other.Destination.ID}, 201)
			cross := in
			cross.Binding.ExpectedRuntimeGeneration = 0
			cross.Deployment.ExpectedRuntimeConfigurationGeneration = 0
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+foreign.ID+"/database-connections", cross, 409)
			connection := databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", path, in, 202)
			if connection.CaptureID == "" || connection.UpgradeOperationID == "" || connection.ApplicationOperationID == "" {
				t.Fatal("public Connect omitted automatic workflow identities")
			}
			active := f.activeConnection(resource.ID)
			a := f.awaitConnectionOperation(connection.ApplicationOperationID)
			f.applicationContainer(a)
			upgrade, err := f.operation(t.Context(), connection.UpgradeOperationID)
			if err != nil || upgrade.Plan == nil || upgrade.DatabaseUpgrade == nil || databaseRunnerValidatePlan(upgrade) != nil || domain.ValidateHostingDatabasePlanV2(*upgrade.Plan) != nil || f.volumeProof(upgrade) != volume || f.credentialMetadata(view) != credentials {
				t.Fatal("automatic Connect failed retained V1 to V2 transition")
			}
			f.appRecord(a, "PUT", "a", "retained_a")
			f.appRecord(a, "GET", "a", "retained_a")
			deployment.ExpectedRuntimeConfigurationGeneration = a.Snapshot.RuntimeConfiguration.Generation
			deployment.IdempotencyKey = "connected_b"
			b := f.awaitConnectionOperation(databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", deployment, 202).ID)
			if b.Result.ContainerID == a.Result.ContainerID || b.Result.PreviousContainerID != a.Result.ContainerID {
				t.Fatal("app replacement did not retire exact predecessor")
			}
			bRuntime := f.applicationContainer(b)
			f.exactMembers(b, upgrade)
			f.appRecord(b, "GET", "a", "retained_a")
			f.appRecord(b, "PUT", "b", "retained_b")
			active = f.connection(resource.ID)
			maintain := domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: view.Resource.Generation, ExpectedMembershipRevision: active.MembershipRevision, IdempotencyKey: "connected_database_replacement"}
			maintained := f.awaitConnectionOperation(databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+view.Resource.ID+"/database/maintenance", maintain, 202).ID)
			if maintained.Result.ContainerID == upgrade.Result.ContainerID || maintained.DatabaseMaintenance == nil || len(maintained.Plan.Database.Network.Members) != 1 || !reflect.DeepEqual(maintained.Plan.Storage, source.Plan.Storage) || !reflect.DeepEqual(maintained.Plan.Database.Credentials, source.Plan.Database.Credentials) || maintained.Plan.Database.Network.NetworkID != upgrade.Plan.Database.Network.NetworkID || f.volumeProof(maintained) != volume || f.credentialMetadata(view) != credentials {
				t.Fatal("database maintenance changed retained identity")
			}
			f.inspect(maintained)
			if !reflect.DeepEqual(bRuntime, f.applicationContainer(b)) {
				t.Fatal("database maintenance replaced the running app or endpoint")
			}
			f.exactMembers(b, maintained)
			f.appRecord(b, "GET", "a", "retained_a")
			f.appRecord(b, "GET", "b", "retained_b")
			f.appRecord(b, "PUT", "db_replaced", "retained_database_replacement")
			f.stop()
			deployment.IdempotencyKey = "connected_c"
			third := databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", deployment, 202)
			wire := newDatabaseUpgradeCompleteWire(third.ID)
			f.upgradeComplete.Store(wire)
			f.start()
			select {
			case <-wire.entered:
			case <-time.After(180 * time.Second):
				t.Fatal("app committed Complete response loss did not enter")
			}
			f.stop()
			select {
			case <-wire.finished:
			case <-time.After(10 * time.Second):
				t.Fatal("app Complete fault handler did not join")
			}
			original, ok := wire.original()
			if !ok {
				t.Fatal("missing original committed Complete")
			}
			c, err := f.operation(t.Context(), third.ID)
			if err != nil || !applicationDatabaseCompleted(c) || !applicationDatabaseCompleteReceiptMatches(c, original) {
				t.Fatal("controller did not atomically settle app before ACK loss")
			}
			pending := false
			for _, e := range f.upgradeJournal().Hosting {
				if e.OperationID == third.ID && e.Phase == "result" && e.ApplicationDatabase != nil && e.Result != nil && *e.Result == original {
					pending = true
				}
			}
			if !pending {
				t.Fatal("runner restart lacks durable exact app Complete body")
			}
			credential, err := os.ReadFile(filepath.Join(f.root, "credential"))
			if err != nil {
				t.Fatal("protected runner credential unavailable")
			}
			staleAuthority := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: original.HostingAuthority, AuthorizationID: strings.Repeat("f", 32), Action: "create"}
			databaseRunnerJSON[json.RawMessage](f, string(credential), "POST", "/api/v1/runners/hosting/database-binding/authorize", staleAuthority, 409)
			clear(credential)
			before := f.applicationContainer(c)
			f.exactMembers(c, maintained)
			counts, applies, deliveries := f.connectionDurableCounts(), f.applyRequests.Load(), f.deliveryRequests.Load()
			runtimeDeliveries, authorizations := f.runtimeDeliveryRequests.Load(), f.connectionAuthorizationRequests.Load()
			since := time.Now().UTC().Format(time.RFC3339Nano)
			f.start()
			select {
			case <-wire.replayed:
			case <-time.After(30 * time.Second):
				t.Fatal("saved app Complete did not replay")
			}
			f.awaitSourceJournalRetired()
			f.stop()
			until := time.Now().UTC().Format(time.RFC3339Nano)
			after := f.applicationContainer(c)
			if !wire.exactReplay() || !reflect.DeepEqual(before, after) || f.connectionDurableCounts() != counts || f.applyRequests.Load() != applies || f.deliveryRequests.Load() != deliveries || f.runtimeDeliveryRequests.Load() != runtimeDeliveries || f.connectionAuthorizationRequests.Load() != authorizations {
				t.Fatal("Complete replay changed runtime, authority, admission or delivery")
			}
			for _, binding := range []string{c.BindingID, maintained.BindingID} {
				if len(bytes.TrimSpace(f.docker("events", "--since", since, "--until", until, "--filter", "label=io.nerocd.binding="+binding, "--format", "{{json .}}"))) != 0 {
					t.Fatal("Complete replay performed a container/network effect")
				}
			}
			finalConnection := f.connection(resource.ID)
			if finalConnection.State != "active" || finalConnection.HoldOperationID != "" || finalConnection.MembershipRevision != c.ApplicationDatabase.MembershipRevision+2 {
				t.Fatal("settled connection membership or hold invalid")
			}
			f.assertConnectionJournalRetired(third.ID)
			f.exactMembers(c, maintained)
			f.appRecord(c, "GET", "a", "retained_a")
			f.appRecord(c, "GET", "b", "retained_b")
			f.appRecord(c, "GET", "db_replaced", "retained_database_replacement")
			f.appRecord(c, "PUT", "c", "retained_c")
			f.appRecord(c, "GET", "c", "retained_c")
			if f.credentialMetadata(view) != credentials || databaseUpgradeAuthorityLogCheck(f.logs.snapshot(), original.HostingAuthority, f.protected) != nil {
				t.Fatal("credential identity or output redaction failed")
			}
			t.Logf("connected HTTP app verified engine=%s capture=%s upgrade=%s app=%s database=%s revision=%d", engine, connection.CaptureID, upgrade.ID, c.ID, maintained.ID, active.MembershipRevision)
		})
	}
}

func TestApplicationDatabaseRuntimeImageProfile(t *testing.T) {
	good := "127.0.0.1:35001/nerocd-http-app@sha256:" + strings.Repeat("a", 64)
	if applicationDatabaseFixtureImage(good) != nil {
		t.Fatal("valid owned image rejected")
	}
	for _, bad := range []string{strings.Replace(good, "127.0.0.1", "localhost", 1), strings.Replace(good, ":35001", ":80", 1), strings.Replace(good, "nerocd-http-app", "foreign", 1), strings.Replace(good, "@sha256:", ":latest", 1), good + "x"} {
		if applicationDatabaseFixtureImage(bad) == nil {
			t.Fatal("foreign image profile accepted")
		}
	}
}

func TestApplicationDatabaseRuntimeCompleteReceiptContract(t *testing.T) {
	original := domain.HostingCompleteRequest{HostingAuthority: domain.HostingAuthority{OperationID: "operation_fixture", RunID: "run_fixture", LeaseID: "lease_fixture", Attempt: 1, Fence: "private_fence_fixture"}, ReceiptID: "complete_fixture", Result: domain.HostingResult{Outcome: "applied", ContainerID: strings.Repeat("a", 64), PlanHash: "sha256:" + strings.Repeat("b", 64), Ready: true}}
	for _, test := range []string{"exact_result", "whole_request_hash", "wrong_receipt", "changed_result", "missing_result", "missing_receipt"} {
		t.Run(test, func(t *testing.T) {
			result := original.Result
			receipt := domain.HostingReceipt{ID: original.ReceiptID, BodyHash: domain.HostingDocumentHash(original.Result)}
			op := domain.HostingOperation{Result: &result, CompleteReceipt: &receipt}
			switch test {
			case "whole_request_hash":
				receipt.BodyHash = domain.HostingDocumentHash(original)
			case "wrong_receipt":
				receipt.ID = "foreign_receipt"
			case "changed_result":
				result.ContainerID = strings.Repeat("c", 64)
			case "missing_result":
				op.Result = nil
			case "missing_receipt":
				op.CompleteReceipt = nil
			}
			if applicationDatabaseCompleteReceiptMatches(op, original) != (test == "exact_result") {
				t.Fatal("Complete receipt did not retain the controller result-body hash contract")
			}
		})
	}
}

func TestApplicationDatabaseRuntimeProfileFlags(t *testing.T) {
	old := databaseRunnerArguments(&databaseRunnerFixture{root: "/fixture", server: &httptest.Server{URL: "https://fixture.invalid"}, upgrade: true})
	next := databaseRunnerArguments(&databaseRunnerFixture{root: "/fixture", server: &httptest.Server{URL: "https://fixture.invalid"}, upgrade: true, connected: true})
	for _, flag := range []string{"--hosting-database-network-capture", "--hosting-runtime-env", "--hosting-application-database", "--hosting-database-membership-maintenance"} {
		if slices.Contains(old, flag) || !slices.Contains(next, flag) {
			t.Fatal("connected flags leaked into legacy profile or missing opt-in")
		}
	}
	if applicationDatabaseCompleted(domain.HostingOperation{State: "succeeded"}) {
		t.Fatal("missing family evidence accepted")
	}
}
