//go:build unix

package runnerruntime

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/source"
)

func checkoutLimits() SourceCheckoutLimits {
	return SourceCheckoutLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 16 << 20, MinFreeBytes: 1, MaxEntries: 1024, MaxDepth: 32, PollInterval: 10 * time.Millisecond}
}
func checkoutCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %s: %v", args[0], err)
	}
	return strings.TrimSpace(string(b))
}
func checkoutTLSFixture(t *testing.T, auth bool, wrap ...func(http.Handler) http.Handler) (SourceCheckoutRequest, string, func(string)) {
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
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	if err = os.WriteFile(filepath.Join(work, "hello.txt"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "add", ".")
	checkoutCommand(t, work, "commit", "-qm", "first")
	commit := checkoutCommand(t, work, "rev-parse", "HEAD")
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth {
			u, p, ok := r.BasicAuth()
			if !ok || u != "fixture" || p != "fixture-password" {
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				w.WriteHeader(401)
				return
			}
		}
		backend.ServeHTTP(w, r)
	})
	for _, middleware := range wrap {
		handler = middleware(handler)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cert := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Fixture-only toolchain wrapper adds the isolated server CA. Production
	// transport has no request option for disabling certificate verification.
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+shellQuote(actual)+" -c http.sslCAInfo="+shellQuote(cert)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	req := SourceCheckoutRequest{BuildID: "build", SourceID: "source", InputID: "input", RepositoryURL: server.URL + "/repo.git", RequestedRef: "main", CheckoutPath: filepath.Join(parent, "checkout"), Limits: checkoutLimits(), Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"127.0.0.1"}, AllowedCIDRs: []string{"127.0.0.0/8"}}}
	return req, commit, func(content string) {
		if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		checkoutCommand(t, work, "add", ".")
		checkoutCommand(t, work, "commit", "-qm", "next")
		checkoutCommand(t, work, "push", filepath.Join(root, "repo.git"), "main")
	}
}

func TestSourceCheckoutHTTPS(t *testing.T) {
	for _, auth := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "authenticated"}[auth], func(t *testing.T) {
			req, first, move := checkoutTLSFixture(t, auth)
			move("second\n")
			req.FrozenCommit = first
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			order := []string{}
			secret := []byte("fixture-password")
			var credential func(context.Context) (SourceCredential, error)
			if auth {
				req.Policy.CredentialReferenceID = "opaque"
				credential = func(context.Context) (SourceCredential, error) {
					order = append(order, "credential")
					return SourceCredential{Kind: "https_basic", Username: "fixture", Bytes: secret, UseUntil: time.Now().Add(time.Minute)}, nil
				}
			}
			receipt, err := WithSourceCheckout(ctx, req, credential, func(_ context.Context, r SourceReceipt) error {
				order = append(order, "receipt")
				if r.Commit != first || r.TreeDigest == "" || r.PolicyHash == "" {
					t.Fatal("unbound receipt")
				}
				return nil
			}, func(_ context.Context, path string, r SourceReceipt) error {
				order = append(order, "use")
				b, err := os.ReadFile(filepath.Join(path, "hello.txt"))
				if err != nil || string(b) != "first\n" {
					t.Fatal("frozen checkout followed moving branch")
				}
				entries, err := os.ReadDir(filepath.Dir(path))
				if err != nil || len(entries) != 1 {
					t.Fatal("transport material remains before use")
				}
				if _, err = os.Stat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
					t.Fatal("git metadata retained")
				}
				for _, v := range secret {
					if auth && v != 0 {
						t.Fatal("credential bytes not cleared")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Commit != first || order[len(order)-2] != "receipt" || order[len(order)-1] != "use" {
				t.Fatal("callback order")
			}
			if _, err = os.Stat(req.CheckoutPath); !os.IsNotExist(err) {
				t.Fatal("checkout retained")
			}
		})
	}
}

