package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageContinuitySchema(ctx context.Context, tx pgx.Tx) (bool, error) {
	exists, err := sqlcgen.New(tx).StorageContinuitySchemaPresent(ctx)
	if err != nil || !exists {
		return exists, err
	}
	compatible, err := sqlcgen.New(tx).StorageContinuitySchemaCompatible(ctx)
	if err != nil {
		return false, err
	}
	if !compatible {
		return false, ErrConflict
	}
	return true, nil
}

// storageContinuityPrelock must precede any individual resource lock. The
// destination lock serializes route additions with this complete resource set.
func storageContinuityPrelock(ctx context.Context, tx pgx.Tx, resource string) (domain.HostingProxy, []sqlcgen.HostingProxyRoute, error) {
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, resource)
	if err != nil {
		return domain.HostingProxy{}, nil, hostingError(err)
	}
	if err = q.LockHostingExecutionDestination(ctx, placement.DestinationID); err != nil {
		return domain.HostingProxy{}, nil, err
	}
	row, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if err != nil {
		return domain.HostingProxy{}, nil, hostingError(err)
	}
	// The reservation owner bypasses the ordinary proxy reservation barrier,
	// never the proxy row lock itself.
	if _, err = q.ProxyLock(ctx, row.ID); err != nil {
		return domain.HostingProxy{}, nil, err
	}
	p, err := proxyLoad(ctx, tx, row.ID)
	if err != nil {
		return p, nil, err
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return p, nil, err
	}
	locks := append(append([]sqlcgen.HostingProxyRoute(nil), rows...), sqlcgen.HostingProxyRoute{ResourceID: resource})
	if err = proxyLockRouteResources(ctx, tx, locks); err != nil {
		return p, nil, err
	}
	return p, rows, nil
}

func storageContinuityBarrier(ctx context.Context, tx pgx.Tx, proxy string) error {
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil || !exists {
		return err
	}
	held, err := sqlcgen.New(tx).StorageContinuityExclusive(ctx, proxy)
	if err != nil {
		return err
	}
	if held {
		return ErrConflict
	}
	return nil
}

// Once queued, the existing proxy operation owns mutation. Ordinary generation
// supersession still needs an explicit barrier until its runner receipt settles.
func storageContinuityPendingBarrier(ctx context.Context, tx pgx.Tx, proxy string) error {
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil || !exists {
		return err
	}
	pending, err := sqlcgen.New(tx).StorageContinuityProxyPending(ctx, proxy)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
}

func storageContinuityRouteUnsettled(ctx context.Context, db sqlcgen.DBTX, row sqlcgen.HostingProxyRoute) (string, error) {
	exists, err := sqlcgen.New(db).StorageContinuitySchemaPresent(ctx)
	if err != nil || !exists {
		return "", err
	}
	return sqlcgen.New(db).StorageContinuityRouteState(ctx, sqlcgen.StorageContinuityRouteStateParams{ProxyID: row.ProxyID, ResourceID: row.ResourceID})
}

