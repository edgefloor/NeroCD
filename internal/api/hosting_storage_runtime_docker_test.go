package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nerocd/internal/hostingproxy"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func storageDiagnosticWord(value string, allowed ...string) string {
	for _, word := range allowed {
		if value == word {
			return word
		}
	}
	return "unknown"
}

func storageDiagnosticResult(r *domain.HostingStorageArchiveResult) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"archive_outcome": storageDiagnosticWord(r.ArchiveOutcome, "succeeded", "failed", "not_attempted"),
		"resume_outcome":  storageDiagnosticWord(r.ResumeOutcome, "resumed", "not_needed", "uncertain"),
		"failure_code":    storageDiagnosticWord(r.FailureCode, "", "helper_failed", "archive_invalid", "ownership_mismatch", "interrupted", "resume_failed", "canceled"),
		"uncertain":       r.Uncertain, "restore_verified": r.RestoreVerified,
	}
}

func storageDiagnosticPhases(entries []runner.JournalStorageArchive, operation string) []string {
	var phases []string
	for _, entry := range entries {
		if entry.Plan.OperationID != operation || len(phases) == 32 {
			continue
		}
		phase := entry.Phase
		if entry.Progress != nil {
			phase = entry.Progress.Phase
		}
		phases = append(phases, storageDiagnosticWord(phase, "plan", "prepared", "apply_intent", "writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_unverified", "helper_identified", "archive_published", "restore_verified", "resume_intent", "resumed", "complete", "storage_create_intent", "storage_materialized", "recovery_settlement"))
	}
	return phases
}

