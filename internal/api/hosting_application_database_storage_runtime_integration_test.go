package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/store/sqlcgen"
	"nerocd/web"
)

const applicationDatabaseStorageVersion = "application-database.storage.v1"

func TestApplicationDatabaseStorageReadinessFailureLifecycle(t *testing.T) {
	stage := domain.HostingApplicationDatabaseStage{OperationID: "operation", Phase: "attached", Hash: "stage", Candidate: domain.HostingApplicationDatabaseCandidate{Hash: "candidate"}}
	var public applicationDatabaseStoragePublic
	if json.Unmarshal([]byte(`{"id":"operation","state":"failed","application_database_storage_state":{"version":"application-database.storage.v1","phase":"replacement_failed","receipt_id":"failed_receipt","state_hash":"writer","stage_hash":"stage","candidate_hash":"candidate","recovery_actions":["resume_predecessor"],"recovery_state":"none"}}`), &public) != nil {
		t.Fatal("fixture projection invalid")
	}
	for _, mode := range []string{"stopped_failure_live_runner", "old_exit_failure", "transient_attached", "candidate_stop_incomplete", "wrong_operation", "wrong_stage", "wrong_candidate", "missing_resume_action", "wrong_state", "missing_projection"} {
		t.Run(mode, func(t *testing.T) {
			copy := public
			state := *public.StorageState
			copy.StorageState = &state
			var exit databaseRunnerExit
			switch mode {
			case "old_exit_failure":
				exit = databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}
			case "transient_attached":
				copy.StorageState.Phase = "stopped"
			case "candidate_stop_incomplete":
				copy.StorageState.Phase = "candidate_stopped"
			case "wrong_operation":
				copy.ID = "other"
			case "wrong_stage":
				copy.StorageState.StageHash = "other"
			case "wrong_candidate":
				copy.StorageState.CandidateHash = "other"
			case "missing_resume_action":
				copy.StorageState.RecoveryActions = []string{}
			case "wrong_state":
				copy.State = "applying"
			case "missing_projection":
				copy.StorageState = nil
			}
			if applicationDatabaseRouteProfileFailureObserved(true, "readiness", stage, &copy, exit) != (mode == "stopped_failure_live_runner") {
				t.Fatal("storage readiness observation confused stopped recovery with old runner-exit failure")
			}
		})
	}
	if !applicationDatabaseRouteProfileFailureObserved(false, "readiness", stage, nil, databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}) || applicationDatabaseRouteProfileFailureObserved(false, "readiness", stage, nil, databaseRunnerExit{}) {
		t.Fatal("storage-less readiness failure semantics changed")
	}
}

// This fixture qualifies a trusted root client that initializes its one owned
// volume. The normal nonroot, storage-less client remains a separate profile.
type applicationDatabaseStorageFixture struct {
	Metadata    domain.HostingStorage
	Plan        *domain.HostingStoragePlan
	Files       map[string][]byte
	VolumeProof string
}

type applicationDatabaseStorageAck struct {
	ExpectedStorageGeneration int64  `json:"expected_storage_generation"`
	ExpectedStorageHash       string `json:"expected_storage_hash"`
	AcknowledgeInterruption   bool   `json:"acknowledge_interruption"`
}

type applicationDatabaseStoragePublic struct {
	ID      string                `json:"id"`
	RunID   string                `json:"run_id"`
	State   string                `json:"state"`
	Result  *domain.HostingResult `json:"result"`
	Storage *struct {
		Version    string                   `json:"version"`
		Strategy   string                   `json:"strategy"`
		StorageRef domain.HostingStorageRef `json:"storage_ref"`
		Hash       string                   `json:"hash"`
	} `json:"application_database_storage"`
	StorageState *struct {
		Version           string   `json:"version"`
		Phase             string   `json:"phase"`
		ReceiptID         string   `json:"receipt_id"`
		StateHash         string   `json:"state_hash"`
		PreviousStateHash string   `json:"previous_state_hash"`
		StageHash         string   `json:"stage_hash"`
		CandidateHash     string   `json:"candidate_hash"`
		RecoveryActions   []string `json:"recovery_actions"`
		RecoveryAction    string   `json:"recovery_action"`
		RecoveryState     string   `json:"recovery_state"`
	} `json:"application_database_storage_state"`
}

func (s *applicationDatabaseStorageFixture) public(f *databaseRunnerFixture, id string) applicationDatabaseStoragePublic {
	f.t.Helper()
	p := databaseRunnerJSON[applicationDatabaseStoragePublic](f, f.token, "GET", "/api/v1/hosting-operations/"+id, nil, 200)
	if p.ID != id || p.Storage == nil || p.Storage.Version != applicationDatabaseStorageVersion || p.Storage.Strategy != "replacement_with_interruption" || p.Storage.StorageRef != s.Plan.Reference || p.Storage.Hash == "" || p.StorageState == nil || p.StorageState.Version != applicationDatabaseStorageVersion || p.StorageState.StateHash == "" || p.StorageState.ReceiptID == "" || p.StorageState.RecoveryActions == nil {
		f.t.Fatal("public storage operation omitted exact immutable composition or safe progress")
	}
	return p
}

func applicationDatabaseStorageProxyPause(ctx context.Context, r *applicationDatabaseRouteFixture, id string, paused bool, docker func(context.Context, ...string) ([]byte, error)) error {
	invalid := errors.New("owned fixture proxy pause identity or result unconfirmed")
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return invalid
	}
	raw, err := docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(raw)) != r.daemonID {
		return invalid
	}
	inspect := func() (bool, error) {
		raw, err := docker(ctx, "inspect", id, "--format", `{"Id":{{json .Id}},"Image":{{json .Config.Image}},"Proxy":{{json (index .Config.Labels "io.nerocd.proxy")}},"Daemon":{{json (index .Config.Labels "io.nerocd.daemon")}},"Server":{{json (index .Config.Labels "io.nerocd.server")}},"Destination":{{json (index .Config.Labels "io.nerocd.destination")}},"Runner":{{json (index .Config.Labels "io.nerocd.runner")}},"State":{{json .State}}}`)
		var proof struct {
			ID, Image, Proxy, Daemon, Server, Destination, Runner string
			State                                                 struct{ Running, Paused bool }
		}
		if err != nil || json.Unmarshal(raw, &proof) != nil || proof.ID != id || proof.Image != hostingproxy.Image || proof.Proxy != r.proxy.ID || proof.Daemon != r.daemonID || proof.Server != r.proxy.ServerID || proof.Destination != r.proxy.DestinationID || proof.Runner != r.proxy.RunnerID || !proof.State.Running {
			return false, invalid
		}
		return proof.State.Paused, nil
	}
	actual, err := inspect()
	if err != nil {
		return err
	}
	if actual != paused {
		action := "unpause"
		if paused {
			action = "pause"
		}
		if _, err = docker(ctx, "container", action, id); err != nil {
			return invalid
		}
		if actual, err = inspect(); err != nil || actual != paused {
			return invalid
		}
	}
	return nil
}

func applicationDatabaseStorageRecoveryIdentity(action, operation, run, proxy, failedProxy string) bool {
	return action == "resume_predecessor" && run == "storage_recover_"+operation && proxy == "" || action == "complete_replacement" && proxy == "appdb_storage_proxy_"+operation && run == "proxy_"+proxy && proxy != failedProxy
}

func TestApplicationDatabaseStorageRecoveryTaskIdentity(t *testing.T) {
	for _, test := range []struct {
		name, action, run, proxy, failed string
		want                             bool
	}{
		{"resume", "resume_predecessor", "storage_recover_original", "", "", true},
		{"actual_proxy_dispatch", "complete_replacement", "proxy_appdb_storage_proxy_original", "appdb_storage_proxy_original", "failed_p", true},
		{"invented_run_prefix", "complete_replacement", "run_appdb_storage_proxy_original", "appdb_storage_proxy_original", "failed_p", false},
		{"failed_proxy_reuse", "complete_replacement", "proxy_appdb_storage_proxy_original", "appdb_storage_proxy_original", "appdb_storage_proxy_original", false},
		{"foreign_proxy", "complete_replacement", "proxy_appdb_storage_proxy_other", "appdb_storage_proxy_other", "failed_p", false},
		{"resume_with_proxy", "resume_predecessor", "storage_recover_original", "failed_p", "failed_p", false},
		{"foreign_resume", "resume_predecessor", "storage_recover_other", "", "", false},
		{"unknown_action", "retry", "storage_recover_original", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if applicationDatabaseStorageRecoveryIdentity(test.action, "original", test.run, test.proxy, test.failed) != test.want {
				t.Fatal("recovery association differs from actual task dispatch or reuses failed identity")
			}
		})
	}
}

// Admission rereads both public reviews after recovery and maintenance. In
// particular it must not reuse the failed B readiness path or its stale route CAS.
func (s *applicationDatabaseStorageFixture) requestResumedReplacement(f *databaseRunnerFixture, resource domain.Resource, previous domain.HostingOperation) domain.HostingOperation {
	f.t.Helper()
	deployment := domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: previous.Snapshot.RuntimeConfiguration.Generation, IdempotencyKey: "storage_resume_redeploy_c", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}
	input := s.deployment(f, f.routes.deployment(deployment))
	return databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", input, 202)
}

