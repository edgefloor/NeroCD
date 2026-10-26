//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type connectedExecutionFixture struct {
	t                                       *testing.T
	mu                                      sync.Mutex
	j                                       *runner.AttemptJournal
	dir                                     string
	op                                      domain.HostingOperation
	a                                       domain.HostingAuthority
	lease                                   domain.RunLease
	observationLease                        *domain.RunLease
	stage                                   *domain.HostingApplicationDatabaseStage
	retirementPlan                          *domain.HostingDatabaseBindingEndpointPlan
	canonicalRetirement                     *domain.HostingDatabaseBindingEndpointReceipt
	retirementStateFault                    string
	retirementEchoes, retirementRejects     int
	retirement                              *domain.HostingDatabaseBindingEndpointReceipt
	deny, lose                              string
	failEnvironment                         bool
	delivery                                string
	grants, complete, generic, observations int
	stages                                  []string
	requests                                []string
}

func newConnectedExecutionFixture(t *testing.T, kind string, replacement bool) *connectedExecutionFixture {
	t.Helper()
	f, _ := applicationDatabaseFixture(t, kind, false)
	in := f.in.Proof
	p := f.in.Plan
	if replacement {
		c := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: in.OperationID, PlanHash: p.Hash, CreateNonce: in.CreateNonce, ContainerID: strings.Repeat("8", 64), ImageID: p.Release.ImageID, DaemonID: p.Release.DaemonID, DefaultNetworkID: strings.Repeat("9", 64)}
		c.Hash = c.DocumentHash()
		ep, err := domain.CompileHostingApplicationDatabaseAttachPlan(p, in, c)
		if err != nil {
			t.Fatal(err)
		}
		r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "old_attached", OperationID: in.OperationID, PlanHash: ep.Hash, BindingHash: in.Binding.Hash, MembershipRevision: ep.MembershipRevision, Phase: "attached", Observation: &domain.HostingDatabaseBindingEndpointObservation{Present: true, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: ep.Target, EndpointID: strings.Repeat("7", 64)}}}
		r.Hash = r.DocumentHash()
		h := domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: in.OperationID, ContainerID: c.ContainerID, PlanHash: p.Hash}
		in.Members = []domain.HostingDatabaseNetworkMemberProof{{Binding: in.Binding, EndpointPlan: ep, Receipt: r, ApplicationHealthy: h, DatabasePlan: in.BindingDatabasePlan, ApplicationProjectID: in.ApplicationProjectID, DatabaseProjectID: in.DatabaseProjectID}}
		in.OperationID, in.CreateNonce, in.MembershipRevision = "application_replace", strings.Repeat("b", 32), ep.MembershipRevision
		s := p.Snapshot
		s.HostPort++
		p, err = domain.CompileHostingApplicationDatabasePlan(s, p.Release, &h, nil, in)
		if err != nil {
			t.Fatal(err)
		}
	}
	proof, err := domain.NewHostingApplicationDatabaseExecutionProof(in)
	if err != nil {
		t.Fatal(err)
	}
	j, dir := openStorageCoordinatorJournal(t)
	lease := domain.RunLease{ID: "connected_lease", RunID: "run_" + in.OperationID, RunnerID: p.Snapshot.RunnerID, Attempt: 1, Fence: "connected_fence", Status: domain.LeaseActive, ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now().UTC()}
	a := hostingAuthority(lease.RunID, in.OperationID, lease)
	op := domain.HostingOperation{ID: in.OperationID, RunID: a.RunID, ProjectID: in.ApplicationProjectID, ResourceID: p.Snapshot.ResourceID, BindingID: p.Snapshot.BindingID, Snapshot: p.Snapshot, Release: &p.Release, Plan: &p, ApplicationDatabase: &proof, State: "prepared", TaskMode: "deploy"}
	if domain.ValidateHostingApplicationDatabaseOperation(op) != nil {
		t.Fatal("invalid connected coordinator fixture")
	}
	return &connectedExecutionFixture{t: t, j: j, dir: dir, op: op, a: a, lease: lease}
}

func (f *connectedExecutionFixture) has(phase string) bool {
	for _, e := range f.j.Snapshot().Hosting {
		if storageEntryAuthority(e) == f.a && e.Phase == phase {
			return true
		}
	}
	return false
}

