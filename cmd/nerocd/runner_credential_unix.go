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

func prepareRunnerEnrollmentFiles(enrollmentFilename, credentialFilename string) (string, string, string, error) {
	enrollment, err := readRunnerCredentialFile(enrollmentFilename)
	if err != nil {
		return "", "", "", fmt.Errorf("enrollment file: %w", err)
	}
	credential, err := readRunnerCredentialFile(credentialFilename)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", "", fmt.Errorf("pending credential: %w", err)
		}
		credential, err = createRunnerCredentialFile(credentialFilename)
		if err != nil {
			return "", "", "", err
		}
	}
	sum := sha256.Sum256([]byte(credential))
	hash := hex.EncodeToString(sum[:])
	return enrollment, credential, "enroll_consume_" + hash[:32], nil
}

func createRunnerCredentialFile(filename string) (string, error) {
	dirname, basename := filepath.Split(filename)
	if dirname == "" {
		dirname = "."
	}
	if basename == "" || basename == "." || basename == ".." || strings.ContainsAny(basename, `/\`) {
		return "", errors.New("credential filename is invalid")
	}
	dirfd, err := unix.Open(strings.TrimSuffix(dirname, string(os.PathSeparator)), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(dirfd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(dirfd, &stat); err != nil {
		return "", err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("credential directory must be an owner-only mode-0700 directory")
	}
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

func removeRunnerEnrollmentFile(filename string) error {
	fd, err := unix.Open(filename, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || int(stat.Uid) != os.Geteuid() || stat.Size <= 0 || stat.Size > 64*1024 {
		_ = unix.Close(fd)
		return errors.New("enrollment file changed before removal")
	}
	zeros := make([]byte, stat.Size)
	if _, err := unix.Pwrite(fd, zeros, 0); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	if err := unix.Unlink(filename); err != nil {
		return err
	}
	dirfd, err := unix.Open(filepath.Dir(filename), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirfd) }()
	return unix.Fsync(dirfd)
}
