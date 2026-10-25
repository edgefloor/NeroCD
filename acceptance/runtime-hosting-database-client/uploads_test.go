package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func uploadTestHandler(t *testing.T, directory, instance string) (*httpUploadHandler, http.Handler) {
	t.Helper()
	uploads, err := newHTTPUploadHandler(directory, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	application, err := httpDatabaseInstanceHandler(instance, httpDatabaseUploads(httpDatabaseHandler(func(context.Context, string, string, string) (string, error) { return "retained_db_record", nil }), uploads))
	if err != nil {
		_ = uploads.Close()
		t.Fatal(err)
	}
	return uploads, application
}

func uploadTestRequest(handler http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, body))
	return response
}

func TestStorageReadinessWritesBeforeFailureAndOnlyOnce(t *testing.T) {
	directory := t.TempDir()
	uploads, err := newHTTPUploadHandler(directory, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uploads.Close() })
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	count := 0
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	handler := httpStorageWriteThenUnready(base, uploads, func(ctx context.Context, action, key, value string) (string, error) {
		count++
		if action != "put" || key != "candidate_write" || value != "retained_candidate_before_recovery" {
			t.Error("readiness wrote an unexpected database record")
		}
		data, err := os.ReadFile(filepath.Join(directory, "candidate_write"))
		want := make([]byte, 65537)
		for i := range want {
			want[i] = byte(i*31) ^ 66
		}
		if err != nil || !bytes.Equal(data, want) {
			t.Error("database write began before the atomic upload completed")
		}
		close(entered)
		select {
		case <-release:
			return value, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	for _, target := range []struct{ method, path string }{{"POST", "/fixture-write-then-unready"}, {"GET", "/fixture-write-then-unready?extra=1"}, {"GET", "/ready"}} {
		if uploadTestRequest(handler, target.method, target.path, nil).Code != 404 {
			t.Fatal("controlled failure escaped its fixed readiness path")
		}
	}
	if count != 0 {
		t.Fatal("non-readiness request triggered candidate writes")
	}
	results := make(chan int, 9)
	go func() { results <- uploadTestRequest(handler, "GET", "/fixture-write-then-unready", nil).Code }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("readiness did not reach deterministic database write")
	}
	select {
	case <-results:
		t.Fatal("readiness responded before candidate writes completed")
	default:
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { results <- uploadTestRequest(handler, "GET", "/fixture-write-then-unready", nil).Code })
	}
	releaseOnce.Do(func() { close(release) })
	group.Wait()
	for range 9 {
		if <-results != 503 {
			t.Fatal("controlled readiness failure became ready")
		}
	}
	if count != 1 {
		t.Fatal("repeated readiness probes rewrote candidate data")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 || files[0].Name() != "candidate_write" {
		t.Fatal("controlled failure left unexpected upload files")
	}
	if response := uploadTestRequest(httpStorageWriteThenUnready(base, nil, nil), "GET", "/fixture-write-then-unready", nil); response.Code != 404 {
		t.Fatal("storage failure path escaped into storage-less image")
	}
}

func TestUploadRetainsBytesAndModesAcrossApplicationReplacement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "uploads")
	a, appA := uploadTestHandler(t, directory, strings.Repeat("a", 12))
	data := bytes.Repeat([]byte{0, 1, 127, 255}, 16385)[:65537]
	put := uploadTestRequest(appA, "PUT", "/uploads/a", bytes.NewReader(data))
	if put.Code != 201 || put.Header().Get("X-NeroCD-Fixture-Instance") != strings.Repeat("a", 12) {
		t.Fatal("A upload lacks actual instance or success")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, appB := uploadTestHandler(t, directory, strings.Repeat("b", 12))
	t.Cleanup(func() { _ = b.Close() })
	get := uploadTestRequest(appB, "GET", "/uploads/a", nil)
	if get.Code != 200 || get.Header().Get("X-NeroCD-Fixture-Instance") != strings.Repeat("b", 12) || len(get.Body.Bytes()) != 65537 || sha256.Sum256(get.Body.Bytes()) != sha256.Sum256(data) {
		t.Fatal("B did not read exact retained upload")
	}
	for path, mode := range map[string]os.FileMode{directory: 0750, filepath.Join(directory, "a"): 0640} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("retained upload permissions changed")
		}
	}
	if put := uploadTestRequest(appB, "PUT", "/uploads/b", strings.NewReader("new_b_data")); put.Code != 201 {
		t.Fatal("B upload write failed")
	}
	if get := uploadTestRequest(appB, "GET", "/uploads/b", nil); get.Code != 200 || get.Body.String() != "new_b_data" {
		t.Fatal("B new data not readable")
	}
	if record := uploadTestRequest(appB, "GET", "/records/a", nil); record.Code != 200 || record.Body.String() != "retained_db_record" {
		t.Fatal("upload handler changed database record routing")
	}
}

type uploadBrokenBody struct{ sent bool }

