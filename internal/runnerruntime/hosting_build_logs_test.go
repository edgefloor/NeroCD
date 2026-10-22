package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runner"
)

type observedBuildEngine struct {
	buildEngineFunc
	output func(context.Context, hostingbuild.OutputSink) error
}

func (e observedBuildEngine) BuildWithOutput(ctx context.Context, r hostingbuild.Request, sink hostingbuild.OutputSink) (hostingbuild.Result, error) {
	if err := e.output(ctx, sink); err != nil {
		return hostingbuild.Result{}, hostingbuild.ErrOutputSink
	}
	return e.buildEngineFunc(ctx, r)
}

type logOverrideClient struct {
	*buildFixtureClient
	post func(context.Context, domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error)
}

func (c logOverrideClient) BuildEvents(ctx context.Context, b domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	return c.post(ctx, b)
}

func TestBuildOutputRedactsEverySplitBeforeJournalAndTransport(t *testing.T) {
	for _, secret := range []string{"token-secret", base64.StdEncoding.EncodeToString([]byte("token-secret"))} {
		for split := 0; split <= len(secret); split++ {
			t.Run(secret+"/"+string(rune('a'+split)), func(t *testing.T) {
				e, f, a, at := buildFixture(t)
				if err := e.setBuildLogSecrets("token-secret"); err != nil {
					t.Fatal(err)
				}
				if _, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				f.beforeLogs = func(b domain.HostingBuildLogBatch) error {
					raw, _ := json.Marshal(e.journal.Snapshot())
					if strings.Contains(string(raw), secret) || strings.Contains(b.Events[0].Message, secret) {
						t.Error("secret crossed persistence boundary")
					}
					return nil
				}
				s, err := newBuildOutputAdapter(e, a, at)
				if err != nil {
					t.Fatal(err)
				}
				for _, chunk := range []string{"prefix " + secret[:split], secret[split:] + " suffix"} {
					if err = s.Sink(t.Context(), hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte(chunk)}); err != nil {
						t.Fatal(err)
					}
				}
				if err = s.Finish(t.Context()); err != nil {
					t.Fatal(err)
				}
				var text strings.Builder
				for _, event := range f.logs {
					text.WriteString(event.Message)
					if event.SourceTimestamp != nil {
						t.Fatal("invented timestamp")
					}
				}
				if text.String() != "prefix "+runner.RedactionMarker+" suffix" {
					t.Fatalf("redaction changed text: %q", text.String())
				}
			})
		}
	}
}

func TestBuildOutputUTF8FramingAndFiniteStreams(t *testing.T) {
	e, f, a, at := buildFixture(t)
	_, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("é", 9000) + "\x00\xff終"
	for i := 0; i < len(input); {
		n := min(16383, len(input)-i)
		if err = s.Sink(t.Context(), hostingbuild.Output{Component: hostingbuild.OutputRailpack, Stream: hostingbuild.OutputSystem, Data: []byte(input[i : i+n])}); err != nil {
			t.Fatal(err)
		}
		i += n
		for _, tail := range s.tails {
			if len(tail) > 3 {
				t.Fatal("unbounded UTF8 tail")
			}
		}
	}
	if err = s.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, event := range f.logs {
		if !utf8.ValidString(event.Message) || len(event.Message) > 16384 || strings.ContainsRune(event.Message, 0) {
			t.Fatal("invalid UTF8 frame")
		}
		got.WriteString(event.Message)
	}
	if got.String() != strings.Repeat("é", 9000)+"��終" {
		t.Fatal("UTF8 framing changed valid content")
	}
	if s.Sink(t.Context(), hostingbuild.Output{Component: "invented", Stream: "stdout"}) == nil {
		t.Fatal("unbounded component accepted")
	}
}