func TestApplicationDatabaseStorageResumedReplacementAdmission(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			proxy := domain.HostingProxy{ID: "proxy", ServerID: "server", DestinationID: "destination", RunnerID: "runner", ControlProjectID: "project", Enabled: true, DaemonID: "daemon", LoadedGeneration: 7, DesiredGeneration: 7}
			route := domain.HostingRoute{ID: "route", ResourceID: "application", Version: 4, SelectedReleaseOperationID: "resumed_a", Hostname: "connected-" + engine + ".example.test", PathPrefix: "/", Enabled: true}
			loaded := "loaded_after_recovery"
			options := domain.HostingRouteSwitchOptions{ResourceID: route.ResourceID, LoadedGeneration: &proxy.LoadedGeneration, LoadedProxyOperationID: &loaded, Available: true, Reason: "available", Routes: []domain.HostingRouteSwitchRoute{{ID: route.ID, Version: route.Version, SelectedReleaseOperationID: route.SelectedReleaseOperationID, Hostname: route.Hostname, PathPrefix: route.PathPrefix}}, Targets: []domain.HostingRouteSwitchTarget{{OperationID: route.SelectedReleaseOperationID, Kind: "current_healthy"}}}
			var requests []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.Path)
				var body any
				switch req.URL.Path {
				case "/api/v1/servers/server/proxy":
					if req.Header.Get("Authorization") != "Bearer admin" {
						t.Error("proxy review lost admin authentication")
					}
					body = proxy
				case "/api/v1/resources/application/route-switches":
					body = options
				case "/api/v1/resources/application/storage-deployment-options":
					body = map[string]any{"application_database_storage": map[string]any{"version": applicationDatabaseStorageVersion, "strategy": "replacement_with_interruption", "expected_storage_generation": 3, "expected_storage_hash": "saved_storage_hash", "acknowledge_interruption_required": true}}
				case "/api/v1/resources/application/database-connection/deployments":
					if req.Method != "POST" {
						t.Error("replacement was not actually submitted")
					}
					var got struct {
						domain.HostingDeploymentInput
						Storage applicationDatabaseStorageAck `json:"application_database_storage"`
					}
					if json.NewDecoder(req.Body).Decode(&got) != nil {
						t.Error("invalid replacement request")
					}
					want := domain.HostingDeploymentInput{ExpectedGeneration: 11, ExpectedRuntimeConfigurationGeneration: 9, IdempotencyKey: "storage_resume_redeploy_c", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}, ApplicationDatabaseRouteContinuity: &domain.HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 7, ExpectedLoadedProxyOperationID: loaded, Routes: []domain.HostingRouteSwitchSelection{{RouteID: route.ID, ExpectedVersion: 4, ExpectedSelectedReleaseOperationID: "resumed_a"}}}}
					if !reflect.DeepEqual(got.HostingDeploymentInput, want) || got.Storage != (applicationDatabaseStorageAck{3, "saved_storage_hash", true}) {
						t.Error("post-recovery deployment lost fresh route/storage CAS, readiness or runtime generation")
					}
					w.WriteHeader(http.StatusAccepted)
					body = domain.HostingOperation{ID: "new_c"}
				default:
					t.Error("unexpected recovery replacement request")
					w.WriteHeader(404)
					return
				}
				if req.URL.Path != "/api/v1/servers/server/proxy" && req.Header.Get("Authorization") != "Bearer owner" {
					t.Error("public review/admission lost owner authentication")
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			f := &databaseRunnerFixture{t: t, server: server, client: server.Client(), token: "owner", adminToken: "admin", serverID: proxy.ServerID}
			f.routes = &applicationDatabaseRouteFixture{f: f, proxy: proxy, route: route, daemonID: proxy.DaemonID}
			s := &applicationDatabaseStorageFixture{Metadata: domain.HostingStorage{ResourceID: "application", Generation: 3, Hash: "saved_storage_hash"}}
			previous := domain.HostingOperation{ID: "resumed_a"}
			previous.Snapshot.RuntimeConfiguration.Generation = 9
			if s.requestResumedReplacement(f, domain.Resource{ID: "application", Generation: 11}, previous).ID != "new_c" {
				t.Fatal("replacement admission lost new operation identity")
			}
			want := []string{"GET /api/v1/servers/server/proxy", "GET /api/v1/resources/application/route-switches", "GET /api/v1/resources/application/storage-deployment-options", "POST /api/v1/resources/application/database-connection/deployments"}
			if !reflect.DeepEqual(requests, want) {
				t.Fatal("replacement skipped fresh public review or duplicated admission")
			}
		})
	}
}

