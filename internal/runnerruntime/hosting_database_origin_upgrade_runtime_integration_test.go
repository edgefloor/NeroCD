//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
)

const databaseOriginUpgradeOwner = "nerocd-database-origin-upgrade-20261024-b084"

// The helper is executed only in a temporary, separately networked client. Its
// credential input travels over stdin; neither Docker configuration nor argv
// contains the value. Errors deliberately exclude driver/server diagnostics.
type databaseOriginClientInput struct {
	Engine, Host, Address, Stage string
	Password                     []byte
	Expected                     map[string][]byte
}

func TestHostingDatabaseOriginUpgradeClient(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_ORIGIN_CLIENT") != databaseOriginUpgradeOwner {
		t.Skip("private client subprocess only")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 8193))
	defer clear(raw)
	var in databaseOriginClientInput
	defer func() { clear(in.Password) }()
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &in) != nil || len(in.Password) != 64 || len(in.Expected) < 1 || len(in.Expected) > 3 {
		t.Fatal("private client input refused")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	address, err := netip.ParseAddr(in.Address)
	if err != nil || !address.Is4() || !address.IsPrivate() {
		t.Fatal("private client endpoint refused")
	}
	addresses, err := net.DefaultResolver.LookupHost(ctx, in.Host)
	if err != nil || len(addresses) != 1 || addresses[0] != in.Address {
		t.Fatal("deterministic database DNS did not resolve exact endpoint")
	}
	if in.Engine == "postgresql" {
		config, err := pgx.ParseConfig("host=127.0.0.1 port=5432 dbname=app user=ncd_app sslmode=disable")
		if err != nil {
			t.Fatal("private PostgreSQL client configuration failed")
		}
		config.Host, config.Password = in.Host, string(in.Password)
		config.TLSConfig, config.Fallbacks = nil, nil
		config.ConnectTimeout = 2 * time.Second
		config.RuntimeParams = map[string]string{"application_name": "nerocd-origin-upgrade-client", "search_path": "pg_catalog", "statement_timeout": "2000"}
		conn, err := pgx.ConnectConfig(ctx, config)
		config.Password = ""
		if err != nil {
			t.Fatal("private PostgreSQL app authentication failed")
		}
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if conn.Close(closeCtx) != nil {
				t.Error("private PostgreSQL client close failed")
			}
		}()
		var user string
		var superuser bool
		if conn.QueryRow(ctx, "SELECT current_user, rolsuper FROM pg_catalog.pg_roles WHERE rolname=current_user").Scan(&user, &superuser) != nil || user != "ncd_app" || superuser {
			t.Fatal("private PostgreSQL app role mismatch")
		}
		for key, want := range in.Expected {
			if key == in.Stage {
				continue
			}
			var got []byte
			if conn.QueryRow(ctx, "SELECT payload FROM public.runtime_rows WHERE stage=$1", key).Scan(&got) != nil || !bytes.Equal(got, want) {
				t.Fatal("private PostgreSQL predecessor data mismatch")
			}
		}
		if in.Stage != "" {
			if _, err := conn.Exec(ctx, "INSERT INTO public.runtime_rows(stage,payload) VALUES($1,$2)", in.Stage, in.Expected[in.Stage]); err != nil {
				t.Fatal("private PostgreSQL write failed")
			}
		}
		for key, want := range in.Expected {
			var got []byte
			if conn.QueryRow(ctx, "SELECT payload FROM public.runtime_rows WHERE stage=$1", key).Scan(&got) != nil || !bytes.Equal(got, want) {
				t.Fatal("private PostgreSQL retained data mismatch")
			}
		}
	} else if in.Engine == "redis" {
		for key, want := range in.Expected {
			if key == in.Stage {
				continue
			}
			reply, err := databaseOriginClientRESP(ctx, in.Host, in.Password, [][]string{{"GET", "fixture:db7319:" + key}})
			if err != nil || len(reply) != 1 || string(reply[0]) != hex.EncodeToString(want) {
				t.Fatal("private Redis predecessor data mismatch")
			}
		}
		if in.Stage != "" {
			reply, err := databaseOriginClientRESP(ctx, in.Host, in.Password, [][]string{{"SET", "fixture:db7319:" + in.Stage, hex.EncodeToString(in.Expected[in.Stage])}})
			if err != nil || len(reply) != 1 || string(reply[0]) != "OK" {
				t.Fatal("private Redis write failed")
			}
		}
		for key, want := range in.Expected {
			reply, err := databaseOriginClientRESP(ctx, in.Host, in.Password, [][]string{{"GET", "fixture:db7319:" + key}})
			if err != nil || len(reply) != 1 || string(reply[0]) != hex.EncodeToString(want) {
				t.Fatal("private Redis retained data mismatch")
			}
		}
	} else {
		t.Fatal("private database engine refused")
	}
	t.Log("private DNS and authenticated app-role data verified")
}

