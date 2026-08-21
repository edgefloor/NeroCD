//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxBackupMetadataBytes = 64 << 10

func secureBackupDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("backup path must be absolute")
	}
	fd, err := openBackupDirectoryPath(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || stat.Mode&0o7777 != 0o700 {
		return errors.New("unsafe backup directory")
	}
	return nil
}

func secureBackupRegular(path string) error {
	file, err := openSecureBackupFile(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func openSecureBackupFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("backup path must be absolute")
	}
	parent, base, err := openBackupParent(path)
	if err != nil {
		return nil, errors.New("backup file cannot be opened")
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("backup file cannot be opened")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Mode&0o7777 != 0o400 && stat.Mode&0o7777 != 0o600 {
		_ = unix.Close(fd)
		return nil, errors.New("unsafe backup file")
	}
	file := os.NewFile(uintptr(fd), "secure-backup-file")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("backup file cannot be opened")
	}
	return file, nil
}

// openBackupDirectoryPath walks every absolute component from the trusted root
// descriptor. Each hop is directory-only and no-follow, so an intermediate
// symlink replacement cannot redirect a later open.
func openBackupDirectoryPath(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, errors.New("backup path must be absolute")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			unix.Close(fd)
			return -1, errors.New("backup path contains parent traversal")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openBackupParent(path string) (int, string, error) {
	if !filepath.IsAbs(path) {
		return -1, "", errors.New("backup path must be absolute")
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == "." || base == "/" {
		return -1, "", errors.New("backup file path is invalid")
	}
	fd, err := openBackupDirectoryPath(filepath.Dir(clean))
	return fd, base, err
}

func readSecureBackupFile(path string) ([]byte, error) {
	file, err := openSecureBackupFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxBackupMetadataBytes+1))
}
