package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
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

func TestComposePreApplyFailureRollbackTerminalizationIsNonfatal(t *testing.T) {
	claim := domain.ClaimedRun{Lease: domain.RunLease{ID: "lease", Attempt: 7}}
	var transitions [][2]string
	err := composePreApplyFailure(func(expected, target, _, _ string, _ *bool) error {
		transitions = append(transitions, [2]string{expected, target})
		return nil
	}, claim, true, "provenance_resolution_failed", errors.New("resolver failed"))
	if err != nil {
		t.Fatalf("composePreApplyFailure(rollback) error = %v, want nil", err)
	}
	want := [][2]string{
		{domain.DeploymentPreparing, domain.DeploymentApplying},
		{domain.DeploymentApplying, domain.DeploymentVerifying},
		{domain.DeploymentVerifying, domain.DeploymentRollbackFailed},
	}
	if !slices.Equal(transitions, want) {
		t.Errorf("composePreApplyFailure(rollback) transitions = %v, want %v", transitions, want)
	}
}

func TestComposePreApplyFailureRollbackTransitionFailureIsFatal(t *testing.T) {
	transitionErr := errors.New("transition rejected")
	claim := domain.ClaimedRun{Lease: domain.RunLease{ID: "lease", Attempt: 7}}
	err := composePreApplyFailure(func(_, target, _, _ string, _ *bool) error {
		if target == domain.DeploymentRollbackFailed {
			return transitionErr
		}
		return nil
	}, claim, true, "provenance_resolution_failed", errors.New("resolver failed"))
	if !errors.Is(err, transitionErr) {
		t.Errorf("composePreApplyFailure(rollback transition failure) error = %v, want %v", err, transitionErr)
	}
}

func TestOSProvenanceCommandClassifiesCanceledAndDeadlineContexts(t *testing.T) {
	command := osProvenanceCommand{}
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		want       string
	}{
		{name: "canceled", newContext: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}, want: "canceled"},
		{name: "deadline", newContext: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, want: "deadline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.newContext()
			defer cancel()
			_, err := command.Run(ctx, "sh", []string{"-c", "exit 0"}, t.TempDir())
			var execution *provenanceExecutionError
			if !errors.As(err, &execution) {
				t.Fatalf("osProvenanceCommand.Run(%s) error = %v, want provenance execution error", tt.name, err)
			}
			if execution.reason != tt.want {
				t.Errorf("osProvenanceCommand.Run(%s) reason = %q, want %q", tt.name, execution.reason, tt.want)
			}
		})
	}
	if _, err := command.Run(context.Background(), "sh", []string{"-c", "exit 0"}, t.TempDir()); err != nil {
		t.Errorf("osProvenanceCommand.Run(fresh context) error = %v, want nil", err)
	}
}

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

