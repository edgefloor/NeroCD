//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

type applicationDatabaseFake struct {
	t                    *testing.T
	e                    *applicationDatabaseEngine
	in                   applicationDatabaseEngineInput
	db                   *databaseFake
	apps                 map[string]applicationDatabaseContainer
	bridgeID             string
	authorized           bool
	effects              []string
	createErr, removeErr error
	startErr             error
	returnID             bool
	ready                bool
}

func TestApplicationDatabaseEngineOwnsIsolatedConfiguration(t *testing.T) {
	e, err := newApplicationDatabaseEngine("", DatabaseEngineConfig{stateRoot: t.TempDir(), secretRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	path := e.image.configDir
	if e.create == nil {
		t.Fatal("missing protected creation transport")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private Docker configuration retained", err)
	}
}

func applicationDatabaseFixture(t *testing.T, engine string, previous bool) (*applicationDatabaseFake, *hostingruntimeprotocol.Environment) {
	t.Helper()
	db, credentials := databaseFixture(t, engine)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result := db.apply(ctx, credentials)
	if !result.Ready {
		t.Fatal("fake DB could not establish original owned record")
	}
	databaseContainer := db.containers[result.ContainerID]
	databaseEndpoint := databaseContainer["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[db.plan.Database.NetworkName].(map[string]any)
	databaseEndpoint["EndpointID"] = db.network["Containers"].(map[string]any)[result.ContainerID].(map[string]string)["EndpointID"]
	databaseEndpoint["DNSNames"] = []string{strings.TrimPrefix(databaseContainer["Name"].(string), "/"), result.ContainerID[:12]}
	record, exists, err := db.config.readRecord(db.plan.Database.NetworkName + ".json")
	if err != nil || !exists {
		t.Fatal("missing initialized record")
	}
	var origin domain.HostingDatabaseNetworkOrigin
	raw, err := json.Marshal(record)
	if err != nil || json.Unmarshal(raw, &origin) != nil {
		t.Fatal("record proof conversion")
	}
	r := imageTestRelease()
	r.DaemonID = db.plan.Release.DaemonID
	r.Hash = ""
	r.Hash = domain.HostingDocumentHash(r)
	s := imageTestPlan(t).Snapshot
	s.PolicyVersion, s.DestinationID, s.ServerID, s.RunnerID = domain.HostingRuntimePolicyVersion, db.plan.Snapshot.DestinationID, db.plan.Snapshot.ServerID, db.plan.Snapshot.RunnerID
	s.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}
	b := domain.HostingDatabaseBinding{Version: domain.HostingDatabaseBindingVersion, ID: domain.HostingDatabaseBindingID(s.BindingID, db.plan.Snapshot.BindingID), Generation: 1, ApplicationResourceID: s.ResourceID, ApplicationResourceGeneration: s.ResourceGeneration, ApplicationBindingID: s.BindingID, DatabaseResourceID: db.plan.Snapshot.ResourceID, DatabaseResourceGeneration: db.plan.Snapshot.ResourceGeneration, DatabaseBindingID: db.plan.Snapshot.BindingID, ProjectID: "project", DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, Database: db.plan.Snapshot.Database, NetworkName: db.plan.Database.NetworkName, Alias: domain.HostingDatabaseBindingAlias(db.plan.Snapshot.BindingID), EnvironmentNames: domain.HostingDatabaseBindingEnvironmentNames(engine), RuntimeConfiguration: s.RuntimeConfiguration}
	for _, ref := range db.plan.Database.Credentials {
		if ref.Role == "app" {
			b.Credential = ref
		}
	}
	b.Hash = b.DocumentHash()
	h := domain.HostingHealthy{Snapshot: db.plan.Snapshot, Release: db.plan.Release, OperationID: db.plan.Storage.CreationOperationID, ContainerID: result.ContainerID, PlanHash: db.plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *db.plan.Storage, CreationPlanHash: db.plan.Hash}}
	proof := domain.HostingApplicationDatabasePlanInput{OperationID: "app_deploy", CreateNonce: strings.Repeat("a", 32), Binding: b, ApplicationProjectID: "project", DatabaseProjectID: "project", BindingApplicationSnapshot: s, BindingDatabasePlan: db.plan, DatabasePlan: db.plan, DatabaseHealthy: h, Origin: origin, MembershipRevision: 1}
	f := &applicationDatabaseFake{t: t, db: db, apps: map[string]applicationDatabaseContainer{}, bridgeID: strings.Repeat("9", 64), returnID: true, ready: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !f.ready {
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(server.Close)
	s.HostPort = server.Listener.Addr().(*net.TCPAddr).Port
	if previous {
		old := domain.HostingHealthy{Snapshot: proof.BindingApplicationSnapshot, Release: r, OperationID: "old_app", ContainerID: strings.Repeat("8", 64), PlanHash: "sha256:" + strings.Repeat("7", 64)}
		ep := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: "old_attach", BindingHash: b.Hash, MembershipRevision: 1, Action: "attach", Target: domain.HostingDatabaseBindingEndpointTarget{ContainerID: old.ContainerID, ContainerOperationID: old.OperationID, ImageID: r.ImageID, ReleaseHash: r.Hash, ContainerPlanHash: old.PlanHash, NetworkID: origin.NetworkID, NetworkName: b.NetworkName, DaemonID: r.DaemonID}}
		ep.Hash = ep.DocumentHash()
		receipt := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "old_attached", OperationID: ep.OperationID, PlanHash: ep.Hash, BindingHash: b.Hash, MembershipRevision: 1, Phase: "attached", Observation: &domain.HostingDatabaseBindingEndpointObservation{Endpoint: domain.HostingDatabaseBindingEndpoint{Target: ep.Target, EndpointID: strings.Repeat("6", 64)}, Present: true}}
		receipt.Hash = receipt.DocumentHash()
		proof.Members = []domain.HostingDatabaseNetworkMemberProof{{Binding: b, EndpointPlan: ep, Receipt: receipt, ApplicationHealthy: old, DatabasePlan: db.plan, ApplicationProjectID: "project", DatabaseProjectID: "project"}}
	}
	s.PolicyVersion = domain.HostingApplicationDatabasePolicyVersion
	s.ApplicationDatabase = domain.HostingApplicationDatabaseRef{BindingID: b.ID, BindingHash: b.Hash}
	p, err := domain.CompileHostingApplicationDatabasePlan(s, r, nil, nil, proof)
	if err != nil {
		t.Fatal("application fixture compilation", err)
	}
	f.in = applicationDatabaseEngineInput{Plan: p, Proof: proof}
	f.e = &applicationDatabaseEngine{image: db.e, config: db.config, create: f.create}
	if previous {
		m := p.ApplicationDatabase.PreviousMembers[0]
		old := f.app(m.Endpoint.Target.ContainerID, m.Endpoint.Target.ContainerOperationID, m.Endpoint.Target.ContainerPlanHash, "old", true)
		old.Name = "/old_app"
		old.NetworkSettings.Networks[b.NetworkName] = applicationDatabaseEndpoint{NetworkID: origin.NetworkID, EndpointID: m.Endpoint.EndpointID, IPAddress: "172.28.0.4", DNSNames: []string{"old_app", old.ID[:12]}}
		f.apps[old.ID] = old
		f.addEndpoint(old.ID, m.Endpoint.EndpointID)
	}
	db.e.command = imageCommandFunc(f.command)
	metadata := hostingruntimeprotocol.Metadata{Authority: domain.HostingAuthority{OperationID: proof.OperationID, RunID: "run_" + proof.OperationID, LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: p.Hash, RuntimeConfiguration: s.RuntimeConfiguration, RequestHash: "sha256:" + strings.Repeat("f", 64), Purpose: hostingruntimeprotocol.Purpose, UseUntil: time.Now().Add(time.Minute)}
	environment, err := hostingruntimeprotocol.NewEnvironment(metadata, []hostingruntimeprotocol.Entry{{Name: b.EnvironmentNames[0], Value: []byte("private-canary-value")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(environment.Close)
	return f, environment
}

func (f *applicationDatabaseFake) app(id, operation, plan, nonce string, running bool) applicationDatabaseContainer {
	var c applicationDatabaseContainer
	c.ID, c.Image, c.Name = id, f.in.Plan.Release.ImageID, "/"+applicationDatabaseName(f.in)
	c.Config.Labels = map[string]string{imageBindingLabel: f.in.Plan.Snapshot.BindingID, imageOperationLabel: operation, imagePlanLabel: plan, imageReleaseLabel: f.in.Plan.Release.Hash, applicationDatabaseNonceLabel: nonce}
	c.State.Running = running
	c.State.Status = "created"
	if running {
		c.State.Status = "running"
	}
	c.HostConfig.NetworkMode, c.HostConfig.IpcMode, c.HostConfig.RestartPolicy.Name = "default", "private", "no"
	c.HostConfig.CgroupnsMode = "private"
	key := strconv.Itoa(f.in.Plan.Snapshot.ContainerPort) + "/tcp"
	c.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string
	}{key: {{"127.0.0.1", strconv.Itoa(f.in.Plan.Snapshot.HostPort)}}}
	c.NetworkSettings.Networks = map[string]applicationDatabaseEndpoint{"bridge": {}}
	if running {
		c.NetworkSettings.Networks["bridge"] = applicationDatabaseEndpoint{NetworkID: f.bridgeID, EndpointID: strings.Repeat("7", 64), IPAddress: "172.17.0.4"}
	}
	return c
}

func (f *applicationDatabaseFake) before(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.authorized = true
	return nil
}
func (f *applicationDatabaseFake) mutation(name string) {
	f.t.Helper()
	if !f.authorized {
		f.t.Fatal("effect without fresh authority", name)
	}
	f.authorized = false
	f.effects = append(f.effects, name)
}
func (f *applicationDatabaseFake) addEndpoint(id, endpoint string) {
	f.db.network["Containers"].(map[string]any)[id] = map[string]any{"EndpointID": endpoint, "IPv4Address": "172.28.0.4/16", "IPv6Address": ""}
}
func (f *applicationDatabaseFake) create(ctx context.Context, _ string, flags []string, _ string, values map[string][]byte, before func(context.Context) error) (string, error) {
	if before == nil || before(ctx) != nil || ctx.Err() != nil {
		return "", errApplicationDatabaseEngine
	}
	f.mutation("create")
	if strings.Contains(strings.Join(flags, " "), "private-canary") || len(values) != 1 {
		f.t.Fatal("credential transport escaped borrowed values")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
		f.t.Fatal("create lost original deadline")
	}
	id := strings.Repeat("5", 64)
	f.apps[id] = f.app(id, f.in.Proof.OperationID, f.in.Plan.Hash, f.in.Proof.CreateNonce, false)
	if !f.returnID {
		return "", errors.New("private-canary-value")
	}
	return id, f.createErr
}
func (f *applicationDatabaseFake) command(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	a := args[4:]
	if strings.Contains(strings.Join(args, " "), "private-canary") {
		f.t.Fatal("secret in Docker argv")
	}
	if a[0] == "network" && a[1] == "inspect" && a[2] == "bridge" {
		return json.Marshal(map[string]any{"Id": f.bridgeID, "Name": "bridge", "Driver": "bridge", "Scope": "local"})
	}
	if a[0] == "image" && a[1] == "inspect" && a[len(a)-1] != "{{json .Config.Env}}" && a[2] == hostingReleaseImage(f.in.Plan.Release) {
		r := f.in.Plan.Release
		return json.Marshal(map[string]any{"Id": r.ImageID, "Os": r.OS, "Architecture": r.Architecture, "Variant": r.Variant})
	}
	if a[0] == "container" {
		if a[1] == "ls" {
			filter := a[len(a)-1]
			for id, c := range f.apps {
				if filter == "id="+id || filter == "name=^"+c.Name+"$" {
					return []byte(id), nil
				}
			}
			if strings.HasPrefix(filter, "name=") {
				return nil, nil
			}
		}
		if c, ok := f.apps[a[2]]; ok {
			switch a[1] {
			case "inspect":
				return json.Marshal(c)
			case "start":
				// Captured Docker29 probe: stopped connect only configures;
				// actual endpoints appear at start and are cleared by stop.
				f.mutation("start")
				c.State.Running = true
				c.State.Status = "running"
				c.NetworkSettings.Networks["bridge"] = applicationDatabaseEndpoint{NetworkID: f.bridgeID, EndpointID: strings.Repeat("7", 64), IPAddress: "172.17.0.4"}
				n := c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName]
				n.NetworkID, n.EndpointID, n.IPAddress = f.in.Proof.Origin.NetworkID, strings.Repeat("4", 64), "172.28.0.4"
				c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName] = n
				f.addEndpoint(c.ID, n.EndpointID)
				f.apps[c.ID] = c
				return nil, f.startErr
			case "stop":
				// stop has --time before the exact ID, handled below.
			case "rm":
				f.mutation("rm")
				if f.removeErr != nil {
					return nil, f.removeErr
				}
				delete(f.apps, c.ID)
				return nil, nil
			}
		}
		if a[1] == "stop" {
			id := a[len(a)-1]
			c := f.apps[id]
			f.mutation("stop")
			c.State.Running = false
			c.State.Status = "exited"
			for name, n := range c.NetworkSettings.Networks {
				n.EndpointID, n.IPAddress = "", ""
				c.NetworkSettings.Networks[name] = n
			}
			delete(f.db.network["Containers"].(map[string]any), id)
			f.apps[id] = c
			return nil, nil
		}
	}
	if a[0] == "network" && (a[1] == "connect" || a[1] == "disconnect") {
		id := a[3]
		c := f.apps[id]
		f.mutation(a[1])
		if a[1] == "connect" {
			c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName] = applicationDatabaseEndpoint{DNSNames: []string{strings.TrimPrefix(c.Name, "/"), id[:12]}}
		} else {
			delete(c.NetworkSettings.Networks, f.in.Proof.Binding.NetworkName)
			delete(f.db.network["Containers"].(map[string]any), id)
		}
		f.apps[id] = c
		return nil, nil
	}
	return f.db.command(ctx, name, args, dir, env)
}

func applicationDatabaseAttachPlan(f *applicationDatabaseFake, c applicationDatabaseCandidate) domain.HostingDatabaseBindingEndpointPlan {
	p := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: f.in.Proof.OperationID, BindingHash: f.in.Proof.Binding.Hash, MembershipRevision: f.in.Proof.MembershipRevision + 1, Action: "attach", Target: domain.HostingDatabaseBindingEndpointTarget{ContainerID: c.ContainerID, ContainerOperationID: c.OperationID, ImageID: c.ImageID, ReleaseHash: f.in.Plan.Release.Hash, ContainerPlanHash: c.PlanHash, NetworkID: f.in.Proof.Origin.NetworkID, NetworkName: f.in.Proof.Binding.NetworkName, DaemonID: c.DaemonID}}
	p.Hash = p.DocumentHash()
	return p
}

