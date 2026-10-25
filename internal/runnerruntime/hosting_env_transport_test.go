//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestAuthorizedRuntimeEnvironmentCreateRechecksAfterInspection(t *testing.T) {
	for _, mode := range []string{"revoked", "canceled", "nil_callback"} {
		t.Run(mode, func(t *testing.T) {
			// No executable is available even if a regression reaches Run. The
			// mocked command below serves read-only daemon inspection only.
			t.Setenv("PATH", t.TempDir())
			e := testImageEngine(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inspected, authorized, callbacks := false, true, 0
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if strings.Join(args[4:], " ") != "info --format {{json .ID}}" {
					t.Fatal("unexpected process command")
				}
				inspected, authorized = true, false
				if mode == "canceled" {
					cancel()
				}
				return []byte(`"daemon"`), nil
			})
			before := func(ctx context.Context) error {
				callbacks++
				if !inspected {
					t.Fatal("authority checked before final inspection")
				}
				if !authorized || ctx.Err() != nil {
					return errors.New("private-canary-value")
				}
				return nil
			}
			if mode == "nil_callback" {
				before = nil
			}
			id, err := e.createWithAuthorizedRuntimeEnvironment(ctx, "daemon", []string{"--name", "owned"}, "sha256:"+strings.Repeat("a", 64), map[string][]byte{"PASSWORD": []byte("private-canary-value")}, before)
			if err == nil || id != "" || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe protected create outcome", err)
			}
			if mode == "revoked" && (!inspected || callbacks != 1) {
				t.Fatal("revocation missed final boundary")
			}
			if mode == "canceled" && (!inspected || callbacks != 0) {
				t.Fatal("canceled inspection reached admission")
			}
			if mode == "nil_callback" && (inspected || callbacks != 0) {
				t.Fatal("missing authority reached Docker inspection")
			}
		})
	}
}

// The test binary impersonates docker without putting fixture values in argv or
// environment. Only stdin carries the payload. No helper writes its input.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "docker" {
		os.Exit(hostingEnvironmentHelper())
	}
	os.Exit(m.Run())
}

func hostingEnvironmentHelper() int {
	if len(os.Args) > 1 && os.Args[1] == "descendant" {
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0o600)
		for {
			time.Sleep(time.Second)
		}
	}
	mode := ""
	for i, arg := range os.Args {
		if arg == "--name" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if strings.HasPrefix(mode, "early-parent-") {
		child := exec.Command(os.Args[0], "descendant", "descendant.pid")
		if mode == "early-parent-exit" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if child.Start() != nil {
			return 2
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat("descendant.pid"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return 2
			}
			time.Sleep(10 * time.Millisecond)
		}
		switch mode {
		case "early-parent-failure":
			return 1
		case "early-parent-invalid":
			if _, err := fmt.Fprintln(os.Stdout, "invalid"); err != nil {
				return 6
			}
		default:
			if _, err := fmt.Fprintln(os.Stdout, strings.Repeat("a", 64)); err != nil {
				return 6
			}
		}
		return 0
	}
	if mode == "blocked" {
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "descendant", "descendant.pid")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			return 2
		}
		_ = os.WriteFile("started", []byte("ready"), 0o600)
		for {
			time.Sleep(time.Second)
		}
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, hostingEnvironmentInputLimit+1))
	if err != nil || len(body) > hostingEnvironmentInputLimit {
		return 3
	}
	var previous struct{ Launches int }
	if raw, readErr := os.ReadFile("observation.json"); readErr == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	observation, _ := json.Marshal(struct {
		Args, Env []string
		Launches  int
	}{os.Args[1:], os.Environ(), previous.Launches + 1})
	if os.WriteFile("observation.json", observation, 0o600) != nil {
		return 4
	}
	switch mode {
	case "echo-failure":
		_, _ = os.Stdout.Write(body)
		_, _ = os.Stderr.Write(body)
		return 1
	case "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("a"), 129))
	case "invalid":
		_, _ = os.Stdout.Write(body)
	default:
		connected := strings.Contains(mode, "_appdb_") && (string(body) == "PGDATABASE=private-canary-value\n" || string(body) == "REDIS_HOST=private-canary-value\n")
		if !connected && string(body) != "APP_EMPTY=\nAPP_SECRET=fixture-value-$#= Unicode-árvíz  \n" {
			return 5
		}
		if _, err := fmt.Fprintln(os.Stdout, strings.Repeat("a", 64)); err != nil {
			return 6
		}
	}
	return 0
}

