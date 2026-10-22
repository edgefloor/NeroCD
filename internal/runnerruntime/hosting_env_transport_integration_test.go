package runnerruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHostingEnvTransportLinuxDocker(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_ENV_TRANSPORT_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-env-transport/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("qualification requires the Linux Docker CLI")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	e := testImageEngine(t)
	info, err := e.docker(ctx, "info", "--format", "{{.OSType}}")
	if err != nil || strings.TrimSpace(string(info)) != "linux" {
		t.Fatal("qualification requires a Linux Docker daemon")
	}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		t.Fatal("daemon identity unavailable")
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		t.Fatal("fixture owner unavailable")
	}
	owner := "env-transport-" + hex.EncodeToString(token)
	label := "io.nerocd.runtime-hosting-env-transport.owner"
	imageTag := owner + ":scratch"
	// Empty scratch filesystem and fixed inert command. No container is started.
	command := exec.CommandContext(ctx, "docker", "--host", e.endpoint, "--config", e.configDir, "image", "import", "--change", `CMD ["/unused"]`, "--change", "LABEL "+label+"="+owner, "-", imageTag)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.configDir, "DOCKER_CONFIG=" + e.configDir, "DOCKER_CONTEXT="}
	command.Stdin = bytes.NewReader(make([]byte, 1024))
	imported, err := command.Output()
	if err != nil {
		t.Fatal("scratch image import failed")
	}
	imageID := strings.TrimSpace(string(imported))
	if !hostingEnvironmentImageID.MatchString(imageID) {
		t.Fatal("invalid scratch image identity")
	}
	containers := []string{}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		for _, id := range containers {
			got, inspectErr := e.inspectContainer(cleanup, id)
			if inspectErr != nil || got.ID != id || got.Config.Labels[label] != owner {
				t.Error("owned fixture container identity unavailable")
				continue
			}
			if _, err := e.docker(cleanup, "container", "rm", "--force", "--volumes", id); err != nil {
				t.Error("owned fixture container cleanup failed")
			}
		}
		raw, inspectErr := e.docker(cleanup, "image", "inspect", imageID, "--format", "{{.Id}} {{index .Config.Labels \""+label+"\"}}")
		if inspectErr != nil || strings.TrimSpace(string(raw)) != imageID+" "+owner {
			t.Error("owned scratch image identity unavailable")
		} else if _, err := e.docker(cleanup, "image", "rm", imageTag); err != nil {
			t.Error("owned scratch image cleanup failed")
		}
	})
	create := func(name string, values map[string][]byte) string {
		t.Helper()
		id, createErr := e.createWithRuntimeEnvironment(ctx, daemon, []string{"--name", owner + name, "--restart", "no", "--label", label + "=" + owner, "--pull=never"}, imageID, values)
		if createErr != nil {
			t.Fatal("protected container create failed")
		}
		containers = append(containers, id)
		return id
	}
	sentinel := create("-sentinel", nil)
	before, err := e.docker(ctx, "container", "inspect", sentinel)
	if err != nil {
		t.Fatal("sentinel inspection failed")
	}
	values := map[string][]byte{
		"APP_EMPTY": {}, "APP_SPACE": []byte("has spaces"), "APP_HASH": []byte("#literal#"),
		"APP_EQUALS": []byte("a=b=c"), "APP_UNICODE": []byte("árvíztűrő 東京"),
		"APP_DOLLAR": []byte("$HOME ${DOCKER_HOST} $$"), "APP_WHITESPACE": []byte(" \t leading and trailing \t "),
	}
	id := create("-payload", values)
	raw, err := e.docker(ctx, "container", "inspect", id, "--format", "{{json .Config.Env}}")
	if err != nil {
		t.Fatal("environment inspection failed")
	}
	var entries []string
	if json.Unmarshal(raw, &entries) != nil || len(entries) != len(values) {
		t.Fatal("Docker environment cardinality mismatch")
	}
	actual := map[string][]byte{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatal("Docker environment entry invalid")
		}
		actual[name] = []byte(value)
	}
	// Equal treats empty nil/non-nil slices differently, so compare bytes.
	for name, expected := range values {
		if got, present := actual[name]; !present || !bytes.Equal(got, expected) {
			t.Fatalf("Docker changed literal environment case %s", name)
		}
	}
	container, err := e.inspectContainer(ctx, id)
	if err != nil || container.State.Running {
		t.Fatal("qualification unexpectedly started workload")
	}
	after, err := e.docker(ctx, "container", "inspect", sentinel)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("qualification mutated another fixture target")
	}
	files, err := os.ReadDir(e.configDir)
	if err != nil || len(files) != 1 || files[0].Name() != "config.json" {
		t.Fatal("transport wrote unexpected config files")
	}
	config, err := os.ReadFile(filepath.Join(e.configDir, "config.json"))
	if err != nil || string(config) != "{}\n" {
		t.Fatal("isolated Docker config changed")
	}
	t.Log("Linux Docker stdin transport preserved all literal values; stopped target and sentinel intact; no payload files")
}
