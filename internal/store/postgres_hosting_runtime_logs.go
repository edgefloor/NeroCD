package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func runtimeLogAuthorityHash(project, destination, server, runner string, grant time.Time, credential, enrollment string) string {
	return domain.HostingDocumentHash(struct {
		Project, Destination, Server, Runner string
		Grant                                time.Time
		Credential, Enrollment               string
	}{project, destination, server, runner, grant, credential, enrollment})
}

func runtimeLogRandom() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func runtimeLogTarget(h domain.HostingHealthy) domain.HostingRuntimeLogTarget {
	s := h.Snapshot
	return domain.HostingRuntimeLogTarget{ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, OperationID: h.OperationID, DaemonID: h.Release.DaemonID, ContainerID: h.ContainerID, ImageID: h.Release.ImageID, ReleaseHash: h.Release.Hash, PlanHash: h.PlanHash, RuntimeNamespace: s.RuntimeNamespace}
}

func runtimeLogStackTarget(h domain.HostingHealthy, component string) (domain.HostingRuntimeLogTarget, error) {
	if domain.ValidateHostingStackHealthy(h) != nil {
		return domain.HostingRuntimeLogTarget{}, ErrConflict
	}
	for _, item := range h.Stack.Components {
		if item.Component != component {
			continue
		}
		target := runtimeLogTarget(h)
		target.Component = component
		target.ContainerName = domain.HostingStackObjectID(target.BindingID, "component", component)
		target.ContainerID, target.ImageID = item.ContainerID, item.ImageID
		if target.Validate() != nil {
			return domain.HostingRuntimeLogTarget{}, ErrConflict
		}
		return target, nil
	}
	return domain.HostingRuntimeLogTarget{}, ErrConflict
}

func runtimeLogPublic(row sqlcgen.HostingRuntimeLogSession, now time.Time) (domain.HostingRuntimeLogSession, error) {
	var target domain.HostingRuntimeLogTarget
	if json.Unmarshal(row.Target, &target) != nil {
		return domain.HostingRuntimeLogSession{}, ErrConflict
	}
	state := row.State
	if row.EndedAt == nil && !row.DemandExpiresAt.After(now) && state != "unsupported_driver" {
		state = "ended"
	}
	if state == "following" && (row.EpochExpiresAt == nil || !row.EpochExpiresAt.After(now)) {
		state = "disconnected"
	}
	return domain.HostingRuntimeLogSession{ID: row.ID, RequestID: row.RequestID, ResourceID: row.ResourceID, ProjectID: row.ProjectID, ActorID: row.ActorID, Target: target, State: state, CreatedAt: row.CreatedAt, DemandExpiresAt: row.DemandExpiresAt, EndedAt: row.EndedAt, PurgeAfter: row.PurgeAfter, ServerTime: now, Sensitive: true}, nil
}

type runtimeLogResult[T any] struct {
	value   T
	failure error
}

// runtimeLogTransaction can commit a closed gap/termination marker while rejecting
// publication. Fatal errors still roll back the complete transaction.
func runtimeLogTransaction[T any](ctx context.Context, s *PostgresStore, call func(pgx.Tx) (runtimeLogResult[T], error)) (T, error) {
	result, err := buildTransaction(ctx, s, call)
	if err != nil {
		var zero T
		return zero, err
	}
	return result.value, result.failure
}

// RuntimeLogSessionScope reveals only scope belonging to the supplied actor.
func (s *PostgresStore) RuntimeLogSessionScope(ctx context.Context, actor, id string) (RuntimeLogScope, error) {
	r, err := s.queries.RuntimeLogSessionScope(ctx, sqlcgen.RuntimeLogSessionScopeParams{ID: id, ActorID: actor})
	if err != nil {
		return RuntimeLogScope{}, hostingError(err)
	}
	return RuntimeLogScope{ActorID: r.ActorID, ResourceID: r.ResourceID}, nil
}

