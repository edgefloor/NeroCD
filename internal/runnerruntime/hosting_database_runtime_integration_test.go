//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/domain"
)

const databaseRuntimeOwner = "nerocd-database-runtime-20261024-db7319"

type databaseRuntimeFixture struct {
	t               *testing.T
	engine          *imageEngine
	config          DatabaseEngineConfig
	recipe          domain.HostingDatabaseRecipe
	request         hostingOwnedVolumeRequest
	evidence        hostingOwnedVolumeEvidence
	plan            domain.HostingApplyPlan
	passwords       map[string][]byte
	credentialFiles map[string][]byte
	root            string
}

func databaseRuntimeAuthority(ctx context.Context) error { return ctx.Err() }

func databaseRuntimeEngine(t *testing.T, endpoint string) *imageEngine {
	t.Helper()
	engine, err := newImageEngine(endpoint)
	if err != nil {
		t.Fatal("production image engine unavailable")
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error("production image engine cleanup failed")
		}
	})
	return engine
}

func databaseRuntimeDocker(t *testing.T, engine *imageEngine, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	raw, err := engine.docker(ctx, args...)
	if err != nil {
		t.Fatal("owned database fixture Docker action failed")
	}
	return raw
}

// Only closed labels may enter qualification output, including for future errors.
func databaseRuntimePreparationStage(message string) string {
	switch message {
	case "invalid database preparation profile":
		return "profile_validation"
	case "unsupported database recipe":
		return "recipe_selection"
	case "database native image platform unsupported":
		return "native_platform_selection"
	case "database daemon unavailable":
		return "daemon_identity"
	case "database image pull authority unavailable":
		return "image_pull_authority"
	case "database image pull not confirmed":
		return "image_pull"
	case "database prepared image unavailable":
		return "prepared_image_inspection"
	case "database image lacks exact child manifest digest":
		return "child_manifest_digest"
	case "database prepared release changed":
		return "release_verification"
	default:
		return "unclassified"
	}
}

func TestDatabaseRuntimePreparationStage(t *testing.T) {
	for message, want := range map[string]string{
		"invalid database preparation profile":                     "profile_validation",
		"unsupported database recipe":                              "recipe_selection",
		"database native image platform unsupported":               "native_platform_selection",
		"database daemon unavailable":                              "daemon_identity",
		"database image pull authority unavailable":                "image_pull_authority",
		"database image pull not confirmed":                        "image_pull",
		"database prepared image unavailable":                      "prepared_image_inspection",
		"database image lacks exact child manifest digest":         "child_manifest_digest",
		"database prepared release changed":                        "release_verification",
		"unexpected error: synthetic credential and header values": "unclassified",
	} {
		if got := databaseRuntimePreparationStage(message); got != want {
			t.Errorf("preparation stage = %q, want %q", got, want)
		}
	}
}