func databaseOriginClientRESP(ctx context.Context, host string, password []byte, commands [][]string) ([][]byte, error) {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, "6379"))
	if err != nil {
		return nil, errors.New("private Redis DNS connection failed")
	}
	return databaseRESPExchange(ctx, conn, "ncd_app", password, commands)
}

// Exercise the private client's failure output locally, without a daemon or DB.
func TestDatabaseOriginUpgradeClientFailureRedaction(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := strings.Repeat("s", 64)
	for _, input := range []string{`{"Password":"` + marker, strings.Repeat("x", 8193) + marker, `{"Password":"` + marker + `","Expected":{"a":"YQ=="}}`} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, executable, "-test.run", "^TestHostingDatabaseOriginUpgradeClient$")
		cmd.Env = append(os.Environ(), "NEROCD_DATABASE_ORIGIN_CLIENT="+databaseOriginUpgradeOwner)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil || bytes.Contains(out, []byte(marker)) || !bytes.Contains(out, []byte("private client input refused")) {
			t.Fatal("private client failure output escaped redaction")
		}
	}
}

func databaseOriginHealthy(f *databaseRuntimeFixture, operation string, p domain.HostingApplyPlan, result domain.HostingResult) domain.HostingHealthy {
	return domain.HostingHealthy{OperationID: operation, ContainerID: result.ContainerID, Snapshot: p.Snapshot, Release: p.Release, PlanHash: p.Hash, Storage: &domain.HostingHealthyStorage{Plan: *p.Storage, CreationPlanHash: f.request.OriginPlanHash}}
}

func databaseOriginCapture(f *databaseRuntimeFixture, operation string, result domain.HostingResult) domain.HostingDatabaseNetworkCapturePlan {
	f.t.Helper()
	h := databaseOriginHealthy(f, operation, f.plan, result)
	// This receipt is a fixture-local materialization acknowledgement derived from
	// real Docker evidence. It does not attest controller/store/API provenance.
	receipt := domain.HostingReceipt{ID: "fixture_materialized_origin_upgrade", BodyHash: domain.HostingDocumentHash(f.evidence), CreatedAt: time.Now().UTC()}
	m := domain.HostingStorageMaterialization{StorageRef: f.evidence.Storage, State: "materialized", CreationOperationID: f.evidence.OriginOperationID, CreationPlanHash: f.evidence.OriginPlanHash, CreationNonce: f.evidence.CreationNonce, DaemonID: f.evidence.DaemonID, Labels: maps.Clone(f.evidence.Labels), MaterializedReceipt: &receipt}
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(operation), SourceOperationID: operation, ProjectID: f.request.Storage.ProjectID, ResourceID: f.plan.Snapshot.ResourceID, BindingID: f.plan.Snapshot.BindingID, DestinationID: f.plan.Snapshot.DestinationID, ServerID: f.plan.Snapshot.ServerID, RunnerID: f.plan.Snapshot.RunnerID, SourcePlan: f.plan, SourceHealthy: h, Storage: f.request.Storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: f.evidence.VolumeName, Driver: f.evidence.Driver, Scope: f.evidence.Scope, Labels: maps.Clone(f.evidence.Labels), Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil {
		f.t.Fatal("physical capture context refused")
	}
	return p
}

