package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

const (
	runnerEventBatchCount = 64
	runnerEventBatchBytes = 256 * 1024
	runnerEventFlushDelay = 250 * time.Millisecond
)

type attemptReporter struct {
	journal    *runner.AttemptJournal
	supervisor *attemptSupervisor
	server     string
	token      string
	authority  runner.AttemptIdentity

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}

	flushMu sync.Mutex
	fatalMu sync.Mutex
	fatal   error
	stop    sync.Once
}

func startAttemptReporter(parent context.Context, supervisor *attemptSupervisor, journal *runner.AttemptJournal, server, token, runID string, lease domain.RunLease) *attemptReporter {
	ctx, cancel := context.WithCancel(parent)
	r := &attemptReporter{
		journal: journal, supervisor: supervisor, server: server, token: token,
		authority: journalAttemptIdentity(runID, lease, supervisor),
		ctx:       ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	go r.run()
	return r
}

func (r *attemptReporter) Stop() {
	r.stop.Do(func() {
		r.cancel()
		<-r.done
	})
}

func (r *attemptReporter) Emit(stream, message string, sequence int) error {
	if err := r.Err(); err != nil {
		return err
	}
	id, err := runner.NewJournalID("event")
	if err != nil {
		r.fail(err)
		return err
	}
	event := runner.JournalEvent{
		ID: id, Attempt: r.currentAuthority(), Sequence: sequence,
		Stream: stream, Message: message, CreatedAt: time.Now().UTC(),
	}
	if _, err := r.journal.AppendEvent(event); err != nil {
		r.fail(err)
		return err
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

func (r *attemptReporter) Err() error {
	r.fatalMu.Lock()
	defer r.fatalMu.Unlock()
	return r.fatal
}

func (r *attemptReporter) WaitEmpty(ctx context.Context) error {
	select {
	case r.wake <- struct{}{}:
	default:
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := r.Err(); err != nil {
			return err
		}
		if !journalHasAttemptEvents(r.journal.Snapshot(), r.authority) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.supervisor.Context().Done():
			return fmt.Errorf("%w: %v", errLeaseAuthorityLost, r.supervisor.Context().Err())
		case <-ticker.C:
		}
	}
}

func (r *attemptReporter) run() {
	defer close(r.done)
	ticker := time.NewTicker(runnerEventFlushDelay)
	defer ticker.Stop()
	backoff := 100 * time.Millisecond
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
		for journalHasAttemptEvents(r.journal.Snapshot(), r.authority) {
			err := r.flushOnce(r.ctx)
			if err == nil {
				backoff = 100 * time.Millisecond
				continue
			}
			if r.ctx.Err() != nil {
				return
			}
			if classifyRunnerFailure(err) != runnerFailureTransient {
				r.fail(err)
				return
			}
			if !waitRunnerRetry(r.ctx, r.supervisor, backoff) {
				r.fail(err)
				return
			}
			if backoff < time.Second {
				backoff *= 2
			}
		}
	}
}

func (r *attemptReporter) flushOnce(parent context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	events := journalEventBatch(r.journal.Snapshot(), r.authority)
	if len(events) == 0 {
		return nil
	}
	ctx, cancel, err := r.supervisor.RequestContextFrom(parent)
	if err != nil {
		return err
	}
	defer cancel()
	if err := r.postEvents(ctx, events); err != nil {
		return err
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return r.journal.AckEvents(ids)
}

func (r *attemptReporter) postEvents(ctx context.Context, events []runner.JournalEvent) error {
	requestEvents := make([]runnerEventRequest, 0, len(events))
	for _, event := range events {
		requestEvents = append(requestEvents, runnerEventRequest{EventKey: event.ID, Sequence: event.Sequence, Stream: event.Stream, Message: event.Message})
	}
	var ack runnerEventBatchAck
	err := postAPIIntoContext(ctx, r.server+"/api/v1/runners/events/batch", runnerEventBatchRequest{
		RunID: r.authority.RunID, LeaseID: r.authority.LeaseID, Attempt: r.authority.Attempt, Fence: r.authority.Fence, Events: requestEvents,
	}, r.token, &ack)
	if err != nil {
		return err
	}
	if err := validateEventAck(events, ack); err != nil {
		return err
	}
	return nil
}

func (r *attemptReporter) currentAuthority() runner.AttemptIdentity {
	authority := r.authority
	authority.ExpiresAt = r.supervisor.Expiry()
	return authority
}

func (r *attemptReporter) fail(cause error) {
	r.fatalMu.Lock()
	if r.fatal == nil {
		r.fatal = fmt.Errorf("%w: runner event transport: %v", errLeaseAuthorityLost, cause)
	}
	r.fatalMu.Unlock()
	r.supervisor.cancel()
}

func journalAttemptIdentity(runID string, lease domain.RunLease, supervisor *attemptSupervisor) runner.AttemptIdentity {
	expiresAt := lease.ExpiresAt
	if supervisor != nil {
		expiresAt = supervisor.Expiry()
	}
	return runner.AttemptIdentity{RunID: runID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, CreatedAt: lease.CreatedAt, ExpiresAt: expiresAt}
}

func journalHasAttemptEvents(snapshot runner.JournalSnapshot, authority runner.AttemptIdentity) bool {
	for _, event := range snapshot.Events {
		if sameJournalAttempt(event.Attempt, authority) {
			return true
		}
	}
	return false
}

func journalEventBatch(snapshot runner.JournalSnapshot, authority runner.AttemptIdentity) []runner.JournalEvent {
	eligible := make([]runner.JournalEvent, 0, len(snapshot.Events))
	for _, event := range snapshot.Events {
		if !sameJournalAttempt(event.Attempt, authority) {
			continue
		}
		eligible = append(eligible, event)
	}
	return boundedJournalEventBatch(eligible)
}

func boundedJournalEventBatch(events []runner.JournalEvent) []runner.JournalEvent {
	batch := make([]runner.JournalEvent, 0, min(len(events), runnerEventBatchCount))
	bytes := 0
	for _, event := range events {
		size := len(event.ID) + len(event.Stream) + len(event.Message)
		if len(batch) == runnerEventBatchCount || (len(batch) > 0 && bytes+size > runnerEventBatchBytes) {
			break
		}
		if size > runnerEventBatchBytes {
			return []runner.JournalEvent{event}
		}
		batch = append(batch, event)
		bytes += size
	}
	return batch
}

func validateEventAck(sent []runner.JournalEvent, ack runnerEventBatchAck) error {
	if len(sent) != len(ack.Events) {
		return errors.New("runner event acknowledgement count mismatch")
	}
	for i := range sent {
		if ack.Events[i].EventKey != sent[i].ID || ack.Events[i].RunID != sent[i].Attempt.RunID || ack.Events[i].LeaseID != sent[i].Attempt.LeaseID || ack.Events[i].Attempt != sent[i].Attempt.Attempt || ack.Events[i].RequestedSequence != sent[i].Sequence || ack.Events[i].Stream != sent[i].Stream || ack.Events[i].Message != sent[i].Message {
			return errors.New("runner event acknowledgement content mismatch")
		}
	}
	return nil
}

func sameJournalAttempt(left, right runner.AttemptIdentity) bool {
	return left.RunID == right.RunID && left.LeaseID == right.LeaseID && left.Attempt == right.Attempt && left.Fence == right.Fence
}

func waitRunnerRetry(ctx context.Context, supervisor *attemptSupervisor, base time.Duration) bool {
	guard := supervisor.GuardDeadline()
	if !guard.After(time.Now()) {
		return false
	}
	delay := base + runnerRetryJitter(base/4)
	if remaining := time.Until(guard); delay >= remaining {
		delay = remaining / 2
	}
	if delay <= 0 {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-supervisor.Context().Done():
		return false
	case <-timer.C:
		return true
	}
}

func runnerRetryJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(entropy[:]) % uint64(max))
}

