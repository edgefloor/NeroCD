package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"reflect"
)

func TestBuildExecutorResponseLossReplayNeverRebuilds(t *testing.T) {
	for _, phase := range []string{"source", "result"} {
		t.Run(phase, func(t *testing.T) {
			e, client, a, at := buildFixture(t)
			client.loseSource = phase == "source"
			client.loseComplete = phase == "result"
			calls := 0
			e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
				calls++
				return fixtureBuildResult(r), nil
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if e.Execute(ctx, a, at) == nil {
				t.Fatal("lost response accepted")
			}
			retained := e.journal.Snapshot().Build
			if len(retained) != 2 {
				t.Fatalf("lost retained requests: %+v", retained)
			}
			// Simulate a fresh executor using the same durable journal, with engine and
			// source acquisition forbidden. Replay can only resend an exact request.
			restarted := *e
			restarted.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("replay rebuilt")
				return hostingbuild.Result{}, nil
			})
			replayErr := restarted.Reconcile(ctx)
			if phase == "source" {
				if replayErr == nil || len(e.journal.Snapshot().Build) != 1 || calls != 0 {
					t.Fatal("source replay inferred a build result")
				}
			} else {
				if replayErr != nil {
					t.Fatal(replayErr)
				}
				if len(e.journal.Snapshot().Build) != 0 || calls != 1 || len(client.completeRequests) != 2 || !reflect.DeepEqual(client.completeRequests[0], client.completeRequests[1]) {
					t.Fatal("result replay mutated retained request")
				}
			}
		})
	}
}
func TestBuildExecutorRestartWithoutResultStaysUncertain(t *testing.T) {
	e, client, a, at := buildFixture(t)
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		return hostingbuild.Result{}, hostingbuild.ErrCleanupUnconfirmed
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil {
		t.Fatal("cleanup uncertainty accepted")
	}
	client.mu.Lock()
	client.live = false
	client.build.State = "cleanup_required"
	client.mu.Unlock()
	if e.Reconcile(ctx) == nil || e.Execute(ctx, a, at) == nil || len(e.journal.Snapshot().Build) != 1 {
		t.Fatal("uncertain intent was forgotten")
	}
}
func TestBuildExecutorReplayRejectsChangedProofAndLocalCleanup(t *testing.T) {
	for _, mode := range []string{"tuple", "hash", "artifact", "checkout", "marker"} {
		t.Run(mode, func(t *testing.T) {
			e, client, a, at := buildFixture(t)
			client.loseComplete = true
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if e.Execute(ctx, a, at) == nil {
				t.Fatal("response not lost")
			}
			switch mode {
			case "tuple":
				client.receipts[1].Authority.Fence = "other"
			case "hash":
				client.receipts[1].BodyHash = client.receipts[0].BodyHash
			case "artifact":
				copy := *client.build.Artifact
				copy.ImageID = copy.ConfigDigest
				client.build.Artifact = &copy
			case "checkout":
				if err := os.Mkdir(filepath.Join(e.config.SourceRoot, a.BuildID), 0700); err != nil {
					t.Fatal(err)
				}
			case "marker":
				if err := os.Mkdir(e.config.Engine.StateDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.config.Engine.StateDir, "cleanup_unconfirmed.json"), []byte("owned evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if e.Reconcile(ctx) == nil || len(e.journal.Snapshot().Build) != 2 {
				t.Fatal("mismatched proof or unresolved cleanup acknowledged")
			}
		})
	}
}
func TestBuildExecutorFailedBuildCompletionAfterCleanup(t *testing.T) {
	e, client, a, at := buildFixture(t)
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		return hostingbuild.Result{}, errors.New("private source output must not persist")
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil {
		t.Fatal("failed build returned success")
	}
	if client.build.State != "failed" || client.build.Artifact != nil || client.build.FailureCode != "build_failed" || len(e.journal.Snapshot().Build) != 0 {
		t.Fatalf("known cleaned failure not committed: %+v", client.build)
	}
}
func TestBuildExecutorOperationDeadlineNotExtended(t *testing.T) {
	e, _, a, at := buildFixture(t)
	var deadline time.Time
	e.engine = buildEngineFunc(func(c context.Context, _ hostingbuild.Request) (hostingbuild.Result, error) {
		deadline, _ = c.Deadline()
		<-c.Done()
		return hostingbuild.Result{}, c.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	expected, _ := ctx.Deadline()
	if e.Execute(ctx, a, at) == nil || !deadline.Equal(expected) {
		t.Fatal("operation deadline changed")
	}
}
func TestBuildExecutorMappingPreservesSettings(t *testing.T) {
	e, client, _, _ := buildFixture(t)
	s := client.build.Snapshot
	s.Source.Settings = domain.HostingBuildSettings{Strategy: "static", ContextDir: "web", PublishDir: "dist", SPAFallback: true}
	r := buildEngineRequest(s, "/owned/source", "observed")
	if r.ContextDir != "web" || r.PublishDir != "dist" || !r.SPAFallback || r.Limits.MemoryBytes != e.config.Policy.Limits.MemoryBytes {
		t.Fatal("snapshot settings or limits lost")
	}
}

func TestBuildExecutorExpiryProofDoesNotAuthorizeNewWork(t *testing.T) {
	e, client, a, at := buildFixture(t)
	client.live = false
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil || len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("historical tuple authorized new work")
	}
}

func TestBuildExecutorNeverAdoptsOrphanedEngineReceipt(t *testing.T) {
	e, _, a, at := buildFixture(t)
	dir := filepath.Join(e.config.Engine.StateDir, a.BuildID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "receipt.json"), []byte("orphaned completed engine receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		t.Fatal("orphaned engine result was adopted")
		return hostingbuild.Result{}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil || len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("orphaned state must reject before fresh effects")
	}
}
