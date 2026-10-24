//go:build linux || darwin

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"nerocd/internal/storagearchive"
)

func cliArchive(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, h := range []*tar.Header{
		{Name: ".", Typeflag: tar.TypeDir, Mode: 0700, Uid: os.Geteuid(), Gid: os.Getegid(), Uname: storagearchive.Format, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)},
		{Name: "private-upload-name", Typeflag: tar.TypeReg, Mode: 0600, Size: 14, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)},
	} {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write([]byte("private-upload")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestValidateModePrivateSpoolAndPublicSummary(t *testing.T) {
	spool := t.TempDir()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"validate"}, bytes.NewReader(cliArchive(t)), &out, func(string) (*os.Root, error) { t.Fatal("validate opened a workload mount"); return nil, nil }, spool); err != nil {
		t.Fatal(err)
	}
	var summary storagearchive.Summary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Format != storagearchive.Format || summary.Members != 2 {
		t.Fatal("invalid public summary")
	}
	if strings.Contains(out.String(), "private-upload") {
		t.Fatal("summary leaked archive content or member name")
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatal("private spool survived validation")
	}
}

type countedInput struct{ n int }

func (r *countedInput) Read(p []byte) (int, error) { clear(p); r.n += len(p); return len(p), nil }

func TestInputBudgetsAndClosedModes(t *testing.T) {
	spool := t.TempDir()
	open := func(string) (*os.Root, error) { t.Fatal("invalid input opened a workload mount"); return nil, nil }
	r := &countedInput{}
	err := run(t.Context(), []string{"validate", "--max-archive-bytes=1536", "--max-data-bytes=512", "--max-file-bytes=512"}, r, io.Discard, open, spool)
	if !errors.Is(err, storagearchive.ErrLimit) || r.n != 1537 {
		t.Fatal("input stream exceeded bounded spool budget")
	}
	for _, args := range [][]string{{}, {"shell"}, {"validate", "--input=/secret"}, {"restore", "/arbitrary"}, {"validate", "--max-members=0"}} {
		r := &countedInput{}
		if err := run(t.Context(), args, r, io.Discard, open, spool); err == nil || r.n != 0 {
			t.Fatal("invalid helper mode/options consumed input")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx, []string{"validate"}, &countedInput{}, io.Discard, open, spool); !errors.Is(err, context.Canceled) {
		t.Fatal("input spooling ignored cancellation")
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed input left a private spool")
	}
}

func TestCreateRestoreFixedMountRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filesystem helper requires Linux")
	}
	source, target, spool := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "upload"), []byte("client upload"), 0640); err != nil {
		t.Fatal(err)
	}
	open := func(name string) (*os.Root, error) {
		switch name {
		case "/source":
			return os.OpenRoot(source)
		case "/target":
			return os.OpenRoot(target)
		default:
			t.Fatal("helper requested arbitrary mount")
			return nil, nil
		}
	}
	var archive, summary bytes.Buffer
	if err := run(t.Context(), []string{"create"}, nil, &archive, open, spool); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"restore"}, bytes.NewReader(archive.Bytes()), &summary, open, spool); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "upload"))
	if err != nil || string(got) != "client upload" {
		t.Fatal("CLI restore changed upload")
	}
	if err := run(t.Context(), []string{"restore"}, bytes.NewReader(archive.Bytes()), io.Discard, open, spool); err == nil {
		t.Fatal("CLI reused populated target")
	}
}

func TestArchiveCommandChild(t *testing.T) {
	if os.Getenv("NEROCD_ARCHIVE_SIGNAL_CHILD") != "1" {
		return
	}
	os.Args = []string{"nerocd-storage-archive", "validate"}
	status := commandReady(func(stage string) {
		if stage != os.Getenv("NEROCD_ARCHIVE_SIGNAL_STAGE") {
			return
		}
		ready := os.NewFile(3, "archive-test-blocked")
		code := byte(1)
		if stage == "output" {
			code = 2
		}
		if _, err := ready.Write([]byte{code}); err != nil {
			t.Fatal("child blocked handshake failed")
		}
		if err := ready.Close(); err != nil {
			t.Fatal("child blocked handshake close failed")
		}
	})
	os.Exit(status)
}

