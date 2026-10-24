//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errApplicationDatabaseExecution = errors.New("application database execution requires exact retained evidence and current authority")

type applicationDatabaseExecutionEngine interface {
	CreateStopped(context.Context, applicationDatabaseEngineInput, *hostingruntimeprotocol.Environment, time.Time, func(context.Context) error) (applicationDatabaseCandidate, error)
	ConfigureStoppedNetwork(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, domain.HostingDatabaseBindingEndpointPlan, func(context.Context) error) (applicationDatabaseConfiguration, error)
	StartAndObserve(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, domain.HostingDatabaseBindingEndpointPlan, applicationDatabaseConfiguration, func(context.Context) error) (domain.HostingDatabaseBindingEndpointObservation, error)
	VerifyReady(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, domain.HostingDatabaseBindingEndpointObservation) error
	Retire(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, domain.HostingDatabaseBindingEndpointObservation, domain.HostingDatabaseBindingEndpointPlan, func(context.Context) error) (applicationDatabaseRetirement, error)
	ObserveCandidate(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, applicationDatabaseRecoveryEvidence) (applicationDatabaseRecoveryObservation, error)
	Close() error
}

func applicationDatabaseScope(op domain.HostingOperation, a domain.HostingAuthority, runnerID, mode string) bool {
	return op.ID == a.OperationID && op.Snapshot.RunnerID == runnerID && op.TaskMode == mode && (mode == "deploy" && op.RunID == a.RunID || mode == "observe" && op.ReconciliationRunID == a.RunID) && op.ApplicationDatabase != nil && domain.ValidateHostingApplicationDatabaseOperation(op) == nil
}

func applicationDatabaseSame(expected, got domain.HostingOperation) bool {
	return expected.ID == got.ID && expected.ProjectID == got.ProjectID && expected.ResourceID == got.ResourceID && expected.BindingID == got.BindingID && domain.HostingDocumentHash(expected.Snapshot) == domain.HostingDocumentHash(got.Snapshot) && domain.HostingDocumentHash(expected.ApplicationDatabase) == domain.HostingDocumentHash(got.ApplicationDatabase) && (expected.Plan == nil || domain.HostingDocumentHash(expected.Plan) == domain.HostingDocumentHash(got.Plan))
}

func applicationDatabaseInput(op domain.HostingOperation) (applicationDatabaseEngineInput, error) {
	if op.Plan == nil || op.ApplicationDatabase == nil || domain.ValidateHostingApplicationDatabaseOperation(op) != nil {
		return applicationDatabaseEngineInput{}, errApplicationDatabaseExecution
	}
	p, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	if err != nil || domain.ValidateHostingApplicationDatabasePlan(*op.Plan, p) != nil {
		return applicationDatabaseEngineInput{}, errApplicationDatabaseExecution
	}
	return applicationDatabaseEngineInput{Plan: *op.Plan, Proof: p}, nil
}

func applicationDatabaseEntry(s *attemptSupervisor, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, phase, id string) runner.JournalHosting {
	snapshot := op.Snapshot
	return runner.JournalHosting{ID: id, Attempt: journalAttemptIdentity(a.RunID, lease, s), OperationID: a.OperationID, Phase: phase, ApplicationDatabase: op.ApplicationDatabase, ApplicationSnapshot: &snapshot, RuntimeEnvironment: true, RuntimeConfiguration: snapshot.RuntimeConfiguration, CreatedAt: time.Now().UTC()}
}

// An existing body retains its original timestamp and lease expiry. Renewals
// never change a delivery window or an already published receipt.
func appendApplicationDatabaseEntry(j *runner.AttemptJournal, e runner.JournalHosting) (runner.JournalHosting, error) {
	for _, old := range j.Snapshot().Hosting {
		if old.ID == e.ID {
			e.CreatedAt, e.Attempt = old.CreatedAt, old.Attempt
			if domain.HostingDocumentHash(e) != domain.HostingDocumentHash(old) {
				return runner.JournalHosting{}, errApplicationDatabaseExecution
			}
			return old, nil
		}
	}
	_, err := j.AppendHosting(e)
	return e, err
}

type applicationDatabasePrepareCommand struct {
	command imageCommand
	before  func(context.Context) error
}

func (c applicationDatabasePrepareCommand) RunEnvironment(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	if len(args) >= 6 && args[4] == "image" && args[5] == "pull" {
		if ctx.Err() != nil || c.before(ctx) != nil || ctx.Err() != nil {
			return nil, errApplicationDatabaseExecution
		}
	}
	return c.command.RunEnvironment(ctx, name, args, dir, env)
}

