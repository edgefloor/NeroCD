//go:build linux || darwin

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const runnerSecretMaxBytes = 64 * 1024

type FileSecretResolver struct {
	mu sync.Mutex
	fd int
}

func OpenFileSecretResolver(root string) (*FileSecretResolver, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("runner secret root is required")
	}
	fd, err := openDirectoryNoSymlinks(root)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, os.NewSyscallError("fstat runner secret root", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 {
		unix.Close(fd)
		return nil, errors.New("runner secret root must be an exact mode-0700 directory")
	}
	if int(stat.Uid) != os.Geteuid() {
		unix.Close(fd)
		return nil, errors.New("runner secret root must be owned by the runner user")
	}
	return &FileSecretResolver{fd: fd}, nil
}

func (r *FileSecretResolver) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	return err
}

func (r *FileSecretResolver) Read(reference string) (string, error) {
	if !validSecretLogicalReference(reference) {
		return "", errors.New("runner secret reference is invalid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fd < 0 {
		return "", errors.New("runner secret resolver is closed")
	}
	fd, err := unix.Openat(r.fd, reference, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", os.NewSyscallError("open runner secret", err)
	}
	file := os.NewFile(uintptr(fd), "runner-secret")
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", os.NewSyscallError("fstat runner secret", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", errors.New("runner secret must be a regular file")
	}
	if int(stat.Uid) != os.Geteuid() {
		return "", errors.New("runner secret must be owned by the runner user")
	}
	mode := stat.Mode & 0o7777
	if mode != 0o400 && mode != 0o600 {
		return "", errors.New("runner secret must have exact mode 0400 or 0600")
	}
	if stat.Size <= 0 {
		return "", errors.New("runner secret is empty")
	}
	if stat.Size > runnerSecretMaxBytes {
		return "", errors.New("runner secret exceeds size limit")
	}
	contents, err := io.ReadAll(io.LimitReader(file, runnerSecretMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read runner secret: %w", err)
	}
	if len(contents) > runnerSecretMaxBytes {
		return "", errors.New("runner secret exceeds size limit")
	}
	contents = bytes.TrimSuffix(contents, []byte{'\n'})
	contents = bytes.TrimSuffix(contents, []byte{'\r'})
	if len(contents) == 0 {
		return "", errors.New("runner secret is empty")
	}
	if bytes.IndexByte(contents, 0) >= 0 || !utf8.Valid(contents) {
		return "", errors.New("runner secret contains an unsafe value")
	}
	for _, value := range string(contents) {
		if unicode.IsControl(value) {
			return "", errors.New("runner secret contains an unsafe value")
		}
	}
	return string(contents), nil
}

func openDirectoryNoSymlinks(root string) (int, error) {
	start := "."
	if strings.HasPrefix(root, "/") {
		start = "/"
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, os.NewSyscallError("open runner secret root base", err)
	}
	components := strings.Split(root, "/")
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			unix.Close(fd)
			return -1, errors.New("runner secret root must not contain parent traversal")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, os.NewSyscallError("open runner secret root component", openErr)
		}
		fd = next
	}
	return fd, nil
}
