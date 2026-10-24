//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type databaseNetworkCaptureNextClient interface {
	ClaimNextHostingDatabaseNetworkCapture(context.Context, string) (domain.HostingDatabaseNetworkCaptureClaim, error)
}

type automaticCaptureDispatcher struct {
	config        Config
	journal       databaseNetworkCaptureJournal
	client        databaseNetworkCaptureClient
	selection     databaseNetworkCaptureNextClient
	open          captureRuntimeObserverFactory
	preflight     func(context.Context) error
	hostValidated bool
}

func newAutomaticDatabaseNetworkCaptureDispatcher(c Config, j *runner.AttemptJournal) (automaticDatabaseNetworkCaptureDispatcher, error) {
	return newAutomaticDatabaseNetworkCaptureDispatcherWith(c, j, runnerclient.New(c.Server, c.Token), openCaptureRuntimeObserver, func(ctx context.Context) error {
		return ValidateDatabaseExecution(ctx, *c.Database, c.ImageDockerEndpoint)
	})
}

func newAutomaticDatabaseNetworkCaptureDispatcherWith(c Config, j databaseNetworkCaptureJournal, client databaseNetworkCaptureClient, open captureRuntimeObserverFactory, preflight func(context.Context) error) (automaticDatabaseNetworkCaptureDispatcher, error) {
	if c.Database == nil || !c.Database.NetworkCapture {
		return nil, nil
	}
	if err := validateAutomaticDatabaseNetworkCaptureConfig(c); err != nil {
		return nil, err
	}
	selection, supported := client.(databaseNetworkCaptureNextClient)
	if !supported {
		return nil, errors.New("automatic database capture selection is unsupported by this client")
	}
	if j == nil || open == nil || preflight == nil {
		return nil, errDatabaseNetworkCaptureExecution
	}
	return &automaticCaptureDispatcher{config: c, journal: j, client: client, selection: selection, open: open, preflight: preflight}, nil
}

func (d *automaticCaptureDispatcher) Reconcile(ctx context.Context) error {
	entries := d.journal.Snapshot().DatabaseNetworkCapture
	if len(entries) == 0 {
		if !captureRuntimeJournalCompatible(d.journal.Snapshot(), "") {
			return d.BeforeOrdinary(ctx)
		}
		return nil
	}
	id := strings.TrimPrefix(entries[0].Attempt.RunID, "capture_")
	if !captureRuntimeJournalCompatible(d.journal.Snapshot(), id) {
		return errDatabaseNetworkCaptureExecution
	}
	chain, err := captureExecutionChain(d.journal, d.config.RunnerID, id)
	if err != nil || len(chain) == 0 || len(chain) < 3 && !chain[0].Attempt.ExpiresAt.After(time.Now()) {
		return errDatabaseNetworkCaptureExecution
	}
	return d.execute(ctx, func(observer databaseNetworkCaptureObserver) error {
		return replayDatabaseNetworkCapture(ctx, d.journal, d.client, observer, d.config.RunnerID, id)
	})
}

func (d *automaticCaptureDispatcher) Poll(ctx context.Context) (captureDispatchOutcome, error) {
	if !captureRuntimeJournalCompatible(d.journal.Snapshot(), "") {
		return captureDispatchEmpty, errDatabaseNetworkCaptureExecution
	}
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	claim, err := d.selection.ClaimNextHostingDatabaseNetworkCapture(request, d.config.RunnerID)
	requestErr := request.Err()
	cancel()
	if err != nil {
		switch {
		case runnerHTTPStatus(err, http.StatusNotFound) && requestErr == nil:
			return captureDispatchEmpty, nil
		case runnerHTTPStatus(err, http.StatusConflict) && requestErr == nil:
			return captureDispatchContended, nil
		default:
			return captureDispatchEmpty, captureExecutionFailure(ctx)
		}
	}
	if requestErr != nil {
		return captureDispatchEmpty, captureExecutionFailure(ctx)
	}
	err = d.execute(ctx, func(observer databaseNetworkCaptureObserver) error {
		return executeClaimedDatabaseNetworkCapture(ctx, d.journal, d.client, observer, d.config.RunnerID, claim)
	})
	return captureDispatchCompleted, err
}

func (d *automaticCaptureDispatcher) BeforeOrdinary(ctx context.Context) error {
	if ctx.Err() != nil {
		return captureExecutionFailure(ctx)
	}
	if !d.hostValidated {
		if err := d.preflight(ctx); err != nil {
			return errDatabaseNetworkCaptureExecution
		}
		if ctx.Err() != nil {
			return captureExecutionFailure(ctx)
		}
		d.hostValidated = true
	}
	return nil
}

func (d *automaticCaptureDispatcher) execute(ctx context.Context, work func(databaseNetworkCaptureObserver) error) (err error) {
	observer := &lazyCaptureRuntimeObserver{config: DatabaseEngineConfig{stateRoot: d.config.Database.StateRoot, secretRoot: d.config.Database.SecretRoot}, endpoint: d.config.ImageDockerEndpoint, open: d.open}
	defer func() {
		if observer.Close() != nil {
			err = errors.Join(err, errDatabaseNetworkCaptureExecution)
		}
	}()
	return work(observer)
}

// The journal and live plan checks precede this first host access. The caller
// owns Close and joins the synchronous observation before retiring its lifetime.
type lazyCaptureRuntimeObserver struct {
	config   DatabaseEngineConfig
	endpoint string
	open     captureRuntimeObserverFactory
	observer databaseNetworkCaptureObserver
	close    func() error
}

func (o *lazyCaptureRuntimeObserver) ObserveDatabaseNetworkOrigin(ctx context.Context, plan domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
	if o.observer == nil {
		observer, closeObserver, err := o.open(ctx, o.config, o.endpoint)
		if err != nil || observer == nil || closeObserver == nil {
			if closeObserver != nil {
				_ = closeObserver()
			}
			return domain.HostingDatabaseNetworkOrigin{}, errDatabaseNetworkCaptureExecution
		}
		o.observer, o.close = observer, closeObserver
	}
	return o.observer.ObserveDatabaseNetworkOrigin(ctx, plan, before)
}

func (o *lazyCaptureRuntimeObserver) Close() error {
	if o.close != nil {
		return o.close()
	}
	return nil
}
