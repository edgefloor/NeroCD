package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type readinessSampleFunc func(context.Context) domain.ServerReadinessReport

func (f readinessSampleFunc) Sample(ctx context.Context) domain.ServerReadinessReport { return f(ctx) }

type readinessClientFixture struct {
	challenge func(context.Context) (domain.ServerReadinessChallenge, error)
	report    func(context.Context, domain.ServerReadinessReport) (domain.ServerReadinessAck, error)
}

func (f readinessClientFixture) ServerReadinessChallenge(ctx context.Context, _ string, _ ...runnerclient.ServerReadinessTransport) (domain.ServerReadinessChallenge, error) {
	return f.challenge(ctx)
}
func (f readinessClientFixture) ReportServerReadiness(ctx context.Context, in domain.ServerReadinessReport, _ ...runnerclient.ServerReadinessTransport) (domain.ServerReadinessAck, error) {
	return f.report(ctx, in)
}
func readinessChallengeFixture() domain.ServerReadinessChallenge {
	now := time.Now().UTC()
	return domain.ServerReadinessChallenge{SampleID: "srd_" + strings.Repeat("a", 32), Profile: domain.ServerReadinessProfile, ServerID: "server", DestinationID: "destination", RunnerID: "runner", IssuedAt: now, ExpiresAt: now.Add(10 * time.Second)}
}
func readinessReportFixture() domain.ServerReadinessReport {
	return domain.ServerReadinessReport{OS: "unknown", Architecture: "unknown", Execution: "enabled", Configuration: "matched", Docker: "unreachable", DaemonOS: "unknown", DaemonArchitecture: "unknown", CLI: "unknown", WorkRoot: "unknown", JournalRoot: "unknown"}
}

func TestReadinessCollectorRetryBudgetAndImmutableBody(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var reports []domain.ServerReadinessReport
			challenges, samples := 0, 0
			collector := readinessCollector{runnerID: "runner", client: readinessClientFixture{challenge: func(ctx context.Context) (domain.ServerReadinessChallenge, error) {
				challenges++
				if deadline, _ := ctx.Deadline(); time.Until(deadline) > 2*time.Second {
					t.Error("challenge deadline")
				}
				return readinessChallengeFixture(), nil
			}, report: func(ctx context.Context, in domain.ServerReadinessReport) (domain.ServerReadinessAck, error) {
				if deadline, _ := ctx.Deadline(); time.Until(deadline) > 2*time.Second {
					t.Error("report deadline")
				}
				reports = append(reports, in)
				return domain.ServerReadinessAck{}, &runnerclient.APIError{StatusCode: status}
			}}, sampler: readinessSampleFunc(func(ctx context.Context) domain.ServerReadinessReport {
				samples++
				if deadline, _ := ctx.Deadline(); time.Until(deadline) > 5*time.Second {
					t.Error("sample deadline")
				}
				return readinessReportFixture()
			})}
			collector.cycle(t.Context())
			want := 1
			if status == 500 {
				want = 2
			}
			if len(reports) != want || samples != 1 || challenges != 1 {
				t.Fatal(len(reports), samples, challenges)
			}
			if want == 2 && !reflect.DeepEqual(reports[0], reports[1]) {
				t.Fatal("retry refreshed body")
			}
		})
	}
}

func TestReadinessCollectorDiscardsInvalidAndLateSamples(t *testing.T) {
	for _, mode := range []string{"profile", "runner", "late"} {
		t.Run(mode, func(t *testing.T) {
			samples, reports := 0, 0
			collector := readinessCollector{runnerID: "runner", client: readinessClientFixture{challenge: func(context.Context) (domain.ServerReadinessChallenge, error) {
				c := readinessChallengeFixture()
				if mode == "profile" {
					c.Profile = "other"
				}
				if mode == "runner" {
					c.RunnerID = "other"
				}
				if mode == "late" {
					c.IssuedAt = c.IssuedAt.Add(-9950 * time.Millisecond)
					c.ExpiresAt = c.IssuedAt.Add(10 * time.Second)
				}
				return c, nil
			}, report: func(context.Context, domain.ServerReadinessReport) (domain.ServerReadinessAck, error) {
				reports++
				return domain.ServerReadinessAck{}, nil
			}}, sampler: readinessSampleFunc(func(context.Context) domain.ServerReadinessReport {
				samples++
				time.Sleep(80 * time.Millisecond)
				return readinessReportFixture()
			})}
			collector.cycle(t.Context())
			if reports != 0 || mode != "late" && samples != 0 {
				t.Fatal(samples, reports)
			}
		})
	}
}