func TestBuildOutputResponseLossRetriesExactBeforeCompletion(t *testing.T) {
	e, f, a, at := buildFixture(t)
	lost := false
	e.client = logOverrideClient{f, func(ctx context.Context, b domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
		ack, err := f.BuildEvents(ctx, b)
		if err == nil && !lost {
			lost = true
			return domain.HostingBuildLogAck{}, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("response lost")}
		}
		return ack, err
	}}
	e.engine = observedBuildEngine{e.engine.(buildEngineFunc), func(ctx context.Context, sink hostingbuild.OutputSink) error {
		return sink(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStderr, Data: []byte("actual build output\n")})
	}}
	f.beforeComplete = func() {
		if len(e.journal.Snapshot().BuildLogs) != 0 {
			t.Error("completion overtook output")
		}
		if len(f.logs) != 2 {
			t.Error("build output missing before completion")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := e.Execute(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	if !lost || len(f.logRequests) != 3 || domain.HostingDocumentHash(f.logRequests[0]) != domain.HostingDocumentHash(f.logRequests[1]) || len(f.logs) != 2 || e.journal.Depth() != 0 {
		t.Fatal("response loss changed identity or duplicated output")
	}
}

func TestBuildOutputInvalidAckCancellationAndReplayRetainEvidence(t *testing.T) {
	for _, mode := range []string{"invalid_ack", "cancel", "authority", "old_history"} {
		t.Run(mode, func(t *testing.T) {
			e, f, a, at := buildFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			e.client = logOverrideClient{f, func(c context.Context, b domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
				switch mode {
				case "cancel":
					cancel()
					return domain.HostingBuildLogAck{}, context.Canceled
				case "authority", "old_history":
					return domain.HostingBuildLogAck{}, errors.New("conflict")
				default:
					ack, err := f.BuildEvents(c, b)
					if err == nil {
						ack.Receipts[0].BodyHash = "changed"
					}
					return ack, err
				}
			}}
			if e.Execute(ctx, a, at) == nil || len(f.completeRequests) != 0 {
				t.Fatal("delivery failure produced ordinary completion")
			}
			pending := e.journal.Snapshot()
			if len(pending.BuildLogs) != 1 || len(pending.Build) != 1 || len(pending.BuildLogCursors) != 1 {
				t.Fatal("failure lost intent/log/cursor")
			}
			// Replay drains the exact retained event but must leave unresolved intent.
			e.client = f
			if e.Reconcile(t.Context()) == nil || len(e.journal.Snapshot().BuildLogs) != 0 || len(f.logs) != 1 || len(f.completeRequests) != 0 {
				t.Fatal("replay recreated execution or failed exact delivery")
			}
		})
	}
}

func TestBuildLogReplayPrecedesRetainedResultAndNeverBuilds(t *testing.T) {
	e, f, a, at := buildFixture(t)
	f.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil {
		t.Fatal("expected lost completion")
	}
	f.mu.Lock()
	f.live = true
	f.build.State = "building"
	f.mu.Unlock()
	entry, err := e.journal.AppendBuildLog(at, a.BuildID, "build", "system", "retained final output")
	if err != nil {
		t.Fatal(err)
	}
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		t.Fatal("replay called engine")
		return hostingbuild.Result{}, nil
	})
	f.beforeComplete = func() {
		if len(e.journal.Snapshot().BuildLogs) != 0 {
			t.Error("result overtook retained logs")
		}
		f.mu.Lock()
		f.build.State = "succeeded"
		f.mu.Unlock()
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.logs[len(f.logs)-1].EventKey != entry.ID || e.journal.Depth() != 0 {
		t.Fatal("replay changed event or failed settlement")
	}
	if err = e.Reconcile(t.Context()); err != nil || len(f.logs) != 2 {
		t.Fatal("second replay duplicated output", err)
	}
}

func TestBuildLogReplayDoesNotInferNewAuthorityFromCompletionProof(t *testing.T) {
	e, f, a, at := buildFixture(t)
	f.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil {
		t.Fatal("expected lost completion")
	}
	if _, err := e.journal.AppendBuildLog(at, a.BuildID, "build", "system", "unacknowledged"); err != nil {
		t.Fatal(err)
	}
	if e.Reconcile(t.Context()) == nil || len(e.journal.Snapshot().BuildLogs) != 1 {
		t.Fatal("historical completion proof authorized logs")
	}
}

func TestBuildLogHistoricalReceiptRecoveryAfterAuthorityLoss(t *testing.T) {
	e, f, a, at := buildFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	e.client = logOverrideClient{f, func(c context.Context, b domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
		_, err := f.BuildEvents(c, b)
		if err != nil {
			return domain.HostingBuildLogAck{}, err
		}
		cancel()
		return domain.HostingBuildLogAck{}, context.Canceled
	}}
	if e.Execute(ctx, a, at) == nil || len(f.logs) != 1 || len(e.journal.Snapshot().BuildLogs) != 1 {
		t.Fatal("lost committed log was not retained")
	}
	f.mu.Lock()
	f.live = false
	f.cancelRequested = true
	f.mu.Unlock()
	e.client = f
	if e.Reconcile(t.Context()) == nil {
		t.Fatal("unresolved execution intent must block startup")
	}
	if len(e.journal.Snapshot().BuildLogs) != 0 || len(f.logs) != 1 || len(f.completeRequests) != 0 {
		t.Fatal("historical log replay allocated output or completion")
	}
	if len(f.logRequests) != 2 || domain.HostingDocumentHash(f.logRequests[0]) != domain.HostingDocumentHash(f.logRequests[1]) {
		t.Fatal("historical replay changed request")
	}
}

func TestBuildLogReplayUncommittedAndChangedIntentBlock(t *testing.T) {
	for _, mode := range []string{"uncommitted", "changed_intent", "foreign_tuple"} {
		t.Run(mode, func(t *testing.T) {
			e, f, a, at := buildFixture(t)
			_, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.journal.AppendBuildLog(at, a.BuildID, "build", "system", "pending"); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "uncommitted":
				f.live = false
			case "foreign_tuple":
				f.authority.Fence = "other"
			default:
				f.build.Snapshot.Source.RequestedRef = "changed"
				f.build.Snapshot.InputID = ""
				f.build.Snapshot.InputID = domain.HostingDocumentHash(f.build.Snapshot)
			}
			if e.Reconcile(t.Context()) == nil || len(e.journal.Snapshot().BuildLogs) != 1 || len(f.logs) != 0 {
				t.Fatal("unverified replay changed evidence")
			}
			if mode == "changed_intent" && len(f.logRequests) != 0 {
				t.Fatal("changed immutable intent reached transport")
			}
		})
	}
}