func storageContinuityLoadedScope(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, c domain.HostingStorageRouteContinuity, rows []sqlcgen.HostingProxyRoute, initial bool) error {
	if !p.Enabled || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.RestoreOwnershipHeld || p.LoadedGeneration != c.Request.ExpectedLoadedGeneration || p.DaemonID != c.DaemonID || p.DestinationID != c.DestinationID || p.ServerID != c.ServerID || p.RunnerID != c.RunnerID {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	pending, err := q.ProxyPending(ctx, p.ID)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	if prior.ID != c.Request.ExpectedLoadedProxyOperationID || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash {
		return ErrConflict
	}
	acceptedAt, err := q.DatabaseClock(ctx)
	if err != nil {
		return err
	}
	if err = storageContinuityTrustedRoutes(prior, c, acceptedAt); err != nil {
		return err
	}
	current := make([]domain.HostingProxyRouteTarget, 0, len(rows))
	own := make([]domain.HostingRouteSwitchSelection, 0, len(c.Request.Routes))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if initial || row.ResourceID != c.ResourceID {
			if err = proxyRouteBindingAvailable(ctx, tx, p, row); err != nil {
				return err
			}
		}
		target, targetErr := proxyHealthyTarget(ctx, tx, p, row)
		if targetErr != nil && row.ResourceID == c.ResourceID {
			target, targetErr = stoppedStackRouteTarget(ctx, tx, p, row)
		}
		if targetErr != nil {
			return targetErr
		}
		current = append(current, target)
		if row.ResourceID == c.ResourceID {
			if row.BindingID != c.BindingID || row.ProjectID != c.ProjectID {
				return ErrConflict
			}
			own = append(own, domain.HostingRouteSwitchSelection{RouteID: row.ID, ExpectedVersion: row.Version, ExpectedSelectedReleaseOperationID: row.SelectedReleaseOperationID})
		}
	}
	canonical, err := domain.CanonicalHostingStorageRouteContinuityInput(domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: c.Request.ExpectedLoadedGeneration, ExpectedLoadedProxyOperationID: prior.ID, Routes: own})
	if err != nil || !reflect.DeepEqual(canonical.Routes, c.Request.Routes) || !reflect.DeepEqual(proxySortedTargets(current), proxySortedTargets(prior.Snapshot.Routes)) {
		return ErrConflict
	}
	// Preserve the global guard except for this exact resource's loaded routes.
	foreign, err := q.StorageContinuityForeignPortReferenced(ctx, sqlcgen.StorageContinuityForeignPortReferencedParams{
		DestinationID: c.DestinationID, HostPort: int32(c.HostPort), ProxyID: c.ProxyID, ResourceID: c.ResourceID, BindingID: c.BindingID,
		PreviousOperationID: c.PreviousHealthy.OperationID, LoadedProxyOperationID: prior.ID, RouteIds: storageContinuityRouteIDs(c),
	})
	if err != nil {
		return err
	}
	if foreign {
		return ErrConflict
	}
	return nil
}

func storageContinuityTrustedRoutes(prior domain.HostingProxyOperation, c domain.HostingStorageRouteContinuity, acceptedAt time.Time) error {
	if prior.Result == nil {
		return ErrConflict
	}
	for _, pick := range c.Request.Routes {
		host := ""
		for _, target := range prior.Snapshot.Routes {
			if target.RouteID == pick.RouteID {
				host = target.Hostname
			}
		}
		qualified := false
		for _, observation := range prior.Result.Routes {
			if observation.RouteID == pick.RouteID && observation.HTTPReady && observation.HTTPSReady && observation.Certificate.Hostname == host && observation.Certificate.Status == "trusted" && observation.Certificate.ExpiresAt.After(acceptedAt) {
				qualified = true
			}
		}
		if host == "" || !qualified {
			return ErrConflict
		}
	}
	return nil
}

// validateStorageContinuityFinal takes no new locks. Fresh admission has already
// locked destination, proxy and all route resources. Repeat the time-sensitive
// precondition after origin/audit/authority waits, immediately before the ACK.
func validateStorageContinuityFinal(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return nil
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return err
	}
	if state != "reserved" {
		return ErrConflict
	}
	p, err := proxyLoad(ctx, tx, c.ProxyID)
	if err != nil {
		return err
	}
	if !p.Enabled || p.LoadedGeneration != c.Request.ExpectedLoadedGeneration || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.RestoreOwnershipHeld || p.DaemonID != c.DaemonID {
		return ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	if prior.ID != c.Request.ExpectedLoadedProxyOperationID || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash {
		return ErrConflict
	}
	acceptedAt, err := sqlcgen.New(tx).DatabaseClock(ctx)
	if err != nil {
		return err
	}
	return storageContinuityTrustedRoutes(prior, c, acceptedAt)
}

func storageContinuityRouteIDs(c domain.HostingStorageRouteContinuity) []string {
	ids := make([]string, len(c.Request.Routes))
	for n, r := range c.Request.Routes {
		ids[n] = r.RouteID
	}
	return ids
}