type replayAttempt struct {
	authority   runner.AttemptIdentity
	events      []runner.JournalEvent
	completions []runner.JournalCompletion
}

func reconcileRunnerJournal(server, token string, journal *runner.AttemptJournal) error {
	snapshot := journal.Snapshot()
	attempts := groupReplayAttempts(snapshot)
	for _, pending := range attempts {
		lease, fenced, err := probeReplayAuthority(server, token, pending.authority)
		if err != nil {
			return err
		}
		if fenced {
			// A bounded read-only request made with the exact attempt capability is
			// the only startup outcome that authorizes durable stale-entry removal.
			if err := journal.DiscardAttempt(pending.authority.LeaseID, pending.authority.Attempt); err != nil {
				return err
			}
			fmt.Printf("journal_reconciliation=fenced attempt=%d\n", pending.authority.Attempt)
			continue
		}
		supervisor := newAttemptSupervisor(lease)
		err = reconcileAttempt(server, token, supervisor, pending)
		supervisor.Close()
		if err != nil {
			// Authentication, conflicts, transport failures, validation failures and
			// local guard expiry are not proof of fencing. Preserve the journal and
			// prevent heartbeat/claim traffic until a later startup can reconcile it.
			return err
		}
		if err := journal.DiscardAttempt(pending.authority.LeaseID, pending.authority.Attempt); err != nil {
			return err
		}
	}
	return nil
}