type phaseOnlyClient struct{ BuildPhaseClient }
type outputlessEngine struct{ localBuildEngine }

func TestBuildLogPreflightAndCanceledTailNeverStartEffects(t *testing.T) {
	for _, mode := range []string{"transport", "engine"} {
		t.Run(mode, func(t *testing.T) {
			e, _, a, at := buildFixture(t)
			if mode == "transport" {
				e.client = phaseOnlyClient{e.client}
			} else {
				e.engine = outputlessEngine{e.engine}
			}
			e.lock = func(string, string) (func(), error) { t.Fatal("preflight started effects"); return nil, nil }
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if e.Execute(ctx, a, at) == nil || e.journal.Depth() != 0 {
				t.Fatal("missing output capability silently disabled logging")
			}
		})
	}
	e, f, a, at := buildFixture(t)
	if err := e.setBuildLogSecrets("token-secret"); err != nil {
		t.Fatal(err)
	}
	_, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Sink(t.Context(), hostingbuild.Output{Component: hostingbuild.OutputBuild, Stream: hostingbuild.OutputSystem, Data: []byte("token-")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.Finish(ctx) == nil || len(f.logs) != 0 || len(e.journal.Snapshot().BuildLogs) != 0 {
		t.Fatal("canceled operation flushed buffered secret prefix")
	}
}

func TestBuildLogEscapedFramesStayWithinCanonicalBatchBound(t *testing.T) {
	e, f, a, at := buildFixture(t)
	_, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []hostingbuild.OutputComponent{hostingbuild.OutputBuild, hostingbuild.OutputBuildKit, hostingbuild.OutputRailpack} {
		for _, stream := range []hostingbuild.OutputStream{hostingbuild.OutputSystem, hostingbuild.OutputStdout, hostingbuild.OutputStderr} {
			if err = s.Sink(t.Context(), hostingbuild.Output{Component: component, Stream: stream, Data: []byte(strings.Repeat("\x01<", 8192))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, b := range f.logRequests {
		raw, err := json.Marshal(b)
		if err != nil || len(raw) > domain.HostingBuildLogByteLimit || len(b.Events) > domain.HostingBuildLogBatchLimit {
			t.Fatal("escaped batch exceeded bound", err)
		}
	}
	if err = s.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = domain.CanonicalHostingBuildLogs(domain.HostingBuildLogBatch{Authority: a, Events: make([]domain.HostingBuildLogEvent, 65)}); err == nil {
		t.Fatal("oversized batch accepted")
	}
}

func TestBuildLogJournalFailureNeverCompletesBuild(t *testing.T) {
	e, f, a, at := buildFixture(t)
	e.engine = observedBuildEngine{e.engine.(buildEngineFunc), func(ctx context.Context, sink hostingbuild.OutputSink) error {
		if err := e.journal.Close(); err != nil {
			t.Fatal(err)
		}
		return sink(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte("not persisted")})
	}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil || len(f.completeRequests) != 0 || len(e.journal.Snapshot().Build) != 1 || len(e.journal.Snapshot().BuildLogCursors) != 1 {
		t.Fatal("journal failure erased intent or completed build")
	}
}
