package runnerruntime

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

const observationPageSize = 4
const observationCadence = 10 * time.Second

// HostingObservationConfig enables independent read-only collection. HTTP is
// permitted only for explicit literal-loopback fixtures, never by the CLI.
type HostingObservationConfig struct {
	TrustedLoopbackHTTP bool
	client              hostingObservationClient
	sampler             hostingObservationSampler
	interval            time.Duration
}
type hostingObservationClient interface {
	HostingObservationTargets(context.Context, domain.HostingObservationTargetsInput, ...runnerclient.ObservationTransport) (domain.HostingObservationTargets, error)
	ReportHostingObservation(context.Context, domain.HostingRuntimeObservation, ...runnerclient.ObservationTransport) (domain.HostingObservationAck, error)
}
type hostingObservationSampler interface {
	Sample(context.Context, domain.HostingObservationTarget) domain.HostingRuntimeObservation
}

type hostingObservationCollector struct {
	client           hostingObservationClient
	sampler          hostingObservationSampler
	runnerID, cursor string
	options          runnerclient.ObservationTransport
	interval         time.Duration
}

func validateHostingObservationConfig(c Config) error {
	if c.HostingObservation == nil {
		return nil
	}
	if !c.Execute || c.Once || c.CompleteStatus != "" {
		return errors.New("hosting observation requires execution and forbids once and generic completion")
	}
	return runnerclient.ValidateObservationServer(c.Server, runnerclient.ObservationTransport{TrustedLoopbackHTTP: c.HostingObservation.TrustedLoopbackHTTP})
}
func startHostingObservation(parent context.Context, c Config) (func(), error) {
	settings := c.HostingObservation
	collector := hostingObservationCollector{client: settings.client, sampler: settings.sampler, runnerID: c.RunnerID, options: runnerclient.ObservationTransport{TrustedLoopbackHTTP: settings.TrustedLoopbackHTTP}, interval: observationCadence}
	if collector.client == nil {
		collector.client = runnerclient.New(c.Server, c.Token)
	}
	closeSampler := func() {}
	if collector.sampler == nil {
		sampler, err := newImageObservation(c.ImageDockerEndpoint)
		if err != nil {
			return nil, err
		}
		collector.sampler = sampler
		closeSampler = func() { _ = sampler.Close() }
	}
	if settings.interval > 0 {
		collector.interval = settings.interval
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { defer close(done); defer closeSampler(); collector.run(ctx) }()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }, nil
}
func (c *hostingObservationCollector) run(ctx context.Context) {
	for ctx.Err() == nil {
		started := time.Now()
		c.cycle(ctx)
		delay := c.interval - time.Since(started)
		if delay < 0 {
			delay = 0
		}
		if !waitWorkerInterval(ctx, delay) {
			return
		}
	}
}
func (c *hostingObservationCollector) cycle(ctx context.Context) {
	fetch, cancel := context.WithTimeout(ctx, 2*time.Second)
	page, err := c.client.HostingObservationTargets(fetch, domain.HostingObservationTargetsInput{AfterBindingID: c.cursor, Limit: observationPageSize}, c.options)
	cancel()
	received := time.Now()
	if err != nil || ctx.Err() != nil || validateObservationPage(page, c.runnerID, c.cursor, received) != nil {
		return
	}
	// At most four samples exist. No queued page or unbounded worker queue exists.
	var samples sync.WaitGroup
	for _, target := range page.Targets {
		remaining := target.ExpiresAt.Sub(received)
		if remaining > 10*time.Second {
			remaining = 10 * time.Second
		}
		deadline := received.Add(remaining)
		samples.Go(func() {
			started := time.Now()
			sampleCtx, stop := context.WithDeadline(ctx, deadline)
			defer stop()
			bounded, finish := context.WithTimeout(sampleCtx, 5*time.Second)
			body := c.sampler.Sample(bounded, target)
			finish()
			duration := time.Since(started)
			if ctx.Err() != nil || duration > 5*time.Second || !time.Now().Before(deadline) {
				return
			}
			body.BindingID, body.SampleID, body.SampleDurationMS = target.BindingID, target.SampleID, int(duration.Milliseconds())
			if body.Validate() != nil {
				return
			}
			// Both attempts use the identical canonical body and original challenge.
			for attempt := 0; attempt < 2; attempt++ {
				report, stopReport := context.WithTimeout(sampleCtx, time.Second)
				_, reportErr := c.client.ReportHostingObservation(report, body, c.options)
				stopReport()
				if reportErr == nil || runnerclient.ClassifyFailure(reportErr) != runnerclient.FailureTransient || sampleCtx.Err() != nil {
					return
				}
			}
		})
	}
	samples.Wait()
	if page.EndOfSweep {
		c.cursor = ""
	} else {
		c.cursor = page.NextCursor
	}
}

var observationOpaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var observationSampleID = regexp.MustCompile(`^hos_[a-f0-9]{32}$`)
var observationDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validateObservationPage(page domain.HostingObservationTargets, runnerID, after string, now time.Time) error {
	invalid := errors.New("invalid hosting observation targets")
	if page.Targets == nil || len(page.Targets) > observationPageSize {
		return invalid
	}
	if page.NextCursor != "" && (!observationOpaqueID.MatchString(page.NextCursor) || page.NextCursor <= after) {
		return invalid
	}
	if !page.EndOfSweep && page.NextCursor == "" {
		return invalid
	}
	previous := after
	seen := map[string]bool{}
	for _, target := range page.Targets {
		if validateObservationTarget(target, runnerID, now) != nil || target.BindingID <= previous || page.NextCursor < target.BindingID {
			return invalid
		}
		previous = target.BindingID
		for _, key := range []string{"sample:" + target.SampleID, "container:" + target.ContainerID, "resource:" + target.ResourceID, "operation:" + target.OperationID} {
			if seen[key] {
				return invalid
			}
			seen[key] = true
		}
	}
	return nil
}
func validateObservationTarget(t domain.HostingObservationTarget, runnerID string, now time.Time) error {
	invalid := errors.New("invalid hosting observation target")
	for _, id := range []string{t.ResourceID, t.BindingID, t.DestinationID, t.ServerID, t.RunnerID, t.OperationID, t.DaemonID} {
		if !observationOpaqueID.MatchString(id) {
			return invalid
		}
	}
	if t.RunnerID != runnerID || !observationSampleID.MatchString(t.SampleID) || !dockerObjectID.MatchString(t.ContainerID) || !observationDigest.MatchString(t.ImageID) || !observationDigest.MatchString(t.ReleaseHash) || !observationDigest.MatchString(t.PlanHash) || !imageNamespace.MatchString(t.RuntimeNamespace) || t.SampleTimeoutSeconds != 5 || t.HostPort < 1024 {
		return invalid
	}
	if t.IssuedAt.IsZero() || t.ExpiresAt.Sub(t.IssuedAt) != 10*time.Second || t.IssuedAt.After(now.Add(time.Second)) || !t.ExpiresAt.After(now) {
		return invalid
	}
	if strings.Contains(t.ReadinessPath, "\uFFFD") {
		return invalid
	}
	return domain.ValidateHostingDeployment(domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "observation", ContainerPort: t.ContainerPort, HostPort: t.HostPort, Readiness: domain.HostingReadiness{Path: t.ReadinessPath, TimeoutSeconds: 5}})
}