func executeConnectedImageClaim(s *attemptSupervisor, j *runner.AttemptJournal, reporter *attemptReporter, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, local bool, environment *RuntimeEnvironmentConfig, config *DatabaseExecutionConfig) error {
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	c := runnerclient.New(server, token)
	var op domain.HostingOperation
	if err := retryAttemptRequest(s, func(ctx context.Context) error { var err error; op, err = c.HostingPlan(ctx, a); return err }); err != nil {
		return err
	}
	if op.ApplicationDatabase == nil && op.Snapshot.ApplicationDatabase == (domain.HostingApplicationDatabaseRef{}) {
		return executeImageClaim(s, j, reporter, server, token, claim, endpoint, local, environment)
	}
	if config == nil || !config.ApplicationDatabase || environment == nil || !applicationDatabaseScope(op, a, claim.Lease.RunnerID, claim.Run.RunSpec.HostingMode) || claim.PrimitivePlan.RunID != a.RunID || claim.Lease.RunID != a.RunID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errApplicationDatabaseExecution
	}
	ec, err := NewDatabaseEngineConfig(config.StateRoot, config.SecretRoot)
	if err != nil {
		return errApplicationDatabaseExecution
	}
	e, err := newApplicationDatabaseEngine(endpoint, ec)
	if err != nil {
		return errApplicationDatabaseExecution
	}
	defer func() { _ = e.Close() }()
	if op.TaskMode == "observe" {
		return observeApplicationDatabaseClaim(s, c, e, a, op, drain)
	}
	if op.State != "queued" && op.State != "preparing" && op.State != "prepared" || op.Result != nil || op.MutationHold || op.RestoreOwnershipHeld {
		return errApplicationDatabaseExecution
	}
	if op.Release == nil {
		copy := *e.image
		copy.command = applicationDatabasePrepareCommand{command: e.image.command, before: func(ctx context.Context) error {
			current, err := c.HostingPlan(ctx, a)
			if err != nil || !applicationDatabaseScope(current, a, claim.Lease.RunnerID, "deploy") || !applicationDatabaseSame(op, current) || current.Result != nil || current.MutationHold || current.State != "preparing" && current.State != "queued" {
				return errApplicationDatabaseExecution
			}
			return ctx.Err()
		}}
		release, err := copy.Prepare(s.Context(), op.Snapshot)
		if err != nil {
			return errApplicationDatabaseExecution
		}
		req := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("appdb_prepared", a.LeaseID, a.Attempt, "release"), Release: release}
		entry := applicationDatabaseEntry(s, a, claim.Lease, op, "prepared", req.ReceiptID)
		entry.Prepared = &req
		if _, err = appendApplicationDatabaseEntry(j, entry); err != nil {
			return err
		}
		previous := op
		if err = retryAttemptRequest(s, func(ctx context.Context) error { var err error; op, err = c.HostingPrepared(ctx, req); return err }); err != nil {
			return err
		}
		if !applicationDatabaseScope(op, a, claim.Lease.RunnerID, "deploy") || !applicationDatabaseSame(previous, op) || op.PreparedReceipt == nil || op.PreparedReceipt.ID != req.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(release) || op.Release == nil || *op.Release != release {
			return errApplicationDatabaseExecution
		}
	}
	if _, err = applicationDatabaseInput(op); err != nil || e.image.verifyLocalRelease(s.Context(), *op.Release) != nil {
		return errApplicationDatabaseExecution
	}
	return executeApplicationDatabasePrepared(s, j, drain, c, e, a, claim.Lease, op, *environment)
}

type applicationDatabaseExchange struct {
	s     *attemptSupervisor
	j     *runner.AttemptJournal
	c     runnerclient.Client
	a     domain.HostingAuthority
	lease domain.RunLease
	op    domain.HostingOperation
	in    applicationDatabaseEngineInput
	e     applicationDatabaseExecutionEngine
	stage *domain.HostingApplicationDatabaseStage
}

func (x *applicationDatabaseExchange) before(action string) func(context.Context) error {
	return func(ctx context.Context) error {
		if ctx.Err() != nil || x.s.Context().Err() != nil {
			return errLeaseAuthorityLost
		}
		state, err := x.c.HostingApplicationDatabaseState(ctx, x.a, x.op)
		hash := ""
		if x.stage != nil {
			hash = x.stage.Hash
		}
		if err != nil || state.Stage == nil && hash != "" || state.Stage != nil && state.Stage.Hash != hash {
			return errApplicationDatabaseExecution
		}
		id, err := newRuntimeDeliveryID()
		if err != nil {
			return err
		}
		req := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: x.a, AuthorizationID: id, ExpectedStageHash: hash, Action: action}
		grant, err := x.c.HostingApplicationDatabaseAuthorize(ctx, req, x.op, x.in.Proof.MembershipRevision, x.s.GuardDeadline())
		if err != nil || !time.Now().Before(grant.UseUntil) || ctx.Err() != nil || x.s.Context().Err() != nil {
			return errApplicationDatabaseExecution
		}
		return nil
	}
}