func newDatabaseRuntimeFixture(t *testing.T, root, kind, suffix string, materialize bool) *databaseRuntimeFixture {
	t.Helper()
	version := domain.HostingDatabasePostgresRecipe
	if kind == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	recipe, err := domain.HostingDatabaseRecipeFor(kind, version)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, kind+"-"+suffix)
	for _, path := range []string{private, filepath.Join(private, "state"), filepath.Join(private, "secrets")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal("private fixture root creation failed")
		}
	}
	config, err := NewDatabaseEngineConfig(filepath.Join(private, "state"), filepath.Join(private, "secrets"))
	if err != nil {
		t.Fatal("real database host roots refused")
	}
	if config.hostCheck != nil || config.readiness != nil {
		t.Fatal("fixture must use production host and readiness checks")
	}
	engine := databaseRuntimeEngine(t, defaultImageDockerEndpoint)
	storage := domain.HostingStorage{ProjectID: "proj_db7319", ResourceID: "res_db7319_" + kind + "_" + suffix, BindingID: "binding_db7319_" + kind + "_" + suffix, DestinationID: "dest_db7319", Kind: "platform_managed", Driver: "local", MountPath: recipe.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "usr_db7319", CreatedAt: time.Now().UTC()}
	storage.ID, storage.VolumeName = domain.HostingStorageIdentity(storage.BindingID)
	storage.Hash = storage.DocumentHash()
	identity := sha256.Sum256([]byte(databaseRuntimeOwner + kind + suffix))
	snapshot := domain.HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, ServerID: "server_db7319", RunnerID: "runner_db7319", ResourceGeneration: 1, RuntimeNamespace: "ncd_" + hex.EncodeToString(identity[:16]), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, Storage: storage.Reference(), PolicyVersion: domain.HostingDatabasePolicyVersion, Database: domain.HostingDatabaseExecutionRef{Engine: kind, RecipeVersion: version}}
	refs := slices.Clone(recipe.Credentials)
	passwords := map[string][]byte{}
	for i := range refs {
		refs[i].SecretID = domain.HostingDatabaseSecretID(snapshot.ResourceID, refs[i].Role)
		refs[i].Version = 1
		t.Logf("protected credential reference role=%s username=%s secret_id=%s version=%d", refs[i].Role, refs[i].Username, refs[i].SecretID, refs[i].Version)
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			t.Fatal("fixture credential generation failed")
		}
		passwords[refs[i].Role] = []byte(hex.EncodeToString(entropy[:]))
		clear(entropy[:])
	}
	t.Cleanup(func() {
		for _, value := range passwords {
			clear(value)
		}
	})
	snapshot.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(snapshot, refs)
	if err != nil {
		t.Fatal(err)
	}
	release, err := engine.PrepareDatabase(t.Context(), snapshot, config, databaseRuntimeAuthority)
	if err != nil {
		t.Fatalf("production pinned database preparation failed: stage=%s", databaseRuntimePreparationStage(err.Error()))
	}
	if domain.ValidateHostingDatabaseRelease(snapshot, release) != nil || release.Architecture != "arm64" {
		t.Fatal("prepared database recipe/platform mismatch")
	}
	origin := "hop_db7319_" + kind + "_" + suffix + "_a"
	storagePlan := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: storage.VolumeName, MountPath: recipe.MountPath, CreationOperationID: origin, CreationNonce: domain.HostingStorageCreationNonce(storage.Reference(), origin, release.DaemonID), DaemonID: release.DaemonID}
	plan, err := domain.CompileHostingDatabasePlan(snapshot, release, nil, storagePlan, domain.HostingDatabasePlan{Reference: snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(storage.BindingID), Credentials: refs})
	if err != nil {
		t.Fatal(err)
	}
	request := hostingOwnedVolumeRequest{Storage: storage, DaemonID: release.DaemonID, OriginOperationID: origin, OriginPlanHash: plan.Hash, CreationNonce: storagePlan.CreationNonce, CreatePermitted: materialize}
	fixture := &databaseRuntimeFixture{t: t, engine: engine, config: config, recipe: recipe, request: request, plan: plan, passwords: passwords, credentialFiles: map[string][]byte{}, root: private}
	t.Cleanup(func() {
		for _, value := range fixture.credentialFiles {
			clear(value)
		}
	})
	if materialize {
		if present, err := engine.ownedVolumePresent(t.Context(), storage.VolumeName); err != nil || present {
			t.Fatal("fixture volume must be absent before fresh materialization")
		}
		fixture.evidence, err = engine.ensureOwnedVolume(t.Context(), request)
		if err != nil {
			t.Fatal("actual owned volume materialization failed")
		}
	}
	fixture.request.CreatePermitted = false
	t.Logf("resolved fixture engine=%s binding=%s volume=%s network=%s origin=%s plan=%s recipe_index=%s child=%s config_id=%s daemon=%s", kind, storage.BindingID, storage.VolumeName, plan.Database.NetworkName, origin, plan.Hash, recipe.ImageReference, release.CanonicalImage, release.ImageID, release.DaemonID)
	return fixture
}

