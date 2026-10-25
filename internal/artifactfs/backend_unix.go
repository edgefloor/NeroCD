//go:build linux || darwin

package artifactfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	rootMode   = uint32(0o700)
	objectMode = uint32(0o600)
	modeMask   = uint32(0o7777)
)

type directoryBackend struct {
	fd int
}

type fileMetadata struct {
	device uint64
	inode  uint64
	size   int64
}

func openDirectoryBackend(root string) (*directoryBackend, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, invalidError("unsafe store root")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ioError("open filesystem root", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = unix.Close(fd)
		}
	}()

	trimmed := strings.TrimPrefix(root, string(filepath.Separator))
	if trimmed != "" {
		for _, component := range strings.Split(trimmed, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." {
				return nil, invalidError("unsafe store root")
			}
			next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if openErr != nil {
				return nil, invalidError("unsafe store root")
			}
			_ = unix.Close(fd)
			fd = next
		}
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, ioError("inspect store root", err)
	}
	if uint32(stat.Mode)&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) || uint32(stat.Mode)&modeMask != rootMode || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return nil, invalidError("unsafe store root")
	}
	keep = true
	return &directoryBackend{fd: fd}, nil
}

func (d *directoryBackend) createTemp() (string, *os.File, error) {
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, ioError("generate temporary name", err)
		}
		name := ".artifactfs-put-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, objectMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, ioError("create temporary object", err)
		}
		if err := unix.Fchmod(fd, objectMode); err != nil {
			_ = unix.Close(fd)
			_ = d.remove(name)
			return "", nil, ioError("set temporary object mode", err)
		}
		file := os.NewFile(uintptr(fd), "artifactfs-temporary-object")
		if file == nil {
			_ = unix.Close(fd)
			return "", nil, ioError("create temporary object handle", unix.EBADF)
		}
		if _, verifyErr := inspectRegularFile(fd, -1); verifyErr != nil {
			_ = file.Close()
			_ = d.remove(name)
			return "", nil, verifyErr
		}
		return name, file, nil
	}
	return "", nil, ioError("create temporary object", unix.EEXIST)
}

func (d *directoryBackend) openRegular(name string, maxBytes int64) (*os.File, fileMetadata, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT):
			return nil, fileMetadata{}, os.ErrNotExist
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
			return nil, fileMetadata{}, ErrIntegrity
		default:
			return nil, fileMetadata{}, ioError("open object", err)
		}
	}
	metadata, err := inspectRegularFile(fd, maxBytes)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fileMetadata{}, err
	}
	file := os.NewFile(uintptr(fd), "artifactfs-object")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fileMetadata{}, ioError("create object handle", unix.EBADF)
	}
	return file, metadata, nil
}

func (d *directoryBackend) verifyOpenFile(file *os.File, expected fileMetadata, maxBytes int64) error {
	metadata, err := inspectRegularFile(int(file.Fd()), maxBytes)
	if err != nil {
		return err
	}
	if metadata != expected {
		return ErrIntegrity
	}
	return nil
}

func inspectRegularFile(fd int, maxBytes int64) (fileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fileMetadata{}, ioError("inspect object", err)
	}
	if uint32(stat.Mode)&uint32(unix.S_IFMT) != uint32(unix.S_IFREG) || uint32(stat.Mode)&modeMask != objectMode || uint32(stat.Uid) != uint32(os.Geteuid()) || uint64(stat.Nlink) != 1 || stat.Size < 0 || (maxBytes >= 0 && stat.Size > maxBytes) {
		return fileMetadata{}, ErrIntegrity
	}
	return fileMetadata{device: uint64(stat.Dev), inode: uint64(stat.Ino), size: stat.Size}, nil
}

func (d *directoryBackend) remove(name string) error {
	err := unix.Unlinkat(d.fd, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return ioError("remove temporary object", err)
	}
	return nil
}

func (d *directoryBackend) sync() error { return unix.Fsync(d.fd) }

func (d *directoryBackend) close() error {
	if err := unix.Close(d.fd); err != nil {
		return fmt.Errorf("artifactfs: close store root: %w", err)
	}
	return nil
}

// A root-directory flock serializes all cooperating Store instances, including
// independently launched runner processes configured with the same pool.
func (d *directoryBackend) lock(ctx context.Context) error {
	for {
		err := unix.Flock(d.fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return ioError("lock store", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (d *directoryBackend) unlock() { _ = unix.Flock(d.fd, unix.LOCK_UN) }
func (d *directoryBackend) ownership(key string, max int64) (Ownership, error) {
	pool, err := d.poolID()
	if err != nil {
		return Ownership{}, err
	}
	file, meta, err := d.openRegular(key, max)
	if err != nil {
		return Ownership{}, err
	}
	defer file.Close()
	return Ownership{PoolID: pool, Device: meta.device, Inode: meta.inode, Bytes: meta.size}, nil
}

func (d *directoryBackend) poolID() (string, error) {
	var root unix.Stat_t
	if err := unix.Fstat(d.fd, &root); err != nil {
		return "", ioError("inspect pool", err)
	}
	id := sha256.Sum256([]byte(fmt.Sprintf("artifactfs.pool.v1:%d:%d", root.Dev, root.Ino)))
	return fmt.Sprintf("sha256:%x", id), nil
}
