// Package artifactfs stores bounded content-addressed blobs in a private local
// directory. It is a filesystem foundation; authorization remains the
// responsibility of a future consumer.
package artifactfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
)

const maxConfiguredBytes = int64(1 << 30)

var (
	// ErrInvalid reports an invalid store configuration, key, size, or source.
	ErrInvalid = errors.New("artifactfs: invalid input")
	// ErrIntegrity reports content or filesystem metadata that violates the
	// content-addressed store invariants.
	ErrIntegrity = errors.New("artifactfs: integrity failure")
	// ErrClosed reports an operation attempted after Store.Close.
	ErrClosed = errors.New("artifactfs: store closed")
	// ErrUnsupported reports a platform without the required confined filesystem operations.
	ErrUnsupported = errors.New("artifactfs: unsupported platform")
)

var keyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Blob describes one verified content-addressed object.
type Blob struct {
	Key    string
	SHA256 string
	Size   int64
}

// Store confines all filesystem access to an owned private directory.
// Its methods and Close are safe for concurrent use. A handle returned by Open
// remains valid after Store.Close and must be closed by the caller.
type Store struct {
	mu            sync.RWMutex
	backend       *directoryBackend
	syncDirectory func() error
	maxBytes      int64
	closed        bool
}

// OpenStore opens an existing absolute root. Every path component must be a
// real directory, and the root itself must be owned by the effective user with
// exact mode 0700.
func OpenStore(root string, maxBytes int64) (*Store, error) {
	if maxBytes <= 0 || maxBytes > maxConfiguredBytes {
		return nil, invalidError("invalid size limit")
	}
	backend, err := openDirectoryBackend(root)
	if err != nil {
		return nil, err
	}
	return &Store{backend: backend, maxBytes: maxBytes, syncDirectory: backend.sync}, nil
}

// Put verifies and atomically publishes exactly size bytes under their
// expected lowercase SHA-256 key. Put reads at most size+1 bytes and never
// buffers the complete object. Context cancellation is checked between reads;
// as with all io.Reader APIs, the caller controls interruption of a blocked
// source Read call.
func (s *Store) Put(ctx context.Context, src io.Reader, expectedSHA256 string, size int64) (Blob, error) {
	blob, _, err := s.PutOwned(ctx, src, expectedSHA256, size)
	return blob, err
}

