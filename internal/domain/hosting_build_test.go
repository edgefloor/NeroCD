package domain_test

import (
	"nerocd/internal/domain"
	"nerocd/internal/source"
	"strings"
	"testing"
	"time"
)

func TestHostingBuildSettingsAndSourceAdmission(t *testing.T) {
	base := domain.HostingGitSource{ID: "source", ResourceID: "res", ProjectID: "project", Generation: 1, RepositoryURL: "https://example.com/repo.git", RequestedRef: "main", CredentialKind: "public", Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}}, Settings: domain.HostingBuildSettings{Strategy: "dockerfile", ContextDir: "apps/web", DockerfilePath: "deploy/Dockerfile", BuildTarget: "release"}}
	if err := domain.ValidateHostingGitSource(base); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../escape", "/absolute", "a/../b", "a//b", ".", ".git/config", "credentials.json", ".env.production", "id_ed25519", "a\\b", "a:b", "a/\x00b"} {
		t.Run(p, func(t *testing.T) {
			v := base
			v.Settings.ContextDir = p
			if domain.ValidateHostingGitSource(v) == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, url := range []string{"https://user:secret@example.com/repo", "https://example.com:99999/repo", "https://other.test/repo", "file:///tmp/repo", "https://example.com/repo?token=x"} {
		v := base
		v.RepositoryURL = url
		if domain.ValidateHostingGitSource(v) == nil {
			t.Errorf("unsafe URL accepted: %s", url)
		}
	}
	for _, ref := range []string{"-main", "main:other", "a..b", "main\x01", "main@{1}"} {
		v := base
		v.RequestedRef = ref
		if domain.ValidateHostingGitSource(v) == nil {
			t.Errorf("unsafe ref accepted: %q", ref)
		}
	}
	for _, s := range []domain.HostingBuildSettings{{Strategy: "railpack", BuildTarget: "x"}, {Strategy: "dockerfile", SPAFallback: true}, {Strategy: "static", DockerfilePath: "Dockerfile"}, {Strategy: "dockerfile", BuildTarget: "x --secret"}} {
		if domain.ValidateHostingBuildSettings(s) == nil {
			t.Fatalf("incompatible settings accepted: %+v", s)
		}
	}
	static := domain.HostingBuildSettings{Strategy: "static", ContextDir: "apps/web", PublishDir: "dist", SPAFallback: true}
	if domain.ValidateHostingBuildSettings(static) != nil {
		t.Fatal("prepared static settings denied")
	}
}
func TestHostingBuildReceiptBinding(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	snap := domain.HostingBuildSnapshot{BuildID: "build", InputID: digest, Source: domain.HostingGitSource{ID: "source"}, Policy: domain.HostingBuildPolicy{Platform: "linux/amd64"}}
	r := domain.HostingBuildSourceReceipt{BuildID: "build", SourceID: "source", InputID: digest, Commit: strings.Repeat("a", 40), PolicyHash: domain.HostingDocumentHash(snap.Source.Policy), TreeDigest: digest}
	if err := domain.ValidateHostingBuildSourceReceipt(r, snap); err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.Commit = "main"
	if domain.ValidateHostingBuildSourceReceipt(bad, snap) == nil {
		t.Fatal("moving ref accepted")
	}
	literal := snap
	literal.Source.RequestedRef = strings.Repeat("f", 40)
	if domain.ValidateHostingBuildSourceReceipt(r, literal) == nil {
		t.Fatal("literal requested commit changed")
	}
	b := domain.HostingBuild{ID: "build", DaemonID: "daemon", Snapshot: snap, SourceReceipt: &r}
	a := domain.HostingBuildArtifact{Origin: "local_build", BuildID: b.ID, SourceID: "source", InputID: digest, SourceReceiptHash: domain.HostingDocumentHash(&r), DaemonID: "daemon", Platform: "linux/amd64", ImageID: digest, IndexDigest: digest, ManifestDigest: digest, ConfigDigest: digest, ContextDigest: digest, RequestDigest: digest}
	if err := domain.ValidateHostingBuildArtifact(a, b); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.HostingBuildArtifact){func(a *domain.HostingBuildArtifact) { a.BuildID = "other" }, func(a *domain.HostingBuildArtifact) { a.InputID = "other" }, func(a *domain.HostingBuildArtifact) { a.DaemonID = "other" }, func(a *domain.HostingBuildArtifact) { a.Platform = "linux/arm64" }, func(a *domain.HostingBuildArtifact) { a.SourceReceiptHash = "other" }, func(a *domain.HostingBuildArtifact) { a.ImageID = "sha256:" + strings.Repeat("b", 64) }} {
		v := a
		change(&v)
		if domain.ValidateHostingBuildArtifact(v, b) == nil {
			t.Fatal("unbound artifact accepted")
		}
	}
	p := domain.HostingBuildPolicy{Identity: digest, ToolchainIdentity: digest, Protocol: domain.HostingBuildProtocol, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 10 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Platform: "linux/amd64", Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 64 << 20, Deadline: time.Minute, ContextBytes: 1, ContextFiles: 1, PlanBytes: 1, LogBytes: 1, ExportBytes: 1, UnpackedBytes: 1, UsageBytes: 1, ReserveBytes: 1, PollInterval: time.Second, DiskMode: "monitored"}}
	if err := domain.ValidateHostingBuildPolicy(p); err != nil {
		t.Fatal(err)
	}
	p.Limits.DiskMode = "quota"
	if domain.ValidateHostingBuildPolicy(p) == nil {
		t.Fatal("hard quota admitted")
	}
	if domain.ValidHostingBuildFailure("stderr_secret") || !domain.ValidHostingBuildFailure("cleanup_unconfirmed") {
		t.Fatal("failure vocabulary")
	}
}
