package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// errSettledBuildFailure is returned only after exact server completion proof
// and durable local journal acknowledgment. It does not describe uncertainty.
var errSettledBuildFailure = errors.New("local build failed after confirmed completion")

// BuildPhaseClient is the authenticated nonsecret build protocol. Implementations
// must support concurrent status requests and honor cancellation.
type BuildPhaseClient interface {
	BuildStatus(context.Context, hostingbuildprotocol.StatusRequest) (hostingbuildprotocol.StatusResponse, error)
	BuildSourceBegin(context.Context, hostingbuildprotocol.SourceBeginRequest) (hostingbuildprotocol.PhaseResponse, error)
	BuildSourceCommit(context.Context, hostingbuildprotocol.SourceCommitRequest) (hostingbuildprotocol.PhaseResponse, error)
	BuildPrepare(context.Context, hostingbuildprotocol.PrepareRequest) (hostingbuildprotocol.PhaseResponse, error)
	BuildComplete(context.Context, hostingbuildprotocol.CompleteRequest) (hostingbuildprotocol.PhaseResponse, error)
}

// BuildTarget is trusted placement, never a resource-user request. An empty
// BindingID explicitly permits bindings on this exact runner/server/destination.
type BuildTarget struct{ RunnerID, ServerID, DestinationID, BindingID string }

// BuildConfig contains explicit operator settings. Policy must exactly match the
// server snapshot. SourceRoot and Engine.StateDir must be separate owned roots.
type BuildConfig struct {
	Compose                        bool `json:"compose,omitempty"`
	PrivateGit                     *PrivateGitConfig
	SourceRoot                     string
	Engine                         hostingbuild.Config
	Policy                         domain.HostingBuildPolicy
	Target                         BuildTarget
	StatusInterval, RequestTimeout time.Duration
}

// PrivateGitConfig enables trusted HTTPS Basic/PAT checkout explicitly.
// TrustedLoopbackHTTP permits only isolated literal-loopback controller fixtures.
type PrivateGitConfig struct {
	TrustedLoopbackHTTP bool `json:"trusted_loopback_http"`
}

type localBuildEngine interface {
	Build(context.Context, hostingbuild.Request) (hostingbuild.Result, error)
}
type privateBuildCheckout func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context) (SourceCredential, error), func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error
type buildCheckout func(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error

// BuildExecutor executes explicitly configured trusted Git builds. It never settles a generic run.
// Reconcile must finish before Execute. Host and journal locks prevent concurrent
// effects; an unresolved intent blocks another execution of the same build.
type BuildExecutor struct {
	config          BuildConfig
	client          BuildPhaseClient
	journal         *runner.AttemptJournal
	engine          localBuildEngine
	checkout        buildCheckout
	privateCheckout privateBuildCheckout
	lock            func(string, string) (func(), error)
	observe         func(context.Context) (string, error)
	logSecrets      []runner.SecretMaterial
}

// ComputeBuildToolchainIdentity hashes a versioned inventory without local paths.
// Docker is hashed with a 128 MiB bound; the engine independently checks the
// pinned Buildx binary and immutable planner image before it accepts builds.
func ComputeBuildToolchainIdentity(ctx context.Context, c hostingbuild.Config, platform string) (string, error) {
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return "", errors.New("unsupported build platform")
	}
	if !filepath.IsAbs(c.DockerBinary) || filepath.Clean(c.DockerBinary) != c.DockerBinary {
		return "", errors.New("explicit Docker binary required")
	}
	info, err := os.Lstat(c.DockerBinary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 || info.Size() > 128<<20 {
		return "", errors.New("unsafe Docker binary")
	}
	f, err := os.Open(c.DockerBinary)
	if err != nil {
		return "", errors.New("docker binary unavailable")
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	b := make([]byte, 64<<10)
	var n int64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		read, e := f.Read(b)
		n += int64(read)
		if n > 128<<20 {
			return "", errors.New("docker binary exceeds bound")
		}
		_, _ = h.Write(b[:read])
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", errors.New("docker binary unreadable")
		}
	}
	inventory := struct{ Version, Protocol, Platform, DockerSHA256, BuildxSHA256, BuildKit, Dockerfile, Railpack, Static, Planner string }{
		"hosting-executor-toolchain.v1", domain.HostingBuildProtocol, platform, "sha256:" + hex.EncodeToString(h.Sum(nil)), c.BuildxSHA256, hostingbuild.BuildKitImage, hostingbuild.DockerfileFrontend, hostingbuild.RailpackFrontend, hostingbuild.StaticImage, c.PlannerImage,
	}
	caIdentity, err := hostingbuild.RegistryCADigests(c.RegistryCACertificates)
	if err != nil {
		return "", err
	}
	if len(caIdentity) > 0 {
		return domain.HostingDocumentHash(struct {
			Inventory   any
			RegistryCAs map[string]string
		}{inventory, caIdentity}), nil
	}
	return domain.HostingDocumentHash(inventory), nil
}

