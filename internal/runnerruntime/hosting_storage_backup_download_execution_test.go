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

func TestStorageBackupDownloadOwnedCopyAndMetadataCompletion(t *testing.T) {
	for _, mode := range []string{"success", "missing", "tampered", "disconnect", "completion_loss", "changed_plan"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("archive bytes must never enter journal")
			digest := sha256.Sum256(data)
			key := hex.EncodeToString(digest[:])
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := artifactfs.OpenStore(root, domain.HostingStorageArchiveMaxBytes)
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
			p := domain.HostingStorageBackupDownloadPlan{SessionID: "session", ActorID: "actor", ProjectID: "project", ResourceID: "resource", BindingID: "binding", BackupID: "backup", CopyID: "copy", RunnerID: "runner", DestinationID: "destination", DaemonID: "daemon", Blob: domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: "sha256:" + key, SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}, LocalCopy: domain.BackupLocalCopyProof{PoolID: proof.PoolID, Device: proof.Device, Inode: proof.Inode, Bytes: proof.Bytes, Created: proof.Created}, ExpiresAt: now.Add(time.Minute)}
			p.Hash = domain.HostingDocumentHash(p)
			if p.Validate() != nil {
				t.Fatal("invalid fixture plan")
			}
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: "storage_transfer_session", RunSpec: domain.RunSpec{Type: domain.RunTypeStorageBackupTransfer, HostingOperationID: "session", HostingMode: domain.HostingStorageBackupDownloadMode}}, Lease: domain.RunLease{ID: "lease", RunID: "storage_transfer_session", RunnerID: "runner", Status: domain.LeaseActive, Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
			claim.PrimitivePlan.RunID = claim.Run.ID
			j, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			parent := runnerclient.WithStorageArchiveTransport(t.Context(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(parent, claim.Lease)
			defer s.Close()
			var streams, completions, plans atomic.Int32
			var allowCompletion atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/storage-backup-download/plan":
					current := p
					if plans.Add(1) > 1 && mode == "changed_plan" {
						current.ActorID = "other"
						current.Hash = ""
						current.Hash = domain.HostingDocumentHash(current)
					}
					_ = json.NewEncoder(w).Encode(current)
				case "/api/v1/runners/hosting/storage-backup-download/stream":
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
					var metadata domain.HostingStorageBackupDownloadStream
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

					_ = json.NewEncoder(w).Encode(map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
				case "/api/v1/runners/complete":
					completions.Add(1)
					var request runnerclient.CompleteRequest
					_ = json.NewDecoder(r.Body).Decode(&request)
					expected := "succeeded"
					if mode == "missing" || mode == "tampered" || mode == "disconnect" || mode == "changed_plan" {
						expected = "failed"
					}
					if request.Status != expected {
						t.Errorf("completion %s expected %s", request.Status, expected)
					}
					if len(j.Snapshot().Completions) != 1 {
						t.Error("completion was not durable before send")
					}
					if mode == "completion_loss" && !allowCompletion.Load() {
						http.Error(w, "unavailable", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(claim.Lease)
				case "/api/v1/runners/lease":
					_ = json.NewEncoder(w).Encode(claim.Lease)
				default:
					t.Errorf("unexpected request path %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			drained := false
			err = executeStorageBackupDownloadClaim(s, j, func(context.Context) error { drained = true; return nil }, server.URL, "token", claim, &StorageArchiveExecutionConfig{BackupDownloads: true, ArtifactRoot: root, TrustedLoopbackHTTP: true})
			if mode == "completion_loss" {
				if err == nil || len(j.Snapshot().Completions) != 1 {
					t.Fatal("lost completion did not retain metadata")
				}
			} else if err != nil || len(j.Snapshot().Completions) != 0 {
				t.Fatalf("execution/completion failed %v", err)
			}
			if mode == "completion_loss" {
				allowCompletion.Store(true)
				if err = reconcileRunnerJournalWithCounters(server.URL, "token", j, &runnerOperationalCounters{}, parent); err != nil {
					t.Fatal(err)
				}
				if streams.Load() != 1 || completions.Load() != 2 || len(j.Snapshot().Completions) != 0 {
					t.Fatal("startup replay retransmitted bytes or retained completion")
				}
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
