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
	"sort"
	"strconv"
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

type databaseOriginTopShape struct {
	OutputBounded, HeaderMatches, RowsBounded bool
	FieldsMatch, PIDsCanonical, PIDsUnique    bool
	UIDsMatch, ExpectedPIDKnown, PIDPresent   bool
	OwnershipMatches                          bool
	Rows                                      int
}

// This projection never retains process IDs, user strings, or raw ps output.
func databaseOriginTopSummary(raw []byte, expectedPID int) databaseOriginTopShape {
	shape := databaseOriginTopShape{OutputBounded: len(raw) <= 8192, ExpectedPIDKnown: expectedPID > 0}
	if !shape.OutputBounded {
		return shape
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	shape.HeaderMatches = slices.Equal(strings.Fields(lines[0]), []string{"PID", "UID"})
	shape.RowsBounded = len(lines) >= 2 && len(lines) <= 256
	if !shape.RowsBounded {
		return shape
	}
	shape.Rows = len(lines) - 1
	shape.FieldsMatch, shape.PIDsCanonical, shape.PIDsUnique, shape.UIDsMatch = true, true, true, true
	seen := map[int]bool{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			shape.FieldsMatch = false
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid < 1 || strconv.Itoa(pid) != fields[0] {
			shape.PIDsCanonical = false
		} else {
			shape.PIDsUnique = shape.PIDsUnique && !seen[pid]
			seen[pid] = true
			shape.PIDPresent = shape.PIDPresent || pid == expectedPID
		}
		shape.UIDsMatch = shape.UIDsMatch && fields[1] == "999"
	}
	shape.OwnershipMatches = databaseUpgradeTopMatches(raw, expectedPID)
	return shape
}

func TestDatabaseOriginTopDiagnosticShape(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		pid       int
		want      databaseOriginTopShape
	}{
		{"exact", "PID UID\n123 999\n124 999\n", 123, databaseOriginTopShape{true, true, true, true, true, true, true, true, true, true, 2}},
		{"wrong_header", "USER PID\n123 999\n", 123, databaseOriginTopShape{true, false, true, true, true, true, true, true, true, false, 1}},
		{"wrong_uid", "PID UID\n123 0\n", 123, databaseOriginTopShape{true, true, true, true, true, true, false, true, true, false, 1}},
		{"missing_pid", "PID UID\n124 999\n", 123, databaseOriginTopShape{true, true, true, true, true, true, true, true, false, false, 1}},
		{"unknown_pid", "PID UID\n123 999\n", 0, databaseOriginTopShape{true, true, true, true, true, true, true, false, false, false, 1}},
		{"duplicate_pid", "PID UID\n123 999\n123 999\n", 123, databaseOriginTopShape{true, true, true, true, true, false, true, true, true, false, 2}},
		{"noncanonical_pid", "PID UID\n+123 999\n", 123, databaseOriginTopShape{true, true, true, true, false, true, true, true, false, false, 1}},
		{"extra_field", "PID UID\n123 999 private\n", 123, databaseOriginTopShape{true, true, true, false, true, true, true, true, false, false, 1}},
		{"too_many_rows", "PID UID\n" + strings.Repeat("123 999\n", 256), 123, databaseOriginTopShape{OutputBounded: true, HeaderMatches: true, ExpectedPIDKnown: true}},
		{"oversized", strings.Repeat("SENSITIVE-TOP-OUTPUT", 512), 123, databaseOriginTopShape{ExpectedPIDKnown: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := databaseOriginTopSummary([]byte(test.raw), test.pid)
			if got != test.want {
				t.Fatalf("closed top diagnostic mismatch: got %+v want %+v", got, test.want)
			}
			raw, err := json.Marshal(got)
			if err != nil || bytes.Contains(raw, []byte("SENSITIVE-TOP-OUTPUT")) || bytes.Contains(raw, []byte("private")) {
				t.Fatal("top diagnostic leaked private content")
			}
		})
	}
}

