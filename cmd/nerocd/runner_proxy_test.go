package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRunnerProxyConfigurationFailsBeforeRegistrationOrCredentialUse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "must-not-create")
	for _, args := range [][]string{
		{"--capabilities", "proxy_apply"},
		{"--proxy-state-root", root},
		{"--proxy-state-root", root, "--proxy-protocol", "old.v0"},
		{"--proxy-state-root", root, "--proxy-protocol", "managed-traefik.file.v1", "--proxy-docker-binary", "relative", "--proxy-docker-socket", "/var/run/docker.sock"},
	} {
		command := append([]string{"--server", server.URL, "--token", "fixture-registration-token"}, args...)
		if err := runRunner(command); err == nil {
			t.Fatalf("invalid proxy configuration accepted: %v", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid local proxy configuration performed %d authentication or registration requests", requests.Load())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid local configuration created its state root: %v", err)
	}
}
