//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestDatabaseBackupDownloadOwnedCopyAndMetadataCompletion(t *testing.T) {
	for _, mode := range []string{"success", "missing", "tampered", "disconnect", "completion_loss", "changed_plan", "settlement_failure", "settlement_failure_completion_loss"} {
		t.Run(mode, func(t *testing.T) {
			lostCompletion := mode == "completion_loss" || mode == "settlement_failure_completion_loss"
			data := []byte("archive bytes must never enter journal")
			digest := sha256.Sum256(data)
			key := hex.EncodeToString(digest[:])
			parentRoot, err := filepath.EvalSymlinks(t.TempDir())
			root := filepath.Join(parentRoot, "logical-backups")
			if e := os.Mkdir(root, 0o700); e != nil {
				t.Fatal(e)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := artifactfs.OpenStore(root, domain.HostingDatabaseBackupMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			_, proof, err := store.PutOwned(t.Context(), bytes.NewReader(data), key, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			if mode == "missing" {
				if err = os.Remove(filepath.Join(root, key)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "tampered" {
				if err = os.WriteFile(filepath.Join(root, key), bytes.Repeat([]byte("x"), len(data)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			p := domain.HostingDatabaseBackupDownloadPlan{Version: domain.HostingDatabaseBackupDownloadVersion, BackupVersion: domain.HostingDatabaseMySQLBackupVersion, ServerID: "server", SessionID: "session", ActorID: "actor", ProjectID: "project", ResourceID: "resource", BindingID: "binding", BackupID: "backup", CopyID: "copy", RunnerID: "runner", DestinationID: "destination", DaemonID: "daemon", Blob: domain.HostingDatabaseBackupBlob{Digest: "sha256:" + key, Bytes: int64(len(data)), Format: "mysql_sql"}, LocalCopy: domain.BackupLocalCopyProof{PoolID: proof.PoolID, Device: proof.Device, Inode: proof.Inode, Bytes: proof.Bytes, Created: proof.Created}, ExpiresAt: now.Add(time.Minute)}
			p.Hash = domain.HostingDocumentHash(p)
			if p.Validate() != nil {
				t.Fatal("invalid fixture plan")
			}
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: "db_transfer_session", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: "session", HostingMode: domain.HostingDatabaseBackupDownloadMode}}, Lease: domain.RunLease{ID: "lease", RunID: "db_transfer_session", RunnerID: "runner", Status: domain.LeaseActive, Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
			claim.PrimitivePlan.RunID = claim.Run.ID
			j, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			parent := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(parent, claim.Lease)
			defer s.Close()
			var streams, completions, plans atomic.Int32
			var allowCompletion atomic.Bool
			var firstCompletion atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-backup-download/plan":
					current := p
					if plans.Add(1) > 1 && mode == "changed_plan" {
						current.ActorID = "other"
						current.Hash = ""
						current.Hash = domain.HostingDocumentHash(current)
					}
					_ = json.NewEncoder(w).Encode(current)
				case "/api/v1/runners/hosting/database-backup-download/stream":
					streams.Add(1)
					if mode == "disconnect" {
						h, ok := w.(http.Hijacker)
						if !ok {
							t.Error("no hijacker")
							return
						}
						conn, _, _ := h.Hijack()
						_ = conn.Close()
						return
					}
					mr, e := r.MultipartReader()
					if e != nil {
						t.Error(e)
						return
					}
					meta, e := mr.NextPart()
					if e != nil {
						t.Error(e)
						return
					}
					var metadata domain.HostingDatabaseBackupDownloadStream
					if json.NewDecoder(meta).Decode(&metadata) != nil {
						t.Error("invalid metadata")
						return
					}
					if mode == "missing" || mode == "tampered" {
						if metadata.FailureCode != "archive_unavailable" {
							t.Error("missing unavailable notice")
						}
						if _, e = mr.NextPart(); e != io.EOF {
							t.Error("failure notice contains archive")
						}
					} else {
						if metadata.FailureCode != "" {
							t.Error("unexpected failure notice")
						}
						part, e := mr.NextPart()
						if e != nil {
							t.Error(e)
							return
						}
						got, e := io.ReadAll(part)
						if e != nil || !bytes.Equal(got, data) {
							t.Error("wrong streamed bytes")
						}
						if _, e = mr.NextPart(); e != io.EOF {
							t.Error("extra part")
						}
					}

					if strings.HasPrefix(mode, "settlement_failure") {
						w.WriteHeader(409)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
				case "/api/v1/runners/complete":
					call := completions.Add(1)
					var request runnerclient.CompleteRequest
					_ = json.NewDecoder(r.Body).Decode(&request)
					if request.LeaseID != claim.Lease.ID || request.Attempt != claim.Lease.Attempt || request.Fence != claim.Lease.Fence || request.CompletionKey == "" {
						t.Error("completion authority or idempotency key changed")
					}
					if call == 1 {
						firstCompletion.Store(request)
					} else if firstCompletion.Load().(runnerclient.CompleteRequest) != request {
						t.Error("completion replay changed metadata")
					}
					expected := "succeeded"
					if mode == "missing" || mode == "tampered" || mode == "disconnect" || mode == "changed_plan" || strings.HasPrefix(mode, "settlement_failure") {
						expected = "failed"
					}
					if request.Status != expected {
						t.Errorf("completion %s expected %s", request.Status, expected)
					}
					if len(j.Snapshot().Completions) != 1 {
						t.Error("completion was not durable before send")
					}
					if lostCompletion && !allowCompletion.Load() {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						_ = conn.Close()
						// End this attempt after actual response loss. Restart
						// reconciliation must recover only its durable metadata.
						s.cancel()
						return
					}
					_ = json.NewEncoder(w).Encode(claim.Lease)
				case "/api/v1/runners/claim":
					if len(j.Snapshot().Completions) != 0 {
						t.Error("successor claimed before completion ACK")
					}
					next := claim
					next.Run.ID = "successor"
					next.Lease.RunID = "successor"
					next.Lease.ID = "successor_lease"
					_ = json.NewEncoder(w).Encode(next)
				case "/api/v1/runners/lease":
					_ = json.NewEncoder(w).Encode(claim.Lease)
				default:
					t.Errorf("unexpected request path %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			drained := false
			err = executeDatabaseBackupDownloadClaim(s, j, func(context.Context) error { drained = true; return nil }, server.URL, "token", claim, &DatabaseExecutionConfig{BackupDownloads: true, StateRoot: parentRoot, TrustedLoopbackHTTP: true})
			if lostCompletion {
				if err == nil || len(j.Snapshot().Completions) != 1 {
					t.Fatal("lost completion did not retain metadata")
				}
			} else if err != nil || len(j.Snapshot().Completions) != 0 {
				t.Fatalf("execution/completion failed %v", err)
			}
			if lostCompletion {
				// If startup accidentally reopened the copy, it would now fail.
				if e := os.Remove(filepath.Join(root, key)); e != nil {
					t.Fatal(e)
				}
				allowCompletion.Store(true)
				if err = reconcileRunnerJournalWithCounters(server.URL, "token", j, &runnerOperationalCounters{}, parent); err != nil {
					t.Fatal(err)
				}
				if streams.Load() != 1 || completions.Load() != 2 || len(j.Snapshot().Completions) != 0 {
					t.Fatal("startup replay retransmitted bytes or retained completion")
				}
			}
			next, e := runnerclient.New(server.URL, "token").Claim(parent)
			if e != nil || next.Run.ID != "successor" || next.Lease.ID != "successor_lease" {
				t.Fatalf("completed download blocked next claim: %v", e)
			}
			if !drained || completions.Load() < 1 {
				t.Fatal("completion path not drained exactly once")
			}
			want := int32(1)
			if mode == "changed_plan" {
				want = 0
			}
			if streams.Load() != want {
				t.Fatalf("unexpected streams %d", streams.Load())
			}
			raw, _ := json.Marshal(j.Snapshot())
			if strings.Contains(string(raw), string(data)) || strings.Contains(string(raw), root) || strings.Contains(string(raw), p.Hash) {
				t.Fatal("stream state leaked into journal")
			}
		})
	}
}
