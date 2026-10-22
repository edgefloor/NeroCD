package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// lockGenericLogTask establishes task -> log lock order for every legacy append.
// A persisted relation or any nested internal build authority denies this path.
func lockGenericLogTask(ctx context.Context, q *sqlcgen.Queries, run string) error {
	if _, err := q.LockRunID(ctx, run); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	related, err := q.IsHostingBuildTask(ctx, run)
	if err != nil {
		return err
	}
	if related {
		return ErrConflict
	}
	row, err := q.GetRun(ctx, run)
	if err != nil {
		return err
	}
	r, err := taskRunFromSQLC(row)
	if err != nil {
		return err
	}
	if domain.ContainsHostingBuildExecution(r.RunSpec, r.Workflow) {
		return ErrConflict
	}
	return nil
}

// AppendHostingBuildLogs serializes task/lease, placement, then log state. Exact
// committed replay precedes current authority checks and cannot allocate output.
func (s *PostgresStore) AppendHostingBuildLogs(ctx context.Context, runner string, in domain.HostingBuildLogBatch) (domain.HostingBuildLogAck, error) {
	in, err := domain.CanonicalHostingBuildLogs(in)
	if err != nil {
		return domain.HostingBuildLogAck{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuildLogAck, error) {
		ack := domain.HostingBuildLogAck{Authority: in.Authority, Receipts: make([]domain.HostingBuildLogReceipt, len(in.Events))}
		a := in.Authority
		q := sqlcgen.New(tx)
		b, err := readBuild(ctx, q, a.BuildID)
		if err != nil {
			return ack, err
		}
		if _, err = q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
			return ack, ErrConflict
		}
		if err = checkBuildAuthority(ctx, q, runner, a, b, false); err != nil {
			return ack, err
		}
		row, err := q.GetRun(ctx, a.RunID)
		if err != nil {
			return ack, err
		}
		run, err := taskRunFromSQLC(row)
		if err != nil {
			return ack, err
		}
		if b.RunID != a.RunID || b.Snapshot.RunnerID != runner || run.RequestedBy != b.CreatedBy || domain.ValidateHostingBuildTask(run) != nil {
			return ack, ErrConflict
		}

		newEvents := make([]bool, len(in.Events))
		count := 0
		for i, e := range in.Events {
			hash := domain.HostingBuildLogBodyHash(a, e)
			old, lookup := q.GetHostingBuildLogReceipt(ctx, sqlcgen.GetHostingBuildLogReceiptParams{RunID: a.RunID, EventKey: e.EventKey})
			if lookup == nil {
				var original domain.HostingBuildAuthority
				if json.Unmarshal(old.Authority, &original) != nil || original != a || old.BuildID != b.ID || old.BodyHash != hash || int(old.RequestedSequence) != e.Sequence {
					return ack, ErrConflict
				}
				ack.Receipts[i] = domain.HostingBuildLogReceipt{EventKey: e.EventKey, RequestedSequence: e.Sequence, Sequence: int(old.Sequence), BodyHash: hash}
				continue
			}
			if !errors.Is(lookup, pgx.ErrNoRows) {
				return ack, lookup
			}
			newEvents[i] = true
			count++
		}
		if count == 0 {
			return ack, nil
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return ack, err
		}
		if _, err = q.LockHostingBuildState(ctx, b.ID); err != nil {
			return ack, err
		}
		b, err = readBuild(ctx, q, b.ID)
		if err != nil {
			return ack, err
		}
		if b.State != "queued" && b.State != "sourcing" && b.State != "building" {
			return ack, ErrConflict
		}
		for _, e := range in.Events {
			if e.Component != "build" && b.State != "building" {
				return ack, ErrConflict
			}
		}
		if err = q.AcquireRunLogLock(ctx, a.RunID); err != nil {
			return ack, err
		}
		state, err := q.LockHostingBuildLogState(ctx, sqlcgen.LockHostingBuildLogStateParams{BuildID: b.ID, RunID: a.RunID})
		if err != nil {
			return ack, err
		}
		if !state.HistoryComplete || int64(state.LastSequence)+int64(count) > math.MaxInt32 {
			return ack, ErrConflict
		}
		next := state.LastSequence
		for i, e := range in.Events {
			if !newEvents[i] {
				continue
			}
			next++
			attempt := int32(a.Attempt)
			requested := int32(e.Sequence)
			hash := domain.HostingBuildLogBodyHash(a, e)
			if err = q.InsertHostingBuildLog(ctx, sqlcgen.InsertHostingBuildLogParams{ID: "blog_" + domain.HostingDocumentHash(struct{ Run, Key string }{a.RunID, e.EventKey})[7:], RunID: a.RunID, Sequence: next, Stream: e.Stream, Message: e.Message, EventKey: &e.EventKey, LeaseID: &a.LeaseID, Attempt: &attempt, RequestedSequence: &requested, Component: e.Component, SourceTimestamp: e.SourceTimestamp}); err != nil {
				return ack, err
			}
			if err = q.InsertHostingBuildLogReceipt(ctx, sqlcgen.InsertHostingBuildLogReceiptParams{BuildID: b.ID, RunID: a.RunID, EventKey: e.EventKey, Authority: buildJSON(a), RequestedSequence: requested, Sequence: next, BodyHash: hash}); err != nil {
				return ack, err
			}
			ack.Receipts[i] = domain.HostingBuildLogReceipt{EventKey: e.EventKey, RequestedSequence: e.Sequence, Sequence: int(next), BodyHash: hash}
		}
		if err = casBuild(q.AdvanceHostingBuildLogSequence(ctx, sqlcgen.AdvanceHostingBuildLogSequenceParams{BuildID: b.ID, LastSequence: state.LastSequence, LastSequence_2: next})); err != nil {
			return ack, err
		}
		// Expiry progresses independently of locks. Recheck after every blocking write.
		return ack, freshBuildAuthority(ctx, tx, runner, a, b)
	})
}