func TestCanonicalComposeControlledExternalHealthNetwork(t *testing.T) {
	const (
		project = "server-owned"
		image   = "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	service := `{"api":{"image":"` + image + `"}}`
	accepted := `{"services":` + service + `,"networks":{"health":{"external":true,"name":"server-owned_health","ipam":{}}}}`

	canonical, images, err := canonicalCompose([]byte(accepted), project)
	if err != nil {
		t.Fatalf("canonicalCompose(%s) error = %v, want nil", accepted, err)
	}
	wantCanonical := `{"networks":{"health":{"external":true,"ipam":{},"name":"server-owned_health"}},"services":{"api":{"image":"` + image + `"}}}`
	if got := string(canonical); got != wantCanonical {
		t.Errorf("canonicalCompose(%s) = %s, want %s", accepted, got, wantCanonical)
	}
	if wantImages := []string{image}; !slices.Equal(images, wantImages) {
		t.Errorf("canonicalCompose(%s) images = %v, want %v", accepted, images, wantImages)
	}
	reordered := `{"networks":{"health":{"ipam":{},"name":"server-owned_health","external":true}},"services":` + service + `}`
	reorderedCanonical, reorderedImages, err := canonicalCompose([]byte(reordered), project)
	if err != nil {
		t.Fatalf("canonicalCompose(%s) error = %v, want nil", reordered, err)
	}
	if got := string(reorderedCanonical); got != string(canonical) {
		t.Errorf("canonicalCompose(%s) = %s, want deterministic result %s", reordered, got, canonical)
	}
	if !slices.Equal(reorderedImages, images) {
		t.Errorf("canonicalCompose(%s) images = %v, want deterministic images %v", reordered, reorderedImages, images)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "wrong_logical_key", raw: `{"services":` + service + `,"networks":{"other":{"external":true,"name":"server-owned_health","ipam":{}}}}`},
		{name: "wrong_name", raw: `{"services":` + service + `,"networks":{"health":{"external":true,"name":"other_health","ipam":{}}}}`},
		{name: "extra_fields", raw: `{"services":` + service + `,"networks":{"health":{"external":true,"name":"server-owned_health","ipam":{},"driver":"bridge"}}}`},
		{name: "second_external_network", raw: `{"services":` + service + `,"networks":{"health":{"external":true,"name":"server-owned_health","ipam":{}},"other":{"external":true,"name":"other","ipam":{}}}}`},
		{name: "external_volume", raw: `{"services":` + service + `,"volumes":{"data":{"external":true}}}`},
		{name: "malformed_descriptor", raw: `{"services":` + service + `,"networks":{"health":{"external":"true","name":"server-owned_health","ipam":{}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, gotErr := canonicalCompose([]byte(tt.raw), project); gotErr == nil {
				t.Errorf("canonicalCompose(%s) error = nil, want rejection", tt.raw)
			}
		})
	}
}

func TestCanonicalComposeFileSecretsAreStableAcrossAttemptsAndRollback(t *testing.T) {
	digest := strings.Repeat("a", 64)
	bindings := []domain.SecretBinding{{Name: "database-password", Provider: domain.ProviderRunnerFile, Reference: "database_password", Target: "file:db_password", Version: "v1", Required: true}}
	first := []byte(`{"services":{"api":{"image":"registry.example/api@sha256:` + digest + `","secrets":["db_password"]}},"secrets":{"db_password":{"file":"/work/.nerocd-compose-secrets-first/secret-001"}}}`)
	recovered := []byte(`{"services":{"api":{"image":"registry.example/api@sha256:` + digest + `","secrets":["db_password"]}},"secrets":{"db_password":{"file":"/work/.nerocd-compose-secrets-recovered/secret-001"}}}`)
	rollback := []byte(`{"services":{"api":{"image":"registry.example/api@sha256:` + digest + `","secrets":["db_password"]}},"secrets":{"db_password":{"file":"/work/.nerocd-compose-secrets-rollback/secret-001"}}}`)
	firstCanonical, _, err := canonicalCompose(first, "server-owned", bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{recovered, rollback} {
		canonical, _, canonicalErr := canonicalCompose(raw, "server-owned", bindings)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		if string(canonical) != string(firstCanonical) {
			t.Fatalf("canonicalCompose attempt hash input = %s, want %s", canonical, firstCanonical)
		}
	}
	if strings.Contains(string(firstCanonical), ".nerocd-compose-secrets-") {
		t.Fatalf("canonicalCompose retained generated secret path: %s", firstCanonical)
	}
}

func TestCanonicalComposeRejectsUnboundOrUnsafeEffectiveSecrets(t *testing.T) {
	digest := strings.Repeat("a", 64)
	bindings := []domain.SecretBinding{{Name: "application", Provider: domain.ProviderRunnerFile, Reference: "application", Target: "file:application", Version: "v1", Required: true}}
	sources := map[string]string{"application": "/runner/secrets/application"}
	service := `{"image":"registry.example/api@sha256:` + digest + `","secrets":["application"]}`
	for name, raw := range map[string]string{
		"unbound_etc_descriptor": `{"services":{"api":` + service + `},"secrets":{"application":{"file":"/runner/secrets/application"},"steal":{"file":"/etc/shadow"}}}`,
		"external_descriptor":    `{"services":{"api":` + service + `},"secrets":{"application":{"external":true}}}`,
		"extra_descriptor_key":   `{"services":{"api":` + service + `},"secrets":{"application":{"file":"/runner/secrets/application","name":"attacker"}}}`,
		"wrong_validated_source": `{"services":{"api":` + service + `},"secrets":{"application":{"file":"/etc/shadow"}}}`,
		"unauthorized_service":   `{"services":{"api":{"image":"registry.example/api@sha256:` + digest + `","secrets":["steal"]}},"secrets":{"application":{"file":"/runner/secrets/application"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := canonicalComposeWithSecretSources([]byte(raw), "server-owned", bindings, sources); err == nil {
				t.Fatalf("canonicalComposeWithSecretSources accepted unsafe effective secret config %s", raw)
			}
		})
	}
}

func TestCanonicalComposeAcceptsAuthorizedMultiServiceSecrets(t *testing.T) {
	digest := strings.Repeat("a", 64)
	bindings := []domain.SecretBinding{{Name: "application", Provider: domain.ProviderRunnerFile, Reference: "application", Target: "file:application", Version: "v1", Required: true}}
	raw := `{"services":{"api":{"image":"registry.example/api@sha256:` + digest + `","secrets":["application"]},"worker":{"image":"registry.example/worker@sha256:` + digest + `","secrets":[{"source":"application","target":"/run/secrets/application"}]}},"secrets":{"application":{"name":"server-owned_application","file":"/runner/secrets/application"}}}`
	canonical, _, err := canonicalComposeWithSecretSources([]byte(raw), "server-owned", bindings, map[string]string{"application": "/runner/secrets/application"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "/runner/secrets/application") || !strings.Contains(string(canonical), "nerocd-secret://application") {
		t.Fatalf("canonicalComposeWithSecretSources did not replace source path safely: %s", canonical)
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