// lockRuntimeLogOriginalAuthority uses saved placement for authorization and only
// compares current healthy evidence to stop collection. Replacement never grants
// authority over a different target or erases the original output label.
func lockRuntimeLogOriginalAuthority(ctx context.Context, tx pgx.Tx, before sqlcgen.HostingRuntimeLogSession, runner, credential string) (sqlcgen.HostingRuntimeLogSession, bool, error) {
	q := sqlcgen.New(tx)
	var target domain.HostingRuntimeLogTarget
	if json.Unmarshal(before.Target, &target) != nil {
		return before, false, ErrConflict
	}
	if runner != "" && runner != target.RunnerID || credential != "" && credential != before.CredentialHash {
		return before, false, ErrConflict
	}
	if err := q.LockHostingExecutionDestination(ctx, target.DestinationID); err != nil {
		return before, false, err
	}
	if _, err := q.LockRuntimeLogOriginalDestination(ctx, sqlcgen.LockRuntimeLogOriginalDestinationParams{ID: target.DestinationID, ServerID: target.ServerID, RunnerID: target.RunnerID}); err != nil {
		return before, false, ErrConflict
	}
	resource, err := q.LockHostingRuntimeResource(ctx, before.ResourceID)
	if err != nil {
		return before, false, err
	}
	if resource.ProjectID != before.ProjectID {
		return before, false, ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, before.ActorID, before.ProjectID, "viewer"); err != nil {
		return before, false, err
	}
	grant, err := q.LockObservationGrant(ctx, sqlcgen.LockObservationGrantParams{DestinationID: target.DestinationID, ProjectID: before.ProjectID})
	if err != nil || grant.RevokedAt != nil {
		return before, false, auth.ErrForbidden
	}
	binding, err := q.RuntimeLogCurrentBinding(ctx, before.ResourceID)
	changed := false
	if errors.Is(err, pgx.ErrNoRows) {
		changed = true
	} else if err != nil {
		return before, false, err
	} else if binding.ID != target.BindingID || binding.DestinationID != target.DestinationID || binding.RuntimeNamespace != target.RuntimeNamespace {
		changed = true
	}
	state, err := q.LockObservationSettledState(ctx, target.BindingID)
	if errors.Is(err, pgx.ErrNoRows) {
		changed = true
	} else if err != nil {
		return before, false, err
	} else if !state.RestoreAdmitted {
		return before, false, ErrHostingRestoreHold
	}
	held, err := q.HostingBindingRestoreHeld(ctx, target.BindingID)
	if err != nil {
		return before, false, err
	}
	if held {
		return before, false, ErrHostingRestoreHold
	}
	current, err := q.LockObservationRunner(ctx, target.RunnerID)
	if err != nil {
		return before, false, err
	}
	if current.Status != "active" || current.TokenHash != before.CredentialHash || !slices.Contains(current.Capabilities, domain.HostingRuntimeLogsCapability) || target.Component != "" && !slices.Contains(current.Capabilities, domain.HostingStackRuntimeLogsCapability) || slices.Contains(current.Capabilities, "shell") {
		return before, false, auth.ErrForbidden
	}
	enrollment, err := q.LockObservationEnrollment(ctx, sqlcgen.LockObservationEnrollmentParams{RunnerID: target.RunnerID, CredentialHash: &current.TokenHash})
	if err != nil {
		return before, false, auth.ErrForbidden
	}
	if runtimeLogAuthorityHash(before.ProjectID, target.DestinationID, target.ServerID, target.RunnerID, grant.CreatedAt, current.TokenHash, enrollment) != before.AuthorityHash {
		return before, false, auth.ErrForbidden
	}
	var healthy domain.HostingHealthy
	if len(state.Healthy) == 0 || json.Unmarshal(state.Healthy, &healthy) != nil || domain.ValidateHostingSnapshotRelease(healthy.Snapshot, healthy.Release) != nil {
		changed = true
	} else if target.Component != "" {
		selected, selectErr := runtimeLogStackTarget(healthy, target.Component)
		changed = changed || state.HoldOperationID != nil || selectErr != nil || selected != target
	} else if runtimeLogTarget(healthy) != target {
		changed = true
	}
	row, err := q.LockRuntimeLogSession(ctx, before.ID)
	if err != nil {
		return before, false, err
	}
	if row.ActorID != before.ActorID || row.ResourceID != before.ResourceID {
		return before, false, ErrConflict
	}
	now, err := q.HostingObservationClock(ctx)
	if err != nil {
		return row, false, err
	}
	if !row.PurgeAfter.After(now) {
		return row, false, ErrRuntimeLogsExpired
	}
	if changed && row.EndedAt == nil {
		row, err = q.EndRuntimeLogSession(ctx, sqlcgen.EndRuntimeLogSessionParams{ID: row.ID, State: "target_changed"})
		if err != nil {
			return row, false, err
		}
	}
	return row, changed, nil
}

