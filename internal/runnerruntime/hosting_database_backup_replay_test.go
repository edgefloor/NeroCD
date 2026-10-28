//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func backupJournalEntries(t *testing.T, p domain.HostingDatabaseBackupPlan) (runner.JournalDatabaseBackup, runner.JournalDatabaseBackup) {
	t.Helper()
	now := time.Now().UTC()
	a := domain.HostingAuthority{OperationID: p.Operation.ID, RunID: "db_backup_" + p.Operation.ID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	identity := runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	intent := runner.JournalDatabaseBackup{ID: "intent", Attempt: identity, Plan: p, Request: domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, Phase: "intent"}, CreatedAt: now}
	if p.Component != nil {
		intent.Request.ComponentVersion = p.Operation.Version
		intent.Request.WritersResumed = true
	}
	complete := intent
	complete.ID = "complete"
	complete.Request.ReceiptID = "complete"
	complete.Request.Phase = "complete"
	complete.Request.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
	return intent, complete
}
func TestDatabaseBackupJournalRestartLostAcknowledgmentAndNoEffectReplay(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql", "mysql_component", "postgresql_component"} {
		t.Run(engine, func(t *testing.T) {
			var p domain.HostingDatabaseBackupPlan
			if engine == "mysql_component" || engine == "postgresql_component" {
				_, p, _, _ = componentBackupEngineFixtureFor(t, "success", engine == "mysql_component")
			} else {
				_, _, p = backupEngineFixture(t, engine)
			}
			if engine == "postgresql_component" {
				p.SourcePlan.Stack.Definition.Services[1].Environment = map[string]string{"POSTGRES_USER": "postgres"}
				p.SourcePlan.Snapshot.Stack.Hash = domain.HostingDocumentHash(p.SourcePlan.Stack.Definition)
				p.Source.Snapshot = p.SourcePlan.Snapshot
				p.SourcePlan.Hash = ""
				p.SourcePlan.Hash = domain.HostingDocumentHash(p.SourcePlan)
				p.Source.PlanHash = p.SourcePlan.Hash
				p.Hash = ""
				p.Hash = domain.HostingDocumentHash(p)
			}
			intent, complete := backupJournalEntries(t, p)
			path := filepath.Join(t.TempDir(), "attempt.journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.AppendDatabaseBackup(intent); err != nil {
				t.Fatal(err)
			}
			if _, err = j.AppendDatabaseBackup(complete); err != nil {
				t.Fatal(err)
			}
			bad := complete
			bad.Request.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 65537, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
			if _, err = j.AppendDatabaseBackup(bad); err == nil {
				t.Fatal("journal ID reused with changed result")
			}
			if _, err = j.AppendCompletion(runner.JournalCompletion{ID: "generic", Attempt: intent.Attempt, Status: "succeeded", CreatedAt: time.Now().UTC()}); err == nil {
				t.Fatal("generic completion bypassed family receipts")
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal("journal18 restart", err)
			}
			defer func() { _ = j.Close() }()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/database-backup/mutation" {
					t.Errorf("replay attempted effect or claim: %s", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				var in domain.HostingDatabaseBackupMutation
				if json.NewDecoder(r.Body).Decode(&in) != nil || domain.HostingDocumentHash(in) != domain.HostingDocumentHash(complete.Request) {
					t.Error("replay changed historical full request")
					w.WriteHeader(400)
					return
				}
				out := p.Operation
				out.CreatedAt = time.Now().UTC()
				out.UpdatedAt = out.CreatedAt
				out.Consistency = "logical_snapshot_source_writable"
				out.State = "succeeded"
				out.OwnershipHeld = false
				out.Blob = in.Blob
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			c := Config{Server: server.URL, Token: "runner", Database: &DatabaseExecutionConfig{LogicalBackups: true, TrustedLoopbackHTTP: true}}
			if err = reconcileDatabaseBackupJournal(t.Context(), c, j); err != nil {
				t.Fatal("exact ACK replay", err)
			}
			if requests != 1 || len(j.Snapshot().DatabaseBackup) != 0 {
				t.Fatal("replay did not retire exact family evidence")
			}
		})
	}
}
func TestDatabaseBackupJournalIntentOnlyNeverRepeatsImport(t *testing.T) {
	_, _, p := backupEngineFixture(t)
	intent, _ := backupJournalEntries(t, p)
	j, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err = j.AppendDatabaseBackup(intent); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer s.Close()
	if err = reconcileDatabaseBackupJournal(context.Background(), Config{Server: s.URL, Token: "runner", Database: &DatabaseExecutionConfig{LogicalBackups: true, TrustedLoopbackHTTP: true}}, j); err == nil {
		t.Fatal("intent inferred committed effect")
	}
	if calls != 0 || len(j.Snapshot().DatabaseBackup) != 1 {
		t.Fatal("intent was sent or dropped")
	}
}

