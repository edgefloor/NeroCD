package runnerruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// HostingReadinessConfig opts into sequential, read-only host diagnostics.
// TrustedLoopbackHTTP exists only for explicit local tests, never CLI configuration.
type HostingReadinessConfig struct {
	TrustedLoopbackHTTP bool
	client              readinessClient
	sampler             readinessSampler
	interval            time.Duration
}
type readinessClient interface {
	ServerReadinessChallenge(context.Context, string, ...runnerclient.ServerReadinessTransport) (domain.ServerReadinessChallenge, error)
	ReportServerReadiness(context.Context, domain.ServerReadinessReport, ...runnerclient.ServerReadinessTransport) (domain.ServerReadinessAck, error)
}
type readinessSampler interface {
	Sample(context.Context) domain.ServerReadinessReport
}

type readinessCollector struct {
	client   readinessClient
	sampler  readinessSampler
	runnerID string
	options  runnerclient.ServerReadinessTransport
	interval time.Duration
}

func validateHostingReadinessConfig(c Config) error {
	if c.HostingReadiness == nil {
		return nil
	}
	if !c.Execute || c.Once || c.CompleteStatus != "" {
		return errors.New("hosting readiness requires execution and forbids once and generic completion")
	}
	return runnerclient.ValidateServerReadinessServer(c.Server, runnerclient.ServerReadinessTransport{TrustedLoopbackHTTP: c.HostingReadiness.TrustedLoopbackHTTP})
}

func startHostingReadiness(parent context.Context, c Config, journalRoot string) (func(), error) {
	settings := c.HostingReadiness
	collector := readinessCollector{client: settings.client, sampler: settings.sampler, runnerID: c.RunnerID, options: runnerclient.ServerReadinessTransport{TrustedLoopbackHTTP: settings.TrustedLoopbackHTTP}, interval: 10 * time.Second}
	if collector.client == nil {
		collector.client = runnerclient.New(c.Server, c.Token)
	}
	closeSampler := func() {}
	if collector.sampler == nil {
		sampler, err := newReadinessSampler(c.ImageDockerEndpoint, c.WorkDir, journalRoot)
		if err != nil {
			return nil, err
		}
		collector.sampler = sampler
		closeSampler = sampler.Close
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

func (c *readinessCollector) run(ctx context.Context) {
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
func (c *readinessCollector) cycle(parent context.Context) {
	cycle, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	fetch, stop := context.WithTimeout(cycle, 2*time.Second)
	challenge, err := c.client.ServerReadinessChallenge(fetch, c.runnerID, c.options)
	stop()
	now := time.Now()
	if err != nil || cycle.Err() != nil || runnerclient.ValidateServerReadinessChallenge(challenge, c.runnerID, now) != nil {
		return
	}
	sampleWindow, stopWindow := context.WithDeadline(cycle, challenge.ExpiresAt)
	defer stopWindow()
	sampleCtx, stopSample := context.WithTimeout(sampleWindow, 5*time.Second)
	started := time.Now()
	body := c.sampler.Sample(sampleCtx)
	elapsed := time.Since(started)
	late := sampleCtx.Err() != nil
	stopSample()
	// Filesystem syscalls run synchronously on the qualified local filesystem.
	// They cannot be forcibly cancelled. Late samples are discarded, never sent.
	if late || elapsed > 5*time.Second || sampleWindow.Err() != nil || !time.Now().Before(challenge.ExpiresAt) {
		return
	}
	body.SampleID = challenge.SampleID
	body.Profile = challenge.Profile
	body.DurationMS = int(elapsed.Milliseconds())
	if body.Validate() != nil {
		return
	}
	// Retry at most once using the exact same body and original cycle/deadline.
	for attempt := 0; attempt < 2; attempt++ {
		if sampleWindow.Err() != nil || !time.Now().Before(challenge.ExpiresAt) {
			return
		}
		report, stopReport := context.WithTimeout(sampleWindow, 2*time.Second)
		_, reportErr := c.client.ReportServerReadiness(report, body, c.options)
		stopReport()
		if reportErr == nil {
			return
		}
		if runnerclient.ClassifyFailure(reportErr) != runnerclient.FailureTransient {
			return
		}
	}
}
