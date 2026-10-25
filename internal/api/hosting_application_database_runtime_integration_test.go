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
	"log/slog"
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
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
	"nerocd/web"
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

// This is retained permission history, used only to prove terminal denial.
// Its identifier, complete request and response must all belong to the original
// attempt; a newly invented identifier would not prove historical correlation.
func applicationDatabaseOriginalCreateAuthorization(id, requestHash string, raw, response []byte, original domain.HostingCompleteRequest, revision int64) (domain.HostingApplicationDatabaseEffectRequest, bool) {
	var request domain.HostingApplicationDatabaseEffectRequest
	var grant domain.HostingApplicationDatabaseEffectAuthorization
	if domain.DecodeHostingStorageProtocolJSON(raw, &request) != nil || domain.DecodeHostingStorageProtocolJSON(response, &grant) != nil || request.Validate() != nil || grant.Validate() != nil {
		return request, false
	}
	return request, request.AuthorizationID == id && requestHash == domain.HostingDocumentHash(request) && request.HostingAuthority == original.HostingAuthority && request.Action == "create" && request.ExpectedStageHash == "" &&
		grant.AuthorizationID == id && grant.OperationID == original.OperationID && grant.PlanHash == original.Result.PlanHash && grant.Action == request.Action && grant.StageHash == request.ExpectedStageHash && grant.LeaseID == original.LeaseID && grant.Attempt == original.Attempt && grant.Fence == original.Fence && grant.MembershipRevision == revision
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

// Each engine has isolated controller metadata but shares the owned DinD daemon.
// Release only its verified final app, so the next schema can reuse its ports.
func applicationDatabaseCleanupIdentity(op domain.HostingOperation) bool {
	return op.Result != nil && op.Plan != nil && op.Release != nil && op.ID != "" && op.BindingID != "" && op.Plan.Hash != "" && op.Release.Hash != "" && op.Release.DaemonID != "" && op.Result.PlanHash == op.Plan.Hash && op.Result.ImageID == op.Release.ImageID && op.Result.DaemonID == op.Release.DaemonID && len(op.Result.ContainerID) == 64 && strings.Trim(op.Result.ContainerID, "0123456789abcdef") == ""
}

func cleanupApplicationDatabaseFinal(ctx context.Context, op domain.HostingOperation, docker func(context.Context, ...string) ([]byte, error)) error {
	invalid := errors.New("final fixture application cleanup identity or absence unconfirmed")
	if !applicationDatabaseCleanupIdentity(op) {
		return invalid
	}
	daemon := func() bool {
		raw, err := docker(ctx, "info", "--format", "{{.ID}}")
		return err == nil && strings.TrimSpace(string(raw)) == op.Release.DaemonID
	}
	if !daemon() {
		return invalid
	}
	const format = `{"Id":{{json .Id}},"Image":{{json .Image}},"Config":{"Labels":{{json .Config.Labels}}}}`
	raw, err := docker(ctx, "container", "inspect", op.Result.ContainerID, "--format", format)
	defer clear(raw)
	var container struct {
		ID, Image string
		Config    struct{ Labels map[string]string }
	}
	if err != nil || json.Unmarshal(raw, &container) != nil || container.ID != op.Result.ContainerID || container.Image != op.Release.ImageID || container.Config.Labels["io.nerocd.binding"] != op.BindingID || container.Config.Labels["io.nerocd.operation"] != op.ID || container.Config.Labels["io.nerocd.plan"] != op.Plan.Hash || container.Config.Labels["io.nerocd.release"] != op.Release.Hash || !daemon() {
		return invalid
	}
	if _, err = docker(ctx, "container", "rm", "--force", op.Result.ContainerID); err != nil {
		return invalid
	}
	remaining, err := docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+op.Result.ContainerID)
	if err != nil || len(bytes.TrimSpace(remaining)) != 0 || !daemon() {
		return invalid
	}
	return nil
}

func (f *databaseRunnerFixture) appRecord(op domain.HostingOperation, method, key, value string) {
	f.t.Helper()
	if applicationDatabaseHTTPRecord(f.t.Context(), op, method, key, value) != nil {
		f.t.Fatal("application read/write failed")
	}
}

