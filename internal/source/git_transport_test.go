package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestControlledGitCheckoutPinsHostnameAndRejectsRedirects(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if !GitCurlResolveAvailable(ctx) {
		t.Skip("installed Git lacks required curloptResolve support")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	command := func(dir string, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, git, args...)
		c.Dir = dir
		c.Env = environment
		return c.CombinedOutput()
	}
	for _, args := range [][]string{{"init", "--quiet", "--bare", filepath.Join(root, "repo.git")}, {"init", "--quiet", "--initial-branch=main", filepath.Join(root, "seed")}} {
		if out, err := command(root, args...); err != nil {
			t.Fatalf("fixture init: %v %s", err, out)
		}
	}
	seed := filepath.Join(root, "seed")
	if err := os.WriteFile(filepath.Join(seed, "hello"), []byte("pinned source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "hello"}, {"commit", "--quiet", "-m", "fixture"}, {"-c", "protocol.file.allow=always", "push", filepath.Join(root, "repo.git"), "HEAD:refs/heads/main"}} {
		if out, err := command(seed, args...); err != nil {
			t.Fatalf("fixture commit: %v %s", err, out)
		}
	}
	environment = append(environment, "GIT_ALLOW_PROTOCOL=http:https")
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		http.Error(w, "must not connect", 500)
	}))
	defer target.Close()
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned checkout", true: "redirect rejected"}[redirect], func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.HasPrefix(r.Host, "no-such-host.invalid:") {
					t.Errorf("original Host lost: %s", r.Host)
				}
				if redirect {
					http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusFound)
					return
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			local, _ := url.Parse(server.URL)
			repository, _ := url.Parse("http://no-such-host.invalid:" + local.Port() + "/repo.git")
			workspace := t.TempDir()
			commit, err := ControlledGitCheckout(ctx, workspace, repository, netip.MustParseAddr("127.0.0.1"), "main", func(_ context.Context, _ string, args []string, dir string) ([]byte, error) {
				if dir == "" {
					dir = root
				}
				out, err := command(dir, args...)
				if err != nil {
					return nil, errors.New("controlled fetch failed")
				}
				return out, nil
			})
			if redirect {
				if err == nil {
					t.Fatal("redirect was accepted")
				}
			} else {
				if err != nil || !validGitCommit(commit) {
					t.Fatalf("pinned fetch: %s %v", commit, err)
				}
				data, err := os.ReadFile(filepath.Join(workspace, "hello"))
				if err != nil || string(data) != "pinned source\n" {
					t.Fatal("checkout did not materialize source")
				}
			}
			if requests.Load() == 0 || redirected.Load() != 0 {
				t.Fatalf("pin/redirect requests=%d redirected=%d", requests.Load(), redirected.Load())
			}
		})
	}
}