func runtimeLogFinalAuthority(ctx context.Context, tx pgx.Tx, row sqlcgen.HostingRuntimeLogSession) (time.Time, error) {
	if err := authorizeBuildActor(ctx, tx, row.ActorID, row.ProjectID, "viewer"); err != nil {
		return time.Time{}, err
	}
	now, err := sqlcgen.New(tx).HostingObservationClock(ctx)
	if err != nil {
		return now, err
	}
	if !row.PurgeAfter.After(now) {
		return now, ErrRuntimeLogsExpired
	}
	return now, nil
}

// OpenRuntimeLogSession allocates demand only for an immutable current settled target.
func (s *PostgresStore) OpenRuntimeLogSession(ctx context.Context, actor, resource string, in domain.HostingRuntimeLogOpen) (domain.HostingRuntimeLogSession, error) {
	in, err := in.Canonical()
	if err != nil {
		return domain.HostingRuntimeLogSession{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeLogSession, error) {
		var zero domain.HostingRuntimeLogSession
		q := sqlcgen.New(tx)
		if err := q.LockRuntimeLogQuota(ctx); err != nil {
			return zero, err
		}
		old, e := q.ReadRuntimeLogOpenReceipt(ctx, sqlcgen.ReadRuntimeLogOpenReceiptParams{ActorID: actor, ResourceID: resource, RequestID: in.RequestID})
		if e == nil {
			row, _, err := lockRuntimeLogOriginalAuthority(ctx, tx, old, "", "")
			if err != nil {
				return zero, err
			}
			if row.RequestHash != domain.HostingDocumentHash(in) {
				return zero, ErrConflict
			}
			now, err := runtimeLogFinalAuthority(ctx, tx, row)
			if err != nil {
				return zero, err
			}
			return runtimeLogPublic(row, now)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return zero, e
		}
		now, err := q.HostingObservationClock(ctx)
		if err != nil {
			return zero, err
		}
		if !in.RequestExpiresAt.After(now) {
			return zero, ErrRuntimeLogsExpired
		}
		if in.RequestExpiresAt.After(now.Add(30 * time.Second)) {
			return zero, ErrConflict
		}
		capability := domain.HostingRuntimeLogsCapability
		if in.Component != "" {
			capability = domain.HostingStackRuntimeLogsCapability
		}
		p, err := lockHostingSettledPlacement(ctx, tx, resource, "", "", capability)
		if err != nil {
			return zero, err
		}
		if p.healthy == nil {
			return zero, ErrConflict
		}
		if err = authorizeBuildActor(ctx, tx, actor, p.project, "viewer"); err != nil {
			return zero, err
		}
		quota, err := q.RuntimeLogQuotas(ctx, sqlcgen.RuntimeLogQuotasParams{ActorID: actor, ResourceID: resource, ProjectID: p.project})
		if err != nil {
			return zero, err
		}
		if quota.ActiveResource >= 1 || quota.ActiveActor >= 2 || quota.RetainedActor >= 8 || quota.RetainedProject >= 32 {
			return zero, ErrConflict
		}
		id, err := runtimeLogRandom()
		if err != nil {
			return zero, err
		}
		targetIdentity := runtimeLogTarget(*p.healthy)
		if in.Component != "" {
			if p.healthy.OperationID != in.ExpectedOperationID {
				return zero, ErrConflict
			}
			targetIdentity, err = runtimeLogStackTarget(*p.healthy, in.Component)
			if err != nil {
				return zero, err
			}
		}
		if err = targetIdentity.Validate(); err != nil {
			return zero, ErrConflict
		}
		target, err := json.Marshal(targetIdentity)
		if err != nil {
			return zero, err
		}
		row, err := q.CreateRuntimeLogSession(ctx, sqlcgen.CreateRuntimeLogSessionParams{ID: "rls_" + id, ActorID: actor, ResourceID: resource, ProjectID: p.project, RunnerID: p.runner, RequestID: in.RequestID, RequestHash: domain.HostingDocumentHash(in), RequestExpiresAt: in.RequestExpiresAt, Target: target, AuthorityHash: p.authorityIdentity, CredentialHash: p.credential})
		if err != nil {
			return zero, err
		}
		now, err = runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return zero, err
		}
		if !in.RequestExpiresAt.After(now) {
			return zero, ErrRuntimeLogsExpired
		}
		if in.RequestExpiresAt.After(now.Add(30 * time.Second)) {
			return zero, ErrConflict
		}
		return runtimeLogPublic(row, now)
	})
}

