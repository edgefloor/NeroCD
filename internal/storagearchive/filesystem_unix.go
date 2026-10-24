//go:build linux

package storagearchive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type metadata struct {
	device, inode, links uint64
	mode, uid, gid       uint32
	size, mtime, mns     int64
	ctime, cns           int64
}

func closeFile(file *os.File, err *error) {
	if closeErr := file.Close(); closeErr != nil {
		*err = errors.Join(*err, fmt.Errorf("close archive filesystem handle: %w", closeErr))
	}
}

func inspect(file *os.File) (metadata, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return metadata{}, fmt.Errorf("inspect archive filesystem handle: %w", err)
	}
	m := metadata{device: uint64(st.Dev), inode: uint64(st.Ino), links: uint64(st.Nlink), mode: uint32(st.Mode), uid: st.Uid, gid: st.Gid, size: st.Size, mtime: st.Mtim.Sec, mns: st.Mtim.Nsec, ctime: st.Ctim.Sec, cns: st.Ctim.Nsec}
	kind := m.mode & unix.S_IFMT
	if (kind != unix.S_IFDIR && kind != unix.S_IFREG) || m.mode & ^uint32(unix.S_IFMT|0777) != 0 || m.uid > 2097151 || m.gid > 2097151 || m.size < 0 || (kind == unix.S_IFREG && m.links != 1) {
		return metadata{}, ErrUnsupported
	}
	n, err := unix.Flistxattr(int(file.Fd()), nil)
	if err != nil {
		return metadata{}, fmt.Errorf("inspect archive extended metadata: %w", err)
	}
	if n != 0 {
		return metadata{}, ErrUnsupported
	}
	if kind == unix.S_IFREG && m.size > 0 {
		if st.Blocks < 0 || st.Blocks < (m.size+511)/512 {
			return metadata{}, ErrUnsupported
		}
		hole, err := unix.Seek(int(file.Fd()), 0, unix.SEEK_HOLE)
		if err != nil || hole != m.size {
			return metadata{}, ErrUnsupported
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return metadata{}, fmt.Errorf("rewind source file: %w", err)
		}
	}
	return m, nil
}

func pinned(root *os.Root) (*os.File, metadata, error) {
	if root == nil {
		return nil, metadata{}, ErrUnsupported
	}
	file, err := root.Open(".")
	if err != nil {
		return nil, metadata{}, fmt.Errorf("open pinned archive root: %w", err)
	}
	m, err := inspect(file)
	if err != nil || m.mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, metadata{}, errors.Join(ErrUnsupported, err, file.Close())
	}
	return file, m, nil
}

func child(parent *os.File, name string, device uint64) (*os.File, metadata, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, metadata{}, fmt.Errorf("inspect no-follow archive member: %w", err)
	}
	if kind := before.Mode & unix.S_IFMT; kind != unix.S_IFDIR && kind != unix.S_IFREG {
		return nil, metadata{}, ErrUnsupported
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, metadata{}, fmt.Errorf("open no-follow archive member: %w", err)
	}
	file := os.NewFile(uintptr(fd), "storage-archive-member")
	m, err := inspect(file)
	if err != nil || m.device != device || m.device != uint64(before.Dev) || m.inode != before.Ino || m.mode != before.Mode {
		return nil, metadata{}, errors.Join(ErrUnsupported, err, file.Close())
	}
	return file, m, nil
}

type budgetWriter struct {
	ctx context.Context
	w   io.Writer
	n   int64
	max int64
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.max-w.n {
		return 0, ErrLimit
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

// Write creates an uncompressed v1 archive from a quiesced pinned source.
// It never closes writer. Cancellation is cooperative between IO operations;
// the caller must arrange to interrupt a blocked writer it owns.
func Write(ctx context.Context, source *os.Root, writer io.Writer, l Limits) (result Summary, err error) {
	if !l.valid() || writer == nil {
		return result, ErrLimit
	}
	root, meta, err := pinned(source)
	if err != nil {
		return result, err
	}
	defer closeFile(root, &err)
	hash := sha256.New()
	bounded := &budgetWriter{ctx: ctx, w: io.MultiWriter(writer, hash), max: l.ArchiveBytes}
	tw := tar.NewWriter(bounded)
	var walk func(*os.File, metadata, string) error
	walk = func(file *os.File, original metadata, name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if result.Members >= l.Members || (name != "." && !validName(name, l)) {
			return ErrLimit
		}
		h := &tar.Header{Name: name, Uid: int(original.uid), Gid: int(original.gid), Mode: int64(original.mode & 0777), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR, Typeflag: tar.TypeDir}
		if name == "." {
			h.Uname = Format
		}
		if original.mode&unix.S_IFMT == unix.S_IFREG {
			h.Typeflag, h.Size = tar.TypeReg, original.size
		}
		if h.Size > l.FileBytes || h.Size > l.DataBytes-result.DataBytes {
			return ErrLimit
		}
		if err := tw.WriteHeader(h); err != nil {
			return fmt.Errorf("write archive header: %w", err)
		}
		result.Members++
		result.DataBytes += h.Size
		if h.Typeflag == tar.TypeReg {
			n, err := io.CopyBuffer(tw, contextReader{ctx, io.LimitReader(file, h.Size)}, make([]byte, 32<<10))
			if err != nil {
				return fmt.Errorf("write archive payload: %w", err)
			}
			if n != h.Size {
				return io.ErrUnexpectedEOF
			}
		} else {
			var names []string
			for {
				entries, readErr := file.ReadDir(128)
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				if len(names) > l.Members-result.Members {
					return ErrLimit
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return fmt.Errorf("list source directory: %w", readErr)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			slices.Sort(names)
			for _, base := range names {
				relative := base
				if name != "." {
					relative = name + "/" + base
				}
				if !validName(relative, l) {
					return ErrUnsupported
				}
				if err := func() (err error) {
					opened, meta, err := child(file, base, original.device)
					if err != nil {
						return err
					}
					defer closeFile(opened, &err)
					return walk(opened, meta, relative)
				}(); err != nil {
					return err
				}
			}
		}
		after, err := inspect(file)
		if err != nil || after != original {
			return errors.Join(ErrUnsupported, err)
		}
		return nil
	}
	if err := walk(root, meta, "."); err != nil {
		return Summary{}, err
	}
	if err := tw.Close(); err != nil {
		return Summary{}, fmt.Errorf("finish archive: %w", err)
	}
	result.Format, result.ArchiveBytes = Format, bounded.n
	result.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func openDirectory(root *os.File, relative string, device uint64) (*os.File, error) {
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate target root: %w", err)
	}
	current := os.NewFile(uintptr(fd), "storage-archive-directory")
	if relative == "." {
		return current, nil
	}
	for _, part := range strings.Split(relative, "/") {
		next, meta, openErr := child(current, part, device)
		closeErr := current.Close()
		if openErr != nil || closeErr != nil || meta.mode&unix.S_IFMT != unix.S_IFDIR {
			if next != nil {
				closeErr = errors.Join(closeErr, next.Close())
			}
			return nil, errors.Join(ErrUnsupported, openErr, closeErr)
		}
		current = next
	}
	return current, nil
}

func createAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("create exclusive restore file: %w", err)
	}
	return os.NewFile(uintptr(fd), "storage-archive-created-file"), nil
}

func setMetadata(file *os.File, h *tar.Header) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return fmt.Errorf("inspect restored ownership: %w", err)
	}
	if int(st.Uid) != h.Uid || int(st.Gid) != h.Gid {
		if err := file.Chown(h.Uid, h.Gid); err != nil {
			return fmt.Errorf("preserve restored ownership: %w", err)
		}
	}
	if err := file.Chmod(os.FileMode(h.Mode)); err != nil {
		return fmt.Errorf("preserve restored mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync restored member: %w", err)
	}
	return nil
}

