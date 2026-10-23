package runnerruntime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// HostingRuntimeLogsConfig enables demand-driven read-only log collection.
// TrustedLoopbackHTTP is available only to explicit programmatic fixtures.
type HostingRuntimeLogsConfig struct {
	TrustedLoopbackHTTP bool
	client              runtimeLogClient
	source              runtimeLogSource
}
type runtimeLogClient interface {
	ClaimRuntimeLogSessions(context.Context, domain.HostingRuntimeLogClaimInput, ...runnerclient.RuntimeLogTransport) (domain.HostingRuntimeLogClaims, error)
	AppendRuntimeLogs(context.Context, domain.HostingRuntimeLogBatch, ...runnerclient.RuntimeLogTransport) (domain.HostingRuntimeLogAck, error)
}

func validateHostingRuntimeLogsConfig(c Config) error {
	if c.HostingRuntimeLogs == nil {
		return nil
	}
	if !c.Execute || c.Once || c.CompleteStatus != "" || len(c.Token) > 4096 {
		return errors.New("runtime logs require execution and forbid once and generic completion")
	}
	return runnerclient.ValidateRuntimeLogServer(c.Server, runnerclient.RuntimeLogTransport{TrustedLoopbackHTTP: c.HostingRuntimeLogs.TrustedLoopbackHTTP})
}
func startHostingRuntimeLogs(parent context.Context, c Config) (func(), error) {
	settings := c.HostingRuntimeLogs
	client := settings.client
	if client == nil {
		client = runnerclient.New(c.Server, c.Token)
	}
	source := settings.source
	if source == nil {
		var err error
		source, err = newDockerRuntimeLogs(c.ImageDockerEndpoint)
		if err != nil {
			return nil, err
		}
	}
	instance, err := newRuntimeDeliveryID()
	if err != nil {
		source.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	collector := runtimeLogCollector{client: client, source: source, runner: c.RunnerID, token: c.Token, instance: instance, options: runnerclient.RuntimeLogTransport{TrustedLoopbackHTTP: settings.TrustedLoopbackHTTP}}
	go func() { defer close(done); defer source.Close(); collector.run(ctx) }()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }, nil
}

type runtimeLogCollector struct {
	client                  runtimeLogClient
	source                  runtimeLogSource
	runner, token, instance string
	options                 runnerclient.RuntimeLogTransport
}
type activeRuntimeLog struct {
	claim   domain.HostingRuntimeLogClaim
	updates chan runtimeLogGrant
	cancel  context.CancelFunc
}
type runtimeLogGrant struct {
	claim    domain.HostingRuntimeLogClaim
	deadline time.Time
}

