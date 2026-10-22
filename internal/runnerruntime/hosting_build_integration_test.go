//go:build linux

package runnerruntime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"nerocd/internal/source"
)

func buildRuntimeGit(t *testing.T, files map[string]string) (string, source.RepositoryPolicy) {
	t.Helper()
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	for name, content := range files {
		p := filepath.Join(work, name)
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkoutCommand(t, work, "add", ".")
	checkoutCommand(t, work, "commit", "-qm", "fixture")
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	cert := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// This fixture-only trusted Git wrapper adds only the isolated server's CA.
	// The executor has no URL-controlled CA or certificate-verification bypass.
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+shellQuote(actual)+" -c http.sslCAInfo="+shellQuote(cert)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return server.URL + "/repo.git", source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"127.0.0.1"}, AllowedCIDRs: []string{"127.0.0.0/8"}}
}
func buildRuntimeService(t *testing.T, f *buildFixtureClient) runnerclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer isolated-build-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		decode := func(v any) error {
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
			d.DisallowUnknownFields()
			return d.Decode(v)
		}
		var response any
		var err error
		switch r.URL.Path {
		case "/api/v1/runners/builds/events":
			var req domain.HostingBuildLogBatch
			if err = decode(&req); err == nil {
				response, err = f.BuildEvents(r.Context(), req)
			}
		case "/api/v1/runners/builds/status":
			var req hostingbuildprotocol.StatusRequest
			if err = decode(&req); err == nil {
				if req.Authority != f.authority {
					err = errors.New("authority")
				} else {
					response, err = f.BuildStatus(r.Context(), req)
				}
			}
		case "/api/v1/runners/builds/source-begin":
			var req hostingbuildprotocol.SourceBeginRequest
			if err = decode(&req); err == nil {
				response, err = f.BuildSourceBegin(r.Context(), req)
			}
		case "/api/v1/runners/builds/source-commit":
			var req hostingbuildprotocol.SourceCommitRequest
			if err = decode(&req); err == nil {
				response, err = f.BuildSourceCommit(r.Context(), req)
			}
		case "/api/v1/runners/builds/prepare":
			var req hostingbuildprotocol.PrepareRequest
			if err = decode(&req); err == nil {
				response, err = f.BuildPrepare(r.Context(), req)
			}
		case "/api/v1/runners/builds/complete":
			var req hostingbuildprotocol.CompleteRequest
			if err = decode(&req); err == nil {
				response, err = f.BuildComplete(r.Context(), req)
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "phase unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return runnerclient.New(server.URL, "isolated-build-token")
}
func buildRuntimeExecutor(t *testing.T, id string, files map[string]string, strategy string) (*BuildExecutor, *buildFixtureClient, domain.HostingBuildAuthority) {
	t.Helper()
	e, f, a, at := buildFixture(t)
	_ = at
	url, policy := buildRuntimeGit(t, files)
	s := f.build.Snapshot
	s.BuildID = id
	s.Source.RepositoryURL = url
	s.Source.Policy = policy
	s.Source.Settings = domain.HostingBuildSettings{Strategy: strategy}
	root := t.TempDir()
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(sources, 0700); err != nil {
		t.Fatal(err)
	}
	c := e.config
	c.SourceRoot = sources
	c.StatusInterval = 100 * time.Millisecond
	c.RequestTimeout = 5 * time.Second
	c.Engine = hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), StateDir: filepath.Join(root, "state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE")}
	c.Policy.Platform = "linux/" + runtime.GOARCH
	c.Policy.Limits = domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 1 << 30, Deadline: 3 * time.Minute, ContextBytes: 8 << 20, ContextFiles: 1000, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 256 << 20, UnpackedBytes: 512 << 20, UsageBytes: 3 << 30, ReserveBytes: 128 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}
	identity, err := ComputeBuildToolchainIdentity(t.Context(), c.Engine, c.Policy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	c.Policy.ToolchainIdentity = identity
	s.Policy = c.Policy
	s.InputID = ""
	s.InputID = domain.HostingDocumentHash(s)
	a.BuildID = id
	a.RunID = "build_run_" + id
	f.authority = a
	f.build.ID = id
	f.build.RunID = a.RunID
	f.build.Snapshot = s
	result, err := NewBuildExecutor(c, buildRuntimeService(t, f), e.journal)
	if err != nil {
		t.Fatal(err)
	}
	return result, f, a
}
func buildRuntimeExecute(ctx context.Context, e *BuildExecutor, a domain.HostingBuildAuthority) error {
	at := runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(-time.Second)}
	return e.Execute(ctx, a, at)
}
func buildRuntimeDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("owned fixture docker %s: %v: %.1000s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}
func buildRuntimeHTTP(t *testing.T, name, expected string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/local/bin/docker", "run", "--pull=never", "--rm", "--network", "container:"+name, "--entrypoint", "/bin/busybox", hostingbuild.StaticImage, "wget", "-T", "2", "-qO-", "http://127.0.0.1:80")
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil && string(out) == expected {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("owned workload HTTP did not return exact expected content")
}
func TestHostingBuildRuntime(t *testing.T) {
	if os.Getenv("NEROCD_BUILD_EXECUTOR_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-hosting-build/gate.sh")
	}
	buildRuntimeDocker(t, "pull", hostingbuild.StaticImage)
	survivor := ""
	for _, strategy := range []string{"dockerfile", "static", "railpack"} {
		t.Run(strategy, func(t *testing.T) {
			files := map[string]string{"index.html": "git-to-" + strategy + "\n"}
			if strategy == "dockerfile" {
				files["Dockerfile"] = "FROM " + hostingbuild.StaticImage + "\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\n"
			}
			if strategy == "railpack" {
				files["Staticfile"] = "root: .\n"
			}
			e, f, a := buildRuntimeExecutor(t, "runtime-"+strategy, files, strategy)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			if err := buildRuntimeExecute(ctx, e, a); err != nil {
				t.Fatal(err)
			}
			if f.build.Artifact == nil || domain.ValidateHostingBuildArtifact(*f.build.Artifact, f.build) != nil || len(e.journal.Snapshot().Build) != 0 {
				t.Fatal("invalid actual artifact or unsettled journal")
			}
			name := "nerocd-executor-" + strategy
			container := buildRuntimeDocker(t, "run", "--detach", "--name", name, "--label", "io.nerocd.build-executor.fixture="+a.BuildID, "--env", "PORT=80", f.build.Artifact.ImageID)
			t.Cleanup(func() { buildRuntimeDocker(t, "rm", "--force", container) })
			buildRuntimeHTTP(t, name, "git-to-"+strategy+"\n")
			t.Logf("source=%s image=%s manifest=%s config=%s", f.build.SourceReceipt.Commit, f.build.Artifact.ImageID, f.build.Artifact.ManifestDigest, f.build.Artifact.ConfigDigest)
			if strategy == "dockerfile" {
				survivor = container
				// Keep this exact independent workload through subsequent failure cases.
				for _, failure := range []string{"failed", "cancel", "lease-loss", "daemon", "response-loss", "source-response-loss"} {
					t.Run(failure, func(t *testing.T) { runRuntimeBuildFailure(t, failure, survivor) })
				}
			}
		})
	}
}
func runRuntimeBuildFailure(t *testing.T, mode, survivor string) {
	content := "FROM " + hostingbuild.StaticImage + "\nRUN exit 7\n"
	if mode == "cancel" || mode == "lease-loss" {
		content = "FROM " + hostingbuild.StaticImage + "\nRUN sleep 60 # " + mode + "\n"
	}
	if mode == "response-loss" || mode == "source-response-loss" {
		content = "FROM " + hostingbuild.StaticImage + "\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\n"
	}
	e, f, a := buildRuntimeExecutor(t, "runtime-"+mode, map[string]string{"Dockerfile": content, "index.html": "retained-result\n"}, "dockerfile")
	if mode == "daemon" {
		f.build.Snapshot.ExpectedDaemonID = "unselected-daemon"
		s := f.build.Snapshot
		s.InputID = ""
		f.build.Snapshot.InputID = domain.HostingDocumentHash(s)
	}
	f.loseComplete = mode == "response-loss"
	f.loseSource = mode == "source-response-loss"
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- buildRuntimeExecute(ctx, e, a) }()
	if mode == "cancel" || mode == "lease-loss" {
		deadline := time.Now().Add(30 * time.Second)
		for {
			ids := buildRuntimeDocker(t, "ps", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner")
			if ids != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("owned builder never started")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if mode == "cancel" {
			cancel()
		} else {
			f.mu.Lock()
			f.live = false
			f.mu.Unlock()
		}
	}
	if err := <-done; err == nil {
		t.Fatal("injected failure returned success")
	}
	switch mode {
	case "response-loss":
		if err := e.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(e.journal.Snapshot().Build) != 0 {
			t.Fatal("response-loss replay not settled")
		}
		t.Log("exact completion replay settled actual retained artifact")
	case "failed":
		if f.build.State != "failed" || f.build.Artifact != nil || len(e.journal.Snapshot().Build) != 0 {
			t.Fatal("known cleaned build failure was not committed")
		}
	default:
		if e.Reconcile(t.Context()) == nil || len(e.journal.Snapshot().Build) == 0 {
			t.Fatal("unresolved intent was forgotten")
		}
	}
	if ids := buildRuntimeDocker(t, "ps", "--all", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner"); ids != "" {
		t.Fatalf("owned builder/planner leaked: %s", ids)
	}
	if _, err := os.Stat(filepath.Join(e.config.Engine.StateDir, "cleanup_unconfirmed.json")); !os.IsNotExist(err) {
		t.Fatal("engine cleanup unconfirmed")
	}
	buildRuntimeHTTP(t, survivor, "git-to-dockerfile\n")
	t.Logf("%s preserved independent HTTP and verified builder cleanup", mode)
}

func TestBuildLocalReservationAndObserver(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	release, err := lockBuildSource(root, "owned")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := lockBuildSource(root, "owned"); err == nil {
		other()
		t.Fatal("second process lock admitted")
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if validateBuildSourceRoot(link) == nil {
		t.Fatal("symlink source root accepted")
	}
	binary := filepath.Join(root, "docker")
	body := "#!/bin/sh\nprintf '%s' '{\"ID\":\"owned-daemon\",\"OSType\":\"linux\",\"CgroupVersion\":\"2\",\"DockerRootDir\":\"" + root + "\"}'\n"
	if err = os.WriteFile(binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c := BuildConfig{SourceRoot: root, Engine: hostingbuild.Config{DockerBinary: binary, Socket: "/fixed.sock"}, RequestTimeout: time.Second}
	id, err := observeBuildDaemon(t.Context(), c)
	if err != nil || id != "owned-daemon" {
		t.Fatalf("fixed daemon observer: %v", err)
	}
	if err = os.WriteFile(binary, []byte("#!/bin/sh\nsleep 30 &\necho $! > "+shellQuote(filepath.Join(root, "child"))+"\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err = observeBuildDaemon(ctx, c); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("observer cancellation did not join")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".build-observer-") {
			t.Fatal("observer transport workspace remained")
		}
	}
}

func TestBuildLocalCheckoutFailureWaitsForCleanup(t *testing.T) {
	url, policy := buildRuntimeGit(t, map[string]string{"index.html": "public"})
	e, f, _, _ := buildFixture(t)
	s := f.build.Snapshot
	s.Source.RepositoryURL = url
	s.Source.Policy = policy
	expected := errors.New("private runtime failure")
	for _, cancelDuringUse := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringUse), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			path := filepath.Join(e.config.SourceRoot, "checkout")
			err := checkoutPublicBuild(ctx, s, path, "", func(context.Context, domain.HostingBuildSourceReceipt) error { return nil }, func(context.Context, string) error {
				if cancelDuringUse {
					cancel()
				}
				return expected
			})
			if cancelDuringUse {
				if err == nil || err == expected {
					t.Fatal("parent cancellation lost after use")
				}
			} else if err != expected {
				t.Fatalf("known failure lost after cleanup: %v", err)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("checkout cleanup did not join")
			}
		})
	}
}