func (x *applicationDatabaseExchange) publish(stage domain.HostingApplicationDatabaseStage) error {
	stage.ReceiptID = attemptMutationKey("appdb_stage", x.a.LeaseID, x.a.Attempt, stage.Phase)
	stage.Hash = stage.DocumentHash()
	if stage.ValidateTransition(x.stage, x.in.Plan, x.in.Proof) != nil {
		return errApplicationDatabaseExecution
	}
	e := applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "application_stage", stage.ReceiptID)
	e.Plan, e.ApplicationStage = x.op.Plan, &stage
	if _, err := appendApplicationDatabaseEntry(x.j, e); err != nil {
		return err
	}
	if err := retryAttemptRequest(x.s, func(ctx context.Context) error {
		ack, err := x.c.HostingApplicationDatabaseStage(ctx, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: x.a, Stage: stage}, x.op)
		if err == nil && (!applicationDatabaseSame(x.op, ack) || !ack.MutationHold || ack.Result != nil) {
			return errApplicationDatabaseExecution
		}
		return err
	}); err != nil {
		return err
	}
	state, err := x.c.HostingApplicationDatabaseState(x.s.Context(), x.a, x.op)
	if err != nil || state.Stage == nil || state.Stage.Hash != stage.Hash {
		return errApplicationDatabaseExecution
	}
	x.stage = &stage
	return nil
}

func (x *applicationDatabaseExchange) endpointReceipt(p domain.HostingDatabaseBindingEndpointPlan, phase string, observation *domain.HostingDatabaseBindingEndpointObservation) domain.HostingDatabaseBindingEndpointReceipt {
	r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: attemptMutationKey("appdb_endpoint", x.a.LeaseID, x.a.Attempt, phase), OperationID: x.a.OperationID, PlanHash: p.Hash, BindingHash: x.in.Proof.Binding.Hash, MembershipRevision: p.MembershipRevision, Phase: phase, Observation: observation}
	if observation == nil {
		r.Intent = &domain.HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: p.ExpectedEndpointID}
	}
	r.Hash = r.DocumentHash()
	return r
}

func executeApplicationDatabasePrepared(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, c runnerclient.Client, e applicationDatabaseExecutionEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, environmentConfig RuntimeEnvironmentConfig) error {
	return executeApplicationDatabasePreparedWithIntent(s, j, drain, c, e, a, lease, op, environmentConfig, nil)
}

func executeApplicationDatabasePreparedWithIntent(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, c runnerclient.Client, e applicationDatabaseExecutionEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, environmentConfig RuntimeEnvironmentConfig, retained *runner.JournalHosting) error {
	in, err := applicationDatabaseInput(op)
	if err != nil || op.State != "prepared" || op.MutationHold || op.Result != nil {
		return errApplicationDatabaseExecution
	}
	for _, old := range j.Snapshot().Hosting {
		if storageEntryAuthority(old) == a && old.Phase == "apply_intent" && (retained == nil || domain.HostingDocumentHash(old) != domain.HostingDocumentHash(*retained)) {
			return errApplicationDatabaseExecution
		}
	}
	intent := applicationDatabaseEntry(s, a, lease, op, "apply_intent", attemptMutationKey("appdb_intent", a.LeaseID, a.Attempt, "create"))
	intent.Plan = op.Plan
	if retained != nil {
		if storageEntryAuthority(*retained) != a || retained.Plan == nil || retained.Phase != "apply_intent" || domain.HostingDocumentHash(retained.Plan) != domain.HostingDocumentHash(op.Plan) || retained.ApplicationDatabase == nil || retained.ApplicationDatabase.Hash != op.ApplicationDatabase.Hash || !time.Now().Before(databaseIntentWindow(*retained, s)) {
			return errApplicationDatabaseExecution
		}
		intent = *retained
	} else {
		intent.DeliveryID, err = newRuntimeDeliveryID()
		if err != nil {
			return err
		}
	}
	if _, err = appendApplicationDatabaseEntry(j, intent); err != nil {
		return err
	}
	deadline := databaseIntentWindow(intent, s)
	environment, err := fetchHostingRuntimeEnvironment(s, c, lease.RunnerID, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: in.Plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration, DeliveryID: intent.DeliveryID}, environmentConfig, deadline)
	if err != nil {
		return err
	}
	defer environment.Close()
	var admitted domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error {
		var err error
		admitted, err = c.HostingApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if !applicationDatabaseScope(admitted, a, lease.RunnerID, "deploy") || !applicationDatabaseSame(op, admitted) || !admitted.MutationHold || admitted.Result != nil || admitted.State != "applying" && admitted.State != "verifying" {
		return errApplicationDatabaseExecution
	}
	x := applicationDatabaseExchange{s: s, j: j, c: c, a: a, lease: lease, op: admitted, in: in, e: e}
	candidate, createErr := e.CreateStopped(s.Context(), in, environment, deadline, x.before("create"))
	actual := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: candidate.OperationID, PlanHash: candidate.PlanHash, CreateNonce: candidate.CreateNonce, ContainerID: candidate.ContainerID, ImageID: candidate.ImageID, DaemonID: candidate.DaemonID, DefaultNetworkID: candidate.DefaultNetworkID}
	actual.Hash = actual.DocumentHash()
	if actual.Validate(in.Plan, in.Proof) == nil {
		entry := applicationDatabaseEntry(s, a, lease, op, "application_candidate", attemptMutationKey("appdb_candidate", a.LeaseID, a.Attempt, "actual"))
		entry.Plan, entry.ApplicationCandidate = op.Plan, &actual
		if _, err = appendApplicationDatabaseEntry(j, entry); err != nil {
			return err
		}
	} else {
		return errApplicationDatabaseExecution
	}
	if createErr != nil {
		return errApplicationDatabaseExecution
	}
	attach, err := domain.CompileHostingApplicationDatabaseAttachPlan(in.Plan, in.Proof, actual)
	if err != nil {
		return errApplicationDatabaseExecution
	}
	stage := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, OperationID: a.OperationID, PlanHash: in.Plan.Hash, Phase: "candidate_created", Candidate: actual, AttachPlan: attach}
	if err = x.publish(stage); err != nil {
		return err
	}
	return x.finish(candidate, drain)
}

