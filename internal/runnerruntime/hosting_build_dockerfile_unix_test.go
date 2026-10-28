//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
)

func storedSourceFixture(t *testing.T) (domain.HostingBuildSnapshot, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	content := "FROM scratch\nLABEL version=one\n"
	s := domain.HostingBuildSnapshot{BuildID: "stored-build", InputID: "sha256:" + strings.Repeat("a", 64), Source: domain.HostingGitSource{Kind: "dockerfile", ID: "source", ResourceID: "resource", ProjectID: "project", Generation: 1, Content: content, ContentHash: domain.HostingDockerfileContentHash(content), Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}, Policy: domain.HostingBuildPolicy{SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 2 << 20, MinFreeBytes: 1, MaxEntries: 4, MaxDepth: 2, PollInterval: time.Millisecond}}}
	return s, filepath.Join(root, s.BuildID)
}

func TestStoredDockerfileMaterializationExactReceiptAndCleanup(t *testing.T) {
	s, path := storedSourceFixture(t)
	accepted := false
	err := materializeStoredDockerfile(t.Context(), s, path, "", func(_ context.Context, r domain.HostingBuildSourceReceipt) error {
		if err := domain.ValidateHostingBuildSourceReceipt(r, s); err != nil {
			return err
		}
		accepted = true
		return nil
	}, func(_ context.Context, p string) error {
		if !accepted {
			t.Fatal("engine before durable source acceptance")
		}
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 1 || entries[0].Name() != "Dockerfile" {
			t.Fatal("unexpected context", entries, err)
		}
		raw, err := os.ReadFile(filepath.Join(p, "Dockerfile"))
		if err != nil || string(raw) != s.Source.Content {
			t.Fatal("source changed", err)
		}
		info, _ := os.Stat(filepath.Join(p, "Dockerfile"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("unsafe source mode")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("source retained", err)
	}
}

func TestStoredDockerfileMaterializationRejectsStaleAndCanceledAuthority(t *testing.T) {
	for _, mode := range []string{"hash", "mixed", "cancel", "accept-denied", "symlink", "existing"} {
		t.Run(mode, func(t *testing.T) {
			s, path := storedSourceFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			switch mode {
			case "hash":
				s.Source.ContentHash = "sha256:" + strings.Repeat("b", 64)
			case "mixed":
				s.Source.RepositoryURL = "https://example.com/repo"
			case "cancel":
				cancel()
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := materializeStoredDockerfile(ctx, s, path, "", func(context.Context, domain.HostingBuildSourceReceipt) error { return errors.New("authority denied") }, func(context.Context, string) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("invalid source/authority reached engine", err)
			}
			if mode != "symlink" && mode != "existing" {
				if _, err = os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed source retained")
				}
			}
		})
	}
}

func TestStoredDockerfileCleanupFailureAndPostAcceptCancellation(t *testing.T) {
	s, path := storedSourceFixture(t)
	root := filepath.Dir(path)
	err := materializeStoredDockerfile(t.Context(), s, path, "", func(context.Context, domain.HostingBuildSourceReceipt) error { return nil }, func(context.Context, string) error { return os.Chmod(root, 0500) })
	if restoreErr := os.Chmod(root, 0700); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	var failure *SourceCheckoutError
	if !errors.As(err, &failure) || failure.Code != "cleanup_unconfirmed" {
		t.Fatal("cleanup failure was not retained", err)
	}
	if cleanupErr := os.RemoveAll(path); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := false
	err = materializeStoredDockerfile(ctx, s, path, "", func(context.Context, domain.HostingBuildSourceReceipt) error { cancel(); return nil }, func(context.Context, string) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled receipt started engine", err)
	}
}

func TestStoredDockerfileExecutorDurableReceiptAndRetainedReplay(t *testing.T) {
	e, client, a, at := buildFixture(t)
	root, err := filepath.EvalSymlinks(e.config.SourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	e.config.SourceRoot = root
	s := &client.build.Snapshot
	s.Source.Kind = "dockerfile"
	s.Source.RepositoryURL = ""
	s.Source.RequestedRef = ""
	s.Source.CredentialKind = ""
	s.Source.Policy = domain.HostingGitSource{}.Policy
	s.Source.Content = "FROM scratch\n"
	s.Source.ContentHash = domain.HostingDockerfileContentHash(s.Source.Content)
	s.InputID = ""
	s.InputID = domain.HostingDocumentHash(*s)
	e.checkout = materializeStoredDockerfile
	client.beforeSource = func() {
		found := false
		for _, entry := range e.journal.Snapshot().Build {
			found = found || entry.Source != nil && entry.Source.Receipt.Kind == "dockerfile"
		}
		if !found {
			t.Error("source send preceded durable receipt")
		}
	}
	builds := 0
	e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
		builds++
		return fixtureBuildResult(r), nil
	})
	client.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e.Execute(ctx, a, at) == nil {
		t.Fatal("response loss was not exercised")
	}
	if builds != 1 || client.build.SourceReceipt == nil || client.build.SourceReceipt.Kind != "dockerfile" {
		t.Fatal("stored build did not complete")
	}
	e.checkout = func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
		t.Fatal("retained result rematerialized source")
		return nil
	}
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		t.Fatal("retained replay called engine/material seam")
		return hostingbuild.Result{}, nil
	})
	if err = e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("exact retained receipt not settled")
	}
}