func databaseOriginCredentials(f *databaseRuntimeFixture, in databaseNetworkUpgradeInput) *DatabaseCredentials {
	f.t.Helper()
	var values []DatabaseCredentialValue
	for _, ref := range in.Plan.Database.Credentials {
		v, err := NewDatabaseCredentialValue(ref, f.passwords[ref.Role])
		if err != nil {
			f.t.Fatal("physical upgrade value refused")
		}
		values = append(values, v)
	}
	defer func() {
		for i := range values {
			values[i].Close()
		}
	}()
	credentials, err := newDatabaseNetworkUpgradeCredentials(in, values, time.Now().Add(10*time.Minute))
	if err != nil {
		f.t.Fatal("physical upgrade credentials refused")
	}
	f.t.Cleanup(credentials.Close)
	return credentials
}

// Only the exact existing-volume presence query may accompany observation. A
// broader volume subcommand allowance could conceal a storage effect.
func databaseOriginReadOnlyAllowed(args []string, volume string) bool {
	if len(args) < 2 {
		return false
	}
	if slices.Equal(args, []string{"volume", "ls", "--quiet", "--filter", "name=" + volume}) && volume != "" {
		return true
	}
	return args[0] == "info" || args[1] == "inspect" && slices.Contains([]string{"image", "container", "network", "volume"}, args[0]) || args[0] == "container" && slices.Contains([]string{"ls", "top"}, args[1])
}

// Diagnostics contain only fixed action tags; raw arguments may include private
// paths, and arbitrary command errors must never enter fixture output.
func databaseOriginCommandStage(args []string) string {
	if len(args) == 0 {
		return "unclassified"
	}
	if args[0] == "info" {
		return "daemon_info"
	}
	if len(args) < 2 {
		return "unclassified"
	}
	switch args[0] + " " + args[1] {
	case "container ls":
		return "container_list"
	case "container inspect":
		return "container_inspect"
	case "container top":
		return "container_top"
	case "container create":
		return "container_create"
	case "container start":
		return "container_start"
	case "container stop":
		return "container_stop"
	case "container rm":
		return "container_remove"
	case "container exec":
		return "container_exec"
	case "volume ls":
		return "volume_list"
	case "volume inspect":
		return "volume_inspect"
	case "network inspect":
		return "network_inspect"
	case "image inspect":
		return "image_inspect"
	default:
		return "unclassified"
	}
}

type databaseOriginCandidateShape struct {
	DNSNameAtZero, DNSAliasAtOne, DNSShortIDAtTwo       bool
	DNSNamePresent, DNSAliasPresent, DNSShortIDPresent  bool
	Inspected, BaseMatches, QualifiedMatches            bool
	Running, PIDPresent                                 bool
	State                                               string
	AliasMatches, DNSMatches, DNSNil                    bool
	AliasCount, DNSCount                                int
	NetworkIDPresent, EndpointIDPresent, AddressPresent bool
	Links, IPAMEntries, DriverOptions, GatewayPriority  int
}