// Decode only the archive family; never log a journal or decode application payloads.
func storageDiagnosticJournal(input io.Reader) ([]runner.JournalStorageArchive, error) {
	const limit = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || len(raw) > limit {
		return nil, fmt.Errorf("journal unavailable or exceeds diagnostic bound")
	}
	var snapshot struct {
		Entries []runner.JournalStorageArchive `json:"storage_archive"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Entries) > 128 {
		return nil, fmt.Errorf("journal snapshot invalid or exceeds diagnostic bound")
	}
	return snapshot.Entries, nil
}

func TestStorageRuntimeDiagnosticBoundaries(t *testing.T) {
	secret := "credential-or-application-payload"
	r := &domain.HostingStorageArchiveResult{ArchiveOutcome: secret, ResumeOutcome: "resumed", FailureCode: secret, Blob: &domain.HostingStorageArchiveBlob{SHA256: secret}}
	entries := []runner.JournalStorageArchive{{Plan: domain.HostingStorageArchivePlan{OperationID: "owned"}, Phase: secret}, {Plan: domain.HostingStorageArchivePlan{OperationID: "other"}, Phase: "complete"}}
	raw, err := json.Marshal(map[string]any{"result": storageDiagnosticResult(r), "phases": storageDiagnosticPhases(entries, "owned")})
	if err != nil || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "unknown") || strings.Contains(string(raw), "complete") {
		t.Fatal("diagnostic allowlist leaked unapproved values")
	}
	for _, input := range []string{"{", strings.Repeat(" ", (4<<20)+1), `{"storage_archive":[` + strings.Repeat(`{},`, 128) + `{}]}`} {
		if _, err := storageDiagnosticJournal(strings.NewReader(input)); err == nil {
			t.Fatal("unbounded or malformed diagnostic journal accepted")
		}
	}
	decoded, err := storageDiagnosticJournal(strings.NewReader(`{"events":[{"payload":"` + secret + `"}],"storage_archive":[]}`))
	if err != nil || len(decoded) != 0 {
		t.Fatal("unrelated journal payload influenced archive diagnostic")
	}
	entries = nil
	for range 40 {
		entries = append(entries, runner.JournalStorageArchive{Plan: domain.HostingStorageArchivePlan{OperationID: "owned"}, Progress: &domain.HostingStorageArchiveProgress{Phase: "helper_intent"}})
	}
	if phases := storageDiagnosticPhases(entries, "owned"); len(phases) != 32 || phases[0] != "helper_intent" {
		t.Fatal("diagnostic phase bound or progress selection changed")
	}
}

// Direct fixture starts must use a prerequisite image already verified by the
// gate. This covers Pebble and Silo without changing production pull behavior.
func storageRuntimeDockerArguments(args []string) []string {
	if len(args) > 0 && args[0] == "run" {
		return append([]string{"run", "--pull=never"}, args[1:]...)
	}
	return args
}

// Docker progress and warnings use stderr. They must never become a container
// ID, JSON document, or other stdout value consumed by the fixture.
func storageRuntimeDockerOutput(command *exec.Cmd) ([]byte, error) {
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, diagnostics.Bytes())
	}
	return output, nil
}

func TestStorageRuntimeDockerOutputSeparatesDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		failure    bool
	}{
		{name: "successful ID with pull-like diagnostics", mode: "success"},
		{name: "failed command keeps stderr", mode: "failure", failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStorageRuntimeDockerOutputProcess$")
			command.Env = append(os.Environ(), "NEROCD_STORAGE_DOCKER_OUTPUT_TEST="+test.mode)
			output, err := storageRuntimeDockerOutput(command)
			if test.failure {
				if err == nil || !strings.Contains(err.Error(), "fixture Docker diagnostic") || len(output) != 0 {
					t.Fatalf("failure output=%q error=%v", output, err)
				}
				return
			}
			if err != nil || string(output) != strings.Repeat("a", 64)+"\n" {
				t.Fatalf("container ID contaminated: %q error=%v", output, err)
			}
		})
	}
}

func TestStorageRuntimeDockerOutputProcess(t *testing.T) {
	mode := os.Getenv("NEROCD_STORAGE_DOCKER_OUTPUT_TEST")
	if mode == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "fixture Docker diagnostic")
	if _, err := fmt.Fprintln(os.Stdout, strings.Repeat("a", 64)); err != nil {
		os.Exit(1)
	}
	if mode == "failure" {
		os.Exit(17)
	}
	os.Exit(0)
}

func TestStorageRuntimeDockerDirectStartsNeverPull(t *testing.T) {
	t.Run("remote request contracts", testStorageRemoteRuntimeRequestContracts)
	t.Run("fixture port separation", testStorageRuntimeFixturePortSeparation)
	t.Run("closed helper environment diagnostic", testStorageDiagnosticHelperEnvironment)
	t.Run("owned proxy cleanup", testStorageRuntimeOwnedProxyCleanup)
	original := []string{"run", "--detach", "pinned@sha256:" + strings.Repeat("a", 64)}
	before := append([]string(nil), original...)
	want := append([]string{"run", "--pull=never"}, original[1:]...)
	if actual := storageRuntimeDockerArguments(original); !reflect.DeepEqual(actual, want) {
		t.Fatalf("run arguments=%q want=%q", actual, want)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("input arguments modified")
	}
	inspect := []string{"image", "inspect", "pinned"}
	if actual := storageRuntimeDockerArguments(inspect); !reflect.DeepEqual(actual, inspect) {
		t.Fatal("non-start command changed")
	}
}

const (
	storageRuntimeOwner                 = "nerocd-storage-archive-runtime-20261024-f0a618"
	storageRuntimeApplicationPort       = 20101
	storageRuntimeUnrelatedPort         = 20102
	storageRuntimeMissingPort           = 20103
	storageRuntimeRestorePort           = 20104
	storageRemoteRuntimeApplicationPort = 20111
	storageRemoteRuntimeRestorePort     = 20114
)

var storageRuntimeContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type storageRuntimeProxyProfile struct {
	name, daemon, image, directory string
	labels                         map[string]string
	args                           []string
}

func storageRuntimeProxyProfileFor(root, proxy, server, destination, runnerID, daemon string) (storageRuntimeProxyProfile, error) {
	var zero storageRuntimeProxyProfile
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(filepath.Base(root), storageRuntimeOwner+"-") {
		return zero, errors.New("fixture root ownership mismatch")
	}
	directory := filepath.Join(root, "proxy", proxy)
	settings := hostingproxy.Settings{Directory: directory, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Email: "operator@example.test", CA: hostingproxy.LetsEncryptStaging, ManagementPort: 19080}
	identity := hostingproxy.Identity{ProxyID: proxy, ServerID: server, DestinationID: destination, RunnerID: runnerID, DaemonID: daemon}
	plan, err := hostingproxy.Prepare(identity, settings, 1, 0, nil)
	if err != nil {
		return zero, errors.New("fixture proxy identity invalid")
	}
	return storageRuntimeProxyProfile{
		name: "/nerocd-proxy-" + proxy, daemon: daemon, image: hostingproxy.Image, directory: directory,
		labels: map[string]string{"io.nerocd.proxy": proxy, "io.nerocd.server": server, "io.nerocd.destination": destination, "io.nerocd.runner": runnerID, "io.nerocd.daemon": daemon, "io.nerocd.static": plan.StaticHash()},
		args:   []string{"--global.checknewversion=false", "--global.sendanonymoususage=false", "--api=true", "--api.dashboard=false", "--api.insecure=true", "--entrypoints.traefik.address=127.0.0.1:19080", "--entrypoints.web.address=:80", "--entrypoints.websecure.address=:443", "--providers.file.directory=/dynamic", "--providers.file.watch=true", "--providers.providersthrottleduration=100ms", "--certificatesresolvers.managed.acme.email=operator@example.test", "--certificatesresolvers.managed.acme.storage=/certificates/acme.json", "--certificatesresolvers.managed.acme.caserver=https://localhost:14000/dir", "--certificatesresolvers.managed.acme.httpchallenge.entrypoint=web", "--log.level=ERROR", "--certificatesresolvers.managed.acme.certificatesduration=1", "--api.debug=true"},
	}, nil
}

type storageRuntimeProxyContainer struct {
	ID, Name, Image string
	Config          struct {
		Image  string
		Labels map[string]string
		Cmd    []string
	}
	HostConfig struct {
		NetworkMode    string
		ReadonlyRootfs bool
		RestartPolicy  struct{ Name string }
	}
	State  struct{ Running bool }
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
}

func storageRuntimeCheckProxy(raw []byte, p storageRuntimeProxyProfile, id, image string) (storageRuntimeProxyContainer, error) {
	var c storageRuntimeProxyContainer
	if len(raw) > 64<<10 || json.Unmarshal(raw, &c) != nil {
		return c, errors.New("proxy inspection unavailable or bounded")
	}
	if !storageRuntimeContainerID.MatchString(id) || c.ID != id || c.Name != p.name || c.Image != image || c.Config.Image != p.image {
		return c, errors.New("proxy container identity mismatch")
	}
	for key, value := range p.labels {
		if c.Config.Labels[key] != value {
			return c, errors.New("proxy authority or static profile mismatch")
		}
	}
	if c.HostConfig.NetworkMode != "host" || c.HostConfig.RestartPolicy.Name != "unless-stopped" || !c.HostConfig.ReadonlyRootfs || !reflect.DeepEqual(c.Config.Cmd, p.args) {
		return c, errors.New("proxy runtime profile mismatch")
	}
	expected := map[string]bool{filepath.Join(p.directory, "dynamic") + ":/dynamic:false": true, filepath.Join(p.directory, "certificates") + ":/certificates:true": true}
	if len(c.Mounts) != 2 {
		return c, errors.New("proxy bind roots mismatch")
	}
	for _, mount := range c.Mounts {
		key := fmt.Sprintf("%s:%s:%t", mount.Source, mount.Destination, mount.RW)
		if mount.Type != "bind" || !expected[key] {
			return c, errors.New("proxy bind roots mismatch")
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return c, errors.New("proxy bind roots mismatch")
	}
	return c, nil
}

// Cleanup authority is the exact private fixture profile, never a shared label
// or name alone. Every mutation uses an inspected immutable container ID.
func storageRuntimeRemoveOwnedProxy(p storageRuntimeProxyProfile, command func(...string) ([]byte, error)) error {
	daemon := func() error {
		raw, err := command("info", "--format", "{{.ID}}")
		if err != nil || strings.TrimSpace(string(raw)) != p.daemon {
			return errors.New("proxy cleanup daemon mismatch")
		}
		return nil
	}
	if err := daemon(); err != nil {
		return err
	}
	find := func(filter string) ([]string, error) {
		raw, err := command("container", "ls", "--all", "--no-trunc", "--filter", filter, "--format", "{{.ID}}")
		if err != nil || len(raw) > 1024 {
			return nil, errors.New("proxy inventory unavailable")
		}
		return strings.Fields(string(raw)), nil
	}
	ids, err := find("name=^" + p.name + "$")
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if len(ids) != 1 || !storageRuntimeContainerID.MatchString(ids[0]) {
		return errors.New("proxy inventory identity ambiguous")
	}
	id := ids[0]
	imageRaw, err := command("image", "inspect", p.image, "--format", "{{.Id}}")
	image := strings.TrimSpace(string(imageRaw))
	if err != nil || !strings.HasPrefix(image, "sha256:") || !storageRuntimeContainerID.MatchString(strings.TrimPrefix(image, "sha256:")) {
		return errors.New("pinned proxy image unavailable")
	}
	inspect := func() (storageRuntimeProxyContainer, error) {
		raw, err := command("container", "inspect", id, "--format", "{{json .}}")
		if err != nil {
			return storageRuntimeProxyContainer{}, errors.New("proxy inspection unavailable")
		}
		return storageRuntimeCheckProxy(raw, p, id, image)
	}
	current, err := inspect()
	if err != nil {
		return err
	}
	if err := daemon(); err != nil {
		return err
	}
	if current.State.Running {
		if _, err := command("container", "stop", "--time", "5", id); err != nil {
			return errors.New("owned proxy stop failed")
		}
	}
	current, err = inspect()
	if err != nil {
		return err
	}
	if current.State.Running {
		return errors.New("owned proxy stop unconfirmed")
	}
	if err := daemon(); err != nil {
		return err
	}
	if _, err := command("container", "rm", id); err != nil {
		return errors.New("owned proxy removal failed")
	}
	if err := daemon(); err != nil {
		return err
	}
	remaining, err := find("id=" + id)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("owned proxy absence unconfirmed")
	}
	remaining, err = find("name=^" + p.name + "$")
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("proxy name occupied after owned removal")
	}
	return nil
}

func testStorageRuntimeFixturePortSeparation(t *testing.T) {
	reserved := map[int]bool{}
	for _, port := range []int{storageRuntimeApplicationPort, storageRuntimeUnrelatedPort, storageRuntimeMissingPort, storageRuntimeRestorePort, 80, 443, 19080, 18190, 9000, 9001, 25000, 14000, 15000, 8053} {
		reserved[port] = true
	}
	for _, port := range []int{storageRemoteRuntimeApplicationPort, storageRemoteRuntimeRestorePort} {
		if port < 1024 || port > 65535 || reserved[port] {
			t.Fatalf("remote fixture port overlaps reserved endpoint: %d", port)
		}
		reserved[port] = true
	}
}

func testStorageRuntimeOwnedProxyCleanup(t *testing.T) {
	for _, mode := range []string{"valid", "absent", "already_stopped", "wrong_owner", "wrong_daemon", "changed_daemon", "wrong_bind_root", "duplicate_mount", "wrong_image", "wrong_id", "wrong_name", "wrong_command", "writable_root", "stop_error", "still_running", "changed_after_stop", "remove_error", "absence_unconfirmed", "name_reoccupied"} {
		t.Run(mode, func(t *testing.T) {
			profile, err := storageRuntimeProxyProfileFor("/var/lib/"+storageRuntimeOwner+"-unit", "proxy_fixture", "server_fixture", "dest_fixture", "runner_fixture", "daemon_fixture")
			if err != nil {
				t.Fatal(err)
			}
			id, image := strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
			c := storageRuntimeProxyContainer{ID: id, Name: profile.name, Image: image}
			c.Config.Image = profile.image
			c.Config.Labels = map[string]string{}
			for key, value := range profile.labels {
				c.Config.Labels[key] = value
			}
			c.Config.Cmd = append([]string(nil), profile.args...)
			c.HostConfig.NetworkMode = "host"
			c.HostConfig.ReadonlyRootfs = true
			c.HostConfig.RestartPolicy.Name = "unless-stopped"
			c.State.Running = mode != "already_stopped"
			c.Mounts = append(c.Mounts, struct {
				Type, Source, Destination string
				RW                        bool
			}{"bind", filepath.Join(profile.directory, "dynamic"), "/dynamic", false}, struct {
				Type, Source, Destination string
				RW                        bool
			}{"bind", filepath.Join(profile.directory, "certificates"), "/certificates", true})
			switch mode {
			case "wrong_owner":
				c.Config.Labels["io.nerocd.server"] = "private-unowned-value"
			case "wrong_bind_root":
				c.Mounts[0].Source = "/other/private-root"
			case "duplicate_mount":
				c.Mounts[1] = c.Mounts[0]
			case "wrong_image":
				c.Config.Image = "unowned:latest"
			case "wrong_id":
				c.ID = strings.Repeat("c", 64)
			case "wrong_name":
				c.Name = "/other-proxy"
			case "wrong_command":
				c.Config.Cmd = []string{"private-unapproved-command"}
			case "writable_root":
				c.HostConfig.ReadonlyRootfs = false
			}
			present := mode != "absent"
			mutations := []string{}
			daemonReads := 0
			command := func(args ...string) ([]byte, error) {
				switch args[0] {
				case "info":
					daemonReads++
					if mode == "wrong_daemon" || mode == "changed_daemon" && daemonReads > 1 {
						return []byte("foreign_daemon"), nil
					}
					return []byte(profile.daemon), nil
				case "image":
					return []byte(image), nil
				case "container":
					switch args[1] {
					case "ls":
						filter := args[5]
						if mode == "name_reoccupied" && !present && strings.HasPrefix(filter, "name=") {
							return []byte(strings.Repeat("d", 64)), nil
						}
						if present {
							return []byte(id), nil
						}
						return nil, nil
					case "inspect":
						if args[2] != id {
							t.Fatal("inspection changed immutable ID")
						}
						if mode == "changed_after_stop" && len(mutations) > 0 {
							c.Config.Labels["io.nerocd.static"] = "changed"
						}
						return json.Marshal(c)
					case "stop":
						if !reflect.DeepEqual(args, []string{"container", "stop", "--time", "5", id}) {
							t.Fatal("stop exceeded exact owned ID")
						}
						mutations = append(mutations, "stop")
						if mode == "stop_error" {
							return nil, errors.New("private daemon diagnostic")
						}
						if mode != "still_running" {
							c.State.Running = false
						}
						return []byte(id), nil
					case "rm":
						if !reflect.DeepEqual(args, []string{"container", "rm", id}) {
							t.Fatal("remove exceeded exact owned ID")
						}
						mutations = append(mutations, "rm")
						if mode == "remove_error" {
							return nil, errors.New("private daemon diagnostic")
						}
						if mode != "absence_unconfirmed" {
							present = false
						}
						return []byte(id), nil
					}
				}
				t.Fatalf("unexpected cleanup command family: %q", args[:1])
				return nil, nil
			}
			err = storageRuntimeRemoveOwnedProxy(profile, command)
			success := mode == "valid" || mode == "absent" || mode == "already_stopped"
			if (err == nil) != success {
				t.Fatalf("cleanup status mismatch: success=%t err=%v", success, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("cleanup error exposed raw payload")
			}
			wantMutations := 0
			switch mode {
			case "valid", "remove_error", "absence_unconfirmed", "name_reoccupied":
				wantMutations = 2
			case "already_stopped", "stop_error", "still_running", "changed_after_stop":
				wantMutations = 1
			}
			if len(mutations) != wantMutations {
				t.Fatalf("unexpected cleanup effects: %v", mutations)
			}
			if success {
				before := len(mutations)
				if err := storageRuntimeRemoveOwnedProxy(profile, command); err != nil || len(mutations) != before {
					t.Fatal("owned absence was not idempotent", err)
				}
			}
		})
	}
	for _, root := range []string{"relative", "/var/lib/unowned", "/var/lib/" + storageRuntimeOwner + "-unit/../other"} {
		if _, err := storageRuntimeProxyProfileFor(root, "proxy_fixture", "server_fixture", "dest_fixture", "runner_fixture", "daemon_fixture"); err == nil {
			t.Fatal("unowned or noncanonical fixture root accepted")
		}
	}
}

type storageHelperEnvironmentDiagnostic struct {
	State            string   `json:"state"`
	EntryCount       int      `json:"entry_count"`
	Entries          []string `json:"entries"`
	EntriesTruncated bool     `json:"entries_truncated"`
	RootPathCount    int      `json:"root_path_count"`
	RootHomeCount    int      `json:"root_home_count"`
	DuplicateKeys    bool     `json:"duplicate_keys"`
	ExactOrder       bool     `json:"exact_order"`
	ExactPair        bool     `json:"exact_pair"`
}

// Project only fixed categories, never environment values or unknown key names.
func storageDiagnosticHelperEnvironment(raw json.RawMessage) storageHelperEnvironmentDiagnostic {
	result := storageHelperEnvironmentDiagnostic{State: "invalid_or_bounded"}
	if len(raw) > 16<<10 {
		return result
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return result
	}
	var environment []string
	if json.Unmarshal(raw, &environment) != nil || len(environment) > 64 {
		return result
	}
	result.State = "valid_array"
	result.EntryCount = len(environment)
	seen := map[string]bool{}
	for _, entry := range environment {
		key, value, formed := strings.Cut(entry, "=")
		category := "malformed"
		if formed && key != "" {
			if seen[key] {
				result.DuplicateKeys = true
			}
			seen[key] = true
			switch key {
			case "PATH":
				switch value {
				case "/":
					category = "path_root"
					result.RootPathCount++
				case "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin":
					category = "path_image_default"
				default:
					category = "path_other"
				}
			case "HOME":
				if value == "/" {
					category = "home_root"
					result.RootHomeCount++
				} else {
					category = "home_other"
				}
			default:
				category = "other_key"
			}
		}
		if len(result.Entries) < 8 {
			result.Entries = append(result.Entries, category)
		} else {
			result.EntriesTruncated = true
		}
	}
	result.ExactPair = len(environment) == 2 && result.RootPathCount == 1 && result.RootHomeCount == 1
	result.ExactOrder = len(environment) == 2 && environment[0] == "PATH=/" && environment[1] == "HOME=/"
	return result
}

func testStorageDiagnosticHelperEnvironment(t *testing.T) {
	const secret = "private_environment_canary"
	for _, tc := range []struct {
		name                   string
		value                  any
		pair, order, duplicate bool
	}{
		{"canonical", []string{"PATH=/", "HOME=/"}, true, true, false},
		{"reversed", []string{"HOME=/", "PATH=/"}, true, false, false},
		{"empty", []string{}, false, false, false},
		{"missing", []string{"PATH=/"}, false, false, false},
		{"duplicate", []string{"PATH=/", "PATH=/"}, false, false, true},
		{"extra", []string{"PATH=/", "HOME=/", secret + "=" + secret}, false, false, false},
		{"duplicate_unknown", []string{secret + "=" + secret, secret + "=changed"}, false, false, true},
		{"changed_path", []string{"PATH=" + secret, "HOME=/"}, false, false, false},
		{"changed_home", []string{"PATH=/", "HOME=" + secret}, false, false, false},
		{"image_default", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/"}, false, false, false},
		{"malformed", []string{secret, "=" + secret}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			summary := storageDiagnosticHelperEnvironment(raw)
			encoded, err := json.Marshal(summary)
			if err != nil || strings.Contains(string(encoded), secret) || summary.State != "valid_array" || summary.ExactPair != tc.pair || summary.ExactOrder != tc.order || summary.DuplicateKeys != tc.duplicate {
				t.Fatal("environment diagnostic changed closed classification or leaked payload")
			}
			for _, category := range summary.Entries {
				switch category {
				case "path_root", "path_image_default", "path_other", "home_root", "home_other", "other_key", "malformed":
				default:
					t.Fatal("unapproved environment diagnostic category")
				}
			}
		})
	}
	for _, raw := range []string{"{", `null`, `{"PATH":"` + secret + `"}`, `[1]`, strings.Repeat(" ", (16<<10)+1), strings.Repeat(" ", 16<<10) + `[]`, `["` + strings.Repeat("x", 16<<10) + `"]`, `[` + strings.Repeat(`"OTHER=private",`, 64) + `"OTHER=private"]`} {
		if summary := storageDiagnosticHelperEnvironment(json.RawMessage(raw)); summary.State != "invalid_or_bounded" || len(summary.Entries) != 0 {
			t.Fatal("malformed or unbounded environment diagnostic accepted")
		}
	}
	raw, _ := json.Marshal([]string{"PATH=/", "HOME=/", "X=" + secret, "Y=" + secret, "Z=" + secret, "A=" + secret, "B=" + secret, "C=" + secret, "D=" + secret})
	summary := storageDiagnosticHelperEnvironment(raw)
	encoded, _ := json.Marshal(summary)
	if summary.EntryCount != 9 || !summary.EntriesTruncated || len(summary.Entries) != 8 || strings.Contains(string(encoded), secret) {
		t.Fatal("environment projection bound or secrecy failed")
	}
}

// Use the same payload constructor for the physical fixture and the public-decoder contract.
func storageRemoteRuntimeRestoreRequest(t *testing.T, transferID string, revision int64, name string) domain.HostingStorageRestoreInput {
	t.Helper()
	return domain.HostingStorageRestoreInput{RequestID: storageRemoteRuntimeRequestID(t), Name: name, RemoteSource: &domain.BackupRemoteSource{TransferID: transferID, ExpectedTargetRevision: revision}}
}

func testStorageRemoteRuntimeRestoreRequest(t *testing.T) {
	t.Helper()
	const transferID = "storage_upload_hsb_ea81121f8616f6636c6bbfc9a8f71a73"
	payload := storageRemoteRuntimeRestoreRequest(t, transferID, 3, "blocked-corrupt")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw []byte) (domain.HostingStorageRestoreInput, bool, int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/storage-backups/hsb_ea81121f8616f6636c6bbfc9a8f71a73/restores", bytes.NewReader(raw))
		var result domain.HostingStorageRestoreInput
		ok := decodeStorageArchive(recorder, request, &result)
		return result, ok, recorder.Code
	}
	result, ok, _ := decode(raw)
	if !ok || !reflect.DeepEqual(result, payload) {
		t.Fatal("actual remote restore fixture payload rejected by public decoder")
	}
	valid := string(raw)
	cases := map[string]string{
		"48 character request": strings.Replace(valid, payload.RequestID, strings.Repeat("a", 48), 1),
		"unknown root":         strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		"duplicate root":       strings.TrimSuffix(valid, "}") + `,"request_id":"` + payload.RequestID + `"}`,
		"case alias":           strings.Replace(valid, `"request_id"`, `"Request_ID"`, 1),
		"unknown nested":       strings.Replace(valid, `"expected_target_revision":3`, `"expected_target_revision":3,"unknown":true`, 1),
		"duplicate nested":     strings.Replace(valid, `"expected_target_revision":3`, `"expected_target_revision":3,"expected_target_revision":3`, 1),
		"invalid revision":     strings.Replace(valid, `"expected_target_revision":3`, `"expected_target_revision":0`, 1),
		"empty transfer":       strings.Replace(valid, transferID, "", 1),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok, status := decode([]byte(invalid)); ok || status != http.StatusBadRequest {
				t.Fatalf("invalid request accepted or wrong status: accepted=%v status=%d", ok, status)
			}
		})
	}
}

func storageRemoteRuntimeRequestID(t *testing.T) string {
	t.Helper()
	var request [16]byte
	if _, err := rand.Read(request[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(request[:])
}

func storageRemoteRuntimeBackupRequest(t *testing.T, generation int64, target string, revision int64) domain.HostingStorageBackupInput {
	t.Helper()
	return domain.HostingStorageBackupInput{RequestID: storageRemoteRuntimeRequestID(t), ExpectedResourceGeneration: generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target, Revision: revision}}
}

func storageRemoteRuntimeRetryRequest(t *testing.T, transferID string) domain.HostingStorageBackupTransferRetryInput {
	t.Helper()
	var request [24]byte
	if _, err := rand.Read(request[:]); err != nil {
		t.Fatal(err)
	}
	return domain.HostingStorageBackupTransferRetryInput{RequestID: hex.EncodeToString(request[:]), ExpectedTransferID: transferID}
}

func testStorageRemoteRuntimeRequestContracts(t *testing.T) {
	t.Run("restore", testStorageRemoteRuntimeRestoreRequest)
	t.Run("backup", func(t *testing.T) {
		payload := storageRemoteRuntimeBackupRequest(t, 2, "target_storage", 3)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		decode := func(raw []byte) bool {
			recorder := httptest.NewRecorder()
			var result domain.HostingStorageBackupInput
			return decodeStorageArchive(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/resources/resource_storage/storage-backups", bytes.NewReader(raw)), &result) && result.Validate() == nil
		}
		if !decode(raw) {
			t.Fatal("actual remote backup fixture payload rejected by public decoder")
		}
		valid := string(raw)
		for name, invalid := range map[string]string{
			"48 character request": strings.Replace(valid, payload.RequestID, strings.Repeat("a", 48), 1),
			"zero generation":      strings.Replace(valid, `"expected_resource_generation":2`, `"expected_resource_generation":0`, 1),
			"zero revision":        strings.Replace(valid, `"revision":3`, `"revision":0`, 1),
			"unknown":              strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
			"duplicate":            strings.TrimSuffix(valid, "}") + `,"request_id":"` + payload.RequestID + `"}`,
			"case alias":           strings.Replace(valid, `"request_id"`, `"Request_ID"`, 1),
		} {
			t.Run(name, func(t *testing.T) {
				if decode([]byte(invalid)) {
					t.Fatal("invalid backup accepted")
				}
			})
		}
	})
	t.Run("retry", func(t *testing.T) {
		payload := storageRemoteRuntimeRetryRequest(t, "storage_upload_hsb_ea81121f8616f6636c6bbfc9a8f71a73")
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		decode := func(raw []byte) bool {
			recorder := httptest.NewRecorder()
			var result domain.HostingStorageBackupTransferRetryInput
			return decodeDatabaseBackup(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/storage-backups/backup_storage/transfers/retry", bytes.NewReader(raw)), &result) && result.Validate() == nil
		}
		if len(payload.RequestID) != 48 || !decode(raw) {
			t.Fatal("actual retry fixture payload rejected or identity semantics changed")
		}
		valid := string(raw)
		for name, invalid := range map[string]string{
			"empty request":  strings.Replace(valid, payload.RequestID, "", 1),
			"empty transfer": strings.Replace(valid, payload.ExpectedTransferID, "", 1),
			"unknown":        strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
			"duplicate":      strings.TrimSuffix(valid, "}") + `,"request_id":"` + payload.RequestID + `"}`,
			"case alias":     strings.Replace(valid, `"request_id"`, `"Request_ID"`, 1),
		} {
			t.Run(name, func(t *testing.T) {
				if decode([]byte(invalid)) {
					t.Fatal("invalid retry accepted")
				}
			})
		}
	})
}
