//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// ownedIO has one synchronous user. Raw nonblocking syscalls avoid inherited
// os.File descriptors whose blocked reads cannot reliably be interrupted by
// Close. Poll never waits longer than 50ms before another context check.
type ownedIO struct {
	ctx       context.Context
	file      *os.File
	fd, flags int
	write     bool
	closed    bool
	closeErr  error
	notified  bool
	onBlocked func(string)
}

func newOwnedIO(ctx context.Context, file *os.File, write bool, blocked func(string)) (*ownedIO, error) {
	if file == nil {
		return nil, errUnsupportedStream
	}
	// Capture Fd once, before changing flags. Calling os.File.Fd again can
	// itself restore blocking mode for descriptors managed by Go's poller.
	fd := int(file.Fd())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect owned archive stream: %w", err)
	}
	kind := uint32(stat.Mode) & unix.S_IFMT
	if kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
		return nil, errUnsupportedStream
	}
	if kind == unix.S_IFSOCK {
		socketType, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil {
			return nil, fmt.Errorf("inspect owned socket type: %w", err)
		}
		if socketType != unix.SOCK_STREAM {
			return nil, errUnsupportedStream
		}
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("inspect owned stream flags: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		return nil, fmt.Errorf("make owned stream interruptible: %w", err)
	}
	return &ownedIO{ctx: ctx, file: file, fd: fd, flags: flags, write: write, onBlocked: blocked}, nil
}

func (s *ownedIO) Close() error {
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	_, restoreErr := unix.FcntlInt(uintptr(s.fd), unix.F_SETFL, s.flags)
	s.closeErr = errors.Join(restoreErr, s.file.Close())
	return s.closeErr
}

func wouldBlock(err error) bool {
	return errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK)
}

func (s *ownedIO) wait() error {
	if !s.notified && s.onBlocked != nil {
		s.notified = true
		stage := "input"
		if s.write {
			stage = "output"
		}
		s.onBlocked(stage)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	event := int16(unix.POLLIN)
	if s.write {
		event = unix.POLLOUT
	}
	fds := []unix.PollFd{{Fd: int32(s.fd), Events: event}}
	_, err := unix.Poll(fds, 50)
	if canceled := s.ctx.Err(); canceled != nil {
		return canceled
	}
	if errors.Is(err, unix.EINTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("poll owned archive stream: %w", err)
	}
	if fds[0].Revents&unix.POLLNVAL != 0 {
		return os.ErrClosed
	}
	if fds[0].Revents&unix.POLLERR != 0 || (s.write && fds[0].Revents&unix.POLLHUP != 0) {
		return io.ErrClosedPipe
	}
	return nil
}

func (s *ownedIO) Read(p []byte) (int, error) {
	if s.closed {
		return 0, os.ErrClosed
	}
	if s.write {
		return 0, os.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := unix.Read(s.fd, p)
		if n > 0 {
			return n, nil
		}
		if err == nil {
			return 0, io.EOF
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !wouldBlock(err) {
			return 0, fmt.Errorf("read owned archive stream: %w", err)
		}
		if err := s.wait(); err != nil {
			return 0, err
		}
	}
}

func (s *ownedIO) Write(p []byte) (int, error) {
	if s.closed {
		return 0, os.ErrClosed
	}
	if !s.write {
		return 0, os.ErrInvalid
	}
	written := 0
	for written < len(p) {
		if err := s.ctx.Err(); err != nil {
			return written, err
		}
		chunk := p[written:]
		if len(chunk) > 32<<10 {
			chunk = chunk[:32<<10]
		}
		n, err := unix.Write(s.fd, chunk)
		if n > 0 {
			written += n
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if wouldBlock(err) {
			if err := s.wait(); err != nil {
				return written, err
			}
			continue
		}
		if err != nil {
			return written, fmt.Errorf("write owned archive stream: %w", err)
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func runOwned(ctx context.Context, args []string, stdin, stdout *os.File, blocked func(string)) (err error) {
	if len(args) == 0 || (args[0] != "create" && args[0] != "validate" && args[0] != "restore") {
		return errors.New("invalid archive helper mode")
	}
	var input io.Reader
	if args[0] != "create" {
		owned, err := newOwnedIO(ctx, stdin, false, blocked)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, owned.Close()) }()
		input = owned
	}
	output, err := newOwnedIO(ctx, stdout, true, blocked)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	return run(ctx, args, input, output, os.OpenRoot, "/tmp")
}

// writeDiagnostic makes one nonblocking attempt to write fixed text. A full,
// closed or unsupported sink drops diagnostics rather than delaying failure.
// It never polls, retries or starts a goroutine, even without cancellation.
func writeDiagnostic(stderr *os.File, message string) (err error) {
	stream, err := newOwnedIO(context.Background(), stderr, true, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	data := []byte(message + "\n")
	n, err := unix.Write(stream.fd, data)
	if err != nil {
		return fmt.Errorf("write archive diagnostic: %w", err)
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
