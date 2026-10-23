package hostingproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type receipt struct {
	Identity         Identity
	StaticHash, Hash string
	Generation       uint64
	Routes           []Route
	Phase            string
	Verification     *verificationPolicy `json:",omitempty"`
}
type storage struct {
	root *os.Root
	lock *os.File
}

func secureDirectory(p string) error {
	// Reject symlinks in every existing ancestor before creating the final owned directory.
	for d := p; ; d = filepath.Dir(d) {
		st, err := os.Lstat(d)
		if err == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("unsafe directory ancestor")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	if err := os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("state directory requires mode 0700")
	}
	return owned(st)
}
func openStorage(ctx context.Context, directory string) (*storage, error) {
	if err := secureDirectory(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	s := &storage{root: root}
	ok := false
	defer func() {
		if !ok {
			s.close()
		}
	}()
	for _, dir := range []string{"dynamic", "docker-config", "certificates"} {
		if err = root.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		st, err := root.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
			return nil, errors.New("unsafe state subdirectory")
		}
		if err = owned(st); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(root.FS(), "docker-config")
	if err != nil || len(entries) != 0 {
		return nil, errors.New("docker config must be empty")
	}
	s.lock, err = root.OpenFile("lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = s.checkFile("lock"); err != nil {
		return nil, err
	}
	for {
		err = tryLock(s.lock)
		if err == nil {
			break
		}
		if !errors.Is(err, errBusy) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	ok = true
	return s, nil
}
func (s *storage) close() {
	if s.lock != nil {
		_ = s.lock.Close()
	}
	if s.root != nil {
		_ = s.root.Close()
	}
}
func (s *storage) checkFile(name string) error {
	st, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return fmt.Errorf("unsafe state file %s", name)
	}
	return owned(st)
}
func (s *storage) read(name string) (data []byte, err error) {
	if err := s.checkFile(name); err != nil {
		return nil, err
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	st, err := f.Stat()
	if err != nil || st.Size() > 4<<20 {
		return nil, errors.New("oversized state")
	}
	return fs.ReadFile(s.root.FS(), name)
}
func (s *storage) load() (receipt, error) {
	b, e := s.read("receipt.json")
	if errors.Is(e, os.ErrNotExist) {
		return receipt{}, nil
	}
	if e != nil {
		return receipt{}, e
	}
	var r receipt
	e = json.Unmarshal(b, &r)
	return r, e
}
func (s *storage) save(r receipt) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return s.atomic("receipt.json", b)
}
func (s *storage) atomic(name string, b []byte) (err error) {
	if _, err := s.root.Lstat(name); err == nil {
		if err = s.checkFile(name); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The candidate lives outside the watched directory. Both directories share a filesystem.
	f, err := s.root.OpenFile("candidate", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		// Successful rename removes this name. Any other cleanup failure leaves
		// state that the caller must reconcile instead of reporting clean success.
		if removeErr := s.root.Remove("candidate"); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove unpublished candidate: %w", removeErr))
		}
	}()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = s.root.Rename("candidate", name); err != nil {
		return err
	}
	dir, err := s.root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	return dir.Sync()
}
