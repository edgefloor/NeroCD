package store

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageContinuitySchema(ctx context.Context, tx pgx.Tx) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.hosting_storage_route_continuities') IS NOT NULL`).Scan(&exists)
	if err != nil || !exists {
		return exists, err
	}
	var compatible bool
	if err = tx.QueryRow(ctx, `SELECT nerocd_hosting_storage_route_continuity_schema_compatible()`).Scan(&compatible); err != nil {
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
	var held bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND state IN ('reserved','unresolved'))`, proxy).Scan(&held)
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
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND state IN ('proxy_pending','proxy_failed'))`, proxy).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
}

func storageContinuityRouteUnsettled(ctx context.Context, db sqlcgen.DBTX, row sqlcgen.HostingProxyRoute) (string, error) {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.hosting_storage_route_continuities') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return "", err
	}
	var state string
	err := db.QueryRow(ctx, `SELECT coalesce((SELECT state FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND descriptor->>'resource_id'=$2 AND state IN ('reserved','unresolved','proxy_pending','proxy_failed') ORDER BY created_at DESC LIMIT 1),'')`, row.ProxyID, row.ResourceID).Scan(&state)
	return state, err
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
	var foreign bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM hosting_proxy_routes r JOIN hosting_snapshots s ON s.operation_id=r.selected_release_operation_id WHERE r.enabled AND s.body->>'destination_id'=$1 AND (s.body->>'host_port')::integer=$2 AND NOT(r.proxy_id=$3 AND r.resource_id=$4 AND r.binding_id=$5 AND r.selected_release_operation_id=$6)
 UNION ALL SELECT 1 FROM hosting_proxy_release_references r JOIN hosting_proxy_operations o ON o.id=r.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id JOIN hosting_snapshots s ON s.operation_id=r.release_operation_id WHERE s.body->>'destination_id'=$1 AND (s.body->>'host_port')::integer=$2 AND (p.loaded_generation=o.generation OR p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled')) AND NOT(o.id=$7 AND r.release_operation_id=$6 AND r.route_id=ANY($8::text[]))
 UNION ALL SELECT 1 FROM hosting_proxy_switch_prior_references r JOIN hosting_proxy_operations o ON o.id=r.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id JOIN hosting_snapshots s ON s.operation_id=r.prior_release_operation_id WHERE s.body->>'destination_id'=$1 AND (s.body->>'host_port')::integer=$2 AND (p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))
)`, c.DestinationID, c.HostPort, c.ProxyID, c.ResourceID, c.BindingID, c.PreviousHealthy.OperationID, prior.ID, storageContinuityRouteIDs(c)).Scan(&foreign)
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
	if snap.Storage == (domain.HostingStorageRef{}) || snap.HostPort != in.HostPort || snap.HostPort < 1024 {
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
	var body, accepted []byte
	var hash, state, requestHash string
	err = tx.QueryRow(ctx, `SELECT c.descriptor,c.descriptor_hash,c.state,c.accepted_request,o.request_hash FROM hosting_storage_route_continuities c JOIN hosting_operations o ON o.id=c.operation_id WHERE c.operation_id=$1`, op.ID).Scan(&body, &hash, &state, &accepted, &requestHash)
	if err != nil {
		return c, state, err
	}
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
	var automatic bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_auto_deploy_origins WHERE operation_id=$1 OR deployment_operation_id=$1)`, op.ID).Scan(&automatic); err != nil {
		return err
	}
	if automatic {
		return ErrConflict
	}
	if op.Plan != nil && (op.Plan.PreviousHealthy == nil || domain.HostingDocumentHash(op.Plan.PreviousHealthy) != domain.HostingDocumentHash(c.PreviousHealthy) || op.Plan.RetainPrevious) {
		return ErrConflict
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&raw); err != nil {
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
		_, err := tx.Exec(ctx, `UPDATE hosting_storage_route_continuities SET state=$2 WHERE operation_id=$1 AND state='reserved'`, op.ID, state)
		return err
	}
	// A metadata conflict cannot erase committed evidence of runtime success.
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = s.queueStorageContinuity(ctx, nested, op)
	if err == nil {
		return nested.Commit(ctx)
	}
	if rollbackErr := nested.Rollback(ctx); rollbackErr != nil {
		return rollbackErr
	}
	_, markErr := tx.Exec(ctx, `UPDATE hosting_storage_route_continuities SET state='unresolved' WHERE operation_id=$1 AND state='reserved'`, op.ID)
	return markErr
}

func (s *PostgresStore) queueStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	p, rows, err := storageContinuityPrelock(ctx, tx, op.ResourceID)
	if err != nil {
		return err
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return err
	}
	if state != "reserved" {
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
	_, err = s.queueProxyGeneration(ctx, tx, p, proxyID, op.ProjectID, "", op.CreatedBy, "storage-continuity:"+op.ID, c.RequestHash, domain.AuditEvent{ID: "aud_proxy_storage_" + op.ID, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hosting_storage_route_continuities SET state='proxy_pending',successor_proxy_operation_id=$2 WHERE operation_id=$1 AND state='reserved'`, op.ID, proxyID)
	return err
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
	_, err = tx.Exec(ctx, `UPDATE hosting_storage_route_continuities SET state=$2 WHERE successor_proxy_operation_id=$1 AND state='proxy_pending'`, op.ID, state)
	return err
}

func releaseUnmutatedStorageContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) || op.MutationHold {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE hosting_storage_route_continuities SET state='released' WHERE operation_id=$1 AND state='reserved' AND NOT EXISTS(SELECT 1 FROM hosting_storage_operation_admissions WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1)`, op.ID)
	return err
}