func databaseOriginCandidateSummary(container databaseContainer, operation string, in databaseNetworkUpgradeInput, record databaseNetworkRecord, config DatabaseEngineConfig) databaseOriginCandidateShape {
	n := container.NetworkSettings.Networks[in.Plan.Database.NetworkName]
	dns := []string{strings.TrimPrefix(container.Name, "/"), in.Plan.Database.Network.Alias}
	if len(container.ID) >= 12 {
		dns = append(dns, container.ID[:12])
	}
	state := "unclassified"
	switch container.State.Status {
	case "created", "running", "exited", "dead", "paused", "restarting":
		state = container.State.Status
	}
	secret := filepath.Join(config.secretRoot, databaseGeneration(operation, in.Plan))
	inspection := &databaseUpgradeInspection{Previous: in.PreviousPlan, Origin: in.Proof.Origin}
	short := ""
	if len(container.ID) >= 12 {
		short = container.ID[:12]
	}
	name := strings.TrimPrefix(container.Name, "/")
	return databaseOriginCandidateShape{DNSNameAtZero: len(n.DNSNames) > 0 && n.DNSNames[0] == name, DNSAliasAtOne: len(n.DNSNames) > 1 && n.DNSNames[1] == in.Plan.Database.Network.Alias, DNSShortIDAtTwo: len(n.DNSNames) > 2 && n.DNSNames[2] == short, DNSNamePresent: slices.Contains(n.DNSNames, name), DNSAliasPresent: slices.Contains(n.DNSNames, in.Plan.Database.Network.Alias), DNSShortIDPresent: short != "" && slices.Contains(n.DNSNames, short), Inspected: true, BaseMatches: databaseContainerMatches(container, operation, in.Plan, record, secret), QualifiedMatches: databaseContainerMatchesWithInspection(container, operation, in.Plan, record, secret, inspection), Running: container.State.Running, PIDPresent: container.State.Pid > 0, State: state, AliasMatches: slices.Equal(n.Aliases, []string{in.Plan.Database.Network.Alias}), DNSMatches: slices.Equal(n.DNSNames, dns), DNSNil: n.DNSNames == nil, AliasCount: len(n.Aliases), DNSCount: len(n.DNSNames), NetworkIDPresent: n.NetworkID != "", EndpointIDPresent: n.EndpointID != "", AddressPresent: n.IPAddress != "", Links: len(n.Links), IPAMEntries: len(n.IPAMConfig), DriverOptions: len(n.DriverOpts), GatewayPriority: n.GwPriority}
}

func databaseOriginApplyWithEvidence(f *databaseRuntimeFixture, operation string, in databaseNetworkUpgradeInput, credentials *DatabaseCredentials) domain.HostingResult {
	f.t.Helper()
	base := f.engine.command
	var sequence []string
	dropped := 0
	f.engine.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		raw, err := base.RunEnvironment(ctx, name, args, dir, env)
		stage := "unclassified"
		if len(args) >= 4 {
			stage = databaseOriginCommandStage(args[4:])
		}
		if err != nil {
			stage += ":error"
		} else {
			stage += ":ok"
		}
		if len(sequence) == 256 {
			sequence = sequence[1:]
			dropped++
		}
		sequence = append(sequence, stage)
		return raw, err
	})
	defer func() { f.engine.command = base }()
	result := f.engine.ApplyDatabaseNetworkUpgrade(f.t.Context(), operation, in, f.request, f.evidence, credentials, f.config, databaseRuntimeAuthority)
	if !result.Ready || result.Uncertain || result.Outcome != "succeeded" {
		shape := databaseOriginCandidateShape{State: "unavailable"}
		if dockerObjectID.MatchString(result.ContainerID) {
			container, err := f.engine.inspectDatabaseContainer(f.t.Context(), result.ContainerID)
			if err == nil {
				shape = databaseOriginCandidateSummary(container, operation, in, databaseNetworkRecord(in.Proof.Origin), f.config)
			}
		}
		raw, err := json.Marshal(shape)
		if err != nil {
			f.t.Fatal("closed candidate diagnostics unavailable")
		}
		f.t.Logf("failed_upgrade_candidate_shape=%s closed_command_sequence=%s dropped_stages=%d", raw, strings.Join(sequence, ","), dropped)
	}
	return result
}

