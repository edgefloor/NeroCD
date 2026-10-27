package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type observationPlacement struct {
	resource, project, binding, destination, server, runner, namespace string
	healthy                                                            *domain.HostingHealthy
	heartbeat                                                          *time.Time
	mutation, restore                                                  bool
	identity                                                           string
	authorityIdentity, credential                                      string
	target                                                             domain.HostingObservationTarget
}

// lockObservationPlacement shares the deployment destination serialization point.
// Destination row SHARE precedes grant UPDATE to avoid inversion with revocation.
// No operation or task locks may be acquired after this helper's binding lock.
func lockObservationPlacement(ctx context.Context, tx pgx.Tx, resource, runner, credential string) (observationPlacement, error) {
	return lockHostingSettledPlacement(ctx, tx, resource, runner, credential, domain.HostingObservationCapability)
}

// lockHostingSettledPlacement admits only current enrolled authority for the requested capability.
func lockHostingSettledPlacement(ctx context.Context, tx pgx.Tx, resource, runner, credential, capability string) (observationPlacement, error) {
	p := observationPlacement{resource: resource}
	q := sqlcgen.New(tx)
	before, err := q.ObservationResourceDestination(ctx, resource)
	if err != nil {
		return p, err
	}
	p.project = before.ProjectID
	if before.DestinationID == nil {
		r, e := q.ReadHostingRuntimeResource(ctx, resource)
		p.project = r.ProjectID
		return p, e
	}
	if err = q.LockHostingExecutionDestination(ctx, *before.DestinationID); err != nil {
		return p, err
	}
	if _, err = q.LockObservationDestinationRow(ctx, *before.DestinationID); err != nil {
		return p, err
	}
	r, err := q.LockHostingRuntimeResource(ctx, resource)
	if err != nil {
		return p, err
	}
	p.project = r.ProjectID
	placement, err := q.LockObservationBindingPlacement(ctx, resource)
	if err != nil {
		return p, err
	}
	p.binding = placement.ID
	p.destination = placement.DestinationID
	p.namespace = placement.RuntimeNamespace
	p.server = placement.ServerID
	p.runner = placement.RunnerID
	if p.destination != *before.DestinationID || runner != "" && p.runner != runner {
		return p, ErrConflict
	}
	if _, err = q.LockHostingBuildProject(ctx, p.project); err != nil {
		return p, ErrConflict
	}
	var gateErr error
	grant, err := q.LockObservationGrant(ctx, sqlcgen.LockObservationGrantParams{DestinationID: p.destination, ProjectID: p.project})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		gateErr = ErrConflict
	}
	if grant.RevokedAt != nil {
		gateErr = ErrConflict
	}
	state, err := q.LockObservationSettledState(ctx, p.binding)
	if errors.Is(err, pgx.ErrNoRows) {
		state.RestoreAdmitted = true
	} else if err != nil {
		return p, err
	}
	p.mutation = state.HoldOperationID != nil
	p.restore = !state.RestoreAdmitted
	explicitRestore, err := q.HostingBindingRestoreHeld(ctx, p.binding)
	if err != nil {
		return p, err
	}
	p.restore = p.restore || explicitRestore
	current, err := q.LockObservationRunner(ctx, p.runner)
	if err != nil {
		return p, err
	}
	p.heartbeat = &current.LastHeartbeatAt
	if current.Status != "active" || current.TokenHash == "" || credential != "" && current.TokenHash != credential || !slices.Contains(current.Capabilities, capability) || slices.Contains(current.Capabilities, "shell") {
		gateErr = ErrConflict
	}
	enrollment, err := q.LockObservationEnrollment(ctx, sqlcgen.LockObservationEnrollmentParams{RunnerID: p.runner, CredentialHash: &current.TokenHash})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		gateErr = ErrConflict
	}
	if p.restore {
		gateErr = ErrHostingRestoreHold
	}
	p.credential = current.TokenHash
	p.authorityIdentity = runtimeLogAuthorityHash(p.project, p.destination, p.server, p.runner, grant.CreatedAt, current.TokenHash, enrollment)
	healthy := state.Healthy
	if len(healthy) == 0 {
		return p, gateErr
	}
	var h domain.HostingHealthy
	if json.Unmarshal(healthy, &h) != nil {
		return p, ErrConflict
	}
	snap := h.Snapshot
	// Only the distinct stack-log capability admits whole-stack receipts. The
	// ordinary observation/single-container path below keeps its original gates.
	if capability == domain.HostingStackRuntimeLogsCapability {
		if !slices.Contains(current.Capabilities, domain.HostingRuntimeLogsCapability) || p.mutation || domain.ValidateHostingStackHealthy(h) != nil || snap.ResourceID != resource || snap.BindingID != p.binding || snap.DestinationID != p.destination || snap.ServerID != p.server || snap.RunnerID != p.runner || snap.RuntimeNamespace != p.namespace {
			return p, ErrConflict
		}
		p.healthy = &h
		return p, gateErr
	}
	if snap.ResourceID != resource || snap.BindingID != p.binding || snap.DestinationID != p.destination || snap.ServerID != p.server || snap.RunnerID != p.runner || snap.RuntimeNamespace != p.namespace || h.OperationID == "" || h.ContainerID == "" || len(h.ContainerID) > 128 || h.PlanHash == "" || snap.HostPort < 1024 || domain.ValidateHostingSnapshotRelease(snap, h.Release) != nil {
		return p, ErrConflict
	}
	p.healthy = &h
	p.target = domain.HostingObservationTarget{ResourceID: resource, BindingID: p.binding, DestinationID: p.destination, ServerID: p.server, RunnerID: p.runner, OperationID: h.OperationID, DaemonID: h.Release.DaemonID, ContainerID: h.ContainerID, ImageID: h.Release.ImageID, ReleaseHash: h.Release.Hash, PlanHash: h.PlanHash, RuntimeNamespace: p.namespace, ContainerPort: snap.ContainerPort, HostPort: snap.HostPort, ReadinessPath: snap.Readiness.Path, SampleTimeoutSeconds: domain.HostingObservationTimeoutSeconds}
	p.identity = domain.HostingDocumentHash(struct {
		Healthy           domain.HostingHealthy
		Grant             time.Time
		Token, Enrollment string
	}{h, grant.CreatedAt, current.TokenHash, enrollment})
	return p, gateErr
}

