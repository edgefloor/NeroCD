//go:build nerocd_proxy_fixture

// Package hostingfixture contains shared disposable hosting acceptance inputs.
package hostingfixture

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// GitHTTPS serves a mutable repository using Git's real HTTPS smart protocol.
// The returned update publishes a commit and returns its exact object identity.
// Authentication is required when user is nonempty. A private Git wrapper adds
// only the fixture CA; the source transport still enforces its network policy.
func GitHTTPS(t *testing.T, files map[string]string, user, password string) (string, func(map[string]string) string, func(bool)) {
	t.Helper()
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), actual, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture Git command failed: %v", e)
		}
		return strings.TrimSpace(string(out))
	}
	run(work, "init", "-q", "-b", "main")
	write := func(files map[string]string) string {
		t.Helper()
		for name, value := range files {
			if !filepath.IsLocal(name) {
				t.Fatal("unsafe fixture Git path")
			}
			name = filepath.Join(work, name)
			if e := os.MkdirAll(filepath.Dir(name), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(name, []byte(value), 0600); e != nil {
				t.Fatal(e)
			}
		}
		run(work, "add", ".")
		run(work, "commit", "-qm", "fixture update")
		return run(work, "rev-parse", "HEAD")
	}
	write(files)
	run(root, "clone", "--bare", work, "repo.git")
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var unavailable atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		if user != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != user || p != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="private fixture"`)
				w.WriteHeader(401)
				return
			}
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	if user != "" {
		client := server.Client()
		client.Timeout = 5 * time.Second
		for _, mode := range []string{"anonymous", "wrong", "correct"} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/repo.git/info/refs?service=git-upload-pack", nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong" {
				request.SetBasicAuth(user, "incorrect")
			}
			if mode == "correct" {
				request.SetBasicAuth(user, password)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			closeErr := response.Body.Close()
			expected := 401
			if mode == "correct" {
				expected = 200
			}
			if response.StatusCode != expected || readErr != nil || closeErr != nil {
				t.Fatal("fixture private Git smart HTTP contract failed")
			}
		}
	}

	cert := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+quote(actual)+" -c http.sslCAInfo="+quote(cert)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return server.URL + "/repo.git", func(files map[string]string) string {
		if files == nil {
			return run(work, "rev-parse", "HEAD")
		}
		commit := write(files)
		run(work, "push", filepath.Join(root, "repo.git"), "main")
		return commit
	}, unavailable.Store
}