func (f *connectedExecutionFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path)
	write := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			f.t.Error(err)
		}
	}
	decode := func(v any) {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			f.t.Error(err)
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/")
	switch path {
	case "heartbeat":
		write(domain.Runner{ID: f.lease.RunnerID})
	case "lease":
		if f.observationLease != nil && r.URL.Query().Get("lease_id") == f.observationLease.ID {
			write(*f.observationLease)
		} else {
			write(f.lease)
		}
	case "renew":
		var req struct {
			LeaseID string `json:"lease_id"`
		}
		decode(&req)
		if f.observationLease != nil && req.LeaseID == f.observationLease.ID {
			write(*f.observationLease)
		} else {
			write(f.lease)
		}
	case "hosting/status":
		var req domain.HostingStatusRequest
		decode(&req)
		if req.Phase == "claim_observation" {
			if req.HostingAuthority != f.a || f.observationLease == nil || !f.op.ReconcileRequested {
				f.t.Error("observer claim changed original authority")
			}
			f.op.TaskMode = "observe"
			f.op.ReconciliationRunID = f.observationLease.RunID
			f.op.ObservationLease = f.observationLease
		}
		write(f.op)
	case "hosting/plan":
		write(f.op)
	case "hosting/runtime-environment":
		var req domain.HostingRuntimeEnvironmentRequest
		decode(&req)
		if !f.has("apply_intent") || req.Authority != f.a || req.PlanHash != f.op.Plan.Hash || f.op.State != "prepared" || f.delivery != "" && f.delivery != req.DeliveryID {
			f.t.Error("secret delivery escaped original durable prepared intent")
		}
		f.delivery = req.DeliveryID
		if f.failEnvironment {
			w.WriteHeader(409)
			return
		}
		env, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: req.Authority, DeliveryID: req.DeliveryID, PlanHash: req.PlanHash, RuntimeConfiguration: req.RuntimeConfiguration, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash(f.lease.RunnerID, req), Purpose: hostingruntimeprotocol.Purpose, UseUntil: time.Now().Add(20 * time.Second)}, []hostingruntimeprotocol.Entry{{Name: "APP_SECRET", Value: []byte("connected-secret-canary")}})
		if err != nil {
			f.t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer env.Close()
		w.Header().Set("Cache-Control", "no-store")
		if err = hostingruntimeprotocol.WriteEnvironment(w, env); err != nil {
			f.t.Error(err)
		}
	case "hosting/apply-begin":
		if !f.has("apply_intent") {
			f.t.Error("admission before durable create intent")
		}
		f.op.State, f.op.MutationHold = "applying", true
		write(f.op)
	case "hosting/database-binding/state":
		state := domain.HostingApplicationDatabaseExecutionState{OperationID: f.op.ID, PlanHash: f.op.Plan.Hash, Stage: f.stage, RetirementPlan: f.retirementPlan, RetirementReceipt: f.retirement, MembershipRevision: f.op.ApplicationDatabase.MembershipRevision, HoldOperationID: f.op.ID}
		if state.RetirementPlan != nil {
			f.corruptRetirementState(&state)
		}
		write(state)
	case "hosting/database-binding/authorize":
		var req domain.HostingApplicationDatabaseEffectRequest
		decode(&req)
		hash := ""
		if f.stage != nil {
			hash = f.stage.Hash
		}
		if req.HostingAuthority != f.a || req.ExpectedStageHash != hash || !f.has("apply_intent") && (f.op.TaskMode != "finalize" || req.Action != "retire" || !f.has("application_finalize")) {
			f.t.Error("effect escaped exact current stage/authority")
		}
		if req.Action == f.deny {
			w.WriteHeader(403)
			return
		}
		f.grants++
		write(domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: req.AuthorizationID, OperationID: f.op.ID, PlanHash: f.op.Plan.Hash, Action: req.Action, StageHash: hash, LeaseID: f.a.LeaseID, Attempt: f.a.Attempt, Fence: f.a.Fence, MembershipRevision: f.op.ApplicationDatabase.MembershipRevision, UseUntil: time.Now().Add(10 * time.Second)})
	case "hosting/database-binding/stage":
		var req domain.HostingApplicationDatabaseStageRequest
		decode(&req)
		if req.HostingAuthority != f.a || !f.has("application_candidate") || !f.has("application_stage") {
			f.t.Error("publication escaped durable actual candidate and stage")
		}
		in, _ := domain.HostingApplicationDatabaseExecutionPlanInput(*f.op.ApplicationDatabase)
		if f.stage == nil || f.stage.Hash != req.Stage.Hash {
			// Earlier saved ACKs may be replayed after a later stage was committed.
			known := false
			for _, e := range f.j.Snapshot().Hosting {
				if e.ApplicationStage != nil && e.ApplicationStage.Hash == req.Stage.Hash {
					known = true
				}
			}
			if !known {
				f.t.Error("unknown stage")
			}
			if req.Stage.ValidateTransition(f.stage, *f.op.Plan, in) == nil {
				copy := req.Stage
				f.stage = &copy
				f.stages = append(f.stages, copy.Phase)
			}
		}
		if req.Stage.Phase == f.lose {
			w.WriteHeader(409)
			return
		}
		write(f.op)
	case "hosting/database-binding/retirement-plan":
		in, _ := domain.HostingApplicationDatabaseExecutionPlanInput(*f.op.ApplicationDatabase)
		m := in.Members[0]
		p := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: f.op.ID, BindingHash: in.Binding.Hash, MembershipRevision: in.MembershipRevision + 2, Action: "retire", Target: m.EndpointPlan.Target, ExpectedEndpointID: m.Receipt.Observation.Endpoint.EndpointID}
		p.Hash = p.DocumentHash()
		if f.retirementPlan == nil {
			f.retirementPlan = &p
			intent := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "controller_owned_retirement_intent", OperationID: f.op.ID, PlanHash: p.Hash, BindingHash: in.Binding.Hash, MembershipRevision: p.MembershipRevision, Phase: "retire_intent", Intent: &m.Receipt.Observation.Endpoint}
			intent.Hash = intent.DocumentHash()
			f.canonicalRetirement = &intent
			f.retirement = &intent
		}
		write(*f.retirementPlan)
	case "hosting/database-binding/retirement":
		var req domain.HostingApplicationDatabaseRetirementRequest
		decode(&req)
		if !f.has("application_retirement") || req.PlanHash != f.op.Plan.Hash || req.Receipt.PlanHash != f.retirementPlan.Hash {
			f.t.Error("retirement publication escaped durable exact separate plan identities")
		}
		if req.Receipt.Phase == "retire_intent" && (f.canonicalRetirement == nil || domain.HostingDocumentHash(req.Receipt) != domain.HostingDocumentHash(*f.canonicalRetirement) || f.retirement.Phase == "detached") {
			f.retirementRejects++
			w.WriteHeader(http.StatusConflict)
			return
		}
		if req.Receipt.Phase == "retire_intent" {
			f.retirementEchoes++
		}
		if req.Receipt.ValidateTransition(f.retirement, *f.retirementPlan, f.op.ApplicationDatabase.Binding) != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		copy := req.Receipt
		f.retirement = &copy
		if req.Receipt.Phase == f.lose {
			w.WriteHeader(409)
			return
		}
		write(f.op)
	case "hosting/complete":
		f.complete++
		var req domain.HostingCompleteRequest
		decode(&req)
		if !f.has("result") || req.HostingAuthority != f.a || f.stage == nil || f.stage.Phase != "ready" {
			f.t.Error("completion escaped durable ready result")
		}
		if f.op.Plan.PreviousHealthy != nil && (f.retirement == nil || f.retirement.Phase != "detached") {
			f.t.Error("replacement completed before exact retirement")
		}
		f.op.Result = &req.Result
		f.op.State = "succeeded"
		f.op.MutationHold = false
		f.op.CompleteReceipt = &domain.HostingReceipt{ID: req.ReceiptID, BodyHash: domain.HostingDocumentHash(req.Result), CreatedAt: time.Now().UTC()}
		if f.lose == "complete" {
			w.WriteHeader(409)
			return
		}
		write(f.op)
	case "hosting/reconciliation":
		var req domain.HostingObservation
		decode(&req)
		if req.OperationID != f.op.ID || req.Ready && req.NoOwnedContainers {
			f.t.Error("invalid observation")
		}
		f.observations++
		write(f.op)
	case "complete":
		f.generic++
		f.t.Error("connected path used generic Complete")
		w.WriteHeader(500)
	default:
		f.t.Error("unexpected connected request", path)
		w.WriteHeader(404)
	}
}

