//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

type databaseFake struct {
	mu                                                      sync.Mutex
	t                                                       *testing.T
	e                                                       *imageEngine
	plan                                                    domain.HostingApplyPlan
	req                                                     hostingOwnedVolumeRequest
	config                                                  DatabaseEngineConfig
	network                                                 map[string]any
	containers                                              map[string]map[string]any
	volume                                                  hostingInspectedVolume
	calls                                                   []string
	networkCreates, creates, starts, stops, probes, removes int
	authorized                                              bool
	hook                                                    func(context.Context, []string) ([]byte, error, bool)
}

func databaseFixture(t *testing.T, engine string) (*databaseFake, *DatabaseCredentials) {
	t.Helper()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal(err)
	}
	req := ownedVolumeRequest(t)
	req.Storage.MountPath = recipe.MountPath
	req.Storage.Hash = req.Storage.DocumentHash()
	s := domain.HostingSnapshot{ResourceID: req.Storage.ResourceID, BindingID: req.Storage.BindingID, DestinationID: req.Storage.DestinationID, ServerID: "server_database", RunnerID: "runner_database", ResourceGeneration: 1, RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion, Storage: req.Storage.Reference(), Database: domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}}
	refs := slices.Clone(recipe.Credentials)
	for i := range refs {
		refs[i].SecretID = domain.HostingDatabaseSecretID(s.ResourceID, refs[i].Role)
		refs[i].Version = 1
	}
	s.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(s, refs)
	if err != nil {
		t.Fatal(err)
	}
	p := recipe.Platforms[0]
	r := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + p.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: p.OS, Architecture: p.Architecture, Variant: p.Variant, DaemonID: req.DaemonID}
	r.Hash = domain.HostingDocumentHash(r)
	storage := domain.HostingStoragePlan{Reference: s.Storage, VolumeName: req.Storage.VolumeName, MountPath: recipe.MountPath, DaemonID: req.DaemonID, CreationOperationID: req.OriginOperationID, CreationNonce: domain.HostingStorageCreationNonce(s.Storage, req.OriginOperationID, req.DaemonID)}
	plan, err := domain.CompileHostingDatabasePlan(s, r, nil, storage, domain.HostingDatabasePlan{Reference: s.Database, NetworkName: domain.HostingDatabaseNetworkName(s.BindingID), Credentials: refs})
	if err != nil {
		t.Fatal(err)
	}
	req.CreationNonce = storage.CreationNonce
	req.OriginPlanHash = plan.Hash
	e, err := newImageEngine("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	c := DatabaseEngineConfig{stateRoot: t.TempDir(), secretRoot: t.TempDir(), uid: os.Getuid(), gid: os.Getgid(), hostCheck: func(context.Context, *imageEngine) error { return nil }}
	f := &databaseFake{t: t, e: e, plan: plan, req: req, config: c, containers: map[string]map[string]any{}, volume: ownedVolumeInspection(req)}
	f.config.readiness = func(ctx context.Context, _ string, _ domain.HostingApplyPlan, _ []DatabaseCredentialValue, readOnly, initialize bool, before func(context.Context) error) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("unbounded database probe")
		}
		if !readOnly && databaseAuthorize(ctx, before) != nil {
			return errDatabaseReadiness
		}
		f.mu.Lock()
		f.probes++
		f.mu.Unlock()
		return nil
	}
	e.command = imageCommandFunc(f.command)
	return f, databaseFixtureCredentials(t, plan)
}

func databaseFixtureCredentials(t *testing.T, plan domain.HostingApplyPlan) *DatabaseCredentials {
	t.Helper()
	values := make([]DatabaseCredentialValue, len(plan.Database.Credentials))
	for i, ref := range plan.Database.Credentials {
		value, err := NewDatabaseCredentialValue(ref, []byte(strings.Repeat(string(rune('k'+i)), 48)))
		if err != nil {
			t.Fatal(err)
		}
		values[i] = value
	}
	defer func() {
		for i := range values {
			values[i].Close()
		}
	}()
	credentials, err := NewDatabaseCredentials(plan, values, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentials.Close)
	return credentials
}

func (f *databaseFake) before(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	f.authorized = true
	f.mu.Unlock()
	return nil
}
func (f *databaseFake) evidence() hostingOwnedVolumeEvidence {
	return hostingOwnedVolumeEvidence{Storage: f.req.Storage.Reference(), VolumeName: f.req.Storage.VolumeName, DaemonID: f.req.DaemonID, CreatedAt: f.volume.CreatedAt, OriginOperationID: f.req.OriginOperationID, OriginPlanHash: f.req.OriginPlanHash, CreationNonce: f.req.CreationNonce, Driver: "local", Scope: "local", Labels: f.req.labels()}
}
func (f *databaseFake) apply(ctx context.Context, credentials *DatabaseCredentials) domain.HostingResult {
	return f.e.ApplyDatabase(ctx, f.req.OriginOperationID, f.plan, f.req, f.evidence(), credentials, f.config, f.before)
}

func (f *databaseFake) command(ctx context.Context, name string, args []string, dir string, _ []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commandLocked(ctx, name, args, dir, nil)
}