func TestReadinessCollectorImmediateSequentialAndJoined(t *testing.T) {
	started := make(chan struct{})
	joined := make(chan struct{})
	var calls atomic.Int32
	config := Config{RunnerID: "runner", HostingReadiness: &HostingReadinessConfig{client: readinessClientFixture{challenge: func(context.Context) (domain.ServerReadinessChallenge, error) {
		return readinessChallengeFixture(), nil
	}}, sampler: readinessSampleFunc(func(ctx context.Context) domain.ServerReadinessReport {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(joined)
		return readinessReportFixture()
	}), interval: time.Millisecond}}
	stop, err := startHostingReadiness(t.Context(), config, "unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no immediate collection")
	}
	stop()
	stop()
	select {
	case <-joined:
	default:
		t.Fatal("stop returned before sampler joined")
	}
	if calls.Load() != 1 {
		t.Fatal("overlapping samples")
	}
	var active, max, cycles atomic.Int32
	client := readinessClientFixture{challenge: func(context.Context) (domain.ServerReadinessChallenge, error) {
		return readinessChallengeFixture(), nil
	}, report: func(context.Context, domain.ServerReadinessReport) (domain.ServerReadinessAck, error) {
		cycles.Add(1)
		return domain.ServerReadinessAck{SampleID: readinessChallengeFixture().SampleID, Accepted: true, ObservedAt: time.Now()}, nil
	}}
	config.HostingReadiness = &HostingReadinessConfig{client: client, interval: time.Millisecond, sampler: readinessSampleFunc(func(context.Context) domain.ServerReadinessReport {
		a := active.Add(1)
		if a > max.Load() {
			max.Store(a)
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return readinessReportFixture()
	})}
	stop, err = startHostingReadiness(t.Context(), config, "unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(time.Second)
	for cycles.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if max.Load() != 1 || cycles.Load() < 2 {
		t.Fatal(max.Load(), cycles.Load())
	}
}

func TestReadinessRuntimeIdentityAndStartupFailureLeaveCollectorUnstarted(t *testing.T) {
	var collected atomic.Int32
	var capable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/identity" {
			t.Error("startup reached lifecycle before journal check")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": func() []string {
			if capable.Load() {
				return []string{domain.ServerReadinessCapability}
			}
			return nil
		}()})
	}))
	defer server.Close()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	if err := os.Mkdir(journal, 0755); err != nil {
		t.Fatal(err)
	}
	config := Config{Server: server.URL, Token: "fixture", RunnerID: "runner", WorkDir: root, JournalDir: journal, Execute: true, HostingReadiness: &HostingReadinessConfig{TrustedLoopbackHTTP: true, client: readinessClientFixture{challenge: func(context.Context) (domain.ServerReadinessChallenge, error) {
		collected.Add(1)
		return readinessChallengeFixture(), errors.New("unexpected")
	}}, sampler: readinessSampleFunc(func(context.Context) domain.ServerReadinessReport { return readinessReportFixture() })}}
	if err := runtimeEnvironmentIdentityCheck(t, config); err == nil {
		t.Fatal("missing authenticated readiness capability")
	}
	capable.Store(true)
	if err := runtimeEnvironmentIdentityCheck(t, config); err != nil {
		t.Fatal(err)
	}
	if err := RunContext(t.Context(), config); err == nil {
		t.Fatal("unsafe journal accepted")
	}
	if collected.Load() != 0 {
		t.Fatal("collector started before journal startup")
	}
	config.HostingReadiness = nil
	if err := runtimeEnvironmentIdentityCheck(t, config); err == nil {
		t.Fatal("unconfigured authenticated capability accepted")
	}
}
func runtimeEnvironmentIdentityCheck(t *testing.T, c Config) error {
	t.Helper()
	_, err := runtimeEnvironmentIdentity(t.Context(), c)
	return err
}

func TestReadinessCollectorWaitsForDurableReplay(t *testing.T) {
	root := t.TempDir()
	journalRoot := filepath.Join(root, "journal")
	journal, err := runner.OpenAttemptJournal(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	appendReplayTestEvent(t, journal, replayTestLease())
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	var probes, collected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": []string{domain.ServerReadinessCapability}})
		case "/api/v1/runners/lease":
			probes.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Error("later startup traffic escaped unfinished replay")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	config := Config{Server: server.URL, Token: "fixture", RunnerID: "runner", WorkDir: root, JournalDir: journalRoot, Execute: true, HostingReadiness: &HostingReadinessConfig{TrustedLoopbackHTTP: true, client: readinessClientFixture{challenge: func(context.Context) (domain.ServerReadinessChallenge, error) {
		collected.Add(1)
		return readinessChallengeFixture(), nil
	}}, sampler: readinessSampleFunc(func(context.Context) domain.ServerReadinessReport { return readinessReportFixture() })}}
	if err = RunContext(t.Context(), config); err == nil {
		t.Fatal("unfinished replay allowed runtime startup")
	}
	if probes.Load() != 1 || collected.Load() != 0 {
		t.Fatal("collector did not wait for replay", probes.Load(), collected.Load())
	}
	journal, err = runner.OpenAttemptJournal(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if journal.Depth() != 1 {
		t.Fatal("failed replay changed journal")
	}
}
