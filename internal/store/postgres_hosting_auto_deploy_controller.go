package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

var _ HostingAutoDeployRepository = (*PostgresStore)(nil)

// autoRecordDemand runs inside the signed, provider-verified push transaction.
// Delivery receipt time, rather than a retry's clock, owns the original window.
func (s *PostgresStore) autoRecordDemand(ctx context.Context, tx pgx.Tx, doc githubPushDocument, delivery sqlcgen.HostingGithubPushDelivery, build string) error {
	if doc.Configuration.AutomaticDeployment == nil {
		return nil
	}
	c, err := autoCurrent(ctx, tx, doc)
	if err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	if _, err = q.AutoLockControl(ctx, c.ResourceID); err != nil {
		return err
	}
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(struct {
		Policy, Build string
		Generation    int64
	}{doc.Policy.ID, build, doc.Policy.Generation}), "sha256:")[:40]
	expires := delivery.ReceivedAt.Add(24 * time.Hour)
	if delivery.ExpiresAt.Before(expires) {
		expires = delivery.ExpiresAt
	}
	d := autoDemand{Version: 1, ID: "auto-demand-" + suffix, Context: c, PolicyID: doc.Policy.ID, PolicyGeneration: doc.Policy.Generation, Commit: doc.Policy.DesiredCommit, BuildID: build, DeploymentID: "auto-deploy-" + suffix, SwitchID: "auto-switch-" + suffix, CreatedAt: delivery.ReceivedAt, ExpiresAt: expires, OriginalAttempts: delivery.Attempts}
	now, err := pushClock(ctx, tx)
	if err != nil {
		return err
	}
	if !expires.After(now) || d.OriginalAttempts >= domain.GitHubPushMaxAttempts {
		return ErrConflict
	}
	if _, err = q.AutoReplaceDemand(ctx, sqlcgen.AutoReplaceDemandParams{ResourceID: c.ResourceID, ID: d.ID, PolicyID: d.PolicyID, PolicyGeneration: d.PolicyGeneration, BuildID: d.BuildID, CommitSha: d.Commit, Body: buildJSON(d), Attempts: d.OriginalAttempts, CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt}); err != nil {
		return err
	}
	if _, err = s.hostingJobs.EnqueueAutoDeployTx(ctx, tx, hostingjobs.AutoDeployArgs{ResourceID: c.ResourceID}); err != nil {
		return err
	}
	_, _, _, err = autoLive(ctx, tx, c.ResourceID, d.ID, true)
	return err
}