type connectedExecutionEngine struct {
	f                                                 *connectedExecutionFixture
	creates, configure, starts, retirements, observes int
	phase                                             applicationDatabaseRecoveryPhase
	unknown                                           bool
	loseEffect                                        string
	observeErr                                        error
	beforeEffect                                      func(string)
}

func (e *connectedExecutionEngine) Close() error { return nil }
func (e *connectedExecutionEngine) candidate(in applicationDatabaseEngineInput) applicationDatabaseCandidate {
	return applicationDatabaseCandidate{OperationID: in.Proof.OperationID, PlanHash: in.Plan.Hash, CreateNonce: in.Proof.CreateNonce, ContainerID: strings.Repeat("2", 64), ImageID: in.Plan.Release.ImageID, DaemonID: in.Plan.Release.DaemonID, DefaultNetworkID: strings.Repeat("3", 64)}
}
func (e *connectedExecutionEngine) effect(ctx context.Context, action string, before func(context.Context) error) error {
	if e.beforeEffect != nil {
		e.beforeEffect(action)
	}
	return before(ctx)
}
func (e *connectedExecutionEngine) CreateStopped(ctx context.Context, in applicationDatabaseEngineInput, env *hostingruntimeprotocol.Environment, deadline time.Time, before func(context.Context) error) (applicationDatabaseCandidate, error) {
	if !e.f.has("apply_intent") || e.f.has("application_candidate") || time.Until(deadline) > time.Minute {
		e.f.t.Error("create escaped intent/window")
	}
	if err := e.effect(ctx, "create", before); err != nil {
		return applicationDatabaseCandidate{}, err
	}
	e.creates++
	e.phase = applicationDatabaseRecoveryCreated
	c := e.candidate(in)
	if e.unknown {
		c.ContainerID = ""
		return c, errApplicationDatabaseCreateUncertain
	}
	if e.loseEffect == "create_known" {
		return c, errApplicationDatabaseCreateUncertain
	}
	return c, nil
}
func (e *connectedExecutionEngine) ConfigureStoppedNetwork(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan, before func(context.Context) error) (applicationDatabaseConfiguration, error) {
	if err := e.effect(ctx, "configure", before); err != nil {
		return applicationDatabaseConfiguration{}, err
	}
	e.configure++
	e.phase = applicationDatabaseRecoveryConfigured
	if e.loseEffect == "configure" {
		return applicationDatabaseConfiguration{}, errors.New("configuration outcome interrupted")
	}
	return applicationDatabaseExpectedConfiguration(in, c, p), nil
}
func (e *connectedExecutionEngine) endpoint(p domain.HostingDatabaseBindingEndpointPlan) domain.HostingDatabaseBindingEndpointObservation {
	return domain.HostingDatabaseBindingEndpointObservation{Present: true, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: strings.Repeat("4", 64)}}
}
func (e *connectedExecutionEngine) StartAndObserve(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan, configured applicationDatabaseConfiguration, before func(context.Context) error) (domain.HostingDatabaseBindingEndpointObservation, error) {
	if configured != applicationDatabaseExpectedConfiguration(in, c, p) {
		return domain.HostingDatabaseBindingEndpointObservation{}, errors.New("invalid config")
	}
	if err := e.effect(ctx, "start", before); err != nil {
		return domain.HostingDatabaseBindingEndpointObservation{}, err
	}
	e.starts++
	e.phase = applicationDatabaseRecoveryReady
	if e.loseEffect == "start" {
		return domain.HostingDatabaseBindingEndpointObservation{}, errors.New("start outcome interrupted")
	}
	return e.endpoint(p), nil
}
func (e *connectedExecutionEngine) VerifyReady(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, domain.HostingDatabaseBindingEndpointObservation) error {
	return nil
}
func (e *connectedExecutionEngine) Retire(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, o domain.HostingDatabaseBindingEndpointObservation, p domain.HostingDatabaseBindingEndpointPlan, before func(context.Context) error) (applicationDatabaseRetirement, error) {
	known := false
	for _, entry := range e.f.j.Snapshot().Hosting {
		if entry.ApplicationRetirement != nil && e.f.canonicalRetirement != nil && domain.HostingDocumentHash(entry.ApplicationRetirement) == domain.HostingDocumentHash(e.f.canonicalRetirement) {
			known = true
		}
	}
	if !known || e.f.retirementEchoes == 0 {
		e.f.t.Error("retirement effect escaped durable canonical intent echo")
	}
	if err := e.effect(ctx, "retire", before); err != nil {
		return applicationDatabaseRetirement{}, err
	}
	e.retirements++
	return applicationDatabaseRetirement{ContainerID: p.Target.ContainerID, ContainerRemoved: true, Endpoint: domain.HostingDatabaseBindingEndpointObservation{Endpoint: domain.HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: p.ExpectedEndpointID}}}, nil
}
func (e *connectedExecutionEngine) ObserveCandidate(_ context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p applicationDatabaseRecoveryEvidence) (applicationDatabaseRecoveryObservation, error) {
	e.observes++
	if e.observeErr != nil {
		return applicationDatabaseRecoveryObservation{}, e.observeErr
	}
	return applicationDatabaseRecoveryObservation{Candidate: c, Phase: e.phase, Endpoint: e.endpoint(p.AttachPlan)}, nil
}