// Validate checks the same local prerequisites as NewBuildExecutor without a
// journal, server request or build effects. It may initialize engine state.
func (c BuildConfig) Validate() error { _, err := validatedBuildEngine(c); return err }
func validBuildPlacementID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func validatedBuildEngine(c BuildConfig) (*hostingbuild.Engine, error) {
	if c.Compose != c.Policy.GitCompose || domain.ValidateHostingBuildPolicy(c.Policy) != nil || c.Policy.Platform != "linux/"+runtime.GOARCH || c.StatusInterval < 10*time.Millisecond || c.StatusInterval > time.Second || c.RequestTimeout < 10*time.Millisecond || c.RequestTimeout > 30*time.Second {
		return nil, errors.New("invalid explicit build configuration")
	}
	for _, id := range []string{c.Target.RunnerID, c.Target.ServerID, c.Target.DestinationID} {
		if !validBuildPlacementID(id) {
			return nil, errors.New("explicit build target required")
		}
	}
	if !filepath.IsAbs(c.SourceRoot) || filepath.Clean(c.SourceRoot) != c.SourceRoot || c.SourceRoot == c.Engine.StateDir || buildPathContains(c.SourceRoot, c.Engine.StateDir) || buildPathContains(c.Engine.StateDir, c.SourceRoot) {
		return nil, errors.New("separate absolute build roots required")
	}
	if err := validateBuildSourceRoot(c.SourceRoot); err != nil {
		return nil, err
	}
	identity, err := ComputeBuildToolchainIdentity(context.Background(), c.Engine, c.Policy.Platform)
	if err != nil || identity != c.Policy.ToolchainIdentity {
		return nil, errors.New("build toolchain identity mismatch")
	}
	if c.Target.BindingID != "" && !validBuildPlacementID(c.Target.BindingID) {
		return nil, errors.New("invalid configured build binding")
	}
	engine, err := hostingbuild.New(c.Engine)
	if err != nil {
		return nil, errors.New("local build engine prerequisites unavailable")
	}
	return engine, nil
}

