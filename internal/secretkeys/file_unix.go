//go:build unix

package secretkeys

import (
	"io"
	"os"
	"syscall"
)

func readFile(path string) ([]byte, error) {
	// O_NONBLOCK prevents an unsafe FIFO from blocking before metadata validation.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errInvalidFile
	}
	file := os.NewFile(uintptr(fd), "secret-keyring")
	if file == nil {
		// The descriptor has not been transferred to an os.File.
		_ = syscall.Close(fd)
		return nil, errInvalidFile
	}
	// Every path after acquiring the descriptor, including invalid metadata,
	// closes it. Read-only close errors cannot invalidate the bytes already read.
	defer func() { _ = file.Close() }()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, errInvalidFile
	}
	if !safeMetadata(&stat, uint32(os.Geteuid())) {
		return nil, errInvalidFile
	}
	// The limit also applies if the file grows after Fstat.
	contents, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > maxFileBytes {
		return nil, errInvalidFile
	}
	return contents, nil
}

func safeMetadata(stat *syscall.Stat_t, owner uint32) bool {
	mode := stat.Mode & 0o7777
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG && stat.Uid == owner && (mode == 0o400 || mode == 0o600) && stat.Size > 0 && stat.Size <= maxFileBytes
}
