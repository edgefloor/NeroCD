//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"net/http"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// Database recovery precedes all other hosting recovery. It never reapplies an
// admitted writer, infers a candidate identity, or refreshes deploy credentials.
func reconcileHostingDatabaseJournal(parent context.Context, config Config, j *runner.AttemptJournal) error {
	return reconcileDatabaseJournalWithEngine(parent, config, j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
		if config.Database == nil {
			return nil, DatabaseEngineConfig{}, errors.New("database journal requires explicit execution configuration")
		}
		ec, err := NewDatabaseEngineConfig(config.Database.StateRoot, config.Database.SecretRoot)
		if err != nil {
			return nil, ec, errors.New("database persistent configuration unavailable")
		}
		e, err := newImageEngine(config.ImageDockerEndpoint)
		if err != nil {
			return nil, ec, err
		}
		if err = validateDatabaseDaemon(parent, e, ec); err != nil {
			_ = e.Close()
			return nil, ec, err
		}
		return e, ec, nil
	})
}
func reconcileDatabaseJournalWithEngine(parent context.Context, config Config, j *runner.AttemptJournal, open func() (databaseExecutionEngine, DatabaseEngineConfig, error)) error {
	if config.Database != nil {
		parent = runnerclient.WithDatabaseTransport(parent, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: config.Database.TrustedLoopbackHTTP})
	}
	c := runnerclient.New(config.Server, config.Token)
	seen := map[domain.HostingAuthority]bool{}
	for _, first := range j.Snapshot().Hosting {
		if !runner.HostingJournalUsesDatabase(first) {
			continue
		}
		if config.Database == nil {
			return errors.New("database journal requires explicit execution configuration")
		}
		a := storageEntryAuthority(first)
		if seen[a] {
			continue
		}
		seen[a] = true
		if err := reconcileDatabaseAttempt(parent, config, c, j, first, open); err != nil {
			return err
		}
	}
	return nil
}
func reconcileDatabaseAttempt(parent context.Context, config Config, c runnerclient.Client, j *runner.AttemptJournal, first runner.JournalHosting, open func() (databaseExecutionEngine, DatabaseEngineConfig, error)) error {
	a := storageEntryAuthority(first)
	ref := first.DatabaseRef
	rejectedResult := false
	for _, entry := range j.Snapshot().Hosting {
		if storageEntryAuthority(entry) != a {
			continue
		}
		if !entry.Database || entry.DatabaseRef != ref || entry.StorageRef != first.StorageRef {
			return errors.New("database recovery journal family changed")
		}
		if entry.Result == nil && entry.Prepared == nil && entry.StorageMaterialized == nil {
			continue
		}
		if entry.Result != nil {
			if err := replayHostingEvents(config.Server, config.Token, j, entry.Attempt, parent); err != nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		var op domain.HostingOperation
		var err error
		switch {
		case entry.Result != nil:
			op, err = c.HostingComplete(ctx, *entry.Result)
			if err != nil && runnerHTTPStatus(err, http.StatusConflict) {
				rejectedResult = true
				cancel()
				continue
			}
			if err == nil && (!databaseOperationScope(op, a, config.RunnerID, "deploy", ref) || op.Result == nil || *op.Result != entry.Result.Result || op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.Result.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(entry.Result.Result)) {
				err = errors.New("database completion replay changed exact receipt")
			}
		case entry.Prepared != nil:
			op, err = c.HostingPrepared(ctx, *entry.Prepared)
			if err == nil && (!databaseOperationScope(op, a, config.RunnerID, "deploy", ref) || !databaseOperationPlan(op) || *op.Release != entry.Prepared.Release || op.PreparedReceipt == nil || op.PreparedReceipt.ID != entry.Prepared.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(entry.Prepared.Release)) {
				err = errors.New("database preparation replay changed exact receipt")
			}
		case entry.StorageMaterialized != nil:
			_, err = journalHostingStorageMaterialized(ctx, j, c, entry)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
	cancel()
	if err != nil {
		return err
	}
	if !databaseOperationScope(op, a, config.RunnerID, "deploy", ref) || op.Snapshot.Storage != first.StorageRef {
		return errors.New("database recovery target changed")
	}
	for _, entry := range j.Snapshot().Hosting {
		if storageEntryAuthority(entry) == a && entry.Plan != nil && (!databaseOperationPlan(op) || domain.HostingDocumentHash(entry.Plan) != domain.HostingDocumentHash(op.Plan)) {
			return errors.New("database recovery immutable plan changed")
		}
	}
	if !rejectedResult && databaseSettled(op, a, config.RunnerID, ref) {
		return retireDatabaseAttempt(parent, j, a)
	}
	if op.RestoreOwnershipHeld {
		return errors.New("database restore ownership remains unresolved")
	}
	if op.MutationHold && op.ReconcileRequested {
		if err := observeDatabaseRecovery(parent, config, c, op, a, open); err != nil {
			return err
		}
		// A rejected completion remains evidence until the controller acknowledges
		// that exact receipt. Observation cannot silently erase it.
		if rejectedResult {
			return errors.New("database rejected completion evidence retained after observation")
		}
		ctx, cancel = context.WithTimeout(parent, 5*time.Second)
		settled, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
		cancel()
		if err != nil {
			return err
		}
		if databaseSettled(settled, a, config.RunnerID, ref) {
			return retireDatabaseAttempt(parent, j, a)
		}
		return errors.New("database observed state lacks exact terminal settlement")
	}
	if rejectedResult {
		return errors.New("database completion was not acknowledged; evidence retained")
	}
	if op.State != "prepared" || op.Result != nil || !databaseOperationPlan(op) {
		return errors.New("database possible effects remain held; owner observation required")
	}
	// Never continue beyond the original recorded window, even if a renewed
	// lease would otherwise permit a longer request.
	for _, entry := range j.Snapshot().Hosting {
		if storageEntryAuthority(entry) == a && entry.Phase == "apply_intent" && (!time.Now().Before(entry.CreatedAt.Add(time.Minute)) || !time.Now().Before(entry.Attempt.ExpiresAt)) {
			return errors.New("database original delivery window expired; evidence retained")
		}
	}
	lease, fenced, err := probeReplayAuthority(config.Server, config.Token, first.Attempt, parent)
	if err != nil {
		return err
	}
	if fenced || lease.RunnerID != config.RunnerID {
		return errors.New("database prepared replay lost original authority")
	}
	s := newAttemptSupervisorFrom(parent, lease)
	defer s.Close()
	renewer := startLeaseRenewer(s.Context(), s, config.Server, config.Token, lease, time.Second)
	defer renewer.Stop()
	watcher := startLeaseWatcher(s.Context(), config.Server, config.Token, lease, time.Second, s.cancel)
	defer watcher.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, config.Server, config.Token, a.RunID, lease)
	defer reporter.Stop()
	e, ec, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	return executeDatabasePrepared(s, j, reporter.WaitEmpty, c, e, a, lease, op, *config.Database, ec)
}
func observeDatabaseRecovery(parent context.Context, config Config, c runnerclient.Client, op domain.HostingOperation, a domain.HostingAuthority, open func() (databaseExecutionEngine, DatabaseEngineConfig, error)) error {
	if op.Result == nil || op.Result.ContainerID == "" {
		return errors.New("database observation requires exact durable candidate")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	claimed, err := c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "claim_observation"})
	cancel()
	if err != nil {
		return err
	}
	if claimed.ID != op.ID || claimed.TaskMode != "observe" || claimed.ObservationLease == nil || claimed.ObservationLease.RunID != claimed.ReconciliationRunID || claimed.ObservationLease.RunnerID != config.RunnerID || claimed.ObservationLease.Status != domain.LeaseActive {
		return errors.New("database fresh observation authority unavailable")
	}
	lease := *claimed.ObservationLease
	s := newAttemptSupervisorFrom(parent, lease)
	defer s.Close()
	renewer := startLeaseRenewer(s.Context(), s, config.Server, config.Token, lease, time.Second)
	defer renewer.Stop()
	watcher := startLeaseWatcher(s.Context(), config.Server, config.Token, lease, time.Second, s.cancel)
	defer watcher.Stop()
	observeA := hostingAuthority(lease.RunID, op.ID, lease)
	var plan domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error { var err error; plan, err = c.HostingPlan(ctx, observeA); return err }); err != nil {
		return err
	}
	if !databaseOperationScope(plan, observeA, config.RunnerID, "observe", op.Snapshot.Database) || !databaseOperationPlan(plan) || domain.HostingDocumentHash(plan.Plan) != domain.HostingDocumentHash(op.Plan) || plan.Result == nil || *plan.Result != *op.Result {
		return errors.New("database observation plan or durable candidate changed")
	}
	e, ec, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	return executeDatabaseObservation(s, c, e, observeA, plan, *config.Database, ec)
}