func (f *databaseFake) commandLocked(ctx context.Context, name string, args []string, dir string, _ []string) ([]byte, error) {
	if name != "docker" || dir != f.e.configDir || len(args) < 5 || !slices.Equal(args[:4], []string{"--host", f.e.endpoint, "--config", f.e.configDir}) {
		f.t.Fatal("database command lost explicit daemon isolation")
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("unbounded database command")
	}
	a := args[4:]
	f.calls = append(f.calls, strings.Join(a, " "))
	for _, value := range a {
		if strings.Contains(value, strings.Repeat("k", 48)) || value == "--publish" || value == "--privileged" || value == "sh" || value == "bash" {
			f.t.Fatal("secret/public port/shell escaped private recipe")
		}
	}
	mutation := len(a) > 1 && (a[1] == "create" || a[1] == "start" || a[1] == "stop" || a[1] == "pull" || a[1] == "rm")
	if mutation {
		if !f.authorized {
			f.t.Fatal("Docker mutation without current-authority callback")
		}
		f.authorized = false
	}
	if f.hook != nil {
		if raw, err, handled := f.hook(ctx, a); handled {
			return raw, err
		}
	}
	switch {
	case slices.Equal(a, []string{"info", "--format", "{{json .ID}}"}):
		return json.Marshal(f.req.DaemonID)
	case len(a) > 1 && a[0] == "image" && a[1] == "inspect":
		if a[len(a)-1] == "{{json .Config.Env}}" {
			return []byte("[]"), nil
		}
		return json.Marshal(map[string]any{"Id": f.plan.Release.ImageID, "RepoDigests": []string{f.plan.Release.CanonicalImage}, "Os": f.plan.Release.OS, "Architecture": f.plan.Release.Architecture, "Variant": f.plan.Release.Variant})
	case len(a) > 1 && a[0] == "image" && a[1] == "pull":
		return []byte("pulled"), nil
	case len(a) > 1 && a[0] == "network" && a[1] == "ls":
		if f.network != nil {
			return []byte(f.plan.Database.NetworkName + "\n"), nil
		}
		return nil, nil
	case len(a) > 1 && a[0] == "network" && a[1] == "create":
		f.networkCreates++
		labels := map[string]string{}
		for i := 0; i < len(a)-1; i++ {
			if a[i] == "--label" {
				key, value, _ := strings.Cut(a[i+1], "=")
				labels[key] = value
			}
		}
		f.network = map[string]any{"Id": strings.Repeat("d", 64), "Name": f.plan.Database.NetworkName, "Driver": "bridge", "Scope": "local", "Internal": true, "Labels": labels, "Options": map[string]string{}, "IPAM": map[string]any{"Driver": "default", "Config": []map[string]string{{"Subnet": "172.28.0.0/16", "Gateway": "172.28.0.1"}}}, "Containers": map[string]any{}}
		return []byte(strings.Repeat("d", 64) + "\n"), nil
	case len(a) > 1 && a[0] == "network" && a[1] == "inspect":
		return json.Marshal(f.network)
	case len(a) > 1 && a[0] == "volume" && a[1] == "ls":
		return []byte(f.req.Storage.VolumeName + "\n"), nil
	case len(a) > 1 && a[0] == "volume" && a[1] == "inspect":
		return json.Marshal(f.volume)
	case len(a) > 1 && a[0] == "container" && a[1] == "ls":
		var ids []string
		for id, container := range f.containers {
			filter := a[len(a)-1]
			if strings.HasPrefix(filter, "volume=") || filter == "name=^"+container["Name"].(string)+"$" || filter == "id="+id {
				ids = append(ids, id)
			}
		}
		return []byte(strings.Join(ids, "\n")), nil
	case len(a) > 1 && a[0] == "container" && a[1] == "inspect":
		return json.Marshal(f.containers[a[2]])
	case len(a) > 1 && a[0] == "container" && a[1] == "exec":
		if !slices.Equal(a[2:], []string{"--user", "0:0", a[4], "stat", "-c", "%u:%g", "/proc/1"}) || f.containers[a[4]] == nil {
			f.t.Fatal("noncanonical database process inspection")
		}
		return []byte("999:999\n"), nil
	case len(a) > 1 && a[0] == "container" && a[1] == "create":
		f.creates++
		id := fmt.Sprintf("%064x", f.creates)
		labels := map[string]string{}
		var env []string
		for i := 0; i < len(a)-1; i++ {
			if a[i] == "--label" {
				key, value, _ := strings.Cut(a[i+1], "=")
				labels[key] = value
			}
			if a[i] == "--env" {
				env = append(env, a[i+1])
			}
		}
		_, cmd := databaseRecipeArgs(f.plan)
		f.containers[id] = map[string]any{"Id": id, "Name": "/" + a[3], "Image": f.plan.Release.ImageID, "Config": map[string]any{"Labels": labels, "Env": env, "Cmd": cmd, "Entrypoint": []string{"docker-entrypoint.sh"}}, "State": map[string]any{"Running": false, "Status": "created"}, "HostConfig": map[string]any{"NetworkMode": f.plan.Database.NetworkName, "IpcMode": "private", "RestartPolicy": map[string]any{"Name": "unless-stopped"}}, "Mounts": []map[string]any{{"Type": "volume", "Name": f.plan.Storage.VolumeName, "Driver": "local", "Destination": f.plan.Storage.MountPath, "RW": true}, {"Type": "bind", "Source": filepath.Join(f.config.secretRoot, databaseGeneration(f.currentOperation(a), f.plan)), "Destination": databaseSecretMount, "RW": false}}, "NetworkSettings": map[string]any{"Networks": map[string]any{f.plan.Database.NetworkName: map[string]any{"NetworkID": strings.Repeat("d", 64)}}}}
		return []byte(id + "\n"), nil
	case len(a) > 1 && a[0] == "container" && a[1] == "start":
		f.starts++
		id := a[2]
		f.containers[id]["State"] = map[string]any{"Running": true, "Status": "running"}
		f.network["Containers"].(map[string]any)[id] = map[string]string{"EndpointID": strings.Repeat("a", 64), "IPv4Address": "172.28.0." + string(rune('2'+f.starts-1)) + "/16"}
		return []byte(id), nil
	case len(a) > 1 && a[0] == "container" && a[1] == "stop":
		f.stops++
		id := a[len(a)-1]
		f.containers[id]["State"] = map[string]any{"Running": false, "Status": "exited"}
		delete(f.network["Containers"].(map[string]any), id)
		return []byte(id), nil
	case len(a) > 1 && a[0] == "container" && a[1] == "rm":
		if len(a) != 3 || f.containers[a[2]] == nil || f.containers[a[2]]["State"].(map[string]any)["Running"].(bool) {
			f.t.Fatal("retirement attempted nonexact or running container removal")
		}
		f.removes++
		delete(f.containers, a[2])
		delete(f.network["Containers"].(map[string]any), a[2])
		return []byte(a[2]), nil
	default:
		f.t.Fatalf("unexpected database command %q", strings.Join(a, " "))
		return nil, errors.New("unexpected command")
	}
}