func prepareStorageContinuity(ctx context.Context, tx pgx.Tx, operation, project string, snap *domain.HostingSnapshot, in domain.HostingDeploymentInput) (*domain.HostingStorageRouteContinuity, error) {
	if in.StorageRouteContinuity == nil {
		return nil, nil
	}
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrConflict
	}
	if snap.Storage == (domain.HostingStorageRef{}) && snap.Stack == (domain.HostingStackRef{}) || snap.HostPort != in.HostPort || snap.HostPort < 1024 {
		return nil, ErrConflict
	}
	p, rows, err := storageContinuityPrelock(ctx, tx, snap.ResourceID)
	if err != nil {
		return nil, err
	}
	if err = storageContinuityBarrier(ctx, tx, p.ID); err != nil {
		return nil, err
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, snap.BindingID)
	if err != nil {
		return nil, err
	}
	var healthy domain.HostingHealthy
	if err = json.Unmarshal(raw, &healthy); err != nil {
		return nil, err
	}
	c := &domain.HostingStorageRouteContinuity{OperationID: operation, RequestHash: domain.HostingDocumentHash(in), Request: *in.StorageRouteContinuity, ProxyID: p.ID, ProjectID: project, ResourceID: snap.ResourceID, BindingID: snap.BindingID, DestinationID: snap.DestinationID, ServerID: snap.ServerID, RunnerID: snap.RunnerID, HostPort: snap.HostPort, DaemonID: p.DaemonID, PreviousHealthy: healthy, LoadedSnapshotHash: domain.HostingDocumentHash(prior.Snapshot)}
	snap.StorageRouteContinuity.Hash = domain.HostingDocumentHash(c)
	if c.Validate(*snap) != nil {
		return nil, ErrConflict
	}
	if err = storageContinuityLoadedScope(ctx, tx, p, *c, rows, true); err != nil {
		return nil, err
	}
	return c, nil
}

func readStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) (domain.HostingStorageRouteContinuity, string, error) {
	var c domain.HostingStorageRouteContinuity
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil {
		return c, "", err
	}
	if !exists {
		return c, "", ErrConflict
	}
	row, err := sqlcgen.New(tx).ReadStorageRouteContinuity(ctx, op.ID)
	if err != nil {
		return c, "", err
	}
	body, accepted, hash, state, requestHash := row.Descriptor, row.AcceptedRequest, row.DescriptorHash, row.State, row.RequestHash
	var input domain.HostingDeploymentInput
	if json.Unmarshal(body, &c) != nil || json.Unmarshal(accepted, &input) != nil || c.OperationID != op.ID || c.ProjectID != op.ProjectID || c.RequestHash != requestHash || domain.HostingDocumentHash(input) != requestHash || hash != op.Snapshot.StorageRouteContinuity.Hash || c.Validate(op.Snapshot) != nil {
		return c, state, ErrConflict
	}
	return c, state, nil
}

// validateStorageContinuityFresh is called by the existing materialization path
// through hostingFreshRuntimeGate, before a volume-create ACK or writer grant.
func validateStorageContinuityFresh(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return nil
	}
	p, rows, err := storageContinuityPrelock(ctx, tx, op.ResourceID)
	if err != nil {
		return err
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return err
	}
	if state != "reserved" || c.ProxyID != p.ID {
		return ErrConflict
	}
	if op.Release != nil && op.Release.DaemonID != c.DaemonID {
		return ErrConflict
	}
	automatic, err := sqlcgen.New(tx).StorageContinuityAutomatic(ctx, op.ID)
	if err != nil {
		return err
	}
	if automatic {
		return ErrConflict
	}
	if op.Plan != nil && (op.Plan.PreviousHealthy == nil || domain.HostingDocumentHash(op.Plan.PreviousHealthy) != domain.HostingDocumentHash(c.PreviousHealthy) || op.Plan.RetainPrevious) {
		return ErrConflict
	}
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, op.BindingID)
	if err != nil {
		return err
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(raw, &healthy) != nil || domain.HostingDocumentHash(healthy) != domain.HostingDocumentHash(c.PreviousHealthy) {
		return ErrConflict
	}
	return storageContinuityLoadedScope(ctx, tx, p, c, rows, false)
}

func (s *PostgresStore) settleStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, result domain.HostingResult) error {
	if op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return nil
	}
	if result.Outcome == "failed" && !op.MutationHold {
		return releaseUnmutatedStorageContinuity(ctx, tx, op)
	}
	if result.Outcome != "succeeded" || result.Uncertain {
		state := "unresolved"
		if result.Outcome == "failed" && result.Restored && !result.Uncertain {
			state = "released"
		}
		_, err := sqlcgen.New(tx).SetStorageContinuityReservedState(ctx, sqlcgen.SetStorageContinuityReservedStateParams{OperationID: op.ID, State: state})
		return err
	}
	// A metadata conflict cannot erase committed evidence of runtime success.
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = s.queueStorageContinuity(ctx, nested, op, "reserved", op.CreatedBy, domain.AuditEvent{ID: "aud_proxy_storage_" + op.ID, CreatedAt: time.Now().UTC()})
	if err == nil {
		return nested.Commit(ctx)
	}
	if rollbackErr := nested.Rollback(ctx); rollbackErr != nil {
		return rollbackErr
	}
	_, markErr := sqlcgen.New(tx).SetStorageContinuityReservedState(ctx, sqlcgen.SetStorageContinuityReservedStateParams{OperationID: op.ID, State: "unresolved"})
	return markErr
}

