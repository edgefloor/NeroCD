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
	"nerocd/internal/runner"
)

const composeBuildSource = `services:
  web:
    build:
      context: ../app
      dockerfile: Dockerfile
      target: runtime
    image: example/web:local
    volumes: [uploads:/uploads]
  worker:
    image: example/web:local
    command: [worker]
  jobs:
    build: .
  database:
    image: postgres:17
volumes:
  uploads:
`

func composeBuildFixture(t *testing.T) (*BuildExecutor, *buildFixtureClient, domain.HostingBuildAuthority, runner.AttemptIdentity) {
	t.Helper()
	e, client, a, at := buildFixture(t)
	e.config.Compose = true
	e.config.Policy.GitCompose = true
	settings := domain.HostingBuildSettings{Strategy: "compose", ContextDir: "projects/site", ComposeFile: "deploy/compose.yaml", PublicComponent: "web", ContainerPort: 8080}
	client.build.Snapshot.Source.Settings = settings
	client.build.Snapshot.Policy = e.config.Policy
	client.build.Snapshot.InputID = ""
	client.build.Snapshot.InputID = domain.HostingDocumentHash(client.build.Snapshot)
	checkout := e.checkout
	e.checkout = func(ctx context.Context, s domain.HostingBuildSnapshot, p, frozen string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
		return checkout(ctx, s, p, frozen, accept, func(ctx context.Context, p string) error {
			name := filepath.Join(p, settings.ContextDir, settings.ComposeFile)
			if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(name, []byte(composeBuildSource), 0o600); err != nil {
				return err
			}
			return use(ctx, p)
		})
	}
	return e, client, a, at
}

func TestGitComposeBuildExecutorMaterializesMixedSource(t *testing.T) {
	e, client, a, at := composeBuildFixture(t)
	var requests []hostingbuild.Request
	e.engine = buildEngineFunc(func(ctx context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
		requests = append(requests, r)
		return fixtureBuildResult(r), nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := e.Execute(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	result := client.build.ComposeResult
	if client.build.State != "succeeded" || client.build.Artifact != nil || result == nil || len(result.Services) != 2 || len(requests) != 2 || domain.ValidateHostingBuildComposeResult(*result, client.build) != nil {
		t.Fatal("mixed Compose result missing or invalid")
	}
	for i, owner := range []string{"jobs", "web"} {
		request := requests[i]
		if request.BuildID != domain.HostingComposeBuildID(a.BuildID, owner) || request.SourceID != client.build.Snapshot.Source.ID || request.InputID != client.build.Snapshot.InputID || request.Strategy != hostingbuild.Dockerfile || request.DaemonID != "daemon" || request.Limits.ExportBytes != e.config.Policy.Limits.ExportBytes/2 {
			t.Fatal("child build lost parent identity or budget")
		}
	}
	if requests[0].ContextDir != "projects/site/deploy" || requests[1].ContextDir != "projects/site/app" || requests[1].BuildTarget != "runtime" || requests[1].DockerfilePath != "Dockerfile" {
		t.Fatal("Compose relative context/Dockerfile/target semantics changed")
	}
	web, _ := domain.HostingComposeBuildArtifact(*result, "web")
	alias, ok := domain.HostingComposeBuildArtifact(*result, "worker")
	if !ok || web != alias {
		t.Fatal("same-image alias did not retain builder artifact")
	}
	if _, ok = domain.HostingComposeBuildArtifact(*result, "database"); ok {
		t.Fatal("registry image became local build")
	}
	if len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("successful build left unresolved journal")
	}
}

func TestGitComposeBuildExecutorFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"source_fetch", "missing_file", "invalid_source", "build_failed", "cleanup_unconfirmed", "runner_disabled", "policy_disabled"} {
		t.Run(mode, func(t *testing.T) {
			e, client, a, at := composeBuildFixture(t)
			calls := 0
			e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				calls++
				if mode == "cleanup_unconfirmed" {
					return hostingbuild.Result{}, hostingbuild.ErrCleanupUnconfirmed
				}
				return hostingbuild.Result{}, errors.New("build compiler failed")
			})
			switch mode {
			case "source_fetch":
				e.checkout = func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
					return sourceError("fetch_failed")
				}
			case "missing_file", "invalid_source":
				checkout := e.checkout
				e.checkout = func(ctx context.Context, s domain.HostingBuildSnapshot, p, frozen string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
					return checkout(ctx, s, p, frozen, accept, func(ctx context.Context, p string) error {
						name := filepath.Join(p, s.Source.Settings.ContextDir, s.Source.Settings.ComposeFile)
						if mode == "missing_file" {
							if err := os.Remove(name); err != nil {
								return err
							}
						} else if err := os.WriteFile(name, []byte("services: [bad]"), 0o600); err != nil {
							return err
						}
						return use(ctx, p)
					})
				}
			case "runner_disabled":
				e.config.Compose = false
			case "policy_disabled":
				e.config.Policy.GitCompose = false
				client.build.Snapshot.Policy = e.config.Policy
				client.build.Snapshot.InputID = ""
				client.build.Snapshot.InputID = domain.HostingDocumentHash(client.build.Snapshot)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := e.Execute(ctx, a, at)
			settled := mode != "cleanup_unconfirmed" && mode != "runner_disabled" && mode != "policy_disabled"
			if errors.Is(err, errSettledBuildFailure) != settled {
				t.Fatalf("failure settlement changed: %s %v", mode, err)
			}
			if settled && (client.build.State != "failed" || client.build.ComposeResult != nil || client.build.Artifact != nil || len(e.journal.Snapshot().Build) != 0) {
				t.Fatal("failed source/build produced deployable result or unresolved completion")
			}
			if mode == "source_fetch" && (client.build.FailureCode != "source_failed" || calls != 0) {
				t.Fatal("fetch failure entered engine or lost failure code")
			}
			if mode == "cleanup_unconfirmed" && len(e.journal.Snapshot().Build) == 0 {
				t.Fatal("unconfirmed cleanup discarded intent")
			}
			if strings.HasSuffix(mode, "disabled") && (calls != 0 || len(e.journal.Snapshot().Build) != 0) {
				t.Fatal("disabled strategy had source or engine effects")
			}
		})
	}
}

