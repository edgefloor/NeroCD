package runnerruntime

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

var errDatabaseNetworkCaptureExecution = errors.New("database network capture remains unresolved")
var databaseNetworkCaptureID = regexp.MustCompile(`^hnc_[0-9a-f]{32}$`)
var databaseNetworkCaptureRunnerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type databaseNetworkCaptureClient interface {
	ClaimHostingDatabaseNetworkCapture(context.Context, string, string) (domain.HostingDatabaseNetworkCaptureClaim, error)
	HostingDatabaseNetworkCapturePlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error)
	RecordHostingDatabaseNetworkOrigin(context.Context, string, domain.HostingDatabaseNetworkOriginRequest, domain.HostingDatabaseNetworkCapturePlan) (domain.HostingDatabaseNetworkOriginReceipt, error)
}

type databaseNetworkCaptureJournal interface {
	Snapshot() runner.JournalSnapshot
	AppendDatabaseNetworkCapture(runner.JournalDatabaseNetworkCapture) (runner.JournalDatabaseNetworkCapture, error)
	RetireDatabaseNetworkCapture(runner.AttemptIdentity) error
}

type databaseNetworkCaptureObserver interface {
	ObserveDatabaseNetworkOrigin(context.Context, domain.HostingDatabaseNetworkCapturePlan, func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error)
}

func captureExecutionFailure(ctx context.Context) error {
	// Do not expose transport diagnostics, private authority or journal paths.
	return errors.Join(errDatabaseNetworkCaptureExecution, ctx.Err())
}

func captureExecutionTarget(c domain.HostingDatabaseNetworkCapture, runnerID, id string) bool {
	return c.Plan.Validate() == nil && c.ID == id && c.Plan.OperationID == id && c.RunID == "capture_"+id && c.Plan.RunnerID == runnerID && c.State == "running" && c.Receipt == nil && c.CreatedBy != "" && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt)
}

func captureExecutionAuthority(c domain.HostingDatabaseNetworkCaptureClaim) domain.HostingAuthority {
	l := c.Lease
	return domain.HostingAuthority{OperationID: c.Capture.ID, RunID: l.RunID, LeaseID: l.ID, Attempt: l.Attempt, Fence: l.Fence}
}

func captureExecutionAck(p domain.HostingDatabaseNetworkCapturePlan, in domain.HostingDatabaseNetworkOriginRequest, ack domain.HostingDatabaseNetworkOriginReceipt) bool {
	return ack.ID == in.ReceiptID && ack.BodyHash == domain.HostingDocumentHash(in) && !ack.CreatedAt.IsZero() && ack.OperationID == in.OperationID && ack.SourceOperationID == p.SourceOperationID && ack.PlanHash == p.Hash && ack.OriginHash == in.Origin.DocumentHash() && ack.Origin == in.Origin && ack.ObservedAt.Equal(in.ObservedAt) && ack.RunnerID == p.RunnerID && !ack.ObservedAt.After(ack.CreatedAt.Add(30*time.Second))
}

// The accepted journal validates complete chains on append and load. Recheck the
// selected chain's identity and ordering here before choosing a continuation.
func captureExecutionChain(j databaseNetworkCaptureJournal, runnerID, id string) ([]runner.JournalDatabaseNetworkCapture, error) {
	var chain []runner.JournalDatabaseNetworkCapture
	for _, e := range j.Snapshot().DatabaseNetworkCapture {
		if e.Attempt.RunID == "capture_"+id || e.Claim != nil && e.Claim.Capture.ID == id || e.Plan != nil && e.Plan.ID == id || e.Origin != nil && e.Origin.OperationID == id || e.Ack != nil && e.Ack.OperationID == id {
			chain = append(chain, e)
		}
	}
	if len(chain) == 0 {
		return nil, nil
	}
	if len(chain) > 4 || chain[0].Claim == nil || !captureExecutionTarget(chain[0].Claim.Capture, runnerID, id) {
		return nil, errDatabaseNetworkCaptureExecution
	}
	c := *chain[0].Claim
	at := journalAttemptIdentity(c.Lease.RunID, c.Lease, nil)
	if c.Lease.RunID != c.Capture.RunID || c.Lease.RunnerID != runnerID || c.Lease.Status != domain.LeaseActive || c.Lease.CompletedAt != nil || c.Lease.CompletionKey != "" {
		return nil, errDatabaseNetworkCaptureExecution
	}
	for i, e := range chain {
		if e.Phase != [...]string{"claim", "plan", "origin", "ack"}[i] || !reflect.DeepEqual(e.Attempt, at) {
			return nil, errDatabaseNetworkCaptureExecution
		}
		switch i {
		case 1:
			if e.Plan == nil || !reflect.DeepEqual(*e.Plan, c.Capture) {
				return nil, errDatabaseNetworkCaptureExecution
			}
		case 2:
			if e.Origin == nil || e.Origin.Validate() != nil || e.Origin.HostingAuthority != captureExecutionAuthority(c) || e.Origin.PlanHash != c.Capture.Plan.Hash || c.Capture.Plan.ValidateOrigin(e.Origin.Origin) != nil {
				return nil, errDatabaseNetworkCaptureExecution
			}
		case 3:
			if e.Ack == nil || !captureExecutionAck(c.Capture.Plan, *chain[2].Origin, *e.Ack) {
				return nil, errDatabaseNetworkCaptureExecution
			}
		}
	}
	return chain, nil
}