func (f *databaseFake) currentOperation(args []string) string {
	for i := range args {
		if args[i] == "--label" && strings.HasPrefix(args[i+1], imageOperationLabel+"=") {
			return strings.TrimPrefix(args[i+1], imageOperationLabel+"=")
		}
	}
	f.t.Fatal("missing operation label")
	return ""
}

func TestDatabaseEngineApplyAndReadOnlyObserve(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, credentials := databaseFixture(t, engine)
			result := f.apply(context.Background(), credentials)
			if result.Outcome != "succeeded" || !result.Ready || result.Uncertain || f.networkCreates != 1 || f.creates != 1 || f.starts != 1 {
				t.Fatalf("private engine failed: %+v counts %d/%d/%d", result, f.networkCreates, f.creates, f.starts)
			}
			calls := len(f.calls)
			before := f.probes
			observation, err := f.e.ObserveDatabase(context.Background(), domain.HostingOperation{ID: f.req.OriginOperationID, Plan: &f.plan, Result: &result}, credentials, f.config)
			if err != nil || !observation.Ready || observation.ContainerID != result.ContainerID || f.probes != before+1 {
				t.Fatal("exact read-only observation failed", err)
			}
			for _, call := range f.calls[calls:] {
				if strings.Contains(call, " start ") || strings.Contains(call, " create ") || strings.Contains(call, " stop ") {
					t.Fatal("observation mutated Docker")
				}
			}
		})
	}
}

func TestDatabaseEngineSecondReleaseStopsSameVolumeWriter(t *testing.T) {
	f, credentials := databaseFixture(t, "postgresql")
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal(first)
	}
	oldPlan := f.plan
	old := domain.HostingHealthy{OperationID: f.req.OriginOperationID, ContainerID: first.ContainerID, Snapshot: oldPlan.Snapshot, Release: oldPlan.Release, PlanHash: oldPlan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *oldPlan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
	next := oldPlan.Snapshot
	next.ResourceGeneration++
	var err error
	f.plan, err = domain.CompileHostingDatabasePlan(next, oldPlan.Release, &old, *oldPlan.Storage, *oldPlan.Database)
	if err != nil {
		t.Fatal(err)
	}
	nextCredentials := databaseFixtureCredentials(t, f.plan)
	result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), nextCredentials, f.config, f.before)
	if !result.Ready || result.Uncertain || result.PreviousContainerID != first.ContainerID || f.networkCreates != 1 || f.creates != 2 || f.starts != 2 || f.stops != 1 {
		t.Fatalf("second release did not preserve stopped writer/volume: %+v counts %d/%d/%d/%d", result, f.networkCreates, f.creates, f.starts, f.stops)
	}
	if f.containers[first.ContainerID] != nil || f.removes != 1 {
		t.Fatal("proven stopped predecessor was not retired")
	}
	record, exists, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
	if err != nil || !exists || record.OriginPlan != oldPlan.Hash || record.OriginPlan == f.plan.Hash || !record.InitReady {
		t.Fatal("release replaced original initialization identity")
	}
}

