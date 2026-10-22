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
	target                                                             domain.HostingObservationTarget
}

// lockObservationPlacement shares the deployment destination serialization point.
// Destination row SHARE precedes grant UPDATE to avoid inversion with revocation.
// No operation or task locks may be acquired after this helper's binding lock.
func lockObservationPlacement(ctx context.Context, tx pgx.Tx, resource, runner, credential string) (observationPlacement, error) {
	p := observationPlacement{resource: resource}
	var destination *string
	if err := tx.QueryRow(ctx, `SELECT r.project_id,b.destination_id FROM resources r LEFT JOIN resource_bindings b ON b.resource_id=r.id WHERE r.id=$1`, resource).Scan(&p.project, &destination); err != nil {
		return p, err
	}
	if destination == nil {
		err := tx.QueryRow(ctx, `SELECT project_id FROM resources WHERE id=$1 FOR SHARE`, resource).Scan(&p.project)
		return p, err
	}
	q := sqlcgen.New(tx)
	if err := q.LockHostingExecutionDestination(ctx, *destination); err != nil {
		return p, err
	}
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM destinations WHERE id=$1 FOR SHARE`, *destination).Scan(&locked); err != nil {
		return p, err
	}
	if err := tx.QueryRow(ctx, `SELECT project_id FROM resources WHERE id=$1 FOR UPDATE`, resource).Scan(&p.project); err != nil {
		return p, err
	}
	if err := tx.QueryRow(ctx, `SELECT b.id,b.destination_id,b.runtime_namespace,d.server_id,s.runner_id FROM resource_bindings b JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id WHERE b.resource_id=$1 FOR SHARE OF b,s`, resource).Scan(&p.binding, &p.destination, &p.namespace, &p.server, &p.runner); err != nil {
		return p, err
	}
	if p.destination != *destination || runner != "" && p.runner != runner {
		return p, ErrConflict
	}
	if _, err := q.LockHostingBuildProject(ctx, p.project); err != nil {
		return p, ErrConflict
	}
	var revoked *time.Time
	var granted time.Time
	var gateErr error
	if err := tx.QueryRow(ctx, `SELECT revoked_at,created_at FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2 FOR UPDATE`, p.destination, p.project).Scan(&revoked, &granted); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		gateErr = ErrConflict
	}
	if revoked != nil {
		gateErr = ErrConflict
	}
	var healthy []byte
	var hold *string
	var admitted bool
	err := tx.QueryRow(ctx, `SELECT healthy,hold_operation_id,restore_admitted FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE`, p.binding).Scan(&healthy, &hold, &admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		admitted = true
	} else if err != nil {
		return p, err
	}
	p.mutation = hold != nil
	p.restore = !admitted
	var explicitRestore bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id=$1)`, p.binding).Scan(&explicitRestore); err != nil {
		return p, err
	}
	p.restore = p.restore || explicitRestore
	var token, status string
	var capabilities []string
	err = tx.QueryRow(ctx, `SELECT token_hash,last_heartbeat_at,status,capabilities FROM runners WHERE id=$1 FOR SHARE`, p.runner).Scan(&token, &p.heartbeat, &status, &capabilities)
	if err != nil {
		return p, err
	}
	if status != "active" || token == "" || credential != "" && token != credential || !slices.Contains(capabilities, domain.HostingObservationCapability) || slices.Contains(capabilities, "shell") {
		gateErr = ErrConflict
	}
	var enrollment string
	if err = tx.QueryRow(ctx, `SELECT id FROM runner_enrollments WHERE runner_id=$1 AND used_at IS NOT NULL AND revoked_at IS NULL AND credential_hash=$2 ORDER BY id LIMIT 1 FOR SHARE`, p.runner, token).Scan(&enrollment); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		gateErr = ErrConflict
	}
	if p.restore {
		gateErr = ErrHostingRestoreHold
	}
	if len(healthy) == 0 {
		return p, gateErr
	}
	var h domain.HostingHealthy
	if json.Unmarshal(healthy, &h) != nil {
		return p, ErrConflict
	}
	snap := h.Snapshot
	if snap.ResourceID != resource || snap.BindingID != p.binding || snap.DestinationID != p.destination || snap.ServerID != p.server || snap.RunnerID != p.runner || snap.RuntimeNamespace != p.namespace || h.OperationID == "" || h.ContainerID == "" || len(h.ContainerID) > 128 || h.PlanHash == "" || snap.HostPort < 1024 || domain.ValidateHostingSnapshotRelease(snap, h.Release) != nil {
		return p, ErrConflict
	}
	p.healthy = &h
	p.target = domain.HostingObservationTarget{ResourceID: resource, BindingID: p.binding, DestinationID: p.destination, ServerID: p.server, RunnerID: p.runner, OperationID: h.OperationID, DaemonID: h.Release.DaemonID, ContainerID: h.ContainerID, ImageID: h.Release.ImageID, ReleaseHash: h.Release.Hash, PlanHash: h.PlanHash, RuntimeNamespace: p.namespace, ContainerPort: snap.ContainerPort, HostPort: snap.HostPort, ReadinessPath: snap.Readiness.Path, SampleTimeoutSeconds: domain.HostingObservationTimeoutSeconds}
	p.identity = domain.HostingDocumentHash(struct {
		Healthy           domain.HostingHealthy
		Grant             time.Time
		Token, Enrollment string
	}{h, granted, token, enrollment})
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
	rows, err := s.pool.Query(ctx, `SELECT b.id,b.resource_id FROM resource_bindings b JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id JOIN hosting_binding_state bs ON bs.binding_id=b.id WHERE s.runner_id=$1 AND b.id>$2 AND bs.healthy IS NOT NULL ORDER BY b.id LIMIT $3`, runner, in.AfterBindingID, in.Limit+1)
	if err != nil {
		return result, err
	}
	type candidate struct{ binding, resource string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.binding, &c.resource); err != nil {
			rows.Close()
			return result, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(candidates) > in.Limit {
		result.EndOfSweep = false
		candidates = candidates[:in.Limit]
	}
	for _, c := range candidates {
		target, e := s.issueHostingObservation(ctx, runner, credential, c.resource)
		if e != nil && !errors.Is(e, ErrConflict) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrHostingRestoreHold) {
			return result, e
		}
		if e == nil {
			result.Targets = append(result.Targets, target)
		}
		result.NextCursor = c.binding
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
		var resource string
		if err := tx.QueryRow(ctx, `SELECT resource_id FROM resource_bindings WHERE id=$1`, in.BindingID).Scan(&resource); err != nil {
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