func (x *applicationDatabaseExchange) finish(candidate applicationDatabaseCandidate, drain func(context.Context) error) error {
	if x.stage == nil || x.stage.Phase == "uncertain" {
		return errApplicationDatabaseExecution
	}
	stage := *x.stage
	if stage.Phase == "candidate_created" {
		r := x.endpointReceipt(stage.AttachPlan, "attach_intent", nil)
		stage.Phase, stage.EndpointReceipt = "attach_intent", &r
		if err := x.publish(stage); err != nil {
			return err
		}
	}
	if stage.Phase == "attach_intent" {
		if _, err := x.e.ConfigureStoppedNetwork(x.s.Context(), x.in, candidate, stage.AttachPlan, x.before("configure")); err != nil {
			return errApplicationDatabaseExecution
		}
		stage.Phase = "network_configured"
		if err := x.publish(stage); err != nil {
			return err
		}
	}
	if stage.Phase == "network_configured" {
		configured := applicationDatabaseExpectedConfiguration(x.in, candidate, stage.AttachPlan)
		observed, err := x.e.StartAndObserve(x.s.Context(), x.in, candidate, stage.AttachPlan, configured, x.before("start"))
		if err != nil {
			return errApplicationDatabaseExecution
		}
		r := x.endpointReceipt(stage.AttachPlan, "attached", &observed)
		stage.Phase, stage.EndpointReceipt = "attached", &r
		if err = x.publish(stage); err != nil {
			return err
		}
	}
	if stage.Phase == "attached" {
		if err := x.e.VerifyReady(x.s.Context(), x.in, candidate, *stage.EndpointReceipt.Observation); err != nil {
			return errApplicationDatabaseExecution
		}
		stage.Phase = "ready"
		if err := x.publish(stage); err != nil {
			return err
		}
	}
	if stage.Phase != "ready" {
		return errApplicationDatabaseExecution
	}
	if len(x.in.Proof.Members) == 1 {
		if err := x.retire(candidate, *stage.EndpointReceipt.Observation); err != nil {
			return err
		}
	}
	r := domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: candidate.ContainerID, ImageID: candidate.ImageID, DaemonID: candidate.DaemonID, PlanHash: x.in.Plan.Hash}
	if x.in.Plan.PreviousHealthy != nil {
		r.PreviousContainerID = x.in.Plan.PreviousHealthy.ContainerID
	}
	req := domain.HostingCompleteRequest{HostingAuthority: x.a, ReceiptID: attemptMutationKey("appdb_result", x.a.LeaseID, x.a.Attempt, "complete"), Result: r}
	entry := applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "result", req.ReceiptID)
	entry.Result = &req
	if _, err := appendApplicationDatabaseEntry(x.j, entry); err != nil {
		return err
	}
	if drain != nil {
		if err := drain(x.s.Context()); err != nil {
			return err
		}
	}
	return completeApplicationDatabase(x.s.Context(), x.j, x.c, x.a, x.op, req)
}

