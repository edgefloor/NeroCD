package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runnerclient"
)

type registryBuildClient struct {
	*buildFixtureClient
	calls int
	deny  bool
}

func (c *registryBuildClient) BuildRegistryCredential(ctx context.Context, runner string, in domain.HostingBuildRegistryCredentialRequest, _ runnerclient.SourceCredentialTransport) (*domain.HostingRegistryCredentialDelivery, error) {
	c.calls++
	if c.deny || in.Authority != c.authority || c.build.State != "building" || runner != c.build.Snapshot.RunnerID {
		return nil, errors.New("denied")
	}
	return &domain.HostingRegistryCredentialDelivery{Credential: domain.HostingRegistryCredential{Registry: "registry.example", Username: "build-user", Password: "registry-protected-value"}, UseUntil: time.Now().Add(30 * time.Second)}, ctx.Err()
}

type registryBuildEngine struct {
	buildEngineFunc
	run func(context.Context, hostingbuild.Request, hostingbuild.OutputSink, hostingbuild.RegistryCredentialProvider) (hostingbuild.Result, error)
}

func (e registryBuildEngine) BuildWithRegistryCredential(c context.Context, r hostingbuild.Request, s hostingbuild.OutputSink, p hostingbuild.RegistryCredentialProvider) (hostingbuild.Result, error) {
	return e.run(c, r, s, p)
}

func TestBuildRegistryFreshDeliveryRedactionAndMetadataReplay(t *testing.T) {
	for _, lose := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost completion"}[lose], func(t *testing.T) {
			e, fixture, a, at := buildFixture(t)
			fixture.build.Snapshot.Source.Settings.BuildRegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "secret_registry", Version: 1}
			snapshot := fixture.build.Snapshot
			snapshot.InputID = ""
			fixture.build.Snapshot.InputID = domain.HostingDocumentHash(snapshot)
			client := &registryBuildClient{buildFixtureClient: fixture}
			e.client = client
			fixture.loseComplete = lose
			engineCalls := 0
			e.engine = registryBuildEngine{buildEngineFunc: func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("uncredentialed fallback")
				return hostingbuild.Result{}, nil
			}, run: func(c context.Context, r hostingbuild.Request, s hostingbuild.OutputSink, p hostingbuild.RegistryCredentialProvider) (hostingbuild.Result, error) {
				engineCalls++
				material, err := p(c)
				if err != nil {
					return hostingbuild.Result{}, err
				}
				defer material.Close()
				if r.BuildRegistry == nil || r.BuildRegistry.SecretID != "secret_registry" {
					t.Fatal("reference omitted from request")
				}
				for _, value := range []string{material.Password, base64.StdEncoding.EncodeToString([]byte(material.Username + ":" + material.Password))} {
					for _, chunk := range []string{value[:5], value[5:] + "\n"} {
						if err = s(c, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte(chunk)}); err != nil {
							return hostingbuild.Result{}, err
						}
					}
				}
				return fixtureBuildResult(r), nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := e.Execute(ctx, a, at)
			if (err != nil) != lose {
				t.Fatal("execution outcome", err)
			}
			if client.calls != 1 || engineCalls != 1 {
				t.Fatal("fresh delivery/build count", client.calls, engineCalls)
			}
			raw, _ := json.Marshal(e.journal.Snapshot())
			if strings.Contains(string(raw), "registry-protected-value") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("build-user:registry-protected-value"))) {
				t.Fatal("secret journaled")
			}
			for _, event := range fixture.logs {
				if strings.Contains(event.Message, "registry-protected-value") {
					t.Fatal("secret delivered in output")
				}
			}
			if lose {
				client.deny = true
				e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
					t.Fatal("metadata replay invoked engine")
					return hostingbuild.Result{}, nil
				})
				if err = e.Reconcile(ctx); err != nil {
					t.Fatal("metadata replay", err)
				}
				if client.calls != 1 {
					t.Fatal("metadata replay reacquired secret")
				}
			}
		})
	}
}

type scrubRegistryEngine struct {
	buildEngineFunc
	state string
}

func (e scrubRegistryEngine) ScrubRegistryCredentials() error {
	return hostingbuild.ScrubRegistryCredentials(e.state)
}

func TestBuildRegistryCleanupFailureBlocksReplayUntilReconciled(t *testing.T) {
	e, client, a, at := buildFixture(t)
	client.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := e.Execute(ctx, a, at); err == nil {
		t.Fatal("lost completion unexpectedly accepted")
	}
	before, _ := json.Marshal(e.journal.Snapshot())
	state := e.config.Engine.StateDir
	blocked := filepath.Join(state, "fixture", "docker", "config.json")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "unexpected"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	e.engine = scrubRegistryEngine{state: state, buildEngineFunc: func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
		t.Fatal("reconciliation rebuilt")
		return hostingbuild.Result{}, nil
	}}
	if err := e.Reconcile(ctx); !errors.Is(err, hostingbuild.ErrCleanupUnconfirmed) {
		t.Fatal("failed config scrub did not hold replay", err)
	}
	after, _ := json.Marshal(e.journal.Snapshot())
	if string(before) != string(after) {
		t.Fatal("cleanup failure changed durable completion")
	}
	// Exact operator removal repairs only the credential-path obstruction.
	if err := os.Remove(filepath.Join(blocked, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "cleanup_unconfirmed.json")
	if err := os.WriteFile(marker, []byte("retained resource hold"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(ctx); err == nil {
		t.Fatal("credential scrub weakened resource cleanup hold")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("resource hold removed", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal("verified cleanup did not allow metadata replay", err)
	}
}