func applicationDatabaseHTTPRecord(ctx context.Context, op domain.HostingOperation, method, key, value string) error {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(op.Snapshot.HostPort)+"/records/"+key, strings.NewReader(value))
	if err != nil {
		return errors.New("HTTP fixture request invalid")
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("running application HTTP request failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	defer clear(raw)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != 200 || len(raw) > 4096 || string(raw) != value {
		return errors.New("application read/write failed")
	}
	return nil
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
	if mode := os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER"); mode != "" && mode != "0" && mode != "1" {
		t.Fatal("invalid browser mode")
	}
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
		if !t.Run(engine, func(t *testing.T) {
			var routeTrust *applicationDatabaseRouteFixture
			if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
				applicationDatabaseRouteCapacity(t)
				roots, public := newApplicationDatabaseRouteTrust(t)
				routeTrust = &applicationDatabaseRouteFixture{roots: roots, publicRoot: public}
			}
			f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
			view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseCreateInput(engine, f.environment, f.destination), 201)
			f.registerRouteDatabaseCleanup(view.Resource.ID)
			f.stop()
			sourceRequest := f.deploy(view, "connected_source_v1")
			f.registerRouteOperationCleanup(sourceRequest.ID)
			f.start()
			source := f.await(sourceRequest.ID)
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
			// Real server admission requires the canonical compose capability even
			// for this metadata-only foreign placement; shell remains absent.
			enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, f.token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_foreign_acceptance", RunnerName: "foreign metadata", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy}}, 201)
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
			var browser *applicationDatabaseBrowser
			var connection domain.HostingDatabaseConnection
			f.stop()
			if os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER") == "1" {
				browser = f.startApplicationDatabaseBrowser(resource, view, engine)
				connection = browser.await("connect").Connection
			} else {
				connection = databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", path, in, 202)
			}
			if connection.CaptureID == "" || connection.UpgradeOperationID == "" || connection.ApplicationOperationID == "" {
				t.Fatal("public Connect omitted automatic workflow identities")
			}
			f.registerRouteOperationCleanup(connection.UpgradeOperationID)
			f.registerRouteOperationCleanup(connection.ApplicationOperationID)
			f.start()
			active := f.activeConnection(resource.ID)
			a := f.awaitConnectionOperation(connection.ApplicationOperationID)
			f.applicationContainer(a)
			upgrade, err := f.operation(t.Context(), connection.UpgradeOperationID)
			if err != nil || upgrade.Plan == nil || upgrade.DatabaseUpgrade == nil || databaseRunnerValidatePlan(upgrade) != nil || domain.ValidateHostingDatabasePlanV2(*upgrade.Plan) != nil || f.volumeProof(upgrade) != volume || f.credentialMetadata(view) != credentials {
				t.Fatal("automatic Connect failed retained V1 to V2 transition")
			}
			f.appRecord(a, "PUT", "a", "retained_a")
			f.appRecord(a, "GET", "a", "retained_a")
			if routeTrust != nil {
				f.initialApplicationDatabaseRoute(a, engine, routeTrust.roots, routeTrust.publicRoot)
			}
			deployment.ExpectedRuntimeConfigurationGeneration = a.Snapshot.RuntimeConfiguration.Generation
			deployment.IdempotencyKey = "connected_b"
			f.stop()
			var bRequest domain.HostingOperation
			if browser != nil {
				browser.advance("redeploy", active.MembershipRevision, a, map[string]string{"a": "retained_a"})
				bRequest = browser.await("redeploy").Operation
			} else {
				var input any = deployment
				if f.routes != nil {
					input = f.routes.deployment(deployment)
				}
				bRequest = databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", input, 202)
			}
			f.registerRouteOperationCleanup(bRequest.ID)
			if f.routes != nil {
				f.routes.recoverYield(bRequest, a, active.MembershipRevision)
			} else {
				f.start()
			}
			b := f.awaitConnectionOperation(bRequest.ID)
			if b.Result.ContainerID == a.Result.ContainerID || b.Result.PreviousContainerID != a.Result.ContainerID {
				t.Fatal("app replacement did not retire exact predecessor")
			}
			bRuntime := f.applicationContainer(b)
			f.exactMembers(b, upgrade)
			f.appRecord(b, "GET", "a", "retained_a")
			f.appRecord(b, "PUT", "b", "retained_b")
			if f.routes != nil {
				f.routes.assertSelected(b)
				f.routes.record(t.Context(), b, "GET", "https_a", "retained_https_a")
				f.routes.record(t.Context(), b, "PUT", "https_b", "retained_https_b")
				f.routes.record(t.Context(), b, "GET", "https_b", "retained_https_b")
			}
			active = f.connection(resource.ID)
			maintain := domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: view.Resource.Generation, ExpectedMembershipRevision: active.MembershipRevision, IdempotencyKey: "connected_database_replacement"}
			f.stop()
			var maintenanceRequest domain.HostingOperation
			if browser != nil {
				browser.advance("maintenance", active.MembershipRevision, b, map[string]string{"a": "retained_a", "b": "retained_b"})
				maintenanceRequest = browser.await("maintenance").Operation
			} else {
				maintenanceRequest = databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+view.Resource.ID+"/database/maintenance", maintain, 202)
			}
			f.registerRouteOperationCleanup(maintenanceRequest.ID)
			f.start()
			maintained := f.awaitConnectionOperation(maintenanceRequest.ID)
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
			if f.routes != nil {
				f.routes.record(t.Context(), b, "GET", "db_replaced", "retained_database_replacement")
			}
			f.stop()
			deployment.IdempotencyKey = "connected_c"
			var third domain.HostingOperation
			if browser != nil {
				browser.advance("replay", active.MembershipRevision, b, map[string]string{"a": "retained_a", "b": "retained_b", "db_replaced": "retained_database_replacement"})
				third = browser.await("replay").Operation
			} else {
				var input any = deployment
				if f.routes != nil {
					input = f.routes.deployment(deployment)
				}
				third = databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", input, 202)
			}
			wire := newDatabaseUpgradeCompleteWire(third.ID)
			f.registerRouteOperationCleanup(third.ID)
			f.upgradeComplete.Store(wire)
			f.start()
			var failed <-chan struct{}
			if browser != nil {
				failed = browser.failed
			}
			wait, stopWait := context.WithTimeout(t.Context(), 180*time.Second)
			reason := waitApplicationDatabaseComplete(wait, f.process, wire.entered, failed)
			stopWait()
			if reason != "" {
				if browser != nil {
					browser.reportFailure(reason)
				} else {
					_ = f.emitApplicationDatabaseFailure(third.ID, reason)
				}
				t.Fatal("app committed Complete response loss failed before entry: " + reason)
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
			defer clear(credential)
			var authorizationID, authorizationHash string
			var authorizationRequest, authorizationResponse []byte
			if f.database.QueryRow(t.Context(), `SELECT authorization_id,request_hash,request,response FROM hosting_application_database_effect_authorizations WHERE operation_id=$1 AND request->>'action'='create' ORDER BY created_at,authorization_id LIMIT 1`, third.ID).Scan(&authorizationID, &authorizationHash, &authorizationRequest, &authorizationResponse) != nil {
				t.Fatal("missing original persisted create authorization")
			}
			staleAuthority, exact := applicationDatabaseOriginalCreateAuthorization(authorizationID, authorizationHash, authorizationRequest, authorizationResponse, original, c.ApplicationDatabase.MembershipRevision)
			clear(authorizationRequest)
			clear(authorizationResponse)
			if !exact {
				t.Fatal("persisted create authorization differs from original Complete authority")
			}
			before := f.applicationContainer(c)
			f.exactMembers(c, maintained)
			counts, applies, deliveries := f.connectionDurableCounts(), f.applyRequests.Load(), f.deliveryRequests.Load()
			runtimeDeliveries, authorizations := f.runtimeDeliveryRequests.Load(), f.connectionAuthorizationRequests.Load()
			since := time.Now().UTC().Format(time.RFC3339Nano)
			// Effect authorization requires a live lease before it reads historical
			// authorization IDs. Complete retired this exact lease, so denial is
			// specifically 404/not_found, while saved Complete ACK replay remains valid.
			denied := databaseRunnerJSON[map[string]string](f, string(credential), "POST", "/api/v1/runners/hosting/database-binding/authorize", staleAuthority, http.StatusNotFound)
			clear(credential)
			if denied["code"] != "not_found" || f.connectionDurableCounts() != counts || f.applyRequests.Load() != applies || f.deliveryRequests.Load() != deliveries || f.runtimeDeliveryRequests.Load() != runtimeDeliveries || f.connectionAuthorizationRequests.Load() != authorizations+1 || !reflect.DeepEqual(before, f.applicationContainer(c)) {
				t.Fatal("terminal original authorization was not denied without mutation or delivery")
			}
			authorizations++
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
			if f.routes != nil {
				f.routes.assertSelected(c)
				for key, value := range map[string]string{"https_a": "retained_https_a", "https_b": "retained_https_b", "a": "retained_a", "b": "retained_b", "db_replaced": "retained_database_replacement", "c": "retained_c"} {
					f.routes.record(t.Context(), c, "GET", key, value)
				}
				f.assertApplicationDatabaseRetirement(b, a)
				f.assertApplicationDatabaseRetirement(c, b)
				fmt.Println("DATABASE_CONNECTION_ROUTE_PASS " + engine)
			}
			if f.credentialMetadata(view) != credentials || databaseUpgradeAuthorityLogCheck(f.logs.snapshot(), original.HostingAuthority, f.protected) != nil {
				t.Fatal("credential identity or output redaction failed")
			}
			if browser != nil {
				browser.advance("finish", finalConnection.MembershipRevision, c, map[string]string{"a": "retained_a", "b": "retained_b", "db_replaced": "retained_database_replacement", "c": "retained_c"})
				browser.awaitFinished()
			}
			if f.process != nil {
				t.Fatal("final fixture cleanup requires joined runner")
			}
			cleanup, stopCleanup := context.WithTimeout(t.Context(), 15*time.Second)
			err = cleanupApplicationDatabaseFinal(cleanup, c, func(ctx context.Context, args ...string) ([]byte, error) {
				return databaseRunnerDocker(ctx, nil, args...)
			})
			stopCleanup()
			if err != nil {
				t.Fatal("verified final application cleanup failed")
			}
			port, portErr := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(c.Snapshot.HostPort))
			if portErr != nil {
				t.Fatal("final application loopback port was not released")
			}
			if port.Close() != nil {
				t.Fatal("final application port proof did not close")
			}
			t.Logf("connected HTTP app verified engine=%s capture=%s upgrade=%s app=%s database=%s revision=%d", engine, connection.CaptureID, upgrade.ID, c.ID, maintained.ID, active.MembershipRevision)
		}) {
			return
		}
		if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
			applicationDatabaseRouteCapacity(t)
			fmt.Println("DATABASE_CONNECTION_ROUTE_CAPACITY_PASS " + engine + " success")
		}
	}
}