func (x *applicationDatabaseExchange) retire(candidate applicationDatabaseCandidate, observed domain.HostingDatabaseBindingEndpointObservation) error {
	p, err := x.c.HostingApplicationDatabaseRetirementPlan(x.s.Context(), x.a, x.op)
	if err != nil {
		return err
	}
	for _, old := range x.j.Snapshot().Hosting {
		if storageEntryAuthority(old) == x.a && old.ApplicationRetirement != nil && old.ApplicationRetirement.Phase == "detached" {
			if old.ApplicationRetirementPlan == nil || *old.ApplicationRetirementPlan != p {
				return errApplicationDatabaseExecution
			}
			return x.publishRetirement(p, *old.ApplicationRetirement)
		}
	}
	intent := x.endpointReceipt(p, "retire_intent", nil)
	if err = x.publishRetirement(p, intent); err != nil {
		return err
	}
	retired, err := x.e.Retire(x.s.Context(), x.in, candidate, observed, p, x.before("retire"))
	if err != nil || !retired.ContainerRemoved || retired.ContainerID != p.Target.ContainerID || retired.Endpoint.Present {
		return errApplicationDatabaseExecution
	}
	r := x.endpointReceipt(p, "detached", &retired.Endpoint)
	return x.publishRetirement(p, r)
}

func (x *applicationDatabaseExchange) publishRetirement(p domain.HostingDatabaseBindingEndpointPlan, r domain.HostingDatabaseBindingEndpointReceipt) error {
	entry := applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "application_retirement", r.ReceiptID)
	entry.Plan, entry.ApplicationStage, entry.ApplicationRetirementPlan, entry.ApplicationRetirement = x.op.Plan, x.stage, &p, &r
	if _, err := appendApplicationDatabaseEntry(x.j, entry); err != nil {
		return err
	}
	if err := retryAttemptRequest(x.s, func(ctx context.Context) error {
		_, err := x.c.HostingApplicationDatabaseRetirement(ctx, domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: x.a, PlanHash: x.in.Plan.Hash, Receipt: r}, p, x.op)
		return err
	}); err != nil {
		return err
	}
	state, err := x.c.HostingApplicationDatabaseState(x.s.Context(), x.a, x.op)
	if err != nil || state.RetirementPlan == nil || *state.RetirementPlan != p || state.RetirementReceipt == nil || state.RetirementReceipt.Hash != r.Hash {
		return errApplicationDatabaseExecution
	}
	return nil
}

func completeApplicationDatabase(ctx context.Context, j *runner.AttemptJournal, c runnerclient.Client, a domain.HostingAuthority, expected domain.HostingOperation, req domain.HostingCompleteRequest) error {
	ack, err := c.HostingComplete(ctx, req)
	if err != nil {
		return err
	}
	if !applicationDatabaseScope(ack, a, expected.Snapshot.RunnerID, "deploy") || !applicationDatabaseSame(expected, ack) || ack.State != "succeeded" || ack.MutationHold || ack.RestoreOwnershipHeld || ack.Result == nil || *ack.Result != req.Result || ack.CompleteReceipt == nil || ack.CompleteReceipt.ID != req.ReceiptID || ack.CompleteReceipt.BodyHash != domain.HostingDocumentHash(req.Result) {
		return errApplicationDatabaseExecution
	}
	return j.AckHostingConnection(a)
}

func observeApplicationDatabaseClaim(s *attemptSupervisor, c runnerclient.Client, e applicationDatabaseExecutionEngine, a domain.HostingAuthority, op domain.HostingOperation, drains ...func(context.Context) error) error {
	in, err := applicationDatabaseInput(op)
	if err != nil {
		return err
	}
	state, err := c.HostingApplicationDatabaseState(s.Context(), a, op)
	if err != nil || state.Stage == nil {
		return errApplicationDatabaseExecution
	}
	stage := state.Stage
	candidate := privateApplicationDatabaseCandidate(stage.Candidate)
	evidence := applicationDatabaseRecoveryEvidence{AttachPlan: stage.AttachPlan}
	if stage.Phase == "network_configured" || stage.Phase == "attached" || stage.Phase == "ready" {
		v := applicationDatabaseExpectedConfiguration(in, candidate, stage.AttachPlan)
		evidence.Configuration = &v
	}
	if stage.EndpointReceipt != nil {
		evidence.Started = stage.EndpointReceipt.Observation
	}
	observed, err := e.ObserveCandidate(s.Context(), in, candidate, evidence)
	if err != nil || observed.Candidate != candidate {
		return errApplicationDatabaseExecution
	}
	observation := domain.HostingObservation{HostingAuthority: a, DaemonID: candidate.DaemonID, ContainerID: candidate.ContainerID, ImageID: candidate.ImageID, BindingID: op.BindingID, PlanHash: in.Plan.Hash, ReleaseHash: in.Plan.Release.Hash, Ready: observed.Phase == applicationDatabaseRecoveryReady}
	if observed.Phase == applicationDatabaseRecoveryAbsent {
		observation.NoOwnedContainers, observation.ContainerID, observation.ImageID = true, "", ""
	}
	for _, drain := range drains {
		if err := drain(s.Context()); err != nil {
			return err
		}
	}
	var ack domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error {
		var err error
		ack, err = c.HostingReconciliation(ctx, observation)
		return err
	}); err != nil {
		return err
	}
	// Observation records readiness only. It must preserve the original hold,
	// result and stage; it cannot activate a candidate or settle deployment.
	if !applicationDatabaseSame(op, ack) || ack.MutationHold != op.MutationHold || ack.State != op.State || domain.HostingDocumentHash(ack.Result) != domain.HostingDocumentHash(op.Result) {
		return errApplicationDatabaseExecution
	}
	after, err := c.HostingApplicationDatabaseState(s.Context(), a, op)
	if err != nil || domain.HostingDocumentHash(after) != domain.HostingDocumentHash(state) {
		return errApplicationDatabaseExecution
	}
	return nil
}

