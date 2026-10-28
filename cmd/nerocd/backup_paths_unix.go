//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openSecureBackupDirectory(path string) (*os.File, error) {
	fd, err := openBackupDirectoryPath(path)
	if err != nil {
		return nil, err
	}
	if err := verifyBackupDirectoryFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "backup-directory"), nil
}

func secureBackupDirectory(path string) error {
	directory, err := openSecureBackupDirectory(path)
	if err != nil {
		return err
	}
	return directory.Close()
}

func createBackupDirectoryAt(parent *os.File, name string) (*os.File, error) {
	if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "backup-staging"), nil
}

func createBackupFileAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func publishBackupDirectoryAt(parent *os.File, staging, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errors.New("backup destination already exists or cannot be inspected")
	}
	if err := unix.Renameat(int(parent.Fd()), staging, int(parent.Fd()), name); err != nil {
		return err
	}
	return parent.Sync()
}

func removeBackupStagingAt(parent, staging *os.File, name string) {
	// If publication renamed staging, even a failed fsync must not remove the
	// published files. Inspect the pinned parent before touching its contents.
	var entry, opened unix.Stat_t
	if unix.Fstat(int(staging.Fd()), &opened) != nil || unix.Fstatat(int(parent.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || entry.Dev != opened.Dev || entry.Ino != opened.Ino {
		return
	}
	for _, child := range []string{"database.dump", "manifest.json"} {
		_ = unix.Unlinkat(int(staging.Fd()), child, 0)
	}
	_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
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
	defer func() { _ = unix.Close(parent) }()
	return openSecureBackupFileFD(parent, base)
}

func openSecureBackupFileAt(parent *os.File, name string) (*os.File, error) {
	return openSecureBackupFileFD(int(parent.Fd()), name)
}

func openSecureBackupFileFD(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
			_ = unix.Close(fd)
			return -1, errors.New("backup path contains parent traversal")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
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