// Every observation must remain within an explicit read-only command
// set, and leave retained state, credentials and actual writer identity unchanged.
func databaseOriginReadOnly(t *testing.T, f *databaseRuntimeFixture, run func()) {
	t.Helper()
	state, secrets := observerTree(t, f.config.stateRoot), observerTree(t, f.config.secretRoot)
	base := f.engine.command
	var sequence []string
	var inspected databaseContainer
	var top databaseOriginTopShape
	topCount, dropped := 0, 0
	record := func(stage string) {
		if len(sequence) == 256 {
			sequence = sequence[1:]
			dropped++
		}
		sequence = append(sequence, stage)
	}
	f.engine.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if len(args) < 6 {
			record("unclassified:refused")
			return nil, errors.New("read-only fixture command refused")
		}
		a := args[4:]
		allowed := databaseOriginReadOnlyAllowed(a, f.request.Storage.VolumeName)
		if !allowed {
			record(databaseOriginCommandStage(a) + ":refused")
			t.Errorf("observation command refused: stage=%s", databaseOriginCommandStage(a))
			return nil, errors.New("read-only fixture effect refused")
		}
		raw, err := base.RunEnvironment(ctx, name, args, dir, env)
		stage := databaseOriginCommandStage(a)
		if err != nil {
			record(stage + ":error")
		} else {
			record(stage + ":ok")
		}
		if stage == "container_inspect" {
			inspected = databaseContainer{}
			if err == nil && len(raw) <= 256<<10 {
				// A malformed response cannot supply the PID for a later top.
				var current databaseContainer
				if json.Unmarshal(raw, &current) == nil && len(a) > 2 && current.ID == a[2] {
					inspected.ID, inspected.State.Pid = current.ID, current.State.Pid
				}
			}
		}
		if stage == "container_top" {
			expectedPID := 0
			if len(a) > 2 && a[2] == inspected.ID {
				expectedPID = inspected.State.Pid
			}
			top = databaseOriginTopSummary(raw, expectedPID)
			topCount++
		}
		return raw, err
	})
	defer func() {
		f.engine.command = base
		if !maps.Equal(state, observerTree(t, f.config.stateRoot)) || !maps.Equal(secrets, observerTree(t, f.config.secretRoot)) {
			t.Error("read-only physical observation changed protected state")
		}
		if t.Failed() {
			raw, err := json.Marshal(top)
			if err != nil {
				t.Error("closed observation diagnostics unavailable")
				return
			}
			t.Logf("closed_observation_command_sequence=%s dropped_stages=%d top_count=%d top_shape=%s", strings.Join(sequence, ","), dropped, topCount, raw)
		}
	}()
	run()
}

// FailNow must still check protected state and restore the command runner. This
// child intentionally fails; its parent checks only closed fixture diagnostics.
func TestDatabaseOriginReadOnlyFatalDiagnostics(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_OBSERVATION_FATAL") == "fixture" {
		state, secrets := t.TempDir(), t.TempDir()
		path := filepath.Join(state, "record")
		if os.WriteFile(path, []byte("original"), 0o600) != nil {
			t.Fatal("fixture state unavailable")
		}
		baseCalls := 0
		id := strings.Repeat("a", 64)
		engine := &imageEngine{command: imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
			baseCalls++
			if len(args) > 5 && args[4] == "container" {
				switch args[5] {
				case "inspect":
					return json.Marshal(map[string]any{"Id": id, "State": map[string]any{"Pid": 123}})
				case "top":
					return []byte("PID UID\n123 0\nSENSITIVE-OBSERVATION-OUTPUT 999\n"), nil
				}
			}
			return nil, errors.New("SENSITIVE-OBSERVATION-ERROR")
		})}
		f := &databaseRuntimeFixture{engine: engine, config: DatabaseEngineConfig{stateRoot: state, secretRoot: secrets}}
		t.Cleanup(func() {
			before := baseCalls
			_, _ = engine.command.RunEnvironment(t.Context(), "fixture", nil, "", nil)
			if baseCalls != before+1 {
				t.Error("read-only command runner restoration failed")
			} else {
				t.Log("read-only command runner restored")
			}
		})
		databaseOriginReadOnly(t, f, func() {
			for range 260 {
				_, _ = engine.docker(t.Context(), "info", "SENSITIVE-OBSERVATION-ARGUMENT")
			}
			_, _ = engine.docker(t.Context(), "container", "inspect", id)
			_, _ = engine.docker(t.Context(), "container", "top", id, "-eo", "pid,uid")
			if os.WriteFile(path, []byte("changed"), 0o600) != nil {
				t.Fatal("fixture state change unavailable")
			}
			t.Fatal("injected observation assertion")
		})
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("fixture subprocess unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run", "^TestDatabaseOriginReadOnlyFatalDiagnostics$", "-test.v")
	cmd.Env = append(os.Environ(), "NEROCD_DATABASE_OBSERVATION_FATAL=fixture")
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || bytes.Contains(out, []byte("SENSITIVE-OBSERVATION")) {
		t.Fatal("failure subprocess escaped closed diagnostics")
	}
	for _, want := range []string{"injected observation assertion", "read-only physical observation changed protected state", "read-only command runner restored", "closed_observation_command_sequence=daemon_info:error", "dropped_stages=6 top_count=1", `"ExpectedPIDKnown":true`, `"PIDPresent":true`, `"PIDsCanonical":false`, `"UIDsMatch":false`, `"OwnershipMatches":false`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("fatal observation omitted required evidence: %s", want)
		}
	}
}

