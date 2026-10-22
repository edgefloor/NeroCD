package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func buildJournalFixture() (JournalBuild, JournalBuild, JournalBuild) {
	attempt := testJournalAuthority()
	attempt.RunID = "build_run_build-one"
	authority := domain.HostingBuildAuthority{BuildID: "build-one", RunID: attempt.RunID, LeaseID: attempt.LeaseID, Attempt: attempt.Attempt, Fence: attempt.Fence}
	digest := "sha256:" + strings.Repeat("a", 64)
	receipt := domain.HostingBuildSourceReceipt{BuildID: authority.BuildID, SourceID: "source_one", InputID: digest, Commit: strings.Repeat("b", 40), PolicyHash: digest, TreeDigest: digest}
	artifact := domain.HostingBuildArtifact{Origin: "local_build", BuildID: authority.BuildID, SourceID: receipt.SourceID, InputID: receipt.InputID, SourceReceiptHash: hostingbuildprotocol.SourceReceiptHash(receipt), ContextDigest: digest, RequestDigest: digest, DaemonID: "daemon_one", ImageID: digest, IndexDigest: digest, ManifestDigest: digest, ConfigDigest: digest, Platform: "linux/amd64"}
	intent := JournalBuild{ID: "build_intent", Attempt: attempt, BuildID: authority.BuildID, Phase: "attempt_intent", InputHash: digest, CreatedAt: attempt.CreatedAt}
	source := JournalBuild{ID: "build_source", Attempt: attempt, BuildID: authority.BuildID, Phase: "source", Source: &hostingbuildprotocol.SourceCommitRequest{Authority: authority, ReceiptID: "build_source", Receipt: receipt}, CreatedAt: attempt.CreatedAt}
	result := JournalBuild{ID: "build_result", Attempt: attempt, BuildID: authority.BuildID, Phase: "result", Result: &hostingbuildprotocol.CompleteRequest{Authority: authority, ReceiptID: "build_result", ExpectedRevision: 3, Completion: hostingbuildprotocol.Completion{State: "succeeded", Artifact: &artifact}}, CreatedAt: attempt.CreatedAt}
	return intent, source, result
}
func openBuildJournal(t *testing.T) (*AttemptJournal, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, directory
}

func TestBuildJournalV5ReopenRetainsExactReplayAndExplicitSettlement(t *testing.T) {
	j, directory := openBuildJournal(t)
	intent, source, result := buildJournalFixture()
	for _, entry := range []JournalBuild{intent, source, result} {
		if _, err := j.AppendBuild(entry); err != nil {
			t.Fatal(err)
		}
	}
	before := j.Snapshot()
	if j.Depth() != 3 || j.state.Version != 5 {
		t.Fatal("build depth/version not retained")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if !reflect.DeepEqual(reopened.Snapshot(), before) {
		t.Fatal("restart changed replay request")
	}
	for _, entry := range before.Build {
		got, e := reopened.AppendBuild(entry)
		if e != nil || !reflect.DeepEqual(got, entry) {
			t.Fatal("idempotent replay changed", e)
		}
	}
	expired := intent.Attempt
	expired.ExpiresAt = expired.ExpiresAt.Add(time.Hour)
	if err = reopened.DiscardAttempt(expired); err == nil || reopened.Depth() != 3 {
		t.Fatal("generic stale discard lost unresolved build records")
	}
	if err = reopened.AckBuild(source.ID); err != nil {
		t.Fatal(err)
	}
	// A standalone exact result remains replayable after source acknowledgment.
	if _, err = reopened.AppendBuild(result); err != nil {
		t.Fatal("acknowledged source was incorrectly required for replay", err)
	}
	if err = reopened.AckBuild(result.ID); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DiscardAttempt(intent.Attempt); err == nil {
		t.Fatal("remaining intent inferred settled from receipt removal")
	}
	if err = reopened.AckBuild(intent.ID); err != nil || reopened.Depth() != 0 {
		t.Fatal("explicit settlement failed", err)
	}
	if reopened.state.Version != 5 {
		t.Fatal("acknowledgment downgraded durable format")
	}
}

func TestBuildJournalMixedVersionsAndProxyCannotDowngrade(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			j, directory := openBuildJournal(t)
			j.state.Version = version
			event := JournalEvent{ID: "legacy_event", Attempt: testJournalAuthority(), Sequence: 1, Stream: "stdout", Message: "nonsecret", CreatedAt: time.Now().UTC()}
			if _, err := j.AppendEvent(event); err != nil {
				t.Fatal(err)
			}
			if j.state.Version != version {
				t.Fatal("non-build append upgraded old format")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenAttemptJournal(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			intent, _, _ := buildJournalFixture()
			if _, err = reopened.AppendBuild(intent); err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.AppendProxy(proxyJournalFixture(t)); err != nil || reopened.state.Version != 5 {
				t.Fatal("proxy append downgraded build journal", err)
			}
			if len(reopened.Snapshot().Events) != 1 || len(reopened.Snapshot().Build) != 1 || len(reopened.Snapshot().Proxy) != 1 {
				t.Fatal("mixed journal lost old records")
			}
		})
	}
	for _, version := range []int{1, 2, 3, 4, 8} {
		intent, _, _ := buildJournalFixture()
		if validateJournalState(journalState{Version: version, Build: []JournalBuild{intent}}) == nil {
			t.Fatal("build evidence accepted in wrong format")
		}
	}
	fresh, _ := openBuildJournal(t)
	if fresh.state.Version != 1 {
		t.Fatal("fresh journals unexpectedly use v5")
	}
}