func (f *databaseRuntimeFixture) credentials(plan domain.HostingApplyPlan, changed bool) *DatabaseCredentials {
	f.t.Helper()
	var values []DatabaseCredentialValue
	for i, ref := range plan.Database.Credentials {
		password := bytes.Clone(f.passwords[ref.Role])
		if changed && i == 0 {
			if password[0] == 'a' {
				password[0] = 'b'
			} else {
				password[0] = 'a'
			}
		}
		value, err := NewDatabaseCredentialValue(ref, password)
		clear(password)
		if err != nil {
			f.t.Fatal("protected fixture value refused")
		}
		values = append(values, value)
	}
	defer func() {
		for i := range values {
			values[i].Close()
		}
	}()
	credentials, err := NewDatabaseCredentials(plan, values, time.Now().Add(10*time.Minute))
	if err != nil {
		f.t.Fatal("protected fixture delivery refused")
	}
	f.t.Cleanup(credentials.Close)
	return credentials
}

func (f *databaseRuntimeFixture) apply(engine *imageEngine, operation string, plan domain.HostingApplyPlan, changed bool, before func(context.Context) error) domain.HostingResult {
	f.t.Helper()
	return engine.ApplyDatabase(f.t.Context(), operation, plan, f.request, f.evidence, f.credentials(plan, changed), f.config, before)
}

func (f *databaseRuntimeFixture) next(previousOperation string, previousPlan domain.HostingApplyPlan, result domain.HostingResult) domain.HostingApplyPlan {
	f.t.Helper()
	healthy := domain.HostingHealthy{Snapshot: previousPlan.Snapshot, Release: previousPlan.Release, OperationID: previousOperation, ContainerID: result.ContainerID, PlanHash: previousPlan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *previousPlan.Storage, CreationPlanHash: f.request.OriginPlanHash}}
	snapshot := previousPlan.Snapshot
	snapshot.ResourceGeneration++
	plan, err := domain.CompileHostingDatabasePlan(snapshot, previousPlan.Release, &healthy, *previousPlan.Storage, *previousPlan.Database)
	if err != nil {
		f.t.Fatal("database successor compilation failed")
	}
	return plan
}

