//go:build linux && nerocd_proxy_fixture

package runnerruntime

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

type privateSSHFixture struct {
	repository, fingerprint, root, serverID, wrongFingerprint string
	keys                                                      []string
	update                                                    func(string, bool)
	commits                                                   []string
}

func privateRuntimeSSH(t *testing.T) *privateSSHFixture {
	t.Helper()
	image := os.Getenv("NEROCD_SOURCE_SSH_IMAGE")
	if image == "" {
		t.Fatal("SSH browser requires existing owned SSH server image")
	}
	root, update := privateRuntimeRepository(t, "dockerfile")
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	address, fingerprint, serverID := sourceCheckoutSSHServer(t, root, image)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	f := &privateSSHFixture{repository: "ssh://git@example.com:" + port + "/repo.git", fingerprint: fingerprint, root: root, serverID: serverID}
	f.commits = append(f.commits, checkoutCommand(t, filepath.Join(root, "repo.git"), "rev-parse", "main"))
	f.update = func(body string, fail bool) {
		update(body, fail)
		f.commits = append(f.commits, checkoutCommand(t, filepath.Join(root, "repo.git"), "rev-parse", "main"))
	}
	for _, name := range []string{"client", "client-B", "client-wrong"} {
		if name != "client" {
			fixtureProcess(t, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(root, name))
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		f.keys = append(f.keys, string(raw))
	}
	f.wrongFingerprint = strings.Fields(fixtureProcess(t, "ssh-keygen", "-lf", filepath.Join(root, "client-wrong.pub")))[1]
	return f
}

func (s *privateSSHFixture) rotate(t *testing.T, f *controllerBuildFixture, secret domain.ManagedSecret, expected int64, key int) {
	t.Helper()
	f.request(t, "POST", "/api/v1/managed-secrets/"+secret.ID+"/versions", f.admin, app.ManagedSecretRotateInput{ExpectedVersion: expected, Value: s.keys[key]}, 201, nil)
	if key == 1 {
		public, err := os.ReadFile(filepath.Join(s.root, "client-B.pub"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(s.root, "client.pub"), public, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *privateSSHFixture) patterns() []string {
	var result []string
	for _, key := range s.keys {
		result = append(result, key)
		for _, line := range strings.Split(key, "\n") {
			if len(line) > 32 && !strings.HasPrefix(line, "-----") {
				result = append(result, line)
			}
		}
	}
	return result
}