func TestDatabaseOriginUpgradeDiagnosticBoundaries(t *testing.T) {
	volume := "ncdv_fixture"
	exact := []string{"volume", "ls", "--quiet", "--filter", "name=" + volume}
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{"exact_existing_volume", exact, true},
		{"other_volume", []string{"volume", "ls", "--quiet", "--filter", "name=foreign"}, false},
		{"extra_argument", append(slices.Clone(exact), "--all"), false},
		{"changed_flag", []string{"volume", "ls", "--format", "--filter", "name=" + volume}, false},
		{"remove", []string{"volume", "rm", volume}, false},
		{"create", []string{"volume", "create", volume}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if databaseOriginReadOnlyAllowed(test.args, volume) != test.want {
				t.Fatal("read-only volume classification mismatch")
			}
		})
	}
	f, in, credentials := upgradeFixture(t, "redis")
	credentials.Close()
	marker := "SENSITIVE-DIAGNOSTIC-MARKER"
	container := databaseContainer{}
	container.ID, container.Name, container.State.Status = marker, marker, marker
	container.NetworkSettings.Networks = map[string]databaseUpgradeEndpoint{in.Plan.Database.NetworkName: {Aliases: []string{marker}, DNSNames: []string{marker}, IPAddress: marker, NetworkID: marker, EndpointID: marker}}
	raw, err := json.Marshal(databaseOriginCandidateSummary(container, marker, in, databaseNetworkRecord(in.Proof.Origin), f.config))
	if err != nil || bytes.Contains(raw, []byte(marker)) || databaseOriginCommandStage([]string{marker, marker}) != "unclassified" {
		t.Fatal("candidate diagnostic leaked unclassified content")
	}
	if databaseOriginCommandStage([]string{"container", "create", marker}) != "container_create" {
		t.Fatal("candidate diagnostic classification mismatch")
	}
	container.ID = strings.Repeat("a", 64)
	container.Name = "/expected_name"
	endpoint := container.NetworkSettings.Networks[in.Plan.Database.NetworkName]
	endpoint.Aliases = []string{in.Plan.Database.Network.Alias}
	endpoint.DNSNames = []string{"expected_name", in.Plan.Database.Network.Alias, container.ID[:12]}
	container.NetworkSettings.Networks[in.Plan.Database.NetworkName] = endpoint
	got := databaseOriginCandidateSummary(container, marker, in, databaseNetworkRecord(in.Proof.Origin), f.config)
	if !got.DNSMatches || !got.DNSNameAtZero || !got.DNSAliasAtOne || !got.DNSShortIDAtTwo || !got.DNSNamePresent || !got.DNSAliasPresent || !got.DNSShortIDPresent || !got.AliasMatches || got.DNSNil || got.DNSCount != 3 {
		t.Fatal("candidate DNS diagnostic identity flags mismatch")
	}
	endpoint.DNSNames = nil
	container.NetworkSettings.Networks[in.Plan.Database.NetworkName] = endpoint
	got = databaseOriginCandidateSummary(container, marker, in, databaseNetworkRecord(in.Proof.Origin), f.config)
	if !got.DNSNil || got.DNSMatches || got.DNSNamePresent || got.DNSAliasPresent || got.DNSShortIDPresent || got.DNSCount != 0 {
		t.Fatal("candidate missing DNS diagnostic flags mismatch")
	}
}

// Every successful observation must remain within an explicit read-only command
// set, and leave retained state, credentials and actual writer identity unchanged.
func databaseOriginReadOnly(t *testing.T, f *databaseRuntimeFixture, run func()) {
	t.Helper()
	state, secrets := observerTree(t, f.config.stateRoot), observerTree(t, f.config.secretRoot)
	base := f.engine.command
	f.engine.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if len(args) < 6 {
			return nil, errors.New("read-only fixture command refused")
		}
		a := args[4:]
		allowed := databaseOriginReadOnlyAllowed(a, f.request.Storage.VolumeName)
		if !allowed {
			t.Errorf("observation command refused: stage=%s", databaseOriginCommandStage(a))
			return nil, errors.New("read-only fixture effect refused")
		}
		return base.RunEnvironment(ctx, name, args, dir, env)
	})
	defer func() { f.engine.command = base }()
	run()
	if !maps.Equal(state, observerTree(t, f.config.stateRoot)) || !maps.Equal(secrets, observerTree(t, f.config.secretRoot)) {
		t.Fatal("read-only physical observation changed protected state")
	}
}

func databaseOriginTemporaryClient(f *databaseRuntimeFixture, plan domain.HostingApplyPlan, run func(string)) {
	f.t.Helper()
	name := "ncd-origin-client-" + f.recipe.Engine
	id := strings.TrimSpace(string(databaseRuntimeDocker(f.t, f.engine, "container", "create", "--name", name, "--label", "io.nerocd.origin-upgrade.client="+databaseOriginUpgradeOwner, "--network", plan.Database.NetworkName, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "sleep", plan.Release.ImageID, "300")))
	if !dockerObjectID.MatchString(id) {
		f.t.Fatal("private client create identity unavailable")
	}
	removed := false
	remove := func() {
		if !removed {
			databaseRuntimeDocker(f.t, f.engine, "container", "rm", "--force", id)
			removed = true
		}
	}
	f.t.Cleanup(remove)
	databaseRuntimeDocker(f.t, f.engine, "container", "start", id)
	executable, err := os.Executable()
	if err != nil {
		f.t.Fatal("private client executable unavailable")
	}
	databaseRuntimeDocker(f.t, f.engine, "cp", executable, id+":/tmp/origin-client.test")
	run(id)
	remove()
	if strings.TrimSpace(string(databaseRuntimeDocker(f.t, f.engine, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id))) != "" {
		f.t.Fatal("private client remained attached")
	}
}