func TestApplicationDatabaseRuntimeFinalCleanupIsolation(t *testing.T) {
	for _, mode := range []string{"shared_port", "foreign_daemon", "changed_daemon", "foreign_id", "foreign_image", "foreign_binding", "foreign_operation", "foreign_plan", "foreign_release", "inspect_error", "remove_error", "inventory_error", "residue"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			other := strings.Repeat("b", 64)
			op := domain.HostingOperation{ID: "final_c", BindingID: "app_binding", Plan: &domain.HostingApplyPlan{Hash: "plan"}, Release: &domain.HostingRelease{Hash: "release", ImageID: "image", DaemonID: "daemon"}, Result: &domain.HostingResult{ContainerID: id, PlanHash: "plan", ImageID: "image", DaemonID: "daemon"}}
			labels := map[string]string{"io.nerocd.binding": op.BindingID, "io.nerocd.operation": op.ID, "io.nerocd.plan": op.Plan.Hash, "io.nerocd.release": op.Release.Hash}
			container := map[string]any{"Id": id, "Image": op.Release.ImageID, "Config": map[string]any{"Labels": labels}}
			switch mode {
			case "foreign_id":
				container["Id"] = other
			case "foreign_image":
				container["Image"] = "foreign"
			case "foreign_binding":
				labels["io.nerocd.binding"] = "foreign"
			case "foreign_operation":
				labels["io.nerocd.operation"] = "foreign"
			case "foreign_plan":
				labels["io.nerocd.plan"] = "foreign"
			case "foreign_release":
				labels["io.nerocd.release"] = "foreign"
			}
			// The second schema can allocate the same third port. The daemon
			// cannot start Redis C while the first schema's PG C still owns it.
			occupied := map[int]string{20002: id, 21000: other}
			removals, daemonChecks := 0, 0
			docker := func(_ context.Context, args ...string) ([]byte, error) {
				switch {
				case slices.Equal(args, []string{"info", "--format", "{{.ID}}"}):
					daemonChecks++
					if mode == "foreign_daemon" || mode == "changed_daemon" && daemonChecks > 1 {
						return []byte("foreign"), nil
					}
					return []byte("daemon"), nil
				case len(args) == 5 && slices.Equal(args[:3], []string{"container", "inspect", id}) && args[3] == "--format":
					if mode == "inspect_error" {
						return nil, errors.New("SECRET-CANARY")
					}
					return json.Marshal(container)
				case slices.Equal(args, []string{"container", "rm", "--force", id}):
					removals++
					if mode == "remove_error" {
						return nil, errors.New("SECRET-CANARY")
					}
					if mode != "residue" {
						delete(occupied, 20002)
					}
					return []byte(id), nil
				case slices.Equal(args, []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id=" + id}):
					if mode == "inventory_error" {
						return nil, errors.New("SECRET-CANARY")
					}
					return []byte(occupied[20002]), nil
				default:
					t.Error("cleanup attempted a command outside exact final-C scope")
					return nil, errors.New("unexpected command")
				}
			}
			if occupied[20002] == "" {
				t.Fatal("regression model lost shared occupied port")
			}
			err := cleanupApplicationDatabaseFinal(t.Context(), op, docker)
			if (err == nil) != (mode == "shared_port") || err != nil && strings.Contains(err.Error(), "CANARY") {
				t.Fatal("cleanup did not fail closed with sanitized error")
			}
			if occupied[21000] != other {
				t.Fatal("cleanup touched unrelated container")
			}
			if mode == "shared_port" {
				if occupied[20002] != "" || removals != 1 {
					t.Fatal("verified PG C still blocks Redis C's port")
				}
				occupied[20002] = "redis_c"
			} else if !slices.Contains([]string{"remove_error", "inventory_error", "residue"}, mode) && (removals != 0 || occupied[20002] != id) {
				t.Fatal("failed inspection or foreign identity was treated as absence or removal authority")
			}
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

func TestApplicationDatabaseRuntimeTerminalDenialEnvelope(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeErrorEnvelope(w, http.StatusNotFound, "not_found", "resource not found")
	}))
	t.Cleanup(server.Close)
	f := &databaseRunnerFixture{t: t, server: server, client: server.Client()}
	denied := databaseRunnerJSON[map[string]string](f, "", http.MethodPost, "/api/v1/runners/hosting/database-binding/authorize", nil, http.StatusNotFound)
	if denied["code"] != "not_found" || denied["error"] != "resource not found" {
		t.Fatal("terminal denial did not decode the established flat public error envelope")
	}
}

