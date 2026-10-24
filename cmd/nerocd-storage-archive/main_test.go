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
	status := commandReady(func() {
		ready := os.NewFile(3, "archive-test-ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			t.Fatal("child handshake failed")
		}
		if err := ready.Close(); err != nil {
			t.Fatal("child handshake close failed")
		}
	})
	os.Exit(status)
}

func TestSignalInterruptsOwnedBlockedInputAndRedactsErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := readyR.Close(); err != nil {
			t.Error(err)
		}
	}()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveCommandChild$")
	cmd.Env = append(os.Environ(), "NEROCD_ARCHIVE_SIGNAL_CHILD=1")
	cmd.ExtraFiles = []*os.File{readyW}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = stdin.Close() })
	if err := readyW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := readyR.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var started [1]byte
	if _, err := io.ReadFull(readyR, started[:]); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal("archive command never became ready")
	}
	if _, err := stdin.Write([]byte("private-unfinished-upload")); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal("child input failed")
	}
	begin := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 1 || time.Since(begin) > 2*time.Second {
		t.Fatal("signal did not promptly stop blocked archive input")
	}
	if out.Len() != 0 || stderr.String() != "storage archive helper failed\n" {
		t.Fatal("command diagnostics leaked input or dynamic errors")
	}
	if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}
