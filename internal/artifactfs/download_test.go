//go:build linux || darwin

package artifactfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOwnedPinsExactDownloadDescriptor(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte("exact archived upload")
	digest := sha256.Sum256(content)
	key := hex.EncodeToString(digest[:])
	blob, proof, err := store.PutOwned(context.Background(), bytes.NewReader(content), key, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	f, got, err := store.OpenOwned(context.Background(), key, proof)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got != blob {
		t.Fatal("blob changed")
	}
	// A later path replacement cannot redirect an already pinned descriptor.
	if err = os.Rename(filepath.Join(root, key), filepath.Join(root, "held")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, key), content, 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("pinned descriptor changed")
	}
	if next, _, err := store.OpenOwned(context.Background(), key, proof); err == nil {
		next.Close()
		t.Fatal("replacement inode adopted")
	}
	wrong := proof
	wrong.PoolID = "wrong"
	if next, _, err := store.OpenOwned(context.Background(), key, wrong); err == nil {
		next.Close()
		t.Fatal("wrong pool accepted")
	}
	if err = os.WriteFile(filepath.Join(root, key), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if next, _, err := store.OpenOwned(context.Background(), key, proof); err == nil {
		next.Close()
		t.Fatal("tampered object accepted")
	}
}
