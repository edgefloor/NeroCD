package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
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

func TestBuildSecretsRedactPrefixedMultilineBeforeJournalAndTransport(t *testing.T) {
	for _, tc := range []struct{ name, value, output, want string }{
		{"encoded whole with overlapping characters", "D\nR", "#8 0.123 RApS\n#8 0.123 D\n#8 0.123 R\n", "#8 0.123 [REDACTED]\n#8 0.123 [REDACTED]\n#8 0.123 [REDACTED]\n"},
		{"overlapping lines and EOF", "abc\nabcdef", "#8 0.123 abc\n#8 0.123 abcdef\n#8 0.123 abc", "#8 0.123 [REDACTED]\n#8 0.123 [REDACTED]\n#8 0.123 [REDACTED]"},
		{"lf", "protected-build-secret\nsecond-line", "#8 0.123 protected-build-secret\n#8 0.123 second-line\n", "#8 0.123 [REDACTED]\n#8 0.123 [REDACTED]\n"},
		{"crlf", "protected-build-secret\r\nsecond-line\r\n", "#8 0.123 protected-build-secret\r\n#8 0.123 second-line\r\n", "#8 0.123 [REDACTED]\r\n#8 0.123 [REDACTED]\r\n"},
		{"single character and empty lines", "x\n\nx\r\ny", "#8 0.123 x\n#8 0.123 \n#8 0.123 x\r\n#8 0.123 y\n", "#8 0.123 [REDACTED]\n#8 0.123 \n#8 0.123 [REDACTED]\r\n#8 0.123 [REDACTED]\n"},
	} {
		for _, chunkSize := range []int{1, 7, 16384} {
			t.Run(fmt.Sprintf("%s/chunk-%d", tc.name, chunkSize), func(t *testing.T) {
				e, f, a, at := buildFixture(t)
				if _, err := e.journal.AppendBuild(runner.JournalBuild{ID: "intent", Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				var durableText strings.Builder
				seenDurable := map[string]bool{}
				f.beforeLogs = func(batch domain.HostingBuildLogBatch) error {
					for _, entry := range e.journal.Snapshot().BuildLogs {
						if !seenDurable[entry.ID] {
							seenDurable[entry.ID] = true
							for _, event := range entry.Batch.Events {
								durableText.WriteString(event.Message)
							}
						}
					}
					persisted, err := json.Marshal(e.journal.Snapshot())
					if err != nil {
						return err
					}
					for _, line := range strings.Split(tc.value, "\n") {
						line = strings.TrimSuffix(line, "\r")
						if line == "" {
							continue
						}
						unsafe := "#8 0.123 " + line
						if strings.Contains(string(persisted), unsafe) {
							t.Error("multiline fragment crossed journal boundary")
						}
						for _, event := range batch.Events {
							if strings.Contains(event.Message, unsafe) {
								t.Error("multiline fragment crossed transport boundary")
							}
						}
					}
					return nil
				}
				adapter, err := newBuildOutputAdapter(e, a, at)
				if err != nil {
					t.Fatal(err)
				}
				if err = adapter.addBuildSecrets(t.Context(), []domain.HostingBuildSecretValue{{Name: "TOKEN", Value: tc.value}}); err != nil {
					t.Fatal(err)
				}
				for offset := 0; offset < len(tc.output); offset += chunkSize {
					if err = adapter.Sink(t.Context(), hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte(tc.output[offset:min(offset+chunkSize, len(tc.output))])}); err != nil {
						t.Fatal(err)
					}
				}
				if err = adapter.Finish(t.Context()); err != nil {
					t.Fatal(err)
				}
				var delivered strings.Builder
				for _, event := range f.logs {
					delivered.WriteString(event.Message)
				}
				if delivered.String() != tc.want || durableText.String() != tc.want {
					t.Fatal("prefixed multiline output was not fully redacted")
				}
			})
		}
	}
}

func TestBuildSecretsMultilinePatternsRemainDeduplicatedAtMaximumInput(t *testing.T) {
	e, _, a, at := buildFixture(t)
	adapter, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	value := strings.Repeat("x\n", 32767) + "y\n"
	values := make([]domain.HostingBuildSecretValue, 64)
	for i := range values {
		values[i] = domain.HostingBuildSecretValue{Name: fmt.Sprintf("SECRET_%d", i), Value: value}
	}
	var retained *runner.Redactor
	for range 2 {
		if err = adapter.addBuildSecrets(t.Context(), values); err != nil {
			t.Fatal(err)
		}
		if retained != nil && adapter.redactor != retained {
			t.Fatal("unchanged materials rebuilt matcher")
		}
		retained = adapter.redactor
		if len(adapter.secrets) != 3 {
			t.Fatalf("maximum multiline input produced %d materials, want3", len(adapter.secrets))
		}
		for _, material := range adapter.secrets {
			if material.Value == value && len(material.Encodings) != 3 || material.Value != value && len(material.Encodings) != 0 {
				t.Fatal("line fragment expansion changed whole-value encodings")
			}
		}
	}
}

// TestBuildSecretsDiverseMaximumScaleProbe is opt-in because it constructs the
// full managed-value bound and its encoded matcher index in one process.
func TestBuildSecretsDiverseMaximumScaleProbe(t *testing.T) {
	if os.Getenv("NEROCD_REDACTION_SCALE_PROBE") != "1" {
		t.Skip("set NEROCD_REDACTION_SCALE_PROBE=1 for maximum diverse matcher allocation probe")
	}
	e, _, a, at := buildFixture(t)
	adapter, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	values := make([]domain.HostingBuildSecretValue, 64)
	sequence := 0
	for i := range values {
		var value strings.Builder
		value.Grow(65536)
		for range 13107 {
			value.WriteByte(alphabet[(sequence>>18)&63])
			value.WriteByte(alphabet[(sequence>>12)&63])
			value.WriteByte(alphabet[(sequence>>6)&63])
			value.WriteByte(alphabet[sequence&63])
			value.WriteByte('\n')
			sequence++
		}
		value.WriteByte('!')
		values[i] = domain.HostingBuildSecretValue{Name: fmt.Sprintf("SECRET_%d", i), Value: value.String()}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	if err = adapter.addBuildSecrets(t.Context(), values); err != nil {
		t.Fatal(err)
	}
	construction := time.Since(started)
	runtime.ReadMemStats(&after)
	input := strings.Repeat("?", 16384)
	started = time.Now()
	got := adapter.redactor.RedactChunk("buildkit/stdout", input)
	for _, event := range adapter.redactor.Flush() {
		got += event.Message
	}
	matching := time.Since(started)
	if got != input || len(adapter.secrets) != 838913 {
		t.Fatal("diverse maximum matcher lost values or changed ordinary output")
	}
	var usage syscall.Rusage
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	peakBytes := usage.Maxrss
	if runtime.GOOS != "darwin" {
		peakBytes *= 1024
	}
	t.Logf("managed_bytes=%d distinct_fragments=%d material_count=%d construction=%s matching_16k=%s allocated_bytes=%d live_heap_bytes=%d peak_rss_bytes=%d", 64*65536, sequence, len(adapter.secrets), construction, matching, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, peakBytes)
}

func TestBuildMaterialsKeepIndexWhenRegistryAndSecretsAreUnchanged(t *testing.T) {
	e, _, a, at := buildFixture(t)
	adapter, err := newBuildOutputAdapter(e, a, at)
	if err != nil {
		t.Fatal(err)
	}
	registry := domain.HostingRegistryCredential{Registry: "registry.example", Username: "build-user", Password: "protected-registry"}
	values := []domain.HostingBuildSecretValue{{Name: "TOKEN", Value: "first-line\nsecond-line"}}
	if err = adapter.addRegistrySecrets(t.Context(), registry); err != nil {
		t.Fatal(err)
	}
	if err = adapter.addBuildSecrets(t.Context(), values); err != nil {
		t.Fatal(err)
	}
	retained := adapter.redactor
	if err = adapter.addRegistrySecrets(t.Context(), registry); err != nil {
		t.Fatal(err)
	}
	if err = adapter.addBuildSecrets(t.Context(), values); err != nil {
		t.Fatal(err)
	}
	if adapter.redactor != retained {
		t.Fatal("unchanged combined material rebuilt matcher index")
	}
}