func TestHostingEnvTransportEncoding(t *testing.T) {
	values := map[string][]byte{"Z": []byte("  a # = $ árvíz  "), "EMPTY": {}}
	body, err := encodeHostingEnvironment(values)
	if err != nil || string(body) != "EMPTY=\nZ=  a # = $ árvíz  \n" {
		t.Fatal("environment encoding changed literal values")
	}
	body[0] = 'X'
	if string(values["Z"]) != "  a # = $ árvíz  " {
		t.Fatal("transport modified caller data")
	}
	for _, name := range []string{"", "1ABC", "A=B", "A-B", "A\nB", "NEROCD_VALUE", strings.Repeat("A", 129), "Á"} {
		if _, err := encodeHostingEnvironment(map[string][]byte{name: []byte("value")}); err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	for _, value := range [][]byte{[]byte("a\x00b"), []byte("a\rb"), []byte("a\nb"), {0xff}, bytes.Repeat([]byte("x"), 4097)} {
		if _, err := encodeHostingEnvironment(map[string][]byte{"APP": value}); err == nil {
			t.Fatal("invalid value accepted")
		}
	}
	values = map[string][]byte{}
	for i := range 64 {
		values[fmt.Sprintf("KEY_%02d", i)] = bytes.Repeat([]byte("x"), 4096)
	}
	if _, err := encodeHostingEnvironment(values); err == nil {
		t.Fatal("encoded total budget ignored")
	}
	for name := range values {
		values[name] = bytes.Repeat([]byte("x"), 4088)
	}
	if body, err := encodeHostingEnvironment(values); err != nil || len(body) != hostingEnvironmentInputLimit {
		t.Fatal("exact encoded total budget rejected")
	}
	values["KEY_00"] = append(values["KEY_00"], 'x')
	if _, err := encodeHostingEnvironment(values); err == nil {
		t.Fatal("one byte beyond total budget accepted")
	}
	for name := range values {
		values[name] = nil
	}
	if _, err := encodeHostingEnvironment(values); err != nil {
		t.Fatal("64 entries rejected")
	}
	values["EXTRA"] = nil
	if _, err := encodeHostingEnvironment(values); err == nil {
		t.Fatal("entry budget ignored")
	}
}

func installHostingEnvironmentTestDocker(t *testing.T) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Symlink(binary, filepath.Join(dir, "docker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func hostingEnvironmentTestEngine(t *testing.T) *imageEngine {
	t.Helper()
	installHostingEnvironmentTestDocker(t)
	e := testImageEngine(t)
	e.command = imageCommandFunc(func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte(`"qualified-daemon"`), nil
	})
	return e
}

func TestHostingOwnedStorageEnvTransportRejectsUnscopedOrChangedMounts(t *testing.T) {
	for _, mode := range []string{"success", "foreign_volume", "changed_origin", "changed_nonce", "changed_daemon", "bind_source", "changed_target", "permission", "raw_mount", "extra_mount", "bind_mount", "short_volume", "equals_mount", "nil_authority", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			e := hostingEnvironmentTestEngine(t)
			storage := hostingEnvironmentTestStorage(t)
			req := storage.Volume
			flags := make([]string, 2, 8)
			copy(flags, []string{"--name", "success"})
			backing := flags[:cap(flags)]
			authorizations := 0
			before := func(ctx context.Context) error {
				authorizations++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
					t.Error("protected create lost bounded lifetime")
				}
				if mode == "revoked" {
					return errors.New("private-canary-value")
				}
				return ctx.Err()
			}
			switch mode {
			case "foreign_volume":
				storage.Plan.VolumeName = "foreign"
			case "changed_origin":
				storage.Volume.OriginOperationID = "foreign"
			case "changed_nonce":
				storage.Plan.CreationNonce, storage.Volume.CreationNonce = strings.Repeat("f", 32), strings.Repeat("f", 32)
			case "changed_daemon":
				storage.Volume.DaemonID = "foreign"
			case "bind_source":
				storage.Volume.Storage.VolumeName = "/tmp"
			case "changed_target":
				storage.Plan.MountPath = "/other"
			case "permission":
				storage.Volume.CreatePermitted = true
			case "raw_mount":
				flags = append(flags, "--mount", "type=volume,src="+req.Storage.VolumeName+",dst=/data/uploads,volume-nocopy")
			case "extra_mount":
				flags = append(flags, "--mount", "type=volume,src="+req.Storage.VolumeName+",dst=/data/uploads,volume-nocopy", "--mount", "type=volume,src=foreign,dst=/extra,volume-nocopy")
			case "bind_mount":
				flags = append(flags, "--mount", "type=bind,src=/tmp,dst=/data/uploads")
			case "short_volume":
				flags = append(flags, "-v", "owned:/data/uploads")
			case "equals_mount":
				flags = append(flags, "--mount=type=volume,src=owned,dst=/data/uploads")
			case "nil_authority":
				before = nil
			}
			if mode == "raw_mount" {
				if _, err := e.createWithAuthorizedRuntimeEnvironment(t.Context(), req.DaemonID, flags, "sha256:"+strings.Repeat("b", 64), nil, before); err == nil {
					t.Fatal("unscoped transport accepted a canonical-looking mount")
				}
			}
			id, err := e.createWithAuthorizedOwnedStorageRuntimeEnvironment(t.Context(), req.DaemonID, flags, "sha256:"+strings.Repeat("b", 64), map[string][]byte{"APP_EMPTY": {}, "APP_SECRET": []byte("fixture-value-$#= Unicode-árvíz  ")}, storage, before)
			raw, readErr := os.ReadFile(filepath.Join(e.configDir, "observation.json"))
			expectedAuthorizations := 0
			if mode == "revoked" {
				expectedAuthorizations = 1
			}
			if mode == "success" {
				if err != nil || id != strings.Repeat("a", 64) || authorizations != 1 || readErr != nil || strings.Count(string(raw), "--mount") != 1 {
					t.Fatal("exact owned mount did not reach one authorized process", err)
				}
				for _, value := range backing[2:] {
					if value != "" {
						t.Fatal("transport appended mount into caller-owned backing array")
					}
				}
			} else if err == nil || id != "" || !os.IsNotExist(readErr) || strings.Contains(err.Error(), "private-canary") || authorizations != expectedAuthorizations {
				t.Fatal("invalid scope or revoked authority reached protected process", err)
			}
		})
	}
}