func appendCaptureExecution(j databaseNetworkCaptureJournal, e runner.JournalDatabaseNetworkCapture) error {
	if e.ID == "" {
		id, err := runner.NewJournalID("capture_" + e.Phase)
		if err != nil {
			return errDatabaseNetworkCaptureExecution
		}
		e.ID = id
	}
	e.CreatedAt = time.Now().UTC()
	saved, err := j.AppendDatabaseNetworkCapture(e)
	if err != nil || !reflect.DeepEqual(saved, e) {
		return errDatabaseNetworkCaptureExecution
	}
	return nil
}

// executeDatabaseNetworkCapture preserves exact-ID selection. Its caller
// serializes selection and replay for this journal.
func executeDatabaseNetworkCapture(parent context.Context, j databaseNetworkCaptureJournal, client databaseNetworkCaptureClient, observer databaseNetworkCaptureObserver, runnerID, id string) error {
	if j == nil || !databaseNetworkCaptureRunnerID.MatchString(runnerID) || !databaseNetworkCaptureID.MatchString(id) {
		return errDatabaseNetworkCaptureExecution
	}
	chain, err := captureExecutionChain(j, runnerID, id)
	if err != nil {
		return errDatabaseNetworkCaptureExecution
	}
	if len(chain) > 0 {
		return replayDatabaseNetworkCapture(parent, j, client, observer, runnerID, id)
	}
	if parent.Err() != nil {
		return captureExecutionFailure(parent)
	}
	if client == nil || observer == nil {
		return errDatabaseNetworkCaptureExecution
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	claim, err := client.ClaimHostingDatabaseNetworkCapture(ctx, runnerID, id)
	cancel()
	if err != nil || !captureExecutionTarget(claim.Capture, runnerID, id) {
		return captureExecutionFailure(parent)
	}
	return executeClaimedDatabaseNetworkCapture(parent, j, client, observer, runnerID, claim)
}

// Both selectors hand off only a validated, live claim. Its complete authority
// is durable before plan retrieval, observer creation, or origin publication.
func executeClaimedDatabaseNetworkCapture(parent context.Context, j databaseNetworkCaptureJournal, client databaseNetworkCaptureClient, observer databaseNetworkCaptureObserver, runnerID string, claim domain.HostingDatabaseNetworkCaptureClaim) error {
	id := claim.Capture.ID
	l := claim.Lease
	if j == nil || client == nil || observer == nil || parent.Err() != nil || !databaseNetworkCaptureRunnerID.MatchString(runnerID) || !databaseNetworkCaptureID.MatchString(id) || !captureExecutionTarget(claim.Capture, runnerID, id) || l.RunID != claim.Capture.RunID || l.RunnerID != runnerID || l.Status != domain.LeaseActive || l.CompletedAt != nil || l.CompletionKey != "" || l.CreatedAt.IsZero() || !l.ExpiresAt.After(time.Now()) {
		return captureExecutionFailure(parent)
	}
	chain, err := captureExecutionChain(j, runnerID, id)
	if err != nil || len(chain) != 0 {
		return captureExecutionFailure(parent)
	}
	at := journalAttemptIdentity(claim.Lease.RunID, claim.Lease, nil)
	if appendCaptureExecution(j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "claim", Claim: &claim}) != nil {
		return captureExecutionFailure(parent)
	}
	return replayDatabaseNetworkCapture(parent, j, client, observer, runnerID, id)
}