func privateApplicationDatabaseCandidate(c domain.HostingApplicationDatabaseCandidate) applicationDatabaseCandidate {
	return applicationDatabaseCandidate{OperationID: c.OperationID, PlanHash: c.PlanHash, CreateNonce: c.CreateNonce, ContainerID: c.ContainerID, ImageID: c.ImageID, DaemonID: c.DaemonID, DefaultNetworkID: c.DefaultNetworkID}
}

// Saved responses are replayed before host inspection. An exact completion ACK
// is the only event that removes the chain. Unknown create outcomes remain held.
func reconcileApplicationDatabaseJournal(ctx context.Context, config Config, j *runner.AttemptJournal) error {
	return reconcileApplicationDatabaseWithEngine(ctx, config, j, func() (applicationDatabaseExecutionEngine, error) {
		ec, err := NewDatabaseEngineConfig(config.Database.StateRoot, config.Database.SecretRoot)
		if err != nil {
			return nil, errApplicationDatabaseExecution
		}
		return newApplicationDatabaseEngine(config.ImageDockerEndpoint, ec)
	})
}

func reconcileApplicationDatabaseWithEngine(parent context.Context, config Config, j *runner.AttemptJournal, open func() (applicationDatabaseExecutionEngine, error)) error {
	if config.Database != nil {
		parent = runnerclient.WithDatabaseTransport(parent, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: config.Database.TrustedLoopbackHTTP})
	}
	c := runnerclient.New(config.Server, config.Token)
	seen := map[domain.HostingAuthority]bool{}
	for _, first := range j.Snapshot().Hosting {
		if first.ApplicationDatabase == nil {
			continue
		}
		if config.Database == nil || !config.Database.ApplicationDatabase || config.RuntimeEnvironment == nil {
			return errApplicationDatabaseExecution
		}
		a := storageEntryAuthority(first)
		if seen[a] {
			continue
		}
		seen[a] = true
		if err := reconcileApplicationDatabaseAttempt(parent, config, c, j, first, open); err != nil {
			return err
		}
	}
	return nil
}