func TestApplicationDatabaseRuntimeOriginalCreateAuthorization(t *testing.T) {
	original := domain.HostingCompleteRequest{HostingAuthority: domain.HostingAuthority{OperationID: "operation_fixture", RunID: "run_fixture", LeaseID: "lease_fixture", Attempt: 1, Fence: "private_fence_fixture"}, Result: domain.HostingResult{PlanHash: "sha256:" + strings.Repeat("b", 64)}}
	for _, name := range []string{"exact_historical", "row_id", "request_hash", "run", "lease", "attempt", "fence", "operation", "grant_id", "grant_plan", "grant_revision", "grant_lease", "grant_attempt", "grant_fence", "grant_action", "malformed"} {
		t.Run(name, func(t *testing.T) {
			id := strings.Repeat("a", 32)
			request := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: original.HostingAuthority, AuthorizationID: id, Action: "create"}
			grant := domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: id, OperationID: original.OperationID, PlanHash: original.Result.PlanHash, Action: "create", LeaseID: original.LeaseID, Attempt: original.Attempt, Fence: original.Fence, MembershipRevision: 7, UseUntil: time.Unix(1, 0).UTC()}
			switch name {
			case "row_id":
				id = strings.Repeat("c", 32)
			case "run":
				request.RunID = "foreign_run"
			case "lease":
				request.LeaseID = "foreign_lease"
			case "attempt":
				request.Attempt++
			case "fence":
				request.Fence = "foreign_fence"
			case "operation":
				request.OperationID = "foreign_operation"
			case "grant_id":
				grant.AuthorizationID = strings.Repeat("c", 32)
			case "grant_plan":
				grant.PlanHash = "sha256:" + strings.Repeat("c", 64)
			case "grant_revision":
				grant.MembershipRevision++
			case "grant_lease":
				grant.LeaseID = "foreign_lease"
			case "grant_attempt":
				grant.Attempt++
			case "grant_fence":
				grant.Fence = "foreign_fence"
			case "grant_action":
				grant.Action, grant.StageHash = "start", "sha256:"+strings.Repeat("c", 64)
			}
			requestHash := domain.HostingDocumentHash(request)
			if name == "request_hash" {
				requestHash = "sha256:" + strings.Repeat("c", 64)
			}
			raw, _ := json.Marshal(request)
			response, _ := json.Marshal(grant)
			if name == "malformed" {
				raw = []byte(`{`)
			}
			saved, ok := applicationDatabaseOriginalCreateAuthorization(id, requestHash, raw, response, original, 7)
			if ok != (name == "exact_historical") || ok && saved != request {
				t.Fatal("historical authorization did not require exact original request and response identity")
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

// The browser controls only ordinary public UI admissions. These channels record
// real API responses; they never prepare, apply or settle an operation.
type applicationDatabaseBrowserAdmission struct {
	Phase      string
	Connection domain.HostingDatabaseConnection
	Operation  domain.HostingOperation
}
type applicationDatabaseBrowserSeen struct {
	Request     [32]byte
	OperationID string
}

type applicationDatabaseBrowserProof struct {
	Operation domain.HostingOperation
	Records   map[string]string
	Route     *applicationDatabaseBrowserRouteProof
}

type applicationDatabaseBrowserRouteProof struct {
	Fixture *applicationDatabaseRouteFixture
	Route   domain.HostingRoute
}
type applicationDatabaseBrowser struct {
	f               *databaseRunnerFixture
	ctx             context.Context
	mu              sync.Mutex
	phase           string
	revision        int64
	operation       string
	latestOperation string
	invalid         bool
	seen            map[string]applicationDatabaseBrowserSeen
	proofs          map[string]applicationDatabaseBrowserProof
	admissions      chan applicationDatabaseBrowserAdmission
	finished        chan struct{}
	failed          chan struct{}
	fail            sync.Once
	finish          sync.Once
	report          sync.Once
	reported        bool
}

type applicationDatabaseFailure struct {
	Reason, Stage, OperationState, Candidate, Running, Identity, PrivateNetwork, DefaultBridge, PredecessorEndpoint, StartGrant, LeaseStatus, LeaseFresh string
	Runner                                                                                                                                               databaseRunnerExit
	Requests                                                                                                                                             string
	Errors                                                                                                                                               []string
}

func applicationDatabaseFailureUnknown() applicationDatabaseFailure {
	return applicationDatabaseFailure{Reason: "unknown", Stage: "unknown", OperationState: "unknown", Candidate: "unknown", Running: "unknown", Identity: "unknown", PrivateNetwork: "unknown", DefaultBridge: "unknown", PredecessorEndpoint: "unknown", StartGrant: "unknown", LeaseStatus: "unknown", LeaseFresh: "unknown"}
}

func applicationDatabaseFact(value bool) string {
	if value {
		return "match"
	}
	return "mismatch"
}

func applicationDatabaseFailureContainer(raw []byte, op domain.HostingOperation, stage domain.HostingApplicationDatabaseStage, out *applicationDatabaseFailure) {
	var container struct {
		ID, Image       string
		Config          struct{ Labels map[string]string }
		State           struct{ Running bool }
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID, EndpointID, IPAddress string }
		}
	}
	if json.Unmarshal(raw, &container) != nil || container.ID != stage.Candidate.ContainerID {
		return
	}
	out.Candidate = "present"
	out.Running = strconv.FormatBool(container.State.Running)
	out.Identity = applicationDatabaseFact(container.Image == stage.Candidate.ImageID && container.Config.Labels["io.nerocd.binding"] == op.BindingID && container.Config.Labels["io.nerocd.operation"] == op.ID && container.Config.Labels["io.nerocd.plan"] == op.Plan.Hash && container.Config.Labels["io.nerocd.release"] == op.Release.Hash)
	n := container.NetworkSettings.Networks[op.ApplicationDatabase.Binding.NetworkName]
	out.PrivateNetwork = applicationDatabaseFact(n.NetworkID == op.ApplicationDatabase.Origin.NetworkID && n.EndpointID != "" && n.IPAddress != "")
	d := container.NetworkSettings.Networks["bridge"]
	out.DefaultBridge = applicationDatabaseFact(d.NetworkID == stage.Candidate.DefaultNetworkID && d.EndpointID != "")
}

func (f *databaseRunnerFixture) emitApplicationDatabaseFailure(operation, reason string) bool {
	out := applicationDatabaseFailureUnknown()
	if slices.Contains([]string{"runner_exit", "browser_failure", "deadline", "go_failure"}, reason) {
		out.Reason = reason
	}
	out.Runner = f.failureRunnerStatus()
	out.Requests = f.apiCounts.summary()
	if len(out.Requests) > 2048 {
		out.Requests = out.Requests[:2048]
	}
	logs := f.logs.snapshot()
	out.Errors = databaseRunnerFailureCategories(logs)
	clear(logs)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.t.Context()), 2*time.Second)
	defer cancel()
	if f.database != nil && operation != "" {
		op, err := f.operation(ctx, operation)
		row, stageErr := sqlcgen.New(f.database).ReadApplicationDatabaseStage(ctx, operation)
		var stage domain.HostingApplicationDatabaseStage
		if err == nil && op.ApplicationDatabase != nil && op.Plan != nil && op.Release != nil {
			if slices.Contains([]string{"queued", "preparing", "prepared", "applying", "verifying", "succeeded", "failed", "uncertain"}, op.State) {
				out.OperationState = op.State
			}
			input, inputErr := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
			if stageErr == nil && inputErr == nil && len(row.Stage) <= domain.HostingApplicationDatabaseJournalJSONLimit && json.Unmarshal(row.Stage, &stage) == nil && stage.OperationID == op.ID && stage.Validate(*op.Plan, input) == nil {
				out.Stage = stage.Phase
				var grants int
				if f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_application_database_effect_authorizations WHERE operation_id=$1 AND request->>'action'='start'`, operation).Scan(&grants) == nil {
					out.StartGrant = "none"
					if grants > 0 {
						out.StartGrant = "recorded"
					}
				}
				var authority domain.HostingAuthority
				var lease string
				var fresh bool
				if json.Unmarshal(row.Authority, &authority) == nil && authority.OperationID == op.ID && authority.RunID == op.RunID && f.database.QueryRow(ctx, `SELECT status,expires_at>clock_timestamp() FROM run_leases WHERE id=$1 AND run_id=$2 AND attempt=$3 AND fence=$4`, authority.LeaseID, authority.RunID, authority.Attempt, authority.Fence).Scan(&lease, &fresh) == nil {
					if slices.Contains([]string{"active", "succeeded", "failed", "canceled", "expired"}, lease) {
						out.LeaseStatus = lease
					}
					out.LeaseFresh = strconv.FormatBool(fresh)
				}
				// Only the validated candidate, and its one authenticated predecessor,
				// may be inspected. Selected output excludes Config.Env and commands.
				const format = `{"Id":{{json .Id}},"Image":{{json .Image}},"Config":{"Labels":{{json .Config.Labels}}},"State":{"Running":{{json .State.Running}}},"NetworkSettings":{"Networks":{{json .NetworkSettings.Networks}}}}`
				if raw, e := databaseRunnerDocker(ctx, nil, "container", "inspect", stage.Candidate.ContainerID, "--format", format); e == nil {
					applicationDatabaseFailureContainer(raw, op, stage, &out)
					clear(raw)
				}
				if op.Plan.ApplicationDatabase != nil && len(op.Plan.ApplicationDatabase.PreviousMembers) == 1 {
					member := op.Plan.ApplicationDatabase.PreviousMembers[0]
					if raw, e := databaseRunnerDocker(ctx, nil, "container", "inspect", member.Endpoint.Target.ContainerID, "--format", format); e == nil {
						var old struct {
							ID              string
							NetworkSettings struct {
								Networks map[string]struct{ NetworkID, EndpointID string }
							}
						}
						if json.Unmarshal(raw, &old) == nil && old.ID == member.Endpoint.Target.ContainerID {
							endpoint := old.NetworkSettings.Networks[member.Endpoint.Target.NetworkName]
							out.PredecessorEndpoint = applicationDatabaseFact(endpoint.NetworkID == member.Endpoint.Target.NetworkID && endpoint.EndpointID == member.Endpoint.EndpointID)
						}
						clear(raw)
					}
				}
			}
		}
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 4096 {
		return false
	}
	output := f.failureOutput
	if output == nil {
		output = os.Stdout
	}
	_, err = fmt.Fprintf(output, "DATABASE_CONNECTION_BROWSER_FAILURE %s\n", raw)
	return err == nil
}

func (b *applicationDatabaseBrowser) reportFailure(reason string) bool {
	b.report.Do(func() {
		b.mu.Lock()
		operation := b.latestOperation
		b.mu.Unlock()
		b.reported = b.f.emitApplicationDatabaseFailure(operation, reason)
		b.fail.Do(func() { close(b.failed) })
	})
	return b.reported
}

func (b *applicationDatabaseBrowser) failureResponse(w http.ResponseWriter) {
	if !b.reportFailure("browser_failure") {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func applicationDatabaseBrowserOwner(api *Server, project, origin string, next http.Handler) http.Handler {
	return api.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := api.app.CurrentPrincipal(r.Context())
		role, roleErr := api.app.ProjectRole(r.Context(), project)
		if !applicationDatabaseBrowserControlRequest(r, origin, e == nil && roleErr == nil && principal.ID == "usr_bootstrap" && role.Role == "owner") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func applicationDatabaseBrowserControlRequest(r *http.Request, origin string, owner bool) bool {
	return owner && r.Header.Get("Origin") == origin && r.Host == strings.TrimPrefix(origin, "http://") && r.URL.RawQuery == "" && !r.URL.ForceQuery && r.ContentLength == 0 && len(r.TransferEncoding) == 0
}

func applicationDatabaseBrowserOrigin(origin string) bool {
	if !strings.HasPrefix(origin, "http://127.0.0.1:") {
		return false
	}
	port := strings.TrimPrefix(origin, "http://127.0.0.1:")
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1024 && n <= 65535 && strconv.Itoa(n) == port
}

func applicationDatabaseBrowserBootstrapComplete(ctx context.Context, handler http.Handler) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/bootstrap-status", nil))
	var status app.BootstrapStatus
	return response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &status) == nil && status.Status == "complete"
}

func (f *databaseRunnerFixture) startApplicationDatabaseBrowser(resource domain.Resource, database domain.HostingDatabase, engine string) *applicationDatabaseBrowser {
	f.t.Helper()
	origin := os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN")
	if !applicationDatabaseBrowserOrigin(origin) {
		f.t.Fatal("browser requires exact owned loopback origin")
	}
	// Supported bootstrap creates the retained installation administrator and its
	// durable lifecycle state. The seeded browser owner is a different account.
	const bootstrapPassword = "database-browser-bootstrap-password"
	f.protected = append(f.protected, []byte(bootstrapPassword))
	administrator, err := f.service.BootstrapAdmin(f.t.Context(), app.BootstrapAdminInput{Email: "database-bootstrap@example.local", Name: "Database browser bootstrap", Password: bootstrapPassword})
	if err != nil || administrator.ID == "" || administrator.ID == "usr_bootstrap" || administrator.ID == "usr_viewer" || administrator.GlobalRole != domain.RoleSystemAdmin {
		f.t.Fatal("browser installation bootstrap did not create a distinct administrator")
	}
	if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
		administratorSession := databaseRunnerJSON[struct {
			Token string `json:"token"`
		}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "database-bootstrap@example.local", "password": bootstrapPassword}, 201)
		f.adminToken = administratorSession.Token
		f.protected = append(f.protected, []byte(administratorSession.Token))
	}
	// Public setup establishes explicit ordinary owner/viewer memberships.
	for _, member := range []app.ProjectMemberInput{{ProjectID: resource.ProjectID, Email: "admin@example.local", Role: "owner"}, {ProjectID: resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		saved := databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", member, 200)
		if saved.ProjectID != member.ProjectID || saved.Role != member.Role || saved.Email != member.Email {
			f.t.Fatal("browser project membership did not match public setup")
		}
	}
	viewer := databaseRunnerJSON[struct {
		Token string `json:"token"`
	}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "viewer@example.local", "password": "viewer"}, 201)
	f.protected = append(f.protected, []byte(viewer.Token))
	private := databaseRunnerJSON[domain.Project](f, viewer.Token, "POST", "/api/v1/projects", app.ProjectInput{Name: "browser private project"}, 201)
	privateEnvironment := databaseRunnerJSON[domain.ProjectEnvironment](f, viewer.Token, "POST", "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: private.ID, Name: "private"}, 201)
	foreign := databaseRunnerJSON[domain.Resource](f, viewer.Token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: private.ID, ProjectEnvironmentID: privateEnvironment.ID, Name: "private app", Kind: "application", Source: resource.Source}, 201)
	// Fixture-only exception: the public API has no local-user role update. This
	// exact isolated seed demotion preserves the distinct bootstrap administrator.
	changed, err := f.database.Exec(f.t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap' AND global_role='system_admin'`)
	if err != nil || changed.RowsAffected() != 1 {
		f.t.Fatal("browser owner demotion did not affect exactly the seeded admin")
	}
	var global, role string
	if f.database.QueryRow(f.t.Context(), `SELECT u.global_role,m.role FROM users u JOIN project_members m ON m.user_id=u.id AND m.project_id=$1 WHERE u.id='usr_bootstrap'`, resource.ProjectID).Scan(&global, &role) != nil || global != "user" || role != "owner" {
		f.t.Fatal("browser does not have exact ordinary project-owner authority")
	}
	var administratorRole, administratorStatus string
	if f.database.QueryRow(f.t.Context(), `SELECT global_role,status FROM users WHERE id=$1`, administrator.ID).Scan(&administratorRole, &administratorStatus) != nil || administratorRole != domain.RoleSystemAdmin || administratorStatus != domain.UserActive {
		f.t.Fatal("browser installation lost its active bootstrap administrator")
	}
	api := NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	if !applicationDatabaseBrowserBootstrapComplete(f.t.Context(), api) {
		f.t.Fatal("browser requires public bootstrap status complete before login")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 12*time.Minute)
	b := &applicationDatabaseBrowser{f: f, ctx: ctx, phase: "connect", seen: map[string]applicationDatabaseBrowserSeen{}, proofs: map[string]applicationDatabaseBrowserProof{}, admissions: make(chan applicationDatabaseBrowserAdmission, 4), finished: make(chan struct{}), failed: make(chan struct{})}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		cancel()
		f.t.Fatal("browser nonce unavailable")
	}
	prefix := "/api/v1/fixture/database-connection/" + hex.EncodeToString(nonce)
	ownerOnly := applicationDatabaseBrowserOwner(api, resource.ProjectID, origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" && r.URL.Path == prefix+"/failure" {
			b.failureResponse(w)
			return
		}
		if r.Method == "GET" && r.URL.Path == prefix+"/state" {
			b.mu.Lock()
			defer b.mu.Unlock()
			writeJSON(w, 200, map[string]any{"phase": b.phase, "revision": b.revision, "operation_id": b.operation})
			return
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, prefix+"/proof/") {
			id := strings.TrimPrefix(r.URL.Path, prefix+"/proof/")
			b.mu.Lock()
			proof, exists := b.proofs[id]
			b.mu.Unlock()
			if !exists {
				w.WriteHeader(404)
				return
			}
			for key, value := range proof.Records {
				if applicationDatabaseHTTPRecord(r.Context(), proof.Operation, "GET", key, value) != nil {
					w.WriteHeader(502)
					return
				}
				if proof.Route != nil && applicationDatabaseRouteHTTPRecord(r.Context(), proof.Route.Fixture.roots, proof.Route.Fixture.hostname, proof.Route.Route.Certificate.Fingerprint, proof.Operation, "GET", key, value) != nil {
					w.WriteHeader(502)
					return
				}
			}
			response := map[string]any{"verified": true, "container_id": proof.Operation.Result.ContainerID, "record_count": len(proof.Records)}
			if proof.Route != nil {
				response["route"] = map[string]any{"route_id": proof.Route.Route.ID, "http_ready": proof.Route.Route.HTTPReady, "https_ready": proof.Route.Route.HTTPSReady, "certificate_status": proof.Route.Route.Certificate.Status, "selected_release_operation_id": proof.Route.Route.SelectedReleaseOperationID, "https_verified": true}
			}
			writeJSON(w, 200, response)
			return
		}
		if r.Method == "POST" && r.URL.Path == prefix+"/finish" {
			b.mu.Lock()
			valid := b.phase == "finish" && len(b.seen) == 4 && !b.invalid
			b.mu.Unlock()
			if !valid {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(204)
			b.finish.Do(func() { close(b.finished) })
			return
		}
		w.WriteHeader(404)
	}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			ownerOnly.ServeHTTP(w, r)
			return
		}
		action := ""
		if r.Method == "POST" {
			switch r.URL.Path {
			case "/api/v1/resources/" + resource.ID + "/database-connections":
				action = "connect"
			case "/api/v1/resources/" + resource.ID + "/database-connection/deployments":
				action = "deploy"
			case "/api/v1/resources/" + database.Resource.ID + "/database/maintenance":
				action = "maintenance"
			}
		}
		if action == "" {
			api.ServeHTTP(w, r)
			return
		}
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, domain.HostingDatabaseConnectionJSONLimit+1))
		defer clear(raw)
		if readErr != nil || len(raw) > domain.HostingDatabaseConnectionJSONLimit {
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			w.Header()[key] = slices.Clone(values)
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		if rec.Code != 202 {
			return
		}
		b.observe(action, raw, rec.Body.Bytes(), resource.ProjectID, resource.ID, database.Resource.ID)
	})
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		cancel()
		f.t.Fatal("owned browser listener unavailable")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	joined := make(chan error, 1)
	go func() { joined <- server.Serve(listener) }()
	f.t.Cleanup(func() {
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		if e := <-joined; !errors.Is(e, http.ErrServerClosed) {
			f.t.Error("browser listener did not join")
		}
	})
	f.t.Cleanup(func() {
		if f.t.Failed() {
			b.reportFailure("go_failure")
		}
	})
	metadata := map[string]any{"origin": origin, "engine": engine, "project_id": resource.ProjectID, "environment": "database runner acceptance", "application_resource_id": resource.ID, "application_name": resource.Name, "database_resource_id": database.Resource.ID, "foreign_resource_id": foreign.ID, "control_path": prefix}
	raw, metadataErr := json.Marshal(metadata)
	if metadataErr != nil {
		f.t.Fatal("browser metadata encoding failed")
	}
	fmt.Printf("DATABASE_CONNECTION_BROWSER_READY %s\n", raw)
	return b
}