func (f *connectedExecutionFixture) run(t *testing.T, e *connectedExecutionEngine, server string) error {
	t.Helper()
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, f.lease)
	defer s.Close()
	return executeApplicationDatabasePrepared(s, f.j, func(context.Context) error { return nil }, runnerclient.New(server, "connected_token"), e, f.a, f.lease, f.op, RuntimeEnvironmentConfig{TrustedLoopbackHTTP: true})
}
func (f *connectedExecutionFixture) config(server string) Config {
	return Config{Server: server, Token: "connected_token", RunnerID: f.lease.RunnerID, RuntimeEnvironment: &RuntimeEnvironmentConfig{TrustedLoopbackHTTP: true}, Database: &DatabaseExecutionConfig{ApplicationDatabase: true, TrustedLoopbackHTTP: true}}
}

func TestConnectedApplicationInitialAndReplacement(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, replacement := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/initial", true: "/replacement"}[replacement], func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, replacement)
				e := &connectedExecutionEngine{f: f}
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err != nil {
					t.Fatal(err)
				}
				if e.creates != 1 || e.configure != 1 || e.starts != 1 || e.retirements != map[bool]int{false: 0, true: 1}[replacement] || f.grants != 3+e.retirements || f.complete != 1 || f.generic != 0 || len(f.j.Snapshot().Hosting) != 0 || strings.Join(f.stages, ",") != "candidate_created,attach_intent,network_configured,attached,ready" {
					t.Fatalf("bad connected execution creates=%d stages=%v grants=%d", e.creates, f.stages, f.grants)
				}
			})
		}
	}
}

func TestConnectedApplicationLostACKReplaysBeforeDocker(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			f := newConnectedExecutionFixture(t, kind, true)
			e := &connectedExecutionEngine{f: f}
			f.lose = "complete"
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			if err := f.run(t, e, server.URL); err == nil {
				t.Fatal("lost completion acknowledged")
			}
			if err := f.j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := runner.OpenAttemptJournal(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			f.j = j
			f.lose = ""
			opens := 0
			err = reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), j, func() (applicationDatabaseExecutionEngine, error) {
				opens++
				return nil, errors.New("Docker unavailable")
			})
			if err != nil || opens != 0 || f.complete != 2 || len(j.Snapshot().Hosting) != 0 || e.creates != 1 {
				t.Fatal("exact completion replay touched Docker or failed settlement", err)
			}
		})
	}
}

func TestConnectedApplicationStageResponseLossRetainsExactCandidate(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, phase := range []string{"candidate_created", "attach_intent", "network_configured", "attached", "ready"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, false)
				e := &connectedExecutionEngine{f: f}
				f.lose = phase
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err == nil {
					t.Fatal("lost stage ACK ignored")
				}
				original := f.delivery
				f.lose = ""
				if err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { return e, nil }); err != nil {
					t.Fatal(err)
				}
				if e.creates != 1 || e.configure != 1 || e.starts != 1 || f.delivery != original || len(f.j.Snapshot().Hosting) != 0 {
					t.Fatal("replay repeated effect, refreshed delivery or erased hold")
				}
			})
		}
	}
}

func TestConnectedApplicationUnknownCreateAndDeniedEffectsHold(t *testing.T) {
	for _, mode := range []string{"unknown", "create", "configure", "start", "retire"} {
		t.Run(mode, func(t *testing.T) {
			f := newConnectedExecutionFixture(t, "redis", true)
			e := &connectedExecutionEngine{f: f, unknown: mode == "unknown"}
			if mode != "unknown" {
				f.deny = mode
			}
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			if err := f.run(t, e, server.URL); err == nil || len(f.j.Snapshot().Hosting) == 0 || f.complete != 0 {
				t.Fatal("uncertain/denied mutation settled")
			}
			if mode == "unknown" {
				opens := 0
				before := e.creates
				if err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { opens++; return e, nil }); err == nil || opens != 0 || e.creates != before {
					t.Fatal("unknown create discovered or repeated")
				}
			}
			raw, err := os.ReadFile(f.dir + "/journal.json")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "connected-secret-canary") {
				t.Fatal("journal retained runtime secret")
			}
		})
	}
}

