package hostingbuild

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRuntimeBuild runs only in the disposable Linux DinD acceptance fixture.
// Never point it at a user daemon: it intentionally exercises build failures.
func TestRuntimeBuild(t *testing.T) {
	if os.Getenv("NEROCD_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-build/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Linux required")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: buildxChecksum(runtime.GOARCH), StateDir: dir, CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE")})
	if err != nil {
		t.Fatal(err)
	}
	setup := t.TempDir()
	if err = os.Mkdir(filepath.Join(setup, "docker"), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := e.docker(t.Context(), setup, "info", "--format", "{{.ID}}")
	if err != nil {
		t.Fatal(err)
	}
	daemon := strings.TrimSpace(string(b))
	platform := "linux/" + runtime.GOARCH
	// Provision the pinned probe separately so pull diagnostics cannot be mistaken for HTTP content.
	if _, err = e.docker(t.Context(), setup, "pull", StaticImage); err != nil {
		t.Fatal(err)
	}
	source := func(content string) string {
		p := t.TempDir()
		writeTestFile(t, filepath.Join(p, "Dockerfile"), []byte(content))
		return p
	}
	request := func(id string, strategy Strategy, src string) Request {
		return Request{BuildID: id, Checkout: src, SourceID: "immutable-commit", InputID: "fixture-v1", DaemonID: daemon, Strategy: strategy, Platform: platform, Limits: testLimits()}
	}
	assertHTTP := func(t *testing.T, name, expected string) {
		t.Helper()
		var response []byte
		var requestErr error
		for i := 0; i < 30; i++ {
			response, requestErr = e.docker(t.Context(), setup, "run", "--pull=never", "--rm", "--label", "io.nerocd.runtime-build=client", "--network", "container:"+name, "--entrypoint", "/bin/busybox", StaticImage, "wget", "-T", "2", "-qO-", "http://127.0.0.1:80")
			if requestErr == nil {
				break
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		if requestErr != nil || string(response) != expected {
			t.Fatalf("HTTP content for %s: got %q expected %q error=%v", name, response, expected, requestErr)
		}
	}
	var built Result
	t.Run("dockerfile", func(t *testing.T) {
		src := source("FROM " + StaticImage + "\nRUN echo descendant-enforcement > /usr/share/nginx/html/index.html && sleep 2\n")
		req := request("dockerfile", Dockerfile, src)
		var err error
		built, err = e.Build(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if built.Origin != "local_build" || !digestPattern.MatchString(built.ImageID) {
			t.Fatalf("invalid receipt %#v", built)
		}
		t.Logf("receipt=%+v", built)
		again, err := e.Build(t.Context(), req)
		if err != nil || again != built {
			t.Fatalf("immutable replay failed: %v", err)
		}
		writeTestFile(t, filepath.Join(src, "Dockerfile"), []byte("FROM scratch\n"))
		if _, err = e.Build(t.Context(), req); err == nil {
			t.Fatal("changed completed build source accepted")
		}
	})
	if built.ImageID == "" {
		t.FailNow()
	}
	// Independent workload is deliberately kept alive through subsequent failures.
	const survivor = "nerocd-runtime-build-survivor"
	if _, err = e.docker(t.Context(), setup, "run", "--detach", "--name", survivor, "--label", "io.nerocd.runtime-build=survivor", built.ImageID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = e.docker(ctx, setup, "rm", "--force", survivor)
	})
	t.Run("dockerfile-http-and-survivor-before-failures", func(t *testing.T) { assertHTTP(t, survivor, "descendant-enforcement\n") })

	t.Run("nested-context-custom-Dockerfile-target", func(t *testing.T) {
		src := t.TempDir()
		makeSettingDir(t, filepath.Join(src, "apps", "web", "docker"))
		writeTestFile(t, filepath.Join(src, "private.txt"), []byte("outside context"))
		content := "FROM " + StaticImage + " AS chosen\nCOPY marker /usr/share/nginx/html/index.html\nFROM " + StaticImage + " AS other\nRUN echo wrong-target > /usr/share/nginx/html/index.html\n"
		writeTestFile(t, filepath.Join(src, "apps", "web", "docker", "Release.Dockerfile"), []byte(content))
		writeTestFile(t, filepath.Join(src, "apps", "web", "marker"), []byte("selected-target\n"))
		req := request("nested-dockerfile", Dockerfile, src)
		req.ContextDir = "apps/web"
		req.DockerfilePath = "docker/Release.Dockerfile"
		req.BuildTarget = "chosen"
		result, err := e.Build(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		name := "nerocd-runtime-build-settings"
		if _, err = e.docker(t.Context(), setup, "run", "--detach", "--name", name, result.ImageID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = e.docker(t.Context(), setup, "rm", "--force", name) }()
		assertHTTP(t, name, "selected-target\n")
		changed := req
		changed.BuildTarget = "other"
		if _, err = e.Build(t.Context(), changed); err == nil {
			t.Fatal("changed target reused completed ID")
		}
		writeTestFile(t, filepath.Join(src, "apps", "web", "docker", "Other.Dockerfile"), []byte(content))
		changed = req
		changed.DockerfilePath = "docker/Other.Dockerfile"
		if _, err = e.Build(t.Context(), changed); err == nil {
			t.Fatal("changed Dockerfile reused completed ID")
		}
		failed := req
		failed.BuildID = "settings-failed-target"
		failed.BuildTarget = "absent-stage"
		if _, err = e.Build(t.Context(), failed); err == nil {
			t.Fatal("missing stage build succeeded")
		}
		assertHTTP(t, survivor, "descendant-enforcement\n")
		invalid := req
		invalid.BuildID = "settings-invalid"
		invalid.ContextDir = "../outside"
		if _, err = e.Build(t.Context(), invalid); err == nil {
			t.Fatal("invalid context admitted")
		}
		if _, err = os.Stat(filepath.Join(dir, invalid.BuildID)); !os.IsNotExist(err) {
			t.Fatal("invalid settings created build state")
		}
	})
	t.Run("prepared-static-nested-SPA", func(t *testing.T) {
		src := t.TempDir()
		publish := filepath.Join(src, "frontend", "dist")
		makeSettingDir(t, publish)
		writeTestFile(t, filepath.Join(publish, "index.html"), []byte("prepared-spa\n"))
		writeTestFile(t, filepath.Join(publish, "bundle.js"), []byte("public-bundle\n"))
		writeTestFile(t, filepath.Join(publish, "Dockerfile"), []byte("private instructions"))
		writeTestFile(t, filepath.Join(publish, "nginx.conf"), []byte("private config"))
		writeTestFile(t, filepath.Join(src, "private.txt"), []byte("outside publish"))
		req := request("nested-static-spa", Static, src)
		req.ContextDir = "frontend"
		req.PublishDir = "dist"
		req.SPAFallback = true
		result, err := e.Build(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if result.ContextPolicy != staticContextPolicy {
			t.Fatal("static policy not recorded")
		}
		name := "nerocd-runtime-build-spa"
		if _, err = e.docker(t.Context(), setup, "run", "--detach", "--name", name, result.ImageID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = e.docker(t.Context(), setup, "rm", "--force", name) }()
		assertHTTP(t, name, "prepared-spa\n")
		for path, want := range map[string]string{"/account/deep/link": "prepared-spa\n", "/bundle.js": "public-bundle\n"} {
			b, err := e.docker(t.Context(), setup, "run", "--rm", "--network", "container:"+name, "--entrypoint", "/bin/busybox", StaticImage, "wget", "-T", "2", "-qO-", "http://127.0.0.1:80"+path)
			if err != nil || string(b) != want {
				t.Fatalf("SPA HTTP %s: %q %v", path, b, err)
			}
		}
		for _, path := range []string{"/missing.js", "/private.txt", "/.nerocd-static.conf", "/nginx.conf"} {
			b, err := e.docker(t.Context(), setup, "run", "--rm", "--network", "container:"+name, "--entrypoint", "/bin/busybox", StaticImage, "wget", "-T", "2", "-SO-", "http://127.0.0.1:80"+path)
			if err == nil || !strings.Contains(string(b), "404") {
				t.Fatalf("missing asset %s did not404: %s %v", path, b, err)
			}
		}
		if _, err = e.docker(t.Context(), setup, "run", "--rm", "--network", "none", "--entrypoint", "/bin/sh", result.ImageID, "-ec", "test ! -e /usr/share/nginx/html/Dockerfile && test ! -e /usr/share/nginx/html/nginx.conf && test ! -e /usr/share/nginx/html/.nerocd-static.Dockerfile && test ! -e /usr/share/nginx/html/.nerocd-static.conf && test ! -e /usr/share/nginx/html/private.txt"); err != nil {
			t.Fatal("nonpublic static files packaged")
		}
		changed := req
		changed.SPAFallback = false
		if _, err = e.Build(t.Context(), changed); err == nil {
			t.Fatal("changed SPA setting reused BuildID")
		}
		assertHTTP(t, survivor, "descendant-enforcement\n")
	})
	for _, strategy := range []Strategy{Static, Railpack} {
		t.Run(string(strategy), func(t *testing.T) {
			src := t.TempDir()
			writeTestFile(t, filepath.Join(src, "index.html"), []byte("runtime-build-"+string(strategy)))
			if strategy == Railpack {
				writeTestFile(t, filepath.Join(src, "Staticfile"), []byte("root: .\n"))
			}
			result, err := e.Build(t.Context(), request(string(strategy), strategy, src))
			if err != nil {
				t.Fatal(err)
			}
			name := "nerocd-runtime-build-" + string(strategy)
			if _, err = e.docker(t.Context(), setup, "run", "--detach", "--name", name, "--label", "io.nerocd.runtime-build="+string(strategy), "--env", "PORT=80", result.ImageID); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = e.docker(t.Context(), setup, "rm", "--force", name) }()
			assertHTTP(t, name, "runtime-build-"+string(strategy))
			if strategy == Static {
				b, err := e.docker(t.Context(), setup, "run", "--rm", "--network", "container:"+name, "--entrypoint", "/bin/busybox", StaticImage, "wget", "-T", "2", "-SO-", "http://127.0.0.1:80/deep/missing")
				if err == nil || !strings.Contains(string(b), "404") {
					t.Fatal("default static unexpectedly enables SPA fallback")
				}
			}

		})
	}
	t.Run("descendant-cpu-memory-enforcement", func(t *testing.T) {
		req := request("descendants", Dockerfile, source("FROM "+StaticImage+"\nRUN sh -c '(while :; do :; done) & worker=$!; sleep 4; kill \"$worker\"; wait \"$worker\" || true' nerocd-build-step-proof\n"))
		req.Limits.CPUQuota = 50000
		type completion struct {
			result Result
			err    error
		}
		finished := make(chan completion, 1)
		go func() { r, err := e.Build(t.Context(), req); finished <- completion{r, err} }()
		observed := false
		lastEvidence := "none"
		sawProof := false
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case done := <-finished:
				if done.err != nil {
					t.Fatal(done.err)
				}
				if !observed {
					t.Fatalf("no enforcing ancestor and throttled descendant sample observed; last=%s", lastEvidence)
				}
				return
			case <-ticker.C:
				ids, err := e.docker(t.Context(), setup, "ps", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range strings.Fields(string(ids)) {
					c, err := e.inspect(t.Context(), setup, id)
					if err != nil || !c.State.Running {
						continue
					}
					ownedGroup := filepath.Join(e.config.CgroupRoot, "build-"+c.Config.Labels["io.nerocd.hostingbuild.owner"])
					_, err = containment(c.State.PID, req.Limits, ownedGroup)
					group := groupRelative(ownedGroup)
					if err != nil {
						continue
					}
					count, err := inspectDescendants(t.Context(), c.State.PID, group, req.Limits)
					if err != nil {
						t.Fatal(err)
					}
					stat, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cpu.stat"))
					if err != nil {
						continue
					}
					lastEvidence = group + " count=" + strconv.Itoa(count) + " cpu.stat=" + string(stat)
					fields := strings.Fields(string(stat))
					throttled := false
					for i := 0; i+1 < len(fields); i += 2 {
						if fields[i] == "nr_throttled" && fields[i+1] != "0" {
							throttled = true
						}
					}
					proofPID, proofGroup := findBuildStep(t.Context(), "nerocd-build-step-proof", c.State.PID, group)
					inGroup := proofGroup == group || strings.HasPrefix(proofGroup, group+"/")
					if proofPID > 0 && !sawProof {
						sawProof = true
						t.Logf("actual RUN pid=%d cgroup=%s expected=%s count=%d cpu.stat=%s", proofPID, proofGroup, group, count, stat)
					}
					if proofPID > 0 {
						lastEvidence += " actual_RUN_pid=" + strconv.Itoa(proofPID) + " group=" + proofGroup
					}
					if proofPID > 0 && inGroup && throttled && !observed {
						cpu, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cpu.max"))
						memory, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "memory.max"))
						swap, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "memory.swap.max"))
						t.Logf("actual enforcing ancestor %s descendants=%d cpu.max=%s memory.max=%s memory.swap.max=%s cpu.stat=%s", group, count, cpu, memory, swap, stat)
						observed = true
					}
				}
			}
		}
	})
	t.Run("descendant-memory-oom-enforcement", func(t *testing.T) {
		req := request("memory-oom", Dockerfile, source("FROM "+StaticImage+"\n"+`RUN sh -c 'sleep 1; awk "BEGIN {x=1; for(i=0;i<31;i++) x=x x; print length(x)}"' nerocd-memory-proof`+"\n"))
		req.Limits.MemoryBytes = 1 << 30
		finished := make(chan error, 1)
		go func() { _, err := e.Build(t.Context(), req); finished <- err }()
		sawRUN, sawOOM := false, false
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case buildErr := <-finished:
				if buildErr == nil || !sawRUN || !sawOOM {
					t.Fatalf("memory limit proof failed: RUN=%t OOM=%t error=%v", sawRUN, sawOOM, buildErr)
				}
				return
			case <-ticker.C:
				ids, err := e.docker(t.Context(), setup, "ps", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range strings.Fields(string(ids)) {
					c, err := e.inspect(t.Context(), setup, id)
					if err != nil || !c.State.Running {
						continue
					}
					ownedGroup := filepath.Join(e.config.CgroupRoot, "build-"+c.Config.Labels["io.nerocd.hostingbuild.owner"])
					_, err = containment(c.State.PID, req.Limits, ownedGroup)
					group := groupRelative(ownedGroup)
					if err != nil {
						continue
					}
					pid, runGroup := findBuildStep(t.Context(), "nerocd-memory-proof", c.State.PID, group)
					if pid > 0 && (runGroup == group || strings.HasPrefix(runGroup, group+"/")) {
						sawRUN = true
					}
					events, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "memory.events"))
					if err != nil {
						continue
					}
					fields := strings.Fields(string(events))
					for i := 0; i+1 < len(fields); i += 2 {
						if fields[i] == "oom_kill" && fields[i+1] != "0" && !sawOOM {
							sawOOM = true
							t.Logf("actual enforcing ancestor %s memory.max=1073741824 actual_RUN_seen=%t memory.events=%s", group, sawRUN, events)
						}
					}
				}
			}
		}
	})
	t.Run("unpacked-output", func(t *testing.T) {
		req := request("unpacked", Dockerfile, source("FROM "+StaticImage+"\nRUN echo unpacked-check > /unpacked-check\n"))
		req.Limits.UnpackedBytes = 100
		if _, err = e.Build(t.Context(), req); err == nil {
			t.Fatal("oversized unpacked image accepted")
		}
	})
	t.Run("bounded-output", func(t *testing.T) {
		req := request("bounded", Dockerfile, source("FROM "+StaticImage+"\n"))
		req.Limits.ExportBytes = 100
		if _, err = e.Build(t.Context(), req); err == nil {
			t.Fatal("oversize export accepted")
		}
	})
	t.Run("timeout-cleanup", func(t *testing.T) {
		req := request("timeout", Dockerfile, source("FROM "+StaticImage+"\nRUN sleep 120\n"))
		req.Limits.Deadline = 20 * time.Second
		if _, err = e.Build(t.Context(), req); err == nil || !strings.Contains(err.Error(), "sleep 120") {
			t.Fatalf("deadline did not interrupt an actual RUN: %v", err)
		}
		if _, err = os.Stat(filepath.Join(dir, "cleanup_unconfirmed.json")); !os.IsNotExist(err) {
			t.Fatalf("cleanup unconfirmed after timeout: %v", err)
		}
	})
	t.Run("context-escape", func(t *testing.T) {
		src := source("FROM scratch\n")
		if err = os.Symlink("/etc/passwd", filepath.Join(src, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, err = e.Build(t.Context(), request("escape", Dockerfile, src)); err == nil {
			t.Fatal("context escape accepted")
		}
	})
	t.Run("lost-docker-cleanup", func(t *testing.T) {
		// Missing socket is the deterministic daemon-loss injection. This calls the
		// same cleanup path and retains the intent just as Build's deferred cleanup.
		dead := *e
		dead.config.Socket = "/tmp/nerocd-intentionally-missing.sock"
		intent := ownedResources{DaemonID: daemon, Token: "fixture", Builder: "nerocd-lost-builder", Planner: "nerocd-lost-planner", Volume: "nerocd-lost-volume"}
		marker := filepath.Join(dir, "cleanup_unconfirmed.json")
		if err = durableJSON(marker, intent); err != nil {
			t.Fatal(err)
		}
		if err = dead.cleanup(t.Context(), setup, intent); err == nil {
			t.Fatal("lost daemon cleanup confirmed")
		}
		if _, err = e.Build(t.Context(), request("blocked", Dockerfile, source("FROM scratch\n"))); !errors.Is(err, ErrCleanupUnconfirmed) {
			t.Fatalf("lost cleanup did not block: %v", err)
		}
		// Fixture-only reconciliation: no resources were created for this intent.
		if err = os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("survivor-http-after-failures", func(t *testing.T) { assertHTTP(t, survivor, "descendant-enforcement\n") })
	b, err = e.docker(t.Context(), setup, "inspect", survivor)
	if err != nil {
		t.Fatal(err)
	}
	var survivors []containerInfo
	if json.Unmarshal(b, &survivors) != nil || len(survivors) != 1 || !survivors[0].State.Running {
		t.Fatal("independent workload did not survive build failures")
	}
	b, err = e.docker(t.Context(), setup, "ps", "--all", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
	if err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("owned build resources leaked: %s %v", b, err)
	}
	b, err = e.docker(t.Context(), setup, "ps", "--all", "--quiet", "--filter", "name=^/buildx_buildkit_nerocd-")
	if err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("unexpected Buildx builder leaked: %s %v", b, err)
	}
	b, err = e.docker(t.Context(), setup, "volume", "ls", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
	if err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("owned builder volume leaked: %s %v", b, err)
	}
	groups, err := os.ReadDir(e.config.CgroupRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.IsDir() && strings.HasPrefix(group.Name(), "build-") {
			t.Fatalf("owned cgroup leaked: %s", group.Name())
		}
	}

}

// The unique argv marker is placed only on the Dockerfile's RUN process.
func findBuildStep(ctx context.Context, marker string, rootPID int, group string) (int, string) {
	processes, _ := processDescendants(ctx, rootPID)
	if processes == nil {
		processes = map[int]bool{}
	}
	budget := newScanBudget(ctx)
	_ = scanTree(budget, filepath.Join("/sys/fs/cgroup", group), func(p string, info os.FileInfo) error {
		if info.Name() != "cgroup.procs" {
			return nil
		}
		b, err := scanReadFile(budget, filepath.Join("/sys/fs/cgroup", group, p))
		if err != nil {
			return err
		}
		for _, v := range strings.Fields(string(b)) {
			if err := budget.step(); err != nil {
				return err
			}
			pid, err := strconv.Atoi(v)
			if err == nil {
				processes[pid] = true
			}
		}
		return nil
	})
	for pid := range processes {
		if err := budget.step(); err != nil {
			return 0, ""
		}
		cmd, err := scanReadFile(budget, filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil || !strings.Contains(string(cmd), marker) {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
		if err != nil {
			continue
		}
		return pid, strings.TrimSpace(strings.TrimPrefix(string(b), "0::"))
	}
	return 0, ""
}

// The gate creates this receipt and OCI artifact using the accepted pre-settings
// engine compiled from its pinned Git revision, not a simulated current receipt.
func TestRuntimeLegacyStaticReplay(t *testing.T) {
	if os.Getenv("NEROCD_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires disposable legacy artifact fixture")
	}
	cfg := Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: buildxChecksum(runtime.GOARCH), StateDir: "/legacy/state", CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE")}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("/legacy/state/legacy-static/receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var previous Result
	if json.Unmarshal(b, &previous) != nil || previous.ContextPolicy != "" {
		t.Fatal("not a genuine legacy receipt")
	}
	req := Request{BuildID: "legacy-static", Checkout: "/legacy/source", SourceID: "legacy-source", InputID: "legacy-input", DaemonID: previous.DaemonID, Strategy: Static, Platform: "linux/" + runtime.GOARCH, Limits: testLimits()}
	// An executable cannot be invoked at its configured path during this replay.
	// Its contents and Config identity remain unchanged, and New already verified it.
	if err = os.Rename(cfg.BuildxBinary, cfg.BuildxBinary+".replay-disabled"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(cfg.BuildxBinary+".replay-disabled", cfg.BuildxBinary); err != nil {
			t.Error(err)
		}
	}()
	got, err := e.Build(t.Context(), req)
	if err != nil || got != previous {
		t.Fatalf("legacy artifact replay changed or rebuilt: %v", err)
	}
	changed := req
	changed.SPAFallback = true
	if _, err = e.Build(t.Context(), changed); err == nil {
		t.Fatal("legacy receipt authorized changed settings")
	}
	changed = req
	changed.InputID = "different-input"
	if _, err = e.Build(t.Context(), changed); err == nil {
		t.Fatal("legacy receipt authorized changed source identity")
	}
	b, err = e.docker(t.Context(), cfg.StateDir, "ps", "--all", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
	if err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatal("legacy replay created builder")
	}
	t.Logf("legacy receipt replayed without builder: request=%s context=%s image=%s", got.RequestDigest, got.ContextDigest, got.ImageID)
}