type databaseOriginMountExpectation struct {
	Volume, DataSource, DataDestination, SecretSource string
}

func databaseOriginSourceProjection(c databaseContainer, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, mounts databaseOriginMountExpectation) (databaseContainer, bool) {
	inspection := &databaseUpgradeInspection{Previous: plan, Origin: domain.HostingDatabaseNetworkOrigin(record)}
	if !c.State.Running || c.State.Status != "running" || c.State.Pid < 1 || !databaseContainerMatchesWithInspection(c, operation, plan, record, mounts.SecretSource, inspection) || len(c.Mounts) != 2 || mounts.DataSource == "" || !filepath.IsAbs(mounts.DataSource) || filepath.Clean(mounts.DataSource) != mounts.DataSource {
		return c, false
	}
	seen := map[string]bool{}
	for _, mount := range c.Mounts {
		if seen[mount.Destination] {
			return c, false
		}
		seen[mount.Destination] = true
		switch mount.Destination {
		case mounts.DataDestination:
			if mount.Type != "volume" || mount.Name != mounts.Volume || mount.Driver != "local" || mount.Source != mounts.DataSource || !mount.RW {
				return c, false
			}
		case databaseSecretMount:
			if mount.Type != "bind" || mount.Source != mounts.SecretSource || mount.RW || mount.Name != "" || mount.Driver != "" {
				return c, false
			}
		default:
			return c, false
		}
	}
	if !seen[mounts.DataDestination] || !seen[databaseSecretMount] {
		return c, false
	}
	c.Mounts = slices.Clone(c.Mounts)
	sort.Slice(c.Mounts, func(i, j int) bool { return c.Mounts[i].Destination < c.Mounts[j].Destination })
	return c, true
}

func databaseOriginClosedState(state string) string {
	switch state {
	case "created", "running", "exited", "dead", "paused", "restarting":
		return state
	default:
		return "unclassified"
	}
}

type databaseOriginSourceDiagnostic struct {
	Inspected, BeforeValid, AfterValid                                    bool
	IdentityEqual, ConfigEqual, HostConfigEqual, StateEqual, NetworkEqual bool
	MountOrderEqual, ValidatedMountSetEqual                               bool
	PIDBefore, PIDAfter                                                   int
	StateBefore, StateAfter                                               string
	RunningBefore, RunningAfter                                           bool
}

func databaseOriginSourceDifference(before, current databaseContainer, normalizedBefore, normalizedCurrent databaseContainer, inspected, beforeValid, afterValid bool) databaseOriginSourceDiagnostic {
	return databaseOriginSourceDiagnostic{Inspected: inspected, BeforeValid: beforeValid, AfterValid: afterValid, IdentityEqual: before.ID == current.ID && before.Name == current.Name && before.Image == current.Image, ConfigEqual: domain.HostingDocumentHash(before.Config) == domain.HostingDocumentHash(current.Config), HostConfigEqual: domain.HostingDocumentHash(before.HostConfig) == domain.HostingDocumentHash(current.HostConfig), StateEqual: before.State == current.State, NetworkEqual: domain.HostingDocumentHash(before.NetworkSettings) == domain.HostingDocumentHash(current.NetworkSettings), MountOrderEqual: domain.HostingDocumentHash(before.Mounts) == domain.HostingDocumentHash(current.Mounts), ValidatedMountSetEqual: beforeValid && afterValid && domain.HostingDocumentHash(normalizedBefore.Mounts) == domain.HostingDocumentHash(normalizedCurrent.Mounts), PIDBefore: before.State.Pid, PIDAfter: current.State.Pid, StateBefore: databaseOriginClosedState(before.State.Status), StateAfter: databaseOriginClosedState(current.State.Status), RunningBefore: before.State.Running, RunningAfter: current.State.Running}
}