func hostingEnvironmentTestStorage(t *testing.T) hostingEnvironmentOwnedStorage {
	t.Helper()
	req := ownedVolumeRequest(t)
	req.DaemonID = "qualified-daemon"
	req.CreationNonce = domain.HostingStorageCreationNonce(req.Storage.Reference(), req.OriginOperationID, req.DaemonID)
	return hostingEnvironmentOwnedStorage{Volume: req, Plan: domain.HostingStoragePlan{Reference: req.Storage.Reference(), VolumeName: req.Storage.VolumeName, MountPath: req.Storage.MountPath, CreationOperationID: req.OriginOperationID, CreationNonce: req.CreationNonce, DaemonID: req.DaemonID}}
}

func hostingEnvironmentTestCreate(t *testing.T, e *imageEngine, owned bool) func(context.Context, string, []string, string, map[string][]byte) (string, error) {
	t.Helper()
	if !owned {
		return e.createWithRuntimeEnvironment
	}
	storage := hostingEnvironmentTestStorage(t)
	return func(ctx context.Context, daemon string, flags []string, image string, values map[string][]byte) (string, error) {
		return e.createWithAuthorizedOwnedStorageRuntimeEnvironment(ctx, daemon, flags, image, values, storage, func(current context.Context) error { return current.Err() })
	}
}