func (s *applicationDatabaseStorageFixture) recoverFailure(f *databaseRunnerFixture, engine, failure string, resource domain.Resource, database domain.HostingDatabase, previous, op domain.HostingOperation, stage domain.HostingApplicationDatabaseStage, active domain.HostingDatabaseConnection, wire *applicationDatabaseRouteYieldWire) {
	f.t.Helper()
	r := f.routes
	candidate := op
	candidate.Result = &domain.HostingResult{ContainerID: stage.Candidate.ContainerID}
	data := applicationDatabaseStoragePayload(66)
	if failure == "readiness" {
		s.assertReadinessStopped(f, previous, candidate, stage)
	} else {
		s.assertTransition(f, previous, candidate)
		if applicationDatabaseUpload(f.t.Context(), nil, "", "", candidate, "GET", "retained_a", s.Files["retained_a"], "") != nil {
			f.t.Fatal("failed candidate lost retained upload before recovery")
		}
		if applicationDatabaseUpload(f.t.Context(), nil, "", "", candidate, "PUT", "candidate_write", data, "") != nil || applicationDatabaseHTTPRecord(f.t.Context(), candidate, "PUT", "candidate_write", "retained_candidate_before_recovery") != nil {
			f.t.Fatal("candidate data writes before recovery failed")
		}
	}
	s.Files["candidate_write"] = data
	action := "resume_predecessor"
	var failedProxy domain.HostingProxyOperationView
	if failure == "cutover" {
		action = "complete_replacement"
		r.awaitYieldedRoute(op)
		id := strings.TrimSpace(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/nerocd-proxy-"+r.proxy.ID+"$")))
		docker := func(ctx context.Context, args ...string) ([]byte, error) {
			return databaseRunnerDocker(ctx, nil, args...)
		}
		if applicationDatabaseStorageProxyPause(f.t.Context(), r, id, false, docker) != nil {
			f.t.Fatal("proxy fault target identity unavailable")
		}
		paused := false
		f.t.Cleanup(func() {
			if paused {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if applicationDatabaseStorageProxyPause(ctx, r, id, false, docker) != nil {
					f.t.Error("exact paused fixture proxy fallback unpause unconfirmed")
				}
			}
		})
		paused = true
		if applicationDatabaseStorageProxyPause(f.t.Context(), r, id, true, docker) != nil {
			f.t.Fatal("owned proxy unavailability injection failed")
		}
		f.start()
		deadline := time.Now().Add(120 * time.Second)
		for {
			failedProxy = databaseRunnerJSON[domain.HostingProxyOperationView](f, f.token, "GET", "/api/v1/resources/"+op.ResourceID+"/route-switches/"+op.ApplicationDatabaseRouteContinuity.ProxyOperationID, nil, 200)
			if failedProxy.ID == op.ApplicationDatabaseRouteContinuity.ProxyOperationID && (failedProxy.State == "failed" || failedProxy.State == "uncertain" || failedProxy.State == "refused") && (failedProxy.EngineOutcome == "refused" || failedProxy.EngineOutcome == "uncertain") {
				break
			}
			if time.Now().After(deadline) || f.process.status().Exited {
				f.t.Fatal("paused proxy did not retain an exact terminal unsuccessful switch")
			}
			select {
			case <-f.t.Context().Done():
				f.t.Fatal("proxy unavailability wait canceled")
			case <-time.After(100 * time.Millisecond):
			}
		}
		f.stop()
		if applicationDatabaseStorageProxyPause(f.t.Context(), r, id, false, docker) != nil {
			f.t.Fatal("exact fixture proxy restoration failed")
		}
		paused = false
		s.assertTransition(f, previous, candidate)
		if wire == nil {
			f.t.Fatal("failed switch missing original yield replay evidence")
		}
		select {
		case <-wire.replayed:
		default:
			f.t.Fatal("original lost yield ACK did not replay before failed P")
		}
	}
	deadline := time.Now().Add(180 * time.Second)
	var original applicationDatabaseStoragePublic
	for {
		original = s.public(f, op.ID)
		if slices.Contains(original.StorageState.RecoveryActions, action) {
			break
		}
		if original.StorageState.RecoveryState != "none" || time.Now().After(deadline) {
			f.t.Fatal("exact storage failure did not offer a fresh recovery action")
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("storage recovery offer wait canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
	state := original.StorageState
	if state.StageHash != stage.Hash || state.CandidateHash != stage.Candidate.Hash {
		f.t.Fatal("public recovery stage/candidate differ from actual retained candidate")
	}
	input := map[string]any{"action": action, "operation_id": op.ID, "stage_hash": state.StageHash, "candidate_hash": state.CandidateHash, "expected_membership_revision": active.MembershipRevision, "idempotency_key": "storage_" + failure + "_recovery", "storage_state_hash": state.StateHash, "application_database_storage": applicationDatabaseStorageAck{s.Metadata.Generation, s.Metadata.Hash, true}}
	path := "/api/v1/resources/" + op.ResourceID + "/database-connection/recovery"
	databaseRunnerJSON[json.RawMessage](f, "", "POST", path, input, 401)
	for _, key := range []string{"stage_hash", "candidate_hash", "storage_state_hash"} {
		stale := maps.Clone(input)
		stale[key] = "sha256:" + strings.Repeat("0", 64)
		databaseRunnerJSON[json.RawMessage](f, f.token, "POST", path, stale, 409)
	}
	var browser *applicationDatabaseBrowser
	var accepted applicationDatabaseStoragePublic
	if os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER") == "1" && (engine == "postgresql" && failure == "readiness" || engine == "redis" && failure == "cutover") {
		profile := "storage_resume"
		if failure == "cutover" {
			profile = "storage_complete"
		}
		browser = f.startApplicationDatabaseBrowser(resource, database, engine, &applicationDatabaseBrowserRecovery{Action: action, OperationID: op.ID, Profile: profile, Revision: active.MembershipRevision, Expected: input, Original: op})
		accepted.ID = browser.await("recover").Operation.ID
		browser.mu.Lock()
		input = maps.Clone(browser.recovery.Submitted)
		browser.mu.Unlock()
	} else {
		accepted = databaseRunnerJSON[applicationDatabaseStoragePublic](f, f.token, "POST", path, input, 202)
	}
	queued := s.public(f, op.ID)
	if accepted.ID != op.ID || queued.RunID != original.RunID || queued.State != original.State || !reflect.DeepEqual(queued.Result, original.Result) || queued.StorageState.RecoveryAction != action || queued.StorageState.RecoveryState != "queued" {
		f.t.Fatal("recovery admission replaced original O/result or lacks independent queued progress")
	}
	var run, proxyID string
	if f.database.QueryRow(f.t.Context(), `SELECT run_id,coalesce(proxy_operation_id,'') FROM hosting_application_database_storage_recoveries WHERE operation_id=$1 AND action=$2`, op.ID, action).Scan(&run, &proxyID) != nil {
		f.t.Fatal("recovery missing immutable same-operation association")
	}
	if !applicationDatabaseStorageRecoveryIdentity(action, op.ID, run, proxyID, failedProxy.ID) {
		f.t.Fatal("recovery task or fresh proxy identity differs from deterministic association")
	}
	replay := databaseRunnerJSON[applicationDatabaseStoragePublic](f, f.token, "POST", path, input, 202)
	if replay.ID != op.ID {
		f.t.Fatal("identical recovery request did not retain original operation")
	}
	if browser != nil {
		select {
		case <-browser.recoveryObserved:
		case <-browser.failed:
			f.t.Fatal("browser did not observe queued recovery")
		case <-browser.ctx.Done():
			f.t.Fatal("browser queued recovery observation timed out")
		}
	}
	f.start()
	deadline = time.Now().Add(180 * time.Second)
	for {
		current := s.public(f, op.ID)
		if current.StorageState.RecoveryAction != action {
			f.t.Fatal("recovery progress lost immutable action")
		}
		if current.StorageState.RecoveryState == "succeeded" {
			break
		}
		if current.StorageState.RecoveryState != "queued" && current.StorageState.RecoveryState != "running" || time.Now().After(deadline) || f.process.status().Exited {
			f.t.Fatal("storage recovery did not independently prove successful settlement")
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("storage recovery wait canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
	f.stop()
	selected := previous
	var maintained domain.HostingOperation
	if action == "resume_predecessor" {
		settled := s.public(f, op.ID)
		if settled.State != original.State || !reflect.DeepEqual(settled.Result, original.Result) {
			f.t.Fatal("resume recovery rewrote original failed operation outcome")
		}
		f.applicationContainer(previous)
		// Successful inventories, rather than an inspect error, prove the exact
		// failed B is absent while A uses the original retained volume.
		for _, filter := range []string{"id=" + stage.Candidate.ContainerID, "label=io.nerocd.operation=" + op.ID} {
			if strings.TrimSpace(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", filter))) != "" {
				f.t.Fatal("resume left the proven-stopped failed candidate container behind")
			}
		}
		s.assertFiles(f, previous)
		r.assertSelected(previous)
		r.record(f.t.Context(), previous, "GET", "a", "retained_a")
		r.record(f.t.Context(), previous, "GET", "candidate_write", "retained_candidate_before_recovery")
		writers := strings.Fields(string(f.docker("container", "ls", "--quiet", "--no-trunc", "--filter", "volume="+s.Plan.VolumeName)))
		if len(writers) != 1 || writers[0] != previous.Result.ContainerID {
			f.t.Fatal("resumed predecessor is not the sole running volume writer")
		}
		resumed := f.connection(op.ResourceID)
		if resumed.State != "active" || resumed.HoldOperationID != "" || resumed.MembershipRevision <= active.MembershipRevision {
			f.t.Fatal("resumed connection lacks fresh endpoint membership revision or retained holds")
		}
		s.assertResumeMembership(f, op, previous, stage, run, active.MembershipRevision, resumed.MembershipRevision)
		oldDatabase, err := f.operation(f.t.Context(), op.ApplicationDatabase.DatabaseHealthy.OperationID)
		if err != nil || oldDatabase.Plan == nil || oldDatabase.Result == nil {
			f.t.Fatal("resumed database maintenance predecessor unavailable")
		}
		databaseVolume, credentials := f.volumeProof(oldDatabase), f.credentialMetadata(database)
		maintenance := databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+database.Resource.ID+"/database/maintenance", domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: database.Resource.Generation, ExpectedMembershipRevision: resumed.MembershipRevision, IdempotencyKey: "storage_resume_database_maintenance"}, 202)
		f.registerRouteOperationCleanup(maintenance.ID)
		f.start()
		maintained = f.awaitConnectionOperation(maintenance.ID)
		f.stop()
		if f.volumeProof(maintained) != databaseVolume || f.credentialMetadata(database) != credentials {
			f.t.Fatal("database maintenance after resume changed retained database identity")
		}
		proof := s.public(f, op.ID)
		if maintained.Plan.Database.Network == nil || maintained.Plan.Database.Network.MembershipRevision != resumed.MembershipRevision || len(maintained.Plan.Database.Network.Members) != 1 {
			f.t.Fatal("database maintenance did not compile resumed membership revision")
		}
		member := maintained.Plan.Database.Network.Members[0]
		actualApp := f.applicationContainer(previous)
		if member.ReceiptID != proof.StorageState.ReceiptID || member.ReceiptHash != proof.StorageState.StateHash || member.Endpoint.Target != op.ApplicationDatabase.Members[0].EndpointPlan.Target || member.Endpoint.EndpointID != actualApp.Endpoints[op.ApplicationDatabase.Binding.NetworkName].EndpointID {
			f.t.Fatal("database maintenance did not consume exact fresh recovery receipt and observed endpoint")
		}
		f.inspect(maintained)
		var members map[string]json.RawMessage
		if json.Unmarshal(f.docker("network", "inspect", previous.ApplicationDatabase.Origin.NetworkID, "--format", "{{json .Containers}}"), &members) != nil || len(members) != 2 || members[previous.Result.ContainerID] == nil || members[maintained.Result.ContainerID] == nil {
			f.t.Fatal("maintenance after resume did not preserve exact active application/database endpoints")
		}
		s.assertFiles(f, previous)
		r.record(f.t.Context(), previous, "GET", "candidate_write", "retained_candidate_before_recovery")
		r.record(f.t.Context(), previous, "PUT", "resumed_maintenance", "retained_after_resume_maintenance")
	} else {
		current, err := f.operation(f.t.Context(), op.ID)
		if err != nil || !applicationDatabaseCompleted(current) || current.Result.ContainerID != stage.Candidate.ContainerID || current.ApplicationDatabaseRouteContinuity.ProxyOperationID != failedProxy.ID {
			f.t.Fatal("forward recovery did not finalize the exact original candidate and failed-P identity")
		}
		p2 := databaseRunnerJSON[domain.HostingProxyOperationView](f, f.token, "GET", "/api/v1/resources/"+op.ResourceID+"/route-switches/"+proxyID, nil, 200)
		old := databaseRunnerJSON[domain.HostingProxyOperationView](f, f.token, "GET", "/api/v1/resources/"+op.ResourceID+"/route-switches/"+failedProxy.ID, nil, 200)
		if p2.ID != proxyID || p2.State != "applied" || p2.EngineOutcome != "applied" || old.State != failedProxy.State || old.EngineOutcome != failedProxy.EngineOutcome {
			f.t.Fatal("forward recovery lost immutable failed P or exact applied P2")
		}
		r.assertSelected(current)
		s.assertFiles(f, current)
		r.record(f.t.Context(), current, "GET", "a", "retained_a")
		r.record(f.t.Context(), current, "GET", "candidate_write", "retained_candidate_before_recovery")
		s.write(f, current, "recovered", 44)
		f.assertApplicationDatabaseRetirement(current, previous)
		selected = current
	}
	var leases, targets int
	if f.database.QueryRow(f.t.Context(), `SELECT count(*) FROM run_leases l JOIN task_runs r ON r.id=l.run_id WHERE l.run_id=$1 AND l.status='succeeded' AND l.attempt=1 AND l.completed_at IS NOT NULL AND r.status='succeeded' AND r.finished_at IS NOT NULL AND l.runner_id=$2 AND NOT EXISTS(SELECT 1 FROM run_leases original WHERE original.run_id=$3 AND (original.id=l.id OR original.fence=l.fence))`, run, op.Snapshot.RunnerID, op.RunID).Scan(&leases) != nil || leases != 1 {
		f.t.Fatal("storage recovery lacks exact fresh succeeded run/lease/fence independent of original prepare authority")
	}
	if action == "resume_predecessor" {
		if f.database.QueryRow(f.t.Context(), `SELECT count(*) FROM hosting_task_targets t JOIN task_runs r ON r.id=t.run_id WHERE t.run_id=$1 AND t.operation_id=$2 AND t.binding_id=$3 AND t.runner_id=$4 AND r.run_spec->>'hosting_operation_id'=t.operation_id AND r.run_spec->>'hosting_mode'=t.mode`, run, op.ID, op.BindingID, op.Snapshot.RunnerID).Scan(&targets) != nil || targets != 1 {
			f.t.Fatal("resume recovery run is not targeted at exact original application")
		}
	} else if f.database.QueryRow(f.t.Context(), `SELECT count(*) FROM hosting_proxy_task_targets WHERE run_id=$1 AND operation_id=$2 AND proxy_id=$3 AND runner_id=$4 AND mode='apply'`, run, proxyID, r.proxy.ID, op.Snapshot.RunnerID).Scan(&targets) != nil || targets != 1 {
		f.t.Fatal("forward recovery run is not targeted at exact fresh proxy")
	}
	r.assertUnrelated()
	if browser != nil {
		browser.advance("finish", f.connection(op.ResourceID).MembershipRevision, selected, map[string]string{"a": "retained_a", "candidate_write": "retained_candidate_before_recovery"})
		browser.awaitFinished()
		fmt.Println("DATABASE_CONNECTION_STORAGE_RECOVERY_UI_PASS " + engine + " " + action)
	}
	if action == "resume_predecessor" {
		// Recovery UI observes the resumed A first. A fresh API deployment then
		// proves the recovered membership can retire A and settle new C normally.
		request := s.requestResumedReplacement(f, resource, previous)
		f.registerRouteOperationCleanup(request.ID)
		revision := f.connection(resource.ID).MembershipRevision
		r.recoverYield(request, previous, revision)
		current := f.awaitConnectionOperation(request.ID)
		f.stop()
		if current.ID == op.ID || current.ID == previous.ID || current.Result.ContainerID == stage.Candidate.ContainerID || current.Result.ContainerID == previous.Result.ContainerID || current.Result.PreviousContainerID != previous.Result.ContainerID {
			f.t.Fatal("resumed replacement reused failed B or did not replace exact recovered A")
		}
		f.assertApplicationDatabaseRetirement(current, previous)
		f.exactMembers(current, maintained)
		r.assertSelected(current)
		s.assertFiles(f, current)
		for key, value := range map[string]string{"a": "retained_a", "candidate_write": "retained_candidate_before_recovery", "resumed_maintenance": "retained_after_resume_maintenance"} {
			r.record(f.t.Context(), current, "GET", key, value)
		}
		s.write(f, current, "retained_c", 55)
		r.record(f.t.Context(), current, "PUT", "after_resume_redeploy", "retained_after_resume_redeploy")
		r.record(f.t.Context(), current, "GET", "after_resume_redeploy", "retained_after_resume_redeploy")
		originalAfter := s.public(f, op.ID)
		if originalAfter.State != original.State || !reflect.DeepEqual(originalAfter.Result, original.Result) || originalAfter.StorageState.RecoveryState != "succeeded" {
			f.t.Fatal("new deployment rewrote original failure or recovery settlement")
		}
		f.assertConnectionJournalRetired(current.ID)
		r.assertUnrelated()
		f.t.Log("resumed A replaced by fresh C with exact retained database/upload identity and trusted HTTPS writes")
	}
}

func (s *applicationDatabaseStorageFixture) assertResumeMembership(f *databaseRunnerFixture, op, previous domain.HostingOperation, stage domain.HostingApplicationDatabaseStage, run string, before, after int64) {
	f.t.Helper()
	if len(op.ApplicationDatabase.Members) != 1 || after != before+1 {
		f.t.Fatal("resumed membership did not advance exactly once")
	}
	head, err := sqlcgen.New(f.database).ReadDatabaseMembershipHead(f.t.Context(), op.ApplicationDatabase.Binding.DatabaseBindingID)
	var members []json.RawMessage
	if err != nil || head.Revision != after || json.Unmarshal(head.Members, &members) != nil || len(members) != 1 {
		f.t.Fatal("resumed membership head unavailable")
	}
	var current struct {
		Resume *struct {
			Version             string          `json:"version"`
			OperationID         string          `json:"operation_id"`
			MembershipRevision  int64           `json:"membership_revision"`
			OriginalReceiptHash string          `json:"original_receipt_hash"`
			WriterReceipt       json.RawMessage `json:"writer_receipt"`
		} `json:"resume"`
	}
	var raw []byte
	if json.Unmarshal(members[0], &current) != nil || current.Resume == nil || f.database.QueryRow(f.t.Context(), `SELECT request FROM hosting_application_database_storage_writers WHERE operation_id=$1 AND phase='resumed'`, op.ID).Scan(&raw) != nil {
		f.t.Fatal("resumed writer or fresh membership receipt absent")
	}
	defer clear(raw)
	var writer struct {
		domain.HostingAuthority
		Receipt json.RawMessage `json:"receipt"`
	}
	var receipt struct {
		Version             string                                            `json:"version"`
		Phase               string                                            `json:"phase"`
		PredecessorEndpoint *domain.HostingDatabaseBindingEndpointObservation `json:"predecessor_endpoint"`
	}
	if json.Unmarshal(raw, &writer) != nil || json.Unmarshal(writer.Receipt, &receipt) != nil || writer.OperationID != op.ID || writer.RunID != run || writer.LeaseID == "" || writer.Fence == "" || writer.Attempt != 1 || receipt.Version != applicationDatabaseStorageVersion || receipt.Phase != "resumed" || receipt.PredecessorEndpoint == nil {
		f.t.Fatal("resumed receipt lacks exact fresh recovery authority or actual endpoint observation")
	}
	// Decode the narrowed fields by their wire names; the full immutable receipt
	// is compared below without inventing a replacement proof or hash.
	var wireReceipt struct {
		ReceiptID string `json:"receipt_id"`
		Hash      string `json:"hash"`
	}
	if json.Unmarshal(writer.Receipt, &wireReceipt) != nil {
		f.t.Fatal("resumed receipt identity invalid")
	}
	old := op.ApplicationDatabase.Members[0]
	r := current.Resume
	var a, b map[string]any
	if json.Unmarshal(r.WriterReceipt, &a) != nil || json.Unmarshal(writer.Receipt, &b) != nil || !reflect.DeepEqual(a, b) || r.Version != applicationDatabaseStorageVersion || r.OperationID != op.ID || r.MembershipRevision != after || r.OriginalReceiptHash != old.Receipt.Hash {
		f.t.Fatal("fresh membership does not pin original attachment and exact resumed receipt")
	}
	var actual, historical map[string]any
	oldRaw, _ := json.Marshal(old)
	if json.Unmarshal(members[0], &actual) != nil || json.Unmarshal(oldRaw, &historical) != nil {
		f.t.Fatal("resumed membership history unavailable")
	}
	delete(actual, "resume")
	delete(historical, "resume")
	if !reflect.DeepEqual(actual, historical) {
		f.t.Fatal("resume rewrote historical application, attachment or database evidence")
	}
	endpoint := receipt.PredecessorEndpoint.Endpoint
	inspection := f.applicationContainer(previous)
	if !receipt.PredecessorEndpoint.Present || endpoint.Target != old.EndpointPlan.Target || endpoint.EndpointID == old.Receipt.Observation.Endpoint.EndpointID || endpoint.EndpointID != inspection.Endpoints[op.ApplicationDatabase.Binding.NetworkName].EndpointID {
		f.t.Fatal("resumed endpoint differs from exact restarted container or reused stale attachment")
	}
	public := s.public(f, op.ID)
	if public.StorageState.ReceiptID != wireReceipt.ReceiptID || public.StorageState.StateHash != wireReceipt.Hash {
		f.t.Fatal("public resumed state differs from durable writer receipt")
	}
	var membership []byte
	a, b = nil, nil
	if f.database.QueryRow(f.t.Context(), `SELECT body FROM hosting_application_database_storage_membership_receipts WHERE database_binding_id=$1 AND revision=$2 AND operation_id=$3`, op.ApplicationDatabase.Binding.DatabaseBindingID, after, op.ID).Scan(&membership) != nil || json.Unmarshal(membership, &a) != nil || json.Unmarshal(members[0], &b) != nil || !reflect.DeepEqual(a, b) {
		f.t.Fatal("resumed head lacks separate exact immutable membership receipt")
	}
	retained, err := sqlcgen.New(f.database).ReadApplicationDatabaseStage(f.t.Context(), op.ID)
	var oldStage domain.HostingApplicationDatabaseStage
	if err != nil || json.Unmarshal(retained.Stage, &oldStage) != nil || !reflect.DeepEqual(oldStage, stage) || databaseUpgradeAuthorityLogCheck(f.logs.snapshot(), writer.HostingAuthority, f.protected) != nil {
		f.t.Fatal("resume changed original stage or exposed recovery authority")
	}
}

func (s *applicationDatabaseStorageFixture) bootstrap(f *databaseRunnerFixture, resource domain.Resource, previous domain.HostingOperation, deployment domain.HostingDeploymentInput) domain.HostingOperation {
	f.t.Helper()
	s.Metadata = databaseRunnerJSON[domain.HostingStorage](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/storage", domain.HostingStorageInput{RequestID: domain.HostingDocumentHash(struct{ Resource, Profile string }{resource.ID, "connected_storage_success"})[7:39], ExpectedResourceGeneration: resource.Generation, MountPath: "/data/uploads"}, 201)
	if s.Metadata.Validate() != nil || s.Metadata.ResourceID != resource.ID || s.Metadata.BindingID != previous.BindingID || s.Metadata.MountPath != "/data/uploads" {
		f.t.Fatal("connected upload metadata differs from exact application binding")
	}
	f.stop()
	deployment.ExpectedRuntimeConfigurationGeneration = previous.Snapshot.RuntimeConfiguration.Generation
	deployment.IdempotencyKey = "connected_storage_bootstrap"
	request := databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", s.deployment(f, deployment), 202)
	f.registerRouteOperationCleanup(request.ID)
	f.start()
	current := f.awaitConnectionOperation(request.ID)
	if current.Plan.Storage == nil || current.Plan.Storage.Reference != (domain.HostingStorageRef{StorageID: s.Metadata.ID, Generation: s.Metadata.Generation, Hash: s.Metadata.Hash}) || current.Plan.Storage.VolumeName != s.Metadata.VolumeName || current.Plan.Storage.CreationOperationID != current.ID {
		f.t.Fatal("bootstrap did not materialize exact application-owned upload volume")
	}
	copy := *current.Plan.Storage
	s.Plan = &copy
	s.VolumeProof = f.volumeProof(current)
	var volume struct{ Labels map[string]string }
	materialization := domain.HostingStorageMaterialization{CreationOperationID: current.ID, CreationPlanHash: current.Plan.Hash, CreationNonce: copy.CreationNonce, DaemonID: copy.DaemonID}
	if json.Unmarshal([]byte(s.VolumeProof), &volume) != nil || !maps.Equal(volume.Labels, domain.ExpectedHostingStorageLabels(s.Metadata, materialization)) {
		f.t.Fatal("initial upload volume labels differ from exact owned creation provenance")
	}
	f.applicationContainer(current)
	f.assertApplicationDatabaseRetirement(current, previous)
	return current
}

func applicationDatabaseStorageStopBeforeStart(oldRunning, nextRunning bool, stopped, started string) bool {
	a, errA := time.Parse(time.RFC3339Nano, stopped)
	b, errB := time.Parse(time.RFC3339Nano, started)
	return !oldRunning && nextRunning && errA == nil && errB == nil && !a.IsZero() && !b.IsZero() && !a.After(b)
}

func (s *applicationDatabaseStorageFixture) assertTransition(f *databaseRunnerFixture, previous, candidate domain.HostingOperation) {
	f.t.Helper()
	if s.Plan == nil || candidate.Plan == nil || candidate.Plan.Storage == nil || *candidate.Plan.Storage != *s.Plan || previous.Plan == nil || previous.Plan.Storage == nil || *previous.Plan.Storage != *s.Plan {
		f.t.Fatal("one-writer transition changed exact retained volume origin")
	}
	old, stopped, _ := s.inspectWriter(f, previous)
	next, _, started := s.inspectWriter(f, candidate)
	if !applicationDatabaseStorageStopBeforeStart(old.State.Running, next.State.Running, stopped, started) {
		f.t.Fatal("previous application was not independently proven stopped before candidate start")
	}
	writers := strings.Fields(string(f.docker("container", "ls", "--quiet", "--no-trunc", "--filter", "volume="+s.Plan.VolumeName)))
	if len(writers) != 1 || writers[0] != candidate.Result.ContainerID {
		f.t.Fatal("retained upload volume has an extra or missing running writer")
	}
}

func (s *applicationDatabaseStorageFixture) inspectWriter(f *databaseRunnerFixture, op domain.HostingOperation) (databaseRunnerContainer, string, string) {
	f.t.Helper()
	if s.Plan == nil || op.Plan == nil || op.Plan.Storage == nil || *op.Plan.Storage != *s.Plan || op.Result == nil || op.Release == nil {
		f.t.Fatal("writer observation lacks exact retained plan and container identity")
	}
	raw := f.docker("container", "inspect", op.Result.ContainerID, "--format", `{"Id":{{json .Id}},"Image":{{json .Image}},"Config":{"Labels":{{json .Config.Labels}}},"State":{{json .State}},"Mounts":{{json .Mounts}}}`)
	var c databaseRunnerContainer
	var state struct {
		State struct{ FinishedAt, StartedAt string }
	}
	if json.Unmarshal(raw, &c) != nil || json.Unmarshal(raw, &state) != nil || c.ID != op.Result.ContainerID || c.Image != op.Release.ImageID || c.Config.Labels["io.nerocd.binding"] != op.BindingID || c.Config.Labels["io.nerocd.operation"] != op.ID || c.Config.Labels["io.nerocd.plan"] != op.Plan.Hash || c.Config.Labels["io.nerocd.release"] != op.Release.Hash || !applicationDatabaseStorageMount(c, s.Plan) {
		f.t.Fatal("one-writer observation lacks exact container, image, labels or mount")
	}
	return c, state.State.FinishedAt, state.State.StartedAt
}

func (s *applicationDatabaseStorageFixture) assertReadinessStopped(f *databaseRunnerFixture, previous, candidate domain.HostingOperation, stage domain.HostingApplicationDatabaseStage) {
	f.t.Helper()
	if candidate.State != "failed" || candidate.ApplicationDatabaseYield != nil || candidate.CompleteReceipt != nil || !candidate.MutationHold || stage.Phase != "attached" {
		f.t.Fatal("storage readiness failure lost exact failed operation or safety hold")
	}
	old, oldFinished, _ := s.inspectWriter(f, previous)
	next, finished, started := s.inspectWriter(f, candidate)
	stopA, errA := time.Parse(time.RFC3339Nano, oldFinished)
	startB, errB := time.Parse(time.RFC3339Nano, started)
	stopB, errC := time.Parse(time.RFC3339Nano, finished)
	if old.State.Running || next.State.Running || errA != nil || errB != nil || errC != nil || stopA.IsZero() || startB.IsZero() || stopB.IsZero() || stopA.After(startB) || startB.After(stopB) {
		f.t.Fatal("storage readiness failure did not independently prove A then B stopped")
	}
	if strings.TrimSpace(string(f.docker("container", "ls", "--quiet", "--no-trunc", "--filter", "volume="+s.Plan.VolumeName))) != "" || f.volumeProof(candidate) != s.VolumeProof {
		f.t.Fatal("failed readiness retained a running volume writer or changed volume identity")
	}
	var total int
	if f.database.QueryRow(f.t.Context(), `SELECT count(*) FROM hosting_application_database_storage_writers WHERE operation_id=$1`, candidate.ID).Scan(&total) != nil || total != 4 {
		f.t.Fatal("failed readiness lacks exact ordered stop and failure receipt cardinality")
	}
	var authority domain.HostingAuthority
	prior, observation, lastReceipt := "", "", ""
	for i, phase := range []string{"stop_intent", "stopped", "candidate_stopped", "replacement_failed"} {
		var raw []byte
		if f.database.QueryRow(f.t.Context(), `SELECT request FROM hosting_application_database_storage_writers WHERE operation_id=$1 AND phase=$2`, candidate.ID, phase).Scan(&raw) != nil || len(raw) > 32768 {
			f.t.Fatal("failed readiness writer receipt unavailable")
		}
		var request struct {
			domain.HostingAuthority
			Receipt struct {
				Version           string                   `json:"version"`
				ReceiptID         string                   `json:"receipt_id"`
				Phase             string                   `json:"phase"`
				PlanHash          string                   `json:"plan_hash"`
				StorageRef        domain.HostingStorageRef `json:"storage_ref"`
				PreviousStateHash string                   `json:"previous_state_hash"`
				StageHash         string                   `json:"stage_hash"`
				CandidateHash     string                   `json:"candidate_hash"`
				PredecessorHash   string                   `json:"predecessor_hash"`
				ObservationHash   string                   `json:"observation_hash"`
				Hash              string                   `json:"hash"`
			} `json:"receipt"`
		}
		err := json.Unmarshal(raw, &request)
		clear(raw)
		receipt := request.Receipt
		if i == 0 {
			authority = request.HostingAuthority
		}
		if err != nil || request.HostingAuthority != authority || authority.OperationID != candidate.ID || authority.RunID != candidate.RunID || authority.LeaseID == "" || authority.Fence == "" || authority.Attempt < 1 || receipt.Version != applicationDatabaseStorageVersion || receipt.Phase != phase || receipt.ReceiptID == "" || receipt.Hash == "" || receipt.PlanHash != candidate.Plan.Hash || receipt.StorageRef != s.Plan.Reference || receipt.PredecessorHash != domain.HostingDocumentHash(candidate.Plan.PreviousHealthy) || receipt.PreviousStateHash != prior {
			f.t.Fatal("failed readiness stop/failure receipt lost exact provenance or hash chain")
		}
		if i >= 2 && (receipt.StageHash != stage.Hash || receipt.CandidateHash != stage.Candidate.Hash || receipt.ObservationHash == "") {
			f.t.Fatal("failed readiness receipt does not identify the exact stopped candidate")
		}
		if i == 2 {
			observation = receipt.ObservationHash
		}
		if i == 3 && receipt.ObservationHash != observation {
			f.t.Fatal("replacement failure does not retain exact candidate-stop observation")
		}
		prior, lastReceipt = receipt.Hash, receipt.ReceiptID
	}
	public := s.public(f, candidate.ID)
	phase, err := f.applicationDatabasePhaseAuthority(authority.LeaseID)
	if err != nil || phase.HostingAuthority != authority || phase.Mode != "deploy" || phase.LeaseStatus != "failed" || phase.TaskStatus != "failed" || phase.CompletionKey != lastReceipt || public.StorageState.StateHash != prior || public.StorageState.ReceiptID != lastReceipt || databaseUpgradeAuthorityLogCheck(f.logs.snapshot(), authority, f.protected) != nil {
		f.t.Fatal("failed readiness did not settle original failed task under exact failure receipt")
	}
	f.assertConnectionJournalRetired(candidate.ID)
}

func (s *applicationDatabaseStorageFixture) deployment(f *databaseRunnerFixture, input any) any {
	f.t.Helper()
	options := databaseRunnerJSON[struct {
		Review *struct {
			Version                         string `json:"version"`
			Strategy                        string `json:"strategy"`
			ExpectedStorageGeneration       int64  `json:"expected_storage_generation"`
			ExpectedStorageHash             string `json:"expected_storage_hash"`
			AcknowledgeInterruptionRequired bool   `json:"acknowledge_interruption_required"`
		} `json:"application_database_storage"`
	}](f, f.token, "GET", "/api/v1/resources/"+s.Metadata.ResourceID+"/storage-deployment-options", nil, 200)
	r := options.Review
	if r == nil || r.Version != applicationDatabaseStorageVersion || r.Strategy != "replacement_with_interruption" || r.ExpectedStorageGeneration != s.Metadata.Generation || r.ExpectedStorageHash != s.Metadata.Hash || !r.AcknowledgeInterruptionRequired {
		f.t.Fatal("public connected storage review differs from exact saved metadata")
	}
	raw, err := json.Marshal(input)
	var out map[string]any
	if err != nil || json.Unmarshal(raw, &out) != nil {
		f.t.Fatal("connected storage deployment request unavailable")
	}
	out["application_database_storage"] = applicationDatabaseStorageAck{s.Metadata.Generation, s.Metadata.Hash, true}
	return out
}

func applicationDatabaseStoragePayload(seed byte) []byte {
	data := make([]byte, 65537)
	for i := range data {
		data[i] = byte(i*31) ^ seed
	}
	return data
}

// The real Docker cp archive is inspected in memory. No archive paths are
// extracted and neither the request nor response can choose a host path.
func applicationDatabaseUploadArchive(raw, expected []byte) error {
	invalid := errors.New("upload archive identity, bounds or metadata invalid")
	if len(raw) > 1<<20 || len(expected) != 65537 {
		return invalid
	}
	reader := tar.NewReader(bytes.NewReader(raw))
	h, err := reader.Next()
	if err != nil || h.Typeflag != tar.TypeReg || h.Name != "retained_a" || h.Linkname != "" || h.Mode != 0640 || h.Uid != 12345 || h.Gid != 12346 || h.Size != int64(len(expected)) {
		return invalid
	}
	data, err := io.ReadAll(io.LimitReader(reader, 65538))
	defer clear(data)
	if err != nil || len(data) != len(expected) || sha256.Sum256(data) != sha256.Sum256(expected) {
		return invalid
	}
	if _, err := reader.Next(); err != io.EOF {
		return invalid
	}
	return nil
}

func applicationDatabaseUploadDirectoryArchive(raw []byte, files map[string][]byte) error {
	invalid := errors.New("upload directory archive identity, bounds or metadata invalid")
	if len(raw) > 1<<20 || len(files) < 1 || len(files) > 3 {
		return invalid
	}
	reader := tar.NewReader(bytes.NewReader(raw))
	directory, seen := false, map[string]bool{}
	for range len(files) + 2 {
		h, err := reader.Next()
		if err == io.EOF {
			if directory && len(seen) == len(files) {
				return nil
			}
			return invalid
		}
		if err != nil || h.Linkname != "" || h.Uid != 12345 || h.Gid != 12346 {
			return invalid
		}
		if h.Typeflag == tar.TypeDir && h.Name == "uploads/" && h.Mode == 0750 && !directory {
			directory = true
			continue
		}
		key := strings.TrimPrefix(h.Name, "uploads/")
		data, exists := files[key]
		if !exists || seen[key] || h.Name != "uploads/"+key || h.Typeflag != tar.TypeReg || h.Mode != 0640 || h.Size != int64(len(data)) || len(data) != 65537 {
			return invalid
		}
		actual, err := io.ReadAll(io.LimitReader(reader, 65538))
		match := err == nil && len(actual) == len(data) && sha256.Sum256(actual) == sha256.Sum256(data)
		clear(actual)
		if !match {
			return invalid
		}
		seen[key] = true
	}
	return invalid
}

func applicationDatabaseStorageMount(c databaseRunnerContainer, plan *domain.HostingStoragePlan) bool {
	if plan == nil {
		return len(c.Mounts) == 0
	}
	if len(c.Mounts) != 1 || plan.MountPath != "/data/uploads" || plan.VolumeName == "" {
		return false
	}
	m := c.Mounts[0]
	return m.Type == "volume" && m.Name == plan.VolumeName && m.Destination == plan.MountPath && m.RW
}

func applicationDatabaseUpload(ctx context.Context, roots *x509.CertPool, hostname, fingerprint string, op domain.HostingOperation, method, key string, expected []byte, target string) error {
	invalid := errors.New("connected upload identity, trust or content invalid")
	if op.Result == nil || len(op.Result.ContainerID) != 64 || strings.Trim(op.Result.ContainerID, "0123456789abcdef") != "" || len(expected) != 65537 || len(key) == 0 || len(key) > 64 || strings.Trim(key, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" || method != "GET" && method != "PUT" {
		return invalid
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	url := "http://127.0.0.1:" + strconv.Itoa(op.Snapshot.HostPort) + "/uploads/" + key
	if roots != nil {
		canonical, err := domain.NormalizeHostingHostname(hostname)
		ip, port, dialErr := net.SplitHostPort(target)
		if err != nil || canonical != hostname || len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "" || dialErr != nil || ip != "127.0.0.1" || port == "" {
			return invalid
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != hostname+":443" {
				return nil, invalid
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", target)
		}
		url = "https://" + hostname + "/uploads/" + key
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var body io.Reader
	if method == "PUT" {
		body = bytes.NewReader(expected)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return invalid
	}
	response, err := client.Do(req)
	if err != nil {
		return invalid
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 65538))
	defer clear(data)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.Header.Get("X-NeroCD-Fixture-Instance") != op.Result.ContainerID[:12] {
		return invalid
	}
	if roots != nil {
		if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || len(response.TLS.PeerCertificates) == 0 {
			return invalid
		}
		hash := sha256.Sum256(response.TLS.PeerCertificates[0].Raw)
		if hex.EncodeToString(hash[:]) != fingerprint {
			return invalid
		}
	}
	if method == "PUT" {
		if response.StatusCode != http.StatusCreated || len(data) != 0 {
			return invalid
		}
	} else if response.StatusCode != http.StatusOK || len(data) != len(expected) || sha256.Sum256(data) != sha256.Sum256(expected) {
		return invalid
	}
	return nil
}

func (s *applicationDatabaseStorageFixture) assertFiles(f *databaseRunnerFixture, op domain.HostingOperation) {
	f.t.Helper()
	f.applicationContainer(op)
	if op.Plan.Storage == nil || s.Plan == nil || *op.Plan.Storage != *s.Plan {
		f.t.Fatal("upload volume creation origin changed across replacement")
	}
	if f.volumeProof(op) != s.VolumeProof {
		f.t.Fatal("upload volume daemon metadata or original creation identity changed")
	}
	for key, value := range s.Files {
		if applicationDatabaseUpload(f.t.Context(), nil, "", "", op, "GET", key, value, "") != nil {
			f.t.Fatal("connected retained upload failed direct application read")
		}
		if f.routes != nil && applicationDatabaseUpload(f.t.Context(), f.routes.roots, f.routes.hostname, f.routes.route.Certificate.Fingerprint, op, "GET", key, value, "127.0.0.1:443") != nil {
			f.t.Fatal("connected retained upload failed trusted HTTPS read")
		}
	}
	raw := f.docker("cp", op.Result.ContainerID+":/data/uploads/retained_a", "-")
	if applicationDatabaseUploadArchive(raw, s.Files["retained_a"]) != nil {
		f.t.Fatal("retained upload digest or 0640 ownership proof failed")
	}
	if applicationDatabaseUploadDirectoryArchive(f.docker("cp", op.Result.ContainerID+":/data/uploads", "-"), s.Files) != nil {
		f.t.Fatal("retained upload directory 0750 ownership, contents or digest proof failed")
	}
}

func (s *applicationDatabaseStorageFixture) write(f *databaseRunnerFixture, op domain.HostingOperation, key string, seed byte) {
	f.t.Helper()
	data := applicationDatabaseStoragePayload(seed)
	var err error
	if f.routes != nil {
		err = applicationDatabaseUpload(f.t.Context(), f.routes.roots, f.routes.hostname, f.routes.route.Certificate.Fingerprint, op, "PUT", key, data, "127.0.0.1:443")
	} else {
		err = applicationDatabaseUpload(f.t.Context(), nil, "", "", op, "PUT", key, data, "")
	}
	if err != nil {
		f.t.Fatal("connected application upload write failed")
	}
	if s.Files == nil {
		s.Files = map[string][]byte{}
	}
	s.Files[key] = data
	s.assertFiles(f, op)
}

func TestHostingApplicationDatabaseStorageCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_CONNECTION_STORAGE") != "1" {
		t.Skip("requires reviewed connected storage physical profile")
	}
	testHostingApplicationDatabaseCompiledRunner(t, true)
}

func TestHostingApplicationDatabaseStorageFailuresCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_CONNECTION_STORAGE") != "1" {
		t.Skip("requires reviewed connected storage recovery physical profile")
	}
	testHostingApplicationDatabaseRouteFailuresCompiledRunner(t, true)
}

func TestApplicationDatabaseStorageProxyPause(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &applicationDatabaseRouteFixture{daemonID: "daemon", proxy: domain.HostingProxy{ID: "proxy", ServerID: "server", DestinationID: "destination", RunnerID: "runner"}}
	for _, mode := range []string{"exact", "foreign_daemon", "wrong_id", "wrong_image", "wrong_proxy", "wrong_server", "wrong_destination", "wrong_runner", "stopped", "inspect_error", "pause_error", "pause_unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			paused, mutations := false, 0
			docker := func(_ context.Context, args ...string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"info", "--format", "{{.ID}}"}) {
					if mode == "foreign_daemon" {
						return []byte("foreign"), nil
					}
					return []byte("daemon"), nil
				}
				if len(args) > 0 && args[0] == "inspect" {
					if mode == "inspect_error" {
						return nil, errors.New("PRIVATE-CANARY")
					}
					proof := map[string]any{"Id": id, "Image": hostingproxy.Image, "Proxy": "proxy", "Daemon": "daemon", "Server": "server", "Destination": "destination", "Runner": "runner", "State": map[string]bool{"Running": mode != "stopped", "Paused": paused}}
					for field, fault := range map[string]string{"Id": "wrong_id", "Image": "wrong_image", "Proxy": "wrong_proxy", "Server": "wrong_server", "Destination": "wrong_destination", "Runner": "wrong_runner"} {
						if mode == fault {
							proof[field] = "foreign"
						}
					}
					return json.Marshal(proof)
				}
				if len(args) == 3 && args[0] == "container" && args[2] == id {
					mutations++
					if mode == "pause_error" {
						return nil, errors.New("PRIVATE-CANARY")
					}
					if mode != "pause_unconfirmed" {
						paused = args[1] == "pause"
					}
					return []byte(id), nil
				}
				return nil, errors.New("unexpected command")
			}
			err := applicationDatabaseStorageProxyPause(t.Context(), r, id, true, docker)
			if (err == nil) != (mode == "exact") {
				t.Fatal("proxy pause accepted unknown identity or unconfirmed mutation")
			}
			if mode == "exact" {
				if applicationDatabaseStorageProxyPause(t.Context(), r, id, false, docker) != nil || paused || mutations != 2 {
					t.Fatal("exact proxy did not unpause under renewed identity check")
				}
			} else if mode != "pause_error" && mode != "pause_unconfirmed" && mutations != 0 {
				t.Fatal("proxy pause mutated after identity mismatch")
			}
		})
	}
}

