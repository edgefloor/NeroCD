package hostingbuild

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// ErrOutputSink reports an observer failure without exposing its error or data.
var ErrOutputSink = errors.New("build output observer failed")

// OutputComponent identifies the producer of selected build text.
type OutputComponent string

const (
	// OutputBuild identifies engine messages.
	OutputBuild OutputComponent = "build"
	// OutputBuildKit identifies Buildx build output.
	OutputBuildKit OutputComponent = "buildkit"
	// OutputRailpack identifies captured Railpack planner output.
	OutputRailpack OutputComponent = "railpack"
)

// OutputStream identifies the source stream; captured merged output uses system.
type OutputStream string

const (
	// OutputSystem identifies engine or captured merged messages.
	OutputSystem OutputStream = "system"
	// OutputStdout identifies a command's standard output.
	OutputStdout OutputStream = "stdout"
	// OutputStderr identifies a command's standard error.
	OutputStderr OutputStream = "stderr"
)

// Output contains raw bytes owned by the receiver, with at most 16 KiB per call.
// Chunks may split UTF-8 sequences and may contain secrets from source or tools.
type Output struct {
	Component OutputComponent
	Stream    OutputStream
	Data      []byte
}

// OutputSink synchronously consumes output. It must honor context cancellation.
// Calls are serialized; receivers may retain or modify Data. A trusted adapter
// must redact and frame these raw bytes before persisting or delivering them.
type OutputSink func(context.Context, Output) error

type outputObserver struct {
	ctx    context.Context
	cancel context.CancelFunc
	sink   OutputSink
	gate   chan struct{}
	failed atomic.Bool
}

func newOutputObserver(ctx context.Context, cancel context.CancelFunc, sink OutputSink) *outputObserver {
	if sink == nil {
		return nil
	}
	return &outputObserver{ctx: ctx, cancel: cancel, sink: sink, gate: make(chan struct{}, 1)}
}

func (o *outputObserver) emit(component OutputComponent, stream OutputStream, p []byte) error {
	if o == nil {
		return nil
	}
	select {
	case o.gate <- struct{}{}:
		defer func() { <-o.gate }()
	case <-o.ctx.Done():
		return o.ctx.Err()
	}
	for len(p) > 0 {
		if o.failed.Load() {
			return ErrOutputSink
		}
		if err := o.ctx.Err(); err != nil {
			return err
		}
		n := min(len(p), 16<<10)
		chunk := append([]byte(nil), p[:n]...)
		if err := o.sink(o.ctx, Output{Component: component, Stream: stream, Data: chunk}); err != nil {
			o.failed.Store(true)
			o.cancel()
			return ErrOutputSink
		}
		p = p[n:]
	}
	return nil
}

type observedWriter struct {
	budget    *cappedWriter
	observer  *outputObserver
	component OutputComponent
	stream    OutputStream
}

func (w observedWriter) Write(p []byte) (int, error) {
	n, err := w.budget.Write(p)
	if err != nil {
		return n, err
	}
	// The budget lock is released before calling external code.
	if err = w.observer.emit(w.component, w.stream, p); err != nil {
		return n, err
	}
	return n, nil
}

func (e *Engine) observedCommand(ctx context.Context, dir string, logs *cappedWriter, observer *outputObserver, component OutputComponent, stdoutOnly bool, binary string, args ...string) error {
	if observer == nil {
		return e.command(ctx, dir, logs, binary, args...)
	}
	var stderr io.Writer = observedWriter{logs, observer, component, OutputStderr}
	stream := OutputStdout
	if stdoutOnly {
		stream = OutputSystem
		stderr = logs
	}
	stdout := observedWriter{logs, observer, component, stream}
	err := e.commandStreams(ctx, dir, stdout, stderr, binary, args...)
	if observer.failed.Load() {
		return ErrOutputSink
	}
	return err
}