func TestApplicationDatabaseEngineStoppedConfigurationObservedAfterStart(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, engine, false)
			c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
			if err != nil || f.apps[c.ContainerID].State.Running || len(f.effects) != 1 {
				t.Fatal("stopped create", err)
			}
			if err := f.e.InspectCandidate(context.Background(), f.in, c); err != nil {
				t.Fatal(err)
			}
			if c.DefaultNetworkID == "" || f.apps[c.ContainerID].NetworkSettings.Networks["bridge"].NetworkID != "" {
				t.Fatal("stopped create confused configuration with observed bridge identity")
			}
			plan := applicationDatabaseAttachPlan(f, c)
			if _, err := f.e.StartAndObserve(context.Background(), f.in, c, plan, applicationDatabaseConfiguration{}, f.before); err == nil || len(f.effects) != 1 {
				t.Fatal("started before attachment")
			}
			configured, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, plan, f.before)
			if err != nil || configured.Hash == "" || f.apps[c.ContainerID].NetworkSettings.Networks[f.in.Proof.Binding.NetworkName].EndpointID != "" || f.apps[c.ContainerID].State.Running {
				t.Fatal("configuration must not fabricate an endpoint", err)
			}
			if _, present := f.db.network["Containers"].(map[string]any)[c.ContainerID]; present {
				t.Fatal("stopped configuration invented network user")
			}
			if _, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, plan, f.before); err == nil || len(f.effects) != 2 {
				t.Fatal("repeated configuration mutated again")
			}
			observation, err := f.e.StartAndObserve(context.Background(), f.in, c, plan, configured, f.before)
			if err != nil || !observation.Present || observation.Endpoint.EndpointID == "" {
				t.Fatal("running endpoint missing", err)
			}
			if _, err := f.e.StartAndObserve(context.Background(), f.in, c, plan, configured, f.before); err == nil || len(f.effects) != 3 {
				t.Fatal("repeated start mutated again")
			}
			if err := f.e.VerifyReady(context.Background(), f.in, c, observation); err != nil {
				t.Fatal("readiness", err)
			}
			if err := f.e.InspectCandidate(context.Background(), f.in, c); err != nil {
				t.Fatal("running candidate observation", err)
			}
			if strings.Join(f.effects, ",") != "create,connect,start" {
				t.Fatal("wrong effects", f.effects)
			}
		})
	}
}