func TestBuildJournalDeepCopiesAndIDConflicts(t *testing.T) {
	j, _ := openBuildJournal(t)
	_, source, result := buildJournalFixture()
	returned, err := j.AppendBuild(result)
	if err != nil {
		t.Fatal(err)
	}
	result.Result.Completion.Artifact.DaemonID = "caller_mutation"
	returned.Result.Completion.Artifact.SourceID = "returned_mutation"
	snapshot := j.Snapshot()
	snapshot.Build[0].Result.Completion.Artifact.ImageID = "snapshot_mutation"
	if j.Snapshot().Build[0].Result.Completion.Artifact.DaemonID != "daemon_one" || j.Snapshot().Build[0].Result.Completion.Artifact.SourceID != "source_one" {
		t.Fatal("artifact alias escaped journal")
	}
	if _, err = j.AppendBuild(source); err != nil {
		t.Fatal(err)
	}
	source.Source.Receipt.SourceID = "changed"
	if j.Snapshot().Build[1].Source.Receipt.SourceID != "source_one" {
		t.Fatal("source request aliases caller")
	}
	old := j.Snapshot().Build[1]
	old.CreatedAt = old.CreatedAt.Add(time.Second)
	if _, err = j.AppendBuild(old); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("same ID changed body accepted", err)
	}
}

func TestBuildJournalValidatesPayloadAndCrossEntryBindingsInEitherOrder(t *testing.T) {
	intent, source, result := buildJournalFixture()
	for _, test := range []struct {
		name   string
		entry  JournalBuild
		mutate func(*JournalBuild)
	}{
		{"intent_has_source", intent, func(e *JournalBuild) { e.Source = source.Source }},
		{"intent_hash", intent, func(e *JournalBuild) { e.InputHash = "repository URL" }},
		{"tuple", source, func(e *JournalBuild) { e.Source.Authority.Fence = "other" }},
		{"build_run", source, func(e *JournalBuild) { e.Attempt.RunID = "build_run_other" }},
		{"receipt_id", source, func(e *JournalBuild) { e.Source.ReceiptID = "other" }},
		{"commit", source, func(e *JournalBuild) { e.Source.Receipt.Commit = "main" }},
		{"source_digest", source, func(e *JournalBuild) { e.Source.Receipt.TreeDigest = "private transport diagnostic" }},
		{"oversize_document", source, func(e *JournalBuild) {
			e.Attempt.Fence = strings.Repeat("f", journalMaxProvenanceBytes)
			e.Source.Authority.Fence = e.Attempt.Fence
		}},
		{"missing_timestamp", intent, func(e *JournalBuild) { e.CreatedAt = time.Time{} }},
		{"result_revision", result, func(e *JournalBuild) { e.Result.ExpectedRevision = 0 }},
		{"result_extra_payload", result, func(e *JournalBuild) { e.Source = source.Source }},
		{"artifact_build", result, func(e *JournalBuild) { e.Result.Completion.Artifact.BuildID = "other" }},
		{"artifact_digest", result, func(e *JournalBuild) { e.Result.Completion.Artifact.ImageID = "not a digest" }},
		{"failure_code", result, func(e *JournalBuild) {
			e.Result.Completion = hostingbuildprotocol.Completion{State: "failed", FailureCode: "raw stderr"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := cloneJournalBuild(test.entry)
			test.mutate(&entry)
			if validateJournalBuild(entry) == nil {
				t.Fatal("malformed build record accepted")
			}
		})
	}
	for _, resultFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("result_first_%t", resultFirst), func(t *testing.T) {
			j, _ := openBuildJournal(t)
			_, src, res := buildJournalFixture()
			res.Result.Completion.Artifact.SourceReceiptHash = "sha256:" + strings.Repeat("f", 64)
			first, second := src, res
			if resultFirst {
				first, second = res, src
			}
			if _, err := j.AppendBuild(first); err != nil {
				t.Fatal(err)
			}
			before := j.Snapshot()
			if _, err := j.AppendBuild(second); !errors.Is(err, ErrJournalConflict) {
				t.Fatal("cross-entry source mismatch accepted", err)
			}
			if !reflect.DeepEqual(j.Snapshot(), before) {
				t.Fatal("failed cross-entry append changed state")
			}
		})
	}
}

