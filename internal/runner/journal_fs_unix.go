//go:build unix

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	journalStateFilename = "journal.json"
	journalLockFilename  = "journal.lock"
)

type secureJournalStore struct {
	mu     sync.Mutex
	dir    *os.File
	dirfd  int
	lock   *os.File
	closed bool
}

func openSecureJournalStore(path string, maxBytes int) (*secureJournalStore, []byte, error) {
	if path == "" {
		return nil, nil, errors.New("runner journal directory is required")
	}
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("runner journal directory must be absolute")
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, nil, err
	}
	dir := os.NewFile(uintptr(fd), path)
	var store *secureJournalStore
	closeOnError := true
	defer func() {
		if closeOnError {
			if store != nil {
				_ = store.Close()
			} else {
				_ = dir.Close()
			}
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, nil, errors.New("runner journal path must be a directory")
	}
	if stat.Mode&0o777 != 0o700 {
		return nil, nil, fmt.Errorf("runner journal permissions are %04o, want 0700", stat.Mode&0o777)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return nil, nil, fmt.Errorf("runner journal owner uid is %d, want effective uid %d", stat.Uid, os.Geteuid())
	}
	store = &secureJournalStore{dir: dir, dirfd: fd}
	if err := store.acquireLock(); err != nil {
		return nil, nil, err
	}
	contents, err := store.read(maxBytes)
	if err != nil {
		return nil, nil, err
	}
	closeOnError = false
	return store, contents, nil
}

// acquireLock protects the complete load/mutate/rewrite lifetime. The lock
// inode is opened relative to the already-verified directory handle, so a
// later pathname replacement cannot redirect it. flock is released by the
// kernel on process death; we intentionally retain the same checked inode and
// never remove a stale-looking lock file ourselves.
func (s *secureJournalStore) acquireLock() error {
	fd, err := unix.Openat(s.dirfd, journalLockFilename, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open runner journal lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), journalLockFilename)
	cleanup := func(cause error) error { _ = lock.Close(); return cause }
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return cleanup(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return cleanup(errors.New("runner journal lock must be an owner-only 0600 singly-linked regular file"))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return cleanup(errors.New("runner journal is already in use"))
		}
		return cleanup(fmt.Errorf("lock runner journal: %w", err))
	}
	// The directory fsync gives a newly-created lock entry crash durability. It
	// is harmless for an existing entry and occurs before journal state is read.
	if err := unix.Fsync(s.dirfd); err != nil {
		return cleanup(err)
	}
	s.lock = lock
	return nil
}

func (s *secureJournalStore) read(maxBytes int) ([]byte, error) {
	fd, err := unix.Openat(s.dirfd, journalStateFilename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), journalStateFilename)
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("runner journal state must be an owner-only 0600 regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxBytes {
		return nil, errors.New("runner journal byte limit exceeded")
	}
	return contents, nil
}

func (s *secureJournalStore) Write(contents []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	suffix, err := NewJournalID("tmp")
	if err != nil {
		return err
	}
	temporary := "." + journalStateFilename + "." + strings.TrimPrefix(suffix, "tmp_")
	fd, err := unix.Openat(s.dirfd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = unix.Unlinkat(s.dirfd, temporary, 0)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(s.dirfd, temporary, s.dirfd, journalStateFilename); err != nil {
		return err
	}
	removeTemp = false
	return unix.Fsync(s.dirfd)
}

func (s *secureJournalStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var first error
	if s.lock != nil {
		if err := unix.Flock(int(s.lock.Fd()), unix.LOCK_UN); err != nil && first == nil {
			first = err
		}
		if err := s.lock.Close(); err != nil && first == nil {
			first = err
		}
		s.lock = nil
	}
	if err := s.dir.Close(); err != nil && first == nil {
		first = err
	}
	return first
}
