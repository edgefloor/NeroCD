package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"
)

const httpUploadLimit = 1 << 20

// Only the opted-in disposable storage image exposes this fixed failure path.
// Its first readiness probe completes both deterministic writes before returning
// 503. Repeated probes cannot race another write or turn readiness successful.
func httpStorageWriteThenUnready(base http.Handler, uploads *httpUploadHandler, record func(context.Context, string, string, string) (string, error)) http.Handler {
	if uploads == nil {
		return base
	}
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fixture-write-then-unready" || r.URL.RawQuery != "" {
			base.ServeHTTP(w, r)
			return
		}
		once.Do(func() {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			data := make([]byte, 65537)
			for i := range data {
				data[i] = byte(i*31) ^ 66
			}
			defer clear(data)
			// Invoke the existing bounded handler directly; this URL is never dialed.
			request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://fixture.invalid/uploads/candidate_write", bytes.NewReader(data))
			if err != nil {
				return
			}
			response := httptest.NewRecorder()
			uploads.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				return
			}
			_, _ = record(ctx, "put", "candidate_write", "retained_candidate_before_recovery")
		})
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "fixture readiness intentionally rejected", http.StatusServiceUnavailable)
	})
}

// This handler is enabled only by the separately qualified root fixture image.
// It has no arbitrary path, remove, archive, or backup endpoint.
type httpUploadHandler struct {
	mu        sync.Mutex
	root      *os.Root
	directory *os.File
	uid, gid  int
}

func newHTTPUploadHandler(directory string, uid, gid int) (*httpUploadHandler, error) {
	invalid := errors.New("fixture upload directory unavailable")
	if uid < 0 || gid < 0 || os.MkdirAll(directory, 0750) != nil {
		return nil, invalid
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, invalid
	}
	file, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, invalid
	}
	if err := errors.Join(file.Chown(uid, gid), file.Chmod(0750)); err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, invalid
	}
	return &httpUploadHandler{root: root, directory: file, uid: uid, gid: gid}, nil
}

func (h *httpUploadHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(h.directory.Close(), h.root.Close())
}

func (h *httpUploadHandler) temporary() (*os.File, string, error) {
	// Root has no CreateTemp method. Exclusive creation with a cryptographically
	// random name preserves its directory confinement, including across renames.
	for range 8 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", err
		}
		name := ".upload-" + hex.EncodeToString(nonce[:])
		file, err := h.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", errors.New("fixture upload temporary creation failed")
}

func (h *httpUploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	key := strings.TrimPrefix(r.URL.Path, "/uploads/")
	if !strings.HasPrefix(r.URL.Path, "/uploads/") || !fixtureWord(key, 64) || r.URL.RawQuery != "" || (r.Method != http.MethodPut && r.Method != http.MethodGet) {
		http.Error(w, "invalid upload request", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Context().Err() != nil {
		http.Error(w, "upload request canceled", http.StatusBadRequest)
		return
	}
	info, err := h.root.Lstat(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		http.Error(w, "upload unavailable", http.StatusConflict)
		return
	}
	if r.Method == http.MethodGet {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}
		file, err := h.root.Open(key)
		if err != nil {
			http.Error(w, "upload unavailable", http.StatusInternalServerError)
			return
		}
		actual, statErr := file.Stat()
		if statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Mode().Perm() != 0640 || actual.Size() > httpUploadLimit {
			_ = file.Close()
			http.Error(w, "upload unavailable", http.StatusConflict)
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(file, httpUploadLimit+1))
		defer clear(body)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(body) > httpUploadLimit {
			http.Error(w, "upload unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
		return
	}
	file, temporary, err := h.temporary()
	if err != nil {
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close(); _ = h.root.Remove(temporary) }()
	n, copyErr := io.Copy(file, http.MaxBytesReader(w, r.Body, httpUploadLimit))
	if copyErr != nil || n == 0 || r.Context().Err() != nil {
		http.Error(w, "invalid upload body", http.StatusBadRequest)
		return
	}
	if err := errors.Join(file.Chown(h.uid, h.gid), file.Chmod(0640), file.Sync()); err != nil {
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
	if file.Close() != nil || h.root.Rename(temporary, key) != nil || h.directory.Sync() != nil {
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func httpDatabaseUploads(application http.Handler, uploads *httpUploadHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/uploads/") && uploads != nil {
			uploads.ServeHTTP(w, r)
			return
		}
		application.ServeHTTP(w, r)
	})
}