func (b *applicationDatabaseBrowser) await(phase string) applicationDatabaseBrowserAdmission {
	b.f.t.Helper()
	select {
	case admitted := <-b.admissions:
		if admitted.Phase != phase {
			b.f.t.Fatal("browser admission phase mismatch")
		}
		return admitted
	case <-b.failed:
		b.f.t.Fatal("browser admission correlation failed")
		return applicationDatabaseBrowserAdmission{}
	case <-b.ctx.Done():
		b.f.t.Fatal("browser admission timed out")
		return applicationDatabaseBrowserAdmission{}
	}
}
func (b *applicationDatabaseBrowser) advance(phase string, revision int64, op domain.HostingOperation, records map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.revision, b.operation = phase, revision, op.ID
	b.proofs[op.ID] = applicationDatabaseBrowserProof{Operation: op, Records: records}
	if b.f.routes != nil {
		proof := b.proofs[op.ID]
		proof.Route = &applicationDatabaseBrowserRouteProof{Fixture: b.f.routes, Route: b.f.routes.route}
		b.proofs[op.ID] = proof
	}
}
func (b *applicationDatabaseBrowser) awaitFinished() {
	b.f.t.Helper()
	select {
	case <-b.finished:
	case <-b.failed:
		b.f.t.Fatal("browser admission correlation failed")
	case <-b.ctx.Done():
		b.f.t.Fatal("browser did not finish live UI assertions")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.invalid || len(b.seen) != 4 {
		b.f.t.Fatal("browser admitted an unexpected mutation")
	}
}

func TestApplicationDatabaseRuntimeFailureCapture(t *testing.T) {
	t.Run("exact_candidate_facts_and_unknown", func(t *testing.T) {
		op := domain.HostingOperation{ID: "operation", BindingID: "binding", Plan: &domain.HostingApplyPlan{Hash: "plan"}, Release: &domain.HostingRelease{Hash: "release"}, ApplicationDatabase: &domain.HostingApplicationDatabaseExecutionProof{Binding: domain.HostingDatabaseBinding{NetworkName: "private"}, Origin: domain.HostingDatabaseNetworkOrigin{NetworkID: "network"}}}
		stage := domain.HostingApplicationDatabaseStage{Candidate: domain.HostingApplicationDatabaseCandidate{ContainerID: "candidate", ImageID: "image", DefaultNetworkID: "bridge"}}
		raw := []byte(`{"Id":"candidate","Image":"image","Config":{"Labels":{"io.nerocd.binding":"binding","io.nerocd.operation":"operation","io.nerocd.plan":"plan","io.nerocd.release":"release"},"Env":["SECRET-CANARY"]},"State":{"Running":true},"NetworkSettings":{"Networks":{"private":{"NetworkID":"network","EndpointID":"endpoint","IPAddress":"10.0.0.2"},"bridge":{"NetworkID":"bridge","EndpointID":"default"}}}}`)
		facts := applicationDatabaseFailureUnknown()
		applicationDatabaseFailureContainer(raw, op, stage, &facts)
		if facts.Candidate != "present" || facts.Running != "true" || facts.Identity != "match" || facts.PrivateNetwork != "match" || facts.DefaultBridge != "match" {
			t.Fatal("exact candidate facts lost")
		}
		encoded, _ := json.Marshal(facts)
		if bytes.Contains(encoded, []byte("CANARY")) {
			t.Fatal("inspection emitted raw fields")
		}
		facts = applicationDatabaseFailureUnknown()
		applicationDatabaseFailureContainer(bytes.ReplaceAll(raw, []byte(`"Id":"candidate"`), []byte(`"Id":"foreign"`)), op, stage, &facts)
		if facts.Candidate != "unknown" || facts.Running != "unknown" {
			t.Fatal("foreign or failed inspection became absence or a candidate")
		}
	})
	t.Run("runner_exit_is_prompt", func(t *testing.T) {
		process := &databaseRunnerProcess{done: make(chan struct{}), exit: databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}}
		close(process.done)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if waitApplicationDatabaseComplete(ctx, process, make(chan struct{}), nil) != "runner_exit" || ctx.Err() != nil {
			t.Fatal("exited runner waited for the phase deadline")
		}
	})
	t.Run("browser_and_context_failure", func(t *testing.T) {
		failed := make(chan struct{})
		close(failed)
		if waitApplicationDatabaseComplete(t.Context(), nil, make(chan struct{}), failed) != "browser_failure" {
			t.Fatal("browser failure was ignored")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if waitApplicationDatabaseComplete(ctx, nil, make(chan struct{}), nil) != "deadline" {
			t.Fatal("cancellation was ignored")
		}
	})
	t.Run("diagnostic_before_ack_and_canary_exclusion", func(t *testing.T) {
		var output databaseRunnerLog
		f := &databaseRunnerFixture{t: t, logs: &databaseRunnerLog{}, failureOutput: &output}
		_, _ = f.logs.Write([]byte("Cookie: COOKIE-CANARY private_fence SECRET-CANARY application database execution requires exact retained evidence and current authority"))
		b := &applicationDatabaseBrowser{f: f, failed: make(chan struct{})}
		ack := &applicationDatabaseFailureAck{t: t, ResponseRecorder: httptest.NewRecorder(), output: &output}
		b.failureResponse(ack)
		if ack.Code != 204 {
			t.Fatal("diagnostic was not acknowledged")
		}
		b.failureResponse(httptest.NewRecorder())
		raw := output.snapshot()
		if bytes.Contains(raw, []byte("CANARY")) || bytes.Contains(raw, []byte("private_fence")) || bytes.Count(raw, []byte("DATABASE_CONNECTION_BROWSER_FAILURE")) != 1 {
			t.Fatal("failure snapshot leaked raw material or duplicated output")
		}
		var snapshot applicationDatabaseFailure
		if json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("DATABASE_CONNECTION_BROWSER_FAILURE "))), &snapshot) != nil || snapshot.Candidate != "unknown" || snapshot.Running != "unknown" || len(snapshot.Errors) != 1 || snapshot.Errors[0] != "application_execution" {
			t.Fatal("missing inspection was not explicit unknown or error category escaped allowlist")
		}
		select {
		case <-b.failed:
		default:
			t.Fatal("failure did not unblock Go wait")
		}
	})
	t.Run("owner_origin_host_and_body_guard", func(t *testing.T) {
		const origin = "http://127.0.0.1:18080"
		api, _ := newTestServerWithConfig(ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
		loginCookie := func(email, password string) *http.Cookie {
			raw, _ := json.Marshal(map[string]string{"email": email, "password": password})
			login := httptest.NewRequest(http.MethodPost, origin+"/api/v1/browser-sessions", bytes.NewReader(raw))
			login.Header.Set("Content-Type", "application/json")
			login.Header.Set("Origin", origin)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, login)
			if response.Code != 201 || len(response.Result().Cookies()) != 1 {
				t.Fatal("fixture cookie unavailable")
			}
			return response.Result().Cookies()[0]
		}
		cookie := loginCookie("admin@example.local", "admin")
		viewerCookie := loginCookie("viewer@example.local", "viewer")
		for _, mode := range []string{"system_admin", "anonymous", "foreign_origin", "foreign_host", "body", "query", "viewer"} {
			t.Run(mode, func(t *testing.T) {
				called := false
				handler := applicationDatabaseBrowserOwner(api, "proj_platform", origin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) }))
				r := httptest.NewRequest(http.MethodPost, origin+"/nonce/failure", nil)
				r.Header.Set("Origin", origin)
				r.Header.Set("X-NeroCD-CSRF", "1")
				if mode != "anonymous" {
					r.AddCookie(cookie)
				}
				switch mode {
				case "foreign_origin":
					r.Header.Set("Origin", "http://127.0.0.1:1")
				case "foreign_host":
					r.Host = "127.0.0.1:1"
				case "body":
					r.ContentLength = 1
				case "query":
					r.URL.RawQuery = "unexpected=1"
				case "viewer":
					r.Header.Del("Cookie")
					r.AddCookie(viewerCookie)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, r)
				if called || (rec.Code != 401 && rec.Code != 403) {
					t.Fatal("failure endpoint authorization guard changed")
				}
			})
		}
		for _, mode := range []string{"owner", "foreign_origin", "foreign_host", "body", "query", "nonowner"} {
			r := httptest.NewRequest(http.MethodPost, origin+"/nonce/failure", nil)
			r.Header.Set("Origin", origin)
			switch mode {
			case "foreign_origin":
				r.Header.Set("Origin", "http://127.0.0.1:1")
			case "foreign_host":
				r.Host = "127.0.0.1:1"
			case "body":
				r.ContentLength = 1
			case "query":
				r.URL.RawQuery = "unexpected=1"
			}
			if applicationDatabaseBrowserControlRequest(r, origin, mode != "nonowner") != (mode == "owner") {
				t.Fatal("exact owner control request guard changed")
			}
		}
	})
}

