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
	mux.HandleFunc("GET /download", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(directory, "saved")) })
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5e9, ReadTimeout: 10e9, WriteTimeout: 10e9, IdleTimeout: 10e9}
	log.Fatal(server.ListenAndServe())
}