func (b *uploadBrokenBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("PRIVATE_UPLOAD_CANARY")
}

func TestUploadFailurePreservesPreviousFileAndRemovesTemporary(t *testing.T) {
	directory := t.TempDir()
	uploads, app := uploadTestHandler(t, directory, strings.Repeat("a", 12))
	t.Cleanup(func() { _ = uploads.Close() })
	if uploadTestRequest(app, "PUT", "/uploads/a", strings.NewReader("retained")).Code != 201 {
		t.Fatal("initial upload failed")
	}
	for _, mode := range []string{"empty", "oversize", "broken", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var body io.Reader = strings.NewReader("")
			if mode == "oversize" {
				body = bytes.NewReader(bytes.Repeat([]byte{'x'}, httpUploadLimit+1))
			}
			if mode == "broken" {
				body = &uploadBrokenBody{}
			}
			request := httptest.NewRequest("PUT", "/uploads/a", body)
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != 400 || strings.Contains(response.Body.String(), "PRIVATE_UPLOAD_CANARY") {
				t.Fatal("failed upload succeeded or exposed reader error")
			}
			if get := uploadTestRequest(app, "GET", "/uploads/a", nil); get.Code != 200 || get.Body.String() != "retained" {
				t.Fatal("failed upload replaced previous file")
			}
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 1 || files[0].Name() != "a" {
				t.Fatal("failed upload left temporary files")
			}
		})
	}
}

func TestUploadRejectsTraversalSymlinksAndUnsafeMetadata(t *testing.T) {
	directory := t.TempDir()
	uploads, app := uploadTestHandler(t, directory, strings.Repeat("a", 12))
	t.Cleanup(func() { _ = uploads.Close() })
	for _, path := range []string{"/uploads/../a", "/uploads/a/b", "/uploads/.upload-test", "/uploads/", "/uploads/a?secret=PRIVATE_UPLOAD_CANARY", "/uploads/" + strings.Repeat("a", 65)} {
		if response := uploadTestRequest(app, "PUT", path, strings.NewReader("x")); response.Code != 400 || strings.Contains(response.Body.String(), "PRIVATE_UPLOAD_CANARY") {
			t.Fatal("unsafe upload key accepted or reflected")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if os.WriteFile(outside, []byte("outside"), 0640) != nil {
		t.Fatal("outside setup failed")
	}
	if os.Symlink(outside, filepath.Join(directory, "a")) != nil {
		t.Fatal("symlink setup failed")
	}
	for _, method := range []string{"GET", "PUT"} {
		if uploadTestRequest(app, method, "/uploads/a", strings.NewReader("x")).Code != 409 {
			t.Fatal("symlink upload target accepted")
		}
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatal("upload changed outside file")
	}
	if os.Remove(filepath.Join(directory, "a")) != nil || os.WriteFile(filepath.Join(directory, "a"), []byte("wrong_mode"), 0600) != nil {
		t.Fatal("metadata setup failed")
	}
	if uploadTestRequest(app, "GET", "/uploads/a", nil).Code != 409 {
		t.Fatal("unsafe upload metadata accepted")
	}
	if uploadTestRequest(app, "GET", "/uploads/missing", nil).Code != 404 {
		t.Fatal("missing upload did not stay missing")
	}
}

func TestUploadConcurrentReadersObserveWholeFiles(t *testing.T) {
	uploads, app := uploadTestHandler(t, t.TempDir(), strings.Repeat("a", 12))
	t.Cleanup(func() { _ = uploads.Close() })
	a, b := bytes.Repeat([]byte{'a'}, 65537), bytes.Repeat([]byte{'b'}, 65537)
	if uploadTestRequest(app, "PUT", "/uploads/a", bytes.NewReader(a)).Code != 201 {
		t.Fatal("initial upload failed")
	}
	var group sync.WaitGroup
	for i := range 16 {
		group.Go(func() {
			if i%2 == 0 {
				if uploadTestRequest(app, "PUT", "/uploads/a", bytes.NewReader(b)).Code != 201 {
					t.Error("concurrent upload failed")
				}
				return
			}
			get := uploadTestRequest(app, "GET", "/uploads/a", nil)
			if get.Code != 200 || !bytes.Equal(get.Body.Bytes(), a) && !bytes.Equal(get.Body.Bytes(), b) {
				t.Error("concurrent read saw partial upload")
			}
		})
	}
	group.Wait()
}

func TestBaselineDatabaseProfileHasNoUploadFilesystem(t *testing.T) {
	app := httpDatabaseUploads(httpDatabaseHandler(func(context.Context, string, string, string) (string, error) { return "ready", nil }), nil)
	if uploadTestRequest(app, "PUT", "/uploads/a", strings.NewReader("x")).Code != 404 {
		t.Fatal("baseline image unexpectedly accepted upload")
	}
	if uploadTestRequest(app, "GET", "/ready", nil).Code != 200 {
		t.Fatal("baseline database handler changed")
	}
}