// NewBuildExecutor validates local prerequisites. It creates no build effects.
func NewBuildExecutor(c BuildConfig, client BuildPhaseClient, journal *runner.AttemptJournal) (*BuildExecutor, error) {
	if client == nil || journal == nil {
		return nil, errors.New("build client and journal required")
	}
	if _, ok := client.(buildLogClient); !ok {
		return nil, errors.New("typed build log client required")
	}
	if c.PrivateGit != nil {
		if _, ok := client.(privateBuildCredentialClient); !ok {
			return nil, errors.New("typed private credential client required")
		}
	}
	engine, err := validatedBuildEngine(c)
	if err != nil {
		return nil, err
	}
	// Own the policy slices instead of retaining mutable caller configuration.
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	e := &BuildExecutor{config: c, client: client, journal: journal, engine: engine, checkout: checkoutPublicBuild, privateCheckout: checkoutBuildWithCredential, lock: lockBuildSource}
	e.observe = func(ctx context.Context) (string, error) { return observeBuildDaemon(ctx, e.config) }
	return e, nil
}
func buildPathContains(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (rel == "." || len(rel) < 3 || rel[:3] != "../")
}
func buildAuthority(at runner.AttemptIdentity, id string) domain.HostingBuildAuthority {
	return domain.HostingBuildAuthority{BuildID: id, RunID: at.RunID, LeaseID: at.LeaseID, Attempt: at.Attempt, Fence: at.Fence}
}
func (e *BuildExecutor) validateBuild(b domain.HostingBuild, a domain.HostingBuildAuthority, snapshot *domain.HostingBuildSnapshot) error {
	s := b.Snapshot
	unhashed := s
	unhashed.InputID = ""
	if s.Source.Settings.Strategy == "compose" && (!e.config.Compose || !s.Policy.GitCompose) {
		return errors.New("git Compose build requires explicit runner and operator policy")
	}
	if b.ID != a.BuildID || b.RunID != a.RunID || s.BuildID != a.BuildID || b.Revision < 1 || b.ResourceID != s.Source.ResourceID || b.ProjectID != s.Source.ProjectID || s.ResourceGeneration < 1 || s.InputID != domain.HostingDocumentHash(unhashed) || domain.ValidateHostingGitSource(s.Source) != nil || domain.ValidateHostingBuildTriggerRevision(s) != nil || domain.ValidateHostingBuildPolicy(s.Policy) != nil || !reflect.DeepEqual(s.Policy, e.config.Policy) || !e.validBuildCredentialSnapshot(s) || s.RunnerID != e.config.Target.RunnerID || s.ServerID != e.config.Target.ServerID || s.DestinationID != e.config.Target.DestinationID || !validBuildPlacementID(s.BindingID) || e.config.Target.BindingID != "" && s.BindingID != e.config.Target.BindingID || snapshot != nil && !reflect.DeepEqual(s, *snapshot) {
		return errors.New("build immutable snapshot or target mismatch")
	}
	if s.Source.Kind != "dockerfile" {
		u, err := s.Source.Policy.ValidateURL(s.Source.RepositoryURL, false)
		if err != nil || u.Scheme != "https" {
			return errors.New("HTTPS source required")
		}
	}
	if s.ExpectedDaemonID != "" && b.DaemonID != "" && s.ExpectedDaemonID != b.DaemonID {
		return errors.New("build daemon identity changed")
	}
	if b.SourceReceipt != nil && domain.ValidateHostingBuildSourceReceipt(*b.SourceReceipt, s) != nil {
		return errors.New("build source receipt mismatch")
	}
	if b.Artifact != nil && domain.ValidateHostingBuildArtifact(*b.Artifact, b) != nil {
		return errors.New("build artifact mismatch")
	}
	switch b.State {
	case "queued", "sourcing", "building", "succeeded", "failed", "canceled", "cleanup_required":
	default:
		return errors.New("invalid build state")
	}
	return nil
}
func (e *BuildExecutor) status(ctx context.Context, a domain.HostingBuildAuthority, s *domain.HostingBuildSnapshot) (hostingbuildprotocol.StatusResponse, error) {
	request, cancel := context.WithTimeout(ctx, e.config.RequestTimeout)
	defer cancel()
	status, err := e.client.BuildStatus(request, hostingbuildprotocol.StatusRequest{Authority: a})
	if err != nil {
		return status, errors.New("build status unavailable")
	}
	if err = e.validateBuild(status.Build, a, s); err != nil {
		return status, err
	}
	if status.MayExecute && (status.CancelRequested || status.Build.State != "queued" && status.Build.State != "sourcing" && status.Build.State != "building") {
		return status, errors.New("inconsistent build execution permission")
	}
	return status, nil
}
func requireLive(s hostingbuildprotocol.StatusResponse) error {
	if !s.MayExecute || s.CancelRequested {
		return errors.New("build authority lost")
	}
	return nil
}
func (e *BuildExecutor) phase(ctx context.Context, call func(context.Context) (hostingbuildprotocol.PhaseResponse, error), a domain.HostingBuildAuthority, s domain.HostingBuildSnapshot) (hostingbuildprotocol.PhaseResponse, error) {
	request, cancel := context.WithTimeout(ctx, e.config.RequestTimeout)
	defer cancel()
	r, err := call(request)
	if err != nil {
		return r, errors.New("build phase response unavailable; journal retained")
	}
	return r, e.validateBuild(r.Build, a, &s)
}