func TestSourceCheckoutFailures(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	for _, tc := range []struct {
		name              string
		change            func(*SourceCheckoutRequest)
		acceptErr, useErr bool
		code              string
	}{
		{name: "absent frozen commit", change: func(r *SourceCheckoutRequest) { r.FrozenCommit = strings.Repeat("a", 40) }, code: "fetch_failed"},
		{name: "tree bytes", change: func(r *SourceCheckoutRequest) { r.Limits.MaxTreeBytes = 1 }, code: "tree_limit"},
		{name: "receipt rejection", acceptErr: true, code: "receipt_rejected"},
		{name: "use rejection", useErr: true, code: "use_failed"},
		{name: "reserve admission", change: func(r *SourceCheckoutRequest) { r.Limits.MinFreeBytes = 1 << 62 }, code: "workspace_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			if tc.change != nil {
				tc.change(&r)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			used := false
			_, err := WithSourceCheckout(ctx, r, nil, func(context.Context, SourceReceipt) error {
				if tc.acceptErr {
					return errors.New("private callback detail")
				}
				return nil
			}, func(context.Context, string, SourceReceipt) error {
				used = true
				if tc.useErr {
					return errors.New("private callback detail")
				}
				return nil
			})
			var coded *SourceCheckoutError
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			if used && !tc.useErr {
				t.Fatal("unexpected checkout use")
			}
			if _, err = os.Stat(r.CheckoutPath); !os.IsNotExist(err) {
				t.Fatal("checkout leaked")
			}
		})
	}
}

func TestSourceTreeValidation(t *testing.T) {
	for _, kind := range []string{"symlink", "submodule", "lfs", "depth", "count", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			l := checkoutLimits()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("/etc/passwd", filepath.Join(root, "link"))
			case "submodule":
				err = os.WriteFile(filepath.Join(root, ".gitmodules"), nil, 0600)
			case "lfs":
				err = os.WriteFile(filepath.Join(root, "pointer"), []byte("version https://git-lfs.github.com/spec/v1\n"), 0600)
			case "depth":
				l.MaxDepth = 1
				err = os.MkdirAll(filepath.Join(root, "a", "b"), 0700)
			case "count":
				l.MaxEntries = 1
				err = os.WriteFile(filepath.Join(root, "a"), nil, 0600)
				if err == nil {
					err = os.WriteFile(filepath.Join(root, "b"), nil, 0600)
				}
			case "canceled":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sourceTreeDigest(ctx, root, l); err == nil {
				t.Fatal("unsupported tree admitted")
			}
		})
	}
}

func TestSourceCredentialFailureAndExpiry(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	for _, kind := range []string{"denied", "expired", "callback-cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			secret := []byte("do-not-log")
			called := false
			_, err := WithSourceCheckout(ctx, req, func(context.Context) (SourceCredential, error) {
				if kind == "callback-cancel" {
					cancel()
				}
				c := SourceCredential{Kind: "https_basic", Username: "fixture", Bytes: secret, UseUntil: time.Now().Add(-time.Second)}
				if kind == "denied" {
					return c, errors.New("do-not-log")
				}
				return c, nil
			}, func(context.Context, SourceReceipt) error { called = true; return nil }, func(context.Context, string, SourceReceipt) error { called = true; return nil })
			if err == nil || called || strings.Contains(err.Error(), "do-not-log") {
				t.Fatalf("unexpected credential failure: %v", err)
			}
			for _, b := range secret {
				if b != 0 {
					t.Fatal("credential bytes retained")
				}
			}
		})
	}
}

type cancelScanContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelScanContext) Err() error {
	c.checks++
	if c.checks == 5 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestSourceScanCancellationDuringTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelScanContext{Context: base, cancel: cancel}
	entries, _, err := scanSource(ctx, root, checkoutLimits(), false)
	if !errors.Is(err, context.Canceled) || len(entries) == 0 || len(entries) >= 4 {
		t.Fatalf("did not cancel during scan: entries=%d err=%v", len(entries), err)
	}
}

