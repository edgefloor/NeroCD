package hostingbuild

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestObservedCommandStreamsAndNilCompatibility(t *testing.T) {
	for _, nilSink := range []bool{false, true} {
		t.Run(map[bool]string{false: "observer", true: "legacy"}[nilSink], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			logs := &cappedWriter{remaining: 100, cancel: cancel}
			streams := map[OutputStream]string{}
			var sink OutputSink = func(_ context.Context, output Output) error {
				streams[output.Stream] += string(output.Data)
				return nil
			}
			if nilSink {
				sink = nil
			}
			observer := newOutputObserver(ctx, cancel, sink)
			e := &Engine{}
			if err := e.observedCommand(ctx, t.TempDir(), logs, observer, OutputBuildKit, false, "/bin/sh", "-c", "printf out; printf err >&2"); err != nil {
				t.Fatal(err)
			}
			if logs.remaining != 94 || !strings.Contains(logs.b.String(), "out") || !strings.Contains(logs.b.String(), "err") {
				t.Fatalf("incorrect aggregate capture: %q remaining=%d", logs.b.String(), logs.remaining)
			}
			if nilSink && len(streams) != 0 || !nilSink && (streams[OutputStdout] != "out" || streams[OutputStderr] != "err") {
				t.Fatalf("streams=%v", streams)
			}
		})
	}
}

func TestObservedCommandFailureCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var active atomic.Int32
	observer := newOutputObserver(ctx, cancel, func(context.Context, Output) error {
		active.Add(1)
		defer active.Add(-1)
		return errors.New("SECRET sink diagnostics")
	})
	logs := &cappedWriter{remaining: 10000, cancel: cancel}
	e := &Engine{}
	start := time.Now()
	err := e.observedCommand(ctx, t.TempDir(), logs, observer, OutputBuildKit, false, "/bin/sh", "-c", "printf SECRET; while :; do :; done")
	if err != ErrOutputSink || strings.Contains(err.Error(), "SECRET") || time.Since(start) >= 3*time.Second || active.Load() != 0 {
		t.Fatalf("observer command lifecycle: err=%v active=%d elapsed=%s", err, active.Load(), time.Since(start))
	}
}

func TestCapturedPlannerOutputExcludesCommandDiagnostics(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var outputs []Output
	observer := newOutputObserver(ctx, cancel, func(_ context.Context, output Output) error {
		outputs = append(outputs, output)
		return nil
	})
	logs := &cappedWriter{remaining: 100, cancel: cancel}
	e := &Engine{}
	if err := e.observedCommand(ctx, t.TempDir(), logs, observer, OutputRailpack, true, "/bin/sh", "-c", "printf planner; printf diagnostics >&2"); err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || string(outputs[0].Data) != "planner" || outputs[0].Component != OutputRailpack || outputs[0].Stream != OutputSystem {
		t.Fatalf("captured output=%+v", outputs)
	}
	if logs.remaining != 82 {
		t.Fatalf("unobserved diagnostics escaped aggregate budget: %d", logs.remaining)
	}
}

func TestObservedCommandCancellationJoinsCooperativeSink(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	var active atomic.Int32
	observer := newOutputObserver(ctx, cancel, func(ctx context.Context, _ Output) error {
		active.Add(1)
		defer active.Add(-1)
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	logs := &cappedWriter{remaining: 1 << 20, cancel: cancel}
	e := &Engine{}
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		done <- e.observedCommand(ctx, dir, logs, observer, OutputBuildKit, false, "/bin/sh", "-c", "while :; do printf x; done")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("command never reached sink")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || active.Load() != 0 {
			t.Fatalf("canceled command not joined: err=%v active=%d", err, active.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled command did not join cooperative sink")
	}
}
