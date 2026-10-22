package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type observationSampleFunc func(context.Context, domain.HostingObservationTarget) domain.HostingRuntimeObservation

func (f observationSampleFunc) Sample(ctx context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
	return f(ctx, t)
}

type observationClientFixture struct {
	targets func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error)
	report  func(context.Context, domain.HostingRuntimeObservation) (domain.HostingObservationAck, error)
}

func (f observationClientFixture) HostingObservationTargets(ctx context.Context, in domain.HostingObservationTargetsInput, _ ...runnerclient.ObservationTransport) (domain.HostingObservationTargets, error) {
	return f.targets(ctx, in)
}
func (f observationClientFixture) ReportHostingObservation(ctx context.Context, in domain.HostingRuntimeObservation, _ ...runnerclient.ObservationTransport) (domain.HostingObservationAck, error) {
	if f.report != nil {
		return f.report(ctx, in)
	}
	return domain.HostingObservationAck{}, nil
}
func observationTargetFixture() domain.HostingObservationTarget {
	now := time.Now().UTC()
	return domain.HostingObservationTarget{SampleID: "hos_" + strings.Repeat("a", 32), IssuedAt: now, ExpiresAt: now.Add(10 * time.Second), ResourceID: "res_a", BindingID: "binding_a", DestinationID: "dest_a", ServerID: "server_a", RunnerID: "runner_a", OperationID: "hop_a", DaemonID: "daemon-image-test", ContainerID: strings.Repeat("b", 64), ImageID: "sha256:" + strings.Repeat("c", 64), ReleaseHash: "sha256:" + strings.Repeat("d", 64), PlanHash: "sha256:" + strings.Repeat("e", 64), RuntimeNamespace: "ncd_" + strings.Repeat("a", 36), ContainerPort: 80, HostPort: 18080, ReadinessPath: "/ready", SampleTimeoutSeconds: 5}
}
func observationPageFixture() domain.HostingObservationTargets {
	target := observationTargetFixture()
	return domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{target}, NextCursor: target.BindingID, EndOfSweep: true}
}
func TestObservationPageRejectsAllTargetsBeforeSampling(t *testing.T) {
	for _, mode := range []string{"runner", "container", "image", "namespace", "hostport", "path", "duration", "future", "expired", "duplicate", "cursor", "nil", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			page := observationPageFixture()
			target := &page.Targets[0]
			switch mode {
			case "runner":
				target.RunnerID = "other"
			case "container":
				target.ContainerID = "short"
			case "image":
				target.ImageID = "tag:latest"
			case "namespace":
				target.RuntimeNamespace = "../bad"
			case "hostport":
				target.HostPort = 0
			case "path":
				target.ReadinessPath = "//example.com"
			case "duration":
				target.ExpiresAt = target.IssuedAt.Add(time.Minute)
			case "future":
				target.IssuedAt = target.IssuedAt.Add(time.Minute)
				target.ExpiresAt = target.IssuedAt.Add(10 * time.Second)
			case "expired":
				target.IssuedAt = target.IssuedAt.Add(-time.Minute)
				target.ExpiresAt = target.IssuedAt.Add(10 * time.Second)
			case "duplicate":
				page.Targets = append(page.Targets, *target)
			case "cursor":
				page.NextCursor = "binding_0"
			case "nil":
				page.Targets = nil
			case "oversized":
				for len(page.Targets) < 5 {
					page.Targets = append(page.Targets, *target)
				}
			}
			sampled := 0
			c := hostingObservationCollector{runnerID: "runner_a", client: observationClientFixture{targets: func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
				return page, nil
			}}, sampler: observationSampleFunc(func(context.Context, domain.HostingObservationTarget) domain.HostingRuntimeObservation {
				sampled++
				return domain.HostingRuntimeObservation{}
			})}
			c.cycle(t.Context())
			if sampled != 0 {
				t.Fatal("malformed page reached Docker sampler")
			}
		})
	}
}
func TestObservationCollectorFairCursorAndRetry(t *testing.T) {
	target := observationTargetFixture()
	calls := 0
	reports := []domain.HostingRuntimeObservation{}
	var sampled atomic.Int32
	c := hostingObservationCollector{runnerID: "runner_a", sampler: observationSampleFunc(func(_ context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
		sampled.Add(1)
		return unknownObservation(t, "inspect_failed")
	})}
	c.client = observationClientFixture{targets: func(_ context.Context, in domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
		calls++
		if in.Limit != 4 {
			t.Fatal("unbounded page")
		}
		switch calls {
		case 1:
			if in.AfterBindingID != "" {
				t.Fatal("initial cursor")
			}
			return domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{}, NextCursor: "binding_0", EndOfSweep: false}, nil
		case 2:
			if in.AfterBindingID != "binding_0" {
				t.Fatal("skipped ineligible cursor lost")
			}
			return domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{target}, NextCursor: "binding_z", EndOfSweep: false}, nil
		default:
			if in.AfterBindingID != "binding_z" {
				t.Fatal("fair cursor lost")
			}
			return domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{}, EndOfSweep: true}, nil
		}
	}, report: func(_ context.Context, in domain.HostingRuntimeObservation) (domain.HostingObservationAck, error) {
		reports = append(reports, in)
		if len(reports) == 1 {
			return domain.HostingObservationAck{}, &runnerclient.APIError{StatusCode: 503}
		}
		return domain.HostingObservationAck{}, nil
	}}
	c.cycle(t.Context())
	c.cycle(t.Context())
	c.cycle(t.Context())
	if c.cursor != "" || sampled.Load() != 1 || len(reports) != 2 || domain.HostingDocumentHash(reports[0]) != domain.HostingDocumentHash(reports[1]) {
		t.Fatal("cursor, sample count or exact-body retry contract changed")
	}
}
func TestObservationCollectorDiscardsConflictAndBoundsConcurrency(t *testing.T) {
	page := observationPageFixture()
	base := page.Targets[0]
	page.Targets = nil
	for i := 0; i < 4; i++ {
		target := base
		letter := string(rune('a' + i))
		target.BindingID = "binding_" + letter
		target.ResourceID = "res_" + letter
		target.OperationID = "hop_" + letter
		target.ContainerID = strings.Repeat(letter, 64)
		target.SampleID = "hos_" + strings.Repeat(letter, 32)
		page.Targets = append(page.Targets, target)
	}
	page.NextCursor = "binding_d"
	var active, maximum, reports atomic.Int32
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan struct{})
	c := hostingObservationCollector{runnerID: "runner_a", client: observationClientFixture{targets: func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
		return page, nil
	}, report: func(context.Context, domain.HostingRuntimeObservation) (domain.HostingObservationAck, error) {
		reports.Add(1)
		return domain.HostingObservationAck{}, &runnerclient.APIError{StatusCode: 409}
	}}, sampler: observationSampleFunc(func(ctx context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
		return unknownObservation(t, "inspect_failed")
	})}
	go func() { c.cycle(t.Context()); close(done) }()
	defer close(release)
	for i := 0; i < 4; i++ {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("bounded sampler did not start")
		}
	}
	if maximum.Load() != 4 {
		t.Fatal("sample concurrency not bounded at four")
	}
	release <- struct{}{}
	release <- struct{}{}
	release <- struct{}{}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cycle did not join")
	}
	if reports.Load() != 4 {
		t.Fatal("conflicting evidence retried")
	}
}
func TestObservationCollectorCancelledSampleJoins(t *testing.T) {
	started, joined := make(chan struct{}), make(chan struct{})
	page := observationPageFixture()
	settings := &HostingObservationConfig{TrustedLoopbackHTTP: true, client: observationClientFixture{targets: func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
		return page, nil
	}}, sampler: observationSampleFunc(func(ctx context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
		close(started)
		<-ctx.Done()
		close(joined)
		return unknownObservation(t, "inspect_failed")
	})}
	stop, err := startHostingObservation(t.Context(), Config{RunnerID: "runner_a", HostingObservation: settings})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	stop()
	select {
	case <-joined:
	default:
		t.Fatal("collector stop did not join sample")
	}
	stop()
}
func TestObservationReplayFailureStartsNoCollector(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plan := imageTestPlan(t)
	authority := runner.AttemptIdentity{RunID: "run_observe_replay", LeaseID: "lease", Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	_, err = journal.AppendHosting(runner.JournalHosting{ID: "intent", Attempt: authority, OperationID: "hop_replay", Phase: "apply_intent", Plan: &plan, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_a", "capabilities": []string{domain.HostingObservationCapability}})
		case "/api/v1/runners/hosting/status":
			_ = json.NewEncoder(w).Encode(domain.HostingOperation{ID: "hop_replay", Plan: &plan, MutationHold: true})
		default:
			t.Error("unexpected replay traffic", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	var targets atomic.Int32
	settings := &HostingObservationConfig{TrustedLoopbackHTTP: true, client: observationClientFixture{targets: func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
		targets.Add(1)
		return domain.HostingObservationTargets{}, errors.New("unexpected collector")
	}}, sampler: observationSampleFunc(func(context.Context, domain.HostingObservationTarget) domain.HostingRuntimeObservation {
		t.Error("unexpected sample")
		return domain.HostingRuntimeObservation{}
	})}
	if err = RunContext(t.Context(), Config{Server: server.URL, Token: "token", RunnerID: "runner_a", Execute: true, WorkDir: t.TempDir(), JournalDir: journalPath, HostingObservation: settings}); err == nil || targets.Load() != 0 {
		t.Fatal("failed replay started collector")
	}
}

func TestObservationRuntimeErrorCancelsAndJoinsCollector(t *testing.T) {
	started, joined := make(chan struct{}), make(chan struct{})
	page := observationPageFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_a", "capabilities": []string{domain.HostingObservationCapability}})
		case "/api/v1/runners/heartbeat":
			select {
			case <-started:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Error("unexpected runtime request", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	settings := &HostingObservationConfig{TrustedLoopbackHTTP: true, client: observationClientFixture{targets: func(context.Context, domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
		return page, nil
	}}, sampler: observationSampleFunc(func(ctx context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
		close(started)
		<-ctx.Done()
		close(joined)
		return unknownObservation(t, "inspect_failed")
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := RunContext(ctx, Config{Server: server.URL, Token: "token", RunnerID: "runner_a", Execute: true, WorkDir: t.TempDir(), JournalDir: filepath.Join(t.TempDir(), "journal"), HostingObservation: settings}); err == nil {
		t.Fatal("heartbeat error lost")
	}
	select {
	case <-joined:
	default:
		t.Fatal("runtime returned before collector joined")
	}
}