func (f *databaseRuntimeFixture) live(operation string, plan domain.HostingApplyPlan, result domain.HostingResult, previous string) databaseNetworkRecord {
	f.t.Helper()
	if !result.Ready || result.Uncertain || result.Outcome != "succeeded" || result.PlanHash != plan.Hash || result.ImageID != plan.Release.ImageID {
		f.t.Fatal("database result not exact ready success")
	}
	var container struct {
		ID     string `json:"Id"`
		Image  string
		Config struct {
			Labels map[string]string
			Env    []string
		}
		State struct {
			Running   bool
			StartedAt string
		}
		HostConfig struct {
			PortBindings                map[string]json.RawMessage
			PublishAllPorts, Privileged bool
		}
		Mounts []struct {
			Type, Name, Driver, Source, Destination string
			RW                                      bool
		}
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	if json.Unmarshal(databaseRuntimeDocker(f.t, f.engine, "container", "inspect", result.ContainerID, "--format", "{{json .}}"), &container) != nil || container.ID != result.ContainerID || container.Image != plan.Release.ImageID || !container.State.Running || len(container.HostConfig.PortBindings) != 0 || container.HostConfig.PublishAllPorts || container.HostConfig.Privileged || len(container.Mounts) != 2 || len(container.NetworkSettings.Networks) != 1 {
		f.t.Fatal("independent database container policy mismatch")
	}
	if container.Config.Labels[imageBindingLabel] != plan.Snapshot.BindingID || container.Config.Labels[imageOperationLabel] != operation || container.Config.Labels[imagePlanLabel] != plan.Hash || container.Config.Labels[imageReleaseLabel] != plan.Release.Hash {
		f.t.Fatal("independent container ownership hash mismatch")
	}
	for _, password := range f.passwords {
		for _, env := range container.Config.Env {
			if bytes.Contains([]byte(env), password) {
				f.t.Fatal("credential leaked into Docker environment")
			}
		}
	}
	dataMount, secretMount := false, false
	for _, mount := range container.Mounts {
		if mount.Destination == plan.Storage.MountPath {
			dataMount = mount.Type == "volume" && mount.Name == f.request.Storage.VolumeName && mount.Driver == "local" && mount.RW
		}
		if mount.Destination == databaseSecretMount {
			secretMount = mount.Type == "bind" && !mount.RW && mount.Source == filepath.Join(f.config.secretRoot, databaseGeneration(operation, plan))
		}
	}
	if !dataMount || !secretMount {
		f.t.Fatal("database data/credential mount policy mismatch")
	}
	users := strings.Fields(string(databaseRuntimeDocker(f.t, f.engine, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+f.request.Storage.VolumeName)))
	if len(users) != 1 || users[0] != result.ContainerID {
		f.t.Fatal("database volume must have exactly one current container user")
	}
	if previous != "" && strings.TrimSpace(string(databaseRuntimeDocker(f.t, f.engine, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+previous))) != "" {
		f.t.Fatal("retired exact predecessor still exists")
	}
	var volume hostingInspectedVolume
	if json.Unmarshal(databaseRuntimeDocker(f.t, f.engine, "volume", "inspect", f.request.Storage.VolumeName, "--format", "{{json .}}"), &volume) != nil || volume.Name != f.evidence.VolumeName || volume.Driver != "local" || volume.Scope != "local" || volume.CreatedAt != f.evidence.CreatedAt || !maps.Equal(volume.Labels, f.evidence.Labels) || len(volume.Options) != 0 {
		f.t.Fatal("retained database volume identity changed")
	}
	record, exists, err := f.config.readRecord(plan.Database.NetworkName + ".json")
	if err != nil || !exists || !record.InitReady || record.Configuration != plan.Database.Reference.ConfigurationHash || record.OriginPlan != f.request.OriginPlanHash || record.Storage != f.request.Storage.Reference() || record.NetworkID != container.NetworkSettings.Networks[plan.Database.NetworkName].NetworkID {
		f.t.Fatal("persistent database network origin mismatch")
	}
	var network databaseNetwork
	if json.Unmarshal(databaseRuntimeDocker(f.t, f.engine, "network", "inspect", plan.Database.NetworkName, "--format", "{{json .}}"), &network) != nil || network.ID != record.NetworkID || !network.Internal || network.Driver != "bridge" || network.Scope != "local" || len(network.Containers) != 1 || !maps.Equal(network.Labels, record.labels()) {
		f.t.Fatal("independent private network ownership mismatch")
	}
	if got := strings.TrimSpace(string(databaseRuntimeDocker(f.t, f.engine, "container", "exec", "--user", "0:0", result.ContainerID, "stat", "-c", "%u:%g", "/proc/1"))); got != "999:999" {
		f.t.Fatal("database writer UID/GID mismatch")
	}
	f.checkSecretPolicy()
	f.t.Logf("verified database writer operation=%s container=%s config_id=%s volume=%s created=%s network=%s network_id=%s plan=%s credential_refs_unchanged=true one_writer=true predecessor_absent=%t published_ports=0", operation, result.ContainerID, container.Image, volume.Name, volume.CreatedAt, network.Name, network.ID, plan.Hash, previous != "")
	return record
}

func (f *databaseRuntimeFixture) checkSecretPolicy() {
	f.t.Helper()
	err := filepath.WalkDir(f.config.secretRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("protected fixture credential traversal failed")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("protected ownership unavailable")
		}
		uid, gid, mode := uint32(999), uint32(999), os.FileMode(0600)
		if path == f.config.secretRoot {
			uid, gid, mode = 0, 0, 0700
		} else if entry.IsDir() {
			mode = 0700
		}
		if stat.Uid != uid || stat.Gid != gid || info.Mode().Perm() != mode || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("protected fixture credential policy mismatch")
		}
		if !entry.IsDir() && entry.Name() != "identity.json" {
			raw, err := os.ReadFile(path)
			if err != nil {
				return errors.New("protected credential continuity unavailable")
			}
			defer clear(raw)
			if entry.Name() == "admin-password" && !bytes.Equal(raw, f.passwords["admin"]) {
				return errors.New("protected database credential changed")
			}
			if prior, exists := f.credentialFiles[entry.Name()]; exists {
				if !bytes.Equal(raw, prior) {
					return errors.New("protected credential bytes differ between generations")
				}
			} else {
				f.credentialFiles[entry.Name()] = bytes.Clone(raw)
			}
		}
		return nil
	})
	if err != nil {
		f.t.Fatal("protected credential ownership policy changed")
	}
}

func databaseRuntimeTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		defer clear(raw)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatal("private fixture continuity inspection failed")
	}
	return result
}

func (f *databaseRuntimeFixture) data(plan domain.HostingApplyPlan, result domain.HostingResult, stage string, expected map[string][]byte) {
	f.t.Helper()
	container, err := f.engine.inspectDatabaseContainer(f.t.Context(), result.ContainerID)
	if err != nil {
		f.t.Fatal("application connection target unavailable")
	}
	address := container.NetworkSettings.Networks[plan.Database.NetworkName].IPAddress
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	if f.recipe.Engine == "postgresql" {
		conn, err := databasePostgresConnect(ctx, address, "ncd_app", f.passwords["app"])
		if err != nil {
			f.t.Fatal("real app PostgreSQL authentication failed")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := conn.Close(cleanup); err != nil {
				f.t.Error("application PostgreSQL connection did not close")
			}
		}()
		var user string
		var superuser bool
		if conn.QueryRow(ctx, "SELECT current_user, rolsuper FROM pg_catalog.pg_roles WHERE rolname=current_user").Scan(&user, &superuser) != nil || user != "ncd_app" || superuser {
			f.t.Fatal("PostgreSQL application role mismatch")
		}
		if stage == "a" {
			if _, err := conn.Exec(ctx, "CREATE TABLE public.runtime_rows(stage text PRIMARY KEY, payload bytea NOT NULL)"); err != nil {
				f.t.Fatal("application table unavailable")
			}
		}
		if stage != "" {
			if _, err := conn.Exec(ctx, "INSERT INTO public.runtime_rows(stage,payload) VALUES($1,$2)", stage, expected[stage]); err != nil {
				f.t.Fatal("application row write failed")
			}
		}
		for key, want := range expected {
			var got []byte
			if conn.QueryRow(ctx, "SELECT payload FROM public.runtime_rows WHERE stage=$1", key).Scan(&got) != nil || !bytes.Equal(got, want) {
				f.t.Fatal("PostgreSQL application row changed")
			}
		}
	} else {
		if stage != "" {
			reply, err := databaseRESP(ctx, address, "ncd_app", f.passwords["app"], [][]string{{"SET", "fixture:db7319:" + stage, hex.EncodeToString(expected[stage])}})
			if err != nil || len(reply) != 1 || string(reply[0]) != "OK" {
				f.t.Fatal("authenticated Redis application write failed")
			}
		}
		for key, want := range expected {
			reply, err := databaseRESP(ctx, address, "ncd_app", f.passwords["app"], [][]string{{"GET", "fixture:db7319:" + key}})
			if err != nil || len(reply) != 1 || string(reply[0]) != hex.EncodeToString(want) {
				f.t.Fatal("Redis application key changed")
			}
		}
	}
	var dataset bytes.Buffer
	for _, key := range []string{"a", "b", "c"} {
		dataset.Write(expected[key])
	}
	f.t.Logf("authenticated application dataset engine=%s stage=%s records=%d sha256=%x", f.recipe.Engine, stage, len(expected), sha256.Sum256(dataset.Bytes()))
}

type databaseRuntimeProxy struct {
	server    *http.Server
	transport *http.Transport
	done      chan error
	cancel    context.CancelFunc
	dropped   atomic.Int32
	removes   atomic.Int32
	creates   atomic.Int32
	starts    atomic.Int32
	mutations atomic.Int32
	active    atomic.Int32
	endpoint  string
}