func reconcileApplicationDatabaseAttempt(parent context.Context, config Config, c runnerclient.Client, j *runner.AttemptJournal, first runner.JournalHosting, open func() (applicationDatabaseExecutionEngine, error)) error {
	a := storageEntryAuthority(first)
	op := domain.HostingOperation{ID: a.OperationID, ProjectID: first.ApplicationDatabase.Binding.ProjectID, ResourceID: first.ApplicationSnapshot.ResourceID, BindingID: first.ApplicationSnapshot.BindingID, RunID: a.RunID, TaskMode: "deploy", Snapshot: *first.ApplicationSnapshot, ApplicationDatabase: first.ApplicationDatabase}
	var entries []runner.JournalHosting
	var result *domain.HostingCompleteRequest
	var candidate *domain.HostingApplicationDatabaseCandidate
	var stage *domain.HostingApplicationDatabaseStage
	var intent *runner.JournalHosting
	for _, e := range j.Snapshot().Hosting {
		if storageEntryAuthority(e) != a {
			continue
		}
		if e.ApplicationDatabase == nil || e.ApplicationDatabase.Hash != first.ApplicationDatabase.Hash || e.ApplicationSnapshot == nil || domain.HostingDocumentHash(e.ApplicationSnapshot) != domain.HostingDocumentHash(first.ApplicationSnapshot) {
			return errApplicationDatabaseExecution
		}
		entries = append(entries, e)
		if e.Plan != nil {
			op.Plan, op.Release = e.Plan, &e.Plan.Release
		}
		if e.Result != nil {
			result = e.Result
		}
		if e.ApplicationCandidate != nil {
			candidate = e.ApplicationCandidate
		}
		if e.ApplicationStage != nil {
			stage = e.ApplicationStage
		}
		if e.Phase == "apply_intent" {
			copy := e
			intent = &copy
		}
	}
	if result != nil {
		if err := replayHostingEvents(config.Server, config.Token, j, first.Attempt, parent); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		return completeApplicationDatabase(ctx, j, c, a, op, *result)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	historical, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
	cancel()
	if err != nil || !applicationDatabaseSame(op, historical) {
		return errApplicationDatabaseExecution
	}
	if historical.MutationHold && historical.ReconcileRequested {
		if err := observeApplicationDatabaseRecovery(parent, config, c, a, historical, open); err != nil {
			return err
		}
		// A bounded observer receipt completes only its own task. The original
		// journal and activation hold remain unresolved and block ordinary work.
		return domain.ErrHostingDatabaseRecoveryObserveOnly
	}
	for _, e := range entries {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		var ack domain.HostingOperation
		var err error
		switch {
		case e.Prepared != nil:
			ack, err = c.HostingPrepared(ctx, *e.Prepared)
			if err == nil && (!applicationDatabaseSame(op, ack) || ack.PreparedReceipt == nil || ack.PreparedReceipt.ID != e.Prepared.ReceiptID || ack.PreparedReceipt.BodyHash != domain.HostingDocumentHash(e.Prepared.Release) || ack.Release == nil || *ack.Release != e.Prepared.Release) {
				err = errApplicationDatabaseExecution
			}
		case e.Phase == "application_stage":
			// Each saved immutable body is acknowledged in original order.
			ack, err = c.HostingApplicationDatabaseStage(ctx, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: a, Stage: *e.ApplicationStage}, op)
		case e.ApplicationRetirement != nil:
			ack, err = c.HostingApplicationDatabaseRetirement(ctx, domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, Receipt: *e.ApplicationRetirement}, *e.ApplicationRetirementPlan, op)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	// Prepared-only replay can safely create its first durable intent. Once an
	// intent exists, absence of a returned candidate never permits another create.
	ctx, cancel = context.WithTimeout(parent, 5*time.Second)
	current, err := c.HostingPlan(ctx, a)
	cancel()
	if err != nil || !applicationDatabaseScope(current, a, config.RunnerID, "deploy") || !applicationDatabaseSame(op, current) || current.Result != nil || current.RestoreOwnershipHeld {
		return errApplicationDatabaseExecution
	}
	// The controller cannot still be prepared after authorizing create. Only
	// this live prepared state permits the original pre-admission delivery retry.
	if intent != nil && candidate == nil && current.State != "prepared" {
		return errApplicationDatabaseExecution
	}
	lease, fenced, err := probeReplayAuthority(config.Server, config.Token, first.Attempt, parent)
	if err != nil || fenced || lease.RunnerID != config.RunnerID {
		return errApplicationDatabaseExecution
	}
	s := newAttemptSupervisorFrom(parent, lease)
	defer s.Close()
	validate := exactBuildLease(lease)
	renewer := startLeaseRenewer(s.Context(), s, config.Server, config.Token, lease, time.Second, validate)
	defer renewer.Stop()
	watcher := startLeaseWatcher(s.Context(), config.Server, config.Token, lease, time.Second, s.cancel, validate)
	defer watcher.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, config.Server, config.Token, a.RunID, lease)
	defer reporter.Stop()
	e, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	drain := databaseCompletionDrain(s, reporter, watcher, renewer)
	if candidate == nil {
		return executeApplicationDatabasePreparedWithIntent(s, j, drain, c, e, a, lease, current, *config.RuntimeEnvironment, intent)
	}
	if !current.MutationHold || current.State != "applying" && current.State != "verifying" {
		return errApplicationDatabaseExecution
	}
	in, err := applicationDatabaseInput(current)
	if err != nil {
		return err
	}
	x := applicationDatabaseExchange{s: s, j: j, c: c, a: a, lease: lease, op: current, in: in, e: e, stage: stage}
	if stage != nil && stage.Phase == "ready" {
		for _, entry := range entries {
			if entry.ApplicationRetirement != nil && entry.ApplicationRetirement.Phase == "detached" {
				// The exact ready and detached bodies are already durable and
				// acknowledged above. Completion needs no replacement observation.
				return x.finish(privateApplicationDatabaseCandidate(*candidate), drain)
			}
		}
	}
	attach, err := domain.CompileHostingApplicationDatabaseAttachPlan(in.Plan, in.Proof, *candidate)
	if err != nil {
		return err
	}
	private := privateApplicationDatabaseCandidate(*candidate)
	evidence := applicationDatabaseRecoveryEvidence{AttachPlan: attach}
	if stage != nil && (stage.Phase == "network_configured" || stage.Phase == "attached" || stage.Phase == "ready") {
		v := applicationDatabaseExpectedConfiguration(in, private, attach)
		evidence.Configuration = &v
	}
	if stage != nil && stage.EndpointReceipt != nil {
		evidence.Started = stage.EndpointReceipt.Observation
	}
	observed, err := e.ObserveCandidate(s.Context(), in, private, evidence)
	if err != nil || observed.Candidate != private {
		return errApplicationDatabaseExecution
	}
	if stage == nil {
		if observed.Phase != applicationDatabaseRecoveryCreated {
			return errApplicationDatabaseExecution
		}
		v := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, OperationID: a.OperationID, PlanHash: in.Plan.Hash, Phase: "candidate_created", Candidate: *candidate, AttachPlan: attach}
		if err = x.publish(v); err != nil {
			return err
		}
		stage = x.stage
	}
	// Recovered physical facts can advance a saved intent, but never authorize
	// another effect or turn an unknown outcome into a guessed identity.
	if stage.Phase == "attach_intent" && observed.Phase == applicationDatabaseRecoveryConfigured {
		v := *stage
		v.Phase = "network_configured"
		if err = x.publish(v); err != nil {
			return err
		}
		stage = x.stage
	}
	if stage.Phase == "network_configured" && (observed.Phase == applicationDatabaseRecoveryStarted || observed.Phase == applicationDatabaseRecoveryReady) {
		v := *stage
		r := x.endpointReceipt(attach, "attached", &observed.Endpoint)
		v.Phase, v.EndpointReceipt = "attached", &r
		if err = x.publish(v); err != nil {
			return err
		}
		stage = x.stage
	}
	allowed := stage.Phase == "candidate_created" && observed.Phase == applicationDatabaseRecoveryCreated || stage.Phase == "attach_intent" && (observed.Phase == applicationDatabaseRecoveryCreated || observed.Phase == applicationDatabaseRecoveryConfigured) || stage.Phase == "network_configured" && observed.Phase == applicationDatabaseRecoveryConfigured || (stage.Phase == "attached" || stage.Phase == "ready") && (observed.Phase == applicationDatabaseRecoveryStarted || observed.Phase == applicationDatabaseRecoveryReady)
	if !allowed {
		return errApplicationDatabaseExecution
	}
	return x.finish(private, drain)
}