// Cleanup has its own bound because testing cancels t.Context before Cleanup.
// Every CLI action uses the engine's joined process-group command runner. An
// ambiguous inspect/remove/list outcome remains an error, never inferred absence.
func databaseOriginRemoveClient(parent context.Context, engine *imageEngine, id, name string, plan domain.HostingApplyPlan) error {
	if !dockerObjectID.MatchString(id) {
		return errors.New("private client cleanup identity unavailable")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	raw, err := engine.ownedVolumeDocker(ctx, 64<<10, "container", "inspect", id, "--format", "{{json .}}")
	var container struct {
		ID          string `json:"Id"`
		Name, Image string
		Config      struct{ Labels map[string]string }
		HostConfig  struct{ NetworkMode string }
	}
	if err != nil || json.Unmarshal(raw, &container) != nil || !dockerObjectID.MatchString(id) || container.ID != id || container.Name != "/"+name || container.Image != plan.Release.ImageID || container.Config.Labels["io.nerocd.origin-upgrade.client"] != databaseOriginUpgradeOwner || container.HostConfig.NetworkMode != plan.Database.NetworkName {
		return errors.New("private client cleanup identity unavailable")
	}
	raw, err = engine.ownedVolumeDocker(ctx, 1024, "container", "rm", "--force", "--volumes", id)
	if err != nil || strings.TrimSpace(string(raw)) != id {
		return errors.New("private client removal not confirmed")
	}
	raw, err = engine.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		return errors.New("private client absence not confirmed")
	}
	return nil
}

