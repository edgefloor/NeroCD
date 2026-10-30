//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"io"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExternalInspectionLostAckReplaysReceiptWithoutSecondGET(t *testing.T) {
	e, p := externalImportTestPlan(t)
	var mu sync.Mutex
	gets, heads, terminalPosts := 0, 0, 0
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "HEAD" {
			heads++
			w.WriteHeader(400)
			return
		}
		if r.Method != "GET" || r.URL.Path != "/external-sql/"+p.Stage.S3Source.ObjectKey {
			t.Error("external GET changed literal object selection")
			w.WriteHeader(400)
			return
		}
		gets++
		w.Header().Set("ETag", `"literal-source"`)
		_, _ = io.WriteString(w, "CREATE TABLE retained(id INT);")
	}))
	defer s3.Close()
	p.S3Target.Target.Endpoint = s3.URL
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid protected source fixture")
	}
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "stage_lease", RunID: "db_import_stage_" + p.Stage.ID, RunnerID: p.RunnerID, Attempt: 1, Fence: "stage_fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: lease.RunID, ProjectID: p.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseImportStage, HostingOperationID: p.Stage.ID, HostingMode: "stage"}}, Lease: lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID}}
	path := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	var accepted domain.HostingDatabaseImportMutation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/hosting/database-import-stage/plan":
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v1/runners/lease":
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/hosting/database-import-stage/credentials":
			var in domain.BackupRemoteCredentialRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("invalid stage grant")
			}
			metadata := domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseImportCredentialPurpose, Request: in, RunnerID: p.RunnerID, RequestHash: domain.HostingDocumentHash(struct {
				Runner  string
				Request domain.BackupRemoteCredentialRequest
			}{p.RunnerID, in}), UseUntil: time.Now().Add(45 * time.Second)}
			credentials, e := domain.NewBackupRemoteCredentials(metadata, []byte("access"), []byte("secret"))
			if e != nil {
				t.Errorf("credential fixture invalid: request=%v metadata=%v prefix=%t hash=%t", in.Validate(), metadata.Validate(), in.Authority.RunID == "db_import_stage_"+in.Authority.OperationID, metadata.RequestHash == domain.HostingDocumentHash(struct {
					Runner  string
					Request domain.BackupRemoteCredentialRequest
				}{p.RunnerID, in}))
				w.WriteHeader(400)
				return
			}
			defer credentials.Close()
			w.Header().Set("Cache-Control", "no-store")
			_ = domain.WriteBackupRemoteCredentials(w, credentials)
		case "/api/v1/runners/hosting/database-import-stage/mutation":
			var in domain.HostingDatabaseImportMutation
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Validate() != nil {
				t.Error("invalid inspection receipt")
				w.WriteHeader(400)
				return
			}
			found := false
			for _, v := range j.Snapshot().DatabaseImports {
				found = found || domain.HostingDocumentHash(v.Request) == domain.HostingDocumentHash(in)
			}
			if !found {
				t.Error("inspection sent before durable append")
			}
			out := p.Stage
			out.State = "running"
			if in.Phase == "ready" {
				terminalPosts++
				if in.Ready == nil {
					t.Error("missing immutable ready receipt")
					w.WriteHeader(400)
					return
				}
				out, _ = in.Ready.StageView(p)
				if terminalPosts == 1 {
					accepted = in
					w.WriteHeader(503)
					return
				}
				if domain.HostingDocumentHash(accepted) != domain.HostingDocumentHash(in) {
					t.Error("ready replay changed exact mutation")
				}
			}
			_ = json.NewEncoder(w).Encode(domain.HostingDatabaseImportReceiptResult{Stage: &out})
		default:
			t.Error("unexpected inspection controller request")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, lease)
	config := DatabaseExecutionConfig{ExternalImports: true, TrustedLoopbackHTTP: true, RemoteFixtureOrigin: s3.URL}
	err = executeDatabaseImportWithEngine(s, j, func(context.Context) error { return nil }, server.URL, "token", claim, "", &config, func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error) { return e, nil })
	s.Close()
	if err == nil || len(j.Snapshot().DatabaseImports) != 2 {
		t.Fatal("lost inspection ACK discarded durable evidence", err)
	}
	if j.Close() != nil {
		t.Fatal("close journal")
	}
	reopened, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal("journal24/base1 restart", err)
	}
	mu.Lock()
	j = reopened
	mu.Unlock()
	if err = reconcileDatabaseImportJournal(ctx, Config{Server: server.URL, Token: "token", Database: &config}, j); err != nil {
		t.Fatal("exact receipt replay", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 1 || heads != 0 || terminalPosts != 2 || len(j.Snapshot().DatabaseImports) != 0 {
		t.Fatal("replay repeated GET, lost result or failed acknowledgement order")
	}
}

func TestExternalImportCoordinatorLostAckAndUncertainNeverRepeatSQL(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete ACK lost", true: "partial SQL held"}[partial], func(t *testing.T) {
			e, p := externalImportTestPlan(t)
			h, ip := externalImportTestReady(t, e, p)
			if h.Close() != nil {
				t.Fatal("close stage")
			}
			now := time.Now().UTC()
			lease := domain.RunLease{ID: "import_lease", RunID: "db_import_" + ip.Operation.ID, RunnerID: p.RunnerID, Attempt: 1, Fence: "import_fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: lease.RunID, ProjectID: p.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseImport, HostingOperationID: ip.Operation.ID, HostingMode: "import"}}, Lease: lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID}}
			path := filepath.Join(t.TempDir(), "journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			var mu sync.Mutex
			sqlCalls, terminalPosts := 0, 0
			intentSent := false
			var accepted domain.HostingDatabaseImportMutation
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-import/plan":
					_ = json.NewEncoder(w).Encode(ip)
				case "/api/v1/runners/lease":
					_ = json.NewEncoder(w).Encode(lease)
				case "/api/v1/runners/hosting/database-import/mutation":
					var in domain.HostingDatabaseImportMutation
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.Validate() != nil {
						t.Error("invalid SQL receipt")
						w.WriteHeader(400)
						return
					}
					found := false
					for _, v := range j.Snapshot().DatabaseImports {
						found = found || domain.HostingDocumentHash(v.Request) == domain.HostingDocumentHash(in)
					}
					if !found {
						t.Error("SQL receipt sent before durable append")
					}
					out := ip.Operation
					out.State = "running"
					if in.Phase == "import_intent" {
						intentSent = true
					} else {
						terminalPosts++
						out.WritersResumed = in.WritersResumed
						out.FailureCode = in.FailureCode
						if partial {
							out.State = "uncertain"
						} else {
							out.State = "succeeded"
							out.OwnershipHeld = false
						}
						if terminalPosts == 1 {
							accepted = in
							if !partial {
								w.WriteHeader(503)
								return
							}
						} else if domain.HostingDocumentHash(accepted) != domain.HostingDocumentHash(in) {
							t.Error("terminal replay changed exact SQL receipt")
						}
					}
					_ = json.NewEncoder(w).Encode(domain.HostingDatabaseImportReceiptResult{Import: &out})
				default:
					t.Error("unexpected import controller request")
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
				if in == nil {
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				sqlCalls++
				if !intentSent || len(j.Snapshot().DatabaseImports) != 1 {
					t.Error("SQL started before durable accepted intent")
				}
				if _, err := io.Copy(io.Discard, in); err != nil {
					return err
				}
				code := "0"
				if partial {
					code = "1"
				}
				_, err := io.WriteString(out, externalImportTerminal+code+"\n")
				return err
			}
			ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(ctx, lease)
			config := DatabaseExecutionConfig{ExternalImports: true, TrustedLoopbackHTTP: true}
			err = executeDatabaseImportWithEngine(s, j, func(context.Context) error { return nil }, server.URL, "token", claim, "", &config, func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error) { return e, nil })
			s.Close()
			if err == nil || len(j.Snapshot().DatabaseImports) != 2 {
				t.Fatal("terminal loss/uncertainty discarded evidence", err)
			}
			if j.Close() != nil {
				t.Fatal("close")
			}
			reopened, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal("reopen24", err)
			}
			mu.Lock()
			j = reopened
			mu.Unlock()
			err = reconcileDatabaseImportJournalWithCleanup(ctx, Config{Server: server.URL, Token: "token", Database: &config}, j, func(ctx context.Context, plan domain.HostingDatabaseImportPlan) error {
				return removeConsumedExternalImportStage(ctx, e.host, plan)
			}, nil)
			if partial {
				if err != errExternalImportHeld || len(j.Snapshot().DatabaseImports) != 2 || e.writersResumed {
					t.Fatal("partial SQL replay resumed or acknowledged uncertain work")
				}
			} else if err != nil || len(j.Snapshot().DatabaseImports) != 0 {
				t.Fatal("complete receipt replay/cleanup failed", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if sqlCalls != 1 || terminalPosts != 2 {
				t.Fatal("replay repeated SQL or new terminal mutation")
			}
		})
	}
}