type applicationDatabaseFailureAck struct {
	*httptest.ResponseRecorder
	t      *testing.T
	output *databaseRunnerLog
}

func (w *applicationDatabaseFailureAck) WriteHeader(status int) {
	if status == 204 && len(w.output.snapshot()) == 0 {
		w.t.Error("acknowledgement preceded diagnostic emission")
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestApplicationDatabaseRuntimeBrowserBootstrapPrerequisite(t *testing.T) {
	server, _ := newTestServer()
	if !applicationDatabaseBrowserBootstrapComplete(t.Context(), server) {
		t.Fatal("actual public API complete status rejected")
	}
	for _, test := range []struct {
		name string
		code int
		body string
	}{{"required", 200, `{"status":"required"}`}, {"unavailable", 503, `{"status":"complete"}`}, {"invalid", 200, `{`}, {"missing", 200, `{}`}} {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/bootstrap-status" || r.Header.Get("Authorization") != "" {
					t.Error("bootstrap prerequisite must use the public status endpoint")
				}
				if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Error("bootstrap prerequisite lacks a bounded request context")
				}
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			})
			if applicationDatabaseBrowserBootstrapComplete(t.Context(), handler) {
				t.Fatal("incomplete or unavailable installation admitted browser login")
			}
		})
	}
}

