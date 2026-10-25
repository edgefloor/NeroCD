package artifactfs

import (
	"context"
	"errors"
	"os"
)

// RemoveOwned removes only the exact saved, verified CAS inode. It never picks
// another object on retry. An absent object is an observation, not proof that a
// caller originally created it; admission must retain the original ownership.
func (s *Store) RemoveOwned(ctx context.Context, key string, proof Ownership) (absent bool, err error) {
	return s.RemoveOwnedWithAuthority(ctx, key, proof, func(c context.Context) error { return contextError(c) })
}

// RemoveOwnedWithAuthority rechecks consumer authority immediately before unlink.
func (s *Store) RemoveOwnedWithAuthority(ctx context.Context, key string, proof Ownership, before func(context.Context) error) (absent bool, err error) {
	if before == nil || ctx == nil || !validKey(key) || !proof.Created || proof.Inode == 0 || proof.PoolID == "" || proof.Bytes < 0 {
		return false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.backend == nil {
		return false, ErrClosed
	}
	if err = s.backend.lock(ctx); err != nil {
		return false, err
	}
	defer s.backend.unlock()
	pool, err := s.backend.poolID()
	if err != nil {
		return false, err
	}
	if pool != proof.PoolID {
		return false, ErrIntegrity
	}
	current, err := s.backend.ownership(key, s.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		if err = contextError(ctx); err != nil {
			return false, err
		}
		if err = before(ctx); err != nil {
			return false, err
		}
		return true, s.syncDirectory()
	}
	if err != nil {
		return false, err
	}
	current.Created = true
	if current != proof {
		return false, ErrIntegrity
	}
	file, blob, err := s.openVerified(ctx, key)
	if err != nil {
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if blob.Size != proof.Bytes {
		return false, ErrIntegrity
	}
	// Recheck the directory entry after the content read, under the pool lock.
	current, err = s.backend.ownership(key, s.maxBytes)
	if err != nil {
		return false, err
	}
	current.Created = true
	if current != proof {
		return false, ErrIntegrity
	}
	if err = contextError(ctx); err != nil {
		return false, err
	}
	if err = before(ctx); err != nil {
		return false, err
	}
	if err = s.backend.remove(key); err != nil {
		return false, err
	}
	return false, s.syncDirectory()
}

// ObserveOwned reconciles the saved identity without unlinking anything.
func (s *Store) ObserveOwned(ctx context.Context, key string, proof Ownership, before func(context.Context) error) (bool, error) {
	if ctx == nil || before == nil || !validKey(key) || !proof.Created {
		return false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.backend == nil {
		return false, ErrClosed
	}
	if err := s.backend.lock(ctx); err != nil {
		return false, err
	}
	defer s.backend.unlock()
	pool, err := s.backend.poolID()
	if err != nil {
		return false, err
	}
	if pool != proof.PoolID {
		return false, ErrIntegrity
	}
	current, err := s.backend.ownership(key, s.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		if err = before(ctx); err != nil {
			return false, err
		}
		return true, s.syncDirectory()
	}
	if err != nil {
		return false, err
	}
	current.Created = true
	if current != proof {
		return false, ErrIntegrity
	}
	return false, before(ctx)
}