// autoLive deliberately takes no push quota lock: runner transactions already
// own task/lease -> destination -> proxy/resources before policy/demand. Policy
// writers take the quota first and cannot pass those same placement locks.
func autoLive(ctx context.Context, tx pgx.Tx, resource, demand string, deadline bool) (githubPushDocument, autoContext, sqlcgen.HostingAutoDeployDemand, error) {
	q := sqlcgen.New(tx)
	var doc githubPushDocument
	var c autoContext
	var row sqlcgen.HostingAutoDeployDemand
	policy, err := q.ReadGitHubPushPolicy(ctx, resource)
	if err != nil {
		return doc, c, row, err
	}
	doc, err = pushDocument(policy)
	if err != nil {
		return doc, c, row, err
	}
	if !doc.Policy.Enabled || doc.Configuration.AutomaticDeployment == nil {
		return doc, c, row, ErrConflict
	}
	if err = pushCurrent(ctx, tx, doc, true); err != nil {
		return doc, c, row, err
	}
	c, err = autoCurrent(ctx, tx, doc)
	if err != nil {
		return doc, c, row, err
	}
	locked, err := q.LockGitHubPushPolicy(ctx, resource)
	if err != nil {
		return doc, c, row, err
	}
	if locked.Generation != policy.Generation || string(locked.Body) != string(policy.Body) {
		return doc, c, row, ErrConflict
	}
	control, err := q.AutoLockControl(ctx, resource)
	if err != nil {
		return doc, c, row, err
	}
	if control.ManualEpoch != c.ManualEpoch {
		return doc, c, row, ErrConflict
	}
	row, err = q.AutoLockDemand(ctx, resource)
	if err != nil {
		return doc, c, row, err
	}
	d, err := decodeAutoDemand(row)
	if err != nil {
		return doc, c, row, err
	}
	if row.ID != demand || row.PolicyID != policy.ID || row.PolicyGeneration != policy.Generation || row.CommitSha != doc.Policy.DesiredCommit || row.BuildID != doc.Policy.LastBuildID || d.Context.ManualEpoch != c.ManualEpoch || d.Context.ConfigurationHash != c.ConfigurationHash || d.Context.OwnerID != c.OwnerID || d.Context.ResourceGeneration != c.ResourceGeneration || d.Context.SourceGeneration != c.SourceGeneration || d.Context.RuntimeGeneration != c.RuntimeGeneration || d.Context.Placement != c.Placement || d.Context.RuntimeNamespace != c.RuntimeNamespace || d.Context.ProxyID != c.ProxyID || d.Context.ProxyConfigurationHash != c.ProxyConfigurationHash || d.Context.DaemonID != c.DaemonID {
		return doc, c, row, ErrConflict
	}
	if deadline {
		now, e := pushClock(ctx, tx)
		if e != nil {
			return doc, c, row, e
		}
		if !row.ExpiresAt.After(now) || row.Attempts > domain.GitHubPushMaxAttempts {
			return doc, c, row, ErrConflict
		}
	}
	return doc, c, row, nil
}