func TestApplicationDatabaseStorageStopBeforeStart(t *testing.T) {
	const stopped = "2026-10-25T12:00:00.100000000Z"
	const started = "2026-10-25T12:00:00.200000000Z"
	for _, test := range []struct {
		name        string
		old, next   bool
		stop, start string
		want        bool
	}{
		{"exact", false, true, stopped, started, true},
		{"overlap", false, true, started, stopped, false},
		{"still_running", true, true, stopped, started, false},
		{"candidate_stopped", false, false, stopped, started, false},
		{"unknown_stop", false, true, "", started, false},
		{"unknown_start", false, true, stopped, "0001-01-01T00:00:00Z", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if applicationDatabaseStorageStopBeforeStart(test.old, test.next, test.stop, test.start) != test.want {
				t.Fatal("incorrect independent stop/start observation")
			}
		})
	}
}

func TestApplicationDatabaseStorageUploadArchive(t *testing.T) {
	data := applicationDatabaseStoragePayload(11)
	for _, mode := range []string{"exact", "wrong_owner", "wrong_group", "wrong_mode", "wrong_name", "symlink", "wrong_digest", "short", "extra_member", "oversized", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			writer := tar.NewWriter(&out)
			h := &tar.Header{Name: "retained_a", Mode: 0640, Uid: 12345, Gid: 12346, Size: int64(len(data)), Typeflag: tar.TypeReg}
			body := bytes.Clone(data)
			switch mode {
			case "wrong_owner":
				h.Uid++
			case "wrong_group":
				h.Gid++
			case "wrong_mode":
				h.Mode = 0644
			case "wrong_name":
				h.Name = "../retained_a"
			case "symlink":
				h.Typeflag, h.Linkname, h.Size, body = tar.TypeSymlink, "/foreign", 0, nil
			case "wrong_digest":
				body[5] ^= 1
			case "short":
				body = body[:len(body)-1]
				h.Size--
			}
			if writer.WriteHeader(h) != nil {
				t.Fatal("archive header failed")
			}
			if _, err := writer.Write(body); err != nil {
				t.Fatal("archive body failed")
			}
			if mode == "extra_member" {
				if writer.WriteHeader(&tar.Header{Name: "extra", Mode: 0640, Typeflag: tar.TypeReg}) != nil {
					t.Fatal("extra archive header failed")
				}
			}
			if writer.Close() != nil {
				t.Fatal("archive close failed")
			}
			raw := out.Bytes()
			if mode == "oversized" {
				raw = make([]byte, (1<<20)+1)
			}
			if mode == "truncated" {
				raw = raw[:512+100]
			}
			if (applicationDatabaseUploadArchive(raw, data) == nil) != (mode == "exact") {
				t.Fatal("archive metadata or byte corruption was not distinguished")
			}
		})
	}
}