func (s *PostgresStore) runtimeLogActorMutation(ctx context.Context, actor, id string, closeSession bool) (domain.HostingRuntimeLogSession, error) {
	return runtimeLogTransaction(ctx, s, func(tx pgx.Tx) (runtimeLogResult[domain.HostingRuntimeLogSession], error) {
		out := runtimeLogResult[domain.HostingRuntimeLogSession]{}
		q := sqlcgen.New(tx)
		if err := q.LockRuntimeLogQuota(ctx); err != nil {
			return out, err
		}
		before, err := q.ReadRuntimeLogSession(ctx, id)
		if err != nil {
			return out, err
		}
		if before.ActorID != actor {
			return out, ErrNotFound
		}
		row, changed, err := lockRuntimeLogOriginalAuthority(ctx, tx, before, "", "")
		if err != nil {
			return out, err
		}
		now, err := runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		originalDemandDeadline := row.DemandExpiresAt
		if closeSession {
			if row.EndedAt == nil {
				row, err = q.EndRuntimeLogSession(ctx, sqlcgen.EndRuntimeLogSessionParams{ID: id, State: "ended"})
			}
		} else {
			if changed || row.EndedAt != nil || !row.DemandExpiresAt.After(now) || row.State == "unsupported_driver" {
				out.failure = ErrConflict
				return out, nil
			}
			row, err = q.RenewRuntimeLogDemand(ctx, id)
		}
		if err != nil {
			return out, err
		}
		now, err = runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		if !closeSession && !originalDemandDeadline.After(now) {
			return out, ErrConflict
		}
		out.value, err = runtimeLogPublic(row, now)
		return out, err
	})
}

// RenewRuntimeLogSession renews only unexpired actor-owned demand.
func (s *PostgresStore) RenewRuntimeLogSession(ctx context.Context, actor, id string) (domain.HostingRuntimeLogSession, error) {
	return s.runtimeLogActorMutation(ctx, actor, id, false)
}

// CloseRuntimeLogSession ends collection immediately without deleting cached output.
func (s *PostgresStore) CloseRuntimeLogSession(ctx context.Context, actor, id string) (domain.HostingRuntimeLogSession, error) {
	return s.runtimeLogActorMutation(ctx, actor, id, true)
}

// RuntimeLogPage rechecks the original grant and current viewer before and after reads.
func (s *PostgresStore) RuntimeLogPage(ctx context.Context, actor, id string, after, limit int) (domain.HostingRuntimeLogPage, error) {
	if after < 0 || after > math.MaxInt32 || limit < 1 || limit > 200 {
		return domain.HostingRuntimeLogPage{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeLogPage, error) {
		out := domain.HostingRuntimeLogPage{Items: []domain.HostingRuntimeLogItem{}, NextSequence: after, Markers: []string{}, Sensitive: true}
		q := sqlcgen.New(tx)
		if err := q.LockRuntimeLogQuota(ctx); err != nil {
			return out, err
		}
		before, err := q.ReadRuntimeLogSession(ctx, id)
		if err != nil {
			return out, err
		}
		if before.ActorID != actor {
			return out, ErrNotFound
		}
		row, _, err := lockRuntimeLogOriginalAuthority(ctx, tx, before, "", "")
		if err != nil {
			return out, err
		}
		now, err := runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		out.Session, err = runtimeLogPublic(row, now)
		if err != nil {
			return out, err
		}
		out.DeletedThroughSequence = int(row.DeletedThroughSequence)
		out.Gap = row.SourceGap || after < out.DeletedThroughSequence
		if row.SourceGap {
			out.Markers = append(out.Markers, "source_gap")
		}
		if out.DeletedThroughSequence > 0 {
			out.Markers = append(out.Markers, "prefix_evicted")
		}
		rows, err := q.ListRuntimeLogEvents(ctx, sqlcgen.ListRuntimeLogEventsParams{SessionID: id, Sequence: int32(after), Limit: int32(limit + 1)})
		if err != nil {
			return out, err
		}
		out.EarliestAvailableSequence = out.DeletedThroughSequence + 1
		if row.RetainedRows == 0 {
			out.EarliestAvailableSequence = 0
		}
		for _, event := range rows {
			if len(out.Items) == limit {
				out.Truncated = true
				break
			}
			item := domain.HostingRuntimeLogItem{Sequence: int(event.Sequence), HostingRuntimeLogEvent: domain.HostingRuntimeLogEvent{Component: event.Component, Stream: event.Stream, Message: event.Message, SourceTimestamp: event.SourceTimestamp, MessageTruncated: event.MessageTruncated}, ReceivedAt: event.ReceivedAt}
			out.Items = append(out.Items, item)
			previous := out.NextSequence
			out.NextSequence = item.Sequence
			raw, e := json.Marshal(out)
			if e != nil {
				return out, e
			}
			if len(raw)+128 > domain.HostingRuntimeLogByteLimit {
				out.Items = out.Items[:len(out.Items)-1]
				out.NextSequence = previous
				out.Truncated = true
				break
			}
		}
		if len(out.Items) == 0 && out.NextSequence < out.DeletedThroughSequence {
			out.NextSequence = out.DeletedThroughSequence
		}
		now, err = runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		out.Session.ServerTime = now
		return out, nil
	})
}