func TestDatabaseEngineRejectsAuthorityCredentialsAndVolumeTamper(t *testing.T) {
	for _, test := range []string{"nil_authority", "expired", "wrong_metadata", "wrong_evidence", "wrong_daemon", "incompatible_host"} {
		t.Run(test, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			before := f.before
			switch test {
			case "nil_authority":
				before = nil
			case "expired":
				credentials.until = time.Now().Add(-time.Second)
			case "wrong_metadata":
				f.req.Storage.CreatedBy = ""
			case "wrong_evidence":
				f.volume.CreatedAt = ""
			case "wrong_daemon":
				f.req.DaemonID = "foreign"
			case "incompatible_host":
				f.config.hostCheck = func(context.Context, *imageEngine) error { return errors.New("host unsupported") }
			}
			result := f.e.ApplyDatabase(context.Background(), f.req.OriginOperationID, f.plan, f.req, f.evidence(), credentials, f.config, before)
			if result.Ready || f.networkCreates != 0 || f.creates != 0 || f.starts != 0 {
				t.Fatal("invalid database preflight started mutation")
			}
		})
	}
}

func TestDatabaseEngineLostContainerResponseDoesNotDuplicate(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	lost := true
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "container" && args[1] == "create" && lost {
			lost = false
			return nil, errors.New("private response payload must be redacted"), true
		}
		return nil, nil, false
	}
	first := f.apply(context.Background(), credentials)
	if first.Ready || !first.Uncertain {
		t.Fatal("lost container response lost uncertainty")
	}
	second := f.apply(context.Background(), credentials)
	if second.Ready || !second.Uncertain || f.creates != 0 || f.starts != 0 {
		t.Fatal("unknown create was blindly reissued")
	}
}

func TestDatabaseEngineReadinessFailureKeepsWriterAndBoundedRecovery(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	ctx, cancel := context.WithCancel(context.Background())
	f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
		cancel()
		return errors.New("untrusted secret response")
	}
	failed := f.apply(ctx, credentials)
	if failed.Ready || !failed.Uncertain || failed.ContainerID == "" || f.starts != 1 {
		t.Fatal("failed readiness claimed rollback", failed)
	}
	f.config.readiness = func(ctx context.Context, _ string, _ domain.HostingApplyPlan, _ []DatabaseCredentialValue, _ bool, _ bool, before func(context.Context) error) error {
		return databaseAuthorize(ctx, before)
	}
	recovered := f.apply(context.Background(), credentials)
	if !recovered.Ready || recovered.ContainerID != failed.ContainerID || f.creates != 1 || f.starts != 1 {
		t.Fatal("exact live recovery created a duplicate writer", recovered)
	}
}

func TestDatabaseEngineForeignVolumeAndContainerPolicyBlockWriters(t *testing.T) {
	for _, kind := range []string{"foreign_volume", "unknown_volume_user", "published_port", "loader_env", "secret_mount_writable", "foreign_image", "unexpected_mount", "foreign_label"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "postgresql")
			if kind == "foreign_volume" {
				f.volume.Labels["foreign"] = "label"
				result := f.apply(context.Background(), credentials)
				if result.Ready || f.networkCreates != 0 || f.creates != 0 {
					t.Fatal("foreign volume caused mutation")
				}
				return
			}
			first := f.apply(context.Background(), credentials)
			if !first.Ready {
				t.Fatal(first)
			}
			container := f.containers[first.ContainerID]
			switch kind {
			case "unknown_volume_user":
				f.containers[strings.Repeat("f", 64)] = container
			case "published_port":
				container["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"5432/tcp": []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "5432"}}}
			case "loader_env":
				config := container["Config"].(map[string]any)
				config["Env"] = append(config["Env"].([]string), "LD_PRELOAD=/data/foreign.so")
			case "secret_mount_writable":
				container["Mounts"].([]map[string]any)[1]["RW"] = true
			case "foreign_image":
				container["Image"] = "sha256:" + strings.Repeat("f", 64)
			case "unexpected_mount":
				container["Mounts"] = append(container["Mounts"].([]map[string]any), map[string]any{"Type": "bind", "Source": "/", "Destination": "/foreign", "RW": true})
			case "foreign_label":
				container["Config"].(map[string]any)["Labels"].(map[string]string)["foreign"] = "label"
			}
			_, err := f.e.ObserveDatabase(context.Background(), domain.HostingOperation{ID: f.req.OriginOperationID, Plan: &f.plan, Result: &first}, credentials, f.config)
			if err == nil {
				t.Fatal("read-only observation accepted foreign writer policy")
			}
			result := f.apply(context.Background(), credentials)
			if result.Ready || f.creates != 1 || f.starts != 1 {
				t.Fatal("foreign volume/container policy admitted another writer")
			}
		})
	}
}

func TestDatabaseEngineAuthorityExpiryAtMutationKeepsUncertainty(t *testing.T) {
	for _, at := range []string{"network", "create", "start", "readiness"} {
		t.Run(at, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if len(args) > 1 && (at == "network" && args[0] == "network" && args[1] == "create" || at == "create" && args[0] == "container" && args[1] == "create" || at == "start" && args[0] == "container" && args[1] == "start") {
					cancel()
					return nil, ctx.Err(), true
				}
				return nil, nil, false
			}
			if at == "readiness" {
				f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
					credentials.until = time.Now().Add(-time.Second)
					cancel()
					return nil
				}
			}
			result := f.apply(ctx, credentials)
			if result.Ready || result.Outcome == "succeeded" || !result.Uncertain {
				t.Fatal("expired/uncertain mutation reported successful rollback", result)
			}
		})
	}
}