// Ownership identifies a newly published inode in one verified private pool.
// It is protected runner evidence, never a public filesystem location.
type Ownership struct {
	PoolID  string `json:"pool_id"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Bytes   int64  `json:"bytes"`
	Created bool   `json:"created"`
}

// PutOwned distinguishes publication from adoption. A matching preexisting
// object is readable but cannot acquire creation authority merely by adoption.
func (s *Store) PutOwned(ctx context.Context, src io.Reader, expectedSHA256 string, size int64) (Blob, Ownership, error) {
	if ctx == nil || src == nil || !validKey(expectedSHA256) || size < 0 {
		return Blob{}, Ownership{}, invalidError("invalid put request")
	}
	if err := contextError(ctx); err != nil {
		return Blob{}, Ownership{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.backend == nil {
		return Blob{}, Ownership{}, ErrClosed
	}
	if err := s.backend.lock(ctx); err != nil {
		return Blob{}, Ownership{}, err
	}
	defer s.backend.unlock()
	if size > s.maxBytes {
		return Blob{}, Ownership{}, invalidError("object exceeds size limit")
	}

	tempName, temp, err := s.backend.createTemp()
	if err != nil {
		return Blob{}, Ownership{}, err
	}
	published := false
	defer func() {
		_ = temp.Close()
		if !published {
			_ = s.backend.remove(tempName)
		}
	}()

	hash := sha256.New()
	written, err := copyBounded(ctx, io.MultiWriter(temp, hash), src, size)
	if err != nil {
		return Blob{}, Ownership{}, err
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return Blob{}, Ownership{}, ErrIntegrity
	}
	if err := temp.Sync(); err != nil {
		return Blob{}, Ownership{}, ioError("sync temporary object", err)
	}
	if err := temp.Close(); err != nil {
		return Blob{}, Ownership{}, ioError("close temporary object", err)
	}
	if err := contextError(ctx); err != nil {
		return Blob{}, Ownership{}, err
	}

	err = s.backend.renameNoReplace(tempName, expectedSHA256)
	if err == nil {
		published = true
		if err := s.syncDirectory(); err != nil {
			return Blob{}, Ownership{}, ioError("sync store directory", err)
		}
		proof, err := s.backend.ownership(expectedSHA256, s.maxBytes)
		if err != nil {
			return Blob{}, Ownership{}, err
		}
		proof.Created = true
		return Blob{Key: expectedSHA256, SHA256: expectedSHA256, Size: size}, proof, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return Blob{}, Ownership{}, err
	}

	// A concurrent writer won. Treat it as success only after independently
	// verifying the published object; never overwrite it.
	existing, blob, err := s.openVerified(ctx, expectedSHA256)
	if err != nil {
		return Blob{}, Ownership{}, err
	}
	if closeErr := existing.Close(); closeErr != nil {
		return Blob{}, Ownership{}, ioError("close existing object", closeErr)
	}
	if blob.Size != size {
		return Blob{}, Ownership{}, ErrIntegrity
	}
	proof, err := s.backend.ownership(expectedSHA256, s.maxBytes)
	return blob, proof, err
}

// Open returns a read/seek handle only after verifying ownership, permissions,
// type, link count, bounded size, and the complete content digest.
func (s *Store) Open(ctx context.Context, key string) (io.ReadSeekCloser, Blob, error) {
	if ctx == nil || !validKey(key) {
		return nil, Blob{}, invalidError("invalid object key")
	}
	if err := contextError(ctx); err != nil {
		return nil, Blob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.backend == nil {
		return nil, Blob{}, ErrClosed
	}
	if err := s.backend.lock(ctx); err != nil {
		return nil, Blob{}, err
	}
	defer s.backend.unlock()
	return s.openVerified(ctx, key)
}

func (s *Store) openVerified(ctx context.Context, key string) (*os.File, Blob, error) {
	file, metadata, err := s.backend.openRegular(key, s.maxBytes)
	if err != nil {
		return nil, Blob{}, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()

	hash := sha256.New()
	read, err := copyBounded(ctx, hash, file, metadata.size)
	if err != nil {
		return nil, Blob{}, err
	}
	if read != metadata.size || hex.EncodeToString(hash.Sum(nil)) != key {
		return nil, Blob{}, ErrIntegrity
	}
	if err := s.backend.verifyOpenFile(file, metadata, s.maxBytes); err != nil {
		return nil, Blob{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, Blob{}, ioError("reset object", err)
	}
	valid = true
	return file, Blob{Key: key, SHA256: key, Size: read}, nil
}

// Close prevents new operations and waits for in-flight Store operations.
// It does not invalidate file handles already returned by Open.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.backend == nil {
		return nil
	}
	return s.backend.close()
}

func copyBounded(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	remaining := size + 1
	buffer := make([]byte, 32*1024)
	var total int64
	for remaining > 0 {
		if err := contextError(ctx); err != nil {
			return total, err
		}
		readSize := int64(len(buffer))
		if readSize > remaining {
			readSize = remaining
		}
		n, readErr := src.Read(buffer[:readSize])
		if n < 0 || n > int(readSize) {
			return total, invalidError("source returned invalid count")
		}
		if n > 0 {
			total += int64(n)
			remaining -= int64(n)
			if total > size {
				return total, ErrIntegrity
			}
			if _, err := dst.Write(buffer[:n]); err != nil {
				return total, ioError("write object", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, invalidError("source read failed")
		}
		if n == 0 {
			return total, invalidError("source made no progress")
		}
	}
	return total, nil
}

func validKey(key string) bool { return keyPattern.MatchString(key) }

func contextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func invalidError(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }

func ioError(operation string, err error) error {
	return fmt.Errorf("artifactfs: %s: %w", operation, err)
}

// OpenOwned verifies digest and exact inode ownership on the same returned descriptor.
// The caller owns the descriptor; no second path lookup selects its contents.
func (s *Store) OpenOwned(ctx context.Context, key string, proof Ownership) (io.ReadSeekCloser, Blob, error) {
	if ctx == nil || !validKey(key) || (proof.PoolID == "" || proof.Inode == 0 || proof.Bytes < 1) {
		return nil, Blob{}, ErrIntegrity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.backend == nil {
		return nil, Blob{}, ErrClosed
	}
	if err := s.backend.lock(ctx); err != nil {
		return nil, Blob{}, err
	}
	defer s.backend.unlock()
	file, blob, err := s.openVerified(ctx, key)
	if err != nil {
		return nil, Blob{}, err
	}
	if err = s.backend.verifyOwnershipFile(file, proof); err != nil {
		_ = file.Close()
		return nil, Blob{}, err
	}
	return file, blob, nil
}