func databaseOriginTemporaryClient(f *databaseRuntimeFixture, plan domain.HostingApplyPlan, run func(string)) {
	f.t.Helper()
	name := "ncd-origin-client-" + f.recipe.Engine
	id := strings.TrimSpace(string(databaseRuntimeDocker(f.t, f.engine, "container", "create", "--name", name, "--label", "io.nerocd.origin-upgrade.client="+databaseOriginUpgradeOwner, "--network", plan.Database.NetworkName, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "sleep", plan.Release.ImageID, "300")))
	if !dockerObjectID.MatchString(id) {
		f.t.Fatal("private client create identity unavailable")
	}
	removed := false
	remove := func() error {
		if removed {
			return nil
		}
		if err := databaseOriginRemoveClient(f.t.Context(), f.engine, id, name, plan); err != nil {
			return err
		}
		removed = true
		return nil
	}
	f.t.Cleanup(func() {
		if remove() != nil {
			f.t.Error("private client cleanup not confirmed")
		}
	})
	databaseRuntimeDocker(f.t, f.engine, "container", "start", id)
	executable, err := os.Executable()
	if err != nil {
		f.t.Fatal("private client executable unavailable")
	}
	databaseRuntimeDocker(f.t, f.engine, "cp", executable, id+":/tmp/origin-client.test")
	run(id)
	if remove() != nil {
		f.t.Fatal("private client cleanup not confirmed")
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
	var physicalVolume struct {
		Name, Mountpoint, CreatedAt, Driver, Scope string
		Labels                                     map[string]string
		Options                                    map[string]string
	}
	if json.Unmarshal(databaseRuntimeDocker(t, f.engine, "volume", "inspect", f.evidence.VolumeName, "--format", "{{json .}}"), &physicalVolume) != nil || physicalVolume.Name != f.evidence.VolumeName || physicalVolume.CreatedAt != f.evidence.CreatedAt || physicalVolume.Driver != "local" || physicalVolume.Scope != "local" || !maps.Equal(physicalVolume.Labels, f.evidence.Labels) || len(physicalVolume.Options) != 0 {
		t.Fatal("source mount volume identity unavailable")
	}
	mounts := databaseOriginMountExpectation{Volume: f.evidence.VolumeName, DataSource: physicalVolume.Mountpoint, DataDestination: f.plan.Storage.MountPath, SecretSource: filepath.Join(f.config.secretRoot, databaseGeneration(opA, f.plan))}
	normalizedSource, sourceValid := databaseOriginSourceProjection(source, opA, f.plan, record, mounts)
	if !sourceValid {
		t.Fatal("source identity or exact mount policy unavailable")
	}
	sourceHash := domain.HostingDocumentHash(normalizedSource)
	assertSource := func() {
		t.Helper()
		current, err := f.engine.inspectDatabaseContainer(t.Context(), a.ContainerID)
		normalizedCurrent, currentValid := databaseOriginSourceProjection(current, opA, f.plan, record, mounts)
		if err != nil || !currentValid || domain.HostingDocumentHash(normalizedCurrent) != sourceHash {
			diagnostic := databaseOriginSourceDifference(source, current, normalizedSource, normalizedCurrent, err == nil, sourceValid, currentValid)
			raw, encodeErr := json.Marshal(diagnostic)
			if encodeErr != nil {
				t.Fatal("closed source diagnostics unavailable")
			}
			t.Logf("denial_source_difference=%s", raw)
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
				t.Logf("observation_predicates want_ready=%t error_present=%t ready=%t container_matches=%t", want, err != nil, got.Ready, got.ContainerID == b.ContainerID)
				shape := databaseOriginCandidateShape{State: "unavailable"}
				if container, inspectErr := f.engine.inspectDatabaseContainer(t.Context(), b.ContainerID); inspectErr == nil {
					shape = databaseOriginCandidateSummary(container, opB, in, record, f.config)
				}
				raw, encodeErr := json.Marshal(shape)
				if encodeErr != nil {
					t.Fatal("closed observed candidate diagnostics unavailable")
				}
				t.Logf("failed_observation_candidate_shape=%s", raw)
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

func TestDatabaseOriginUpgradeSourceComparison(t *testing.T) {
	f, in, credentials := upgradeFixture(t, "redis")
	credentials.Close()
	previous := in.Plan.PreviousHealthy
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	source, err := f.e.inspectDatabaseContainer(ctx, previous.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	mounts := databaseOriginMountExpectation{Volume: in.PreviousPlan.Storage.VolumeName, DataSource: filepath.Join(t.TempDir(), "owned-data"), DataDestination: in.PreviousPlan.Storage.MountPath, SecretSource: filepath.Join(f.config.secretRoot, databaseGeneration(previous.OperationID, in.PreviousPlan))}
	for i := range source.Mounts {
		if source.Mounts[i].Destination == mounts.DataDestination {
			source.Mounts[i].Source = mounts.DataSource
		}
	}
	record := databaseNetworkRecord(in.Proof.Origin)
	normalized, valid := databaseOriginSourceProjection(source, previous.OperationID, in.PreviousPlan, record, mounts)
	if !valid {
		t.Fatal("source comparison fixture invalid")
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same", "mount_order", "duplicate_mount", "foreign_volume", "foreign_data_source", "data_readonly", "secret_writable", "foreign_secret_source", "foreign_driver", "pid", "state", "network", "config", "host_policy"} {
		t.Run(mode, func(t *testing.T) {
			var current databaseContainer
			if json.Unmarshal(raw, &current) != nil {
				t.Fatal("source clone failed")
			}
			current.imageEnvironment = append([]string(nil), source.imageEnvironment...)
			data, secret := -1, -1
			for i, m := range current.Mounts {
				if m.Destination == mounts.DataDestination {
					data = i
				}
				if m.Destination == databaseSecretMount {
					secret = i
				}
			}
			want := false
			switch mode {
			case "same":
				want = true
			case "mount_order":
				current.Mounts[0], current.Mounts[1] = current.Mounts[1], current.Mounts[0]
				want = true
			case "duplicate_mount":
				current.Mounts[secret] = current.Mounts[data]
			case "foreign_volume":
				current.Mounts[data].Name = "foreign"
			case "foreign_data_source":
				current.Mounts[data].Source = filepath.Join(t.TempDir(), "foreign")
			case "data_readonly":
				current.Mounts[data].RW = false
			case "secret_writable":
				current.Mounts[secret].RW = true
			case "foreign_secret_source":
				current.Mounts[secret].Source = filepath.Join(t.TempDir(), "foreign")
			case "foreign_driver":
				current.Mounts[data].Driver = "foreign"
			case "pid":
				current.State.Pid++
			case "state":
				current.State.Status = "paused"
			case "network":
				n := current.NetworkSettings.Networks[in.PreviousPlan.Database.NetworkName]
				n.IPAddress = "172.28.0.88"
				current.NetworkSettings.Networks[in.PreviousPlan.Database.NetworkName] = n
			case "config":
				current.Config.User = "root"
			case "host_policy":
				current.HostConfig.Privileged = true
			}
			projected, currentValid := databaseOriginSourceProjection(current, previous.OperationID, in.PreviousPlan, record, mounts)
			got := currentValid && domain.HostingDocumentHash(projected) == domain.HostingDocumentHash(normalized)
			if got != want {
				t.Fatalf("source identity comparison %s accepted=%t want=%t", mode, got, want)
			}
			if mode == "mount_order" {
				difference := databaseOriginSourceDifference(source, current, normalized, projected, true, valid, currentValid)
				if difference.MountOrderEqual || !difference.ValidatedMountSetEqual || !difference.StateEqual || !difference.NetworkEqual {
					t.Fatal("mount-order diagnostic flags mismatch")
				}
			}
		})
	}
	marker := "SENSITIVE-SOURCE-DIAGNOSTIC"
	current := source
	current.Name = marker
	current.State.Status = marker
	current.Config.User = marker
	diagnostic := databaseOriginSourceDifference(source, current, normalized, current, true, true, false)
	encoded, err := json.Marshal(diagnostic)
	if err != nil || bytes.Contains(encoded, []byte(marker)) || diagnostic.StateAfter != "unclassified" {
		t.Fatal("source diagnostic leaked private content")
	}
}

func TestDatabaseOriginUpgradeCleanupAfterCancellation(t *testing.T) {
	id := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	plan := domain.HostingApplyPlan{Release: domain.HostingRelease{ImageID: image}, Database: &domain.HostingDatabasePlan{NetworkName: "private_fixture_network"}}
	for _, mode := range []string{"canceled_parent", "foreign_label", "wrong_id", "inspect_error", "remove_response_lost", "still_present"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			calls := 0
			engine := &imageEngine{endpoint: "unix://fixture", configDir: t.TempDir()}
			engine.command = imageCommandFunc(func(ctx context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if ctx.Err() != nil || !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) <= 0 {
					t.Error("cleanup context canceled or unbounded")
					return nil, errors.New("invalid cleanup context")
				}
				a := args[4:]
				if len(a) > 1 && a[0] == "container" && a[1] == "inspect" {
					if mode == "inspect_error" {
						return nil, errors.New("SENSITIVE-CLEANUP-DIAGNOSTIC")
					}
					identity, owner := id, databaseOriginUpgradeOwner
					if mode == "wrong_id" {
						identity = strings.Repeat("c", 64)
					}
					if mode == "foreign_label" {
						owner = "foreign"
					}
					return json.Marshal(map[string]any{"Id": identity, "Name": "/private_client", "Image": image, "Config": map[string]any{"Labels": map[string]string{"io.nerocd.origin-upgrade.client": owner}}, "HostConfig": map[string]any{"NetworkMode": plan.Database.NetworkName}})
				}
				if slices.Equal(a, []string{"container", "rm", "--force", "--volumes", id}) {
					if mode == "remove_response_lost" {
						return nil, errors.New("SENSITIVE-CLEANUP-DIAGNOSTIC")
					}
					return []byte(id + "\n"), nil
				}
				if slices.Equal(a, []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id=" + id}) {
					if mode == "still_present" {
						return []byte(id), nil
					}
					return nil, nil
				}
				t.Error("unexpected cleanup action")
				return nil, errors.New("unexpected cleanup action")
			})
			err := databaseOriginRemoveClient(parent, engine, id, "private_client", plan)
			if mode == "canceled_parent" {
				if err != nil || calls != 3 {
					t.Fatal("canceled-parent cleanup did not complete")
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "SENSITIVE-CLEANUP-DIAGNOSTIC") {
					t.Fatal("cleanup failed open or leaked private error")
				}
			}
			if (mode == "foreign_label" || mode == "wrong_id" || mode == "inspect_error") && calls != 1 {
				t.Fatal("unconfirmed client identity reached removal")
			}
			if mode == "remove_response_lost" && calls != 2 {
				t.Fatal("ambiguous removal was retried or accepted")
			}
		})
	}
}