// Extract fully validates and hashes archive before any target mutation, then
// validates/hashes again while extracting. Target must be empty and exclusively
// owned by the caller. Any post-mutation failure wraps ErrPartialRestore; the
// marker is best effort and failures never trigger cleanup, rollback or retry.
func Extract(ctx context.Context, archive *os.File, target *os.Root, l Limits) (Summary, error) {
	expected, err := Validate(ctx, archive, l)
	if err != nil {
		return Summary{}, err
	}
	return extractValidated(ctx, archive, target, l, expected)
}

func extractValidated(ctx context.Context, archive *os.File, target *os.Root, l Limits, expected Summary) (result Summary, err error) {
	mutated := false
	defer func() {
		if err != nil && mutated {
			err = errors.Join(ErrPartialRestore, err)
		}
	}()
	root, meta, err := pinned(target)
	if err != nil {
		return result, err
	}
	defer closeFile(root, &err)
	if meta.uid != uint32(os.Geteuid()) || meta.mode&0022 != 0 || meta.mode&0700 != 0700 {
		return result, ErrUnsupported
	}
	entries, err := root.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("inspect empty restore target: %w", err)
	}
	if len(entries) != 0 {
		return result, errors.New("storage archive: restore target is not empty")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	marker, err := createAt(root, IncompleteMarker)
	if err != nil {
		return result, err
	}
	mutated = true
	if err = errors.Join(marker.Sync(), marker.Close(), root.Sync()); err != nil {
		return result, err
	}
	if err := root.Chmod(0700); err != nil {
		return result, fmt.Errorf("protect restore target: %w", err)
	}
	type directory struct {
		header tar.Header
		inode  uint64
	}
	var dirs []directory
	actual, err := scan(ctx, archive, l, func(h *tar.Header, body io.Reader) (err error) {
		if h.Name == "." {
			dirs = append(dirs, directory{*h, meta.inode})
			return nil
		}
		parent, err := openDirectory(root, path.Dir(h.Name), meta.device)
		if err != nil {
			return err
		}
		defer closeFile(parent, &err)
		if h.Typeflag == tar.TypeDir {
			if err := unix.Mkdirat(int(parent.Fd()), path.Base(h.Name), 0700); err != nil {
				return fmt.Errorf("create exclusive restore directory: %w", err)
			}
			opened, info, err := child(parent, path.Base(h.Name), meta.device)
			if err != nil {
				return err
			}
			dirs = append(dirs, directory{*h, info.inode})
			return opened.Close()
		}
		file, err := createAt(parent, path.Base(h.Name))
		if err != nil {
			return err
		}
		defer closeFile(file, &err)
		if _, err := io.CopyBuffer(file, body, make([]byte, 32<<10)); err != nil {
			return fmt.Errorf("restore archive payload: %w", err)
		}
		return setMetadata(file, h)
	})
	if err != nil {
		return result, err
	}
	if actual != expected {
		return result, ErrInvalidArchive
	}
	for i := len(dirs) - 1; i > 0; i-- {
		dir := dirs[i]
		if err := func() (err error) {
			file, err := openDirectory(root, dir.header.Name, meta.device)
			if err != nil {
				return err
			}
			defer closeFile(file, &err)
			info, err := inspect(file)
			if err != nil || info.inode != dir.inode {
				return errors.Join(ErrUnsupported, err)
			}
			return setMetadata(file, &dir.header)
		}(); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := unix.Unlinkat(int(root.Fd()), IncompleteMarker, 0); err != nil {
		return result, fmt.Errorf("finish restore marker: %w", err)
	}
	if err := setMetadata(root, &dirs[0].header); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return actual, nil
}
