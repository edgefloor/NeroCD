package runnerruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type privateFixtureClient struct {
	*buildFixtureClient
	requests []domain.HostingSourceCredentialRequest
	deliver  func(context.Context, domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error)
}

func (f *privateFixtureClient) BuildSourceCredential(ctx context.Context, _ string, in domain.HostingSourceCredentialRequest, _ runnerclient.SourceCredentialTransport) (hostingbuildprotocol.SourceCredential, error) {
	f.requests = append(f.requests, in)
	return f.deliver(ctx, in)
}
func privateBuildFixture(t *testing.T) (*BuildExecutor, *privateFixtureClient, domain.HostingBuildAuthority, runner.AttemptIdentity) {
	t.Helper()
	e, base, a, at := buildFixture(t)
	e.config.PrivateGit = &PrivateGitConfig{}
	s := &base.build.Snapshot
	s.Source.CredentialKind = "https_basic"
	s.Grant = &domain.HostingSourceGrant{ID: "grant", Version: 1, SourceID: s.Source.ID, ResourceID: s.Source.ResourceID, ProjectID: s.Source.ProjectID, RepositoryURL: s.Source.RepositoryURL, PolicyHash: domain.HostingDocumentHash(s.Source.Policy), SecretID: "secret", SecretVersion: 1, Kind: "https_basic", CreatedBy: "owner"}
	s.InputID = ""
	s.InputID = domain.HostingDocumentHash(*s)
	f := &privateFixtureClient{buildFixtureClient: base}
	f.deliver = func(_ context.Context, in domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
		return hostingbuildprotocol.SourceCredential{Authority: a, DeliveryID: in.DeliveryID, RequestHash: hostingbuildprotocol.SourceCredentialRequestHash("runner", in), Kind: "https_basic", Username: "private-user", Bytes: []byte("private-password"), UseUntil: time.Now().Add(30 * time.Second)}, nil
	}
	e.client = f
	e.privateCheckout = func(ctx context.Context, s domain.HostingBuildSnapshot, p, frozen string, credential func(context.Context) (SourceCredential, error), accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
		cred, err := credential(ctx)
		if err != nil {
			return err
		}
		defer clear(cred.Bytes)
		if cred.Kind != "https_basic" {
			t.Fatal("private kind lost")
		}
		// The existing source adapter owns transport destruction before use.
		clear(cred.Bytes)
		return e.checkout(ctx, s, p, frozen, accept, use)
	}
	return e, f, a, at
}
func TestPrivateBuildDeliveryLossUsesDurableIdentityAndNoRestartRefetch(t *testing.T) {
	e, f, a, at := privateBuildFixture(t)
	deliver := f.deliver
	f.deliver = func(ctx context.Context, in domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
		entries := e.journal.Snapshot().Build
		if len(entries) != 1 || entries[0].ID != "build_intent_"+in.DeliveryID || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
			t.Fatal("delivery before exact durable intent")
		}
		if len(f.requests) == 1 {
			return hostingbuildprotocol.SourceCredential{}, errors.New("response lost")
		}
		return deliver(ctx, in)
	}
	f.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := e.Execute(ctx, a, at); err == nil {
		t.Fatal("lost completion accepted")
	}
	if len(f.requests) != 2 || f.requests[0] != f.requests[1] {
		t.Fatal("delivery retry changed identity")
	}
	f.deliver = func(context.Context, domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
		t.Fatal("startup refetched credential")
		return hostingbuildprotocol.SourceCredential{}, nil
	}
	e.privateCheckout = func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context) (SourceCredential, error), func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
		t.Fatal("startup recreated source")
		return nil
	}
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("exact completion not acknowledged")
	}
}
func TestPrivateBuildCredentialDenialsRetainIntentWithoutEngine(t *testing.T) {
	for _, kind := range []string{"unavailable", "expired", "wrong-authority", "wrong-window", "canceled", "lost-authority"} {
		t.Run(kind, func(t *testing.T) {
			e, f, a, at := privateBuildFixture(t)
			deliver := f.deliver
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			f.deliver = func(c context.Context, in domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
				material, _ := deliver(c, in)
				switch kind {
				case "unavailable":
					material.Close()
					return material, errors.New("unavailable")
				case "expired":
					material.UseUntil = time.Now().Add(-time.Second)
				case "wrong-authority":
					material.Authority.Fence = "other"
				case "wrong-window":
					material.UseUntil = time.Now().Add(2 * time.Minute)
				case "canceled":
					cancel()
				case "lost-authority":
					f.mu.Lock()
					f.live = false
					f.mu.Unlock()
					material.Close()
					return material, errors.New("response lost")
				}
				return material, nil
			}
			e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("denied credential reached engine")
				return hostingbuild.Result{}, nil
			})
			if err := e.Execute(ctx, a, at); err == nil {
				t.Fatal("denied private build succeeded")
			}
			if len(f.requests) > 3 || len(e.journal.Snapshot().Build) != 1 || len(f.completeRequests) != 0 {
				t.Fatal("denial discarded evidence or completed")
			}
			n := len(f.requests)
			if err := e.Reconcile(ctx); err == nil {
				t.Fatal("unresolved intent allowed restart")
			}
			if len(f.requests) != n {
				t.Fatal("restart refetched")
			}
		})
	}
}
func TestPrivateBuildSnapshotRequiresExactGrantAndOptIn(t *testing.T) {
	for _, mutate := range []func(*BuildExecutor, *domain.HostingBuildSnapshot){
		func(e *BuildExecutor, _ *domain.HostingBuildSnapshot) { e.config.PrivateGit = nil },
		func(_ *BuildExecutor, s *domain.HostingBuildSnapshot) { s.Grant.Revoked = true },
		func(_ *BuildExecutor, s *domain.HostingBuildSnapshot) { s.Grant.SourceID = "wrong" },
		func(_ *BuildExecutor, s *domain.HostingBuildSnapshot) { s.Grant.PolicyHash = "wrong" },
		func(_ *BuildExecutor, s *domain.HostingBuildSnapshot) {
			s.Source.Policy.CredentialReferenceID = "not-authority"
		},
	} {
		e, f, a, at := privateBuildFixture(t)
		mutate(e, &f.build.Snapshot)
		f.build.Snapshot.InputID = ""
		f.build.Snapshot.InputID = domain.HostingDocumentHash(f.build.Snapshot)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		if err := e.Execute(ctx, a, at); err == nil {
			t.Fatal("invalid grant executed")
		}
		cancel()
		if len(f.requests) != 0 || e.journal.Depth() != 0 {
			t.Fatal("snapshot denial created effects")
		}
	}
}
func TestPrivateBuildOutputRedactsSplitPatternsBeforeJournalAndTransport(t *testing.T) {
	password := "private-password"
	basic := "private-user:" + password
	for _, pattern := range []string{password, basic, base64.StdEncoding.EncodeToString([]byte(basic)), base64.RawURLEncoding.EncodeToString([]byte(password)), hex.EncodeToString([]byte(password))} {
		for split := 0; split <= len(pattern); split++ {
			e, f, a, at := privateBuildFixture(t)
			if err := e.setBuildLogSecrets("runtime-token"); err != nil {
				t.Fatal(err)
			}
			f.beforeLogs = func(batch domain.HostingBuildLogBatch) error {
				raw, _ := json.Marshal(batch)
				if strings.Contains(string(raw), password) || strings.Contains(string(raw), pattern) {
					t.Fatal("private material reached journal/transport")
				}
				return nil
			}
			e.engine = observedBuildEngine{buildEngineFunc: e.engine.(buildEngineFunc), output: func(ctx context.Context, sink hostingbuild.OutputSink) error {
				for _, chunk := range []string{pattern[:split], pattern[split:] + " runtime-token"} {
					if err := sink(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte(chunk)}); err != nil {
						return err
					}
				}
				return nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			err := e.Execute(ctx, a, at)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(f.logs)
			if strings.Contains(string(raw), password) || strings.Contains(string(raw), pattern) || strings.Contains(string(raw), "runtime-token") || !strings.Contains(string(raw), runner.RedactionMarker) {
				t.Fatal("private output not redacted")
			}
			if len(e.logSecrets) != 1 || e.logSecrets[0].Value != "runtime-token" {
				t.Fatal("attempt mutated reusable secrets")
			}
		}
	}
}

func TestPrivateBuildRejectsNoncanonicalDurableDeliverySuffix(t *testing.T) {
	for _, id := range []string{"build_intent_" + strings.Repeat("a", 64), "build_intent_" + strings.Repeat("A", 32), "build_intent_short", "other_" + strings.Repeat("a", 32)} {
		e, f, a, at := privateBuildFixture(t)
		f.build.State = "sourcing"
		if _, err := e.journal.AppendBuild(runner.JournalBuild{ID: id, Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(f.build.Snapshot), CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := e.privateBuildCredential(ctx, a, f.build.Snapshot, id)
		cancel()
		if err == nil || len(f.requests) != 0 || len(e.journal.Snapshot().Build) != 1 {
			t.Fatal("noncanonical delivery identity consumed/transported")
		}
	}
}

func TestPrivateRuntimeTransportPreflightBeforeJournalOrNetwork(t *testing.T) {
	for _, server := range []string{"http://controller.example", "http://localhost:8080", "https://user:password@controller.example"} {
		journal := filepath.Join(t.TempDir(), "not-created")
		err := RunContext(t.Context(), Config{Execute: true, Server: server, RunnerID: "runner", JournalDir: journal, Build: &BuildConfig{Target: BuildTarget{RunnerID: "runner"}, PrivateGit: &PrivateGitConfig{}}})
		if !errors.Is(err, runnerclient.ErrSourceCredential) {
			t.Fatal("transport preflight did not run before local prerequisites", err)
		}
		if _, err := os.Stat(journal); !os.IsNotExist(err) {
			t.Fatal("invalid private transport created journal")
		}
	}
}

func TestPrivateBuildTriggerRevisionPinsFirstCheckout(t *testing.T) {
	e, f, a, at := privateBuildFixture(t)
	expected := strings.Repeat("a", 40)
	f.build.Snapshot.TriggerRevision = &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: expected}
	f.build.Snapshot.InputID = ""
	f.build.Snapshot.InputID = domain.HostingDocumentHash(f.build.Snapshot)
	checkout := e.privateCheckout
	e.privateCheckout = func(c context.Context, s domain.HostingBuildSnapshot, p, frozen string, credential func(context.Context) (SourceCredential, error), accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
		if frozen != expected {
			t.Fatal("private first checkout did not receive trigger commit")
		}
		return checkout(c, s, p, frozen, credential, accept, use)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := e.Execute(ctx, a, at); err != nil {
		t.Fatal(err)
	}
}

func TestSSHBuildReplayNeverRefetchesKeyAndRedactsLines(t *testing.T) {
	e, f, a, at := privateBuildFixture(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	raw = append(raw, bytes.Repeat([]byte(" "), 65536-len(raw))...)
	defer clear(raw)
	s := &f.build.Snapshot
	s.Source.RepositoryURL = "ssh://git@git.example.test/~/team/repo.git"
	s.Source.CredentialKind = "ssh_private_key"
	s.Source.Policy.AllowedSchemes = []string{"ssh"}
	s.Source.Policy.SSHHostFingerprints = []string{"SHA256:" + strings.Repeat("A", 43)}
	s.Grant.Kind = "ssh_private_key"
	s.Grant.RepositoryURL = s.Source.RepositoryURL
	s.Grant.PolicyHash = domain.HostingDocumentHash(s.Source.Policy)
	s.InputID = ""
	s.InputID = domain.HostingDocumentHash(*s)
	f.deliver = func(_ context.Context, in domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
		return hostingbuildprotocol.SourceCredential{Authority: a, DeliveryID: in.DeliveryID, RequestHash: hostingbuildprotocol.SourceCredentialRequestHash("runner", in), Kind: "ssh_private_key", Bytes: bytes.Clone(raw), UseUntil: time.Now().Add(30 * time.Second)}, nil
	}
	e.privateCheckout = func(ctx context.Context, s domain.HostingBuildSnapshot, p, frozen string, credential func(context.Context) (SourceCredential, error), accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
		material, err := credential(ctx)
		if err != nil {
			return err
		}
		if material.Kind != "ssh_private_key" || len(material.Bytes) != 65536 {
			t.Fatal("key transfer changed")
		}
		clear(material.Bytes)
		return e.checkout(ctx, s, p, frozen, accept, use)
	}
	lines := strings.Split(string(pem.EncodeToMemory(block)), "\n")
	secretLine := lines[1]
	original := e.engine
	e.engine = observedBuildEngine{buildEngineFunc: original.Build, output: func(ctx context.Context, sink hostingbuild.OutputSink) error {
		return sink(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuildKit, Stream: hostingbuild.OutputStdout, Data: []byte("key line " + secretLine + "\n")})
	}}
	f.beforeLogs = func(batch domain.HostingBuildLogBatch) error {
		journal, _ := json.Marshal(e.journal.Snapshot())
		transport, _ := json.Marshal(batch)
		if bytes.Contains(journal, []byte(secretLine)) || bytes.Contains(transport, []byte(secretLine)) {
			t.Error("SSH key line crossed persistence boundary")
		}
		return nil
	}
	f.loseComplete = true
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := e.Execute(ctx, a, at); err == nil {
		t.Fatal("lost completion accepted")
	} else {
		t.Log("expected interrupted completion:", err)
	}
	if len(f.requests) != 1 {
		t.Fatal("unexpected key deliveries", len(f.requests))
	}
	f.deliver = func(context.Context, domain.HostingSourceCredentialRequest) (hostingbuildprotocol.SourceCredential, error) {
		t.Fatal("replay refetched key")
		return hostingbuildprotocol.SourceCredential{}, nil
	}
	e.privateCheckout = func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context) (SourceCredential, error), func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
		t.Fatal("replay repeated checkout")
		return nil
	}
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("completion not acknowledged")
	}
	combined, _ := json.Marshal(f.logs)
	if bytes.Contains(combined, []byte(secretLine)) || !bytes.Contains(combined, []byte(runner.RedactionMarker)) {
		t.Fatal("SSH output was not redacted")
	}
}