func TestReadBuildComposeRejectsLinksCredentialPathsAndBounds(t *testing.T) {
	for _, mode := range []string{"normal", "symlink_file", "symlink_parent", "credential", "oversize", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			settings := domain.HostingBuildSettings{Strategy: "compose", ContextDir: "app", ComposeFile: "compose.yaml", PublicComponent: "web", ContainerPort: 8080}
			if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "app", "compose.yaml")
			if err := os.WriteFile(name, []byte(composeBuildSource), 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink_file":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../secret", name); err != nil {
					t.Fatal(err)
				}
			case "symlink_parent":
				settings.ContextDir = "linked"
				if err := os.Symlink("app", filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
			case "credential":
				settings.ComposeFile = ".env"
			case "oversize":
				if err := os.WriteFile(name, make([]byte, (128<<10)+1), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			_, err := readBuildCompose(ctx, root, settings)
			if (err == nil) != (mode == "normal") {
				t.Fatalf("unsafe file boundary %s: %v", mode, err)
			}
		})
	}
}

func TestGitComposeBuildLostCompletionReplaysWithoutSourceOrEngine(t *testing.T) {
	e, client, a, at := composeBuildFixture(t)
	calls := 0
	e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
		calls++
		return fixtureBuildResult(r), nil
	})
	client.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := e.Execute(ctx, a, at); err == nil || len(e.journal.Snapshot().Build) == 0 {
		t.Fatal("lost Compose completion discarded intent")
	}
	if calls != 2 || client.build.ComposeResult == nil {
		t.Fatal("source did not prepare both owners before completion loss")
	}
	e.checkout = func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
		t.Fatal("replay fetched source")
		return errors.New("forbidden")
	}
	e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		t.Fatal("replay rebuilt component")
		return hostingbuild.Result{}, errors.New("forbidden")
	})
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.journal.Snapshot().Build) != 0 || len(client.completeRequests) != 2 || domain.HostingDocumentHash(client.completeRequests[0]) != domain.HostingDocumentHash(client.completeRequests[1]) {
		t.Fatal("Compose completion replay changed metadata or retained settled intent")
	}
}