// One goroutine owns Wait. Every setup failure and explicit stop uses the same
// retained result, and cancellation/kill is always followed by a bounded join.
type archiveTestProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startArchiveTestProcess(t *testing.T, cmd *exec.Cmd, cancel context.CancelFunc) *archiveTestProcess {
	t.Helper()
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal("archive child start failed")
	}
	p := &archiveTestProcess{cmd: cmd, cancel: cancel, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.stop(t) })
	return p
}

func (p *archiveTestProcess) stop(t *testing.T) {
	t.Helper()
	p.cancel()
	select {
	case <-p.done:
		return
	default:
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error("archive child kill failed")
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Error("archive child cleanup did not join")
	}
}

func (p *archiveTestProcess) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(2 * time.Second):
		t.Fatal("archive child did not promptly join")
	}
	return nil
}

func TestSignalInterruptsOwnedBlockedInputAndRedactsErrors(t *testing.T) {
	for _, stage := range []string{"input", "output"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			readyR, readyW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeArchiveTestStream(t, readyR); closeArchiveTestStream(t, readyW) })
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveCommandChild$")
			cmd.Env = append(os.Environ(), "NEROCD_ARCHIVE_SIGNAL_CHILD=1", "NEROCD_ARCHIVE_SIGNAL_STAGE="+stage)
			cmd.ExtraFiles = []*os.File{readyW}
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			var stdin io.WriteCloser
			var outputR, outputW *os.File
			var filled int
			if stage == "input" {
				stdin, err = cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeArchiveTestStream(t, stdin) })
			} else {
				cmd.Stdin = bytes.NewReader(cliArchive(t))
				outputR, outputW, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeArchiveTestStream(t, outputR); closeArchiveTestStream(t, outputW) })
				filled = fillArchiveTestPipe(t, outputW)
				cmd.Stdout = outputW
			}
			p := startArchiveTestProcess(t, cmd, cancel)
			if err := readyW.Close(); err != nil {
				t.Fatal("parent handshake close failed")
			}
			if err := readyR.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal("handshake deadline failed")
			}
			if stdin != nil {
				if _, err := stdin.Write([]byte("private-unfinished-upload")); err != nil {
					t.Fatal("child input failed")
				}
			}
			var blocked [1]byte
			if _, err := io.ReadFull(readyR, blocked[:]); err != nil {
				t.Fatal("archive command never reached would-block polling")
			}
			want := byte(1)
			if stage == "output" {
				want = 2
			}
			if blocked[0] != want {
				t.Fatal("archive command blocked on the wrong stream")
			}
			// The handshake is emitted after EAGAIN. Keep that pipe empty/full for
			// 100ms, then prove the process is still awaiting IO before SIGTERM.
			time.Sleep(100 * time.Millisecond)
			select {
			case <-p.done:
				t.Fatal("archive child exited before cancellation")
			default:
			}
			begin := time.Now()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal("archive child signal failed")
			}
			err = p.wait(t)
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 1 || time.Since(begin) > 2*time.Second {
				t.Fatal("signal did not promptly stop already-blocked owned IO")
			}
			if stderr.String() != "storage archive helper failed\n" {
				t.Fatal("command diagnostics leaked input or dynamic errors")
			}
			if stage == "input" {
				if out.Len() != 0 {
					t.Fatal("failed input produced archive output")
				}
			} else {
				if err := outputW.Close(); err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(io.LimitReader(outputR, int64(filled+1)))
				if err != nil || len(raw) != filled || !bytes.Equal(raw, bytes.Repeat([]byte{'x'}, filled)) {
					t.Fatal("blocked output wrote after cancellation")
				}
			}
			if again := p.wait(t); again != err {
				t.Fatal("child wait evidence was consumed")
			}
			p.stop(t)
		})
	}
}

