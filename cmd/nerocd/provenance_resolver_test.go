package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestDeploymentStatusWatcherCancelsOnlyComposeOperation(t *testing.T) {
	lease := domain.RunLease{ID: "lease", RunID: "run", RunnerID: "runner", Attempt: 1, Fence: "fence", ExpiresAt: time.Now().Add(time.Minute)}
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/deployments/status" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		receipt := "cancel-receipt"
		_ = json.NewEncoder(w).Encode(domain.DeploymentPlan{DeploymentID: "dep", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence", Status: domain.DeploymentCancelRequested, CancellationRequestID: &receipt})
	}))
	defer server.Close()
	operation, cancelOperation := context.WithCancel(supervisor.Context())
	watcher := startDeploymentStatusWatcher(supervisor.Context(), supervisor, server.URL, "token", domain.DeploymentPlan{DeploymentID: "dep", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}, cancelOperation)
	defer watcher.Stop()
	select {
	case <-operation.Done():
	case <-time.After(time.Second):
		t.Fatal("watcher did not cancel compose operation")
	}
	if supervisor.Context().Err() != nil {
		t.Fatal("watcher canceled lease authority")
	}
	if watcher.Receipt() != "cancel-receipt" {
		t.Fatalf("receipt=%q", watcher.Receipt())
	}
}

type fakeProvenanceCommand struct {
	calls   [][]string
	compose string
	keyscan string
	envs    [][]string
}

func (f *fakeProvenanceCommand) Run(_ context.Context, n string, a []string, _ string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{n}, a...))
	if n == "git" && len(a) > 0 && a[0] == "rev-parse" {
		return []byte(strings.Repeat("a", 40) + "\n"), nil
	}
	if n == "docker" {
		return []byte(f.compose), nil
	}
	if n == "ssh-keyscan" {
		return []byte(f.keyscan), nil
	}
	return nil, nil
}
func (f *fakeProvenanceCommand) RunEnvironment(ctx context.Context, n string, a []string, d string, env []string) ([]byte, error) {
	f.envs = append(f.envs, append([]string(nil), env...))
	return f.Run(ctx, n, a, d)
}
func TestResolveDeploymentProvenanceDetachedAndDigestPinned(t *testing.T) {
	previous := lookupRepositoryIP
	lookupRepositoryIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	defer func() { lookupRepositoryIP = previous }()
	f := &fakeProvenanceCommand{compose: `{"services":{"api":{"image":"example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`}
	p := domain.DeploymentPlan{RepositoryURL: "https://git.example/repo", RequestedRef: "main", ComposePath: "compose.yaml", ComposeProject: "test", RepositoryPolicy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"git.example"}}}
	v, e := resolveDeploymentProvenance(context.Background(), p, t.TempDir(), f)
	if e != nil {
		t.Fatal(e)
	}
	if v.GitCommit != strings.Repeat("a", 40) || !slices.Equal(v.ImageDigests, []string{"example/api@sha256:" + strings.Repeat("a", 64)}) {
		t.Fatalf("bad result %#v", v)
	}
	joined := ""
	for _, call := range f.calls {
		line := strings.Join(call, " ")
		if strings.Contains(line, " fetch ") {
			joined = line
		}
	}
	if !strings.Contains(joined, "fetch --no-tags --depth=1 origin main") || !strings.Contains(joined, "http.curloptResolve=git.example:443:8.8.8.8") || !strings.Contains(joined, "http.followRedirects=false") {
		t.Fatalf("not controlled safe fetch: %q", joined)
	}
}

func TestGitCurlResolveOptionKeepsHostnameAndPinsValidatedAddress(t *testing.T) {
	u, err := url.Parse("https://git.example.test:8443/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	value, err := gitCurlResolveOption(u, netip.MustParseAddr("2001:db8::10"))
	if err != nil {
		t.Fatal(err)
	}
	if value != "git.example.test:8443:[2001:db8::10]" {
		t.Fatalf("unexpected curl resolve pin %q", value)
	}
}

func TestResolveDeploymentProvenancePinsTheOnlyAdmittedLookup(t *testing.T) {
	previous := lookupRepositoryIP
	lookups := 0
	lookupRepositoryIP = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.4.4")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil // simulated rebind
	}
	defer func() { lookupRepositoryIP = previous }()
	f := &fakeProvenanceCommand{compose: `{"services":{"api":{"image":"example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`}
	p := domain.DeploymentPlan{RepositoryURL: "https://git.example/repo", RequestedRef: "main", ComposePath: "compose.yaml", ComposeProject: "test", RepositoryPolicy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"git.example"}}}
	if _, err := resolveDeploymentProvenance(context.Background(), p, t.TempDir(), f); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 {
		t.Fatalf("fetch could re-resolve repository hostname: %d lookups", lookups)
	}
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), " fetch ") && !strings.Contains(strings.Join(call, " "), "git.example:443:8.8.4.4") {
			t.Fatalf("fetch was not pinned to admitted answer: %q", call)
		}
	}
}
func TestCanonicalComposeRejectsMutationFeatures(t *testing.T) {
	_, _, e := canonicalCompose([]byte(`{"services":{"x":{"image":"x@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","privileged":true}}}`), "server-owned")
	if e == nil || !strings.Contains(e.Error(), "forbidden") {
		t.Fatalf("got %v", e)
	}
	_, _, e = canonicalCompose([]byte(`{"name":"attacker","services":{"x":{"image":"x@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`), "server-owned")
	if e == nil || !strings.Contains(e.Error(), "project name") {
		t.Fatalf("project override got %v", e)
	}
	_, _, e = canonicalCompose([]byte(`{"services":{"x":{"image":"x:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`), "server-owned")
	if e == nil || !strings.Contains(e.Error(), "digest-pinned") {
		t.Fatalf("tagged image got %v", e)
	}
}