func observationRow(ctx context.Context, tx pgx.Tx, binding string) (sqlcgen.HostingRuntimeObservation, error) {
	q := sqlcgen.New(tx)
	if err := q.EnsureHostingRuntimeObservation(ctx, binding); err != nil {
		return sqlcgen.HostingRuntimeObservation{}, err
	}
	return q.LockHostingRuntimeObservation(ctx, binding)
}

func observationTarget(raw []byte) (domain.HostingObservationTarget, error) {
	var target domain.HostingObservationTarget
	err := json.Unmarshal(raw, &target)
	return target, err
}

func (s *PostgresStore) issueHostingObservation(ctx context.Context, runner, credential, resource string) (domain.HostingObservationTarget, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingObservationTarget, error) {
		var zero domain.HostingObservationTarget
		p, err := lockObservationPlacement(ctx, tx, resource, runner, credential)
		if err != nil {
			return zero, err
		}
		if p.healthy == nil {
			return zero, ErrConflict
		}
		row, err := observationRow(ctx, tx, p.binding)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		now, err := q.HostingObservationClock(ctx)
		if err != nil {
			return zero, err
		}
		if previous, e := observationTarget(row.PendingTarget); e == nil && row.PendingIdentity == p.identity && previous.ExpiresAt.After(now) {
			return previous, nil
		}
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return zero, err
		}
		target := p.target
		target.SampleID = "hos_" + hex.EncodeToString(id)
		target.IssuedAt = now
		target.ExpiresAt = now.Add(10 * time.Second)
		raw, err := json.Marshal(target)
		if err != nil {
			return zero, err
		}
		if len(raw) > domain.HostingObservationJSONLimit {
			return zero, ErrConflict
		}
		err = q.SetHostingRuntimeObservationPending(ctx, sqlcgen.SetHostingRuntimeObservationPendingParams{BindingID: p.binding, PendingTarget: raw, PendingIdentity: p.identity})
		if err != nil {
			return zero, err
		}
		now, err = q.HostingObservationClock(ctx)
		if err != nil {
			return zero, err
		}
		if !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		return target, nil
	})
}

// HostingObservationTargets returns a bounded sweep of current settled placements.
func (s *PostgresStore) HostingObservationTargets(ctx context.Context, runner, credential string, in domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
	result := domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{}, EndOfSweep: true}
	if in.Validate() != nil || credential == "" {
		return result, ErrConflict
	}
	candidates, err := s.queries.ListObservationBindingCandidates(ctx, sqlcgen.ListObservationBindingCandidatesParams{RunnerID: runner, AfterBindingID: in.AfterBindingID, PageLimit: int32(in.Limit + 1)})
	if err != nil {
		return result, err
	}
	if len(candidates) > in.Limit {
		result.EndOfSweep = false
		candidates = candidates[:in.Limit]
	}
	for _, c := range candidates {
		target, e := s.issueHostingObservation(ctx, runner, credential, c.ResourceID)
		if e != nil && !errors.Is(e, ErrConflict) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrHostingRestoreHold) {
			return result, e
		}
		if e == nil {
			result.Targets = append(result.Targets, target)
		}
		result.NextCursor = c.ID
	}
	return result, nil
}