func (s *PostgresStore) queueStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, sourceState, actor string, audit domain.AuditEvent) error {
	if sourceState != "reserved" && sourceState != "unresolved" {
		return ErrConflict
	}
	p, rows, err := storageContinuityPrelock(ctx, tx, op.ResourceID)
	if err != nil {
		return err
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return err
	}
	if state != sourceState {
		return ErrConflict
	}
	// Current healthy now names the successor. Compare the pinned metadata and
	// loaded predecessor directly instead of qualifying the stopped old release.
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	if p.ID != c.ProxyID || !p.Enabled || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.RestoreOwnershipHeld || p.LoadedGeneration != c.Request.ExpectedLoadedGeneration || prior.ID != c.Request.ExpectedLoadedProxyOperationID || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash || p.DaemonID != c.DaemonID {
		return ErrConflict
	}
	pending, err := sqlcgen.New(tx).ProxyPending(ctx, p.ID)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	byID := make(map[string]sqlcgen.HostingProxyRoute, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	pinned := make(map[string]bool, len(c.Request.Routes))
	for _, r := range c.Request.Routes {
		pinned[r.RouteID] = true
	}
	current := make([]domain.HostingProxyRouteTarget, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if row.ResourceID == c.ResourceID {
			if !pinned[row.ID] {
				return ErrConflict
			}
			row.SelectedReleaseOperationID = op.ID
		} else if err = proxyRouteBindingAvailable(ctx, tx, p, row); err != nil {
			return err
		}
		target, targetErr := proxyHealthyTarget(ctx, tx, p, row)
		if targetErr != nil {
			return targetErr
		}
		if row.ResourceID == c.ResourceID {
			target.ReleaseOperationID = c.PreviousHealthy.OperationID
		}
		current = append(current, target)
	}
	if !reflect.DeepEqual(proxySortedTargets(current), proxySortedTargets(prior.Snapshot.Routes)) {
		return ErrConflict
	}
	for _, pick := range c.Request.Routes {
		r, ok := byID[pick.RouteID]
		if !ok || !r.Enabled || r.ResourceID != c.ResourceID || r.BindingID != c.BindingID || r.Version != pick.ExpectedVersion || r.SelectedReleaseOperationID != pick.ExpectedSelectedReleaseOperationID {
			return ErrConflict
		}
		changed, err := sqlcgen.New(tx).ProxySwitchRoute(ctx, sqlcgen.ProxySwitchRouteParams{ID: r.ID, SelectedReleaseOperationID: op.ID, Version: r.Version, SelectedReleaseOperationID_2: r.SelectedReleaseOperationID})
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrConflict
		}
	}
	// queueProxyGeneration uses the existing ordinary full-generation path and
	// keeps every unrelated route, with no strict rollback to a stopped writer.
	proxyID := "pop_storage_" + op.ID
	_, err = s.queueProxyGeneration(ctx, tx, p, proxyID, op.ProjectID, "", actor, "storage-continuity:"+op.ID, c.RequestHash, audit)
	if err != nil {
		return err
	}
	changed, err := sqlcgen.New(tx).QueueStorageContinuity(ctx, sqlcgen.QueueStorageContinuityParams{OperationID: op.ID, SuccessorProxyOperationID: hostingStringPointer(proxyID), State: sourceState})
	if err != nil {
		return fmt.Errorf("link storage route continuity: %w", err)
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

func settleStorageContinuityProxy(ctx context.Context, tx pgx.Tx, op domain.HostingProxyOperation, result domain.HostingProxyResult) error {
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil || !exists {
		return err
	}
	state := "proxy_failed"
	if result.Outcome == "applied" && result.LoadedGeneration == op.Generation {
		state = "loaded"
	}
	_, err = sqlcgen.New(tx).SettleStorageContinuityProxy(ctx, sqlcgen.SettleStorageContinuityProxyParams{SuccessorProxyOperationID: hostingStringPointer(op.ID), State: state})
	return err
}

func releaseUnmutatedStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) || op.MutationHold {
		return nil
	}
	_, err := sqlcgen.New(tx).ReleaseUnmutatedStorageContinuity(ctx, op.ID)
	return err
}