func TestDatabaseEnginePredecessorStopMustBeConfirmed(t *testing.T) {
	f, credentials := databaseFixture(t, "postgresql")
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal(first)
	}
	oldPlan := f.plan
	old := domain.HostingHealthy{OperationID: f.req.OriginOperationID, ContainerID: first.ContainerID, Snapshot: oldPlan.Snapshot, Release: oldPlan.Release, PlanHash: oldPlan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *oldPlan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
	next := oldPlan.Snapshot
	next.ResourceGeneration++
	var err error
	f.plan, err = domain.CompileHostingDatabasePlan(next, oldPlan.Release, &old, *oldPlan.Storage, *oldPlan.Database)
	if err != nil {
		t.Fatal(err)
	}
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "container" && args[1] == "stop" {
			return []byte(first.ContainerID), nil, true
		}
		return nil, nil, false
	}
	result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), databaseFixtureCredentials(t, f.plan), f.config, f.before)
	if result.Ready || !result.Uncertain || f.creates != 1 || f.starts != 1 {
		t.Fatal("unconfirmed old writer stop allowed replacement")
	}
}

func TestDatabaseEngineObserveIncompleteInitProbesButCannotSettle(t *testing.T) {
	f, credentials := databaseFixture(t, "postgresql")
	result := f.apply(context.Background(), credentials)
	if !result.Ready {
		t.Fatal(result)
	}
	name := f.plan.Database.NetworkName + ".json"
	record, exists, err := f.config.readRecord(name)
	if err != nil || !exists {
		t.Fatal("missing history")
	}
	record.InitReady = false
	if err = f.config.writeRecord(context.Background(), name, record, f.before); err != nil {
		t.Fatal(err)
	}
	probes := f.probes
	observation, err := f.e.ObserveDatabase(context.Background(), domain.HostingOperation{ID: f.req.OriginOperationID, Plan: &f.plan, Result: &result}, credentials, f.config)
	if !errors.Is(err, errDatabaseInitializationUnresolved) || observation.Ready || observation.ContainerID != "" || f.probes != probes+1 {
		t.Fatal("incomplete init was silently settled or authentication probe unreachable", err)
	}
	got, _, err := f.config.readRecord(name)
	if err != nil || got.InitReady {
		t.Fatal("read-only observation wrote initialization history")
	}
	if f.creates != 1 || f.starts != 1 || f.stops != 0 {
		t.Fatal("read-only incomplete initialization mutated writer")
	}
}

func TestDatabasePreparePinsChildManifestAndConfigIdentity(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, _ := databaseFixture(t, engine)
			recipe, err := domain.HostingDatabaseRecipeFor(f.plan.Database.Reference.Engine, f.plan.Database.Reference.RecipeVersion)
			if err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, p := range recipe.Platforms {
				if p.Architecture == runtime.GOARCH {
					f.plan.Release.CanonicalImage = strings.Split(recipe.ImageReference, "@")[0] + "@" + p.Digest
					f.plan.Release.OS = p.OS
					f.plan.Release.Architecture = p.Architecture
					f.plan.Release.Variant = p.Variant
					f.plan.Release.Hash = ""
					f.plan.Release.Hash = domain.HostingDocumentHash(f.plan.Release)
					matched = true
				}
			}
			if !matched {
				t.Skip("test host architecture has no frozen recipe")
			}
			release, err := f.e.PrepareDatabase(context.Background(), f.plan.Snapshot, f.config, f.before)
			if err != nil || release != f.plan.Release || release.CanonicalImage == f.plan.Snapshot.ImageReference {
				t.Fatal("preparation confused index/child/config identity", err)
			}
			for _, call := range f.calls {
				if strings.Contains(call, "container create") || strings.Contains(call, "network create") || strings.Contains(call, "volume create") {
					t.Fatal("preparation mutated workload")
				}
			}
			calls := len(f.calls)
			if _, err = f.e.PrepareDatabase(context.Background(), f.plan.Snapshot, f.config, nil); err == nil {
				t.Fatal("image pull accepted missing authority")
			}
			for _, call := range f.calls[calls:] {
				if strings.Contains(call, "image pull") {
					t.Fatal("unauthorized image pull started")
				}
			}
		})
	}
}

func TestDatabaseEngineRuntimeUserAndCapabilityFailuresCannotReportHealthy(t *testing.T) {
	for _, kind := range []string{"runtime_root", "stat_failed", "cap_drop", "foreign_runtime"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if len(args) > 1 && args[0] == "container" && args[1] == "exec" {
					if kind == "runtime_root" {
						return []byte("0:0\n"), nil, true
					}
					if kind == "stat_failed" {
						return nil, errors.New("stat unavailable"), true
					}
				}
				return nil, nil, false
			}
			if kind == "cap_drop" || kind == "foreign_runtime" {
				f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
					for _, container := range f.containers {
						host := container["HostConfig"].(map[string]any)
						if kind == "cap_drop" {
							host["CapDrop"] = []string{"ALL"}
						} else {
							host["Runtime"] = "foreign"
						}
					}
					return nil
				}
			}
			result := f.apply(context.Background(), credentials)
			if result.Ready || !result.Uncertain {
				t.Fatal("unverified runtime identity became healthy", result)
			}
		})
	}
	for _, status := range []string{"", "CapEff: 0000000000000001\n", "CapEff: 0000000000000002\n", "CapEff: not-hex\n"} {
		if databaseHostCapabilities([]byte(status)) {
			t.Fatal("missing host ownership capabilities accepted")
		}
	}
	if !databaseHostCapabilities([]byte("CapEff:\t0000000000000003\n")) {
		t.Fatal("supported ownership capability proof rejected")
	}
}