// Execute requires a bounded operation deadline. The caller owns same-tuple lease
// renewal and cancels ctx on loss; the historical attempt expiry is not a cutoff.
func (e *BuildExecutor) Execute(ctx context.Context, a domain.HostingBuildAuthority, at runner.AttemptIdentity, completionDrain ...func(context.Context) error) error {
	if len(completionDrain) > 1 || len(completionDrain) == 1 && completionDrain[0] == nil {
		return errors.New("invalid build completion drain")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > e.config.Policy.Limits.Deadline || hostingbuildprotocol.ValidateAuthority(a) != nil || buildAuthority(at, a.BuildID) != a || at.CreatedAt.IsZero() || !at.ExpiresAt.After(at.CreatedAt) {
		return errors.New("bounded build context and exact attempt required")
	}
	if _, ok := e.client.(buildLogClient); !ok {
		return errors.New("typed build log client required")
	}
	if _, ok := e.engine.(buildOutputEngine); !ok {
		return errors.New("observed build engine required")
	}
	var output *buildOutputAdapter
	unlock, err := e.lock(e.config.SourceRoot, a.BuildID)
	if err != nil {
		return err
	}
	defer unlock()
	for _, entry := range e.journal.Snapshot().Build {
		if entry.BuildID == a.BuildID {
			return errors.New("unresolved build journal requires reconciliation")
		}
	}
	status, err := e.status(ctx, a, nil)
	if err != nil {
		return err
	}
	if err = requireLive(status); err != nil {
		return err
	}
	if status.Build.State != "queued" || status.Build.SourceReceipt != nil || status.Build.Artifact != nil || status.Build.ComposeResult != nil {
		return errors.New("build already started without local execution evidence")
	}
	// A fresh server response cannot authorize adoption of engine state left
	// without a retained runtime result. In particular, Build's own receipt
	// replay must never fill this journal gap implicitly.
	for _, path := range []string{filepath.Join(e.config.SourceRoot, a.BuildID), filepath.Join(e.config.Engine.StateDir, a.BuildID), filepath.Join(e.config.Engine.StateDir, "cleanup_unconfirmed.json")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			return errors.New("retained local build state requires explicit reconciliation")
		}
	}
	s := status.Build.Snapshot
	intentID, err := runner.NewJournalID("build_intent")
	if err != nil {
		return err
	}
	if _, err = e.journal.AppendBuild(runner.JournalBuild{ID: intentID, Attempt: at, BuildID: a.BuildID, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash(s), CreatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	begin := hostingbuildprotocol.SourceBeginRequest{Authority: a, ExpectedRevision: status.Build.Revision}
	response, err := e.phase(ctx, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
		return e.client.BuildSourceBegin(c, begin)
	}, a, s)
	if err != nil {
		return err
	}
	if response.Acknowledgment != nil || response.Build.State != "sourcing" {
		return errors.New("source begin response mismatch")
	}
	if s.Source.CredentialKind == "public" || s.Source.Kind == "dockerfile" {
		output, err = newBuildOutputAdapter(e, a, at)
		if err != nil {
			return err
		}
		if err = output.Sink(ctx, hostingbuild.Output{Component: hostingbuild.OutputBuild, Stream: hostingbuild.OutputSystem, Data: []byte("Build source acquisition started\n")}); err != nil {
			return err
		}
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	watchCtx, stopWatch := context.WithCancel(operation)
	done := make(chan struct{})
	var watchErr error
	var watchDaemon string
	var watchSource *domain.HostingBuildSourceReceipt
	var watchMu sync.Mutex
	go func() {
		defer close(done)
		ticker := time.NewTicker(e.config.StatusInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				watchMu.Lock()
				pinnedDaemon, pinnedSource := watchDaemon, watchSource
				watchMu.Unlock()
				latest, probeErr := e.status(watchCtx, a, &s)
				if probeErr == nil {
					probeErr = requireLive(latest)
					watchMu.Lock()
					if pinnedDaemon != "" && latest.Build.DaemonID != pinnedDaemon || pinnedSource != nil && (latest.Build.SourceReceipt == nil || *latest.Build.SourceReceipt != *pinnedSource) {
						probeErr = errors.New("live build daemon or source changed")
					}
					watchMu.Unlock()
				}
				if probeErr != nil {
					if watchCtx.Err() == nil {
						watchMu.Lock()
						watchErr = probeErr
						watchMu.Unlock()
						cancel()
					}
					return
				}
			}
		}
	}()
	joined := false
	join := func() {
		if !joined {
			stopWatch()
			<-done
			joined = true
		}
	}
	defer join()
	var built hostingbuild.Result
	var composed *domain.HostingBuildComposeResult
	buildFailed := errors.New("local build failed after confirmed engine cleanup")
	failureCode := "build_failed"
	current := response.Build
	checkout := e.checkout
	if s.Source.CredentialKind == "https_basic" {
		if e.privateCheckout == nil {
			return errors.New("private checkout unavailable; intent retained")
		}
		checkout = func(c context.Context, snapshot domain.HostingBuildSnapshot, path, frozen string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
			return e.privateCheckout(c, snapshot, path, frozen, func(c context.Context) (SourceCredential, error) {
				material, err := e.privateBuildCredential(c, a, s, intentID)
				if err != nil {
					return SourceCredential{}, err
				}
				output, err = newBuildOutputAdapter(e, a, at, runner.SecretMaterial{Value: string(material.Bytes), Encodings: []string{"base64", "base64url", "hex"}}, runner.SecretMaterial{Value: material.Username + ":" + string(material.Bytes), Encodings: []string{"base64", "base64url", "hex"}})
				if err != nil {
					material.Close()
					return SourceCredential{}, err
				}
				if err = output.Sink(c, hostingbuild.Output{Component: hostingbuild.OutputBuild, Stream: hostingbuild.OutputSystem, Data: []byte("Build source acquisition started\n")}); err != nil {
					material.Close()
					return SourceCredential{}, err
				}
				return SourceCredential{Kind: material.Kind, Username: material.Username, Bytes: material.Bytes, UseUntil: material.UseUntil}, nil
			}, accept, use)
		}
	}
	frozen, err := buildFrozenCommit(s, "")
	if err != nil {
		return err
	}
	err = checkout(operation, s, filepath.Join(e.config.SourceRoot, a.BuildID), frozen, func(c context.Context, receipt domain.HostingBuildSourceReceipt) error {
		if domain.ValidateHostingBuildSourceReceipt(receipt, s) != nil {
			return errors.New("source identity mismatch")
		}
		id, idErr := runner.NewJournalID("build_source")
		if idErr != nil {
			return idErr
		}
		req := hostingbuildprotocol.SourceCommitRequest{Authority: a, ReceiptID: id, Receipt: receipt}
		if _, appendErr := e.journal.AppendBuild(runner.JournalBuild{ID: id, Attempt: at, BuildID: a.BuildID, Phase: "source", Source: &req, CreatedAt: time.Now().UTC()}); appendErr != nil {
			return appendErr
		}
		r, commitErr := e.phase(c, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
			return e.client.BuildSourceCommit(c, req)
		}, a, s)
		if commitErr != nil {
			return commitErr
		}
		if r.Acknowledgment == nil || hostingbuildprotocol.ValidateAcknowledgment(*r.Acknowledgment, a, "source", id, hostingbuildprotocol.SourceReceiptHash(receipt)) != nil || r.Build.SourceReceipt == nil || *r.Build.SourceReceipt != receipt {
			return errors.New("source acknowledgment mismatch")
		}
		current = r.Build
		watchMu.Lock()
		receiptCopy := receipt
		watchSource = &receiptCopy
		watchMu.Unlock()
		return e.journal.AckBuild(id)
	}, func(c context.Context, path string) error {
		daemon, observeErr := e.observe(c)
		if observeErr != nil {
			return observeErr
		}
		if current.DaemonID != "" && daemon != current.DaemonID || s.ExpectedDaemonID != "" && daemon != s.ExpectedDaemonID {
			return errors.New("observed daemon mismatch")
		}
		live, liveErr := e.status(c, a, &s)
		if liveErr != nil {
			return liveErr
		}
		if liveErr = requireLive(live); liveErr != nil {
			return liveErr
		}
		req := hostingbuildprotocol.PrepareRequest{Authority: a, ExpectedRevision: live.Build.Revision, DaemonID: daemon}
		r, prepareErr := e.phase(c, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
			return e.client.BuildPrepare(c, req)
		}, a, s)
		if prepareErr != nil {
			return prepareErr
		}
		if r.Acknowledgment != nil || r.Build.State != "building" || r.Build.DaemonID != daemon || r.Build.SourceReceipt == nil || current.SourceReceipt == nil || *r.Build.SourceReceipt != *current.SourceReceipt {
			return errors.New("build preparation mismatch")
		}
		current = r.Build
		watchMu.Lock()
		watchDaemon = daemon
		watchMu.Unlock()
		live, liveErr = e.status(c, a, &s)
		if liveErr != nil {
			return liveErr
		}
		if liveErr = requireLive(live); liveErr != nil {
			return liveErr
		}
		if live.Build.DaemonID != daemon || live.Build.State != "building" {
			return errors.New("build preparation lost")
		}
		var result hostingbuild.Result
		var buildErr error
		if s.Source.Settings.Strategy == "compose" {
			var prepared domain.HostingBuildComposeResult
			prepared, buildErr = e.buildComposeWithEngine(c, current, path, output.Sink, func(child context.Context, request hostingbuild.Request) (hostingbuild.Result, error) {
				return e.runBuildEngine(child, current, a, intentID, request, output)
			})
			if buildErr == nil {
				composed = &prepared
			}
		} else {
			result, buildErr = e.runBuildEngine(c, current, a, intentID, buildEngineRequest(s, path, daemon), output)
		}
		if buildErr != nil {
			if errors.Is(buildErr, hostingbuild.ErrOutputSink) || output.failed {
				return errors.New("build output delivery unresolved; intent retained")
			}
			if errors.Is(buildErr, hostingbuild.ErrCleanupUnconfirmed) {
				return errors.New("local build cleanup unresolved; intent retained")
			}
			return buildFailed
		}
		if c.Err() != nil {
			return c.Err()
		}
		built = result
		return nil
	})
	join()
	watchMu.Lock()
	observerErr := watchErr
	watchMu.Unlock()
	if (s.Source.Settings.Strategy == "compose" || s.Source.Kind == "dockerfile") && observerErr == nil && ctx.Err() == nil && settledComposeSourceFailure(err, filepath.Join(e.config.SourceRoot, a.BuildID), e.config.Engine.StateDir) {
		err = buildFailed
		failureCode = "source_failed"
		if output == nil {
			output, err = newBuildOutputAdapter(e, a, at)
			if err != nil {
				return err
			}
			err = buildFailed
		}
	}
	if err != nil && err != buildFailed || observerErr != nil || ctx.Err() != nil {
		return errors.New("build execution or cleanup unresolved; journal retained")
	}
	if output == nil {
		return errors.New("build output unavailable; intent retained")
	}
	if finishErr := output.Finish(ctx); finishErr != nil {
		return finishErr
	}
	artifact := buildArtifact(built, current)
	completion := hostingbuildprotocol.Completion{State: "succeeded", Artifact: &artifact}
	if s.Source.Settings.Strategy == "compose" {
		completion = hostingbuildprotocol.Completion{State: "succeeded", ComposeResult: composed}
	}
	if err == buildFailed {
		completion = hostingbuildprotocol.Completion{State: "failed", FailureCode: failureCode}
	}
	if completion.State == "succeeded" {
		if s.Source.Settings.Strategy == "compose" {
			if composed == nil || domain.ValidateHostingBuildComposeResult(*composed, current) != nil {
				return errors.New("compose build result identity mismatch")
			}
		} else if domain.ValidateHostingBuildArtifact(artifact, current) != nil || built.BuildID != s.BuildID || built.SourceID != s.Source.ID || built.InputID != s.InputID {
			return errors.New("local build result identity mismatch")
		}
	}
	live, err := e.status(ctx, a, &s)
	if err != nil {
		return err
	}
	if err = requireLive(live); err != nil {
		return err
	}
	if live.Build.DaemonID != current.DaemonID || domain.HostingDocumentHash(live.Build.SourceReceipt) != domain.HostingDocumentHash(current.SourceReceipt) || completion.FailureCode != "source_failed" && live.Build.SourceReceipt == nil {
		return errors.New("completion target changed")
	}
	id, err := runner.NewJournalID("build_result")
	if err != nil {
		return err
	}
	req := hostingbuildprotocol.CompleteRequest{Authority: a, ReceiptID: id, ExpectedRevision: live.Build.Revision, Completion: completion}
	entry := runner.JournalBuild{ID: id, Attempt: at, BuildID: a.BuildID, Phase: "result", Result: &req, CreatedAt: time.Now().UTC()}
	if _, err = e.journal.AppendBuild(entry); err != nil {
		return err
	}
	// The result is durable and source/engine/log cleanup has completed. Join
	// outer authority workers before our own terminal publish, retaining the
	// supervisor's local expiry, parent cancellation and every server fence.
	if len(completionDrain) == 1 {
		if err = completionDrain[0](ctx); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r, err := e.phase(ctx, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
		return e.client.BuildComplete(c, req)
	}, a, s)
	if err != nil {
		return err
	}
	if err = buildResultAcknowledged(r, entry); err != nil {
		return err
	}
	if err = e.journal.AckBuildCompletion(id, *r.Acknowledgment); err != nil {
		return err
	}
	if completion.State != "succeeded" {
		return errSettledBuildFailure
	}
	return nil
}
func buildEngineRequest(s domain.HostingBuildSnapshot, path, daemon string) hostingbuild.Request {
	l := s.Policy.Limits
	settings := s.Source.Settings
	request := hostingbuild.Request{BuildID: s.BuildID, Checkout: path, SourceID: s.Source.ID, InputID: s.InputID, DaemonID: daemon, Strategy: hostingbuild.Strategy(settings.Strategy), Platform: s.Policy.Platform, ContextDir: settings.ContextDir, DockerfilePath: settings.DockerfilePath, BuildTarget: settings.BuildTarget, PublishDir: settings.PublishDir, SPAFallback: settings.SPAFallback, Limits: hostingbuild.Limits(l)}
	if settings.Strategy == "dockerfile" && len(settings.BuildVariables) > 0 {
		request.BuildArgs = make(map[string]string, len(settings.BuildVariables))
		for _, variable := range settings.BuildVariables {
			request.BuildArgs[variable.Name] = variable.Value
		}
		request.InjectBuildArgs = settings.InjectBuildArgsToDockerfile == nil || *settings.InjectBuildArgsToDockerfile
	}
	for _, ref := range settings.BuildSecrets {
		request.BuildSecrets = append(request.BuildSecrets, hostingbuild.SecretReference{Name: ref.Name, SecretID: ref.Secret.SecretID, Version: ref.Secret.Version})
	}
	if ref := s.Source.Settings.BuildRegistryCredential; ref != nil {
		request.BuildRegistry = &hostingbuild.RegistryReference{SecretID: ref.SecretID, Version: ref.Version}
	}
	return request
}
func buildArtifact(r hostingbuild.Result, b domain.HostingBuild) domain.HostingBuildArtifact {
	return domain.HostingBuildArtifact{Origin: r.Origin, BuildID: r.BuildID, SourceID: r.SourceID, InputID: r.InputID, SourceReceiptHash: domain.HostingDocumentHash(b.SourceReceipt), ContextDigest: r.ContextDigest, RequestDigest: r.RequestDigest, DaemonID: r.DaemonID, ImageID: r.ImageID, IndexDigest: r.IndexDigest, ManifestDigest: r.ManifestDigest, ConfigDigest: r.ConfigDigest, Platform: r.Platform, ContextPolicy: r.ContextPolicy}
}

