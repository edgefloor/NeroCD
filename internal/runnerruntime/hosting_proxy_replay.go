package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func proxyEntryAuthority(entry runner.JournalProxy) domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
}

// reconcileProxyJournal runs before heartbeat or claim and never invokes Apply.
// An unfinished admitted effect requires explicit recovery; neither expiry nor
// a successful local observation substitutes for the missing server receipt.
func reconcileProxyJournal(server, token, runnerID string, journal *runner.AttemptJournal, config ProxyConfig) error {
	if len(journal.Snapshot().Proxy) == 0 {
		return nil
	}
	if !config.Enabled() {
		return errors.New("proxy journal requires its explicit operator configuration")
	}
	client := runnerclient.New(server, token)
	for _, entry := range journal.Snapshot().Proxy {
		if entry.Phase != "prepared" {
			continue
		}
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		_, _, _, err := prepareProxyEngine(ctx, config, entry.Plan, &entry.Preparation)
		stop()
		if err != nil {
			return err
		}
		ctx, stop = context.WithTimeout(context.Background(), 5*time.Second)
		op, err := client.ProxyPrepared(ctx, *entry.Prepared)
		stop()
		if err != nil {
			ctx, stop = context.WithTimeout(context.Background(), 5*time.Second)
			status, probeErr := client.ProxyStatus(ctx, proxyEntryAuthority(entry))
			stop()
			if probeErr != nil || proxyResponseMatches(status, proxyEntryAuthority(entry), runnerID, &entry.Plan) != nil || status.RestoreOwnershipHeld || status.MutationHold || status.PreparedReceipt != nil || status.CompleteReceipt != nil || (status.State != "canceled" && status.State != "superseded") {
				return errors.New("proxy preparation replay remains unresolved")
			}
		} else if err = proxyPreparedAcknowledged(op, entry, runnerID); err != nil {
			return err
		}
		if err = journal.AckProxy(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Proxy {
		if entry.Phase != "result" {
			continue
		}
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		engine, local, _, err := prepareProxyEngine(ctx, config, entry.Plan, &entry.Preparation)
		stop()
		if err != nil {
			return err
		}
		if err = replayHostingEvents(server, token, journal, entry.Attempt); err != nil {
			return err
		}
		op, err := replayProxyResult(client, entry, runnerID, true)
		if err != nil {
			return err
		}
		if err = proxyResultAcknowledged(op, entry, runnerID); err != nil {
			return err
		}
		if err = verifyProxyLocalReceipt(engine, local, entry.Result.Result); err != nil {
			return err
		}
		if err = journal.AckProxy(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Proxy {
		if entry.Phase != "apply_intent" {
			continue
		}
		authority := proxyEntryAuthority(entry)
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		op, err := client.ProxyStatus(ctx, authority)
		stop()
		if err != nil {
			return err
		}
		if proxyResponseMatches(op, authority, runnerID, &entry.Plan) != nil || op.RestoreOwnershipHeld || op.MutationHold || op.Preparation == nil || *op.Preparation != entry.Preparation {
			return errors.New("proxy intent retains uncertain, restored or mismatched ownership")
		}
		if op.Result == nil && op.CompleteReceipt == nil && (op.State == "canceled" || op.State == "superseded") {
			// A terminal unadmitted generation proves that no engine call was
			// authorized. No observation or effect retry is used as that proof.
			if err = journal.AckProxy(entry.ID); err != nil {
				return err
			}
			continue
		}
		if op.Result == nil || op.CompleteReceipt == nil {
			return errors.New("proxy intent requires a committed exact result")
		}
		result := entry
		result.Phase = "result"
		result.ID = attemptMutationKey("proxy_result", authority.LeaseID, authority.Attempt, "proxy")
		result.Result = &domain.HostingProxyCompleteRequest{HostingAuthority: authority, ReceiptID: result.ID, Result: *op.Result}
		if err = proxyResultAcknowledged(op, result, runnerID); err != nil {
			return err
		}
		ctx, stop = context.WithTimeout(context.Background(), 30*time.Second)
		engine, local, _, err := prepareProxyEngine(ctx, config, entry.Plan, &entry.Preparation)
		stop()
		if err != nil {
			return err
		}
		if err = verifyProxyLocalReceipt(engine, local, *op.Result); err != nil {
			return err
		}
		if err = journal.AckProxy(entry.ID); err != nil {
			return err
		}
	}
	return nil
}

func replayProxyResult(client runnerclient.Client, entry runner.JournalProxy, runnerID string, allowLive bool) (domain.HostingProxyOperation, error) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	op, err := client.ProxyStatus(ctx, proxyEntryAuthority(entry))
	stop()
	if err != nil {
		return op, err
	}
	if entry.Result == nil || proxyResponseMatches(op, proxyEntryAuthority(entry), runnerID, &entry.Plan) != nil || op.RestoreOwnershipHeld {
		return op, errors.New("proxy result replay identity or restored ownership changed")
	}
	if op.CompleteReceipt != nil {
		if err = proxyResultAcknowledged(op, entry, runnerID); err != nil {
			return op, err
		}
		ctx, stop = context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return client.ProxyComplete(ctx, *entry.Result)
	}
	if !allowLive {
		return op, errors.New("proxy result has no committed receipt after authority ended")
	}
	lease, fenced, err := probeReplayAuthority(client.Server, client.Token, entry.Attempt)
	if err != nil {
		return op, err
	}
	if fenced {
		return op, errors.New("uncommitted proxy result lost live authority")
	}
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	request, cancel, err := supervisor.RequestContext()
	if err != nil {
		return op, err
	}
	defer cancel()
	return client.ProxyComplete(request, *entry.Result)
}