func TestDatabaseEngineConcurrentExactOperationHasOneWriter(t *testing.T) {
	f, firstCredentials := databaseFixture(t, "redis")
	secondCredentials := databaseFixtureCredentials(t, f.plan)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan domain.HostingResult, 2)
	for _, credentials := range []*DatabaseCredentials{firstCredentials, secondCredentials} {
		go func(credentials *DatabaseCredentials) { <-start; results <- f.apply(ctx, credentials) }(credentials)
	}
	close(start)
	for range 2 {
		select {
		case result := <-results:
			if !result.Ready || result.Uncertain {
				t.Fatal("exact operation did not settle", result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent database calls failed to join")
		}
	}
	if f.networkCreates != 1 || f.creates != 1 || f.starts != 1 {
		t.Fatalf("exact duplicate created multiple writers: %d/%d/%d", f.networkCreates, f.creates, f.starts)
	}
}

func TestDatabaseEngineLostStartThenPreflightFailureRetainsHold(t *testing.T) {
	f, credentials := databaseFixture(t, "postgresql")
	lost := true
	f.hook = func(ctx context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "container" && args[1] == "start" && lost {
			lost = false
			hook := f.hook
			f.hook = nil
			f.authorized = true
			_, err := f.commandLocked(ctx, "docker", append([]string{"--host", f.e.endpoint, "--config", f.e.configDir}, args...), f.e.configDir, nil)
			f.hook = hook
			if err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("start response lost"), true
		}
		return nil, nil, false
	}
	first := f.apply(context.Background(), credentials)
	if first.Ready || !first.Uncertain || f.starts != 1 {
		t.Fatal("possible live writer not retained after lost start", first)
	}
	credentials.until = time.Now().Add(-time.Second)
	calls := len(f.calls)
	second := f.apply(context.Background(), credentials)
	if second.Ready || !second.Uncertain || second.Outcome != "failed" || len(f.calls) != calls || f.creates != 1 || f.starts != 1 {
		t.Fatal("preflight failure erased prior writer uncertainty", second)
	}
	for _, container := range f.containers {
		if !container["State"].(map[string]any)["Running"].(bool) {
			t.Fatal("fixture did not retain possible writer")
		}
	}
}

func databaseNextPlan(t *testing.T, f *databaseFake, previousOperation string, previous domain.HostingResult) *DatabaseCredentials {
	t.Helper()
	old := f.plan
	healthy := domain.HostingHealthy{OperationID: previousOperation, ContainerID: previous.ContainerID, Snapshot: old.Snapshot, Release: old.Release, PlanHash: old.Hash, Storage: &domain.HostingHealthyStorage{Plan: *old.Storage, CreationPlanHash: f.req.OriginPlanHash}}
	next := old.Snapshot
	next.ResourceGeneration++
	var err error
	f.plan, err = domain.CompileHostingDatabasePlan(next, old.Release, &healthy, *old.Storage, *old.Database)
	if err != nil {
		t.Fatal(err)
	}
	return databaseFixtureCredentials(t, f.plan)
}

func TestDatabaseEngineThreeReleasesRetireOnlyStoppedPredecessors(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, credentials := databaseFixture(t, engine)
			previous := f.apply(context.Background(), credentials)
			if !previous.Ready {
				t.Fatal("release A failed", previous)
			}
			operation := f.req.OriginOperationID
			for _, next := range []string{"hop_second", "hop_third"} {
				credentials = databaseNextPlan(t, f, operation, previous)
				result := f.e.ApplyDatabase(context.Background(), next, f.plan, f.req, f.evidence(), credentials, f.config, f.before)
				if !result.Ready || result.Uncertain {
					t.Fatalf("release %s blocked by predecessor history: %+v", next, result)
				}
				operation, previous = next, result
			}
			if len(f.containers) != 1 || f.creates != 3 || f.starts != 3 || f.stops != 2 || f.networkCreates != 1 {
				t.Fatal("retirement did not leave one writer with stable network/volume")
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "volume create") || strings.HasPrefix(call, "volume rm") || strings.Contains(call, "--force") || strings.Contains(call, "--volumes") {
					t.Fatal("retirement changed volume or forced a container removal")
				}
			}
		})
	}
}