func (s *PostgresStore) claimRuntimeLog(ctx context.Context, runner, credential, instance, id string) (domain.HostingRuntimeLogClaim, error) {
	return runtimeLogTransaction(ctx, s, func(tx pgx.Tx) (runtimeLogResult[domain.HostingRuntimeLogClaim], error) {
		out := runtimeLogResult[domain.HostingRuntimeLogClaim]{}
		q := sqlcgen.New(tx)
		if err := q.LockRuntimeLogQuota(ctx); err != nil {
			return out, err
		}
		before, err := q.ReadRuntimeLogSession(ctx, id)
		if err != nil {
			return out, err
		}
		row, changed, err := lockRuntimeLogOriginalAuthority(ctx, tx, before, runner, credential)
		if err != nil {
			if before.RunnerID == runner && (errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrHostingRestoreHold) || before.CredentialHash != credential) {
				if _, endErr := q.EndRuntimeLogSession(ctx, sqlcgen.EndRuntimeLogSessionParams{ID: before.ID, State: "authority_unavailable"}); endErr != nil {
					return out, endErr
				}
				out.failure = ErrConflict
				return out, nil
			}
			return out, err
		}
		if changed {
			out.failure = ErrConflict
			return out, nil
		}
		now, err := runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		if row.EndedAt != nil || !row.DemandExpiresAt.After(now) || row.State == "unsupported_driver" {
			return out, ErrConflict
		}
		if row.LastSequence == math.MaxInt32 || row.LastBatchSequence == math.MaxInt32 {
			return out, ErrConflict
		}
		live := row.EpochExpiresAt != nil && row.EpochExpiresAt.After(now)
		if live && row.InstanceID != instance {
			return out, ErrConflict
		}
		if !live {
			count, e := q.RuntimeLogCollectorCount(ctx, runner)
			if e != nil {
				return out, e
			}
			if count >= 4 {
				return out, ErrConflict
			}
		}
		epoch := row.CollectionEpoch
		if !live {
			epoch, err = runtimeLogRandom()
			if err != nil {
				return out, err
			}
		}
		originalEpochDeadline := row.EpochExpiresAt
		row, err = q.SetRuntimeLogEpoch(ctx, sqlcgen.SetRuntimeLogEpochParams{ID: id, InstanceID: instance, CollectionEpoch: epoch})
		if err != nil {
			return out, err
		}
		now, err = runtimeLogFinalAuthority(ctx, tx, row)
		if err != nil {
			return out, err
		}
		if !row.DemandExpiresAt.After(now) || row.EpochExpiresAt == nil || !row.EpochExpiresAt.After(now) {
			return out, ErrConflict
		}
		if live && originalEpochDeadline != nil && !originalEpochDeadline.After(now) {
			return out, ErrConflict
		}
		var target domain.HostingRuntimeLogTarget
		if json.Unmarshal(row.Target, &target) != nil {
			return out, ErrConflict
		}
		out.value = domain.HostingRuntimeLogClaim{SessionID: id, InstanceID: instance, CollectionEpoch: epoch, EpochExpiresAt: *row.EpochExpiresAt, DemandExpiresAt: row.DemandExpiresAt, Target: target, NextSequence: int(row.LastSequence) + 1, NextBatchSequence: int(row.LastBatchSequence) + 1}
		return out, nil
	})
}