func TestApplicationDatabaseStorageTrustedUpload(t *testing.T) {
	data := applicationDatabaseStoragePayload(17)
	for _, mode := range []string{"exact", "wrong_instance", "missing_instance", "untrusted", "wrong_hostname", "wrong_fingerprint", "wrong_digest", "oversized", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/uploads/retained_a" || r.Method != "GET" {
					t.Error("upload probe changed target")
				}
				instance := id[:12]
				if mode == "wrong_instance" {
					instance = strings.Repeat("b", 12)
				}
				if mode != "missing_instance" {
					w.Header().Set("X-NeroCD-Fixture-Instance", instance)
				}
				if mode == "redirect" {
					w.Header().Set("Location", "https://public.example.org")
					w.WriteHeader(302)
					return
				}
				body := bytes.Clone(data)
				if mode == "wrong_digest" {
					body[17] ^= 1
				}
				if mode == "oversized" {
					body = append(body, 0)
				}
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)
			roots := x509.NewCertPool()
			if mode != "untrusted" {
				roots.AddCert(server.Certificate())
			}
			hash := sha256.Sum256(server.Certificate().Raw)
			fingerprint, host := hex.EncodeToString(hash[:]), "example.com"
			if mode == "wrong_hostname" {
				host = "foreign.example.test"
			}
			if mode == "wrong_fingerprint" {
				fingerprint = strings.Repeat("b", 64)
			}
			if (applicationDatabaseUpload(t.Context(), roots, host, fingerprint, domain.HostingOperation{Result: &domain.HostingResult{ContainerID: id}}, "GET", "retained_a", data, server.Listener.Addr().String()) == nil) != (mode == "exact") {
				t.Fatal("trusted upload accepted changed identity, trust or bytes")
			}
		})
	}
}

