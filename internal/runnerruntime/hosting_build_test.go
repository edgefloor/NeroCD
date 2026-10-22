package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/source"
)

type buildFixtureClient struct {
	mu                           sync.Mutex
	build                        domain.HostingBuild
	authority                    domain.HostingBuildAuthority
	receipts                     []hostingbuildprotocol.ReceiptAcknowledgment
	live                         bool
	cancelRequested              bool
	loseSource, loseComplete     bool
	beforeSource, beforeComplete func()
	completeRequests             []hostingbuildprotocol.CompleteRequest
	logs                         []domain.HostingBuildLogEvent
	logRequests                  []domain.HostingBuildLogBatch
	beforeLogs                   func(domain.HostingBuildLogBatch) error
}

func (f *buildFixtureClient) BuildEvents(ctx context.Context, b domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	if err := ctx.Err(); err != nil {
		return domain.HostingBuildLogAck{}, err
	}
	if f.beforeLogs != nil {
		if err := f.beforeLogs(b); err != nil {
			return domain.HostingBuildLogAck{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if b.Authority != f.authority {
		return domain.HostingBuildLogAck{}, errors.New("authority")
	}
	f.logRequests = append(f.logRequests, b)
	ack := domain.HostingBuildLogAck{Authority: b.Authority}
	for _, e := range b.Events {
		found := false
		for _, old := range f.logs {
			if old.EventKey == e.EventKey {
				if domain.HostingBuildLogBodyHash(b.Authority, old) != domain.HostingBuildLogBodyHash(b.Authority, e) {
					return domain.HostingBuildLogAck{}, errors.New("body changed")
				}
				found = true
			}
		}
		if !found {
			if !f.live {
				return domain.HostingBuildLogAck{}, errors.New("authority")
			}
			f.logs = append(f.logs, e)
		}
		ack.Receipts = append(ack.Receipts, domain.HostingBuildLogReceipt{EventKey: e.EventKey, RequestedSequence: e.Sequence, Sequence: e.Sequence, BodyHash: domain.HostingBuildLogBodyHash(b.Authority, e)})
	}
	return ack, nil
}

func (f *buildFixtureClient) BuildStatus(ctx context.Context, _ hostingbuildprotocol.StatusRequest) (hostingbuildprotocol.StatusResponse, error) {
	if ctx.Err() != nil {
		return hostingbuildprotocol.StatusResponse{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return hostingbuildprotocol.StatusResponse{Build: f.build, MayExecute: f.live, CancelRequested: f.cancelRequested, Receipts: append([]hostingbuildprotocol.ReceiptAcknowledgment(nil), f.receipts...)}, nil
}
func (f *buildFixtureClient) BuildSourceBegin(_ context.Context, r hostingbuildprotocol.SourceBeginRequest) (hostingbuildprotocol.PhaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Authority != f.authority || r.ExpectedRevision != f.build.Revision {
		return hostingbuildprotocol.PhaseResponse{}, errors.New("CAS")
	}
	f.build.State = "sourcing"
	f.build.Revision++
	return hostingbuildprotocol.PhaseResponse{Build: f.build}, nil
}
func (f *buildFixtureClient) BuildSourceCommit(_ context.Context, r hostingbuildprotocol.SourceCommitRequest) (hostingbuildprotocol.PhaseResponse, error) {
	if f.beforeSource != nil {
		f.beforeSource()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Authority != f.authority {
		return hostingbuildprotocol.PhaseResponse{}, errors.New("authority")
	}
	ack := hostingbuildprotocol.ReceiptAcknowledgment{Authority: r.Authority, Phase: "source", ReceiptID: r.ReceiptID, BodyHash: hostingbuildprotocol.SourceReceiptHash(r.Receipt)}
	if f.build.SourceReceipt == nil {
		copy := r.Receipt
		f.build.SourceReceipt = &copy
		f.build.Revision++
		f.receipts = append(f.receipts, ack)
	}
	if f.loseSource {
		f.loseSource = false
		return hostingbuildprotocol.PhaseResponse{}, errors.New("response lost")
	}
	return hostingbuildprotocol.PhaseResponse{Build: f.build, Acknowledgment: &ack}, nil
}
func (f *buildFixtureClient) BuildPrepare(_ context.Context, r hostingbuildprotocol.PrepareRequest) (hostingbuildprotocol.PhaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ExpectedRevision != f.build.Revision || r.Authority != f.authority || !f.live {
		return hostingbuildprotocol.PhaseResponse{}, errors.New("CAS")
	}
	f.build.DaemonID = r.DaemonID
	f.build.State = "building"
	f.build.Revision++
	return hostingbuildprotocol.PhaseResponse{Build: f.build}, nil
}
func (f *buildFixtureClient) BuildComplete(_ context.Context, r hostingbuildprotocol.CompleteRequest) (hostingbuildprotocol.PhaseResponse, error) {
	if f.beforeComplete != nil {
		f.beforeComplete()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeRequests = append(f.completeRequests, r)
	phase, _ := hostingbuildprotocol.CompletionPhase(r.Completion)
	ack := hostingbuildprotocol.ReceiptAcknowledgment{Authority: r.Authority, Phase: phase, ReceiptID: r.ReceiptID, BodyHash: hostingbuildprotocol.CompletionHash(r.Completion)}
	found := false
	for _, old := range f.receipts {
		if old == ack {
			found = true
		}
	}
	if !found {
		if !f.live || r.Authority != f.authority || r.ExpectedRevision != f.build.Revision {
			return hostingbuildprotocol.PhaseResponse{}, errors.New("CAS")
		}
		f.build.State = r.Completion.State
		f.build.FailureCode = r.Completion.FailureCode
		f.build.Artifact = r.Completion.Artifact
		f.build.Revision++
		f.live = false
		f.receipts = append(f.receipts, ack)
	}
	if f.loseComplete {
		f.loseComplete = false
		return hostingbuildprotocol.PhaseResponse{}, errors.New("response lost")
	}
	return hostingbuildprotocol.PhaseResponse{Build: f.build, Acknowledgment: &ack}, nil
}

type buildEngineFunc func(context.Context, hostingbuild.Request) (hostingbuild.Result, error)

func (f buildEngineFunc) Build(c context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
	return f(c, r)
}
func (f buildEngineFunc) BuildWithOutput(c context.Context, r hostingbuild.Request, _ hostingbuild.OutputSink) (hostingbuild.Result, error) {
	return f(c, r)
}
func buildFixture(t *testing.T) (*BuildExecutor, *buildFixtureClient, domain.HostingBuildAuthority, runner.AttemptIdentity) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := runner.OpenAttemptJournal(filepath.Join(root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	policy := domain.HostingBuildPolicy{Identity: "sha256:" + strings.Repeat("1", 64), ToolchainIdentity: "sha256:" + strings.Repeat("2", 64), Protocol: domain.HostingBuildProtocol, Platform: "linux/amd64", Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 32 << 20, UnpackedBytes: 64 << 20, UsageBytes: 128 << 20, ReserveBytes: 1 << 20, PollInterval: 10 * time.Millisecond, DiskMode: "monitored"}, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 4 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 10, PollInterval: 10 * time.Millisecond}}
	s := domain.HostingBuildSnapshot{BuildID: "fixture", ResourceGeneration: 1, Source: domain.HostingGitSource{ID: "source", ResourceID: "resource", ProjectID: "project", Generation: 1, RepositoryURL: "https://git.example.test/repo.git", RequestedRef: "main", CredentialKind: "public", Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"git.example.test"}}, Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}, BindingID: "binding", DestinationID: "destination", ServerID: "server", RunnerID: "runner", Policy: policy}
	s.InputID = domain.HostingDocumentHash(s)
	a := domain.HostingBuildAuthority{BuildID: s.BuildID, RunID: "build_run_" + s.BuildID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	at := runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(-time.Second)}
	client := &buildFixtureClient{build: domain.HostingBuild{ID: s.BuildID, ProjectID: s.Source.ProjectID, ResourceID: s.Source.ResourceID, State: "queued", RunID: a.RunID, Revision: 1, Snapshot: s}, authority: a, live: true}
	e := &BuildExecutor{config: BuildConfig{SourceRoot: root, Engine: hostingbuild.Config{StateDir: filepath.Join(root, "state")}, Policy: policy, Target: BuildTarget{RunnerID: s.RunnerID, ServerID: s.ServerID, DestinationID: s.DestinationID, BindingID: s.BindingID}, StatusInterval: 10 * time.Millisecond, RequestTimeout: time.Second}, client: client, journal: journal, lock: func(string, string) (func(), error) { return func() {}, nil }, observe: func(context.Context) (string, error) { return "daemon", nil }}
	e.checkout = func(c context.Context, s domain.HostingBuildSnapshot, p, _ string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
		if len(e.journal.Snapshot().Build) == 0 {
			t.Fatal("fetch preceded durable intent")
		}
		if err := os.Mkdir(p, 0700); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(p) }()
		receipt := domain.HostingBuildSourceReceipt{BuildID: s.BuildID, SourceID: s.Source.ID, InputID: s.InputID, Commit: strings.Repeat("a", 40), PolicyHash: domain.HostingDocumentHash(s.Source.Policy), TreeDigest: "sha256:" + strings.Repeat("b", 64)}
		if err := accept(c, receipt); err != nil {
			return err
		}
		return use(c, p)
	}
	e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
		return fixtureBuildResult(r), nil
	})
	return e, client, a, at
}
func fixtureBuildResult(r hostingbuild.Request) hostingbuild.Result {
	return hostingbuild.Result{Origin: "local_build", BuildID: r.BuildID, SourceID: r.SourceID, InputID: r.InputID, DaemonID: r.DaemonID, Platform: r.Platform, ContextDigest: "sha256:" + strings.Repeat("c", 64), RequestDigest: "sha256:" + strings.Repeat("d", 64), ImageID: "sha256:" + strings.Repeat("e", 64), IndexDigest: "sha256:" + strings.Repeat("e", 64), ManifestDigest: "sha256:" + strings.Repeat("f", 64), ConfigDigest: "sha256:" + strings.Repeat("0", 64)}
}
func TestBuildExecutorPublicSuccessAndRenewedLease(t *testing.T) {
	e, client, a, at := buildFixture(t)
	client.beforeSource = func() {
		entries := e.journal.Snapshot().Build
		if len(entries) != 2 || entries[1].Source == nil {
			t.Error("source POST preceded append")
		}
	}
	client.beforeComplete = func() {
		if _, err := os.Lstat(filepath.Join(e.config.SourceRoot, a.BuildID)); !os.IsNotExist(err) {
			t.Error("completion preceded checkout cleanup")
		}
		entries := e.journal.Snapshot().Build
		if len(entries) != 2 || entries[1].Result == nil {
			t.Error("completion POST preceded append")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// Expired initial timestamps represent a renewed same-tuple lease. Fresh
	// authenticated status, not this historical timestamp, governs execution.
	if err := e.Execute(ctx, a, at); err != nil {
		t.Fatal(err)
	}
	if client.build.State != "succeeded" || len(e.journal.Snapshot().Build) != 0 {
		t.Fatal("success not acknowledged")
	}
}
func TestBuildExecutorRejectsUntrustedSnapshots(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.HostingBuild)
	}{
		{"policy", func(b *domain.HostingBuild) { b.Snapshot.Policy.Limits.MemoryBytes++ }},
		{"target", func(b *domain.HostingBuild) { b.Snapshot.RunnerID = "other" }},
		{"private", func(b *domain.HostingBuild) { b.Snapshot.Source.CredentialKind = "https_basic" }},
		{"input", func(b *domain.HostingBuild) { b.Snapshot.InputID = "sha256:" + strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, client, a, at := buildFixture(t)
			test.mutate(&client.build)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if e.Execute(ctx, a, at) == nil {
				t.Fatal("accepted changed snapshot")
			}
			if len(e.journal.Snapshot().Build) != 0 {
				t.Fatal("effect before validation")
			}
		})
	}
}
func TestBuildExecutorCancellationAndLeaseLossJoin(t *testing.T) {
	for _, mode := range []string{"parent", "deadline", "status", "status-error", "daemon-change", "source-change"} {
		t.Run(mode, func(t *testing.T) {
			e, client, a, at := buildFixture(t)
			started := make(chan struct{})
			joined := make(chan struct{})
			e.engine = buildEngineFunc(func(c context.Context, _ hostingbuild.Request) (hostingbuild.Result, error) {
				close(started)
				<-c.Done()
				close(joined)
				return hostingbuild.Result{}, c.Err()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- e.Execute(ctx, a, at) }()
			<-started
			switch mode {
			case "parent":
				cancel()
			case "status":
				client.mu.Lock()
				client.live = false
				client.mu.Unlock()
			case "daemon-change":
				client.mu.Lock()
				client.build.DaemonID = "other-daemon"
				client.mu.Unlock()
			case "source-change":
				client.mu.Lock()
				copy := *client.build.SourceReceipt
				copy.TreeDigest = "sha256:" + strings.Repeat("9", 64)
				client.build.SourceReceipt = &copy
				client.mu.Unlock()
			case "status-error":
				client.mu.Lock()
				client.build.Snapshot.ServerID = "changed"
				client.mu.Unlock()
			}
			if err := <-done; err == nil {
				t.Fatal("canceled execution succeeded")
			}
			select {
			case <-joined:
			default:
				t.Fatal("engine outlived Execute")
			}
			if len(e.journal.Snapshot().Build) == 0 || len(client.completeRequests) != 0 {
				t.Fatal("uncertainty lost")
			}
		})
	}
}
func TestBuildExecutorRejectsDaemonAndPartialResult(t *testing.T) {
	for _, mode := range []string{"daemon", "partial", "cleanup", "source-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			e, client, a, at := buildFixture(t)
			calls := 0
			e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
				calls++
				err := errors.New("failure")
				if mode == "cleanup" {
					err = hostingbuild.ErrCleanupUnconfirmed
				}
				return fixtureBuildResult(r), err
			})
			if mode == "daemon" {
				client.build.Snapshot.ExpectedDaemonID = "other"
				s := client.build.Snapshot
				s.InputID = ""
				client.build.Snapshot.InputID = domain.HostingDocumentHash(s)
			}
			if mode == "source-cleanup" {
				old := e.checkout
				e.engine = buildEngineFunc(func(_ context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
					return fixtureBuildResult(r), nil
				})
				e.checkout = func(c context.Context, s domain.HostingBuildSnapshot, p, f string, a func(context.Context, domain.HostingBuildSourceReceipt) error, u func(context.Context, string) error) error {
					if err := old(c, s, p, f, a, u); err != nil {
						return err
					}
					return errors.New("cleanup unconfirmed")
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if e.Execute(ctx, a, at) == nil {
				t.Fatal("invalid result succeeded")
			}
			if mode == "daemon" && calls != 0 {
				t.Fatal("build reached wrong daemon")
			}
			if client.build.Artifact != nil || mode != "partial" && len(e.journal.Snapshot().Build) == 0 {
				t.Fatal("failed result accepted")
			}
		})
	}
}
func TestBuildToolchainIdentityBoundedAndPathIndependent(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "docker")
	second := filepath.Join(root, "docker-copy")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("binary"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := hostingbuild.Config{DockerBinary: first, BuildxSHA256: "pinned", PlannerImage: "immutable"}
	a, err := ComputeBuildToolchainIdentity(t.Context(), c, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	c.DockerBinary = second
	b, err := ComputeBuildToolchainIdentity(t.Context(), c, "linux/amd64")
	if err != nil || a != b {
		t.Fatal("identity contains local paths")
	}
	if err = os.WriteFile(second, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	b, err = ComputeBuildToolchainIdentity(t.Context(), c, "linux/amd64")
	if err != nil || a == b {
		t.Fatal("binary content not bound")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = ComputeBuildToolchainIdentity(ctx, c, "linux/amd64"); !errors.Is(err, context.Canceled) {
		t.Fatal("hash ignored cancellation")
	}
}

func TestBuildConfigurationValidateSharesConstructorChecks(t *testing.T) {
	e, _, _, _ := buildFixture(t)
	c := e.config
	c.Policy.Protocol = "unsupported"
	err := c.Validate()
	_, constructorErr := NewBuildExecutor(c, e.client, e.journal)
	if err == nil || constructorErr == nil || err.Error() != constructorErr.Error() {
		t.Fatal("standalone validation differs from constructor")
	}
	for _, id := range []string{"", "binding/escape", "with space", "binding\n", strings.Repeat("a", 129)} {
		if validBuildPlacementID(id) {
			t.Fatal("invalid placement ID accepted")
		}
	}
}