func databaseOriginDNSData(f *databaseRuntimeFixture, plan domain.HostingApplyPlan, result domain.HostingResult, stage string, expected map[string][]byte) {
	f.t.Helper()
	container, err := f.engine.inspectDatabaseContainer(f.t.Context(), result.ContainerID)
	if err != nil {
		f.t.Fatal("private DNS writer unavailable")
	}
	in := databaseOriginClientInput{Engine: f.recipe.Engine, Host: plan.Database.Network.Alias, Address: container.NetworkSettings.Networks[plan.Database.NetworkName].IPAddress, Stage: stage, Password: bytes.Clone(f.passwords["app"]), Expected: expected}
	defer clear(in.Password)
	raw, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal("private client delivery failed")
	}
	defer clear(raw)
	databaseOriginTemporaryClient(f, plan, func(id string) {
		ctx, cancel := context.WithTimeout(f.t.Context(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "--host", f.engine.endpoint, "--config", f.engine.configDir, "exec", "--interactive", "--env", "NEROCD_DATABASE_ORIGIN_CLIENT="+databaseOriginUpgradeOwner, id, "/tmp/origin-client.test", "-test.run", "^TestHostingDatabaseOriginUpgradeClient$", "-test.timeout=20s")
		cmd.Dir = f.engine.configDir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.engine.configDir, "DOCKER_CONFIG=" + f.engine.configDir, "DOCKER_HOST=" + f.engine.endpoint, "DOCKER_CONTEXT=", "LANG=C", "LC_ALL=C"}
		configureProvenanceProcess(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdin = bytes.NewReader(raw)
		// Discard diagnostics: failed authentication/decode must never expose input.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if ctx.Err() != nil {
			killProvenanceProcess(cmd)
		}
		if err != nil {
			f.t.Fatal("separate private DNS application client failed")
		}
	})
	var dataset bytes.Buffer
	for _, key := range []string{"a", "b", "c"} {
		dataset.Write(expected[key])
	}
	f.t.Logf("separate private DNS app-role verified engine=%s stage=%s records=%d dataset_sha256=%x alias=%s", f.recipe.Engine, stage, len(expected), sha256.Sum256(dataset.Bytes()), in.Host)
}