func TestApplicationDatabaseStorageProfileArguments(t *testing.T) {
	base := &databaseRunnerFixture{root: "/fixture", server: &httptest.Server{URL: "https://fixture.invalid"}, connected: true, upgrade: true}
	old := databaseRunnerArguments(base)
	base.storage = &applicationDatabaseStorageFixture{}
	next := databaseRunnerArguments(base)
	if len(next) != len(old)+1 || next[len(next)-1] != "--hosting-application-database-storage" || !slices.Equal(old, next[:len(old)]) {
		t.Fatal("storage opt-in changed baseline runner flags")
	}
}

func TestApplicationDatabaseStorageDirectoryArchive(t *testing.T) {
	files := map[string][]byte{"retained_a": applicationDatabaseStoragePayload(11), "retained_b": applicationDatabaseStoragePayload(22)}
	for _, mode := range []string{"exact", "directory_mode", "directory_owner", "missing_directory", "extra_file", "duplicate_file", "wrong_digest", "missing_file", "link", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			writer := tar.NewWriter(&out)
			dir := &tar.Header{Name: "uploads/", Typeflag: tar.TypeDir, Mode: 0750, Uid: 12345, Gid: 12346}
			if mode == "directory_mode" {
				dir.Mode = 0777
			}
			if mode == "directory_owner" {
				dir.Uid = 0
			}
			if mode != "missing_directory" && writer.WriteHeader(dir) != nil {
				t.Fatal("directory header failed")
			}
			for _, key := range []string{"retained_a", "retained_b"} {
				if mode == "missing_file" && key == "retained_b" {
					continue
				}
				data := bytes.Clone(files[key])
				if mode == "wrong_digest" {
					data[0] ^= 1
				}
				h := &tar.Header{Name: "uploads/" + key, Typeflag: tar.TypeReg, Mode: 0640, Uid: 12345, Gid: 12346, Size: int64(len(data))}
				if mode == "link" {
					h.Typeflag, h.Linkname, h.Size, data = tar.TypeSymlink, "/foreign", 0, nil
				}
				if writer.WriteHeader(h) != nil {
					t.Fatal("file header failed")
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal("file body failed")
				}
			}
			if mode == "extra_file" || mode == "duplicate_file" {
				name := "extra"
				if mode == "duplicate_file" {
					name = "retained_a"
				}
				if writer.WriteHeader(&tar.Header{Name: "uploads/" + name, Typeflag: tar.TypeReg, Mode: 0640, Uid: 12345, Gid: 12346}) != nil {
					t.Fatal("extra header failed")
				}
			}
			if writer.Close() != nil {
				t.Fatal("archive close failed")
			}
			raw := out.Bytes()
			if mode == "oversized" {
				raw = make([]byte, (1<<20)+1)
			}
			if (applicationDatabaseUploadDirectoryArchive(raw, files) == nil) != (mode == "exact") {
				t.Fatal("directory archive accepted changed permission, identity or contents")
			}
		})
	}
}