func TestCanonicalComposeAcceptsAndExcludesEffectiveServerProjectName(t *testing.T) {
	canonical, _, err := canonicalCompose([]byte(`{"name":"server-owned","services":{"x":{"image":"x@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`), "server-owned")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), `"name"`) {
		t.Fatalf("canonical compose retained generated project name: %s", canonical)
	}
}

func TestSSHProvenanceUsesFencedRunnerFileAndPinnedHostKey(t *testing.T) {
	previous := lookupRepositoryIP
	lookupRepositoryIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	defer func() { lookupRepositoryIP = previous }()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy_key"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-real-key\n-----END OPENSSH PRIVATE KEY-----\n"), 0400); err != nil {
		t.Fatal(err)
	}
	keyBlob := testSSHHostKeyBlob("ssh-ed25519")
	sum := sha256.Sum256(keyBlob)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	f := &fakeProvenanceCommand{keyscan: "127.0.0.1 ssh-ed25519 " + base64.StdEncoding.EncodeToString(keyBlob) + "\n", compose: `{"services":{"api":{"image":"example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`}
	p := domain.DeploymentPlan{RepositoryURL: "ssh://git@git.internal/repo.git", RequestedRef: "main", ComposePath: "compose.yaml", ComposeProject: "test", SecretBindings: []domain.SecretBinding{{Name: "deploy-key", Provider: domain.ProviderRunnerFile, Reference: "deploy_key", Target: "env:UNUSED", Version: "v1"}}, RepositoryPolicy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowedSchemes: []string{"ssh"}, AllowedHosts: []string{"git.internal"}, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowInternal: true, CredentialReferenceID: "deploy_key", SSHHostFingerprints: []string{fingerprint}}}
	if _, err := resolveDeploymentProvenanceWithCredential(context.Background(), p, t.TempDir(), f, root, func(context.Context, domain.SecretBinding) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fetched := false
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), " fetch ") {
			fetched = true
		}
	}
	for _, environment := range f.envs {
		for _, value := range environment {
			if strings.Contains(value, "not-a-real-key") {
				t.Fatal("private key leaked into child environment")
			}
		}
	}
	if !fetched {
		t.Fatal("SSH resolver did not fetch")
	}
}

func testSSHHostKeyBlob(algorithm string) []byte {
	blob := make([]byte, 4+len(algorithm)+5)
	binary.BigEndian.PutUint32(blob[:4], uint32(len(algorithm)))
	copy(blob[4:], algorithm)
	offset := 4 + len(algorithm)
	binary.BigEndian.PutUint32(blob[offset:offset+4], 1)
	blob[offset+4] = 'x'
	return blob
}

func TestSSHHostKeyMismatchFailsBeforeGitFetch(t *testing.T) {
	previous := lookupRepositoryIP
	lookupRepositoryIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	defer func() { lookupRepositoryIP = previous }()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(root, 0700)
	if err := os.WriteFile(filepath.Join(root, "deploy_key"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nx\n-----END OPENSSH PRIVATE KEY-----\n"), 0400); err != nil {
		t.Fatal(err)
	}
	f := &fakeProvenanceCommand{keyscan: "127.0.0.1 ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte("wrong")) + "\n"}
	p := domain.DeploymentPlan{RepositoryURL: "ssh://git@git.internal/repo.git", RequestedRef: "main", ComposePath: "compose.yaml", ComposeProject: "test", SecretBindings: []domain.SecretBinding{{Name: "deploy-key", Provider: domain.ProviderRunnerFile, Reference: "deploy_key", Target: "env:UNUSED", Version: "v1"}}, RepositoryPolicy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowedSchemes: []string{"ssh"}, AllowedHosts: []string{"git.internal"}, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowInternal: true, CredentialReferenceID: "deploy_key", SSHHostFingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}}
	if _, err := resolveDeploymentProvenanceWithCredential(context.Background(), p, t.TempDir(), f, root, func(context.Context, domain.SecretBinding) error { return nil }); err == nil {
		t.Fatal("wrong SSH host key accepted")
	}
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), " fetch ") {
			t.Fatal("git fetch occurred before host-key rejection")
		}
	}
}

func TestPinnedKnownHostsAcceptsECDSAAndRejectsLegacyOrUnknownAlgorithms(t *testing.T) {
	u, err := url.Parse("ssh://git@git.internal/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	key := testSSHHostKeyBlob("ecdsa-sha2-nistp256")
	sum := sha256.Sum256(key)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	f := &fakeProvenanceCommand{keyscan: "127.0.0.1 ecdsa-sha2-nistp256 " + base64.StdEncoding.EncodeToString(key) + "\n"}
	if _, err := pinnedKnownHosts(context.Background(), f, u, netip.MustParseAddr("127.0.0.1"), []string{fingerprint}); err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []string{"ssh-rsa", "ssh-dss", "unknown-key"} {
		f.keyscan = "127.0.0.1 " + algorithm + " " + base64.StdEncoding.EncodeToString(testSSHHostKeyBlob("ssh-ed25519")) + "\n"
		if _, err := pinnedKnownHosts(context.Background(), f, u, netip.MustParseAddr("127.0.0.1"), []string{fingerprint}); err == nil {
			t.Fatalf("legacy/unknown host key algorithm %q accepted", algorithm)
		}
	}
}
