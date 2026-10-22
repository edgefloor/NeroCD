//go:build linux

package runnerruntime

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runner"
	"nerocd/internal/store"
)

func TestHostingBuildProductionControllerCLI(t *testing.T) {
	if os.Getenv("NEROCD_BUILD_CONTROLLER_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-hosting-build-controller/gate.sh")
	}
	f := newControllerBuildFixture(t)
	buildRuntimeDocker(t, "pull", hostingbuild.StaticImage)
	survivor := ""
	for _, strategy := range []string{"dockerfile", "railpack", "static"} {
		t.Run(strategy, func(t *testing.T) {
			expected := "controller-" + strategy + "\n"
			settings := domain.HostingBuildSettings{Strategy: strategy}
			files := map[string]string{"index.html": expected}
			switch strategy {
			case "dockerfile":
				settings.ContextDir = "apps/web"
				settings.DockerfilePath = "docker/Release.Dockerfile"
				settings.BuildTarget = "chosen"
				files = map[string]string{"apps/web/index.html": expected, "apps/web/docker/Release.Dockerfile": "FROM " + hostingbuild.StaticImage + " AS chosen\nRUN sleep 125\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\nFROM " + hostingbuild.StaticImage + " AS other\nRUN echo wrong-target > /usr/share/nginx/html/index.html\n"}
			case "railpack":
				files["Staticfile"] = "root: .\n"
			case "static":
				settings.ContextDir = "web"
				settings.PublishDir = "dist"
				settings.SPAFallback = true
				files = map[string]string{"web/dist/index.html": expected, "web/dist/app.js": "public-asset\n"}
			}
			r := f.resource(t, files, settings)
			b := f.build(t, r, "actual-"+strategy)
			before := f.renewals.Load()
			f.loseLogResponse.Store(true)
			lostBefore := f.lostLogResponses.Load()
			if strategy == "static" {
				f.loseCompletion.Store(true)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			err := f.agent(ctx)
			if strategy == "static" {
				if err == nil {
					t.Fatal("lost committed response was not observed")
				}
				if err = f.agent(ctx); err != nil {
					t.Fatalf("exact committed completion replay: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if full.State != "succeeded" || full.Artifact == nil || domain.ValidateHostingBuildArtifact(*full.Artifact, full) != nil {
				t.Fatal("production build did not persist a valid artifact")
			}
			if f.lostLogResponses.Load() != lostBefore+1 {
				t.Fatal("typed log response loss was not exercised")
			}
			var logs domain.HostingBuildLogPage
			f.request(t, "GET", "/api/v1/hosting-builds/"+b.ID+"/logs?after_sequence=0&limit=200", f.admin, nil, http.StatusOK, &logs)
			if len(logs.Items) < 2 || !logs.Terminal || !logs.HistoryComplete || logs.Gap {
				t.Fatal("public retained output unavailable")
			}
			actualOutput := false
			for _, item := range logs.Items {
				if item.Component == "buildkit" || item.Component == "railpack" {
					actualOutput = true
				}
				if strings.Contains(item.Message, f.credential) {
					t.Fatal("credential reached public logs")
				}
			}
			if !actualOutput {
				t.Fatal("engine output missing from public logs")
			}
			var task, lease, key string
			var artifacts, receipts int
			if err = f.db.QueryRow(t.Context(), `SELECT r.status,l.status,l.completion_key,(SELECT count(*) FROM hosting_build_artifacts WHERE build_id=$1),(SELECT count(*) FROM hosting_build_receipts WHERE build_id=$1 AND phase='complete') FROM task_runs r JOIN run_leases l ON l.run_id=r.id WHERE r.id=$2`, b.ID, b.RunID).Scan(&task, &lease, &key, &artifacts, &receipts); err != nil {
				t.Fatal(err)
			}
			if task != "succeeded" || lease != "succeeded" || key == "" || artifacts != 1 || receipts != 1 {
				t.Fatalf("terminal task/lease/artifact/receipt not atomic: %s %s %d %d", task, lease, artifacts, receipts)
			}
			if strategy == "dockerfile" {
				f.mu.Lock()
				initial := f.initialExpiries[b.RunID]
				f.mu.Unlock()
				if initial.IsZero() || time.Now().Before(initial) || f.renewals.Load()-before < 2 {
					t.Fatal("actual build did not cross default initial lease with renewal")
				}
				t.Logf("real default lease renewed beyond initial expiry %s; renewals=%d", initial.UTC().Format(time.RFC3339), f.renewals.Load()-before)
			}
			name := "nerocd-controller-" + strategy
			id := buildRuntimeDocker(t, "run", "--detach", "--name", name, "--label", "io.nerocd.build-controller.fixture=owned", "--env", "PORT=80", full.Artifact.ImageID)
			f.containers = append(f.containers, id)
			buildRuntimeHTTP(t, id, expected)
			if strategy == "dockerfile" {
				survivor = id
			}
			if strategy == "static" {
				out := buildRuntimeDocker(t, "run", "--rm", "--network", "container:"+id, "--entrypoint", "/bin/busybox", hostingbuild.StaticImage, "wget", "-T", "2", "-qO-", "http://127.0.0.1:80/deep/link")
				if out != strings.TrimSpace(expected) {
					t.Fatal("prepared SPA deep link failed")
				}
			}
			journal, err := runner.OpenAttemptJournal(f.journal)
			if err != nil {
				t.Fatal(err)
			}
			depth := journal.Depth()
			_ = journal.Close()
			if depth != 0 {
				t.Fatal("successful build retained unsettled journal")
			}
			t.Logf("production %s build=%s image=%s manifest=%s config=%s", strategy, b.ID, full.Artifact.ImageID, full.Artifact.ManifestDigest, full.Artifact.ConfigDigest)
			if strategy == "static" {
				// Age only this completed fixture's output, then use the real retention
				// transaction. Receipts and sequence history must outlive plaintext.
				if _, err = f.db.Exec(t.Context(), `UPDATE run_logs SET created_at=now()-interval '31 days' WHERE run_id=$1`, b.RunID); err != nil {
					t.Fatal(err)
				}
				policy, err := f.pg.UpdateRunLogRetentionPolicy(t.Context(), domain.RunLogRetentionPolicy{Enabled: true, KeepDays: 30, BatchSize: 1000, UpdatedBy: "usr_bootstrap"})
				if err != nil {
					t.Fatal(err)
				}
				execution, err := f.pg.ExecuteRunLogRetention(t.Context(), "build-output-retention", store.RunLogRetentionBodyHash(policy), domain.AuditEvent{ID: "build-output-retention-audit", ActorID: "usr_bootstrap", Action: "run_log.retention", TargetID: b.RunID, CreatedAt: time.Now()})
				if err != nil || execution.Deleted == 0 {
					t.Fatal("production retention failed", err)
				}
				var retained domain.HostingBuildLogPage
					f.request(t, "GET", "/api/v1/hosting-builds/"+b.ID+"/logs?after_sequence=0&limit=200", f.admin, nil, http.StatusOK, &retained)
				if len(retained.Items) != 0 || !retained.Gap || retained.DeletedThroughSequence == 0 || !retained.Terminal || !retained.HistoryComplete {
					t.Fatal("retention lost output cursor evidence")
				}
				t.Logf("typed output response-loss, completion ordering and retention cursor passed: build=%s deleted=%d cursor=%d", b.ID, execution.Deleted, retained.NextSequence)
			}
		})
	}
	t.Run("failed-build-preserves-independent-http", func(t *testing.T) {
		r := f.resource(t, map[string]string{"Dockerfile": "FROM " + hostingbuild.StaticImage + "\nRUN exit 9\n"}, domain.HostingBuildSettings{Strategy: "dockerfile"})
		b := f.build(t, r, "actual-failed")
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		if f.agent(ctx) == nil {
			t.Fatal("failed build returned success")
		}
		full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
		if err != nil || full.State != "failed" || full.Artifact != nil || full.FailureCode != "build_failed" {
			t.Fatalf("production failure metadata: %v", err)
		}
		var task, lease string
		if err = f.db.QueryRow(t.Context(), `SELECT r.status,l.status FROM task_runs r JOIN run_leases l ON l.run_id=r.id WHERE r.id=$1`, b.RunID).Scan(&task, &lease); err != nil || task != "failed" || lease != "failed" {
			t.Fatal("failed build task/lease not settled")
		}
		buildRuntimeHTTP(t, survivor, "controller-dockerfile\n")
	})
	t.Run("sigterm-joins-started-builder-and-retains-intent", func(t *testing.T) {
		r := f.resource(t, map[string]string{"Dockerfile": "FROM " + hostingbuild.StaticImage + "\nRUN sleep 60\n"}, domain.HostingBuildSettings{Strategy: "dockerfile"})
		b := f.build(t, r, "actual-sigterm")
		originalJournal := f.journal
		f.journal = filepath.Join(f.work, "signal-journal")
		defer func() { f.journal = originalJournal }()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		command := f.agentCommand(ctx)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		joined := false
		defer func() {
			if !joined {
				cancel()
				<-done
			}
		}()
		deadline := time.Now().Add(25 * time.Second)
		for {
			current, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == "building" && buildRuntimeDocker(t, "ps", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner") != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("signal fixture owned builder never started")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			joined = true
			if err == nil {
				t.Fatal("SIGTERM returned success")
			}
		case <-time.After(35 * time.Second):
			t.Fatal("SIGTERM did not join cleanup within bound")
		}
		if ids := buildRuntimeDocker(t, "ps", "--all", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner"); ids != "" {
			t.Fatalf("SIGTERM left owned builder: %s", ids)
		}
		journal, err := runner.OpenAttemptJournal(f.journal)
		if err != nil {
			t.Fatal(err)
		}
		retained := len(journal.Snapshot().Build)
		_ = journal.Close()
		if retained != 1 {
			t.Fatal("SIGTERM lost uncertain build intent")
		}
		buildRuntimeHTTP(t, survivor, "controller-dockerfile\n")
		t.Logf("SIGTERM joined CLI pid=%d and owned builder cleanup; exact intent retained", command.Process.Pid)
	})
	t.Run("cancel-preserves-uncertainty-and-independent-http", func(t *testing.T) {
		r := f.resource(t, map[string]string{"Dockerfile": "FROM " + hostingbuild.StaticImage + "\nRUN sleep 60\n"}, domain.HostingBuildSettings{Strategy: "dockerfile"})
		b := f.build(t, r, "actual-cancel")
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- f.agent(ctx) }()
		deadline := time.Now().Add(25 * time.Second)
		var current domain.HostingBuild
		for {
			var err error
			current, err = f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == "building" && buildRuntimeDocker(t, "ps", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner") != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("owned build never started")
			}
			time.Sleep(50 * time.Millisecond)
		}
		f.request(t, "POST", "/api/v1/hosting-builds/"+b.ID+"/cancel", f.admin, map[string]int64{"expected_revision": current.Revision}, http.StatusOK, nil)
		if err := <-done; err == nil {
			t.Fatal("canceled build returned success")
		}
		current, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
		if err != nil || current.State != "cleanup_required" || current.Artifact != nil {
			t.Fatal("cancellation uncertainty was not preserved")
		}
		if ids := buildRuntimeDocker(t, "ps", "--all", "--quiet", "--filter", "label=io.nerocd.hostingbuild.owner"); ids != "" {
			t.Fatalf("owned builder/planner leaked: %s", ids)
		}
		if _, err = os.Stat(filepath.Join(f.config.Engine.StateDir, "cleanup_unconfirmed.json")); !os.IsNotExist(err) {
			t.Fatal("engine cleanup unconfirmed")
		}
		before := f.heartbeats.Load()
		if f.agent(ctx) == nil || f.heartbeats.Load() != before {
			t.Fatal("unsettled startup entered heartbeat/claim")
		}
		journal, err := runner.OpenAttemptJournal(f.journal)
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Snapshot().Build) != 1 {
			_ = journal.Close()
			t.Fatal("cancellation intent lost")
		}
		_ = journal.Close()
		buildRuntimeHTTP(t, survivor, "controller-dockerfile\n")
		t.Log("cancel retained exact intent and cleanup_required; no generic requeue/completion")
	})
	if f.generic.Load() != 0 {
		t.Fatalf("build entered generic event/completion endpoints %d times", f.generic.Load())
	}
}
