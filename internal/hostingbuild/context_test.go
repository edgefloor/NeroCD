package hostingbuild

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{CPUQuota: 100000, MemoryBytes: 1 << 30, SwapBytes: 0, Deadline: 3 * time.Minute, ContextBytes: 8 << 20, ContextFiles: 1000, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 256 << 20, UnpackedBytes: 512 << 20, UsageBytes: 3 << 30, ReserveBytes: 128 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}
}
func writeTestFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestContextBoundaries(t *testing.T) {
	for _, kind := range []string{"escape", "internal-link", "bytes", "files", "clean"} {
		t.Run(kind, func(t *testing.T) {
			src := t.TempDir()
			dst := filepath.Join(t.TempDir(), "context")
			l := testLimits()
			writeTestFile(t, filepath.Join(src, "hello"), []byte("hello"))
			writeTestFile(t, filepath.Join(src, ".env"), []byte("SECRET"))
			if err := os.Mkdir(filepath.Join(src, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(src, ".git", "config"), []byte("credential"))
			switch kind {
			case "escape":
				if err := os.Symlink("/etc/passwd", filepath.Join(src, "bad")); err != nil {
					t.Fatal(err)
				}
			case "internal-link":
				if err := os.Symlink("hello", filepath.Join(src, "bad")); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				l.ContextBytes = 4
			case "files":
				l.ContextFiles = 0
			}
			digest, err := stageContext(context.Background(), src, dst, l)
			if kind != "clean" {
				if err == nil {
					t.Fatal("unsafe/unbounded context accepted")
				}
				return
			}
			if err != nil || !digestPattern.MatchString(digest) {
				t.Fatalf("stage: %s %v", digest, err)
			}
			for _, name := range []string{".env", ".git"} {
				if _, err = os.Stat(filepath.Join(dst, name)); !os.IsNotExist(err) {
					t.Fatalf("excluded file copied: %s", name)
				}
			}
			writeTestFile(t, filepath.Join(src, "hello"), []byte("changed"))
			next, err := stageContext(context.Background(), src, filepath.Join(t.TempDir(), "context"), l)
			if err != nil || next == digest {
				t.Fatalf("changed source not detected: %v", err)
			}
		})
	}
}
func TestRequestRejectsUnsupportedLimits(t *testing.T) {
	r := Request{BuildID: "build", Checkout: "/source", SourceID: "commit", InputID: "input", DaemonID: "daemon", Strategy: Dockerfile, Platform: "linux/arm64", Limits: testLimits()}
	if err := r.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"quota", "", "unlimited"} {
		r.Limits.DiskMode = mode
		if err := r.validate(context.Background()); err == nil {
			t.Fatalf("accepted disk mode %q", mode)
		}
	}
}
func TestStateDirectoryRejectsSymlinkAndPermissions(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(target); err == nil {
		t.Fatal("shared directory accepted")
	}
}

func TestOutputBudgetCancelsWithoutBufferGrowth(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &cappedWriter{remaining: 3, cancel: cancel}
	if n, err := out.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("bounded write %d %v", n, err)
	}
	if _, err := out.Write([]byte("overflow")); err == nil {
		t.Fatal("output limit not enforced")
	}
	if ctx.Err() == nil || out.b.String() != "abc" {
		t.Fatal("overflow did not cancel with bounded retained output")
	}
}
