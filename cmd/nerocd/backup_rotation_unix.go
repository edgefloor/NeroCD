//go:build unix

package main

import (
	"errors"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// rotateSecureBackups retains the newest canonical backup directories.  It
// never follows a name discovered from the filesystem: every directory and
// regular file is re-opened relative to the verified root descriptor.
func rotateSecureBackups(root string, retain int) error {
	if retain < 1 {
		return errors.New("backup retention is invalid")
	}
	fd, err := openBackupDirectoryPath(root)
	if err != nil {
		return errors.New("backup rotation root is unsafe")
	}
	defer func() { _ = unix.Close(fd) }()
	if err := verifyBackupDirectoryFD(fd); err != nil {
		return errors.New("backup rotation root is unsafe")
	}
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return errors.New("backup rotation root cannot be read")
	}
	rootFile := os.NewFile(uintptr(copyFD), "backup-root")
	entries, err := rootFile.ReadDir(-1)
	closeErr := rootFile.Close()
	if err != nil {
		return errors.New("backup rotation root cannot be read")
	}
	if closeErr != nil {
		return errors.New("backup rotation root cannot be read")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "backup-") {
			continue
		}
		if strings.Contains(name, "/") || name == "." || name == ".." {
			return errors.New("backup rotation entry is invalid")
		}
		child, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil || verifyBackupDirectoryFD(child) != nil {
			if child >= 0 {
				_ = unix.Close(child)
			}
			return errors.New("backup rotation entry is unsafe")
		}
		_ = unix.Close(child)
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) <= retain {
		return nil
	}
	for _, name := range names[:len(names)-retain] {
		if err := removeSecureBackupTreeAt(fd, name); err != nil {
			return err
		}
	}
	if err := unix.Fsync(fd); err != nil {
		return errors.New("backup rotation could not be synchronized")
	}
	return nil
}

func verifyBackupDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if fd < 0 || unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || stat.Mode&0o7777 != 0o700 {
		return errors.New("unsafe backup directory")
	}
	return nil
}

func removeSecureBackupTreeAt(parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil || verifyBackupDirectoryFD(fd) != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return errors.New("backup rotation entry is unsafe")
	}
	defer func() { _ = unix.Close(fd) }()
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return errors.New("backup rotation entry cannot be read")
	}
	entryFile := os.NewFile(uintptr(copyFD), "backup-entry")
	entries, err := entryFile.ReadDir(-1)
	closeErr := entryFile.Close()
	if err != nil {
		return errors.New("backup rotation entry cannot be read")
	}
	if closeErr != nil {
		return errors.New("backup rotation entry cannot be read")
	}
	if len(entries) != 2 {
		return errors.New("backup rotation entry has unexpected content")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		child := entry.Name()
		if child != "database.dump" && child != "manifest.json" {
			return errors.New("backup rotation entry has unexpected content")
		}
		if seen[child] {
			return errors.New("backup rotation entry has unexpected content")
		}
		seen[child] = true
		file, openErr := unix.Openat(fd, child, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		var stat unix.Stat_t
		valid := openErr == nil && unix.Fstat(file, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && int(stat.Uid) == os.Geteuid() && stat.Mode&0o7777 == 0o600
		if file >= 0 {
			_ = unix.Close(file)
		}
		if !valid {
			return errors.New("backup rotation entry has unsafe content")
		}
		if err := unix.Unlinkat(fd, child, 0); err != nil {
			return errors.New("backup rotation could not remove file")
		}
	}
	if !seen["database.dump"] || !seen["manifest.json"] {
		return errors.New("backup rotation entry has unexpected content")
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil {
		return errors.New("backup rotation could not remove directory")
	}
	return nil
}