func TestApplicationDatabaseEngineCreateUncertaintyAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing_id", "id_and_error", "inspection_failure", "expired", "canceled", "authority_cancels"} {
		t.Run(mode, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, "postgresql", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Now().Add(time.Minute)
			before := f.before
			switch mode {
			case "missing_id":
				f.returnID = false
			case "id_and_error":
				f.createErr = errors.New("private-canary-value")
			case "inspection_failure":
				original := f.e.create
				f.e.create = func(ctx context.Context, d string, a []string, i string, v map[string][]byte, before func(context.Context) error) (string, error) {
					id, err := original(ctx, d, a, i, v, before)
					c := f.apps[id]
					c.Config.Labels[applicationDatabaseNonceLabel] = "wrong"
					f.apps[id] = c
					return id, err
				}
			case "expired":
				deadline = time.Now().Add(-time.Second)
			case "canceled":
				cancel()
			case "authority_cancels":
				before = func(context.Context) error { cancel(); return nil }
			}
			c, err := f.e.CreateStopped(ctx, f.in, env, deadline, before)
			if err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe error", err)
			}
			if mode == "missing_id" || mode == "id_and_error" || mode == "inspection_failure" {
				if !errors.Is(err, errApplicationDatabaseCreateUncertain) || len(f.effects) != 1 {
					t.Fatal("lost uncertainty", err)
				}
				if mode != "missing_id" && c.ContainerID == "" {
					t.Fatal("lost returned ID")
				}
			} else if len(f.effects) != 0 {
				t.Fatal("effect after expired/canceled authority")
			}
		})
	}
}