func TestApplicationDatabaseStorageBrowserRecoveryCorrelation(t *testing.T) {
	expected := map[string]any{"action": "complete_replacement", "operation_id": "original", "stage_hash": "stage", "candidate_hash": "candidate", "storage_state_hash": "state", "expected_membership_revision": int64(4), "idempotency_key": "fixture_key", "application_database_storage": applicationDatabaseStorageAck{1, "storage", true}}
	original := domain.HostingOperation{ID: "original", ProjectID: "project", ResourceID: "application", BindingID: "binding", RunID: "run_original", Snapshot: domain.HostingSnapshot{ResourceID: "application", DestinationID: "destination"}, Plan: &domain.HostingApplyPlan{Hash: "plan"}}
	response, _ := json.Marshal(original)
	for _, mode := range []string{"exact", "wrong_action", "wrong_operation", "wrong_stage", "wrong_candidate", "wrong_state", "wrong_revision", "wrong_storage_ack", "unknown_field", "changed_replay", "foreign_response", "wrong_phase", "large_response", "boundary_response", "oversize_response", "duplicate_response", "alias_response", "unknown_response", "trailing_response", "null_response", "duplicate_request", "trailing_request", "foreign_project", "foreign_destination", "foreign_plan", "foreign_run", "authority_request"} {
		t.Run(mode, func(t *testing.T) {
			b := &applicationDatabaseBrowser{phase: "recover", seen: map[string]applicationDatabaseBrowserSeen{}, admissions: make(chan applicationDatabaseBrowserAdmission, 4), recovery: &applicationDatabaseBrowserRecovery{Action: "complete_replacement", OperationID: "original", Expected: expected, Original: original}}
			body := maps.Clone(expected)
			body["idempotency_key"] = "actual_browser_generated_key"
			switch mode {
			case "wrong_action":
				body["action"] = "resume_predecessor"
			case "wrong_operation":
				body["operation_id"] = "other"
			case "wrong_stage":
				body["stage_hash"] = "other"
			case "wrong_candidate":
				body["candidate_hash"] = "other"
			case "wrong_state":
				body["storage_state_hash"] = "other"
			case "wrong_revision":
				body["expected_membership_revision"] = int64(6)
			case "wrong_storage_ack":
				body["application_database_storage"] = applicationDatabaseStorageAck{1, "storage", false}
			case "unknown_field":
				body["lease_id"] = "PRIVATE-CANARY"
			case "wrong_phase":
				b.phase = "finish"
			}
			raw, _ := json.Marshal(body)
			reply := bytes.Clone(response)
			if mode == "foreign_response" {
				reply = bytes.ReplaceAll(reply, []byte(`"original"`), []byte(`"foreign"`))
			}
			switch mode {
			case "large_response":
				reply = append(reply, bytes.Repeat([]byte(" "), (33<<10)-len(reply))...)
			case "boundary_response":
				reply = append(reply, bytes.Repeat([]byte(" "), domain.HostingApplicationDatabaseJournalJSONLimit-len(reply))...)
			case "oversize_response":
				reply = append(reply, bytes.Repeat([]byte(" "), domain.HostingApplicationDatabaseJournalJSONLimit+1-len(reply))...)
			case "duplicate_response":
				reply = bytes.Replace(reply, []byte(`"id":`), []byte(`"id":"duplicate","id":`), 1)
			case "alias_response":
				reply = bytes.Replace(reply, []byte(`"id":`), []byte(`"ID":`), 1)
			case "unknown_response":
				reply = bytes.Replace(reply, []byte(`"id":`), []byte(`"lease_id":"PRIVATE-CANARY","id":`), 1)
			case "trailing_response":
				reply = append(reply, []byte(`{}`)...)
			case "null_response":
				var fields map[string]json.RawMessage
				if json.Unmarshal(reply, &fields) != nil {
					t.Fatal("response fixture invalid")
				}
				fields["plan"] = json.RawMessage(`null`)
				reply, _ = json.Marshal(fields)
			case "duplicate_request":
				raw = bytes.Replace(raw, []byte(`"action":`), []byte(`"action":"other","action":`), 1)
			case "trailing_request":
				raw = append(raw, []byte(`{}`)...)
			case "foreign_project":
				reply = bytes.Replace(reply, []byte(`"project"`), []byte(`"foreign"`), 1)
			case "foreign_destination":
				reply = bytes.Replace(reply, []byte(`"destination"`), []byte(`"foreign"`), 1)
			case "foreign_plan":
				reply = bytes.Replace(reply, []byte(`"hash":"plan"`), []byte(`"hash":"foreign"`), 1)
			case "foreign_run":
				reply = bytes.Replace(reply, []byte(`"run_original"`), []byte(`"run_foreign"`), 1)
			case "authority_request":
				body["fence"] = "PRIVATE-CANARY"
				raw, _ = json.Marshal(body)
			}
			b.observe("recover", raw, reply, "project", "application", "database")
			if mode == "changed_replay" {
				body["idempotency_key"] = "changed"
				raw, _ = json.Marshal(body)
			}
			b.observe("recover", raw, reply, "project", "application", "database")
			valid := mode == "exact" || mode == "large_response" || mode == "boundary_response"
			if b.invalid != !valid || valid && (len(b.admissions) != 1 || b.expectedAdmissions() != 1 || b.recovery.Submitted["idempotency_key"] != "actual_browser_generated_key") {
				t.Fatal("browser recovery lost exact public request, operation, phase or identical replay correlation")
			}
			diagnostic, _ := json.Marshal(b.recoveryObservation)
			if len(diagnostic) > 512 || bytes.Contains(diagnostic, []byte("CANARY")) {
				t.Fatal("browser recovery diagnostic exposed response/request data")
			}
		})
	}
}

