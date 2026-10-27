package runner

import (
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
[ "$HOME" -ef "$PWD" ] || exit 25
[ "$XDG_CONFIG_HOME" = "$HOME" ] || exit 26
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
	for _, name := range []string{"success", "failure"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var events []string
			_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://github.com/example/" + name + ".git"}}, root, func(e ProcessEvent) { events = append(events, e.Message) })
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