func databaseOriginUpgradePhysical(t *testing.T, root, kind string) {
	f := newDatabaseRuntimeFixture(t, root, kind, "origin_upgrade", true)
	opA := f.request.OriginOperationID
	a := f.apply(f.engine, opA, f.plan, false, databaseRuntimeAuthority)
	record := f.live(opA, f.plan, a, "")
	original, err := os.ReadFile(filepath.Join(f.config.stateRoot, f.plan.Database.NetworkName+".json"))
	if err != nil {
		t.Fatal("retained origin bytes unavailable")
	}
	expected := map[string][]byte{"a": []byte("retained-before-private-hostname-" + kind)}
	f.data(f.plan, a, "a", expected)
	capture := databaseOriginCapture(f, opA, a)
	t.Logf("fixture_local_materialization engine=%s receipt_id=%s body_hash=%s controller_provenance=false capture_hash=%s", kind, capture.Creation.Receipt.ID, capture.Creation.Receipt.BodyHash, capture.Hash)
	observer, err := newDatabaseNetworkOriginObserver(f.engine, f.config)
	if err != nil {
		t.Fatal("physical origin observer unavailable")
	}
	var origin domain.HostingDatabaseNetworkOrigin
	databaseOriginReadOnly(t, f, func() {
		var err error
		origin, err = observer.ObserveDatabaseNetworkOrigin(t.Context(), capture, databaseRuntimeAuthority)
		if err != nil || origin != domain.HostingDatabaseNetworkOrigin(record) {
			t.Fatal("physical read-only origin capture failed")
		}
	})
	in := upgradeNext(t, f.plan, databaseOriginHealthy(f, opA, f.plan, a), origin)
	credentials := databaseOriginCredentials(f, in)
	opB := "hop_origin_upgrade_" + kind + "_b"
	source, err := f.engine.inspectDatabaseContainer(t.Context(), a.ContainerID)
	if err != nil {
		t.Fatal("source identity unavailable before denials")
	}
	sourceHash := domain.HostingDocumentHash(source)
	assertSource := func() {
		t.Helper()
		current, err := f.engine.inspectDatabaseContainer(t.Context(), a.ContainerID)
		if err != nil || !current.State.Running || domain.HostingDocumentHash(current) != sourceHash {
			t.Fatal("denial changed source writer")
		}
		raw, err := os.ReadFile(filepath.Join(f.config.stateRoot, f.plan.Database.NetworkName+".json"))
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatal("denial changed original origin")
		}
	}
	// A live foreign attachment must be rejected, including by the writer upgrade.
	databaseOriginTemporaryClient(f, f.plan, func(_ string) {
		databaseOriginReadOnly(t, f, func() {
			if got, err := observer.ObserveDatabaseNetworkOrigin(t.Context(), capture, databaseRuntimeAuthority); err == nil || got != (domain.HostingDatabaseNetworkOrigin{}) {
				t.Fatal("foreign client origin capture accepted")
			}
		})
		var denied domain.HostingResult
		databaseOriginReadOnly(t, f, func() {
			denied = f.engine.ApplyDatabaseNetworkUpgrade(t.Context(), opB, in, f.request, f.evidence, credentials, f.config, databaseRuntimeAuthority)
		})
		if denied.Ready || !denied.Uncertain || denied.ContainerID != "" {
			t.Fatal("foreign client upgrade accepted")
		}
		assertSource()
	})
	// Missing and changed retained origins must not enter a reconstruction path.
	path := filepath.Join(f.config.stateRoot, f.plan.Database.NetworkName+".json")
	for _, negative := range []string{"missing", "changed", "expired_authority", "expired_credentials"} {
		if negative == "missing" {
			if os.Rename(path, path+".saved") != nil {
				t.Fatal("origin absence injection failed")
			}
		}
		if negative == "changed" {
			changed := record
			changed.Nonce = strings.Repeat("f", 32)
			if changed.Nonce == record.Nonce {
				changed.Nonce = strings.Repeat("e", 32)
			}
			if f.config.writeRecord(t.Context(), filepath.Base(path), changed, databaseRuntimeAuthority) != nil {
				t.Fatal("origin change injection failed")
			}
		}
		authority := databaseRuntimeAuthority
		if negative == "expired_authority" {
			authority = func(context.Context) error { return context.DeadlineExceeded }
		}
		useCredentials := credentials
		if negative == "expired_credentials" {
			useCredentials = databaseOriginCredentials(f, in)
			useCredentials.until = time.Now().Add(-time.Second)
		}
		var denied domain.HostingResult
		databaseOriginReadOnly(t, f, func() {
			denied = f.engine.ApplyDatabaseNetworkUpgrade(t.Context(), opB, in, f.request, f.evidence, useCredentials, f.config, authority)
		})
		if negative == "missing" {
			if os.Rename(path+".saved", path) != nil {
				t.Fatal("origin absence restoration failed")
			}
		}
		if negative == "changed" {
			if os.WriteFile(path, original, 0600) != nil {
				t.Fatal("origin restoration failed")
			}
		}
		if denied.Ready || !denied.Uncertain || denied.ContainerID != "" {
			t.Fatal("unsafe physical upgrade accepted")
		}
		assertSource()
		t.Logf("physical upgrade denial engine=%s case=%s source_unchanged=true", kind, negative)
	}
	b := databaseOriginApplyWithEvidence(f, opB, in, credentials)
	if got := f.live(opB, in.Plan, b, a.ContainerID); got != record {
		t.Fatal("upgrade changed retained physical origin")
	}
	expected["b"] = []byte("retained-after-private-hostname-" + kind)
	databaseOriginDNSData(f, in.Plan, b, "b", expected)
	if f.engine.Close() != nil {
		t.Fatal("upgrade engine close failed")
	}
	f.engine = databaseRuntimeEngine(t, defaultImageDockerEndpoint)
	credentials = databaseOriginCredentials(f, in)
	observe := func(want bool) {
		databaseOriginReadOnly(t, f, func() {
			got, err := f.engine.ObserveDatabaseNetworkUpgrade(t.Context(), domain.HostingOperation{ID: opB, Plan: &in.Plan, Result: &b}, in, credentials, f.config)
			if want && (err != nil || !got.Ready || got.ContainerID != b.ContainerID) || !want && err == nil {
				t.Fatal("physical upgrade observation mismatch")
			}
		})
	}
	observe(true)
	container, err := f.engine.inspectDatabaseContainer(t.Context(), b.ContainerID)
	if err != nil || container.State.Pid < 1 {
		t.Fatal("actual upgraded writer host PID unavailable")
	}
	top := databaseRuntimeDocker(t, f.engine, "container", "top", b.ContainerID, "-eo", "pid,uid")
	if !databaseUpgradeTopMatches(top, container.State.Pid) {
		t.Fatal("actual Docker top PID/UID profile mismatch")
	}
	t.Logf("actual upgraded writer process engine=%s host_pid=%d uid=999 top_sha256=%x", kind, container.State.Pid, sha256.Sum256(top))
	// Wrong actual alias is refused. Restore the exact configured alias before C.
	databaseRuntimeDocker(t, f.engine, "network", "disconnect", in.Plan.Database.NetworkName, b.ContainerID)
	databaseRuntimeDocker(t, f.engine, "network", "connect", "--alias", "wrong-origin-upgrade", in.Plan.Database.NetworkName, b.ContainerID)
	observe(false)
	databaseRuntimeDocker(t, f.engine, "network", "disconnect", in.Plan.Database.NetworkName, b.ContainerID)
	databaseRuntimeDocker(t, f.engine, "network", "connect", "--alias", in.Plan.Database.Network.Alias, in.Plan.Database.NetworkName, b.ContainerID)
	observe(true)
	next := upgradeNext(t, in.Plan, databaseOriginHealthy(f, opB, in.Plan, b), origin)
	opC := "hop_origin_upgrade_" + kind + "_c"
	c := databaseOriginApplyWithEvidence(f, opC, next, databaseOriginCredentials(f, next))
	if got := f.live(opC, next.Plan, c, b.ContainerID); got != record {
		t.Fatal("redeploy changed retained physical origin")
	}
	expected["c"] = []byte("retained-after-v2-redeploy-" + kind)
	databaseOriginDNSData(f, next.Plan, c, "c", expected)
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("origin bytes changed across upgrade/redeploy")
	}
	t.Logf("physical origin continuity engine=%s original_plan=%s upgrade_plan=%s redeploy_plan=%s volume=%s created=%s network_id=%s origin_hash=%s origin_bytes_unchanged=true", kind, f.plan.Hash, in.Plan.Hash, next.Plan.Hash, f.evidence.VolumeName, f.evidence.CreatedAt, origin.NetworkID, origin.DocumentHash())
}

func TestHostingDatabaseOriginUpgradeNativeRuntime(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_ORIGIN_UPGRADE_RUNTIME") != "disposable-dind" {
		t.Skip("requires reviewed separately authorized origin upgrade DinD fixture")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || os.Geteuid() != 0 {
		t.Fatal("native root Linux arm64 origin upgrade fixture required")
	}
	root := "/var/lib/" + databaseOriginUpgradeOwner
	if os.Mkdir(root, 0700) != nil {
		t.Fatal("origin upgrade fixture root must be new")
	}
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) { databaseOriginUpgradePhysical(t, root, kind) })
	}
}