// ClaimRuntimeLogSessions renews process-local collection authority without user demand.
func (s *PostgresStore) ClaimRuntimeLogSessions(ctx context.Context, runner, credential string, in domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
	out := domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{}, EndOfSweep: true}
	if in.Validate() != nil || credential == "" {
		return out, ErrConflict
	}
	ids, err := s.queries.ListRuntimeLogClaimCandidates(ctx, sqlcgen.ListRuntimeLogClaimCandidatesParams{RunnerID: runner, AfterSessionID: in.AfterSessionID, PageLimit: int32(in.Limit + 1)})
	if err != nil {
		return out, err
	}
	if len(ids) > in.Limit {
		out.EndOfSweep = false
		ids = ids[:in.Limit]
	}
	for _, id := range ids {
		claim, e := s.claimRuntimeLog(ctx, runner, credential, in.InstanceID, id)
		if e == nil {
			out.Targets = append(out.Targets, claim)
		} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrNotFound) && !errors.Is(e, auth.ErrForbidden) && !errors.Is(e, ErrRuntimeLogsExpired) {
			return out, e
		}
		out.NextCursor = id
	}
	return out, nil
}

func runtimeLogPublicationDeadline(ctx context.Context, tx pgx.Tx, row sqlcgen.HostingRuntimeLogSession) (time.Time, error) {
	now, err := runtimeLogFinalAuthority(ctx, tx, row)
	if err != nil {
		return now, err
	}
	if row.EndedAt != nil || !row.DemandExpiresAt.After(now) || row.EpochExpiresAt == nil || !row.EpochExpiresAt.After(now) {
		return now, ErrConflict
	}
	return now, nil
}