func TestSSHSourceFailureSettlesOnlyAfterConfirmedCleanup(t *testing.T) {
	for _, code := range []string{"host_key", "fetch_failed", "credential_denied", "cleanup_unconfirmed", "receipt_rejected", "canceled", "receipt_retained"} {
		t.Run(code, func(t *testing.T) {
			e, f, a, at := privateBuildFixture(t)
			s := &f.build.Snapshot
			s.Source.RepositoryURL = "ssh://git@git.example.test/repo.git"
			s.Source.CredentialKind = "ssh_private_key"
			s.Source.Policy.AllowedSchemes = []string{"ssh"}
			s.Source.Policy.SSHHostFingerprints = []string{"SHA256:" + strings.Repeat("A", 43)}
			s.Grant.Kind = "ssh_private_key"
			s.Grant.RepositoryURL = s.Source.RepositoryURL
			s.Grant.PolicyHash = domain.HostingDocumentHash(s.Source.Policy)
			s.InputID = ""
			s.InputID = domain.HostingDocumentHash(*s)
			e.privateCheckout = func(ctx context.Context, snapshot domain.HostingBuildSnapshot, path, frozen string, credential func(context.Context) (SourceCredential, error), accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
				if code == "receipt_retained" {
					receipt := domain.HostingBuildSourceReceipt{BuildID: snapshot.BuildID, SourceID: snapshot.Source.ID, InputID: snapshot.InputID, Commit: strings.Repeat("a", 40), PolicyHash: domain.HostingDocumentHash(snapshot.Source.Policy), TreeDigest: "sha256:" + strings.Repeat("b", 64)}
					if err := accept(ctx, receipt); err != nil {
						return err
					}
					return sourceError("host_key")
				}
				return sourceError(code)
			}
			e.engine = buildEngineFunc(func(context.Context, hostingbuild.Request) (hostingbuild.Result, error) {
				t.Fatal("failed source reached engine")
				return hostingbuild.Result{}, nil
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			err := e.Execute(ctx, a, at)
			settled := code == "host_key" || code == "fetch_failed" || code == "credential_denied"
			if settled {
				if !errors.Is(err, errSettledBuildFailure) || f.build.State != "failed" || f.build.FailureCode != "source_failed" || len(e.journal.Snapshot().Build) != 0 {
					t.Fatal("clean source failure not settled", err, f.build.State)
				}
			} else if err == nil || len(f.completeRequests) != 0 || len(e.journal.Snapshot().Build) != 1 {
				t.Fatal("uncertain source failure settled", err)
			}
		})
	}
}