func TestDatabaseEngineRetirementLostResponseAndCompletedReplay(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, credentials := databaseFixture(t, engine)
			first := f.apply(context.Background(), credentials)
			if !first.Ready {
				t.Fatal(first)
			}
			credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
			lost := true
			f.hook = func(ctx context.Context, args []string) ([]byte, error, bool) {
				if len(args) > 1 && args[0] == "container" && args[1] == "rm" && lost {
					lost = false
					record, _, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
					if err != nil {
						t.Fatal(err)
					}
					intent, exists, err := f.config.readRetirement("hop_second", f.plan, record, fmt.Sprintf("%064x", 2))
					if err != nil || !exists || intent.Completed || intent.PredecessorID != first.ContainerID {
						t.Fatal("retirement side effect preceded exact durable intent")
					}
					hook := f.hook
					f.hook = nil
					f.authorized = true
					_, err = f.commandLocked(ctx, "docker", append([]string{"--host", f.e.endpoint, "--config", f.e.configDir}, args...), f.e.configDir, nil)
					f.hook = hook
					if err != nil {
						t.Fatal(err)
					}
					return nil, errors.New("retirement response lost"), true
				}
				return nil, nil, false
			}
			apply := func() domain.HostingResult {
				return f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
			}
			failed := apply()
			if failed.Ready || !failed.Uncertain || failed.ContainerID == "" || f.removes != 1 || f.containers[first.ContainerID] != nil {
				t.Fatal("lost removal response did not retain uncertainty", failed)
			}
			observation, err := f.e.ObserveDatabase(context.Background(), domain.HostingOperation{ID: "hop_second", Plan: &f.plan, Result: &failed}, credentials, f.config)
			if !errors.Is(err, errDatabaseRetirementUnresolved) || observation.Ready || f.removes != 1 {
				t.Fatal("readiness alone settled incomplete retirement", err)
			}
			for range 2 {
				result := apply()
				if !result.Ready || result.Uncertain || result.ContainerID != failed.ContainerID || result.PreviousContainerID != first.ContainerID || f.creates != 2 || f.starts != 2 || f.removes != 1 {
					t.Fatal("retirement replay required missing predecessor or duplicated a writer", result)
				}
			}
			record, _, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
			if err != nil {
				t.Fatal(err)
			}
			intent, exists, err := f.config.readRetirement("hop_second", f.plan, record, failed.ContainerID)
			if err != nil || !exists || !intent.Completed {
				t.Fatal("confirmed retirement was not durably completed")
			}
			settled := apply()
			observation, err = f.e.ObserveDatabase(context.Background(), domain.HostingOperation{ID: "hop_second", Plan: &f.plan, Result: &settled}, credentials, f.config)
			if err != nil || !observation.Ready || f.removes != 1 {
				t.Fatal("completed retirement could not be observed read-only", err)
			}
		})
	}
}

func TestDatabaseEngineRetirementNeedsExactExistingReadyReplacement(t *testing.T) {
	for _, kind := range []string{"missing_predecessor_no_intent", "missing_successor", "stopped_successor", "changed_intent", "foreign_user", "running_predecessor"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			first := f.apply(context.Background(), credentials)
			if !first.Ready {
				t.Fatal(first)
			}
			credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
			if kind == "missing_predecessor_no_intent" {
				delete(f.containers, first.ContainerID)
				delete(f.network["Containers"].(map[string]any), first.ContainerID)
			} else {
				f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
					if len(args) > 1 && args[0] == "container" && args[1] == "rm" {
						return nil, errors.New("retirement not acknowledged"), true
					}
					return nil, nil, false
				}
				failed := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
				if failed.Ready || !failed.Uncertain || failed.ContainerID == "" {
					t.Fatal("fixture did not retain unresolved retirement", failed)
				}
				f.hook = nil
				switch kind {
				case "missing_successor":
					delete(f.containers, failed.ContainerID)
					delete(f.network["Containers"].(map[string]any), failed.ContainerID)
				case "stopped_successor":
					f.containers[failed.ContainerID]["State"] = map[string]any{"Running": false, "Status": "exited"}
					delete(f.network["Containers"].(map[string]any), failed.ContainerID)
				case "changed_intent":
					name := databaseRetirementName("hop_second", f.plan)
					raw, err := os.ReadFile(filepath.Join(f.config.stateRoot, name))
					if err != nil {
						t.Fatal(err)
					}
					var intent databaseRetirement
					if json.Unmarshal(raw, &intent) != nil {
						t.Fatal("invalid fixture intent")
					}
					intent.PredecessorPlan = "sha256:" + strings.Repeat("f", 64)
					raw, err = json.Marshal(intent)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(f.config.stateRoot, name), raw, 0o600); err != nil {
						t.Fatal(err)
					}
				case "foreign_user":
					f.containers[strings.Repeat("f", 64)] = f.containers[failed.ContainerID]
				case "running_predecessor":
					f.containers[first.ContainerID]["State"] = map[string]any{"Running": true, "Status": "running"}
				}
			}
			creates, starts, removes := f.creates, f.starts, f.removes
			result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
			if result.Ready || !result.Uncertain || f.creates != creates || f.starts != starts || f.removes != removes {
				t.Fatal("unproven retirement caused mutation or settlement", result)
			}
		})
	}
}

