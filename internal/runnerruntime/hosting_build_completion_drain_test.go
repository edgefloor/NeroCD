package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
)

func TestBuildDispatchCompletionJoinsAuthorityWorkersBeforeACK(t *testing.T) {
	for _, worker := range []string{"watcher", "renewer"} {
		t.Run(worker, func(t *testing.T) {
			e, client, a, _ := buildFixture(t)
			claim := buildDispatchClaim(a)
			claim.Lease.ExpiresAt = time.Now().Add(3 * time.Second)
			entered, committed, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var enterOnce, exitOnce sync.Once
			var renewals atomic.Int32
			var canceledRequest atomic.Bool
			e.engine = buildEngineFunc(func(ctx context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
				select {
				case <-entered:
					return fixtureBuildResult(r), nil
				case <-ctx.Done():
					return hostingbuild.Result{}, ctx.Err()
				}
			})
			e.client = completionFixture{buildFixtureClient: client, complete: func(ctx context.Context, in hostingbuildprotocol.CompleteRequest) (hostingbuildprotocol.PhaseResponse, error) {
				if len(e.journal.Snapshot().Build) < 1 {
					t.Error("completion preceded durable result")
				}
				response, err := client.BuildComplete(ctx, in)
				close(committed)
				<-exited
				if canceledRequest.Load() {
					return response, err
				}
				<-ctx.Done()
				return hostingbuildprotocol.PhaseResponse{}, ctx.Err()
			}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lease := claim.Lease
				switch r.URL.Path {
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
					return
				case "/api/v1/runners/renew":
					_, _ = io.Copy(io.Discard, r.Body)
					if renewals.Add(1) == 1 {
						_ = json.NewEncoder(w).Encode(lease)
						return
					}
					if worker != "renewer" {
						_ = json.NewEncoder(w).Encode(lease)
						return
					}
				case "/api/v1/runners/lease":
					if worker != "watcher" {
						_ = json.NewEncoder(w).Encode(lease)
						return
					}
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				enterOnce.Do(func() { close(entered) })
				defer exitOnce.Do(func() { close(exited) })
				select {
				case <-r.Context().Done():
					canceledRequest.Store(true)
					return
				case <-committed:
					lease.Status = domain.RunSucceeded
					_ = json.NewEncoder(w).Encode(lease)
				}
			}))
			defer server.Close()
			poll := time.Hour
			if worker == "watcher" {
				poll = time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			if err := executeBuildClaim(ctx, server.URL, "fixture", claim, "runner", poll, e, &runnerOperationalCounters{}); err != nil {
				t.Fatalf("committed build canceled its own acknowledgement: %v", err)
			}
			if !canceledRequest.Load() || len(e.journal.Snapshot().Build) != 0 {
				t.Fatal("authority workers survived terminal publish or result was not acknowledged")
			}
			if client.build.State != "succeeded" {
				t.Fatal(errors.New("build did not settle"))
			}
		})
	}
}