// The authenticated real handler returns this repository seam's operation. This
// exercises API202 serialization/observation, not PostgreSQL persistence.
type applicationDatabaseRecoveryResponseRepository struct {
	*connectionHTTPRepository
	response domain.HostingOperation
	input    domain.HostingDatabaseConnectionRecoveryInput
	requests int
}

func (r *applicationDatabaseRecoveryResponseRepository) RequestHostingDatabaseConnectionRecovery(_ context.Context, _ string, resource, actor string, in domain.HostingDatabaseConnectionRecoveryInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.requests++
	r.input = in
	if resource != r.response.ResourceID || actor == "" {
		return domain.HostingOperation{}, errors.New("recovery fixture authorization mismatch")
	}
	return r.response, nil
}

func TestApplicationDatabaseStorageBrowserRecoveryAPI202(t *testing.T) {
	original := domain.HostingOperation{ID: "original", ProjectID: "proj_platform", ResourceID: "connection_app", BindingID: "binding", RunID: "run_original", State: "failed", MutationHold: true, Snapshot: domain.HostingSnapshot{ResourceID: "connection_app", DestinationID: "destination"}}
	original.Plan = &domain.HostingApplyPlan{Snapshot: original.Snapshot, Hash: "saved_plan"}
	mem := newSeededTestStore()
	repo := &applicationDatabaseRecoveryResponseRepository{connectionHTTPRepository: &connectionHTTPRepository{MemoryStore: mem}, response: original}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal("recovery API service unavailable")
	}
	server := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	token := createTestSession(t, server)
	input := domain.HostingDatabaseConnectionRecoveryInput{Action: "resume_predecessor", OperationID: original.ID, StageHash: "sha256:" + strings.Repeat("a", 64), CandidateHash: "sha256:" + strings.Repeat("b", 64), StorageStateHash: "sha256:" + strings.Repeat("c", 64), ExpectedMembershipRevision: 4, IdempotencyKey: "actual_browser_key", ApplicationDatabaseStorage: &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: 1, ExpectedStorageHash: "sha256:" + strings.Repeat("d", 64), AcknowledgeInterruption: true}}
	raw, err := json.Marshal(input)
	if err != nil || input.Validate() != nil {
		t.Fatal("recovery API request invalid")
	}
	response := connectionCall(t, server, "POST", "/api/v1/resources/connection_app/database-connection/recovery", token, string(raw))
	if response.Code != http.StatusAccepted || repo.requests != 1 || !reflect.DeepEqual(repo.input, input) {
		t.Fatal("authenticated recovery API did not return exact accepted operation request")
	}
	var expected map[string]any
	if json.Unmarshal(raw, &expected) != nil {
		t.Fatal("expected request invalid")
	}
	for _, mode := range []string{"actual_202", "lawful_whitespace_above_request_cap"} {
		t.Run(mode, func(t *testing.T) {
			reply := bytes.Clone(response.Body.Bytes())
			if mode != "actual_202" {
				reply = append(reply, bytes.Repeat([]byte(" "), (33<<10)-len(reply))...)
			}
			b := &applicationDatabaseBrowser{phase: "recover", seen: map[string]applicationDatabaseBrowserSeen{}, admissions: make(chan applicationDatabaseBrowserAdmission, 2), recovery: &applicationDatabaseBrowserRecovery{Action: input.Action, OperationID: original.ID, Expected: expected, Original: original}}
			b.observe("recover", raw, reply, original.ProjectID, original.ResourceID, "connection_database")
			if b.invalid || len(b.admissions) != 1 || b.recoveryObservation.Reason != "accepted" || !b.recoveryObservation.RequestMatches || !b.recoveryObservation.OperationMatches {
				t.Fatal("actual API202 operation did not preserve exact observer correlation")
			}
			admitted := <-b.admissions
			if !reflect.DeepEqual(admitted.Operation, original) {
				t.Fatal("API202 observer changed failed operation, plan or destination")
			}
		})
	}
}