func TestDatabaseEngineFailedReplacementReadinessRetainsPredecessor(t *testing.T) {
	f, credentials := databaseFixture(t, "postgresql")
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal(first)
	}
	credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
		cancel()
		return errDatabaseReadiness
	}
	failed := f.e.ApplyDatabase(ctx, "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if failed.Ready || !failed.Uncertain || f.removes != 0 || f.containers[first.ContainerID] == nil || f.containers[first.ContainerID]["State"].(map[string]any)["Running"].(bool) {
		t.Fatal("failed readiness retired predecessor or fabricated restoration", failed)
	}
	if _, err := os.Stat(filepath.Join(f.config.stateRoot, databaseRetirementName("hop_second", f.plan))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed readiness admitted predecessor retirement")
	}
	f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
		return nil
	}
	result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if !result.Ready || f.creates != 2 || f.starts != 2 || f.removes != 1 {
		t.Fatal("authorized recovery failed to finish existing ready replacement", result)
	}
}

func TestDatabaseEngineRetirementRetriesPresentStoppedTargetOnly(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal(first)
	}
	credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "container" && args[1] == "rm" {
			return nil, errors.New("removal rejected"), true
		}
		return nil, nil, false
	}
	failed := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if failed.Ready || !failed.Uncertain || f.removes != 0 || f.containers[first.ContainerID] == nil {
		t.Fatal("removal failure lost stopped predecessor", failed)
	}
	f.hook = nil
	result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if !result.Ready || result.Uncertain || f.removes != 1 || f.creates != 2 || f.starts != 2 {
		t.Fatal("safe exact removal retry failed", result)
	}
}

func TestDatabaseEngineRetirementRevalidatesAfterIntent(t *testing.T) {
	for _, kind := range []string{"authority_lost", "ownership_changed", "replacement_stopped"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "postgresql")
			first := f.apply(context.Background(), credentials)
			if !first.Ready {
				t.Fatal(first)
			}
			credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
			changed := false
			before := func(ctx context.Context) error {
				if _, err := os.Stat(filepath.Join(f.config.stateRoot, databaseRetirementName("hop_second", f.plan))); err == nil && !changed {
					changed = true
					if kind == "authority_lost" {
						return errors.New("stale lease")
					}
					if kind == "ownership_changed" {
						f.containers[first.ContainerID]["Config"].(map[string]any)["Labels"].(map[string]string)["foreign"] = "label"
					}
					if kind == "replacement_stopped" {
						for id, container := range f.containers {
							if id != first.ContainerID {
								container["State"] = map[string]any{"Running": false, "Status": "exited"}
							}
						}
					}
				}
				return f.before(ctx)
			}
			result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, before)
			if !changed || result.Ready || !result.Uncertain || f.removes != 0 || f.containers[first.ContainerID] == nil {
				t.Fatal("retirement skipped post-intent authority/ownership proof", result)
			}
		})
	}
}

func TestDatabaseEngineRetirementAbsenceNeedsSuccessfulListingAndRetainedIntent(t *testing.T) {
	for _, kind := range []string{"listing_failed", "intent_missing", "completed_target_reappeared"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			first := f.apply(context.Background(), credentials)
			if !first.Ready {
				t.Fatal(first)
			}
			old := f.containers[first.ContainerID]
			credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
			result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
			if !result.Ready {
				t.Fatal(result)
			}
			switch kind {
			case "listing_failed":
				f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
					if len(args) > 1 && args[0] == "container" && args[1] == "ls" && args[len(args)-1] == "id="+first.ContainerID {
						return nil, errors.New("list unavailable"), true
					}
					return nil, nil, false
				}
			case "intent_missing":
				if err := os.Remove(filepath.Join(f.config.stateRoot, databaseRetirementName("hop_second", f.plan))); err != nil {
					t.Fatal(err)
				}
			case "completed_target_reappeared":
				f.containers[first.ContainerID] = old
			}
			result = f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
			if result.Ready || !result.Uncertain || f.removes != 1 || f.creates != 2 || f.starts != 2 {
				t.Fatal("unproven absence or completed permission reused", result)
			}
		})
	}
}

func TestDatabaseEngineRetirementAbsenceMustBelongToPinnedDaemon(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal(first)
	}
	credentials = databaseNextPlan(t, f, f.req.OriginOperationID, first)
	f.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if f.removes == 1 && slices.Equal(args, []string{"info", "--format", "{{json .ID}}"}) {
			return []byte(`"different-daemon"`), nil, true
		}
		return nil, nil, false
	}
	result := f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if result.Ready || !result.Uncertain || f.removes != 1 {
		t.Fatal("foreign-daemon absence settled retirement", result)
	}
	record, _, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
	if err != nil {
		t.Fatal(err)
	}
	intent, exists, err := f.config.readRetirement("hop_second", f.plan, record, result.ContainerID)
	if err != nil || !exists || intent.Completed {
		t.Fatal("foreign-daemon absence was durably completed")
	}
	f.hook = nil
	result = f.e.ApplyDatabase(context.Background(), "hop_second", f.plan, f.req, f.evidence(), credentials, f.config, f.before)
	if !result.Ready || f.removes != 1 || f.creates != 2 || f.starts != 2 {
		t.Fatal("pinned-daemon exact absence could not reconcile", result)
	}
}