// Private delivery never runs during startup reconciliation. Its request ID is
// already durable, and retries cannot mint a fresh server-side credential window.
type privateBuildCredentialClient interface {
	BuildSourceCredential(context.Context, string, domain.HostingSourceCredentialRequest, runnerclient.SourceCredentialTransport) (hostingbuildprotocol.SourceCredential, error)
}

func (e *BuildExecutor) validBuildCredentialSnapshot(s domain.HostingBuildSnapshot) bool {
	if s.Source.CredentialKind == "public" || s.Source.Kind == "dockerfile" {
		return s.Grant == nil
	}
	if e.config.PrivateGit == nil || s.Source.CredentialKind != "https_basic" || s.Grant == nil {
		return false
	}
	g := s.Grant
	return g.ID != "" && g.Version == 1 && !g.Revoked && g.Kind == "https_basic" && g.SourceID == s.Source.ID && g.ResourceID == s.Source.ResourceID && g.ProjectID == s.Source.ProjectID && g.RepositoryURL == s.Source.RepositoryURL && g.PolicyHash == domain.HostingDocumentHash(s.Source.Policy) && g.SecretID != "" && g.SecretVersion > 0 && g.CreatedBy != ""
}
func (e *BuildExecutor) privateBuildCredential(ctx context.Context, a domain.HostingBuildAuthority, s domain.HostingBuildSnapshot, intentID string) (hostingbuildprotocol.SourceCredential, error) {
	var zero hostingbuildprotocol.SourceCredential
	client, ok := e.client.(privateBuildCredentialClient)
	id, prefixed := strings.CutPrefix(intentID, "build_intent_")
	if !ok || !prefixed || len(id) != 32 || !domain.ValidHostingSourceDeliveryID(id) || !e.validBuildCredentialSnapshot(s) || e.config.PrivateGit == nil {
		return zero, errors.New("private credential intent unavailable")
	}
	retained := false
	for _, entry := range e.journal.Snapshot().Build {
		if entry.ID == intentID && entry.Phase == "attempt_intent" && buildAuthority(entry.Attempt, entry.BuildID) == a && entry.InputHash == domain.HostingDocumentHash(s) {
			retained = true
		}
	}
	if !retained {
		return zero, errors.New("private credential intent not durable")
	}
	request := domain.HostingSourceCredentialRequest{Authority: a, DeliveryID: id}
	for attempt := 0; attempt < 3; attempt++ {
		status, err := e.status(ctx, a, &s)
		if err != nil || requireLive(status) != nil || status.Build.State != "sourcing" {
			return zero, errors.New("private credential authority lost")
		}
		call, cancel := context.WithTimeout(ctx, e.config.RequestTimeout)
		material, err := client.BuildSourceCredential(call, e.config.Target.RunnerID, request, runnerclient.SourceCredentialTransport{TrustedLoopbackHTTP: e.config.PrivateGit.TrustedLoopbackHTTP})
		cancel()
		if err == nil {
			if ctx.Err() != nil || material.Authority != a || material.DeliveryID != id || material.RequestHash != hostingbuildprotocol.SourceCredentialRequestHash(e.config.Target.RunnerID, request) || material.Kind != "https_basic" || !hostingbuildprotocol.ValidBasicCredential(material.Username, material.Bytes) || !time.Now().Before(material.UseUntil) || material.UseUntil.After(time.Now().Add(time.Minute)) {
				material.Close()
				return zero, errors.New("private credential response mismatch")
			}
			return material, nil
		}
		material.Close()
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if attempt < 2 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return zero, errors.New("private credential unavailable; intent retained")
}

// buildFrozenCommit keeps trigger intent authoritative across first checkout and replay.
func buildFrozenCommit(snapshot domain.HostingBuildSnapshot, frozen string) (string, error) {
	if err := domain.ValidateHostingBuildTriggerRevision(snapshot); err != nil {
		return "", errors.New("invalid build trigger revision")
	}
	if snapshot.TriggerRevision == nil {
		return frozen, nil
	}
	expected := snapshot.TriggerRevision.ExpectedCommit
	if frozen != "" && frozen != expected {
		return "", errors.New("build trigger revision mismatch")
	}
	return expected, nil
}