func TestApplicationDatabaseRuntimeBrowserOrigin(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:18080", "http://127.0.0.1:65535"} {
		if !applicationDatabaseBrowserOrigin(origin) {
			t.Fatal("owned origin rejected")
		}
	}
	for _, origin := range []string{"", "https://127.0.0.1:18080", "http://localhost:18080", "http://127.0.0.1:80", "http://127.0.0.1:018080", "http://127.0.0.1:18080/", "http://127.0.0.1:18080?x=1"} {
		if applicationDatabaseBrowserOrigin(origin) {
			t.Fatal("foreign browser origin accepted")
		}
	}
}

func (b *applicationDatabaseBrowser) observe(action string, raw, response []byte, project, application, database string) {
	b.mu.Lock()
	defer func() {
		if b.invalid && b.failed != nil {
			b.fail.Do(func() { close(b.failed) })
		}
		b.mu.Unlock()
	}()
	phase := b.phase
	if action == "deploy" && phase != "redeploy" && phase != "replay" || action != "deploy" && action != phase || len(response) > domain.HostingApplicationDatabaseJournalJSONLimit {
		b.invalid = true
		return
	}
	admitted := applicationDatabaseBrowserAdmission{Phase: phase}
	id := ""
	if action == "connect" {
		if domain.DecodeHostingStorageProtocolJSON(response, &admitted.Connection) != nil || admitted.Connection.ProjectID != project || admitted.Connection.ApplicationResourceID != application || admitted.Connection.DatabaseResourceID != database || admitted.Connection.ID == "" || admitted.Connection.CaptureID == "" || admitted.Connection.UpgradeOperationID == "" || admitted.Connection.ApplicationOperationID == "" {
			b.invalid = true
			return
		}
		id = admitted.Connection.ApplicationOperationID
	} else {
		target := application
		if action == "maintenance" {
			target = database
		}
		if domain.DecodeHostingStorageProtocolJSON(response, &admitted.Operation) != nil || admitted.Operation.ID == "" || admitted.Operation.ProjectID != project || admitted.Operation.ResourceID != target || admitted.Operation.Snapshot.ResourceID != target {
			b.invalid = true
			return
		}
		id = admitted.Operation.ID
	}
	evidence := applicationDatabaseBrowserSeen{Request: sha256.Sum256(raw), OperationID: id}
	if old, seen := b.seen[phase]; seen {
		if old != evidence {
			b.invalid = true
		}
		return
	}
	b.seen[phase] = evidence
	b.latestOperation = id
	select {
	case b.admissions <- admitted:
	default:
		b.invalid = true
	}
}