// This transport forwards real production CLI calls. It suppresses one actual
// successful retirement response; it never synthesizes daemon state or readiness.
func newDatabaseRuntimeProxy(t *testing.T, root, predecessor string) *databaseRuntimeProxy {
	t.Helper()
	if !dockerObjectID.MatchString(predecessor) {
		t.Fatal("invalid exact retirement fault target")
	}
	path := filepath.Join(root, "retirement.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("owned retirement socket unavailable")
	}
	if err := os.Chmod(path, 0600); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			t.Error("owned retirement socket cleanup failed")
		}
		t.Fatal("owned retirement socket mode unavailable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	p := &databaseRuntimeProxy{done: make(chan error, 1), cancel: cancel, endpoint: "unix://" + path}
	p.transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}, ResponseHeaderTimeout: 40 * time.Second, IdleConnTimeout: time.Second, MaxConnsPerHost: 8, MaxResponseHeaderBytes: 64 << 10}
	target := &url.URL{Scheme: "http", Host: "docker"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = p.transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ModifyResponse = func(response *http.Response) error {
		request := response.Request
		if request.Method == http.MethodPost || request.Method == http.MethodPut || request.Method == http.MethodDelete {
			p.mutations.Add(1)
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/containers/create") {
			p.creates.Add(1)
		}
		if request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/containers/") && strings.HasSuffix(request.URL.Path, "/start") {
			p.starts.Add(1)
		}
		if request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/containers/"+predecessor) && response.StatusCode >= 200 && response.StatusCode < 300 {
			p.removes.Add(1)
			if p.dropped.CompareAndSwap(0, 1) {
				if err := response.Body.Close(); err != nil {
					t.Error("retirement response close failed")
				}
				return errors.New("owned retirement response suppressed")
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusServiceUnavailable) }
	p.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.active.Add(1)
		defer p.active.Add(-1)
		bounded, stop := context.WithTimeout(r.Context(), 45*time.Second)
		defer stop()
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/"+predecessor) {
			for _, key := range []string{"force", "v", "link"} {
				if value := r.URL.Query().Get(key); value != "" && value != "false" && value != "0" {
					t.Error("retirement fault target used unsafe flags")
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
		}
		proxy.ServeHTTP(w, r.WithContext(bounded))
	}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 45 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { p.done <- p.server.Serve(listener) }()
	t.Cleanup(func() {
		p.cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := p.server.Shutdown(cleanup); err != nil {
			t.Error("retirement transport did not shut down")
			if closeErr := p.server.Close(); closeErr != nil {
				t.Error("retirement transport close failed")
			}
		}
		p.transport.CloseIdleConnections()
		select {
		case err := <-p.done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Error("retirement transport failed")
			}
		case <-time.After(5 * time.Second):
			t.Error("retirement transport did not join")
		}
		for deadline := time.Now().Add(5 * time.Second); p.active.Load() != 0; {
			if time.Now().After(deadline) {
				t.Error("retirement transport handlers did not join")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Log("owned retirement transport listener and handlers joined")
	})
	return p
}

