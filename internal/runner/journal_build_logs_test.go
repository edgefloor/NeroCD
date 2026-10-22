package runner

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func logAck(b domain.HostingBuildLogBatch) domain.HostingBuildLogAck {
	a := domain.HostingBuildLogAck{Authority: b.Authority}
	for _, e := range b.Events {
		a.Receipts = append(a.Receipts, domain.HostingBuildLogReceipt{EventKey: e.EventKey, RequestedSequence: e.Sequence, Sequence: e.Sequence, BodyHash: domain.HostingBuildLogBodyHash(b.Authority, e)})
	}
	return a
}

func TestBuildLogJournalCursorReopenAndExactSettlement(t *testing.T) {
	j, path := openBuildJournal(t)
	intent, _, result := buildJournalFixture()
	if _, err := j.AppendBuild(intent); err != nil {
		t.Fatal(err)
	}
	entry, err := j.AppendBuildLog(intent.Attempt, intent.BuildID, "buildkit", "stdout", "output")
	if err != nil {
		t.Fatal(err)
	}
	before := j.Snapshot()
	if j.state.Version != 7 || j.Depth() != 3 {
		t.Fatal("missing v7 cursor/event")
	}
	bad := logAck(entry.Batch)
	bad.Receipts[0].BodyHash = "wrong"
	if j.AckBuildLogs(entry.Batch, bad) == nil || !reflect.DeepEqual(before, j.Snapshot()) {
		t.Fatal("bad ack removed evidence")
	}
	if _, err = j.AppendBuild(result); err != nil {
		t.Fatal(err)
	}
	proof := hostingbuildprotocol.ReceiptAcknowledgment{Authority: result.Result.Authority, Phase: "complete", ReceiptID: result.ID, BodyHash: hostingbuildprotocol.CompletionHash(result.Result.Completion)}
	if j.AckBuildCompletion(result.ID, proof) == nil {
		t.Fatal("completion removed pending logs")
	}
	if err = j.AckBuildLogs(entry.Batch, logAck(entry.Batch)); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().BuildLogCursors) != 1 {
		t.Fatal("ack removed cursor")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	next, err := j.AppendBuildLog(intent.Attempt, intent.BuildID, "build", "system", "next")
	if err != nil || next.Batch.Events[0].Sequence != 2 {
		t.Fatal("restart reused sequence", err)
	}
	snapshot := j.Snapshot()
	snapshot.BuildLogs[0].Batch.Events[0].Message = "changed"
	if j.Snapshot().BuildLogs[0].Batch.Events[0].Message != "next" {
		t.Fatal("snapshot aliases journal")
	}
	if err = j.AckBuildLogs(next.Batch, logAck(next.Batch)); err != nil {
		t.Fatal(err)
	}
	badProof := proof
	badProof.Authority.Fence = "other"
	if j.AckBuildCompletion(result.ID, badProof) == nil {
		t.Fatal("foreign completion settled")
	}
	if err = j.AckBuildCompletion(result.ID, proof); err != nil {
		t.Fatal(err)
	}
	if j.Depth() != 0 {
		t.Fatal("completion did not atomically settle authority")
	}
}

func TestBuildLogJournalAllocationRaceAndMixedVersions(t *testing.T) {
	j, _ := openBuildJournal(t)
	intent, _, _ := buildJournalFixture()
	if _, err := j.AppendBuild(intent); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := j.AppendBuildLog(intent.Attempt, intent.BuildID, "build", "system", "parallel"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i, e := range j.Snapshot().BuildLogs {
		if e.Batch.Events[0].Sequence != i+1 {
			t.Fatal("allocation reordered")
		}
	}
	for version := 1; version <= 7; version++ {
		s := j.state
		s.Version = version
		if (validateJournalState(s) == nil) != (version == 7) {
			t.Fatalf("v%d accepted logs incorrectly", version)
		}
		legacy := journalState{Version: version}
		if validateJournalState(legacy) != nil {
			t.Fatalf("v%d legacy rejected", version)
		}
	}
	if err := j.DiscardAttempt(intent.Attempt); err == nil {
		t.Fatal("generic discard removed logs")
	}
	if _, err := j.AppendEvent(JournalEvent{ID: "generic", Attempt: intent.Attempt, Sequence: 1, Stream: "stdout", Message: "bypass"}); err == nil {
		t.Fatal("generic bypass accepted")
	}
}

func TestBuildLogJournalFailureRollbackAndBounds(t *testing.T) {
	for _, mode := range []string{"closed", "count", "bytes", "ack_closed", "completion_closed", "global_id"} {
		t.Run(mode, func(t *testing.T) {
			j, _ := openBuildJournal(t)
			intent, _, result := buildJournalFixture()
			if _, err := j.AppendBuild(intent); err != nil {
				t.Fatal(err)
			}
			e, err := j.AppendBuildLog(intent.Attempt, intent.BuildID, "build", "system", "first")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "count":
				for i := 0; j.entryCountLocked() < journalMaxEntries; i++ {
					at := testJournalAuthority()
					at.RunID = "other"
					j.state.Events = append(j.state.Events, JournalEvent{ID: fmt.Sprint(i), Attempt: at, Sequence: i + 1, Stream: "stdout", Message: "a"})
				}
			case "bytes":
				at := testJournalAuthority()
				at.RunID = "other"
				for i := 0; i < 40; i++ {
					j.state.Events = append(j.state.Events, JournalEvent{ID: fmt.Sprint(i), Attempt: at, Sequence: i + 1, Stream: "stdout", Message: strings.Repeat("x", journalMaxEventBytes-64)})
				}
			case "completion_closed":
				if err = j.AckBuildLogs(e.Batch, logAck(e.Batch)); err != nil {
					t.Fatal(err)
				}
				if _, err = j.AppendBuild(result); err != nil {
					t.Fatal(err)
				}
				fallthrough
			case "closed", "ack_closed":
				if err = j.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := j.Snapshot()
			version := j.state.Version
			switch mode {
			case "ack_closed":
				err = j.AckBuildLogs(e.Batch, logAck(e.Batch))
			case "completion_closed":
				err = j.AckBuildCompletion(result.ID, hostingbuildprotocol.ReceiptAcknowledgment{Authority: result.Result.Authority, Phase: "complete", ReceiptID: result.ID, BodyHash: hostingbuildprotocol.CompletionHash(result.Result.Completion)})
			case "global_id":
				_, err = j.AppendCompletion(JournalCompletion{ID: e.ID, Attempt: intent.Attempt, Status: "failed"})
			default:
				_, err = j.AppendBuildLog(intent.Attempt, intent.BuildID, "buildkit", "stderr", "next")
			}
			if err == nil || !reflect.DeepEqual(before, j.Snapshot()) || j.state.Version != version {
				t.Fatal("failed write changed durable state", err)
			}
		})
	}
}