func TestBuildJournalCrossFamilyConflictRollsBackBothAppendDirections(t *testing.T) {
	for _, buildFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(buildFirst), func(t *testing.T) {
			j, _ := openBuildJournal(t)
			intent, _, _ := buildJournalFixture()
			event := JournalEvent{ID: intent.ID, Attempt: intent.Attempt, Sequence: 1, Stream: "stdout", Message: "nonsecret", CreatedAt: intent.CreatedAt}
			if buildFirst {
				if _, err := j.AppendBuild(intent); err != nil {
					t.Fatal(err)
				}
				before := j.Snapshot()
				if _, err := j.AppendEvent(event); !errors.Is(err, ErrJournalConflict) {
					t.Fatal("event collided with build record", err)
				}
				if !reflect.DeepEqual(j.Snapshot(), before) {
					t.Fatal("event rollback changed state")
				}
				completion := JournalCompletion{ID: intent.ID, Attempt: intent.Attempt, Status: "failed", CreatedAt: intent.CreatedAt}
				if _, err := j.AppendCompletion(completion); !errors.Is(err, ErrJournalConflict) {
					t.Fatal("completion collided with build record", err)
				}
				// Older hosting appends must also roll back through the shared persist guard.
				hosted := JournalHosting{ID: intent.ID, Attempt: intent.Attempt, OperationID: "hosting_one", Phase: "result", Result: &domain.HostingCompleteRequest{HostingAuthority: domain.HostingAuthority{OperationID: "hosting_one", RunID: intent.Attempt.RunID, LeaseID: intent.Attempt.LeaseID, Attempt: intent.Attempt.Attempt, Fence: intent.Attempt.Fence}}, CreatedAt: intent.CreatedAt}
				if _, err := j.AppendHosting(hosted); !errors.Is(err, ErrJournalConflict) {
					t.Fatal("hosting entry collided with build record", err)
				}
				if !reflect.DeepEqual(j.Snapshot(), before) || j.state.Version != 5 {
					t.Fatal("cross-family persist failure changed build evidence")
				}

			} else {
				if _, err := j.AppendEvent(event); err != nil {
					t.Fatal(err)
				}
				before := j.Snapshot()
				version := j.state.Version
				if _, err := j.AppendBuild(intent); !errors.Is(err, ErrJournalConflict) {
					t.Fatal("build collided with event", err)
				}
				if !reflect.DeepEqual(j.Snapshot(), before) || j.state.Version != version {
					t.Fatal("build conflict failed version/state rollback")
				}
			}
		})
	}
}

func TestBuildJournalLimitsAndPersistenceFailureRollback(t *testing.T) {
	for _, mode := range []string{"append_closed", "ack_closed", "count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			j, directory := openBuildJournal(t)
			intent, _, _ := buildJournalFixture()
			if mode == "ack_closed" {
				if _, err := j.AppendBuild(intent); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "append_closed" || mode == "ack_closed" {
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "count" {
				j.state.Events = make([]JournalEvent, journalMaxEntries)
			}
			if mode == "bytes" {
				for i := range 40 {
					j.state.Events = append(j.state.Events, JournalEvent{ID: fmt.Sprintf("event_%d", i), Attempt: testJournalAuthority(), Sequence: i + 1, Stream: "stdout", Message: strings.Repeat("x", journalMaxEventBytes-64), CreatedAt: time.Now().UTC()})
				}
			}
			before := j.Snapshot()
			version := j.state.Version
			var err error
			if mode == "ack_closed" {
				err = j.AckBuild(intent.ID)
			} else {
				_, err = j.AppendBuild(intent)
			}
			if err == nil || !reflect.DeepEqual(j.Snapshot(), before) || j.state.Version != version {
				t.Fatal("failed journal operation changed durable intent state", err)
			}
			if mode == "ack_closed" {
				reopened, e := OpenAttemptJournal(directory)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				if len(reopened.Snapshot().Build) != 1 {
					t.Fatal("failed acknowledgment lost persisted build")
				}
			}
		})
	}
}

func TestBuildJournalConcurrentIdenticalAppendAndNonsecretEncoding(t *testing.T) {
	j, directory := openBuildJournal(t)
	intent, _, _ := buildJournalFixture()
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { _, err := j.AppendBuild(intent); failures <- err })
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if j.Depth() != 1 {
		t.Fatal("concurrent identical intent duplicated")
	}
	raw, err := os.ReadFile(filepath.Join(directory, journalStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Version int                          `json:"version"`
		Build   []map[string]json.RawMessage `json:"build"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Version != 5 || len(state.Build) != 1 || len(state.Build[0]) != 6 {
		t.Fatal("intent persisted more than exact authority/hash/identity fields")
	}
	for _, forbidden := range []string{"repository_url", "credential", "token", "workspace", "checkout_path", "source", "result"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Fatal("intent persisted transport or execution payload")
		}
	}
}