func TestConnectedApplicationRetirementResponseLossDoesNotRepeatEffect(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, phase := range []string{"retire_intent", "detached", "complete"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, true)
				e := &connectedExecutionEngine{f: f}
				f.lose = phase
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err == nil {
					t.Fatal("lost retirement/completion ACK ignored")
				}
				beforeRetirements, beforeEchoes := e.retirements, f.retirementEchoes
				if phase != "retire_intent" && (beforeRetirements != 1 || !f.has("application_retirement")) {
					t.Fatal("missing durable detached effect")
				}
				f.lose = ""
				opens := 0
				if err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { opens++; return e, nil }); err != nil {
					t.Fatal(err)
				}
				if e.retirements != 1 || e.creates != 1 || f.retirementRejects != 0 || len(f.j.Snapshot().Hosting) != 0 {
					t.Fatal("retirement replay repeated effect or lost chain")
				}
				if phase != "retire_intent" && (f.retirementEchoes != beforeEchoes || e.retirements != beforeRetirements) {
					t.Fatal("detached replay transmitted older intent or repeated effect")
				}
				if phase == "complete" && opens != 0 {
					t.Fatal("saved completion opened effect engine")
				}
			})
		}
	}
}

func TestConnectedApplicationPreparationRechecksPermissionBeforePull(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		calls, checks := 0, 0
		c := applicationDatabasePrepareCommand{command: imageCommandFunc(func(context.Context, string, []string, string, []string) ([]byte, error) { calls++; return nil, nil }), before: func(context.Context) error {
			checks++
			if !allowed {
				return errors.New("revoked")
			}
			return nil
		}}
		_, err := c.RunEnvironment(t.Context(), "docker", []string{"--host", "unix:///test", "--config", "/config", "image", "pull", "nginx:1"}, "/config", nil)
		if checks != 1 || calls != map[bool]int{false: 0, true: 1}[allowed] || (err == nil) != allowed {
			t.Fatal("pull bypassed current permission")
		}
	}
}

func TestConnectedApplicationStartupClaimsFreshObserverWithoutResumingOriginal(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, failedInspection := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/observed", true: "/inspection-failed"}[failedInspection], func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, false)
				e := &connectedExecutionEngine{f: f}
				f.lose = "ready"
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err == nil {
					t.Fatal("missing retained stage")
				}
				f.lose = ""
				f.op.ReconcileRequested = true
				f.lease.ExpiresAt = time.Now().Add(-time.Minute)
				lease := f.lease
				lease.ID, lease.Fence, lease.RunID = "observer_lease", "observer_fence", "observe_"+f.op.ID
				lease.ExpiresAt = time.Now().Add(time.Minute)
				f.observationLease = &lease
				if failedInspection {
					e.observeErr = errors.New("inspection private canary")
				}
				before := f.grants
				original := f.j.Snapshot().Hosting
				err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { return e, nil })
				if err == nil || !failedInspection && !errors.Is(err, domain.ErrHostingDatabaseRecoveryObserveOnly) || f.grants != before || e.creates != 1 || f.complete != 0 || !f.op.MutationHold || domain.HostingDocumentHash(original) != domain.HostingDocumentHash(f.j.Snapshot().Hosting) {
					t.Fatal("observer resumed or retired original authority", err)
				}
				if f.observations != map[bool]int{false: 1, true: 0}[failedInspection] {
					t.Fatal("failed inspection became absence/receipt")
				}
			})
		}
	}
}

func TestConnectedApplicationKnownEffectOutcomeIsObservedWithoutRepeating(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, effect := range []string{"create_known", "configure", "start"} {
			t.Run(kind+"/"+effect, func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, false)
				e := &connectedExecutionEngine{f: f, loseEffect: effect}
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err == nil {
					t.Fatal("interrupted physical outcome ignored")
				}
				e.loseEffect = ""
				if err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { return e, nil }); err != nil {
					t.Fatal(err)
				}
				if e.creates != 1 || e.configure != 1 || e.starts != 1 || e.observes != 1 || len(f.j.Snapshot().Hosting) != 0 {
					t.Fatal("known candidate effect repeated instead of inspected")
				}
			})
		}
	}
}

func TestConnectedApplicationLegacyRejectsSidecarBeforePrepare(t *testing.T) {
	f := newConnectedExecutionFixture(t, "postgresql", false)
	f.op.Plan, f.op.Release = nil, nil
	f.op.State = "queued"
	if validateHostingPlanResponse(f.op, f.a, f.lease.RunnerID, "deploy") == nil {
		t.Fatal("legacy path admitted queued connected sidecar without plan")
	}
	for _, ref := range []bool{false, true} {
		op := f.op
		if ref {
			op.ApplicationDatabase = nil
		}
		if validateHostingPlanResponse(op, f.a, f.lease.RunnerID, "deploy") == nil {
			t.Fatal("connected family reached generic image preparation")
		}
	}
}

func TestConnectedApplicationCancellationJoinsWorkersAndStopsEffects(t *testing.T) {
	f := newConnectedExecutionFixture(t, "redis", false)
	e := &connectedExecutionEngine{f: f}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, f.lease)
	renewer := startLeaseRenewer(s.Context(), s, server.URL, "fixture", f.lease, time.Second, exactBuildLease(f.lease))
	watcher := startLeaseWatcher(s.Context(), server.URL, "fixture", f.lease, time.Second, s.cancel, exactBuildLease(f.lease))
	reporter := startAttemptReporter(s.Context(), s, f.j, server.URL, "fixture", f.a.RunID, f.lease)
	t.Cleanup(func() { reporter.Stop(); watcher.Stop(); renewer.Stop(); s.Close() })
	e.beforeEffect = func(action string) {
		if action == "start" {
			s.cancel()
		}
	}
	err := executeApplicationDatabasePrepared(s, f.j, databaseCompletionDrain(s, reporter, watcher, renewer), runnerclient.New(server.URL, "fixture"), e, f.a, f.lease, f.op, RuntimeEnvironmentConfig{TrustedLoopbackHTTP: true})
	if err == nil || e.starts != 0 || f.complete != 0 || !f.has("apply_intent") {
		t.Fatal("cancellation escaped effect boundary")
	}
	reporter.Stop()
	watcher.Stop()
	renewer.Stop()
	s.Close()
	for _, done := range []<-chan struct{}{reporter.done, watcher.done, renewer.done, s.done} {
		select {
		case <-done:
		default:
			t.Fatal("owned worker outlived attempt")
		}
	}
}

