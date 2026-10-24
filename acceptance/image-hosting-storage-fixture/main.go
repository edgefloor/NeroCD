// Command image-hosting-storage-fixture is a disposable HTTP upload application.
package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

var version = "development"

func main() {
	const directory = "/data/uploads"
	if err := os.MkdirAll(directory, 0700); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, version+"\n") })
	mux.HandleFunc("GET /unready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("PUT /upload", func(w http.ResponseWriter, r *http.Request) {
		file, err := os.CreateTemp(directory, ".upload-")
		if err != nil {
			http.Error(w, "create failed", 500)
			return
		}
		defer func() { _ = os.Remove(file.Name()) }()
		_, copyErr := io.Copy(file, http.MaxBytesReader(w, r.Body, 1<<20))
		syncErr := file.Sync()
		closeErr := file.Close()
		if errors.Join(copyErr, syncErr, closeErr) != nil || os.Rename(file.Name(), filepath.Join(directory, "pending")) != nil {
			http.Error(w, "upload failed", 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /rename", func(w http.ResponseWriter, _ *http.Request) {
		if err := os.Rename(filepath.Join(directory, "pending"), filepath.Join(directory, "saved")); err != nil {
			http.Error(w, "rename failed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /metadata", func(w http.ResponseWriter, _ *http.Request) {
		nested := filepath.Join(directory, "nested")
		if err := os.MkdirAll(nested, 0750); err != nil {
			http.Error(w, "metadata failed", 500)
			return
		}
		if err := os.WriteFile(filepath.Join(nested, "probe"), []byte("owned metadata probe\n"), 0640); err != nil {
			http.Error(w, "metadata failed", 500)
			return
		}
		for _, path := range []string{filepath.Join(directory, "saved"), nested, filepath.Join(nested, "probe")} {
			mode := os.FileMode(0640)
			if path == nested {
				mode = 0750
			}
			if err := errors.Join(os.Chown(path, 12345, 12346), os.Chmod(path, mode)); err != nil {
				http.Error(w, "metadata failed", 500)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /oversize", func(w http.ResponseWriter, _ *http.Request) {
		if err := os.WriteFile(filepath.Join(directory, "oversized"), make([]byte, 262145), 0600); err != nil {
			http.Error(w, "fixture write failed", 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /oversize", func(w http.ResponseWriter, _ *http.Request) {
		if err := os.Remove(filepath.Join(directory, "oversized")); err != nil {
			http.Error(w, "fixture remove failed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /download", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(directory, "saved")) })
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5e9, ReadTimeout: 10e9, WriteTimeout: 10e9, IdleTimeout: 10e9}
	log.Fatal(server.ListenAndServe())
}