func TestHostingEnvTransportCommandContainment(t *testing.T) {
	e := hostingEnvironmentTestEngine(t)
	secret := "fixture-value-$#= Unicode-árvíz  "
	t.Setenv("AMBIENT_SECRET", secret)
	t.Setenv("DOCKER_AUTH_CONFIG", secret)
	t.Setenv("NEROCD_COMPOSE_TRACE", secret)
	values := map[string][]byte{"APP_SECRET": []byte(secret), "APP_EMPTY": {}}
	image := "sha256:" + strings.Repeat("b", 64)
	for _, mode := range []string{"success", "echo-failure", "overflow", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			id, err := e.createWithRuntimeEnvironment(t.Context(), "qualified-daemon", []string{"--name", mode, "--restart", "no", "--publish", "127.0.0.1:18080:80", "--label", "fixture=unit", "--pull=never"}, image, values)
			if mode == "success" {
				if err != nil || id != strings.Repeat("a", 64) {
					t.Fatal("valid container ID was not returned")
				}
			} else if id != "" || err == nil || err.Error() != "hosting runtime environment container create failed" {
				t.Fatal("failed create exposed an unexpected result")
			}
			observation, readErr := os.ReadFile(filepath.Join(e.configDir, "observation.json"))
			if readErr != nil || bytes.Contains(observation, []byte(secret)) {
				t.Fatal("payload leaked into process arguments or environment")
			}
			var got struct{ Args, Env []string }
			if json.Unmarshal(observation, &got) != nil {
				t.Fatal("invalid helper observation")
			}
			if strings.Join(got.Args[:6], "|") != "--host|"+e.endpoint+"|--config|"+e.configDir+"|container|create" || strings.Join(got.Args[len(got.Args)-3:], "|") != "--env-file|/dev/stdin|"+image {
				t.Fatal("Docker target or stdin argument changed")
			}
			for _, entry := range got.Env {
				name, _, _ := strings.Cut(entry, "=")
				switch name {
				case "PATH", "HOME", "LANG", "LC_ALL", "DOCKER_CONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CLI_HINTS":
				default:
					t.Fatal("ambient environment inherited")
				}
			}
			for _, expected := range []string{"HOME=" + e.configDir, "DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT="} {
				found := false
				for _, entry := range got.Env {
					found = found || entry == expected
				}
				if !found {
					t.Fatal("Docker environment was not pinned")
				}
			}
		})
	}
	if string(values["APP_SECRET"]) != secret {
		t.Fatal("caller input was cleared")
	}
	entries, err := os.ReadDir(e.configDir)
	if err != nil || len(entries) != 2 {
		t.Fatal("unexpected persistent transport files")
	}
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(e.configDir, entry.Name()))
		if err != nil || bytes.Contains(contents, []byte(secret)) {
			t.Fatal("persistent fixture file contains payload")
		}
	}
}