func observeApplicationDatabaseRecovery(parent context.Context, config Config, c runnerclient.Client, original domain.HostingAuthority, op domain.HostingOperation, open func() (applicationDatabaseExecutionEngine, error)) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	claimed, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: original, Phase: "claim_observation"})
	cancel()
	if err != nil || !applicationDatabaseSame(op, claimed) || claimed.TaskMode != "observe" || claimed.ObservationLease == nil {
		return errApplicationDatabaseExecution
	}
	lease := *claimed.ObservationLease
	if lease.RunID != claimed.ReconciliationRunID || lease.RunID != "observe_"+op.ID || lease.RunnerID != config.RunnerID || lease.Status != domain.LeaseActive || !time.Now().Before(lease.ExpiresAt) {
		return errApplicationDatabaseExecution
	}
	a := hostingAuthority(lease.RunID, op.ID, lease)
	if !applicationDatabaseScope(claimed, a, config.RunnerID, "observe") {
		return errApplicationDatabaseExecution
	}
	s := newAttemptSupervisorFrom(parent, lease)
	defer s.Close()
	validate := exactBuildLease(lease)
	renewer := startLeaseRenewer(s.Context(), s, config.Server, config.Token, lease, time.Second, validate)
	defer renewer.Stop()
	watcher := startLeaseWatcher(s.Context(), config.Server, config.Token, lease, time.Second, s.cancel, validate)
	defer watcher.Stop()
	var plan domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error { var err error; plan, err = c.HostingPlan(ctx, a); return err }); err != nil {
		return err
	}
	if !applicationDatabaseScope(plan, a, config.RunnerID, "observe") || !applicationDatabaseSame(op, plan) || domain.HostingDocumentHash(plan.Result) != domain.HostingDocumentHash(op.Result) || !plan.MutationHold {
		return errApplicationDatabaseExecution
	}
	e, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	return observeApplicationDatabaseClaim(s, c, e, a, plan, func(ctx context.Context) error { watcher.Stop(); renewer.Stop(); return ctx.Err() })
}
