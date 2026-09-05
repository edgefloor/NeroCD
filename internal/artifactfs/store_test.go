//go:build linux || darwin

package artifactfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPutAndOpenBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		max     int64
	}{
		{name: "empty", content: []byte{}, max: 1},
		{name: "exact limit", content: []byte("exact"), max: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := secureTempRoot(t)
			store, err := OpenStore(root, test.max)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			key := digest(test.content)
			blob, err := store.Put(t.Context(), bytes.NewReader(test.content), key, int64(len(test.content)))
			if err != nil {
				t.Fatal(err)
			}
			if blob != (Blob{Key: key, SHA256: key, Size: int64(len(test.content))}) {
				t.Fatalf("blob = %#v", blob)
			}
			info, err := os.Stat(filepath.Join(root, key))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
				t.Fatalf("stored mode = %v", info.Mode())
			}

			handle, opened, err := store.Open(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			got, err := io.ReadAll(handle)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.content) || opened != blob {
				t.Fatalf("content=%q opened=%#v", got, opened)
			}
			if _, err := handle.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPutRejectsInvalidContentWithoutPublication(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tests := []struct {
		name     string
		reader   io.Reader
		key      string
		size     int64
		want     error
		finalKey string
	}{
		{name: "hash mismatch", reader: strings.NewReader("data"), key: digest([]byte("else")), size: 4, want: ErrIntegrity},
		{name: "short reader", reader: strings.NewReader("abc"), key: digest([]byte("abcd")), size: 4, want: ErrIntegrity},
		{name: "long reader", reader: strings.NewReader("abcde"), key: digest([]byte("abcd")), size: 4, want: ErrIntegrity},
		{name: "read error", reader: failingReader{}, key: digest([]byte("a")), size: 1, want: ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Put(t.Context(), test.reader, test.key, test.size)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.name == "read error" && strings.Contains(err.Error(), "source detail") {
				t.Fatalf("source error detail escaped: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, test.key)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final object exists after failure: %v", err)
			}
			assertNoTemps(t, root)
		})
	}

	reader := &countingReader{reader: strings.NewReader("12345")}
	_, err = store.Put(t.Context(), reader, digest([]byte("12345")), 9)
	if !errors.Is(err, ErrInvalid) || reader.reads != 0 {
		t.Fatalf("oversize error=%v reads=%d", err, reader.reads)
	}
}

func TestPutCancellation(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(t.Context(), nil, strings.Repeat("0", 64), 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil source error = %v", err)
	}
	if _, err := store.Put(t.Context(), strings.NewReader(""), strings.Repeat("0", 64), -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative size error = %v", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Put(canceled, strings.NewReader("data"), digest([]byte("data")), 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}

	ctx, cancelDuring := context.WithCancel(t.Context())
	reader := &cancelingReader{cancel: cancelDuring, content: []byte("abcdefgh")}
	key := digest(reader.content)
	if _, err := store.Put(ctx, reader, key, int64(len(reader.content))); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-stream error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled object exists: %v", err)
	}
	assertNoTemps(t, root)
}