func TestSourceCheckoutRejectsExistingAndUnsafePathsBeforeCredentials(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	if err := os.Mkdir(req.CheckoutPath, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(req.CheckoutPath, "existing")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := false
	_, err := WithSourceCheckout(ctx, req, func(context.Context) (SourceCredential, error) { called = true; return SourceCredential{}, nil }, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err == nil || called {
		t.Fatal("existing checkout reused")
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "preserve" {
		t.Fatal("existing checkout changed")
	}
	req.CheckoutPath = filepath.Join(filepath.Dir(req.CheckoutPath), "link", "checkout")
	if err = os.Symlink(t.TempDir(), filepath.Dir(req.CheckoutPath)); err != nil {
		t.Fatal(err)
	}
	_, err = WithSourceCheckout(ctx, req, nil, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err == nil {
		t.Fatal("symlink parent admitted")
	}
}

func TestSourceCheckoutRedirectNeverFollows(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer target.Close()
	req, _, _ := checkoutTLSFixture(t, false, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := WithSourceCheckout(ctx, req, nil, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err == nil || hit {
		t.Fatal("redirect followed")
	}
}

func TestSourceCheckoutIgnoresAmbientHelpersAndHooks(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	if err := os.WriteFile(config, []byte("[core]\n hooksPath = "+hooks+"\n[credential]\n helper = !touch "+marker+"\n[url \"file:///nonexistent/\"]\n insteadOf = https://\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", hooks)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := WithSourceCheckout(ctx, req, nil, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ambient helper or hook ran")
	}
}

func TestSourceCheckoutCancellationDuringFetch(t *testing.T) {
	started := make(chan struct{})
	req, _, _ := checkoutTLSFixture(t, false, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-r.Context().Done()
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := WithSourceCheckout(ctx, req, nil, func(context.Context, SourceReceipt) error { return errors.New("must not accept") }, func(context.Context, string, SourceReceipt) error { return errors.New("must not use") })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled fetch succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fetch cancellation did not join")
	}
	if _, err := os.Stat(req.CheckoutPath); !os.IsNotExist(err) {
		t.Fatal("canceled checkout retained")
	}
	entries, err := os.ReadDir(filepath.Dir(req.CheckoutPath))
	if err != nil || len(entries) != 0 {
		t.Fatal("canceled transport material retained")
	}
}

func TestSourceWorkspaceAndCommandOutputBounds(t *testing.T) {
	root := t.TempDir()
	material := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bytes"), []byte("bounded-work"), 0600); err != nil {
		t.Fatal(err)
	}
	l := checkoutLimits()
	l.MaxWorkspaceBytes = 1
	if err := checkSourceWorkspace(context.Background(), root, material, l); err == nil {
		t.Fatal("workspace byte limit ignored")
	}
	var output boundedCommandOutput
	if _, err := output.Write(make([]byte, provenanceCommandOutputLimit+1)); err == nil || output.Len() != 0 {
		t.Fatal("command output unbounded")
	}
}

func TestSourceCheckoutRequiresDeadlineAndSingleRef(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	_, err := WithSourceCheckout(context.Background(), req, nil, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	var coded *SourceCheckoutError
	if !errors.As(err, &coded) || coded.Code != "deadline_required" {
		t.Fatal("missing deadline admitted")
	}
	for _, ref := range []string{"main:refs/replace/other", "refs/heads/*", "main^{commit}", "-option"} {
		r := req
		r.RequestedRef = ref
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = validateSourceCheckout(ctx, r, true)
		cancel()
		if err == nil {
			t.Fatalf("unsafe ref accepted: %s", ref)
		}
	}
}

func TestSourceCredentialLeaseStopsActiveFetch(t *testing.T) {
	started := make(chan struct{})
	req, _, _ := checkoutTLSFixture(t, false, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-r.Context().Done()
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	called := false
	go func() {
		_, err := WithSourceCheckout(ctx, req, func(context.Context) (SourceCredential, error) {
			return SourceCredential{Kind: "https_basic", Username: "fixture", Bytes: []byte("fixture"), UseUntil: time.Now().Add(500 * time.Millisecond)}, nil
		}, func(context.Context, SourceReceipt) error { called = true; return nil }, func(context.Context, string, SourceReceipt) error { called = true; return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch never started")
	}
	select {
	case err := <-done:
		if err == nil || called {
			t.Fatal("expired fetch exposed source")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("credential expiry did not stop fetch")
	}
	if _, err := os.Stat(req.CheckoutPath); !os.IsNotExist(err) {
		t.Fatal("expired checkout retained")
	}
}