// autoFirstMutation is called only for a first apply admission, before and
// after blocking writes. Already admitted immutable receipts use existing replay.
func autoFirstMutation(ctx context.Context, tx pgx.Tx, operation string, prelock bool) error {
	q := sqlcgen.New(tx)
	origin, err := q.AutoReadOrigin(ctx, operation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	o, err := decodeAutoOrigin(origin)
	if err != nil {
		return err
	}
	if prelock {
		if _, err = autoPrelock(ctx, tx, origin.ResourceID); err != nil {
			return err
		}
	}
	_, c, row, err := autoLive(ctx, tx, origin.ResourceID, origin.DemandID, true)
	if err != nil {
		return err
	}
	if row.Stage != "waiting_deployment" && row.Stage != "waiting_switch" || o.Attempts <= 0 || o.Attempts > row.Attempts || o.Attempts > domain.GitHubPushMaxAttempts {
		return ErrConflict
	}
	if origin.Kind == "deployment" && (c.BaselineOperationID != o.BaselineOperationID || c.BaselineGeneration != o.BaselineGeneration) {
		return ErrConflict
	}
	for _, route := range o.Routes {
		r, e := q.ProxyGetRoute(ctx, route.ID)
		if e != nil {
			return e
		}
		if origin.Kind == "switch" {
			route.Version++
			route.ReleaseID = o.Demand.DeploymentID
		}
		if !autoRouteSelection(r, route) {
			return ErrConflict
		}
	}
	return nil
}

func autoAdvance(ctx context.Context, tx pgx.Tx, row sqlcgen.HostingAutoDeployDemand, stage, reason string, attempts int32, loaded int64) error {
	_, err := sqlcgen.New(tx).AutoAdvanceDemand(ctx, sqlcgen.AutoAdvanceDemandParams{ResourceID: row.ResourceID, ID: row.ID, Stage: stage, Reason: reason, Attempts: attempts, RetrySeconds: 10, ObservedLoadedGeneration: loaded})
	return err
}

func autoOriginInsert(ctx context.Context, tx pgx.Tx, kind string, o autoOrigin) error {
	id := o.Demand.DeploymentID
	var deploy, proxy *string
	if kind == "deployment" {
		deploy = &id
	} else {
		id = o.Demand.SwitchID
		proxy = &id
	}
	_, err := sqlcgen.New(tx).AutoInsertOrigin(ctx, sqlcgen.AutoInsertOriginParams{OperationID: id, Kind: kind, ResourceID: o.Demand.Context.ResourceID, DemandID: o.Demand.ID, BuildID: o.Demand.BuildID, DeploymentOperationID: deploy, ProxyOperationID: proxy, Body: buildJSON(o), BodyHash: domain.HostingDocumentHash(o)})
	return err
}

// PrepareHostingAutoDeploy advances one durable current demand using only the
// existing deployment/switch admissions. It performs no orchestration network IO.
func (s *PostgresStore) PrepareHostingAutoDeploy(ctx context.Context, resource string) error {
	if s.hostingJobs == nil || !s.hostingJobs.AutoDeployEnabled() {
		return hostingjobs.ErrUnavailable
	}
	// Superseded nonadmitted work is settled in task-first transactions, never
	// while holding the controller's destination/resource prefix.
	if err := s.autoCleanupSuperseded(ctx, resource); err != nil {
		return err
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		if err := q.LockGitHubPushQuota(ctx); err != nil {
			return false, err
		}
		if _, err := autoPrelock(ctx, tx, resource); err != nil {
			return false, err
		}
		row, err := q.AutoReadDemand(ctx, resource)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		d, err := decodeAutoDemand(row)
		if err != nil {
			return false, err
		}
		originalRow := row
		doc, c, row, err := autoLive(ctx, tx, resource, d.ID, false)
		if err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
				return true, autoAdvance(ctx, tx, originalRow, "blocked", "authority_changed", originalRow.Attempts, 0)
			}
			return false, err
		}
		c, err = autoReconcileBaseline(ctx, tx, doc, c)
		if err != nil {
			return false, err
		}
		if row.Stage == "succeeded" || row.Stage == "failed" || row.Stage == "blocked" || row.Stage == "expired" {
			return true, nil
		}
		// A completed exact switch is reconciled even after its original demand
		// expires. No new host mutation is authorized by this observation.
		if row.Stage == "waiting_switch" {
			op, e := proxyOperation(ctx, tx, d.SwitchID)
			if e != nil {
				return false, e
			}
			if op.State == "applied" {
				return true, s.autoSuccess(ctx, tx, doc, c, row, d, op)
			}
			if op.MutationHold || op.State == "uncertain" {
				return true, autoAdvance(ctx, tx, row, row.Stage, "switch_hold", row.Attempts, 0)
			}
			if op.State == "rolled_back" || op.State == "refused" || op.State == "canceled" || op.State == "superseded" {
				return true, autoAdvance(ctx, tx, row, "failed", "switch_failed", row.Attempts, 0)
			}
			return true, autoAdvance(ctx, tx, row, row.Stage, "switch_pending", row.Attempts, 0)
		}
		now, e := pushClock(ctx, tx)
		if e != nil {
			return false, e
		}
		if !row.ExpiresAt.After(now) {
			return true, autoAdvance(ctx, tx, row, "expired", "deadline_expired", row.Attempts, 0)
		}
		if row.Stage == "waiting_build" {
			b, e := readBuild(ctx, q, d.BuildID)
			if e != nil {
				return false, e
			}
			if b.State == "failed" || b.State == "canceled" || b.State == "cleanup_required" {
				return true, autoAdvance(ctx, tx, row, "failed", "build_failed", row.Attempts, 0)
			}
			if b.State != "succeeded" {
				return true, autoAdvance(ctx, tx, row, row.Stage, "build_pending", row.Attempts, 0)
			}
			if b.Artifact == nil || b.SourceReceipt == nil || b.SourceReceipt.Commit != d.Commit {
				return true, autoAdvance(ctx, tx, row, "blocked", "artifact_mismatch", row.Attempts, 0)
			}
		} else {
			op, e := hostingOperation(ctx, tx, d.DeploymentID)
			if e != nil {
				return false, e
			}
			if op.MutationHold {
				return true, autoAdvance(ctx, tx, row, row.Stage, "deployment_hold", row.Attempts, 0)
			}
			if op.State == "failed" || op.State == "canceled" {
				return true, autoAdvance(ctx, tx, row, "failed", "deployment_failed", row.Attempts, 0)
			}
			if op.State != "succeeded" {
				return true, autoAdvance(ctx, tx, row, row.Stage, "deployment_pending", row.Attempts, 0)
			}
		}
		if row.Attempts >= domain.GitHubPushMaxAttempts {
			return true, autoAdvance(ctx, tx, row, "blocked", "budget_exhausted", row.Attempts, 0)
		}
		if err = autoSelections(ctx, tx, c); err != nil {
			return true, autoAdvance(ctx, tx, row, "blocked", "route_drift", row.Attempts, 0)
		}
		save, e := tx.Begin(ctx)
		if e != nil {
			return false, e
		}
		stage, reason, operation := "waiting_deployment", "deployment_queued", d.DeploymentID
		o := autoOrigin{Version: 1, Demand: d, Routes: c.Routes, BaselineOperationID: c.BaselineOperationID, BaselineGeneration: c.BaselineGeneration, Attempts: row.Attempts + 1}
		cfg := doc.Configuration.AutomaticDeployment
		if row.Stage == "waiting_build" {
			_, _, err = s.requestHostingBuildDeploymentTx(ctx, save, d.DeploymentID, resource, doc.Configuration.AutomationActorID, domain.HostingBuildDeploymentInput{ExpectedGeneration: c.ResourceGeneration, ExpectedSourceGeneration: c.SourceGeneration, ExpectedRuntimeConfigurationGeneration: c.RuntimeGeneration, BuildID: d.BuildID, ContainerPort: cfg.ContainerPort, HostPort: 0, Readiness: cfg.Readiness, IdempotencyKey: d.DeploymentID}, domain.AuditEvent{ID: "aud_" + d.DeploymentID})
			if err == nil {
				err = autoOriginInsert(ctx, save, "deployment", o)
			}
		} else {
			p, e := proxyLoad(ctx, save, c.ProxyID)
			if e != nil {
				err = e
			} else {
				prior, e := proxyLoadedPredecessor(ctx, save, p)
				if e != nil {
					err = e
				} else if !autoLoadedHTTPS(prior, c.Routes, now) {
					err = ErrConflict
				} else {
					picks := make([]domain.HostingRouteSwitchSelection, 0, len(c.Routes))
					for _, r := range c.Routes {
						picks = append(picks, domain.HostingRouteSwitchSelection{RouteID: r.ID, ExpectedVersion: r.Version, ExpectedSelectedReleaseOperationID: r.ReleaseID})
					}
					// Use the current verified predecessor for unrelated routes, while
					// preserving exact owned selections from the accepted baseline.
					o.BaselineOperationID, o.BaselineGeneration = prior.ID, p.LoadedGeneration
					_, _, err = s.requestHostingRouteSwitchTx(ctx, save, c.Placement, resource, d.SwitchID, doc.Configuration.AutomationActorID, domain.HostingRouteSwitchInput{ExpectedLoadedGeneration: p.LoadedGeneration, TargetReleaseOperationID: d.DeploymentID, Routes: picks, IdempotencyKey: d.SwitchID}, domain.AuditEvent{ID: "aud_" + d.SwitchID})
					if err == nil {
						err = autoOriginInsert(ctx, save, "switch", o)
					}
				}
			}
			stage, reason, operation = "waiting_switch", "switch_queued", d.SwitchID
		}
		if err != nil {
			if e = save.Rollback(ctx); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
				return false, e
			}
			if !errors.Is(err, ErrConflict) && !errors.Is(err, auth.ErrForbidden) {
				return false, err
			}
			return true, autoAdvance(ctx, tx, row, row.Stage, "admission_deferred", row.Attempts+1, 0)
		}
		if err = save.Commit(ctx); err != nil {
			return false, err
		}
		if err = autoAdvance(ctx, tx, row, stage, reason, row.Attempts+1, 0); err != nil {
			return false, err
		}
		if err = autoFirstMutation(ctx, tx, operation, false); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) autoSuccess(ctx context.Context, tx pgx.Tx, doc githubPushDocument, c autoContext, row sqlcgen.HostingAutoDeployDemand, d autoDemand, op domain.HostingProxyOperation) error {
	q := sqlcgen.New(tx)
	origin, err := q.AutoReadOrigin(ctx, op.ID)
	if err != nil {
		return err
	}
	o, err := decodeAutoOrigin(origin)
	if err != nil || o.Demand.ID != d.ID || o.Demand.PolicyGeneration != doc.Policy.Generation || o.Demand.Context.ManualEpoch != c.ManualEpoch {
		return ErrConflict
	}
	routes := append([]autoRoute(nil), o.Routes...)
	for n := range routes {
		routes[n].Version++
		routes[n].ReleaseID = d.DeploymentID
	}
	p, err := proxyLoad(ctx, tx, c.ProxyID)
	if err != nil {
		return err
	}
	if p.HoldOperationID != "" || p.RestoreOwnershipHeld {
		return autoAdvance(ctx, tx, row, row.Stage, "switch_hold", row.Attempts, 0)
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	now, err := pushClock(ctx, tx)
	if err != nil {
		return err
	}
	if !autoLoadedHTTPS(op, routes, now) || !autoLoadedHTTPS(prior, routes, now) {
		return autoAdvance(ctx, tx, row, "blocked", "route_drift", row.Attempts, 0)
	}
	for _, r := range routes {
		current, e := q.ProxyGetRoute(ctx, r.ID)
		if e != nil {
			return e
		}
		if !autoRouteSelection(current, r) {
			return autoAdvance(ctx, tx, row, "blocked", "route_drift", row.Attempts, 0)
		}
	}
	c.Routes, c.BaselineOperationID, c.BaselineGeneration = routes, prior.ID, p.LoadedGeneration
	if _, err = q.AutoUpdateBaseline(ctx, sqlcgen.AutoUpdateBaselineParams{ResourceID: c.ResourceID, ManualEpoch: c.ManualEpoch, Body: buildJSON(c), BodyHash: domain.HostingDocumentHash(c)}); err != nil {
		return err
	}
	if err = autoAdvance(ctx, tx, row, "succeeded", "https_verified", row.Attempts, p.LoadedGeneration); err != nil {
		return err
	}
	if _, _, _, err = autoLive(ctx, tx, c.ResourceID, d.ID, false); err != nil {
		return err
	}
	now, err = pushClock(ctx, tx)
	if err != nil {
		return err
	}
	if !autoLoadedHTTPS(op, routes, now) || !autoLoadedHTTPS(prior, routes, now) {
		return ErrConflict
	}
	return nil
}

// Wakeups carry no authority and hold no quota/policy/demand locks. A savepoint
// keeps optional queue unavailability from discarding an authoritative receipt;
// bounded recovery independently rediscovers the durable row.
func (s *PostgresStore) autoWakeup(ctx context.Context, tx pgx.Tx, resource string) error {
	if !s.hostingJobs.AutoDeployEnabled() {
		return nil
	}
	save, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err = s.hostingJobs.EnqueueAutoDeployTx(ctx, save, hostingjobs.AutoDeployArgs{ResourceID: resource}); err != nil {
		return save.Rollback(ctx)
	}
	return save.Commit(ctx)
}

func (s *PostgresStore) recoverAutoDeployDispatch(ctx context.Context) error {
	if !s.hostingJobs.AutoDeployEnabled() {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		resources, err := q.AutoRecoverDue(ctx)
		if err != nil {
			return false, err
		}
		for _, resource := range resources {
			if _, err = s.hostingJobs.EnqueueAutoDeployTx(ctx, tx, hostingjobs.AutoDeployArgs{ResourceID: resource}); err != nil {
				return false, err
			}
			if err = q.AutoTouchWakeup(ctx, resource); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}

// autoCleanupSuperseded does not impersonate a server administrator. Immutable
// automatic origin is its only scope; admission or a hold forbids settlement.
func (s *PostgresStore) autoCleanupSuperseded(ctx context.Context, resource string) error {
	origins, err := sqlcgen.New(s.pool).AutoUnsettledOrigins(ctx, resource)
	if err != nil {
		return err
	}
	for _, origin := range origins {
		if err = s.autoCleanupOrigin(ctx, origin); err != nil {
			return err
		}
	}
	return nil
}
func (s *PostgresStore) autoCleanupOrigin(ctx context.Context, before sqlcgen.HostingAutoDeployOrigin) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		o, err := decodeAutoOrigin(before)
		if err != nil {
			return false, err
		}
		var run string
		var deployment domain.HostingOperation
		var switchOp domain.HostingProxyOperation
		if before.Kind == "deployment" {
			deployment, err = hostingOperation(ctx, tx, before.OperationID)
			if err != nil {
				return false, err
			}
			run = deployment.RunID
			if err = hostingLockOperationRuns(ctx, tx, deployment); err != nil {
				return false, err
			}
		} else {
			switchOp, err = proxyOperation(ctx, tx, before.OperationID)
			if err != nil {
				return false, err
			}
			run = switchOp.RunID
			if run != "" {
				if _, err = q.LockHostingTaskForAuthority(ctx, run); err != nil {
					return false, err
				}
			}
		}
		p, err := autoPrelock(ctx, tx, before.ResourceID)
		if err != nil {
			return false, err
		}
		current, err := q.AutoReadOrigin(ctx, before.OperationID)
		if err != nil {
			return false, err
		}
		if string(current.Body) != string(before.Body) || current.BodyHash != before.BodyHash {
			return false, ErrConflict
		}
		policy, err := q.LockGitHubPushPolicy(ctx, before.ResourceID)
		if err != nil {
			return false, err
		}
		doc, err := pushDocument(policy)
		if err != nil {
			return false, err
		}
		control, err := q.AutoLockControl(ctx, before.ResourceID)
		if err != nil {
			return false, err
		}
		demand, err := q.AutoLockDemand(ctx, before.ResourceID)
		if err != nil {
			return false, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return false, err
		}
		stale := demand.ID != o.Demand.ID || policy.Generation != o.Demand.PolicyGeneration || !doc.Policy.Enabled || doc.Configuration.AutomaticDeployment == nil || control.ManualEpoch != o.Demand.Context.ManualEpoch || !o.Demand.ExpiresAt.After(now) || demand.Stage == "blocked" || demand.Stage == "expired" || demand.Stage == "failed"
		if !stale {
			return true, nil
		}
		admitted, err := q.AutoOriginAdmitted(ctx, before.OperationID)
		if err != nil {
			return false, err
		}
		if admitted == nil || *admitted {
			return true, nil
		}
		if before.Kind == "deployment" {
			deployment, err = hostingOperation(ctx, tx, before.OperationID)
			if err != nil {
				return false, err
			}
			if deployment.RunID != run {
				return false, ErrConflict
			}
			if deployment.MutationHold || deployment.State == "succeeded" || deployment.State == "failed" || deployment.State == "canceled" {
				return true, nil
			}
			hold, err := q.LockHostingMutationHold(ctx, deployment.BindingID)
			if err != nil {
				return false, err
			}
			if hold != nil {
				return true, nil
			}
			if _, err = q.CancelHostingOperationState(ctx, sqlcgen.CancelHostingOperationStateParams{ID: deployment.ID, State: "canceled"}); err != nil {
				return false, err
			}
			if _, err = q.ReleaseHostingFailedClaim(ctx, deployment.ID); err != nil {
				return false, err
			}
		} else {
			switchOp, err = proxyOperation(ctx, tx, before.OperationID)
			if err != nil {
				return false, err
			}
			if switchOp.RunID != run {
				return false, ErrConflict
			}
			if switchOp.MutationHold || p.HoldOperationID != "" || p.RestoreOwnershipHeld {
				return true, nil
			}
			if switchOp.State != "queued" && switchOp.State != "dispatched" && switchOp.State != "prepared" {
				return true, nil
			}
			conflict, err := proxyRestoreSwitchMetadata(ctx, tx, p, switchOp)
			if err != nil {
				return false, err
			}
			state := "canceled"
			if conflict {
				state = "uncertain"
			}
			if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: switchOp.ID, State: state}); err != nil {
				return false, err
			}
			if !conflict {
				if _, err = q.ProxySettleUnadmitted(ctx, sqlcgen.ProxySettleUnadmittedParams{ID: p.ID, ActiveOperationID: proxyPointer(switchOp.ID)}); err != nil {
					return false, err
				}
			}
		}
		if run != "" {
			if _, err = q.CancelHostingLeases(ctx, run); err != nil {
				return false, err
			}
			if _, err = q.CancelHostingTask(ctx, run); err != nil {
				return false, err
			}
		}
		if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), domain.AuditEvent{ID: "aud_auto_superseded_" + before.OperationID, ActorID: doc.Configuration.AutomationActorID, Action: "hosting.auto_deploy_superseded", TargetID: before.OperationID, Metadata: map[string]any{"resource_id": before.ResourceID, "demand_id": before.DemandID}}); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

