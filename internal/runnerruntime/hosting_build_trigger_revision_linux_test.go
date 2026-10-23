//go:build linux

package runnerruntime

import (
	"context"
	"nerocd/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildTriggerRevisionFirstTLSCheckout(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "private"}[private], func(t *testing.T) {
			req, commit, move := checkoutTLSFixture(t, private)
			s := domain.HostingBuildSnapshot{BuildID: req.BuildID, InputID: req.InputID, Source: domain.HostingGitSource{ID: req.SourceID, RepositoryURL: req.RepositoryURL, RequestedRef: req.RequestedRef, Policy: req.Policy}, TriggerRevision: &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: commit}}
			s.Policy.SourceLimits = domain.HostingSourceLimits{MaxTreeBytes: req.Limits.MaxTreeBytes, MaxWorkspaceBytes: req.Limits.MaxWorkspaceBytes, MinFreeBytes: req.Limits.MinFreeBytes, MaxEntries: req.Limits.MaxEntries, MaxDepth: req.Limits.MaxDepth, PollInterval: req.Limits.PollInterval}
			var credential func(context.Context) (SourceCredential, error)
			if private {
				s.Source.CredentialKind = "https_basic"
				s.Source.Policy.CredentialReferenceID = "opaque"
				credential = func(context.Context) (SourceCredential, error) {
					return SourceCredential{Kind: "https_basic", Username: "fixture", Bytes: []byte("fixture-password"), UseUntil: time.Now().Add(time.Minute)}, nil
				}
			}
			// Admission has frozen A. Move the configured branch to B before any checkout.
			move("substituted branch B\n")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			used := false
			err := checkoutBuildWithCredential(ctx, s, req.CheckoutPath, "", credential, func(_ context.Context, receipt domain.HostingBuildSourceReceipt) error {
				if receipt.Commit != commit {
					return context.Canceled
				}
				return domain.ValidateHostingBuildSourceReceipt(receipt, s)
			}, func(_ context.Context, path string) error {
				used = true
				b, err := os.ReadFile(filepath.Join(path, "hello.txt"))
				if err != nil {
					return err
				}
				if string(b) != "first\n" {
					t.Fatal("first checkout followed moving branch")
				}
				return nil
			})
			if err != nil || !used {
				t.Fatal("frozen first checkout failed", err)
			}
			if err = checkoutBuildWithCredential(ctx, s, req.CheckoutPath, strings.Repeat("b", 40), credential, func(context.Context, domain.HostingBuildSourceReceipt) error { return nil }, func(context.Context, string) error { t.Fatal("mismatched later checkout used source"); return nil }); err == nil {
				t.Fatal("later frozen mismatch accepted")
			}
			s.TriggerRevision.ExpectedCommit = strings.Repeat("a", 40)
			if err = checkoutBuildWithCredential(ctx, s, req.CheckoutPath, "", credential, func(context.Context, domain.HostingBuildSourceReceipt) error { return nil }, func(context.Context, string) error { t.Fatal("unavailable revision fell back to branch"); return nil }); err == nil {
				t.Fatal("unavailable revision accepted")
			}
		})
	}
}
