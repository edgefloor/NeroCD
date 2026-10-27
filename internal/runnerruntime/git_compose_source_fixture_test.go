//go:build unix && nerocd_proxy_fixture

package runnerruntime

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/hostingfixture"
	"nerocd/internal/source"
)

func TestGitComposeSharedPrivateHTTPSCheckout(t *testing.T) {
	url, update, _ := hostingfixture.GitHTTPS(t, map[string]string{"project/deploy/compose.yaml": "services:\n  app:\n    build: ../app\n", "project/app/Dockerfile": "FROM scratch\n", "project/app/marker": "A\n"}, "fixture-user", "fixture-password")
	url = strings.Replace(url, "127.0.0.1", "example.com", 1)
	commit := update(nil)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("NEROCD_GIT_COMPOSE_SOURCE_DNS") != "actual" {
		old := lookupRepositoryIP
		lookupRepositoryIP = func(_ context.Context, network, host string) ([]netip.Addr, error) {
			if network != "ip" || host != "example.com" {
				t.Fatal("source hostname changed")
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		t.Cleanup(func() { lookupRepositoryIP = old })
	}
	request := SourceCheckoutRequest{BuildID: "compose-source-probe", SourceID: "git-source-fixture", InputID: "immutable-input", RepositoryURL: url, RequestedRef: "main", FrozenCommit: commit, CheckoutPath: filepath.Join(parent, "checkout"), Limits: checkoutLimits(), Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}, AllowedCIDRs: []string{"127.0.0.0/8"}, CredentialReferenceID: "source-grant-fixture"}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	accepted, used := false, false
	secret := []byte("fixture-password")
	receipt, err := WithSourceCheckout(ctx, request, func(context.Context) (SourceCredential, error) {
		return SourceCredential{Kind: "https_basic", Username: "fixture-user", Bytes: secret, UseUntil: time.Now().Add(time.Minute)}, nil
	}, func(_ context.Context, r SourceReceipt) error {
		if r.Commit != commit || r.TreeDigest == "" || r.PolicyHash == "" {
			t.Fatal("source receipt changed")
		}
		accepted = true
		return nil
	}, func(_ context.Context, root string, r SourceReceipt) error {
		used = true
		if !accepted || r.Commit != commit {
			t.Fatal("checkout used before exact receipt")
		}
		body, err := os.ReadFile(filepath.Join(root, "project/app/marker"))
		if err != nil || string(body) != "A\n" {
			t.Fatal("actual checkout content missing")
		}
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 1 {
			t.Fatal("transport credentials survived into source use")
		}
		return nil
	})
	if err != nil || !used || receipt.Commit != commit {
		t.Fatal("shared private HTTPS source probe failed", err)
	}
	if _, err = os.Stat(request.CheckoutPath); !os.IsNotExist(err) {
		t.Fatal("source checkout cleanup incomplete")
	}
	for _, b := range secret {
		if b != 0 {
			t.Fatal("source credential ownership was not cleared")
		}
	}
	t.Log("shared private HTTPS source: exact hostname/CA/basic auth, immutable commit/tree, credential cleanup and checkout cleanup passed")
}
