package runner

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestExecuteCheckoutRejectsUnsafeDestPath(t *testing.T) {
	_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://github.com/example/repo.git"}, DestPath: "../repo"}, t.TempDir(), func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsafe dest_path error")
	}
}

func TestExecuteCheckoutRejectsUnsafeRepositoryURL(t *testing.T) {
	tests := []string{
		t.TempDir(),
		"file:///tmp/repo",
		"https://127.0.0.1/repo.git",
	}
	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: test}, DestPath: "repo"}, t.TempDir(), func(ProcessEvent) {})
			if err == nil {
				t.Fatal("expected unsafe repository url error")
			}
		})
	}
}

func TestExecuteCheckoutIsolatesEnvironmentAndDiscardsDiagnostics(t *testing.T) {
	bin := t.TempDir()
	git := `#!/bin/sh
[ -z "$NEROCD_RUNNER_TOKEN$SSH_AUTH_SOCK$HTTP_PROXY$GIT_CONFIG_COUNT$GIT_CONFIG_PARAMETERS" ] || exit 20
[ "$GIT_CONFIG_NOSYSTEM" = 1 ] || exit 21
[ "$GIT_CONFIG_GLOBAL" = /dev/null ] || exit 22
[ "$GIT_CONFIG_SYSTEM" = /dev/null ] || exit 23
[ "$GIT_TERMINAL_PROMPT" = 0 ] || exit 24
[ -d "$HOME" ] || exit 25
[ "$XDG_CONFIG_HOME" = "$HOME" ] || exit 26
if [ "$1" = rev-parse ]; then
  printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
  exit 0
fi
printf 'credential-canary from Git stdout\n'
printf 'credential-canary from Git stderr\n' >&2
case "$*" in *failure*) exit 1;; esac
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for _, name := range []string{"NEROCD_RUNNER_TOKEN", "SSH_AUTH_SOCK", "HTTP_PROXY", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(name, "ambient-credential-canary")
	}
	for _, name := range []string{"success", "failure", "relative-root"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			inputRoot := root
			if name == "relative-root" {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				inputRoot, err = filepath.Rel(cwd, root)
				if err != nil {
					t.Fatal(err)
				}
			}
			var events []string
			_, err := executeCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://github.com/example/" + name + ".git"}}, inputRoot, func(e ProcessEvent) { events = append(events, e.Message) }, publicCheckoutTransport())
			if (err != nil) != (name == "failure") {
				t.Fatalf("checkout error = %v", err)
			}
			if strings.Contains(strings.Join(events, "\n"), "credential-canary") || err != nil && strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("Git diagnostic leaked")
			}
			left, err := filepath.Glob(filepath.Join(root, "nerocd-checkout-config-*"))
			if err != nil || len(left) != 0 {
				t.Fatalf("checkout configuration remains: %v %v", left, err)
			}
		})
	}
}

func publicCheckoutTransport() checkoutTransport {
	return checkoutTransport{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		available: func(context.Context) bool { return true },
	}
}

func TestCheckoutRejectsEveryBlockedDNSAnswerBeforeGit(t *testing.T) {
	for _, addresses := range [][]string{{"127.0.0.1"}, {"10.1.2.3"}, {"::1"}, {"169.254.169.254"}, {"224.0.0.1"}, {"8.8.8.8", "192.168.1.2"}, {"::ffff:127.0.0.1"}, {}} {
		t.Run(strings.Join(addresses, ","), func(t *testing.T) {
			transport := publicCheckoutTransport()
			transport.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
				var result []netip.Addr
				for _, raw := range addresses {
					result = append(result, netip.MustParseAddr(raw))
				}
				return result, nil
			}
			transport.available = func(context.Context) bool { t.Error("Git started before DNS admission"); return true }
			root := t.TempDir()
			_, err := executeCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://repo.example/repo"}}, root, nil, transport)
			entries, readErr := os.ReadDir(root)
			if err == nil || readErr != nil || len(entries) != 0 {
				t.Fatalf("rejected DNS caused side effects: %v, %v", err, readErr)
			}
		})
	}
}

func TestCheckoutRejectsSymlinkAncestorsAndNeverDeletesExistingDestination(t *testing.T) {
	for _, path := range []string{"previous/link/victim", "previous"} {
		t.Run(path, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(outside, "victim"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "victim", "keep"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "previous"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "previous", "link")); err != nil {
				t.Fatal(err)
			}
			_, err := executeCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://repo.example/repo"}, DestPath: path}, root, nil, publicCheckoutTransport())
			if err == nil {
				t.Fatal("unsafe or reused destination accepted")
			}
			if _, err := os.Stat(filepath.Join(outside, "victim", "keep")); err != nil {
				t.Fatal("external file was removed", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "previous", "link")); err != nil {
				t.Fatal("previous checkout was removed", err)
			}
		})
	}
}

func TestCheckoutRejectsMissingPinSupportAndCanceledContext(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		transport := publicCheckoutTransport()
		transport.available = func(context.Context) bool { return canceled }
		if canceled {
			cancel()
		}
		root := t.TempDir()
		_, err := executeCheckout(ctx, domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://repo.example/repo"}}, root, nil, transport)
		cancel()
		if err == nil || canceled && !errors.Is(err, context.Canceled) {
			t.Fatalf("checkout=%v", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("failed checkout created files")
		}
	}
}

func TestCheckoutPinsOneValidatedLookupWithNoRedirectFallback(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			bin := t.TempDir()
			port := "443"
			if scheme == "http" {
				port = "80"
			}
			script := `#!/bin/sh
case "$*" in
  *fetch*)
    case "$*" in *http.curloptResolve=repo.example.:` + port + `:8.8.8.8*) ;; *) exit 81;; esac
    case "$*" in *http.followRedirects=false*) ;; *) exit 82;; esac
    case "$*" in *http.proxy=*) ;; *) exit 83;; esac
    ;;
  *clone*) exit 84;;
esac
if [ "$1" = rev-parse ]; then printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'; fi
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			calls := 0
			transport := publicCheckoutTransport()
			transport.lookup = func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
				calls++
				if host != "repo.example." {
					t.Errorf("lookup host = %q", host)
				}
				if calls > 1 {
					return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}
			root := t.TempDir()
			path, err := executeCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: scheme + "://repo.example./repo", Ref: "main"}, DestPath: "nested/repo"}, root, nil, transport)
			if err != nil || calls != 1 {
				t.Fatalf("pinned checkout: %v lookups=%d", err, calls)
			}
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				t.Fatal("checkout directory missing")
			}
		})
	}
}