func TestZeroStoreFailsClosed(t *testing.T) {
	var store Store
	key := digest(nil)
	if _, err := store.Put(t.Context(), bytes.NewReader(nil), key, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("zero Store Put = %v", err)
	}
	if _, _, err := store.Open(t.Context(), key); !errors.Is(err, ErrClosed) {
		t.Fatalf("zero Store Open = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("zero Store Close = %v", err)
	}
}

func TestUnsafeRootAndKeys(t *testing.T) {
	root := secureTempRoot(t)
	if _, err := OpenStore("relative", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative root error = %v", err)
	}
	if _, err := OpenStore(root+string(filepath.Separator), 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unclean root error = %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(root, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("root mode error = %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, max := range []int64{0, -1, maxConfiguredBytes + 1} {
		if _, err := OpenStore(root, max); !errors.Is(err, ErrInvalid) {
			t.Fatalf("max %d error = %v", max, err)
		}
	}

	parent := secureTempRoot(t)
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(parent, "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(linkedRoot, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink component error = %v", err)
	}

	store, err := OpenStore(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, key := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), "../" + strings.Repeat("a", 64), strings.Repeat("g", 64)} {
		if _, _, err := store.Open(t.Context(), key); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open key %q error = %v", key, err)
		}
		if _, err := store.Put(t.Context(), strings.NewReader(""), key, 0); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Put key %q error = %v", key, err)
		}
	}
	if _, _, err := store.Open(t.Context(), strings.Repeat("0", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestOpenRejectsUnsafeOrTamperedObjectsWithoutBlocking(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root, key string)
	}{
		{name: "symlink", setup: func(t *testing.T, root, key string) {
			t.Helper()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, key)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, root, key string) {
			t.Helper()
			path := filepath.Join(root, key)
			if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, filepath.Join(root, "other")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", setup: func(t *testing.T, root, key string) {
			t.Helper()
			if err := unix.Mkfifo(filepath.Join(root, key), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong mode", setup: func(t *testing.T, root, key string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, key), []byte("safe"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreadable mode", setup: func(t *testing.T, root, key string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, key), []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, key), 0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong digest", setup: func(t *testing.T, root, key string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, key), []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := secureTempRoot(t)
			store, err := OpenStore(root, 64)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			key := digest([]byte("safe"))
			test.setup(t, root, key)
			if _, _, err := store.Open(t.Context(), key); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestOpenDetectsTamperAfterPutAndPutNeverOverwrites(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte("original")
	key := digest(content)
	if _, err := store.Put(t.Context(), bytes.NewReader(content), key, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, key), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(t.Context(), key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper error = %v", err)
	}
	if _, err := store.Put(t.Context(), bytes.NewReader(content), key, int64(len(content))); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("existing tamper put error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, key))
	if err != nil || string(got) != "tampered" {
		t.Fatalf("existing object overwritten: %q, %v", got, err)
	}
}

func TestRootRenameRemainsDescriptorConfined(t *testing.T) {
	parent := secureTempRoot(t)
	root := filepath.Join(parent, "root")
	moved := filepath.Join(parent, "moved")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("confined")
	key := digest(content)
	if _, err := store.Put(t.Context(), bytes.NewReader(content), key, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, key)); err != nil {
		t.Fatalf("object missing from original descriptor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object escaped to replacement root: %v", err)
	}
}

func TestConcurrentSameKeyAndCloseLifecycle(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("concurrent-content"), 1024)
	key := digest(content)

	const writers = 16
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, putErr := store.Put(t.Context(), bytes.NewReader(content), key, int64(len(content)))
			errorsByWriter <- putErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}
	assertNoTemps(t, root)

	handle, _, err := store.Open(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := store.Put(t.Context(), bytes.NewReader(content), key, int64(len(content))); !errors.Is(err, ErrClosed) {
		t.Fatalf("Put after Close = %v", err)
	}
	if _, _, err := store.Open(t.Context(), key); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open after Close = %v", err)
	}
	got, err := io.ReadAll(handle)
	if closeErr := handle.Close(); err != nil || closeErr != nil || !bytes.Equal(got, content) {
		t.Fatalf("independent handle content=%d read=%v close=%v", len(got), err, closeErr)
	}
}

func TestCloseWaitsForInFlightPut(t *testing.T) {
	root := secureTempRoot(t)
	store, err := OpenStore(root, 64)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := &gatedReader{entered: entered, release: release, content: []byte("blocked")}
	putDone := make(chan error, 1)
	go func() {
		_, putErr := store.Put(t.Context(), reader, digest(reader.content), int64(len(reader.content)))
		putDone <- putErr
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned during active Put: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-putDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func secureTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func assertNoTemps(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".artifactfs-put-") {
			t.Fatalf("temporary object remains: %s", entry.Name())
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("source detail must not escape") }

type countingReader struct {
	reader io.Reader
	reads  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

type cancelingReader struct {
	cancel  context.CancelFunc
	content []byte
	done    bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.content[:len(r.content)/2])
	r.cancel()
	return n, nil
}

type gatedReader struct {
	entered chan<- struct{}
	release <-chan struct{}
	content []byte
	done    bool
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	close(r.entered)
	<-r.release
	return copy(p, r.content), nil
}