func databaseRuntimePositive(t *testing.T, root, kind string) {
	f := newDatabaseRuntimeFixture(t, root, kind, "main", true)
	operationA := f.request.OriginOperationID
	a := f.apply(f.engine, operationA, f.plan, false, databaseRuntimeAuthority)
	recordA := f.live(operationA, f.plan, a, "")
	expected := map[string][]byte{}
	add := func(stage string) {
		value := make([]byte, 128)
		if _, err := rand.Read(value); err != nil {
			t.Fatal("fixture payload generation failed")
		}
		expected[stage] = value
	}
	add("a")
	f.data(f.plan, a, "a", expected)
	bPlan := f.next(operationA, f.plan, a)
	operationB := "hop_db7319_" + kind + "_main_b"
	proxy := newDatabaseRuntimeProxy(t, f.root, a.ContainerID)
	forwarded := databaseRuntimeEngine(t, proxy.endpoint)
	failed := f.apply(forwarded, operationB, bPlan, false, databaseRuntimeAuthority)
	if failed.Ready || !failed.Uncertain || failed.ContainerID == "" || proxy.dropped.Load() != 1 || proxy.removes.Load() != 1 {
		t.Fatal("real lost retirement response did not preserve uncertainty")
	}
	observation, err := forwarded.ObserveDatabase(t.Context(), domain.HostingOperation{ID: operationB, Plan: &bPlan, Result: &failed}, f.credentials(bPlan, false), f.config)
	if !errors.Is(err, errDatabaseRetirementUnresolved) || observation.Ready {
		t.Fatal("read-only observation settled lost retirement response")
	}
	creates, starts, removes := proxy.creates.Load(), proxy.starts.Load(), proxy.removes.Load()
	if creates != 1 || starts != 1 || removes != 1 {
		t.Fatal("replacement did not execute exactly one real create/start/retirement")
	}
	// Reopening the real engine preserves protected state. The exact B retry may
	// finish its retained marker but must never create/start another writer.
	if err := forwarded.Close(); err != nil {
		t.Fatal("prior engine did not close before recovery")
	}
	restarted := databaseRuntimeEngine(t, proxy.endpoint)
	b := f.apply(restarted, operationB, bPlan, false, databaseRuntimeAuthority)
	if b.ContainerID != failed.ContainerID || proxy.creates.Load() != creates || proxy.starts.Load() != starts || proxy.removes.Load() != removes {
		t.Fatal("retirement replay recreated, restarted, or removed a writer")
	}
	recordB := f.live(operationB, bPlan, b, a.ContainerID)
	if recordB.NetworkID != recordA.NetworkID {
		t.Fatal("B changed retained private network")
	}
	add("b")
	f.data(bPlan, b, "b", expected)
	cPlan := f.next(operationB, bPlan, b)
	operationC := "hop_db7319_" + kind + "_main_c"
	c := f.apply(f.engine, operationC, cPlan, false, databaseRuntimeAuthority)
	recordC := f.live(operationC, cPlan, c, b.ContainerID)
	if recordC.NetworkID != recordA.NetworkID {
		t.Fatal("C changed retained private network")
	}
	add("c")
	f.data(cPlan, c, "c", expected)
	stateBefore, secretsBefore := databaseRuntimeTree(t, f.config.stateRoot), databaseRuntimeTree(t, f.config.secretRoot)
	if err := f.engine.Close(); err != nil {
		t.Fatal("prior engine did not close before observation")
	}
	readOnly := databaseRuntimeEngine(t, defaultImageDockerEndpoint)
	f.engine = readOnly
	observed, err := readOnly.ObserveDatabase(t.Context(), domain.HostingOperation{ID: operationC, Plan: &cPlan, Result: &c}, f.credentials(cPlan, false), f.config)
	if err != nil || !observed.Ready || observed.ContainerID != c.ContainerID || !maps.Equal(stateBefore, databaseRuntimeTree(t, f.config.stateRoot)) || !maps.Equal(secretsBefore, databaseRuntimeTree(t, f.config.secretRoot)) {
		t.Fatal("reopened read-only observation changed protected state or writer")
	}
	f.data(cPlan, c, "", expected)
	beforeDenial := proxy.mutations.Load()
	denied := f.apply(restarted, operationC, cPlan, true, databaseRuntimeAuthority)
	if denied.Ready || !denied.Uncertain || proxy.mutations.Load() != beforeDenial || !maps.Equal(stateBefore, databaseRuntimeTree(t, f.config.stateRoot)) || !maps.Equal(secretsBefore, databaseRuntimeTree(t, f.config.secretRoot)) {
		t.Fatal("changed credential was accepted or modified protected state")
	}
	dPlan := f.next(operationC, cPlan, c)
	revoked := f.apply(restarted, "hop_db7319_"+kind+"_main_denied", dPlan, false, func(context.Context) error { return errors.New("fixture authority revoked") })
	if revoked.Ready || !revoked.Uncertain || proxy.mutations.Load() != beforeDenial || !maps.Equal(stateBefore, databaseRuntimeTree(t, f.config.stateRoot)) || !maps.Equal(secretsBefore, databaseRuntimeTree(t, f.config.secretRoot)) {
		t.Fatal("revoked authority modified protected state")
	}
	f.live(operationC, cPlan, c, b.ContainerID)
	f.data(cPlan, c, "", expected)
	t.Logf("database continuity engine=%s A=%s B=%s C=%s same_volume=true same_network=true credential_bytes_unchanged=true protected_observation_read_only=true retirement_response_losses=1 retirement_actual_removals=1 replay_creates=0 replay_starts=0 revoked_denied=true changed_credential_denied=true", kind, a.ContainerID, b.ContainerID, c.ContainerID)
}

