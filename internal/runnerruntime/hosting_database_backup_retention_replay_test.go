//go:build unix

package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func retentionReplayEntry(t *testing.T) runner.JournalDatabaseBackupRetention {
	t.Helper()
	now := time.Now().UTC()
	item := domain.BackupRetentionItem{CopyID: "copy", BackupID: "backup", Location: "local", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, CreatedAt: now, Availability: "available", Selected: true, ProtectedReasons: []string{}}
	op := domain.HostingDatabaseBackupRetentionExecution{ID: "retention", Version: domain.HostingDatabaseBackupRetentionVersion, ProjectID: "project", ResourceID: "resource", PreviewID: "preview", PolicyRevision: 1, PlanHash: "sha256:" + strings.Repeat("b", 64), State: "queued", ReservationHeld: true, LocalState: "pending", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{{CopyID: item.CopyID, BackupID: item.BackupID, Location: "local", State: "pending", ReservationHeld: true}}, CreatedAt: now, UpdatedAt: now}
	copy := domain.BackupRetentionCopy{Item: item, RunnerID: "runner", DestinationID: "destination", ServerID: "server", DaemonID: "daemon", IdentityHash: "sha256:" + strings.Repeat("c", 64), Local: &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("d", 64), Device: 1, Inode: 2, Bytes: item.Bytes, Created: true}}
	p := domain.HostingDatabaseBackupRetentionPlan{Operation: op, ActorID: "owner", RunnerID: "runner", Copies: []domain.BackupRetentionCopy{copy}, TimeoutSeconds: 60}
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: op.ID, RunID: "db_retention_" + op.ID, LeaseID: "oldlease", Attempt: 1, Fence: "oldfence"}
	return runner.JournalDatabaseBackupRetention{ID: "intent", Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, CopyID: item.CopyID, Phase: "intent"}, CreatedAt: now}
}

func TestBackupRetentionStartupLostAcknowledgmentAndSupersession(t *testing.T) {
	testBackupRetentionStartupLostAcknowledgmentAndSupersession(t, domain.HostingDatabaseBackupRetentionVersion)
}
func TestMySQLBackupRetentionStartupLostAcknowledgmentAndSupersession(t *testing.T) {
	testBackupRetentionStartupLostAcknowledgmentAndSupersession(t, domain.HostingDatabaseMySQLBackupRetentionVersion)
}
func testBackupRetentionStartupLostAcknowledgmentAndSupersession(t *testing.T, version string) {
	t.Helper()
	for _, mode := range []string{"committed_result", "unpaired_without_resume", "unpaired_with_resume", "foreign_engine_response"} {
		t.Run(mode, func(t *testing.T) {
			entry := retentionReplayEntry(t)
			entry.Plan.Operation.Version = version
			entry.Plan.Hash = ""
			entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
			entry.Request.PlanHash = entry.Plan.Hash
			path := filepath.Join(t.TempDir(), "journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.AppendDatabaseBackupRetention(entry); err != nil {
				t.Fatal(err)
			}
			result := entry
			result.ID = "deleted"
			result.Request.ReceiptID = "deleted"
			result.Request.Phase = "deleted"
			if mode == "committed_result" || mode == "foreign_engine_response" {
				if _, err = j.AppendDatabaseBackupRetention(result); err != nil {
					t.Fatal(err)
				}
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := j.Close(); err != nil {
					t.Error("fixture cleanup failed")
				}
			}()
			mutations, retirements := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-backup-retention/mutation":
					mutations++
					var in domain.HostingDatabaseBackupRetentionMutation
					if json.NewDecoder(r.Body).Decode(&in) != nil || domain.HostingDocumentHash(in) != domain.HostingDocumentHash(result.Request) {
						t.Error("replay changed committed request")
						w.WriteHeader(400)
						return
					}
					out := entry.Plan.Operation
					if mode == "foreign_engine_response" {
						if version == domain.HostingDatabaseMySQLBackupRetentionVersion {
							out.Version = domain.HostingDatabaseBackupRetentionVersion
						} else {
							out.Version = domain.HostingDatabaseMySQLBackupRetentionVersion
						}
					}
					out.State = "succeeded"
					out.ReservationHeld = false
					out.LocalState = "succeeded"
					out.Items[0].State = "deleted"
					out.Items[0].ReservationHeld = false
					if err := json.NewEncoder(w).Encode(out); err != nil {
						t.Error("fixture response encoding failed")
					}
				case "/api/v1/runners/hosting/database-backup-retention/retirement":
					retirements++
					var in domain.HostingDatabaseBackupRetentionRetirementInput
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != entry.Request.HostingAuthority || in.PlanHash != entry.Plan.Hash {
						t.Error("retirement changed old identity")
						w.WriteHeader(400)
						return
					}
					if mode != "unpaired_with_resume" {
						w.WriteHeader(409)
						return
					}
					if err := json.NewEncoder(w).Encode(domain.HostingDatabaseBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: 2, ReplayOnly: true}); err != nil {
						t.Error("fixture response encoding failed")
					}
				default:
					t.Errorf("startup attempted effect/claim %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			err = reconcileDatabaseBackupRetentionJournal(t.Context(), Config{Server: server.URL, Token: "synthetic", Database: &DatabaseExecutionConfig{BackupRetention: true, TrustedLoopbackHTTP: true}}, j)
			switch mode {
			case "committed_result":
				if err != nil || mutations != 1 || retirements != 0 || j.Depth() != 0 {
					t.Fatalf("committed replay: mutation=%d retirement=%d depth=%d err=%v", mutations, retirements, j.Depth(), err)
				}
			case "foreign_engine_response":
				if err == nil || mutations != 1 || j.Depth() != 2 {
					t.Fatal("foreign engine response retired exact journal evidence")
				}
			default:
				if mutations != 0 || retirements != 1 || j.Depth() != 1 {
					t.Fatal("unpaired intent mutated or lost")
				}
				if (err == nil) != (mode == "unpaired_with_resume") {
					t.Fatalf("supersession: %v", err)
				}
			}
		})
	}
}