func TestConnectedApplicationObserverCannotActivateOrMutate(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, phase := range []applicationDatabaseRecoveryPhase{applicationDatabaseRecoveryReady, applicationDatabaseRecoveryStarted, applicationDatabaseRecoveryAbsent} {
			t.Run(kind+"/"+string(phase), func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, false)
				e := &connectedExecutionEngine{f: f}
				f.lose = "ready"
				server := httptest.NewServer(http.HandlerFunc(f.serve))
				defer server.Close()
				if err := f.run(t, e, server.URL); err == nil {
					t.Fatal("ready ACK loss ignored")
				}
				before := f.grants
				f.op.TaskMode = "observe"
				f.op.ReconciliationRunID = "observe_" + f.op.ID
				lease := f.lease
				lease.ID, lease.RunID, lease.Fence = "observation_lease", f.op.ReconciliationRunID, "observation_fence"
				a := hostingAuthority(lease.RunID, f.op.ID, lease)
				ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
				s := newAttemptSupervisorFrom(ctx, lease)
				defer s.Close()
				e.phase = phase
				if err := observeApplicationDatabaseClaim(s, runnerclient.New(server.URL, "fixture"), e, a, f.op); err != nil {
					t.Fatal(err)
				}
				if f.observations != 1 || f.grants != before || f.complete != 0 || !f.op.MutationHold || f.stage.Phase != "ready" || len(f.j.Snapshot().Hosting) == 0 {
					t.Fatal("read-only observation settled or mutated original operation")
				}
			})
		}
	}
}

func TestConnectedApplicationExpiredOriginalAuthorityNeverResumesEffects(t *testing.T) {
	f := newConnectedExecutionFixture(t, "redis", false)
	e := &connectedExecutionEngine{f: f}
	f.lose = "candidate_created"
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	if err := f.run(t, e, server.URL); err == nil {
		t.Fatal("ACK loss ignored")
	}
	f.lose = ""
	f.lease.ExpiresAt = time.Now().Add(-time.Second)
	opens := 0
	err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { opens++; return e, nil })
	if err == nil || opens != 0 || e.creates != 1 || e.configure != 0 || len(f.j.Snapshot().Hosting) == 0 {
		t.Fatal("expired original authority resumed mutation")
	}
}

func TestConnectedApplicationPreparedReplayRetainsOriginalDelivery(t *testing.T) {
	f := newConnectedExecutionFixture(t, "postgresql", false)
	e := &connectedExecutionEngine{f: f}
	f.failEnvironment = true
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	if err := f.run(t, e, server.URL); err == nil || e.creates != 0 {
		t.Fatal("delivery failure reached create")
	}
	original := f.delivery
	f.failEnvironment = false
	if err := reconcileApplicationDatabaseWithEngine(t.Context(), f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) { return e, nil }); err != nil {
		t.Fatal(err)
	}
	if f.delivery != original || original == "" || e.creates != 1 || len(f.j.Snapshot().Hosting) != 0 {
		t.Fatal("prepared restart changed original delivery or duplicated create")
	}
}

func TestConnectedApplicationQueuedOptOutNeverPreparesImage(t *testing.T) {
	f := newConnectedExecutionFixture(t, "postgresql", false)
	f.op.State = "queued"
	f.op.Plan, f.op.Release = nil, nil
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: f.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: f.op.ID, HostingMode: "deploy"}}, Lease: f.lease}
	claim.PrimitivePlan.RunID = claim.Run.ID
	if err := executeClaimWithOptionsContext(t.Context(), server.URL, "fixture", claim, t.TempDir(), time.Second, f.j, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{}); err == nil {
		t.Fatal("opt-out admitted connected sidecar")
	}
	for _, path := range f.requests {
		if path != "/api/v1/runners/renew" && path != "/api/v1/runners/hosting/plan" {
			t.Fatal("opt-out reached preparation or effect", path)
		}
	}
	if len(f.j.Snapshot().Hosting) != 0 || f.grants != 0 || f.complete != 0 || f.generic != 0 {
		t.Fatal("opt-out mutated connected family")
	}
}

func TestConnectedApplicationCanonicalObservationTaskReachesDedicatedDispatch(t *testing.T) {
	f := newConnectedExecutionFixture(t, "redis", false)
	e := &connectedExecutionEngine{f: f}
	f.lose = "ready"
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	if err := f.run(t, e, server.URL); err == nil {
		t.Fatal("ready ACK loss ignored")
	}
	f.op.TaskMode = "observe"
	f.op.ReconciliationRunID = "observe_" + f.op.ID
	lease := f.lease
	lease.ID, lease.RunID, lease.Fence = "observation_lease", f.op.ReconciliationRunID, "observation_fence"
	f.lease = lease
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: lease.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: f.op.ID, HostingMode: "observe"}}, Lease: lease}
	claim.PrimitivePlan.RunID = claim.Run.ID
	if err := domain.ValidateImageDeploymentTask(claim.Run); err != nil {
		t.Fatal(err)
	}
	before := len(f.requests)
	config := &DatabaseExecutionConfig{ApplicationDatabase: true, TrustedLoopbackHTTP: true, StateRoot: t.TempDir(), SecretRoot: t.TempDir()}
	// The native host is intentionally unavailable. Canonical task dispatch must
	// reach the connected family and fail before its protected engine is opened.
	err := executeClaimWithOptionsContext(t.Context(), server.URL, "fixture", claim, t.TempDir(), time.Second, f.j, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{database: config, runtimeEnvironment: &RuntimeEnvironmentConfig{TrustedLoopbackHTTP: true}})
	if err == nil {
		t.Fatal("unqualified host observed candidate")
	}
	sawPlan := false
	for _, path := range f.requests[before:] {
		if path == "/api/v1/runners/hosting/plan" {
			sawPlan = true
		}
		if strings.Contains(path, "authorize") || path == "/api/v1/runners/complete" || path == "/api/v1/runners/hosting/complete" {
			t.Fatal("observer reached mutation route")
		}
	}
	if !sawPlan || !errors.Is(err, errApplicationDatabaseExecution) {
		t.Fatal("canonical observation task did not reach dedicated dispatch", err)
	}
}