func TestHostingEnvTransportRejectsBeforeCommand(t *testing.T) {
	e := hostingEnvironmentTestEngine(t)
	image := "sha256:" + strings.Repeat("b", 64)
	for _, flags := range [][]string{{"container", "create"}, {"--env", "X=x"}, {"--env-file", "/tmp/value"}, {"--"}, {"--label"}, {"--name", "--env"}, {"--privileged"}, {"--name=bad"}} {
		if id, err := e.createWithRuntimeEnvironment(t.Context(), "qualified-daemon", flags, image, nil); err == nil || id != "" {
			t.Fatal("unexpected create argument accepted")
		}
	}
	for _, image := range []string{"alpine:latest", "--env=x", "sha256:abc"} {
		if _, err := e.createWithRuntimeEnvironment(t.Context(), "qualified-daemon", nil, image, nil); err == nil {
			t.Fatal("mutable or invalid image accepted")
		}
	}
	if _, err := e.createWithRuntimeEnvironment(t.Context(), "qualified-daemon", nil, image, map[string][]byte{"APP": []byte("bad\nvalue")}); err == nil {
		t.Fatal("invalid input reached command")
	}
	if _, err := e.createWithRuntimeEnvironment(t.Context(), "different-daemon", nil, image, nil); err == nil {
		t.Fatal("daemon mismatch reached mutation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.createWithRuntimeEnvironment(ctx, "qualified-daemon", nil, image, nil); err == nil {
		t.Fatal("canceled context reached command")
	}
	if _, err := os.Stat(filepath.Join(e.configDir, "observation.json")); !os.IsNotExist(err) {
		t.Fatal("rejected input launched Docker")
	}
}

func TestHostingEnvTransportCancellationKillsBlockedGroup(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprintf("owned_storage=%t", owned), func(t *testing.T) {
			testHostingEnvTransportCancellationKillsBlockedGroup(t, owned)
		})
	}
}

func testHostingEnvTransportCancellationKillsBlockedGroup(t *testing.T, owned bool) {
	e := hostingEnvironmentTestEngine(t)
	create := hostingEnvironmentTestCreate(t, e, owned)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	values := map[string][]byte{}
	for i := range 63 {
		values[fmt.Sprintf("APP_%02d", i)] = bytes.Repeat([]byte("x"), 4096)
	}
	go func() {
		_, err := create(ctx, "qualified-daemon", []string{"--name", "blocked"}, "sha256:"+strings.Repeat("b", 64), values)
		result <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		pid, err := os.ReadFile(filepath.Join(e.configDir, "descendant.pid"))
		if err == nil {
			childPID, _ = strconv.Atoi(string(pid))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		t.Fatal("descendant never reached started handshake")
	}
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil || time.Since(started) > 4*time.Second {
			t.Fatal("cancellation did not return bounded failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation leaked blocked command")
	}
	// A killed child can briefly remain a zombie until the OS reaps it. On
	// Linux verify that state explicitly; elsewhere signal 0 must fail.
	for time.Now().Before(deadline.Add(3 * time.Second)) {
		err := syscall.Kill(childPID, 0)
		state, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPID))
		if err == syscall.ESRCH || bytes.Contains(state, []byte(") Z ")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived process group cancellation")
}

func TestHostingEnvTransportEarlyParentExitKillsOwnedDescendant(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprintf("owned_storage=%t", owned), func(t *testing.T) {
			testHostingEnvTransportEarlyParentExitKillsOwnedDescendant(t, owned)
		})
	}
}

func testHostingEnvTransportEarlyParentExitKillsOwnedDescendant(t *testing.T, owned bool) {
	for _, mode := range []string{"early-parent-exit", "early-parent-failure", "early-parent-invalid"} {
		t.Run(mode, func(t *testing.T) {
			e := hostingEnvironmentTestEngine(t)
			create := hostingEnvironmentTestCreate(t, e, owned)
			var pid int
			defer func() {
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			id, err := create(ctx, "qualified-daemon", []string{"--name", mode}, "sha256:"+strings.Repeat("b", 64), nil)
			raw, readErr := os.ReadFile(filepath.Join(e.configDir, "descendant.pid"))
			if readErr != nil {
				t.Fatal("owned descendant handshake missing")
			}
			pid, _ = strconv.Atoi(string(raw))
			if pid < 1 {
				t.Fatal("invalid owned descendant identity")
			}
			if id != "" || err == nil || err.Error() != "hosting runtime environment container create failed" || ctx.Err() != nil {
				t.Fatal("early parent exit did not return fixed uncertainty with live context")
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				state, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if syscall.Kill(pid, 0) == syscall.ESRCH || bytes.Contains(state, []byte(") Z ")) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("owned descendant survived uncertain parent exit")
		})
	}
}