func databaseRuntimeNegative(t *testing.T, root, kind, caseName string) {
	f := newDatabaseRuntimeFixture(t, root, kind, caseName, true)
	// Fault setup removes only this freshly materialized, verified empty owned
	// fixture volume. Apply itself must never recreate it or adopt its replacement.
	if _, err := f.engine.ensureOwnedVolume(t.Context(), f.request); err != nil {
		t.Fatal("negative fixture volume ownership changed")
	}
	if users := strings.TrimSpace(string(databaseRuntimeDocker(t, f.engine, "container", "ls", "--all", "--quiet", "--filter", "volume="+f.request.Storage.VolumeName))); users != "" {
		t.Fatal("negative fixture volume unexpectedly in use")
	}
	databaseRuntimeDocker(t, f.engine, "volume", "rm", f.request.Storage.VolumeName)
	if caseName == "foreign" {
		databaseRuntimeDocker(t, f.engine, "volume", "create", "--driver", "local", "--label", "io.nerocd.database-runtime.foreign="+databaseRuntimeOwner, f.request.Storage.VolumeName)
	}
	result := f.apply(f.engine, f.request.OriginOperationID, f.plan, false, databaseRuntimeAuthority)
	if result.Ready || !result.Uncertain || result.ContainerID != "" {
		t.Fatal("missing or foreign materialized volume admitted writer")
	}
	if users := strings.TrimSpace(string(databaseRuntimeDocker(t, f.engine, "container", "ls", "--all", "--quiet", "--filter", "volume="+f.request.Storage.VolumeName))); users != "" {
		t.Fatal("negative volume acquired a container")
	}
	present, err := f.engine.ownedVolumePresent(t.Context(), f.request.Storage.VolumeName)
	if err != nil || present != (caseName == "foreign") {
		t.Fatal("missing/foreign volume state changed")
	}
	if caseName == "foreign" {
		var volume hostingInspectedVolume
		if json.Unmarshal(databaseRuntimeDocker(t, f.engine, "volume", "inspect", f.request.Storage.VolumeName, "--format", "{{json .}}"), &volume) != nil || len(volume.Labels) != 1 || volume.Labels["io.nerocd.database-runtime.foreign"] != databaseRuntimeOwner {
			t.Fatal("foreign volume was adopted or modified")
		}
	}
	t.Logf("database volume denial engine=%s case=%s volume=%s unchanged=true no_writer=true", kind, caseName, f.request.Storage.VolumeName)
}

func TestHostingDatabaseNativeRuntime(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_RUNTIME") != "disposable-dind" {
		t.Skip("requires separately reviewed owned rootful DinD database gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || os.Geteuid() != 0 {
		t.Fatal("native root Linux arm64 fixture required")
	}
	profile := databaseRuntimeEngine(t, defaultImageDockerEndpoint)
	var daemon struct{ OSType, Architecture string }
	if json.Unmarshal(databaseRuntimeDocker(t, profile, "info", "--format", "{{json .}}"), &daemon) != nil || daemon.OSType != "linux" || (daemon.Architecture != "aarch64" && daemon.Architecture != "arm64") {
		t.Fatal("native database daemon architecture mismatch")
	}
	t.Logf("qualified fixture profile binary_goos=%s binary_goarch=%s daemon_os=%s daemon_architecture=%s emulation=false", runtime.GOOS, runtime.GOARCH, daemon.OSType, daemon.Architecture)
	root := "/var/lib/" + databaseRuntimeOwner
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("database fixture root must be new and private")
	}
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) { databaseRuntimePositive(t, root, kind) })
		for _, caseName := range []string{"missing", "foreign"} {
			t.Run(kind+"_"+caseName, func(t *testing.T) { databaseRuntimeNegative(t, root, kind, caseName) })
		}
	}
}