func TestDatabaseBackupCoordinatorJournalsBeforeToolAndLostCompleteAck(t *testing.T) {
	e, f, p := backupEngineFixture(t)
	now := time.Now().UTC()
	p.Operation.CreatedAt = now
	p.Operation.UpdatedAt = now
	p.Operation.Consistency = "logical_snapshot_source_writable"
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	lease := domain.RunLease{ID: "lease", RunID: "db_backup_" + p.Operation.ID, RunnerID: p.RunnerID, Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	claim := domain.ClaimedRun{Lease: lease, Run: domain.TaskRun{ID: lease.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackup, HostingOperationID: p.Operation.ID, HostingMode: "backup"}}, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID}}
	path := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	effects, completePosts := 0, 0
	var accepted domain.HostingDatabaseBackupMutation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/database-backup/plan":
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v1/runners/lease":
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/hosting/database-backup/mutation":
			var in domain.HostingDatabaseBackupMutation
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("invalid mutation")
				w.WriteHeader(400)
				return
			}
			entries := j.Snapshot().DatabaseBackup
			found := false
			for _, entry := range entries {
				if domain.HostingDocumentHash(entry.Request) == domain.HostingDocumentHash(in) {
					found = true
				}
			}
			if !found {
				t.Error("send before durable journal")
			}
			out := p.Operation
			if in.Phase == "intent" {
				if effects != 0 {
					t.Error("tool ran before controller intent")
				}
				out.State = "applying"
			} else {
				completePosts++
				if effects != 1 {
					t.Error("completion before single tool")
				}
				accepted = in
				out.State = "succeeded"
				out.OwnershipHeld = false
				out.Blob = in.Blob
				if completePosts == 1 {
					w.WriteHeader(503)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Error("unexpected protocol endpoint", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		effects++
		entries := j.Snapshot().DatabaseBackup
		if len(entries) != 1 || entries[0].Request.Phase != "intent" {
			t.Error("effect without closed durable intent")
		}
		_, err := out.Write([]byte("PGDMPfixture"))
		return err
	}
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, lease)
	err = executeDatabaseBackupWithEngine(s, j, func(context.Context) error { return nil }, server.URL, "runner", claim, "", &DatabaseExecutionConfig{LogicalBackups: true}, func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error) { return e, nil })
	s.Close()
	if err == nil || effects != 1 || accepted.Phase != "complete" || len(j.Snapshot().DatabaseBackup) != 2 {
		t.Fatal("lost ACK did not preserve exact result", err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if err = reconcileDatabaseBackupJournal(ctx, Config{Server: server.URL, Token: "runner", Database: &DatabaseExecutionConfig{LogicalBackups: true, TrustedLoopbackHTTP: true}}, j); err != nil {
		t.Fatal("restart receipt replay", err)
	}
	if effects != 1 || completePosts != 2 || len(j.Snapshot().DatabaseBackup) != 0 || f.creates != 1 {
		t.Fatal("restart repeated tool or lost evidence")
	}
}

func TestDatabaseBackupReceiptCannotRetireForeignProjection(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			_, _, p := backupEngineFixture(t, engine)
			_, entry := backupJournalEntries(t, p)
			out := p.Operation
			out.State = "succeeded"
			out.OwnershipHeld = false
			out.Consistency = "logical_snapshot_source_writable"
			out.Blob = entry.Request.Blob
			if !databaseBackupReceiptMatches(p, entry.Request, out) {
				t.Fatal("exact immutable receipt rejected")
			}
			for _, mutate := range []func(*domain.HostingDatabaseBackupOperation){
				func(v *domain.HostingDatabaseBackupOperation) {
					if v.Version == domain.HostingDatabaseMySQLBackupVersion {
						v.Version = domain.HostingDatabaseBackupVersion
					} else {
						v.Version = domain.HostingDatabaseMySQLBackupVersion
					}
				}, func(v *domain.HostingDatabaseBackupOperation) { v.SourceResourceID = "foreign" }, func(v *domain.HostingDatabaseBackupOperation) { v.ProjectID = "foreign" }, func(v *domain.HostingDatabaseBackupOperation) { v.BackupID = "foreign" }, func(v *domain.HostingDatabaseBackupOperation) { v.TargetResourceID = "foreign" }, func(v *domain.HostingDatabaseBackupOperation) { v.Mode = "restore" }, func(v *domain.HostingDatabaseBackupOperation) { v.MaxBytes-- }, func(v *domain.HostingDatabaseBackupOperation) { v.TimeoutSeconds++ }, func(v *domain.HostingDatabaseBackupOperation) { v.OwnershipHeld = true },
			} {
				changed := out
				mutate(&changed)
				if databaseBackupReceiptMatches(p, entry.Request, changed) {
					t.Fatal("foreign receipt permitted journal retirement")
				}
			}
		})
	}
}