// ReportHostingObservation accepts evidence only for a still-current challenge.
func (s *PostgresStore) ReportHostingObservation(ctx context.Context, runner, credential string, in domain.HostingRuntimeObservation) (domain.HostingObservationAck, error) {
	var zero domain.HostingObservationAck
	if in.Validate() != nil || credential == "" {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingObservationAck, error) {
		resource, err := sqlcgen.New(tx).ObservationBindingResource(ctx, in.BindingID)
		if err != nil {
			return zero, err
		}
		p, err := lockObservationPlacement(ctx, tx, resource, runner, credential)
		if err != nil {
			return zero, err
		}
		if p.healthy == nil || p.binding != in.BindingID {
			return zero, ErrConflict
		}
		row, err := observationRow(ctx, tx, p.binding)
		if err != nil {
			return zero, err
		}
		var ack domain.HostingObservationAck
		hash := domain.HostingDocumentHash(in)
		if json.Unmarshal(row.AcceptedAck, &ack) == nil && ack.SampleID == in.SampleID {
			if row.AcceptedIdentity != p.identity || ack.BodyHash != hash {
				return zero, ErrConflict
			}
			return ack, nil
		}
		target, err := observationTarget(row.PendingTarget)
		if err != nil || target.SampleID != in.SampleID || row.PendingIdentity != p.identity {
			return zero, ErrConflict
		}
		q := sqlcgen.New(tx)
		now, err := q.HostingObservationClock(ctx)
		if err != nil {
			return zero, err
		}
		if !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		ack = domain.HostingObservationAck{SampleID: in.SampleID, BodyHash: hash, ReceivedAt: now}
		body, err := json.Marshal(in)
		if err != nil {
			return zero, err
		}
		rawAck, err := json.Marshal(ack)
		if err != nil {
			return zero, err
		}
		if err = q.AcceptHostingRuntimeObservation(ctx, sqlcgen.AcceptHostingRuntimeObservationParams{BindingID: p.binding, AcceptedBody: body, AcceptedAck: rawAck}); err != nil {
			return zero, err
		}
		// clock_timestamp, unlike transaction time, includes every blocking write.
		now, err = q.HostingObservationClock(ctx)
		if err != nil {
			return zero, err
		}
		if !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		return ack, nil
	})
}

// HostingRuntimeStatus reports current evidence independently of deployment state.
func (s *PostgresStore) HostingRuntimeStatus(ctx context.Context, actor, resource string) (domain.HostingRuntimeStatus, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingRuntimeStatus, error) {
		out := domain.HostingRuntimeStatus{ResourceID: resource, Freshness: "unknown", Reason: "never_observed", AgentReachability: "unknown", ContainerState: "unknown", Readiness: "unknown", StaleAfterSeconds: 30, TargetIntervalSeconds: 10, SampleTimeoutSeconds: 5}
		// Derive viewer scope before returning any placement details.
		p, gateErr := lockObservationPlacement(ctx, tx, resource, "", "")
		if gateErr != nil && !errors.Is(gateErr, ErrConflict) && !errors.Is(gateErr, ErrHostingRestoreHold) {
			return out, gateErr
		}
		if err := authorizeBuildActor(ctx, tx, actor, p.project, "viewer"); err != nil {
			return out, err
		}
		out.BindingID = p.binding
		out.DestinationID = p.destination
		out.ServerID = p.server
		out.RunnerID = p.runner
		out.MutationHold = p.mutation
		out.RestoreOwnershipHeld = p.restore
		out.LastHeartbeatAt = p.heartbeat
		q := sqlcgen.New(tx)
		now, err := q.HostingObservationClock(ctx)
		if err != nil {
			return out, err
		}
		if p.healthy != nil {
			out.CurrentOperationID = p.healthy.OperationID
			out.CurrentContainerID = p.healthy.ContainerID
		}
		if p.binding != "" {
			row, e := q.LockHostingRuntimeObservation(ctx, p.binding)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return out, e
			}
			now, err = q.HostingObservationClock(ctx)
			if err != nil {
				return out, err
			}
			var ack domain.HostingObservationAck
			var body domain.HostingRuntimeObservation
			target, targetErr := observationTarget(row.AcceptedTarget)
			if targetErr == nil && json.Unmarshal(row.AcceptedAck, &ack) == nil && json.Unmarshal(row.AcceptedBody, &body) == nil {
				out.LastObservation = &domain.HostingLastObservation{OperationID: target.OperationID, ContainerID: target.ContainerID, ObservedAt: ack.ReceivedAt, Observation: body}
				out.ObservedOperationID = target.OperationID
				out.ObservedContainerID = target.ContainerID
				out.ObservedAt = &ack.ReceivedAt
				if gateErr == nil && row.AcceptedIdentity == p.identity {
					out.Freshness = "stale"
					out.Reason = "stale"
					if now.Sub(ack.ReceivedAt) <= 30*time.Second {
						out.Freshness = "fresh"
						out.Reason = "observed"
						out.ContainerState = body.ContainerState
						out.Readiness = body.Readiness
						out.CollectionError = body.CollectionError
						out.ExitCode = body.ExitCode
					}
				} else {
					out.Reason = "target_changed"
				}
			}
		}
		out.ServerTime = now
		if p.heartbeat != nil && now.Sub(*p.heartbeat) <= 30*time.Second {
			out.AgentReachability = "reachable"
		}
		if gateErr != nil {
			out.Freshness = "blocked"
			out.Reason = "authority_unavailable"
			if p.restore {
				out.Reason = "restore_hold"
			}
		}
		if err = authorizeBuildActor(ctx, tx, actor, p.project, "viewer"); err != nil {
			return out, err
		}
		return out, nil
	})
}
