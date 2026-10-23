//go:build linux

package runnerruntime

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Open metadata handles without following any path component's symlink. O_PATH
// cannot block on a FIFO/device and does not read a file or create any state.
func readinessMetadata(path string, directory bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, unix.EINVAL
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}
func readinessCLI() string {
	path, err := exec.LookPath("docker")
	if errors.Is(err, exec.ErrNotFound) {
		return "missing"
	}
	if err != nil {
		return "unsafe"
	}
	fd, err := readinessMetadata(path, false)
	if err != nil {
		return "unsafe"
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return "unknown"
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o022 != 0 || stat.Mode&0o111 == 0 || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return "unsafe"
	}
	if unix.Faccessat(fd, "", unix.X_OK, unix.AT_EACCESS|unix.AT_EMPTY_PATH) != nil {
		return "unsafe"
	}
	return "available"
}
func readinessRoot(path string, journal bool) (string, int64) {
	fd, err := readinessMetadata(path, true)
	if errors.Is(err, unix.ENOENT) {
		return "missing", 0
	}
	if errors.Is(err, unix.EACCES) {
		return "inaccessible", 0
	}
	if err != nil {
		return "unsafe", 0
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return "unknown", 0
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 || journal && stat.Mode&0o777 != 0o700 {
		return "unsafe", 0
	}
	if unix.Faccessat(fd, ".", unix.R_OK|unix.W_OK|unix.X_OK, unix.AT_EACCESS) != nil {
		return "inaccessible", 0
	}
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil {
		return "inaccessible", 0
	}
	free, ok := readinessAvailableBytes(fs.Bavail, fs.Bsize)
	if !ok {
		return "unknown", 0
	}
	return "accessible", free
}
func readinessAvailableBytes(blocks uint64, size int64) (int64, bool) {
	if size <= 0 || blocks > uint64(math.MaxInt64)/uint64(size) {
		return 0, false
	}
	return int64(blocks * uint64(size)), true
}