// AppendRuntimeLogs persists bounded sensitive output and one latest epoch receipt.
// Only the known authenticated runner bearer is redacted; application secrets may
// remain in these explicitly sensitive, authorized log responses.
func (s *PostgresStore) AppendRuntimeLogs(ctx context.Context, runner, credential, knownBearer string, in domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
	in, err := in.Canonical()
	if err != nil {
		return domain.HostingRuntimeLogAck{}, ErrConflict
	}
	hash := sha256.Sum256([]byte(knownBearer))
	if knownBearer == "" || hex.EncodeToString(hash[:]) != credential {
		return domain.HostingRuntimeLogAck{}, ErrConflict
	}
	bodyHash := domain.HostingDocumentHash(in)
	return runtimeLogTransaction(ctx, s, func(tx pgx.Tx) (runtimeLogResult[domain.HostingRuntimeLogAck], error) {
		out := runtimeLogResult[domain.HostingRuntimeLogAck]{}
		q := sqlcgen.New(tx)
		if err := q.LockRuntimeLogQuota(ctx); err != nil {
			return out, err
		}
		before, err := q.ReadRuntimeLogSession(ctx, in.SessionID)
		if err != nil {
			return out, err
		}
		row, changed, err := lockRuntimeLogOriginalAuthority(ctx, tx, before, runner, credential)
		if err != nil {
			return out, err
		}
		if changed {
			out.failure = ErrConflict
			return out, nil
		}
		now, err := runtimeLogPublicationDeadline(ctx, tx, row)
		if err != nil {
			return out, err
		}
		var target domain.HostingRuntimeLogTarget
		if json.Unmarshal(row.Target, &target) != nil {
			return out, ErrConflict
		}
		component := target.Component
		if component == "" {
			component = "application"
		}
		for _, event := range in.Events {
			if event.Component != component {
				return out, ErrConflict
			}
		}
		if row.InstanceID != in.InstanceID || row.CollectionEpoch != in.CollectionEpoch {
			if err = q.MarkRuntimeLogGap(ctx, row.ID); err != nil {
				return out, err
			}
			out.failure = ErrConflict
			return out, nil
		}
		var old domain.HostingRuntimeLogAck
		if json.Unmarshal(row.Receipt, &old) == nil && old.BatchID == in.BatchID {
			if old.BodyHash != bodyHash {
				return out, ErrConflict
			}
			out.value = old
			return out, nil
		}
		if row.LastBatchSequence == math.MaxInt32 || in.BatchSequence != int(row.LastBatchSequence)+1 || int(row.LastSequence) != in.ExpectedSequence || row.State == "unsupported_driver" {
			if err = q.MarkRuntimeLogGap(ctx, row.ID); err != nil {
				return out, err
			}
			out.failure = ErrConflict
			return out, nil
		}
		next := row.LastSequence
		for _, event := range in.Events {
			next++
			message := strings.ReplaceAll(event.Message, knownBearer, "[REDACTED]")
			truncated := event.MessageTruncated
			if len(message) > domain.HostingRuntimeLogMessageLimit {
				message = message[:domain.HostingRuntimeLogMessageLimit]
				for !utf8.ValidString(message) {
					message = message[:len(message)-1]
				}
				truncated = true
			}
			if err = q.InsertRuntimeLogEvent(ctx, sqlcgen.InsertRuntimeLogEventParams{SessionID: row.ID, Sequence: next, Component: event.Component, Stream: event.Stream, Message: message, SourceTimestamp: event.SourceTimestamp, MessageTruncated: truncated, ReceivedAt: now}); err != nil {
				return out, err
			}
		}
		retained, err := q.ListRuntimeLogRetention(ctx, row.ID)
		if err != nil {
			return out, err
		}
		bytes := 0
		for _, event := range retained {
			bytes += int(event.Bytes)
		}
		count := len(retained)
		watermark := row.DeletedThroughSequence
		for _, event := range retained {
			if bytes <= domain.HostingRuntimeLogSessionBytes && count <= domain.HostingRuntimeLogSessionRows {
				break
			}
			bytes -= int(event.Bytes)
			count--
			watermark = event.Sequence
		}
		if watermark > row.DeletedThroughSequence {
			if err = q.DeleteRuntimeLogPrefix(ctx, sqlcgen.DeleteRuntimeLogPrefixParams{SessionID: row.ID, Sequence: watermark}); err != nil {
				return out, err
			}
		}
		first := 0
		if len(in.Events) > 0 {
			first = int(row.LastSequence) + 1
		}
		ack := domain.HostingRuntimeLogAck{SessionID: row.ID, InstanceID: in.InstanceID, CollectionEpoch: in.CollectionEpoch, BatchID: in.BatchID, BodyHash: bodyHash, BatchSequence: in.BatchSequence, FirstSequence: first, LastSequence: int(next), ReceivedAt: now}
		raw, err := json.Marshal(ack)
		if err != nil {
			return out, err
		}
		if _, err = q.AcceptRuntimeLogBatch(ctx, sqlcgen.AcceptRuntimeLogBatchParams{ID: row.ID, LastSequence: next, DeletedThroughSequence: watermark, RetainedRows: int32(count), RetainedBytes: int32(bytes), Receipt: raw, State: in.State, LastBatchSequence: int32(in.BatchSequence)}); err != nil {
			return out, err
		}
		if _, err = runtimeLogPublicationDeadline(ctx, tx, row); err != nil {
			return out, err
		}
		out.value = ack
		return out, nil
	})
}

// PurgeRuntimeLogs deletes at most eight due sessions without acquiring any
// authorization or placement locks after its session-only cleanup locks.
func (s *PostgresStore) PurgeRuntimeLogs(ctx context.Context) (int, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (int, error) {
		q := sqlcgen.New(tx)
		acquired, err := q.TryRuntimeLogJanitor(ctx)
		if err != nil || !acquired {
			return 0, err
		}
		ids, err := q.PurgeRuntimeLogSessions(ctx)
		return len(ids), err
	})
}

// RuntimeLogTargets reads the same current authority as Open without creating demand.
func (s *PostgresStore) RuntimeLogTargets(ctx context.Context, actor, resource string) (domain.HostingRuntimeLogTargets, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeLogTargets, error) {
		var zero domain.HostingRuntimeLogTargets
		p, err := lockHostingSettledPlacement(ctx, tx, resource, "", "", domain.HostingStackRuntimeLogsCapability)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, p.project, "viewer"); err != nil {
			return zero, err
		}
		if p.healthy == nil {
			return zero, ErrConflict
		}
		result := domain.HostingRuntimeLogTargets{ResourceID: resource, OperationID: p.healthy.OperationID, Components: make([]domain.HostingRuntimeLogComponent, 0, len(p.healthy.Stack.Components))}
		for _, component := range p.healthy.Stack.Components {
			result.Components = append(result.Components, domain.HostingRuntimeLogComponent{Component: component.Component, Running: component.Running, ExitCode: component.ExitCode})
		}
		return result, nil
	})
}