// replayDatabaseNetworkCapture never claims a replacement attempt. A persisted
// origin is the immutable publication request, not permission to sample again.
func replayDatabaseNetworkCapture(parent context.Context, j databaseNetworkCaptureJournal, client databaseNetworkCaptureClient, observer databaseNetworkCaptureObserver, runnerID, id string) error {
	if j == nil || !databaseNetworkCaptureRunnerID.MatchString(runnerID) || !databaseNetworkCaptureID.MatchString(id) {
		return errDatabaseNetworkCaptureExecution
	}
	chain, err := captureExecutionChain(j, runnerID, id)
	if err != nil {
		return errDatabaseNetworkCaptureExecution
	}
	if len(chain) == 0 {
		return nil
	}
	at := chain[0].Attempt
	if len(chain) == 4 {
		if j.RetireDatabaseNetworkCapture(at) != nil {
			return captureExecutionFailure(parent)
		}
		return nil
	}
	if client == nil {
		return errDatabaseNetworkCaptureExecution
	}
	if len(chain) == 3 {
		return publishCaptureExecution(parent, j, client, chain)
	}
	if observer == nil {
		return errDatabaseNetworkCaptureExecution
	}
	// This original window is never renewed or replaced. Expiry does not prove
	// that an unresolved chain is disposable, even before origin publication.
	if parent.Err() != nil || !at.ExpiresAt.After(time.Now()) {
		return captureExecutionFailure(parent)
	}
	claim := *chain[0].Claim
	supervisor := newAttemptSupervisorFrom(parent, claim.Lease)
	defer supervisor.Close()
	ctx, cancel := context.WithDeadline(supervisor.Context(), supervisor.GuardDeadline())
	defer cancel()
	a := captureExecutionAuthority(claim)
	current := func(parent context.Context) error {
		request, stop, err := supervisor.RequestContextFrom(parent)
		if err != nil {
			return errDatabaseNetworkCaptureExecution
		}
		defer stop()
		plan, err := client.HostingDatabaseNetworkCapturePlan(request, runnerID, a)
		if err != nil || request.Err() != nil || !reflect.DeepEqual(plan, claim.Capture) {
			return errDatabaseNetworkCaptureExecution
		}
		return nil
	}
	if current(ctx) != nil {
		return captureExecutionFailure(ctx)
	}
	if len(chain) == 1 {
		p := claim.Capture
		if appendCaptureExecution(j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "plan", Plan: &p}) != nil {
			return captureExecutionFailure(ctx)
		}
	}
	origin, err := observer.ObserveDatabaseNetworkOrigin(ctx, claim.Capture.Plan, current)
	if err != nil || current(ctx) != nil || claim.Capture.Plan.ValidateOrigin(origin) != nil {
		return captureExecutionFailure(ctx)
	}
	receipt, err := runner.NewJournalID("capture_origin")
	if err != nil {
		return captureExecutionFailure(ctx)
	}
	in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: a, ReceiptID: receipt, PlanHash: claim.Capture.Plan.Hash, ObservedAt: time.Now().UTC(), Origin: origin}
	if ctx.Err() != nil || appendCaptureExecution(j, runner.JournalDatabaseNetworkCapture{ID: receipt, Attempt: at, Phase: "origin", Origin: &in}) != nil {
		return captureExecutionFailure(ctx)
	}
	// First publication remains bounded by the original supervisor deadline.
	chain, err = captureExecutionChain(j, runnerID, id)
	if err != nil {
		return captureExecutionFailure(ctx)
	}
	return publishCaptureExecution(ctx, j, client, chain)
}

func publishCaptureExecution(parent context.Context, j databaseNetworkCaptureJournal, client databaseNetworkCaptureClient, chain []runner.JournalDatabaseNetworkCapture) error {
	if parent.Err() != nil {
		return captureExecutionFailure(parent)
	}
	in := *chain[2].Origin
	p := chain[0].Claim.Capture.Plan
	// Historical replay gets a fresh bounded request context, never a new lease,
	// receipt ID, sample time, plan or fence. The controller rechecks revocation.
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	ack, err := client.RecordHostingDatabaseNetworkOrigin(ctx, p.RunnerID, in, p)
	cancel()
	if err != nil || !captureExecutionAck(p, in, ack) {
		return captureExecutionFailure(parent)
	}
	if appendCaptureExecution(j, runner.JournalDatabaseNetworkCapture{Attempt: chain[0].Attempt, Phase: "ack", Ack: &ack}) != nil {
		return captureExecutionFailure(parent)
	}
	if j.RetireDatabaseNetworkCapture(chain[0].Attempt) != nil {
		return captureExecutionFailure(parent)
	}
	return nil
}