// An older admitted switch may finish after a newer desired push. Its exact
// matched provenance may advance the accepted route baseline, but never the
// newer demand's stage, commit, operation identity or success receipt.
func autoReconcileBaseline(ctx context.Context, tx pgx.Tx, doc githubPushDocument, c autoContext) (autoContext, error) {
	q := sqlcgen.New(tx)
	origins, err := q.AutoAppliedOrigins(ctx, c.ResourceID)
	if err != nil {
		return c, err
	}
	for _, row := range origins {
		o, err := decodeAutoOrigin(row)
		if err != nil {
			return c, err
		}
		if o.Demand.PolicyID != doc.Policy.ID || o.Demand.PolicyGeneration != doc.Policy.Generation || o.Demand.Context.ManualEpoch != c.ManualEpoch || o.Demand.Context.ConfigurationHash != c.ConfigurationHash || len(o.Routes) != len(c.Routes) {
			continue
		}
		matched := true
		for n, r := range o.Routes {
			if r != c.Routes[n] {
				matched = false
			}
		}
		if !matched {
			continue
		}
		op, err := proxyOperation(ctx, tx, row.OperationID)
		if err != nil {
			return c, err
		}
		routes := append([]autoRoute(nil), o.Routes...)
		for n := range routes {
			routes[n].Version++
			routes[n].ReleaseID = o.Demand.DeploymentID
		}
		for _, r := range routes {
			current, e := q.ProxyGetRoute(ctx, r.ID)
			if e != nil {
				return c, e
			}
			if !autoRouteSelection(current, r) {
				matched = false
			}
		}
		if !matched {
			continue
		}
		p, err := proxyLoad(ctx, tx, c.ProxyID)
		if err != nil {
			return c, err
		}
		if p.RestoreOwnershipHeld || p.HoldOperationID != "" {
			continue
		}
		prior, err := proxyLoadedPredecessor(ctx, tx, p)
		if err != nil {
			return c, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return c, err
		}
		if !autoLoadedHTTPS(op, routes, now) || !autoLoadedHTTPS(prior, routes, now) {
			continue
		}
		c.Routes, c.BaselineOperationID, c.BaselineGeneration = routes, prior.ID, p.LoadedGeneration
		if _, err = q.AutoUpdateBaseline(ctx, sqlcgen.AutoUpdateBaselineParams{ResourceID: c.ResourceID, ManualEpoch: c.ManualEpoch, Body: buildJSON(c), BodyHash: domain.HostingDocumentHash(c)}); err != nil {
			return c, err
		}
		if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
			return c, err
		}
		now, err = pushClock(ctx, tx)
		if err != nil {
			return c, err
		}
		if !autoLoadedHTTPS(op, routes, now) || !autoLoadedHTTPS(prior, routes, now) {
			return c, ErrConflict
		}
		break
	}
	return c, nil
}
