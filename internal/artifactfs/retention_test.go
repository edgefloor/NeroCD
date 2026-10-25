package artifactfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedPublicationAndExactRemoval(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := []byte("PGDMP fixture")
	key := fmt.Sprintf("%x", sha256.Sum256(data))
	_, proof, err := s.PutOwned(t.Context(), bytes.NewReader(data), key, int64(len(data)))
	if err != nil || !proof.Created {
		t.Fatalf("publish %v", err)
	}
	_, adopted, err := s.PutOwned(t.Context(), bytes.NewReader(data), key, int64(len(data)))
	if err != nil || adopted.Created {
		t.Fatalf("adoption %v", err)
	}
	wrong := proof
	wrong.Inode++
	if _, err = s.RemoveOwned(t.Context(), key, wrong); err == nil {
		t.Fatal("changed inode accepted")
	}
	if _, err = s.RemoveOwned(t.Context(), key, adopted); err == nil {
		t.Fatal("adoption grants deletion")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.RemoveOwned(canceled, key, proof); err == nil {
		t.Fatal("cancelled delete")
	}
	if absent, err := s.RemoveOwned(t.Context(), key, proof); err != nil || absent {
		t.Fatalf("remove absent=%v err=%v", absent, err)
	}
	if absent, err := s.RemoveOwned(t.Context(), key, proof); err != nil || !absent {
		t.Fatalf("reconcile absent=%v err=%v", absent, err)
	}
}

func TestOwnedRemovalAbsenceMustSyncAfterUncertainUnlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body := []byte("PGDMP retained")
	key := fmt.Sprintf("%x", sha256.Sum256(body))
	_, proof, err := s.PutOwned(t.Context(), bytes.NewReader(body), key, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("sync uncertain")
	calls := 0
	real := s.syncDirectory
	s.syncDirectory = func() error {
		calls++
		if calls == 1 {
			return injected
		}
		return real()
	}
	if _, err = s.RemoveOwned(t.Context(), key, proof); !errors.Is(err, injected) {
		t.Fatalf("expected sync uncertainty: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.RemoveOwned(canceled, key, proof); !errors.Is(err, context.Canceled) {
		t.Fatalf("absent cancellation: %v", err)
	}
	if absent, err := s.RemoveOwned(t.Context(), key, proof); err != nil || !absent || calls != 2 {
		t.Fatalf("absence must resync: absent=%v calls=%d err=%v", absent, calls, err)
	}
	wrong := proof
	wrong.PoolID = "sha256:" + fmt.Sprintf("%064d", 0)
	if _, err = s.RemoveOwned(t.Context(), key, wrong); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("foreign pool absence: %v", err)
	}
}
