package source

import (
	"net"
	"strings"
	"testing"
)

func TestRepositoryPolicyFailsClosedAndAllowsExplicitInternal(t *testing.T) {
	public := RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"git.example.test"}}
	if _, err := public.ValidateURL("https://git.example.test/repo.git", false); err != nil {
		t.Fatal(err)
	}
	if _, err := public.ValidateURL("http://git.example.test/repo.git", false); err == nil {
		t.Fatal("http unexpectedly allowed")
	}
	if err := public.ValidateResolvedHost("git.example.test", []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}); err == nil {
		t.Fatal("loopback unexpectedly allowed")
	}
	internal := RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowedSchemes: []string{"http"}, AllowedHosts: []string{"git.internal"}, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowInternal: true}
	if _, err := internal.ValidateURL("http://git.internal/repo.git", false); err != nil {
		t.Fatal(err)
	}
	if err := internal.ValidateResolvedHost("git.internal", []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPolicyRedirectAndMetadataDenied(t *testing.T) {
	p := RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"origin.example"}, RedirectHosts: []string{"mirror.example"}}
	if _, err := p.ValidateURL("https://mirror.example/repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ValidateURL("https://evil.example/repo", true); err == nil {
		t.Fatal("unexpected redirect admission")
	}
	if err := p.ValidateResolvedHost("origin.example", []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}); err == nil {
		t.Fatal("metadata unexpectedly allowed")
	}
}

func TestRepositoryPolicyRejectsCredentialsEvenWithoutPassword(t *testing.T) {
	p := RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"origin.example"}}
	if _, err := p.ValidateURL("https://token@origin.example/repo", false); err == nil {
		t.Fatal("username-bearing repository URL unexpectedly allowed")
	}
}

func TestRepositoryPolicyAllowsOnlySSHUserAndRequiresHostFingerprint(t *testing.T) {
	p := RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowedSchemes: []string{"ssh"}, AllowedHosts: []string{"git.internal"}, AllowedCIDRs: []string{"127.0.0.0/8"}, AllowInternal: true, SSHHostFingerprints: []string{"SHA256:abcdefghijklmnop"}}
	if _, err := p.ValidateURL("ssh://git@git.internal/repo.git", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ValidateURL("ssh://git:secret@git.internal/repo.git", false); err == nil {
		t.Fatal("SSH URL password unexpectedly allowed")
	}
	p.SSHHostFingerprints = nil
	if err := p.ValidatePolicy(); err == nil {
		t.Fatal("SSH policy without host fingerprints accepted")
	}
}

func TestRepositoryPolicyRejectsNonCanonicalURLComponentsWithoutEchoingInput(t *testing.T) {
	p := RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"origin.example"}}
	for _, raw := range []string{"https://origin.example/repo?token=sentinel", "https://origin.example/repo#sentinel", "https://token@origin.example/repo", "https:opaque-sentinel"} {
		_, err := p.ValidateURL(raw, false)
		if err == nil || strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), raw) {
			t.Fatalf("unsafe URL error disclosure for %q: %v", raw, err)
		}
	}
}