func TestApplicationDatabaseRuntimeBrowserAdmissionCorrelation(t *testing.T) {
	response, _ := json.Marshal(domain.HostingOperation{ID: "operation", ProjectID: "project", ResourceID: "application", Snapshot: domain.HostingSnapshot{ResourceID: "application"}})
	for _, change := range []string{"exact_retry", "changed_request", "changed_operation", "foreign_project", "foreign_resource", "wrong_phase"} {
		t.Run(change, func(t *testing.T) {
			b := &applicationDatabaseBrowser{phase: "redeploy", seen: map[string]applicationDatabaseBrowserSeen{}, admissions: make(chan applicationDatabaseBrowserAdmission, 4)}
			b.observe("deploy", []byte("original"), response, "project", "application", "database")
			raw, reply := []byte("original"), bytes.Clone(response)
			switch change {
			case "changed_request":
				raw = []byte("changed")
			case "changed_operation":
				reply = bytes.ReplaceAll(reply, []byte(`"operation"`), []byte(`"other"`))
			case "foreign_project":
				reply = bytes.ReplaceAll(reply, []byte(`"project"`), []byte(`"foreign"`))
			case "foreign_resource":
				reply = bytes.ReplaceAll(reply, []byte(`"application"`), []byte(`"foreign"`))
			case "wrong_phase":
				b.phase = "maintenance"
			}
			b.observe("deploy", raw, reply, "project", "application", "database")
			if b.invalid != (change != "exact_retry") || len(b.admissions) != 1 {
				t.Fatal("browser admission changed phase, target, request or operation correlation")
			}
		})
	}
}
