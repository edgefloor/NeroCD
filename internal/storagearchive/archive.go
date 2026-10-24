// Package storagearchive implements the confined, uncompressed upload archive v1.
// Sources must be quiesced by the caller. Metadata change checks are best effort,
// not a transactional filesystem snapshot. Callers own stream cancellation: a
// context cannot interrupt an arbitrary blocked Reader or Writer.
package storagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// Format identifies the only supported wire format.
const Format = "nerocd-storage-v1"

// IncompleteMarker is reserved and never permitted as an archive member.
const IncompleteMarker = ".nerocd-restore-incomplete"

var (
	// ErrInvalidArchive identifies malformed or unsupported archive contents.
	ErrInvalidArchive = errors.New("storage archive: invalid archive")
	// ErrLimit identifies an exhausted or invalid resource budget.
	ErrLimit = errors.New("storage archive: limit exceeded")
	// ErrUnsupported identifies unsupported filesystem metadata or platforms.
	ErrUnsupported = errors.New("storage archive: unsupported filesystem metadata")
	// ErrPartialRestore means the target was mutated and must not be reused.
	// No rollback or marker persistence is guaranteed on a failing filesystem.
	ErrPartialRestore = errors.New("storage archive: partial restore; target must not be reused")
)

// Limits bounds physical archive bytes, payload bytes, individual file bytes,
// members including the root, and canonical ASCII path depth and length.
// All fields must be positive. V1 paths are at most 255 bytes and UID/GID at
// most 2097151, as supported by strict USTAR, without numeric extensions.
type Limits struct {
	ArchiveBytes int64
	DataBytes    int64
	FileBytes    int64
	Members      int
	Depth        int
	PathBytes    int
}

// DefaultLimits supplies conservative defaults for the standalone helper.
func DefaultLimits() Limits {
	return Limits{ArchiveBytes: 256 << 20, DataBytes: 128 << 20, FileBytes: 64 << 20, Members: 100000, Depth: 32, PathBytes: 255}
}

func (l Limits) valid() bool {
	return l.ArchiveBytes >= 1536 && l.ArchiveBytes <= 1<<40 && l.DataBytes > 0 && l.DataBytes <= l.ArchiveBytes && l.FileBytes > 0 && l.FileBytes <= l.DataBytes && l.Members > 0 && l.Members <= 1000000 && l.Depth > 0 && l.Depth <= 256 && l.PathBytes > 0 && l.PathBytes <= 255
}

// Validate rejects invalid limits before an input stream is consumed.
func (l Limits) Validate() error {
	if !l.valid() {
		return ErrLimit
	}
	return nil
}

// Summary is public integrity metadata, valid only when the returned error is
// nil. It contains no upload paths or bytes.
type Summary struct {
	Format       string `json:"format"`
	Digest       string `json:"digest"`
	ArchiveBytes int64  `json:"archive_bytes"`
	DataBytes    int64  `json:"data_bytes"`
	Members      int    `json:"members"`
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func validName(name string, l Limits) bool {
	if name == "" || len(name) > l.PathBytes || name == "." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > l.Depth || parts[0] == IncompleteMarker {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
		for _, c := range []byte(part) {
			if c < 0x21 || c > 0x7e {
				return false
			}
		}
	}
	return true
}

func headerFromBlock(block []byte) (*tar.Header, error) {
	if string(block[257:265]) != "ustar\x0000" || (block[156] != tar.TypeReg && block[156] != tar.TypeDir) {
		return nil, ErrInvalidArchive
	}
	for _, bounds := range [][2]int{{0, 100}, {157, 257}, {265, 297}, {297, 329}, {345, 500}} {
		field := block[bounds[0]:bounds[1]]
		if end := bytes.IndexByte(field, 0); end >= 0 && !bytes.Equal(field[end:], make([]byte, len(field)-end)) {
			return nil, ErrInvalidArchive
		}
	}
	if !bytes.Equal(block[500:512], make([]byte, 12)) {
		return nil, ErrInvalidArchive
	}
	for _, offset := range []int{100, 108, 116, 124, 136, 329, 337} {
		if block[offset]&0x80 != 0 { // No GNU binary numeric fields.
			return nil, ErrInvalidArchive
		}
	}
	h, err := tar.NewReader(bytes.NewReader(block)).Next()
	if err != nil {
		return nil, fmt.Errorf("decode archive header: %w", ErrInvalidArchive)
	}
	if h.Format != tar.FormatUSTAR || h.Mode < 0 || h.Mode > 0777 || h.Uid < 0 || h.Uid > 2097151 || h.Gid < 0 || h.Gid > 2097151 || h.Size < 0 || h.Linkname != "" || h.Gname != "" || h.Devmajor != 0 || h.Devminor != 0 || len(h.PAXRecords) != 0 || !h.ModTime.Equal(time.Unix(0, 0)) || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
		return nil, ErrInvalidArchive
	}
	return h, nil
}