func validRuntimeLogTarget(t domain.HostingRuntimeLogTarget, runner string) bool {
	return t.Validate() == nil && t.RunnerID == runner && dockerObjectID.MatchString(t.ContainerID) && imageNamespace.MatchString(t.RuntimeNamespace)
}
func validateRuntimeLogClaims(page domain.HostingRuntimeLogClaims, runner, instance string, started, received time.Time) ([]runtimeLogGrant, error) {
	invalid := errors.New("invalid runtime log claims")
	if page.Targets == nil || len(page.Targets) > 4 || (!page.EndOfSweep && page.NextCursor == "") || page.NextCursor != "" && !observationOpaqueID.MatchString(page.NextCursor) {
		return nil, invalid
	}
	previous := ""
	grants := make([]runtimeLogGrant, 0, len(page.Targets))
	for _, c := range page.Targets {
		remaining := c.EpochExpiresAt.Sub(received)
		if c.SessionID <= previous || !observationOpaqueID.MatchString(c.SessionID) || c.InstanceID != instance || !runtimeLogID(c.CollectionEpoch) || !validRuntimeLogTarget(c.Target, runner) || c.NextSequence < 1 || c.NextSequence > 2147483647 || c.NextBatchSequence < 1 || c.NextBatchSequence > 2147483647 || remaining <= time.Second || remaining > 10*time.Second || c.DemandExpiresAt.Before(c.EpochExpiresAt) || c.DemandExpiresAt.After(received.Add(30*time.Second)) || page.NextCursor < c.SessionID {
			return nil, invalid
		}
		previous = c.SessionID
		deadline := started.Add(remaining - time.Second)
		if !deadline.After(received) {
			return nil, invalid
		}
		grants = append(grants, runtimeLogGrant{c, deadline})
	}
	return grants, nil
}
func runtimeLogID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > 'f' || r > '9' && r < 'a' {
			return false
		}
	}
	return true
}
func (c *runtimeLogCollector) run(ctx context.Context) {
	for ctx.Err() == nil {
		// A failed or ambiguous session ends this bounded collection group. Waiting
		// out its last server lease ensures reacquisition has a new visible epoch gap.
		delay := c.round(ctx)
		if !waitWorkerInterval(ctx, delay) {
			return
		}
	}
}
func (c *runtimeLogCollector) round(ctx context.Context) time.Duration {
	roundCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	active := map[string]*activeRuntimeLog{}
	finished := make(chan struct{}, 4) // At most one completion per admitted collector.
	defer func() { cancel(); workers.Wait() }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastClaim := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-finished:
			return time.Until(lastClaim.Add(11 * time.Second))
		default:
		}
		started := time.Now()
		request, stop := context.WithTimeout(roundCtx, 2*time.Second)
		page, err := c.client.ClaimRuntimeLogSessions(request, domain.HostingRuntimeLogClaimInput{InstanceID: c.instance, Limit: 4}, c.options)
		stop()
		received := time.Now()
		// A failed response can hide an accepted renewal. Cooldown includes every
		// completed claim attempt, not only acknowledged leases.
		lastClaim = received
		if err == nil {
			grants, invalid := validateRuntimeLogClaims(page, c.runner, c.instance, started, received)
			if invalid != nil {
				return 11 * time.Second
			}
			lastClaim = received
			present := map[string]bool{}
			for _, grant := range grants {
				claim := grant.claim
				present[claim.SessionID] = true
				old := active[claim.SessionID]
				if old != nil {
					if old.claim.CollectionEpoch != claim.CollectionEpoch || !reflect.DeepEqual(old.claim.Target, claim.Target) {
						return 11 * time.Second
					}
					// Never overwrite publication counters from a renewal response.
					select {
					case old.updates <- grant:
					default:
					}
					continue
				}
				if len(active) >= 4 {
					continue
				}
				workerCtx, workerCancel := context.WithCancel(roundCtx)
				item := &activeRuntimeLog{claim: claim, updates: make(chan runtimeLogGrant, 1), cancel: workerCancel}
				active[claim.SessionID] = item
				workers.Go(func() { defer workerCancel(); c.session(workerCtx, grant, item.updates); finished <- struct{}{} })
			}
			if page.EndOfSweep {
				for id, item := range active {
					if !present[id] {
						item.cancel()
						return 11 * time.Second
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return 0
		case <-finished:
			return time.Until(lastClaim.Add(11 * time.Second))
		case <-ticker.C:
		}
	}
}

type runtimeLogQueue struct {
	events chan domain.HostingRuntimeLogEvent
	bytes  atomic.Int64
}

func (q *runtimeLogQueue) push(event domain.HostingRuntimeLogEvent) error {
	cost := int64(len(event.Message) + 128)
	if q.bytes.Add(cost) > 64<<10 {
		q.bytes.Add(-cost)
		return errRuntimeLogSource
	}
	select {
	case q.events <- event:
		return nil
	default:
		q.bytes.Add(-cost)
		return errRuntimeLogSource
	}
}
func (q *runtimeLogQueue) take(event domain.HostingRuntimeLogEvent) {
	q.bytes.Add(-int64(len(event.Message) + 128))
}
func (c *runtimeLogCollector) session(parent context.Context, grant runtimeLogGrant, updates <-chan runtimeLogGrant) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	deadline := grant.deadline
	watchdog := time.AfterFunc(time.Until(deadline), cancel)
	defer watchdog.Stop()
	verify, stop := context.WithTimeout(ctx, 2*time.Second)
	identity, err := c.source.Verify(verify, grant.claim.Target)
	stop()
	if err != nil {
		if errors.Is(err, errRuntimeLogDriver) {
			_ = c.publish(ctx, deadline, grant.claim, []domain.HostingRuntimeLogEvent{}, "unsupported_driver")
		}
		return
	}
	body, err := c.source.Open(ctx, grant.claim.Target)
	if err != nil {
		_ = c.publish(ctx, deadline, grant.claim, []domain.HostingRuntimeLogEvent{}, "disconnected")
		return
	}
	queue := runtimeLogQueue{events: make(chan domain.HostingRuntimeLogEvent, 64)}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer func() { _ = body.Close() }()
		_ = readRuntimeDockerLogs(ctx, body, identity.TTY, c.token, queue.push)
	}()
	defer func() { cancel(); _ = body.Close(); <-readerDone }()
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	events := []domain.HostingRuntimeLogEvent{}
	size := 0
	current := grant.claim
	send := func(state string) bool {
		if !c.publish(ctx, deadline, current, events, state) {
			return false
		}
		current.NextSequence += len(events)
		current.NextBatchSequence++
		events = []domain.HostingRuntimeLogEvent{}
		size = 0
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-readerDone:
			// Source loss is explicit; unflushed fragments are never reconstructed.
			_ = send("disconnected")
			return
		case renewed := <-updates:
			if !time.Now().Before(deadline) || renewed.claim.CollectionEpoch != current.CollectionEpoch || !reflect.DeepEqual(renewed.claim.Target, current.Target) {
				return
			}
			check, finish := context.WithTimeout(ctx, 2*time.Second)
			next, verifyErr := c.source.Verify(check, current.Target)
			finish()
			if verifyErr != nil || next != identity || !time.Now().Before(deadline) || ctx.Err() != nil {
				return
			}
			if !watchdog.Stop() {
				return
			}
			deadline = renewed.deadline
			if !deadline.After(time.Now()) {
				return
			}
			watchdog.Reset(time.Until(deadline))
		case event := <-queue.events:
			queue.take(event)
			// 32 KiB of message bytes bounds worst-case JSON escaping below 256 KiB.
			if len(events) == 64 || size+len(event.Message) > 32<<10 {
				if !send("following") {
					return
				}
			}
			events = append(events, event)
			size += len(event.Message)
		case <-flush.C:
			if !send("following") {
				return
			}
		}
	}
}
func (c *runtimeLogCollector) publish(ctx context.Context, deadline time.Time, claim domain.HostingRuntimeLogClaim, events []domain.HostingRuntimeLogEvent, state string) bool {
	id, err := newRuntimeDeliveryID()
	if err != nil {
		return false
	}
	body, err := (domain.HostingRuntimeLogBatch{SessionID: claim.SessionID, InstanceID: claim.InstanceID, CollectionEpoch: claim.CollectionEpoch, BatchID: id, ExpectedSequence: claim.NextSequence - 1, BatchSequence: claim.NextBatchSequence, Events: events, State: state}).Canonical()
	if err != nil {
		return false
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		authority, stop := context.WithDeadline(ctx, deadline)
		request, finish := context.WithTimeout(authority, 2*time.Second)
		_, err = c.client.AppendRuntimeLogs(request, body, c.options)
		finish()
		stop()
		if err == nil {
			return ctx.Err() == nil && time.Now().Before(deadline)
		}
		if runnerclient.ClassifyFailure(err) != runnerclient.FailureTransient {
			return false
		}
	}
	return false
}
