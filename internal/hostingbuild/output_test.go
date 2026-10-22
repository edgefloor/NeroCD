package hostingbuild

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutputCopiesChunksAndSharesBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	payload := bytes.Repeat([]byte("€"), 20000)
	var received []byte
	var retained [][]byte
	budget := &cappedWriter{remaining: int64(len(payload)), cancel: cancel}
	observer := newOutputObserver(ctx, cancel, func(_ context.Context, output Output) error {
		if output.Component != OutputBuildKit || output.Stream != OutputStdout || len(output.Data) > 16<<10 {
			t.Errorf("invalid output frame: component=%s stream=%s length=%d", output.Component, output.Stream, len(output.Data))
		}
		// A callback can acquire the buffer lock, and owns its mutable payload.
		budget.mu.Lock()
		if budget.remaining != 0 {
			t.Errorf("callback ran before aggregate capture: remaining=%d", budget.remaining)
		}
		budget.mu.Unlock()
		received = append(received, output.Data...)
		retained = append(retained, output.Data)
		output.Data[0] = 0
		return nil
	})
	writer := observedWriter{budget, observer, OutputBuildKit, OutputStdout}
	if n, err := writer.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	if !bytes.Equal(received, payload) || !bytes.Equal(budget.b.Bytes(), payload) {
		t.Fatal("observer mutation changed source or buffered output")
	}
	for _, p := range retained {
		if p[0] != 0 {
			t.Fatal("retained chunks alias subsequent callback data")
		}
	}
	if _, err := writer.Write([]byte("x")); err == nil || ctx.Err() == nil {
		t.Fatal("shared aggregate budget did not cancel overflow")
	}
}

func TestOutputSerializesConcurrentStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var active atomic.Int32
	var calls atomic.Int32
	observer := newOutputObserver(ctx, cancel, func(_ context.Context, _ Output) error {
		if active.Add(1) != 1 {
			t.Error("concurrent callback")
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		calls.Add(1)
		return nil
	})
	budget := &cappedWriter{remaining: 100, cancel: cancel}
	var wg sync.WaitGroup
	for _, stream := range []OutputStream{OutputStdout, OutputStderr} {
		wg.Go(func() {
			writer := observedWriter{budget, observer, OutputBuildKit, stream}
			for range 10 {
				if _, err := writer.Write([]byte("x")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if calls.Load() != 20 {
		t.Fatalf("callbacks=%d", calls.Load())
	}
}

func TestOutputCancellationReleasesQueuedCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	observer := newOutputObserver(ctx, cancel, func(ctx context.Context, _ Output) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	first := make(chan error, 1)
	go func() { first <- observer.emit(OutputBuildKit, OutputStdout, []byte("x")) }()
	<-entered
	queued := make(chan error, 1)
	go func() { queued <- observer.emit(OutputBuildKit, OutputStderr, []byte("y")) }()
	cancel()
	for _, done := range []chan error{first, queued} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("canceled output succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("canceled callback not joined")
		}
	}
}

func TestOutputFailureIsFixedAndStopsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	observer := newOutputObserver(ctx, cancel, func(context.Context, Output) error {
		calls++
		return errors.New("private sink error and secret chunk")
	})
	err := observer.emit(OutputRailpack, OutputSystem, []byte("private output"))
	if !errors.Is(err, ErrOutputSink) || strings.Contains(err.Error(), "private") || ctx.Err() == nil {
		t.Fatalf("unsafe observer error: %v", err)
	}
	if err = observer.emit(OutputRailpack, OutputSystem, []byte("more")); err == nil || calls != 1 {
		t.Fatalf("callback after failure: calls=%d err=%v", calls, err)
	}
}