// Validate fully parses and hashes an exclusively owned pinned regular file.
// It rejects trailing data, truncated records, all extensions and unsupported
// paths/metadata. The caller must not concurrently modify or close archive.
func Validate(ctx context.Context, archive *os.File, l Limits) (Summary, error) {
	return scan(ctx, archive, l, nil)
}

func scan(ctx context.Context, archive *os.File, l Limits, visit func(*tar.Header, io.Reader) error) (Summary, error) {
	var result Summary
	if !l.valid() || archive == nil {
		return result, ErrLimit
	}
	before, err := archive.Stat()
	if err != nil {
		return result, fmt.Errorf("inspect archive: %w", err)
	}
	if !before.Mode().IsRegular() || before.Size() < 1536 || before.Size() > l.ArchiveBytes || before.Size()%512 != 0 {
		return result, ErrInvalidArchive
	}
	hash := sha256.New()
	r := io.TeeReader(contextReader{ctx, io.NewSectionReader(archive, 0, before.Size())}, hash)
	seen := map[string]byte{}
	var block [512]byte
	buffer := make([]byte, 32<<10)
	for {
		if _, err := io.ReadFull(r, block[:]); err != nil {
			return Summary{}, fmt.Errorf("read archive header: %w", err)
		}
		if block == [512]byte{} {
			if _, err := io.ReadFull(r, block[:]); err != nil || block != [512]byte{} || result.Members == 0 {
				return Summary{}, ErrInvalidArchive
			}
			var extra [1]byte
			if n, err := r.Read(extra[:]); n != 0 || err != io.EOF {
				return Summary{}, ErrInvalidArchive
			}
			break
		}
		h, err := headerFromBlock(block[:])
		if err != nil {
			return Summary{}, err
		}
		if result.Members == 0 {
			if h.Name != "." || h.Typeflag != tar.TypeDir || h.Size != 0 || h.Uname != Format {
				return Summary{}, ErrInvalidArchive
			}
		} else if !validName(h.Name, l) || h.Uname != "" || seen[path.Dir(h.Name)] != tar.TypeDir {
			return Summary{}, ErrInvalidArchive
		}
		if _, duplicate := seen[h.Name]; duplicate {
			return Summary{}, ErrInvalidArchive
		}
		if h.Typeflag == tar.TypeDir && h.Size != 0 {
			return Summary{}, ErrInvalidArchive
		}
		if result.Members >= l.Members || h.Size > l.FileBytes || h.Size > l.DataBytes-result.DataBytes {
			return Summary{}, ErrLimit
		}
		seen[h.Name] = h.Typeflag
		result.Members++
		result.DataBytes += h.Size
		body := &io.LimitedReader{R: r, N: h.Size}
		if visit != nil {
			err = visit(h, body)
		} else {
			_, err = io.CopyBuffer(io.Discard, body, buffer)
		}
		if err != nil {
			return Summary{}, err
		}
		if body.N != 0 {
			return Summary{}, io.ErrUnexpectedEOF
		}
		padding := (512 - h.Size%512) % 512
		if _, err := io.ReadFull(r, block[:padding]); err != nil {
			return Summary{}, fmt.Errorf("read archive padding: %w", err)
		}
		if !bytes.Equal(block[:padding], make([]byte, padding)) {
			return Summary{}, ErrInvalidArchive
		}
	}
	after, err := archive.Stat()
	if err != nil {
		return Summary{}, fmt.Errorf("reinspect archive: %w", err)
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() || !os.SameFile(before, after) {
		return Summary{}, ErrInvalidArchive
	}
	result.Format, result.ArchiveBytes = Format, before.Size()
	result.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}
