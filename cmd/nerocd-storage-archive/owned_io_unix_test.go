//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/unix"
	"nerocd/internal/storagearchive"
)

func closeArchiveTestStream(t *testing.T, stream io.Closer) {
	t.Helper()
	if err := stream.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Error("archive test stream cleanup failed")
	}
}

func TestOwnedIORejectsNonpollableStreams(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, file) })
	if _, err := newOwnedIO(t.Context(), file, false, nil); !errors.Is(err, errUnsupportedStream) {
		t.Fatal("regular file accepted as interruptible stream")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("rejected stream ownership was taken")
	}
	if _, err := newOwnedIO(t.Context(), nil, false, nil); !errors.Is(err, errUnsupportedStream) {
		t.Fatal("nil stream accepted")
	}
}

func TestOwnedIORestoresFlagsAndRetainsCloseFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, r); closeArchiveTestStream(t, w) })
	fd := int(r.Fd())
	duplicate, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(duplicate); err != nil {
			t.Error("duplicate stream close failed")
		}
	}()
	original, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := newOwnedIO(t.Context(), r, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFL, 0)
	if err != nil || changed&unix.O_NONBLOCK == 0 {
		t.Fatal("owned stream did not become nonblocking")
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFL, 0)
	if err != nil || restored != original {
		t.Fatal("stream flags were not restored before close")
	}
	if _, err := owned.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed descriptor was reused")
	}

	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, r2); closeArchiveTestStream(t, w2) })
	broken, err := newOwnedIO(t.Context(), r2, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately close this verified owned descriptor to force flag restoration
	// and close failure. No descriptor allocation occurs before broken.Close.
	if err := unix.Close(broken.fd); err != nil {
		t.Fatal(err)
	}
	first, second := broken.Close(), broken.Close()
	if first == nil || second != first || !errors.Is(first, unix.EBADF) {
		t.Fatal("restore/close failure was discarded")
	}
}

func TestOwnedIOSocketAndPipeSemantics(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(fds[0]), "owned-socket-read")
	writer := os.NewFile(uintptr(fds[1]), "owned-socket-write")
	t.Cleanup(func() { closeArchiveTestStream(t, reader); closeArchiveTestStream(t, writer) })
	in, err := newOwnedIO(t.Context(), reader, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	})
	out, err := newOwnedIO(t.Context(), writer, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := out.Close(); err != nil {
			t.Error(err)
		}
	})
	if n, err := out.Write([]byte("bounded")); err != nil || n != 7 {
		t.Fatal("socket write changed semantics")
	}
	data := make([]byte, 7)
	if _, err := io.ReadFull(in, data); err != nil || string(data) != "bounded" {
		t.Fatal("socket read changed semantics")
	}
	if _, err := out.Read(data); !errors.Is(err, os.ErrInvalid) {
		t.Fatal("wrong-direction stream read accepted")
	}
	if _, err := in.Write(data); !errors.Is(err, os.ErrInvalid) {
		t.Fatal("wrong-direction stream write accepted")
	}
}

func TestOwnedIOCancellationAtWouldBlock(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, r); closeArchiveTestStream(t, w) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	blocked := false
	in, err := newOwnedIO(ctx, r, false, func(stage string) { blocked = stage == "input"; cancel() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := in.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) || !blocked {
		t.Fatal("would-block input did not honor cancellation")
	}
}

func TestRunOwnedValidatesPipeArchive(t *testing.T) {
	input, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, input); closeArchiveTestStream(t, feed) })
	resultR, resultW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, resultR); closeArchiveTestStream(t, resultW) })
	// This small fixture fits the empty pipe, so no background writer is needed.
	archive := cliArchive(t)
	if len(archive) > 4096 {
		t.Fatal("pipe fixture exceeds synchronous write budget")
	}
	if _, err := feed.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := feed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runOwned(t.Context(), []string{"validate"}, input, resultW, nil); err != nil {
		t.Fatal(err)
	}
	var summary storagearchive.Summary
	if err := json.NewDecoder(resultR).Decode(&summary); err != nil || summary.Format != storagearchive.Format || summary.Members != 2 {
		t.Fatal("owned pipe validation lost the public summary")
	}
	var extra [1]byte
	if n, err := resultR.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("owned output was not closed after successful validation")
	}
	if _, err := input.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("owned input was not closed after successful validation")
	}
}

func TestOwnedIORejectsNonstreamSocketsBeforeChangingFlags(t *testing.T) {
	for _, kind := range []int{unix.SOCK_DGRAM, unix.SOCK_SEQPACKET} {
		name := "datagram"
		if kind == unix.SOCK_SEQPACKET {
			name = "sequence_packet"
		}
		t.Run(name, func(t *testing.T) {
			fds, err := unix.Socketpair(unix.AF_UNIX, kind, 0)
			if kind == unix.SOCK_SEQPACKET && (errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.ESOCKTNOSUPPORT)) {
				t.Skip("host does not support UNIX sequence-packet sockets")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(fds[0], true); err != nil {
				t.Fatal(err)
			}
			left := os.NewFile(uintptr(fds[0]), "nonstream-socket")
			right := os.NewFile(uintptr(fds[1]), "nonstream-peer")
			t.Cleanup(func() { closeArchiveTestStream(t, left); closeArchiveTestStream(t, right) })
			original, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, write := range []bool{false, true} {
				owned, err := newOwnedIO(t.Context(), left, write, nil)
				if owned != nil {
					closeArchiveTestStream(t, owned)
					t.Fatal("nonstream socket accepted as byte stream")
				}
				if !errors.Is(err, errUnsupportedStream) {
					t.Fatal("nonstream socket rejection lost its contract")
				}
				flags, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFL, 0)
				if err != nil || flags != original {
					t.Fatal("rejected nonstream socket flags or ownership changed")
				}
			}
		})
	}
}

func TestDiagnosticWritableAndUnsupportedSinks(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, r); closeArchiveTestStream(t, w) })
	if err := writeDiagnostic(w, "storage archive helper failed"); err != nil {
		t.Fatal("writable pipe lost diagnostic")
	}
	data, err := io.ReadAll(io.LimitReader(r, 128))
	if err != nil || string(data) != "storage archive helper failed\n" {
		t.Fatal("diagnostic text or owned close changed")
	}

	file, err := os.CreateTemp(t.TempDir(), "unsupported-diagnostic-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeArchiveTestStream(t, file) })
	if err := writeDiagnostic(file, "storage archive helper failed"); !errors.Is(err, errUnsupportedStream) {
		t.Fatal("unsupported diagnostic sink accepted")
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() != 0 {
		t.Fatal("unsupported sink was changed or closed")
	}
}
