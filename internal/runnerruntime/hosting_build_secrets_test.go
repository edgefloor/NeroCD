package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type secretsBuildClient struct {
	*registryBuildClient
	secretCalls int
}

func (c *secretsBuildClient) BuildSecrets(ctx context.Context, runner string, in domain.HostingBuildSecretsRequest, _ runnerclient.SourceCredentialTransport) (*domain.HostingBuildSecretsDelivery, error) {
	c.secretCalls++
	if c.deny || in.Authority != c.authority || c.build.State != "building" || runner != c.build.Snapshot.RunnerID {
		return nil, errors.New("denied")
	}
	return &domain.HostingBuildSecretsDelivery{UseUntil: time.Now().Add(30 * time.Second), Secrets: []domain.HostingBuildSecretValue{{Name: "TOKEN", Value: "protected-build-secret\nsecond-line"}}}, ctx.Err()
}

type secretsBuildEngine struct {
	buildEngineFunc
	run func(context.Context, hostingbuild.Request, hostingbuild.OutputSink, hostingbuild.BuildMaterialProviders) (hostingbuild.Result, error)
}

func (e secretsBuildEngine) BuildWithMaterials(ctx context.Context, r hostingbuild.Request, s hostingbuild.OutputSink, p hostingbuild.BuildMaterialProviders) (hostingbuild.Result, error) {
	return e.run(ctx, r, s, p)
}

func TestBuildSecretsCombinedDeliveryRedactionAndMetadataReplay(t *testing.T) {
	for _, registry := range []bool{false, true} {
		t.Run(map[bool]string{false: "secrets", true: "combined registry and secrets"}[registry], func(t *testing.T) {
			e, fixture, a, at := buildFixture(t)
			fixture.build.Snapshot.Source.Settings.BuildSecrets = []domain.HostingBuildSecretReference{{Name: "TOKEN", Secret: domain.HostingRegistryCredentialRef{SecretID: "secret_token", Version: 1}}}
			if registry {
				fixture.build.Snapshot.Source.Settings.BuildRegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "secret_registry", Version: 1}
			}
			snapshot := fixture.build.Snapshot
			snapshot.InputID = ""
			fixture.build.Snapshot.InputID = domain.HostingDocumentHash(snapshot)
			client := &secretsBuildClient{registryBuildClient: &registryBuildClient{buildFixtureClient: fixture}}
			e.client = client
			fixture.loseComplete = true
			engineCalls := 0
			e.engine = secretsBuildEngine{buildEngineFunc: func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("unprotected build fallback")
				return hostingbuild.Result{}, nil
			}, run: func(ctx context.Context, r hostingbuild.Request, s hostingbuild.OutputSink, p hostingbuild.BuildMaterialProviders) (hostingbuild.Result, error) {
				engineCalls++
				if len(r.BuildSecrets) != 1 || r.BuildSecrets[0].Name != "TOKEN" || r.BuildSecrets[0].Version != 1 {
					t.Fatal("selected secret identity omitted")
				}
				var values []string
				if registry {
					credential, err := p.Registry(ctx)
					if err != nil {
						return hostingbuild.Result{}, err
					}
					defer credential.Close()
					values = append(values, credential.Password)
				} else if p.Registry != nil {
					t.Fatal("unselected registry material requested")
				}
				secrets, err := p.Secrets(ctx)
				if err != nil {
					return hostingbuild.Result{}, err
				}
				defer clear(secrets)
				values = append(values, secrets[0].Value, base64.StdEncoding.EncodeToString([]byte(secrets[0].Value)))
				for _, value := range values {
					for _, chunk := range []string{value[:5], value[5:] + "\n"} {
						if err = s(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte(chunk)}); err != nil {
							return hostingbuild.Result{}, err
						}
					}
				}
				return fixtureBuildResult(r), nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := e.Execute(ctx, a, at); err == nil {
				t.Fatal("lost completion unexpectedly accepted")
			}
			if client.secretCalls != 1 || client.calls != map[bool]int{false: 0, true: 1}[registry] || engineCalls != 1 {
				t.Fatal("fresh material call count", client.secretCalls, client.calls, engineCalls)
			}
			raw, _ := json.Marshal(e.journal.Snapshot())
			if strings.Contains(string(raw), "protected-build-secret") || strings.Contains(string(raw), "registry-protected-value") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("protected-build-secret\nsecond-line"))) {
				t.Fatal("protected material journaled")
			}
			for _, event := range fixture.logs {
				if strings.Contains(event.Message, "protected-build-secret") || strings.Contains(event.Message, "registry-protected-value") {
					t.Fatal("secret delivered in build output")
				}
			}
			client.deny = true
			e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("metadata replay rebuilt")
				return hostingbuild.Result{}, nil
			})
			if err := e.Reconcile(ctx); err != nil {
				t.Fatal("metadata replay", err)
			}
			if client.secretCalls != 1 || client.calls != map[bool]int{false: 0, true: 1}[registry] {
				t.Fatal("metadata replay fetched protected material")
			}
		})
	}
}

func TestBuildSecretsMaximumValueRedaction(t *testing.T) {
	value := strings.Repeat("x", 65535) + "Y"
	redactor := runner.NewRedactor([]runner.SecretMaterial{{Value: value, Encodings: []string{"base64", "base64url", "hex"}}})
	var output strings.Builder
	for offset := 0; offset < len(value); offset += 4096 {
		output.WriteString(redactor.RedactChunk("build/stdout", value[offset:min(offset+4096, len(value))]))
	}
	for _, event := range redactor.Flush() {
		output.WriteString(event.Message)
	}
	if output.String() != runner.RedactionMarker {
		t.Fatal("maximum managed value was not fully redacted")
	}
}