type connectedMixedCaptureJournal struct {
	databaseNetworkCaptureJournal
	hosting []runner.JournalHosting
}

func (j connectedMixedCaptureJournal) Snapshot() runner.JournalSnapshot {
	out := j.databaseNetworkCaptureJournal.Snapshot()
	out.Hosting = j.hosting
	return out
}

func TestConnectedApplicationStartupACKPrecedesHostAndMixedCaptureHolds(t *testing.T) {
	f := newConnectedExecutionFixture(t, "redis", false)
	e := &connectedExecutionEngine{f: f}
	f.lose = "complete"
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	if err := f.run(t, e, server.URL); err == nil {
		t.Fatal("complete ACK loss ignored")
	}
	f.lose = ""
	opens, preflight, ordinary := 0, 0, 0
	if err := reconcileConnectedBeforeCapture(t.Context(), f.j.Snapshot(), func(ctx context.Context) error {
		return reconcileApplicationDatabaseWithEngine(ctx, f.config(server.URL), f.j, func() (applicationDatabaseExecutionEngine, error) {
			opens++
			return nil, errors.New("host unavailable")
		})
	}); err != nil {
		t.Fatal(err)
	}
	if opens != 0 || preflight != 0 || ordinary != 0 || len(f.j.Snapshot().Hosting) != 0 || f.complete != 2 {
		t.Fatal("completion replay waited for host or ordinary claim")
	}
	other := newConnectedExecutionFixture(t, "redis", false)
	otherEngine := &connectedExecutionEngine{f: other}
	other.lose = "candidate_created"
	otherServer := httptest.NewServer(http.HandlerFunc(other.serve))
	defer otherServer.Close()
	if other.run(t, otherEngine, otherServer.URL) == nil {
		t.Fatal("missing retained app evidence")
	}
	capture := newCaptureExecutionFixture(t, "redis")
	capture.client.lostAck = true
	if capture.execute(t.Context()) == nil {
		t.Fatal("missing capture evidence")
	}
	mixed := connectedMixedCaptureJournal{databaseNetworkCaptureJournal: capture.j, hosting: other.j.Snapshot().Hosting}
	replays := 0
	if err := reconcileConnectedBeforeCapture(t.Context(), mixed.Snapshot(), func(context.Context) error { replays++; return nil }); err != nil || replays != 0 {
		t.Fatal("mixed capture bypassed dedicated hold")
	}
	client := &automaticCaptureTestClient{captureExecutionTestClient: capture.client}
	d, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(automaticCaptureConfig(capture), mixed, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("mixed evidence opened observer")
		return nil, nil, nil
	}, func(context.Context) error { preflight++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if d.Reconcile(t.Context()) == nil || replays != 0 || preflight != 0 || len(capture.j.Snapshot().DatabaseNetworkCapture) == 0 || len(other.j.Snapshot().Hosting) == 0 {
		t.Fatal("mixed evidence executed or cleared a family")
	}
}

func TestConnectedApplicationControllerCanonicalRetirementIntent(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			f := newConnectedExecutionFixture(t, kind, true)
			e := &connectedExecutionEngine{f: f}
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			if err := f.run(t, e, server.URL); err != nil {
				t.Fatal("canonical controller intent rejected", err)
			}
			if f.retirementRejects != 0 || f.retirementEchoes != 1 || e.retirements != 1 || f.complete != 1 || len(f.j.Snapshot().Hosting) != 0 {
				t.Fatal("canonical retirement sequence incomplete")
			}
		})
	}
}

func (f *connectedExecutionFixture) corruptRetirementState(state *domain.HostingApplicationDatabaseExecutionState) {
	if f.retirementStateFault == "" {
		return
	}
	p, r := *state.RetirementPlan, *state.RetirementReceipt
	intent := *r.Intent
	r.Intent = &intent
	state.RetirementPlan, state.RetirementReceipt = &p, &r
	switch f.retirementStateFault {
	case "missing_plan":
		state.RetirementPlan = nil
	case "missing_intent":
		state.RetirementReceipt = nil
	case "operation":
		state.OperationID = "foreign_operation"
	case "operation_plan":
		state.PlanHash = "sha256:" + strings.Repeat("b", 64)
	case "revision":
		state.MembershipRevision++
	case "hold":
		state.HoldOperationID = ""
	case "stage":
		stage := *state.Stage
		stage.ReceiptID = "foreign_ready_stage"
		stage.Hash = stage.DocumentHash()
		state.Stage = &stage
	case "plan":
		p.MembershipRevision++
		p.Hash = p.DocumentHash()
		r.MembershipRevision = p.MembershipRevision
		r.PlanHash = p.Hash
	case "member":
		p.Target.ContainerID = strings.Repeat("b", 64)
		p.Hash = p.DocumentHash()
		r.PlanHash = p.Hash
		r.Intent.Target = p.Target
	case "receipt":
		r.BindingHash = "sha256:" + strings.Repeat("b", 64)
	case "receipt_operation":
		r.OperationID = "foreign_operation"
	case "detached":
		r.Phase = "detached"
		r.Intent = nil
		r.Observation = &domain.HostingDatabaseBindingEndpointObservation{Endpoint: intent, Present: false}
	case "uncertain":
		r.Phase = "uncertain"
		r.Intent = nil
		r.FailureCode = "inspection_failed"
	}
	r.Hash = r.DocumentHash()
}

