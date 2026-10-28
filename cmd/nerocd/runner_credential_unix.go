//go:build unix

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func readRunnerCredentialFile(filename string) (string, error) {
	fd, err := syscall.Open(filename, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), filename)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("credential must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("credential permissions are %04o, want 0600", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("credential ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("credential owner uid is %d, want effective uid %d", stat.Uid, os.Geteuid())
	}
	contents, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return "", err
	}
	if len(contents) > 64*1024 {
		return "", errors.New("credential is too large")
	}
	credential := strings.TrimSpace(string(contents))
	if credential == "" {
		return "", errors.New("credential is empty")
	}
	return credential, nil
}

// runnerEnrollmentFiles owns the file that supplied the consumed token and its
// parent until consumption either fails or cleanup finishes.
type runnerEnrollmentFiles struct {
	token, credential, requestID string
	file, parent                 *os.File
	name                         string
}

func (files *runnerEnrollmentFiles) close() {
	_ = files.file.Close()
	_ = files.parent.Close()
}

func prepareRunnerEnrollmentFiles(enrollmentFilename, credentialFilename string) (*runnerEnrollmentFiles, error) {
	parent, err := openRunnerFileDirectory(filepath.Dir(enrollmentFilename), false)
	if err != nil {
		return nil, fmt.Errorf("enrollment directory: %w", err)
	}
	name := filepath.Base(enrollmentFilename)
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("enrollment file: %w", err)
	}
	files := &runnerEnrollmentFiles{file: os.NewFile(uintptr(fd), "runner-enrollment"), parent: parent, name: name}
	admitted := false
	defer func() {
		if !admitted {
			files.close()
		}
	}()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !validRunnerEnrollmentStat(stat) || stat.Nlink != 1 {
		return nil, errors.New("enrollment file must be a private regular file with one link")
	}
	contents, err := io.ReadAll(io.LimitReader(files.file, 64*1024+1))
	if err != nil {
		return nil, err
	}
	files.token = strings.TrimSpace(string(contents))
	if len(contents) > 64*1024 || files.token == "" {
		return nil, errors.New("enrollment file is empty or too large")
	}
	credential, err := readRunnerCredentialFile(credentialFilename)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("pending credential: %w", err)
		}
		credential, err = createRunnerCredentialFile(credentialFilename)
		if err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256([]byte(credential))
	hash := hex.EncodeToString(sum[:])
	files.credential = credential
	files.requestID = "enroll_consume_" + hash[:32]
	admitted = true
	return files, nil
}

func openRunnerFileDirectory(path string, ownerOnly bool) (*os.File, error) {
	// Pin the parent before admitting its metadata or any child. Native aliases
	// such as macOS /var remain usable; subsequent access never resolves them.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7022 != 0 || stat.Mode&0o700 != 0o700 || int(stat.Uid) != os.Geteuid() || ownerOnly && stat.Mode&0o7777 != 0o700 {
		_ = unix.Close(fd)
		return nil, errors.New("credential directory must be owned by the current user, writable only by its owner, and mode 0700 for credential creation")
	}
	return os.NewFile(uintptr(fd), "runner-credential-directory"), nil
}

func validRunnerEnrollmentStat(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && int(stat.Uid) == os.Geteuid() && stat.Size > 0 && stat.Size <= 64*1024 && stat.Nlink <= 1
}

func createRunnerCredentialFile(filename string) (string, error) {
	dirname, basename := filepath.Split(filename)
	if dirname == "" {
		dirname = "."
	}
	if basename == "" || basename == "." || basename == ".." || strings.ContainsAny(basename, `/\`) {
		return "", errors.New("credential filename is invalid")
	}
	directory, err := openRunnerFileDirectory(strings.TrimSuffix(dirname, string(os.PathSeparator)), true)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	dirfd := int(directory.Fd())
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	credential := "ncr_" + hex.EncodeToString(random)
	tmpRandom := make([]byte, 8)
	if _, err := rand.Read(tmpRandom); err != nil {
		return "", err
	}
	tmp := "." + basename + ".pending-" + hex.EncodeToString(tmpRandom)
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		_ = unix.Close(fd)
		if cleanup {
			_ = unix.Unlinkat(dirfd, tmp, 0)
		}
	}()
	contents := []byte(credential + "\n")
	for len(contents) > 0 {
		written, err := unix.Write(fd, contents)
		if err != nil {
			return "", err
		}
		if written == 0 {
			return "", io.ErrShortWrite
		}
		contents = contents[written:]
	}
	if err := unix.Fsync(fd); err != nil {
		return "", err
	}
	if err := unix.Linkat(dirfd, tmp, dirfd, basename, 0); err != nil {
		return "", err
	}
	if err := unix.Unlinkat(dirfd, tmp, 0); err != nil {
		return "", err
	}
	cleanup = false
	if err := unix.Fsync(dirfd); err != nil {
		return "", err
	}
	return credential, nil
}

func (files *runnerEnrollmentFiles) remove() error {
	fd := int(files.file.Fd())
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !validRunnerEnrollmentStat(stat) {
		return errors.New("enrollment file changed before removal")
	}
	zeros := make([]byte, stat.Size)
	n, err := files.file.WriteAt(zeros, 0)
	if err != nil {
		return err
	}
	if n != len(zeros) {
		return io.ErrShortWrite
	}
	if err := files.file.Sync(); err != nil {
		return err
	}
	var entry unix.Stat_t
	parent := int(files.parent.Fd())
	if err := unix.Fstatat(parent, files.name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	// The admitted parent excludes other users from changing this
	// entry between the identity check and unlink. A renamed ancestor is harmless.
	if entry.Dev != stat.Dev || entry.Ino != stat.Ino || entry.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("enrollment entry changed before removal")
	}
	if err := unix.Unlinkat(parent, files.name, 0); err != nil {
		return err
	}
	return files.parent.Sync()
}
