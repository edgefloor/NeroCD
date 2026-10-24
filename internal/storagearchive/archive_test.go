//go:build linux || darwin

package storagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func testRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	name := t.TempDir()
	root, err := os.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root, name
}

func archiveFile(t *testing.T, raw []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "archive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
	return f
}

func testArchive(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	root := &tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0750, Uid: os.Geteuid(), Gid: os.Getegid(), Uname: Format, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	for _, h := range append([]*tar.Header{root}, headers...) {
		if h.Uid == 0 {
			h.Uid = os.Geteuid()
		}
		if h.Gid == 0 {
			h.Gid = os.Getegid()
		}
		if h.Format == tar.FormatUnknown {
			h.Format = tar.FormatUSTAR
		}
		h.ModTime = time.Unix(0, 0)
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(bytes.Repeat([]byte{'x'}, int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestArchiveRejectsAdversarialMembersBeforeMutation(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../b", "a//b", "./a", "a\\b", "C:drive", "trailing.", "contains space", IncompleteMarker, IncompleteMarker + "/file", "missing-parent/file"} {
		t.Run(name, func(t *testing.T) {
			raw := testArchive(t, &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600})
			f := archiveFile(t, raw)
			if _, err := Validate(t.Context(), f, DefaultLimits()); err == nil {
				t.Fatal("invalid path accepted")
			}
			target, _ := testRoot(t)
			if _, err := Extract(t.Context(), f, target, DefaultLimits()); err == nil || errors.Is(err, ErrPartialRestore) {
				t.Fatal("invalid path accepted or mutated target before validation")
			}
			entries, err := target.ReadFile(IncompleteMarker)
			if !errors.Is(err, os.ErrNotExist) || entries != nil {
				t.Fatal("invalid archive created a target marker")
			}
		})
	}
	cases := map[string][]*tar.Header{
		"duplicate":   {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}},
		"file parent": {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a/b", Typeflag: tar.TypeReg}},
		"symlink":     {{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "outside"}},
		"hardlink":    {{Name: "a", Typeflag: tar.TypeLink, Linkname: "outside"}},
		"fifo":        {{Name: "a", Typeflag: tar.TypeFifo}},
		"device":      {{Name: "a", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}},
		"privilege":   {{Name: "a", Typeflag: tar.TypeReg, Mode: 04755}},
		"sticky":      {{Name: "a", Typeflag: tar.TypeDir, Mode: 01777}},
		"xattr":       {{Name: "a", Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.xattr.user.test": "value"}}},
		"acl":         {{Name: "a", Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.acl.access": "user::rw-"}}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(t.Context(), archiveFile(t, testArchive(t, headers...)), DefaultLimits()); err == nil {
				t.Fatal("unsupported archive metadata accepted")
			}
		})
	}
}

func TestTruncationTrailingDataAndBudgets(t *testing.T) {
	raw := testArchive(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600, Size: 8})
	for _, n := range []int{0, 511, 512, len(raw) - 512, len(raw) - 1} {
		if _, err := Validate(t.Context(), archiveFile(t, raw[:n]), DefaultLimits()); err == nil {
			t.Fatal("truncated archive accepted")
		}
	}
	if _, err := Validate(t.Context(), archiveFile(t, append(bytes.Clone(raw), make([]byte, 512)...)), DefaultLimits()); err == nil {
		t.Fatal("trailing archive bytes accepted")
	}
	for _, limit := range []string{"archive", "payload", "file", "members", "path", "depth", "invalid"} {
		t.Run(limit, func(t *testing.T) {
			l := DefaultLimits()
			input := raw
			switch limit {
			case "archive":
				l.ArchiveBytes = int64(len(raw) - 512)
				l.DataBytes = l.ArchiveBytes
				l.FileBytes = l.DataBytes
			case "payload":
				l.DataBytes, l.FileBytes = 4, 4
			case "file":
				l.FileBytes = 4
			case "members":
				l.Members = 1
			case "path":
				l.PathBytes = 3
			case "depth":
				l.Depth = 1
				input = testArchive(t, &tar.Header{Name: "a", Typeflag: tar.TypeDir}, &tar.Header{Name: "a/b", Typeflag: tar.TypeReg})
			case "invalid":
				l.Members = 0
			}
			if _, err := Validate(t.Context(), archiveFile(t, input), l); err == nil {
				t.Fatal("archive budget ignored")
			}
		})
	}
}

func TestUnsupportedNumericOwnerAndSparseHeader(t *testing.T) {
	raw := testArchive(t)
	for _, field := range []int{3, 108, 116, 156, 500} {
		bad := bytes.Clone(raw)
		switch field {
		case 156:
			bad[field] = tar.TypeGNUSparse
		case 3, 500:
			bad[field] = 'x'
		default:
			copy(bad[field:field+8], "10000000")
		}
		for i := 148; i < 156; i++ {
			bad[i] = ' '
		}
		sum := 0
		for _, c := range bad[:512] {
			sum += int(c)
		}
		copy(bad[148:156], fmt.Sprintf("%06o\x00 ", sum))
		if _, err := Validate(t.Context(), archiveFile(t, bad), DefaultLimits()); err == nil {
			t.Fatal("unsupported numeric or sparse header accepted")
		}
	}
}

func TestNamesAreBoundedCanonicalGraphicalASCII(t *testing.T) {
	for _, s := range []string{"", ".", "..", strings.Repeat("a", 256), "a/", "a\x00b", "café"} {
		if validName(s, DefaultLimits()) {
			t.Fatal("ambiguous name accepted")
		}
	}
}

func TestValidateCancellationAndPayloadDigest(t *testing.T) {
	raw := testArchive(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600, Size: 8})
	f := archiveFile(t, raw)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Validate(ctx, f, DefaultLimits()); !errors.Is(err, context.Canceled) {
		t.Fatal("archive validation ignored cancellation")
	}
	first, err := Validate(t.Context(), f, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("changed!"), 1024); err != nil {
		t.Fatal(err)
	}
	second, err := Validate(t.Context(), f, DefaultLimits())
	if err != nil || first.Digest == second.Digest {
		t.Fatal("payload mutation did not change the validated digest")
	}
}
