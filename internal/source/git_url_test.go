package source

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHRepositoryURLPreservesGitRemoteCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	capture := filepath.Join(root, "args")
	wrapper := filepath.Join(root, "ssh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NEROCD_SSH_ARGS\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, canonical, command string }{
		{"git@example.com:team/repo.git", "ssh://git@example.com/~/team/repo.git", "git-upload-pack 'team/repo.git'"},
		{"git@example.com:/srv/repo.git", "ssh://git@example.com/srv/repo.git", "git-upload-pack '/srv/repo.git'"},
		{"ssh://git@example.com:2222/srv/repo.git", "ssh://git@example.com:2222/srv/repo.git", "git-upload-pack '/srv/repo.git'"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			canonical, err := NormalizeGitRepositoryURL(tc.input)
			if err != nil || canonical != tc.canonical {
				t.Fatalf("canonical=%q err=%v", canonical, err)
			}
			u, err := url.Parse(canonical)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "git", "ls-remote", TransportURL(u))
			command.Env = append(os.Environ(), "GIT_SSH_COMMAND="+wrapper, "GIT_SSH_VARIANT=ssh", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "NEROCD_SSH_ARGS="+capture)
			_ = command.Run() // The capture transport deliberately refuses a network connection.
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(raw), tc.command+"\n") {
				t.Fatalf("remote argv=%q wantcommand=%q", raw, tc.command)
			}
		})
	}
	for _, raw := range []string{"git@example.com:", "-option@example.com:repo", "git@example.com:~other/repo", "git@example.com:repo?query", "git@example.com:repo\ncommand", "git@example.com:repo\\path", "git:password@example.com:repo", "ssh://git:password@example.com/repo", "ssh://git@example.com:/repo", "ssh://git@example.com/repo%00suffix", "ssh://-option@example.com/repo"} {
		if _, err := NormalizeGitRepositoryURL(raw); err == nil {
			t.Fatalf("accepted ambiguous URL %q", raw)
		}
	}
}