// ListHostingBuildLogs exposes only bounded output under current project access.
func (s *PostgresStore) ListHostingBuildLogs(ctx context.Context, actor, id string, after, limit int) (domain.HostingBuildLogPage, error) {
	if after < 0 || after > math.MaxInt32 || limit < 1 || limit > domain.HostingBuildLogReadLimit {
		return domain.HostingBuildLogPage{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuildLogPage, error) {
		out := domain.HostingBuildLogPage{Items: []domain.HostingBuildLogItem{}, NextSequence: after}
		q := sqlcgen.New(tx)
		b, err := readBuild(ctx, q, id)
		if err != nil {
			return out, err
		}
		out.Terminal = b.State == "succeeded" || b.State == "failed" || b.State == "canceled" || b.State == "cleanup_required"
		// An undispatched build has no output and has not lost any log history.
		if b.RunID == "" {
			out.HistoryComplete = true
			return out, authorizeBuildActor(ctx, tx, actor, b.ProjectID, "viewer")
		}
		if _, err = q.LockRunID(ctx, b.RunID); err != nil {
			return out, err
		}
		b, err = readBuild(ctx, q, b.ID)
		if err != nil {
			return out, err
		}
		out.Terminal = b.State == "succeeded" || b.State == "failed" || b.State == "canceled" || b.State == "cleanup_required"
		if err = authorizeBuildActor(ctx, tx, actor, b.ProjectID, "viewer"); err != nil {
			return out, err
		}
		if err = q.AcquireRunLogLock(ctx, b.RunID); err != nil {
			return out, err
		}
		state, err := q.LockHostingBuildLogState(ctx, sqlcgen.LockHostingBuildLogStateParams{BuildID: b.ID, RunID: b.RunID})
		if err != nil {
			return out, err
		}
		out.HistoryComplete = state.HistoryComplete
		out.DeletedThroughSequence = int(state.DeletedThroughSequence)
		out.Gap = !state.HistoryComplete || after < out.DeletedThroughSequence
		earliest, err := q.EarliestHostingBuildLogSequence(ctx, b.RunID)
		if err != nil {
			return out, err
		}
		out.EarliestAvailableSequence = int(earliest)
		rows, err := q.ListHostingBuildLogItems(ctx, sqlcgen.ListHostingBuildLogItemsParams{RunID: b.RunID, Sequence: int32(after), Limit: int32(limit + 1)})
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if len(out.Items) == limit {
				out.Truncated = true
				break
			}
			if len(r.Message) > domain.HostingBuildLogMessageLimit {
				r.Message = r.Message[:domain.HostingBuildLogMessageLimit]
				for !utf8.ValidString(r.Message) {
					r.Message = r.Message[:len(r.Message)-1]
				}
				r.MessageTruncated = true
			}
			item := domain.HostingBuildLogItem{Sequence: int(r.Sequence), Component: r.Component, Stream: r.Stream, Message: r.Message, CreatedAt: r.CreatedAt, SourceTimestamp: r.SourceTimestamp, MessageTruncated: r.MessageTruncated}
			out.Items = append(out.Items, item)
			oldNext := out.NextSequence
			out.NextSequence = item.Sequence
			encoded, err := json.Marshal(out)
			if err != nil {
				return out, err
			}
			// Reserve space for the final boolean metadata and cursor, all JSON escaping
			// is accounted for. A single SQL-projected item always fits this budget.
			if len(encoded)+64 > domain.HostingBuildLogByteLimit {
				out.Items = out.Items[:len(out.Items)-1]
				out.NextSequence = oldNext
				out.Truncated = true
				break
			}
		}
		if len(out.Items) == 0 && out.DeletedThroughSequence > out.NextSequence {
			out.NextSequence = out.DeletedThroughSequence
		}
		return out, authorizeBuildActor(ctx, tx, actor, b.ProjectID, "viewer")
	})
}