func TestDatabaseBackupCoordinatorRetainsUncertainStageAndNeverRepeatsImport(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			e, f, p := backupRestoreEngineFixture(t, engine)
			initialCreates := f.creates
			now := time.Now().UTC()
			p.Operation.CreatedAt, p.Operation.UpdatedAt = now, now
			p.Operation.Consistency = "logical_snapshot_source_writable"
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			lease := domain.RunLease{ID: "lease", RunID: "db_backup_" + p.Operation.ID, RunnerID: p.RunnerID, Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
			claim := domain.ClaimedRun{Lease: lease, Run: domain.TaskRun{ID: lease.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackup, HostingOperationID: p.Operation.ID, HostingMode: "restore"}}, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID}}
			path := filepath.Join(t.TempDir(), "journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			mutations := make(chan domain.HostingDatabaseBackupMutation, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-backup/plan":
					_ = json.NewEncoder(w).Encode(p)
				case "/api/v1/runners/lease":
					_ = json.NewEncoder(w).Encode(lease)
				case "/api/v1/runners/hosting/database-backup/mutation":
					var in domain.HostingDatabaseBackupMutation
					if json.NewDecoder(r.Body).Decode(&in) != nil {
						t.Error("invalid mutation")
						w.WriteHeader(400)
						return
					}
					found := false
					for _, entry := range j.Snapshot().DatabaseBackup {
						found = found || domain.HostingDocumentHash(entry.Request) == domain.HostingDocumentHash(in)
					}
					if !found {
						t.Error("uncertain result sent before durable append")
					}
					mutations <- in
					out := p.Operation
					out.State = "applying"
					if in.Phase == "uncertain" {
						out.State, out.FailureCode = "uncertain", "execution_failed"
					}
					_ = json.NewEncoder(w).Encode(out)
				default:
					t.Error("unexpected effect or generic completion endpoint")
					w.WriteHeader(400)
				}
			}))
			t.Cleanup(server.Close)
			imports := 0
			e.stream = func(_ context.Context, args []string, input io.Reader, _ io.Writer) error {
				if strings.Contains(strings.Join(args, " "), "--single-transaction") || (slices.Contains(args, "mysql") && input != nil) {
					imports++
					return errors.New("arbitrary private SQL/password/path canary")
				}
				return nil
			}
			ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			supervisor := newAttemptSupervisorFrom(ctx, lease)
			err = executeDatabaseBackupWithEngine(supervisor, j, func(context.Context) error { return nil }, server.URL, "runner", claim, "", &DatabaseExecutionConfig{LogicalBackups: true}, func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error) { return e, nil })
			supervisor.Close()
			if err == nil || err.Error() != "database backup execution failed at execution/restore" || !errors.Is(err, errDatabaseBackupHeld) || imports != 1 {
				t.Fatalf("coordinator lost safe stage or changed effects: %v", err)
			}
			for _, phase := range []string{"intent", "uncertain"} {
				select {
				case in := <-mutations:
					if in.Phase != phase || in.PlanHash != p.Hash || phase == "uncertain" && in.FailureCode != "execution_failed" {
						t.Fatal("stage diagnostics changed receipt identity or failure policy")
					}
				default:
					t.Fatal("expected durable receipt missing")
				}
			}
			if len(j.Snapshot().DatabaseBackup) != 2 {
				t.Fatal("uncertain intent/result was acknowledged away")
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = reconcileDatabaseBackupJournal(ctx, Config{Server: server.URL, Token: "runner", Database: &DatabaseExecutionConfig{LogicalBackups: true, TrustedLoopbackHTTP: true}}, j); !errors.Is(err, errDatabaseBackupHeld) || imports != 1 || len(mutations) != 0 || len(j.Snapshot().DatabaseBackup) != 2 || f.creates != initialCreates {
				t.Fatal("restart repeated import, sent mutation or retired uncertain ownership")
			}
		})
	}
}