func TestConnectedApplicationCanonicalRetirementStateFailsClosed(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			f := newConnectedExecutionFixture(t, kind, true)
			f.retirementStateFault = "missing_intent"
			e := &connectedExecutionEngine{f: f}
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			// Establish the same durable ready state once; every fault below is
			// confined to the read-only state response before any retirement echo.
			if err := f.run(t, e, server.URL); err == nil {
				t.Fatal("missing canonical intent admitted retirement")
			}
			in, err := applicationDatabaseInput(f.op)
			if err != nil {
				t.Fatal(err)
			}
			ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			supervisor := newAttemptSupervisorFrom(ctx, f.lease)
			defer supervisor.Close()
			x := applicationDatabaseExchange{s: supervisor, j: f.j, c: runnerclient.New(server.URL, "fixture"), a: f.a, lease: f.lease, op: f.op, in: in, e: e, stage: f.stage}
			candidate := e.candidate(in)
			observed := e.endpoint(f.stage.AttachPlan)
			originalJournal := domain.HostingDocumentHash(f.j.Snapshot().Hosting)
			originalState := domain.HostingDocumentHash(struct {
				Operation domain.HostingOperation
				Stage     *domain.HostingApplicationDatabaseStage
				Plan      *domain.HostingDatabaseBindingEndpointPlan
				Receipt   *domain.HostingDatabaseBindingEndpointReceipt
			}{f.op, f.stage, f.retirementPlan, f.retirement})
			for _, fault := range []string{"missing_plan", "missing_intent", "operation", "operation_plan", "revision", "hold", "stage", "plan", "member", "receipt", "receipt_operation", "detached", "uncertain"} {
				t.Run(fault, func(t *testing.T) {
					f.mu.Lock()
					f.retirementStateFault = fault
					f.mu.Unlock()
					defer func() { f.mu.Lock(); f.retirementStateFault = ""; f.mu.Unlock() }()
					if err := x.retire(candidate, observed); err == nil {
						t.Fatal("invalid canonical state admitted retirement")
					}
					if e.retirements != 0 || f.retirementEchoes != 0 || f.complete != 0 || f.grants != 3 || !f.op.MutationHold || domain.HostingDocumentHash(f.j.Snapshot().Hosting) != originalJournal {
						t.Fatal("invalid state escaped effect/hold/journal boundary")
					}
					actualState := domain.HostingDocumentHash(struct {
						Operation domain.HostingOperation
						Stage     *domain.HostingApplicationDatabaseStage
						Plan      *domain.HostingDatabaseBindingEndpointPlan
						Receipt   *domain.HostingDatabaseBindingEndpointReceipt
					}{f.op, f.stage, f.retirementPlan, f.retirement})
					if actualState != originalState {
						t.Fatal("corrupt response changed retained fixture state")
					}
				})
			}
		})
	}
}

func TestConnectedApplicationRetainedRetirementRejectsCorruptChain(t *testing.T) {
	f := newConnectedExecutionFixture(t, "postgresql", true)
	f.lose = "detached"
	e := &connectedExecutionEngine{f: f}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	if f.run(t, e, server.URL) == nil {
		t.Fatal("missing retained detached body")
	}
	original := f.j.Snapshot().Hosting
	for _, fault := range []string{"missing_intent", "authority", "operation_plan", "stage", "plan", "receipt", "uncertain"} {
		t.Run(fault, func(t *testing.T) {
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var entries []runner.JournalHosting
			if json.Unmarshal(raw, &entries) != nil {
				t.Fatal("journal clone")
			}
			for i := range entries {
				entry := &entries[i]
				if entry.ApplicationRetirement == nil {
					continue
				}
				if fault == "missing_intent" && entry.ApplicationRetirement.Phase == "retire_intent" {
					entries = append(entries[:i], entries[i+1:]...)
					break
				}
				if fault == "authority" {
					entry.Attempt.Fence = "foreign_fence"
					break
				}
				if fault == "operation_plan" {
					entry.Plan.Hash = "sha256:" + strings.Repeat("b", 64)
					break
				}
				if fault == "stage" {
					entry.ApplicationStage.ReceiptID = "foreign_stage"
					entry.ApplicationStage.Hash = entry.ApplicationStage.DocumentHash()
					break
				}
				if fault == "plan" {
					entry.ApplicationRetirementPlan.MembershipRevision++
					entry.ApplicationRetirementPlan.Hash = entry.ApplicationRetirementPlan.DocumentHash()
					break
				}
				if fault == "receipt" {
					entry.ApplicationRetirement.BindingHash = "sha256:" + strings.Repeat("b", 64)
					entry.ApplicationRetirement.Hash = entry.ApplicationRetirement.DocumentHash()
					break
				}
				if fault == "uncertain" {
					entry.ApplicationRetirement.Phase = "uncertain"
					entry.ApplicationRetirement.Intent = nil
					entry.ApplicationRetirement.Observation = nil
					entry.ApplicationRetirement.FailureCode = "inspection_failed"
					entry.ApplicationRetirement.Hash = entry.ApplicationRetirement.DocumentHash()
					break
				}
			}
			if selected, err := retainedApplicationDatabaseRetirement(entries, f.a, f.op); err == nil || selected != nil {
				t.Fatal("corrupt chain selected later receipt")
			}
			if domain.HostingDocumentHash(original) != domain.HostingDocumentHash(f.j.Snapshot().Hosting) || e.retirements != 1 {
				t.Fatal("validation changed durable evidence/effect")
			}
		})
	}
}