func fillArchiveTestPipe(t *testing.T, file *os.File) int {
	t.Helper()
	fd := int(file.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	total := 0
	block := bytes.Repeat([]byte{'x'}, 4096)
	for {
		n, err := unix.Write(fd, block)
		if n > 0 {
			total += n
		}
		if wouldBlock(err) {
			break
		}
		if err != nil || n == 0 || total > 1<<20 {
			t.Fatal("unable to bound full output pipe")
		}
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags); err != nil {
		t.Fatal(err)
	}
	return total
}

func TestArchiveChildCleanupAfterSetupFailures(t *testing.T) {
	for _, step := range []string{"ready_close", "read_deadline"} {
		t.Run(step, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeArchiveTestStream(t, r); closeArchiveTestStream(t, w) })
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveCommandChild$")
			cmd.Env = append(os.Environ(), "NEROCD_ARCHIVE_SIGNAL_CHILD=1")
			cmd.ExtraFiles = []*os.File{w}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeArchiveTestStream(t, stdin) })
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			p := startArchiveTestProcess(t, cmd, cancel)
			// These are failures at the two setup boundaries that previously had
			// no Wait cleanup. The shared cleanup already owns the started child.
			if step == "ready_close" {
				if err := w.Close(); err != nil {
					t.Fatal("initial handshake close failed")
				}
				if err := w.Close(); err == nil {
					t.Fatal("close fault did not occur")
				}
			} else {
				if err := r.Close(); err != nil {
					t.Fatal("initial handshake close failed")
				}
				if err := r.SetReadDeadline(time.Now()); err == nil {
					t.Fatal("deadline fault did not occur")
				}
			}
			p.stop(t)
			if p.wait(t) == nil {
				t.Fatal("setup-failure child unexpectedly succeeded")
			}
		})
	}
}

func TestCommandWithFullStderr(t *testing.T) {
	for _, failure := range []string{"signal", "malformed_input"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			errR, errW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeArchiveTestStream(t, errR); closeArchiveTestStream(t, errW) })
			filled := fillArchiveTestPipe(t, errW)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveCommandChild$")
			cmd.Env = append(os.Environ(), "NEROCD_ARCHIVE_SIGNAL_CHILD=1")
			cmd.Stdout, cmd.Stderr = io.Discard, errW
			var readyR, readyW *os.File
			var stdin io.WriteCloser
			if failure == "signal" {
				readyR, readyW, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeArchiveTestStream(t, readyR); closeArchiveTestStream(t, readyW) })
				cmd.ExtraFiles = []*os.File{readyW}
				cmd.Env = append(cmd.Env, "NEROCD_ARCHIVE_SIGNAL_STAGE=input")
				stdin, err = cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeArchiveTestStream(t, stdin) })
			} else {
				cmd.Stdin = strings.NewReader("private-malformed-archive-bytes")
			}
			p := startArchiveTestProcess(t, cmd, cancel)
			if failure == "signal" {
				if err := readyW.Close(); err != nil {
					t.Fatal("parent handshake close failed")
				}
				if err := readyR.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal("handshake deadline failed")
				}
				if _, err := stdin.Write([]byte("private-unfinished-upload")); err != nil {
					t.Fatal("child input failed")
				}
				var ready [1]byte
				if _, err := io.ReadFull(readyR, ready[:]); err != nil || ready[0] != 1 {
					t.Fatal("child never reached blocked input")
				}
				time.Sleep(100 * time.Millisecond)
				select {
				case <-p.done:
					t.Fatal("child exited before signal")
				default:
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal("child signal failed")
				}
			}
			select {
			case <-p.done:
				var exited *exec.ExitError
				if !errors.As(p.err, &exited) || exited.ExitCode() != 1 {
					t.Fatal("unexpected child exit")
				}
			case <-time.After(300 * time.Millisecond):
				p.stop(t)
				t.Fatal("full diagnostic sink prevented prompt failure; child killed and joined")
			}
			if err := errW.Close(); err != nil {
				t.Fatal("parent diagnostic close failed")
			}
			data, err := io.ReadAll(io.LimitReader(errR, int64(filled+1)))
			if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{'x'}, filled)) {
				t.Fatal("unavailable sink received unexpected diagnostic bytes")
			}
			if p.wait(t) != p.err {
				t.Fatal("child exit result was consumed")
			}
		})
	}
}