func probeReplayAuthority(server, token string, authority runner.AttemptIdentity) (domain.RunLease, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runnerHTTPClient.Timeout)
	defer cancel()
	query := url.Values{}
	query.Set("lease_id", authority.LeaseID)
	query.Set("attempt", fmt.Sprintf("%d", authority.Attempt))
	query.Set("fence", authority.Fence)
	var lease domain.RunLease
	err := getAPIIntoContext(ctx, server+"/api/v1/runners/lease?"+query.Encode(), token, &lease)
	if runnerHTTPStatus(err, 404) {
		var apiErr *runnerAPIError
		var envelope struct {
			Code string `json:"code"`
		}
		if !errors.As(err, &apiErr) || json.Unmarshal([]byte(apiErr.Detail), &envelope) != nil || envelope.Code != "not_found" {
			return domain.RunLease{}, false, fmt.Errorf("probe replay authority returned an unverified 404: %w", err)
		}
		return domain.RunLease{}, true, nil
	}
	if err != nil {
		return domain.RunLease{}, false, fmt.Errorf("probe replay authority: %w", err)
	}
	if lease.ID != authority.LeaseID || lease.RunID != authority.RunID || lease.Attempt != authority.Attempt || lease.Fence != authority.Fence || lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(time.Now()) {
		return domain.RunLease{}, false, errors.New("probe replay authority returned an invalid lease")
	}
	return lease, false, nil
}

func reconcileAttempt(server, token string, supervisor *attemptSupervisor, pending replayAttempt) error {
	if len(pending.completions) == 0 {
		var renewed domain.RunLease
		err := retryAttemptRequest(supervisor, func(ctx context.Context) error {
			return postAPIIntoContext(ctx, server+"/api/v1/runners/renew", struct {
				LeaseID string `json:"lease_id"`
				Attempt int    `json:"attempt"`
				Fence   string `json:"fence"`
			}{pending.authority.LeaseID, pending.authority.Attempt, pending.authority.Fence}, token, &renewed)
		})
		if err != nil {
			return err
		}
		supervisor.Update(renewed)
	}
	reporter := &attemptReporter{supervisor: supervisor, server: server, token: token, authority: pending.authority}
	for offset := 0; offset < len(pending.events); {
		batch := boundedJournalEventBatch(pending.events[offset:])
		if len(batch) == 0 {
			return errors.New("startup replay could not create a bounded event batch")
		}
		if err := retryAttemptRequest(supervisor, func(ctx context.Context) error { return reporter.postEvents(ctx, batch) }); err != nil {
			return err
		}
		offset += len(batch)
	}
	for _, completion := range pending.completions {
		var completed domain.RunLease
		err := retryAttemptRequest(supervisor, func(ctx context.Context) error {
			return postAPIIntoContext(ctx, server+"/api/v1/runners/complete", runnerCompleteRequest{
				LeaseID: completion.Attempt.LeaseID, Attempt: completion.Attempt.Attempt, Fence: completion.Attempt.Fence, CompletionKey: completion.ID, Status: completion.Status,
			}, token, &completed)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func retryAttemptRequest(supervisor *attemptSupervisor, request func(context.Context) error) error {
	backoff := 100 * time.Millisecond
	for {
		ctx, cancel, err := supervisor.RequestContext()
		if err != nil {
			return err
		}
		err = request(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if classifyRunnerFailure(err) != runnerFailureTransient {
			return err
		}
		if !waitRunnerRetry(supervisor.Context(), supervisor, backoff) {
			return fmt.Errorf("%w: replay retry deadline: %v", errLeaseAuthorityLost, err)
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

func groupReplayAttempts(snapshot runner.JournalSnapshot) []replayAttempt {
	byKey := map[string]*replayAttempt{}
	keyFor := func(a runner.AttemptIdentity) string {
		return fmt.Sprintf("%s\x00%d\x00%s\x00%s", a.LeaseID, a.Attempt, a.Fence, a.RunID)
	}
	add := func(a runner.AttemptIdentity) *replayAttempt {
		key := keyFor(a)
		group := byKey[key]
		if group == nil {
			group = &replayAttempt{authority: a}
			byKey[key] = group
		} else if a.ExpiresAt.After(group.authority.ExpiresAt) {
			group.authority.ExpiresAt = a.ExpiresAt
		}
		return group
	}
	for _, event := range snapshot.Events {
		group := add(event.Attempt)
		group.events = append(group.events, event)
	}
	for _, completion := range snapshot.Completions {
		group := add(completion.Attempt)
		group.completions = append(group.completions, completion)
	}
	attempts := make([]replayAttempt, 0, len(byKey))
	for _, attempt := range byKey {
		attempts = append(attempts, *attempt)
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].authority.CreatedAt.Before(attempts[j].authority.CreatedAt) })
	return attempts
}
