//go:build linux

package storagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestRoundTripPinnedRootsAndNumericMetadata(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filesystem helper requires Linux")
	}
	source, sourceName := testRoot(t)
	if err := source.Mkdir("uploads", 0750); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteFile("uploads/example.txt", []byte("retained upload contents\x00"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteFile("empty", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.Chmod(".", 0750); err != nil {
		t.Fatal(err)
	}
	// A root handle continues referring to the pinned directory after rename.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(sourceName, moved); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	written, err := Write(t.Context(), source, &raw, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f := archiveFile(t, raw.Bytes())
	validated, err := Validate(t.Context(), f, DefaultLimits())
	if err != nil || validated != written || written.Members != 4 {
		t.Fatalf("archive validation mismatch: %v", err)
	}
	target, _ := testRoot(t)
	extracted, err := Extract(t.Context(), f, target, DefaultLimits())
	if err != nil || extracted != written {
		t.Fatalf("restore mismatch: %v", err)
	}
	for _, name := range []string{".", "uploads", "uploads/example.txt", "empty"} {
		want, err := source.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := target.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		a, b := want.Sys().(*syscall.Stat_t), got.Sys().(*syscall.Stat_t)
		if want.Mode() != got.Mode() || a.Uid != b.Uid || a.Gid != b.Gid {
			t.Fatal("numeric ownership or ordinary mode changed")
		}
	}
	got, err := target.ReadFile("uploads/example.txt")
	if err != nil || !bytes.Equal(got, []byte("retained upload contents\x00")) {
		t.Fatal("restored payload changed")
	}
	if _, err := target.Lstat(IncompleteMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful restore retained failure marker")
	}
	if _, err := Extract(t.Context(), f, target, DefaultLimits()); err == nil || errors.Is(err, ErrPartialRestore) {
		t.Fatal("nonempty target was reused or mutated")
	}
}

func TestChangedArchiveProducesPartialTargetAndRefusesRetry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filesystem helper requires Linux")
	}
	raw := testArchive(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600, Size: 8})
	f := archiveFile(t, raw)
	expected, err := Validate(t.Context(), f, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("changed!"), 1024); err != nil {
		t.Fatal(err)
	}
	target, _ := testRoot(t)
	if _, err := extractValidated(t.Context(), f, target, DefaultLimits(), expected); !errors.Is(err, ErrPartialRestore) || !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("changed archive did not mark partial restore: %v", err)
	}
	if _, err := target.Stat(IncompleteMarker); err != nil {
		t.Fatal("partial restore marker missing")
	}
	if _, err := Extract(t.Context(), f, target, DefaultLimits()); err == nil || errors.Is(err, ErrPartialRestore) {
		t.Fatal("partial target was reused")
	}
}

type cancelWriter struct {
	cancel context.CancelFunc
	bytes  int
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	w.bytes += len(p)
	w.cancel()
	return len(p), nil
}

func TestContextAndWriteBudgets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filesystem helper requires Linux")
	}
	source, _ := testRoot(t)
	if err := source.WriteFile("file", []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	w := &cancelWriter{cancel: cancel}
	if _, err := Write(ctx, source, w, DefaultLimits()); !errors.Is(err, context.Canceled) || w.bytes > 512 {
		t.Fatal("write did not honor cooperative cancellation")
	}
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	if _, err := Validate(ctx, archiveFile(t, testArchive(t)), DefaultLimits()); !errors.Is(err, context.Canceled) {
		t.Fatal("validation ignored cancellation")
	}
	l := DefaultLimits()
	l.Members = 1
	if _, err := Write(t.Context(), source, io.Discard, l); !errors.Is(err, ErrLimit) {
		t.Fatal("source member budget ignored")
	}
}

func TestSourceLinksSpecialSparseAndExtendedMetadataRejected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filesystem helper requires Linux")
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo", "sparse", "xattr", "acl", "privilege", "marker"} {
		t.Run(kind, func(t *testing.T) {
			source, name := testRoot(t)
			if err := source.WriteFile("file", []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = source.Symlink("file", "link")
			case "hardlink":
				err = source.Link("file", "link")
			case "fifo":
				err = unix.Mkfifo(filepath.Join(name, "pipe"), 0600)
			case "sparse":
				err = os.Truncate(filepath.Join(name, "file"), 1<<20)
			case "xattr":
				err = unix.Setxattr(filepath.Join(name, "file"), "user.nerocd_test", []byte("x"), 0)
			case "acl":
				// Linux POSIX ACL xattr v2: an explicit additional named user.
				acl := make([]byte, 4+5*8)
				binary.LittleEndian.PutUint32(acl, 2)
				for i, entry := range [][3]uint32{{1, 6, ^uint32(0)}, {2, 4, uint32(os.Geteuid() + 1)}, {4, 0, ^uint32(0)}, {16, 4, ^uint32(0)}, {32, 0, ^uint32(0)}} {
					binary.LittleEndian.PutUint16(acl[4+i*8:], uint16(entry[0]))
					binary.LittleEndian.PutUint16(acl[6+i*8:], uint16(entry[1]))
					binary.LittleEndian.PutUint32(acl[8+i*8:], entry[2])
				}
				err = unix.Setxattr(filepath.Join(name, "file"), "system.posix_acl_access", acl, 0)
			case "privilege":
				err = source.Chmod("file", 0600|os.ModeSetuid)
			case "marker":
				err = source.WriteFile(IncompleteMarker, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Write(t.Context(), source, io.Discard, DefaultLimits()); err == nil {
				t.Fatal("unsupported source metadata accepted")
			}
		})
	}
}

func TestUnsafeOrLinkedTargetDoesNotMutateOutside(t *testing.T) {
	f := archiveFile(t, testArchive(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600, Size: 8}))
	for _, mode := range []os.FileMode{0777, 0770, 0500} {
		target, _ := testRoot(t)
		if err := target.Chmod(".", mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Extract(t.Context(), f, target, DefaultLimits()); err == nil || errors.Is(err, ErrPartialRestore) {
			t.Fatal("unsafe target was mutated")
		}
	}
	target, _ := testRoot(t)
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := target.Symlink(outside, "file"); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(t.Context(), f, target, DefaultLimits()); err == nil || errors.Is(err, ErrPartialRestore) {
		t.Fatal("linked populated target was mutated")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "preserve" {
		t.Fatal("restore touched outside sentinel")
	}
}

func TestUnprivilegedOwnerChangeFailsInsteadOfDroppingMetadata(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged ownership failure")
	}
	raw := testArchive(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600, Uid: os.Geteuid() + 1, Gid: os.Getegid()})
	target, _ := testRoot(t)
	if _, err := Extract(t.Context(), archiveFile(t, raw), target, DefaultLimits()); !errors.Is(err, ErrPartialRestore) {
		t.Fatal("unsupported owner change silently succeeded")
	}
	if _, err := target.Stat(IncompleteMarker); err != nil {
		t.Fatal("owner failure did not retain incomplete marker")
	}
}
