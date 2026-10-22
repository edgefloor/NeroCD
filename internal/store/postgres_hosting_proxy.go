package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func proxyValidateSnapshot(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, snapshot domain.HostingProxySnapshot) error {
	if snapshot.ProxyID != p.ID || snapshot.ServerID != p.ServerID || snapshot.DestinationID != p.DestinationID || snapshot.RunnerID != p.RunnerID || !reflect.DeepEqual(snapshot.Configuration, p.Configuration) {
		return ErrConflict
	}
	ordered := append([]domain.HostingProxyRouteTarget(nil), snapshot.Routes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ResourceID == ordered[j].ResourceID {
			return ordered[i].RouteID < ordered[j].RouteID
		}
		return ordered[i].ResourceID < ordered[j].ResourceID
	})
	for _, target := range ordered {
		row, err := sqlcgen.New(tx).ProxyGetRoute(ctx, target.RouteID)
		if err != nil {
			return hostingError(err)
		}
		if !row.Enabled || !p.Enabled {
			return ErrConflict
		}
		actual, err := proxyHealthyTarget(ctx, tx, p, row)
		if err != nil {
			return err
		}
		if actual != target {
			return ErrConflict
		}
	}
	return nil
}

// PrepareProxy is the real River handler. It dispatches only immutable operation
// identity and compiles the predecessor from the last committed generation.
func (s *PostgresStore) PrepareProxy(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	op, err := proxyOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	if op.State != "queued" {
		return nil
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return err
	}
	p, err := proxyLock(ctx, tx, op.ProxyID)
	if err != nil {
		return err
	}
	op, err = proxyOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	if op.State != "queued" || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.RestoreOwnershipHeld {
		return nil
	}
	eligible, err := q.ProxyRunnerEligible(ctx, p.RunnerID)
	if err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	if err = proxyValidateSnapshot(ctx, tx, p, op.Snapshot); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: id, State: "refused"}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return err
	}
	plan, err := domain.CompileHostingProxyPlan(op.Snapshot, p.LoadedGeneration, p.DaemonID)
	if err != nil {
		return ErrConflict
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	runID := "proxy_" + id
	spec, err := json.Marshal(domain.RunSpec{Type: domain.RunTypeProxyApply, Inputs: map[string]any{}, ProxyOperationID: id, ProxyMode: "apply"})
	if err != nil {
		return err
	}
	owner, err := q.ProxyGet(ctx, p.ID)
	if err != nil {
		return err
	}
	if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: runID, ProjectID: p.ControlProjectID, RunSpec: spec, RequestedBy: owner.CreatedBy}); err != nil {
		return err
	}
	if _, err = q.ProxyCreateTaskTarget(ctx, sqlcgen.ProxyCreateTaskTargetParams{RunID: runID, OperationID: id, ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID, RunnerID: p.RunnerID}); err != nil {
		return err
	}
	changed, err := q.ProxySetActive(ctx, sqlcgen.ProxySetActiveParams{ID: p.ID, ActiveOperationID: proxyPointer(id)})
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	changed, err = q.ProxyDispatch(ctx, sqlcgen.ProxyDispatchParams{ID: id, Plan: body, PlanHash: proxyPointer(plan.Hash), RunID: proxyPointer(runID)})
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	audit := domain.AuditEvent{ID: "aud_proxy_dispatch_" + id, ActorID: op.CreatedBy, Action: "hosting.proxy_dispatched", TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"proxy_id": p.ID, "generation": op.Generation, "runner_id": p.RunnerID, "run_id": runID}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) recoverProxyDispatch(ctx context.Context, tx pgx.Tx) error {
	q := sqlcgen.New(tx)
	ids, err := q.ProxyQueuedRecovery(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueProxyTx(ctx, tx, hostingjobs.PrepareProxyArgs{OperationID: id}); err != nil {
			return err
		}
		if _, err = q.ProxyTouchOperation(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func proxyAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) error {
	q := sqlcgen.New(tx)
	if _, err := q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
		return hostingError(err)
	}
	_, err := q.ProxyCheckAuthority(ctx, sqlcgen.ProxyCheckAuthorityParams{ID: a.LeaseID, RunID: a.RunID, RunnerID: runner, Attempt: int32(a.Attempt), Fence: a.Fence, ID_2: a.OperationID, Live: live})
	return hostingError(err)
}
func proxyAttemptLock(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority) (domain.HostingProxyOperation, domain.HostingProxy, error) {
	if err := proxyAuthority(ctx, tx, runner, a, true); err != nil {
		return domain.HostingProxyOperation{}, domain.HostingProxy{}, err
	}
	op, err := proxyOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, domain.HostingProxy{}, err
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return op, domain.HostingProxy{}, err
	}
	p, err := proxyLock(ctx, tx, op.ProxyID)
	if err != nil {
		return op, p, err
	}
	op, err = proxyOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, p, err
	}
	if p.RestoreOwnershipHeld {
		return op, p, ErrHostingRestoreHold
	}
	if p.ActiveOperationID != op.ID || op.RunID != a.RunID {
		return op, p, ErrConflict
	}
	// Destination and proxy admission can wait beyond lease expiry. The task
	// and lease locks are already ours; read current authority after the wait.
	if err = proxyAuthority(ctx, tx, runner, a, true); err != nil {
		return op, p, err
	}
	return op, p, nil
}

// RunnerProxyPlan verifies live exact-target authority before returning typed input.
func (s *PostgresStore) RunnerProxyPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	op, _, err := proxyAttemptLock(ctx, tx, runner, a)
	if err != nil {
		return op, err
	}
	if (op.State != "dispatched" && op.State != "prepared") || op.Plan == nil || op.MutationHold {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	return op, tx.Commit(ctx)
}

func proxyReceiptReplay(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, phase, id string, body any) (bool, error) {
	if err := proxyAuthority(ctx, tx, runner, a, false); err != nil {
		return false, err
	}
	r, err := sqlcgen.New(tx).ProxyReceipt(ctx, sqlcgen.ProxyReceiptParams{OperationID: a.OperationID, Phase: phase})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var authority domain.HostingAuthority
	if err = json.Unmarshal(r.Authority, &authority); err != nil {
		return false, err
	}
	if r.ReceiptID != id || r.BodyHash != domain.HostingDocumentHash(body) || authority != a {
		return false, ErrConflict
	}
	return true, nil
}
func proxyInsertReceipt(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, id string, body any) error {
	if id == "" || len(id) > 128 {
		return ErrConflict
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(raw) > 48<<10 {
		return ErrConflict
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(tx).ProxyInsertReceipt(ctx, sqlcgen.ProxyInsertReceiptParams{OperationID: a.OperationID, Phase: phase, ReceiptID: id, Authority: authority, Body: raw, BodyHash: domain.HostingDocumentHash(body)})
	return err
}

// RunnerProxyPrepared pins exact local daemon and static engine identities before effects.
func (s *PostgresStore) RunnerProxyPrepared(ctx context.Context, runner string, in domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if replay, err := proxyReceiptReplay(ctx, tx, runner, in.HostingAuthority, "prepared", in.ReceiptID, in.HostingProxyPreparation); err != nil {
		return domain.HostingProxyOperation{}, err
	} else if replay {
		return proxyOperation(ctx, tx, in.OperationID)
	}
	op, p, err := proxyAttemptLock(ctx, tx, runner, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	if op.State != "dispatched" || op.Plan == nil || in.PlanHash != op.Plan.Hash || !proxyDaemonIdentity(in.DaemonID) || !proxyDigest(in.EngineHash) || !proxyDigest(in.StaticHash) {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	preparedProxy := p
	preparedProxy.DaemonID = in.DaemonID
	if err = proxyValidateSnapshot(ctx, tx, preparedProxy, op.Snapshot); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	// Snapshot validation acquires resource locks after the proxy lock.
	if err = proxyAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	q := sqlcgen.New(tx)
	changed, err := q.ProxyPinEngine(ctx, sqlcgen.ProxyPinEngineParams{ID: p.ID, DaemonID: proxyPointer(in.DaemonID), StaticHash: proxyPointer(in.StaticHash)})
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if changed != 1 {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if err = proxyInsertReceipt(ctx, tx, in.HostingAuthority, "prepared", in.ReceiptID, in.HostingProxyPreparation); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: op.ID, State: "prepared"}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err = proxyOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// RunnerProxyApplyBegin commits independent durable ownership before any engine effect.
func (s *PostgresStore) RunnerProxyApplyBegin(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	op, p, err := proxyAttemptLock(ctx, tx, runner, a)
	if err != nil {
		return op, err
	}
	if op.State == "applying" && op.MutationHold {
		return op, tx.Commit(ctx)
	}
	if op.State != "prepared" || op.Plan == nil || op.Preparation == nil || p.HoldOperationID != "" || p.DesiredGeneration != op.Generation || op.Plan.PreviousGeneration != p.LoadedGeneration || op.Preparation.DaemonID != p.DaemonID || op.Preparation.StaticHash != p.StaticHash {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if err = proxyValidateSnapshot(ctx, tx, p, op.Snapshot); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyAuthority(ctx, tx, runner, a, true); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	q := sqlcgen.New(tx)
	changed, err := q.ProxyHold(ctx, sqlcgen.ProxyHoldParams{ID: p.ID, HoldOperationID: proxyPointer(op.ID)})
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if changed != 1 {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if _, err = q.ProxyAdmit(ctx, sqlcgen.ProxyAdmitParams{OperationID: op.ID, RunID: a.RunID, LeaseID: a.LeaseID, Authority: authority, PlanHash: op.Preparation.EngineHash}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: op.ID, State: "applying"}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	audit := domain.AuditEvent{ID: "aud_proxy_admit_" + op.ID, ActorID: op.CreatedBy, Action: "hosting.proxy_apply_admitted", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"proxy_id": p.ID, "generation": op.Generation, "runner_id": runner}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err = proxyOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// RunnerProxyStatus reads only the exact persisted attempt, including committed replay.
func (s *PostgresStore) RunnerProxyStatus(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthority(ctx, tx, runner, a, false); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return proxyOperation(ctx, tx, a.OperationID)
}

func proxyValidateResult(op domain.HostingProxyOperation, in domain.HostingProxyResult) error {
	if op.Plan == nil || in.PlanHash != op.Plan.Hash || len(in.Routes) > domain.HostingProxyMaxRoutes || in.LoadedGeneration < 0 || in.LoadedGeneration > op.Generation || in.DaemonID != "" && !proxyDaemonIdentity(in.DaemonID) || in.EngineHash != "" && !proxyDigest(in.EngineHash) || in.ContainerID != "" && !proxyDigest(in.ContainerID) {
		return ErrConflict
	}
	if !op.MutationHold {
		if in.Outcome != "refused" || in.LoadedGeneration != op.Plan.PreviousGeneration || (op.State != "dispatched" && op.State != "prepared") {
			return ErrConflict
		}
	} else if op.Preparation == nil || in.EngineHash != op.Preparation.EngineHash || in.DaemonID != op.Preparation.DaemonID {
		return ErrConflict
	}
	switch in.Outcome {
	case "applied":
		if !op.MutationHold || in.LoadedGeneration != op.Generation || len(in.Routes) != len(op.Snapshot.Routes) {
			return ErrConflict
		}
	case "rolled_back", "refused":
		if in.LoadedGeneration != op.Plan.PreviousGeneration || len(in.Routes) != 0 {
			return ErrConflict
		}
	case "uncertain":
		if !op.MutationHold {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	targets := map[string]domain.HostingProxyRouteTarget{}
	for _, route := range op.Snapshot.Routes {
		targets[route.RouteID] = route
	}
	seen := map[string]bool{}
	for _, observation := range in.Routes {
		route, ok := targets[observation.RouteID]
		certificate := observation.Certificate
		if !ok || seen[observation.RouteID] || certificate.Hostname != route.Hostname || len(certificate.Issuer) > 1024 || (certificate.Status != "pending" && certificate.Status != "trusted") || certificate.Fingerprint != "" && !proxyDigest(certificate.Fingerprint) || observation.HTTPSReady && (!observation.HTTPReady || certificate.Status != "trusted" || !proxyDigest(certificate.Fingerprint) || !certificate.ExpiresAt.After(time.Now())) {
			return ErrConflict
		}
		seen[observation.RouteID] = true
	}
	return nil
}

func proxyDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func proxyDaemonIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 128 && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

// RunnerProxyComplete settles live external evidence atomically. Uncertainty
// completes only the attempt, never the independent proxy ownership hold.
func (s *PostgresStore) RunnerProxyComplete(ctx context.Context, runner string, in domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if replay, err := proxyReceiptReplay(ctx, tx, runner, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return domain.HostingProxyOperation{}, err
	} else if replay {
		return proxyOperation(ctx, tx, in.OperationID)
	}
	op, p, err := proxyAttemptLock(ctx, tx, runner, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	if op.MutationHold && op.State != "applying" {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if err = proxyValidateResult(op, in.Result); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyInsertReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	q := sqlcgen.New(tx)
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: op.ID, State: in.Result.Outcome}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	status := "failed"
	if in.Result.Outcome == "applied" {
		status = "succeeded"
	}
	if _, err = q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: proxyPointer(in.ReceiptID)}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: in.RunID, Status: status}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if in.Result.Outcome != "uncertain" {
		if op.MutationHold {
			changed, err := q.ProxySettle(ctx, sqlcgen.ProxySettleParams{ID: p.ID, ActiveOperationID: proxyPointer(op.ID), LoadedGeneration: in.Result.LoadedGeneration})
			if err != nil {
				return domain.HostingProxyOperation{}, err
			}
			if changed != 1 {
				return domain.HostingProxyOperation{}, ErrConflict
			}
		} else {
			if _, err = q.ProxySettleUnadmitted(ctx, sqlcgen.ProxySettleUnadmittedParams{ID: p.ID, ActiveOperationID: proxyPointer(op.ID)}); err != nil {
				return domain.HostingProxyOperation{}, err
			}
		}
	}
	audit := domain.AuditEvent{ID: "aud_proxy_complete_" + op.ID, ActorID: op.CreatedBy, Action: "hosting.proxy_completed", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"proxy_id": p.ID, "generation": op.Generation, "outcome": in.Result.Outcome, "runner_id": runner}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err = proxyOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// CancelHostingProxyOperation requires current server administration and control
// project ownership; project task controls never authorize whole-proxy cancellation.
func (s *PostgresStore) CancelHostingProxyOperation(ctx context.Context, id, actor string, audit domain.AuditEvent) (domain.HostingProxyOperation, error) {
	before, err := proxyOperation(ctx, s.pool, id)
	if err != nil {
		return before, err
	}
	p, err := proxyLoad(ctx, s.pool, before.ProxyID)
	if err != nil {
		return before, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return before, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	q := sqlcgen.New(tx)
	if before.RunID != "" {
		if _, err = q.LockHostingTaskForAuthority(ctx, before.RunID); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	if err = q.LockHostingExecutionDestination(ctx, p.DestinationID); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	p, err = proxyLock(ctx, tx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err := proxyOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if op.State == "canceled" || op.State == "cancel_requested" {
		return op, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if op.State != "queued" && op.State != "dispatched" && op.State != "prepared" && op.State != "applying" {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	state := "canceled"
	if op.MutationHold {
		state = "cancel_requested"
	}
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: id, State: state}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if !op.MutationHold {
		if _, err = q.ProxySettleUnadmitted(ctx, sqlcgen.ProxySettleUnadmittedParams{ID: p.ID, ActiveOperationID: proxyPointer(id)}); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	if op.RunID != "" {
		if _, err = q.CancelHostingLeases(ctx, op.RunID); err != nil {
			return domain.HostingProxyOperation{}, err
		}
		if _, err = q.CancelHostingTask(ctx, op.RunID); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	audit.ActorID = actor
	audit.Action = "hosting.proxy_cancel_requested"
	audit.TargetID = id
	audit.Metadata = map[string]any{"proxy_id": p.ID, "generation": op.Generation, "mutation_hold": op.MutationHold}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err = proxyOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	return op, proxyCommitMutation(ctx, tx, actor)
}

var _ HostingProxyRepository = (*PostgresStore)(nil)

func proxyString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func proxyPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func proxyRoleRank(role string) int {
	switch role {
	case domain.RoleOwner:
		return 3
	case domain.RoleMaintainer:
		return 2
	case domain.RoleViewer:
		return 1
	}
	return 0
}

// Actor and project authority precede destination admission. Membership removal
// and identity revocation therefore serialize with the authorizing transaction.
func proxyAuthorize(ctx context.Context, tx pgx.Tx, actor, project, minimum string, admin bool) error {
	q := sqlcgen.New(tx)
	user, err := q.ProxyLockUser(ctx, actor)
	if err == nil {
		if admin && user.GlobalRole != domain.RoleSystemAdmin && user.GlobalRole != domain.RoleRunnerAdmin {
			return auth.ErrForbidden
		}
		if _, err = q.ProxyLockProject(ctx, project); err != nil {
			return hostingError(err)
		}
		if user.GlobalRole == domain.RoleSystemAdmin {
			return nil
		}
		role, err := q.ProxyLockMember(ctx, sqlcgen.ProxyLockMemberParams{ProjectID: project, UserID: actor})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && proxyRoleRank(role) < proxyRoleRank(minimum) {
			return auth.ErrForbidden
		}
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Legacy tokens authenticate as their own durable identity and retain only
	// their stored roles. Never inherit the issuer's project memberships, and
	// never treat a managed service-account credential as a legacy token.
	roles, tokenErr := q.ProxyLockLegacyToken(ctx, actor)
	if tokenErr == nil {
		if !slices.Contains(roles, domain.RoleSystemAdmin) {
			return auth.ErrForbidden
		}
		if _, err = q.ProxyLockProject(ctx, project); err != nil {
			return hostingError(err)
		}
		return proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(tokenErr, pgx.ErrNoRows) {
		return tokenErr
	}
	if admin {
		return auth.ErrForbidden
	}
	account, err := q.ProxyLockServiceAccount(ctx, actor)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (account.ProjectID != project || proxyRoleRank(account.Role) < proxyRoleRank(minimum)) {
		return auth.ErrForbidden
	}
	if err != nil {
		return err
	}
	_, err = q.ProxyLockProject(ctx, project)
	return hostingError(err)
}

// Token locks serialize revocation, but wall-clock expiry can pass while a
// destination, proxy, resource, queue, or audit lock is awaited. Check the database
// clock after all such work, immediately before publishing a commit or replay.
func proxyCheckLegacyToken(ctx context.Context, tx pgx.Tx, actor string) error {
	valid, err := sqlcgen.New(tx).ProxyLegacyTokenValid(ctx, actor)
	if err != nil {
		return err
	}
	if !valid {
		return auth.ErrForbidden
	}
	return nil
}

func proxyCommitMutation(ctx context.Context, tx pgx.Tx, actor string) error {
	if err := proxyCheckLegacyToken(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func proxyLoad(ctx context.Context, db sqlcgen.DBTX, id string) (domain.HostingProxy, error) {
	r, err := sqlcgen.New(db).ProxyGet(ctx, id)
	if err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	p := domain.HostingProxy{ID: r.ID, ServerID: r.ServerID, DestinationID: r.DestinationID, RunnerID: r.RunnerID, ControlProjectID: r.ControlProjectID, Enabled: r.Enabled, DesiredGeneration: r.DesiredGeneration, LoadedGeneration: r.LoadedGeneration, DaemonID: proxyString(r.DaemonID), StaticHash: proxyString(r.StaticHash), ActiveOperationID: proxyString(r.ActiveOperationID), HoldOperationID: proxyString(r.HoldOperationID), RestoreOwnershipHeld: r.RestoreHeld}
	err = json.Unmarshal(r.Configuration, &p.Configuration)
	return p, err
}
func proxyLock(ctx context.Context, tx pgx.Tx, id string) (domain.HostingProxy, error) {
	if _, err := sqlcgen.New(tx).ProxyLock(ctx, id); err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	return proxyLoad(ctx, tx, id)
}
func proxyOperation(ctx context.Context, db sqlcgen.DBTX, id string) (domain.HostingProxyOperation, error) {
	r, err := sqlcgen.New(db).ProxyOperation(ctx, id)
	if err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	op := domain.HostingProxyOperation{ID: r.ID, ProxyID: r.ProxyID, ProjectID: proxyString(r.ProjectID), RouteID: proxyString(r.RouteID), Generation: r.Generation, State: r.State, RunID: proxyString(r.RunID), CreatedBy: r.CreatedBy, MutationHold: r.MutationHold, RestoreOwnershipHeld: r.RestoreHeld}
	for _, document := range []struct {
		raw    []byte
		target any
	}{{r.Snapshot, &op.Snapshot}, {r.Plan, &op.Plan}, {r.PreparedReceipt, &op.PreparedReceipt}, {r.Prepared, &op.Preparation}, {r.CompleteReceipt, &op.CompleteReceipt}, {r.Result, &op.Result}} {
		if len(document.raw) > 0 {
			if err = json.Unmarshal(document.raw, document.target); err != nil {
				return domain.HostingProxyOperation{}, err
			}
		}
	}
	return op, nil
}
func proxyGrant(row sqlcgen.HostingProxyHostnameGrant) domain.HostingHostnameGrant {
	return domain.HostingHostnameGrant{ID: row.ID, ProxyID: row.ProxyID, ProjectID: row.ProjectID, Hostname: row.Hostname, Wildcard: row.Wildcard, RevokedAt: row.RevokedAt}
}
func proxyRoute(row sqlcgen.HostingProxyRoute) HostingProxyRouteRecord {
	return HostingProxyRouteRecord{Route: domain.HostingRoute{ID: row.ID, ResourceID: row.ResourceID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, Version: row.Version, SelectedReleaseOperationID: row.SelectedReleaseOperationID, Enabled: row.Enabled, State: "desired", Certificate: domain.HostingProxyCertificate{Hostname: row.Hostname, Status: "pending"}}, ProxyID: row.ProxyID, ProjectID: row.ProjectID, BindingID: row.BindingID, GrantID: row.GrantID}
}
func proxyRouteStatus(ctx context.Context, db sqlcgen.DBTX, row sqlcgen.HostingProxyRoute) (HostingProxyRouteRecord, error) {
	record := proxyRoute(row)
	id, err := sqlcgen.New(db).ProxyRouteOperation(ctx, proxyPointer(row.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	op, err := proxyOperation(ctx, db, id)
	if err != nil {
		return record, err
	}
	record.Route.OperationID = id
	record.Route.State = op.State
	if op.MutationHold {
		record.Route.State = "uncertain"
	}
	if op.RestoreOwnershipHeld {
		record.Route.State = "restore_ownership_held"
	}
	if op.Result != nil && op.Result.Outcome == "applied" && op.Result.LoadedGeneration == op.Generation && row.Enabled {
		matches := false
		for _, target := range op.Snapshot.Routes {
			if target.RouteID == row.ID && target.ReleaseOperationID == row.SelectedReleaseOperationID && target.Hostname == row.Hostname && target.PathPrefix == row.PathPrefix {
				matches = true
			}
		}
		if matches {
			for _, observation := range op.Result.Routes {
				if observation.RouteID == row.ID {
					record.Route.HTTPReady = observation.HTTPReady
					record.Route.Certificate = observation.Certificate
					record.Route.HTTPSReady = observation.HTTPSReady && observation.Certificate.Status == "trusted" && observation.Certificate.ExpiresAt.After(time.Now())
				}
			}
		}
	}
	return record, nil
}

// GetHostingProxy returns administrator-scoped proxy state for application authorization.
func (s *PostgresStore) GetHostingProxy(ctx context.Context, id string) (domain.HostingProxy, error) {
	return proxyLoad(ctx, s.pool, id)
}

// GetHostingProxyByServer returns the managed proxy attached to an immutable server.
func (s *PostgresStore) GetHostingProxyByServer(ctx context.Context, server string) (domain.HostingProxy, error) {
	row, err := sqlcgen.New(s.pool).ProxyGetByServer(ctx, server)
	if err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	return proxyLoad(ctx, s.pool, row.ID)
}

// GetHostingProxyOperation returns internal evidence; application callers must sanitize project views.
func (s *PostgresStore) GetHostingProxyOperation(ctx context.Context, id string) (domain.HostingProxyOperation, error) {
	return proxyOperation(ctx, s.pool, id)
}

// GetHostingRoute returns one route and private authorization scope.
func (s *PostgresStore) GetHostingRoute(ctx context.Context, id string) (HostingProxyRouteRecord, error) {
	row, err := sqlcgen.New(s.pool).ProxyGetRoute(ctx, id)
	if err != nil {
		return HostingProxyRouteRecord{}, hostingError(err)
	}
	return proxyRouteStatus(ctx, s.pool, row)
}

// ListHostingRoutes returns only routes selected by the persisted resource identity.
func (s *PostgresStore) ListHostingRoutes(ctx context.Context, resource string) ([]domain.HostingRoute, error) {
	rows, err := sqlcgen.New(s.pool).ProxyListResourceRoutes(ctx, resource)
	if err != nil {
		return nil, err
	}
	result := make([]domain.HostingRoute, 0, len(rows))
	for _, row := range rows {
		record, err := proxyRouteStatus(ctx, s.pool, row)
		if err != nil {
			return nil, err
		}
		result = append(result, record.Route)
	}
	return result, nil
}

// ListHostingHostnameGrants returns administrator-visible hostname delegations.
func (s *PostgresStore) ListHostingHostnameGrants(ctx context.Context, id string) ([]domain.HostingHostnameGrant, error) {
	rows, err := sqlcgen.New(s.pool).ProxyListGrants(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]domain.HostingHostnameGrant, 0, len(rows))
	for _, row := range rows {
		out = append(out, proxyGrant(row))
	}
	return out, nil
}

func proxyRequestKey(scope, key string) (string, error) {
	if key == "" || len(key) > 128 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return "", ErrConflict
	}
	return domain.HostingDocumentHash(struct{ Scope, Key string }{scope, key}), nil
}
func proxyReplay(ctx context.Context, tx pgx.Tx, id, key, hash string) (string, error) {
	row, err := sqlcgen.New(tx).ProxyIdempotency(ctx, sqlcgen.ProxyIdempotencyParams{ProxyID: id, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.RequestHash != hash {
		return "", ErrConflict
	}
	return row.ID, nil
}

// ConfigureHostingProxy creates or re-enables one immutable server proxy and
// queues its initial empty generation through the same transaction as desired state.
func (s *PostgresStore) ConfigureHostingProxy(ctx context.Context, id, server, actor string, in domain.HostingProxyInput, audit domain.AuditEvent) (domain.HostingProxy, error) {
	if s.hostingJobs == nil {
		return domain.HostingProxy{}, hostingjobs.ErrUnavailable
	}
	if domain.ValidateHostingProxyConfiguration(in.Configuration) != nil {
		return domain.HostingProxy{}, ErrConflict
	}
	key, err := proxyRequestKey("configure", in.IdempotencyKey)
	if err != nil {
		return domain.HostingProxy{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxy{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, in.ControlProjectID, domain.RoleOwner, true); err != nil {
		return domain.HostingProxy{}, err
	}
	q := sqlcgen.New(tx)
	placement, err := q.ProxyPlacement(ctx, server)
	if err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	if err = q.LockHostingExecutionDestination(ctx, placement.DestinationID); err != nil {
		return domain.HostingProxy{}, err
	}
	if existing, lookupErr := q.ProxyGetByServer(ctx, server); lookupErr == nil {
		id = existing.ID
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return domain.HostingProxy{}, lookupErr
	} else {
		eligible, err := q.ProxyRunnerEligible(ctx, placement.RunnerID)
		if err != nil {
			return domain.HostingProxy{}, err
		}
		if !eligible {
			return domain.HostingProxy{}, ErrConflict
		}
		available, err := q.ProxyManagementPortAvailable(ctx, sqlcgen.ProxyManagementPortAvailableParams{DestinationID: placement.DestinationID, HostPort: int32(in.Configuration.ManagementPort)})
		if err != nil {
			return domain.HostingProxy{}, err
		}
		if available == nil || !*available {
			return domain.HostingProxy{}, ErrConflict
		}
		configuration, err := json.Marshal(in.Configuration)
		if err != nil {
			return domain.HostingProxy{}, err
		}
		if _, err = q.ProxyCreate(ctx, sqlcgen.ProxyCreateParams{ID: id, ServerID: server, DestinationID: placement.DestinationID, RunnerID: placement.RunnerID, ControlProjectID: in.ControlProjectID, Configuration: configuration, CreatedBy: actor}); err != nil {
			return domain.HostingProxy{}, hostingError(err)
		}
	}
	p, err := proxyLock(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if p.ControlProjectID != in.ControlProjectID || !reflect.DeepEqual(p.Configuration, in.Configuration) {
		return p, ErrConflict
	}
	hash := domain.HostingDocumentHash(in)
	if replay, err := proxyReplay(ctx, tx, p.ID, key, hash); err != nil {
		return p, err
	} else if replay != "" {
		return p, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if _, err = q.ProxySetEnabled(ctx, sqlcgen.ProxySetEnabledParams{ID: p.ID, Enabled: true}); err != nil {
		return p, err
	}
	p.Enabled = true
	if _, err = s.queueProxyGeneration(ctx, tx, p, "pop_"+strings.TrimPrefix(domain.HostingDocumentHash(audit.ID), "sha256:")[:24], in.ControlProjectID, "", actor, key, hash, audit); err != nil {
		return p, err
	}
	p, err = proxyLoad(ctx, tx, p.ID)
	if err != nil {
		return p, err
	}
	return p, proxyCommitMutation(ctx, tx, actor)
}

// SetHostingProxyEnabled withdraws all routes on disable and queues a whole generation.
func (s *PostgresStore) SetHostingProxyEnabled(ctx context.Context, id, actor, key string, enabled bool, audit domain.AuditEvent) (domain.HostingProxy, error) {
	if s.hostingJobs == nil {
		return domain.HostingProxy{}, hostingjobs.ErrUnavailable
	}
	p, err := proxyLoad(ctx, s.pool, id)
	if err != nil {
		return p, err
	}
	requestKey, err := proxyRequestKey("enabled", key)
	if err != nil {
		return p, err
	}
	hash := domain.HostingDocumentHash(struct {
		Enabled bool
		Key     string
	}{enabled, key})
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true); err != nil {
		return p, err
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, p.DestinationID); err != nil {
		return p, err
	}
	p, err = proxyLock(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if replay, err := proxyReplay(ctx, tx, id, requestKey, hash); err != nil {
		return p, err
	} else if replay != "" {
		return p, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !enabled {
		if _, err = q.ProxyWithdrawAllRoutes(ctx, id); err != nil {
			return p, err
		}
	}
	if _, err = q.ProxySetEnabled(ctx, sqlcgen.ProxySetEnabledParams{ID: id, Enabled: enabled}); err != nil {
		return p, err
	}
	p.Enabled = enabled
	if _, err = s.queueProxyGeneration(ctx, tx, p, "pop_"+strings.TrimPrefix(domain.HostingDocumentHash(audit.ID), "sha256:")[:24], p.ControlProjectID, "", actor, requestKey, hash, audit); err != nil {
		return p, err
	}
	p, err = proxyLoad(ctx, tx, id)
	if err != nil {
		return p, err
	}
	return p, proxyCommitMutation(ctx, tx, actor)
}

// GrantHostingHostname serializes overlap checks with all proxy grant and route mutations.
func (s *PostgresStore) GrantHostingHostname(ctx context.Context, id, grantID, actor string, in domain.HostingHostnameGrantInput, audit domain.AuditEvent) (domain.HostingHostnameGrant, error) {
	p, err := proxyLoad(ctx, s.pool, id)
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true); err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	q := sqlcgen.New(tx)
	if _, err = q.ProxyLockProject(ctx, in.ProjectID); err != nil {
		return domain.HostingHostnameGrant{}, hostingError(err)
	}
	if err = q.LockHostingExecutionDestination(ctx, p.DestinationID); err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	if _, err = proxyLock(ctx, tx, id); err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	host, err := domain.NormalizeHostingHostname(in.Hostname)
	if err != nil || host != in.Hostname {
		return domain.HostingHostnameGrant{}, ErrConflict
	}
	grants, err := q.ProxyListGrants(ctx, id)
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	for _, grant := range grants {
		if grant.RevokedAt == nil && grant.ProjectID != in.ProjectID && domain.HostingHostnameGrantsOverlap(grant.Hostname, grant.Wildcard, in.Hostname, in.Wildcard) {
			return domain.HostingHostnameGrant{}, ErrConflict
		}
	}
	row, err := q.ProxyCreateGrant(ctx, sqlcgen.ProxyCreateGrantParams{ID: grantID, ProxyID: id, ProjectID: in.ProjectID, Hostname: in.Hostname, Wildcard: in.Wildcard, GrantedBy: actor})
	if err != nil {
		return domain.HostingHostnameGrant{}, hostingError(err)
	}
	audit.ActorID = actor
	audit.Action = "hosting.proxy_hostname_granted"
	audit.TargetID = row.ID
	audit.Metadata = map[string]any{"proxy_id": id, "project_id": in.ProjectID, "hostname": in.Hostname, "wildcard": in.Wildcard}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	return proxyGrant(row), proxyCommitMutation(ctx, tx, actor)
}

// RevokeHostingHostname atomically withdraws affected desired routes and queues replacement.
func (s *PostgresStore) RevokeHostingHostname(ctx context.Context, id, grantID, actor, key string, audit domain.AuditEvent) (domain.HostingProxy, error) {
	if s.hostingJobs == nil {
		return domain.HostingProxy{}, hostingjobs.ErrUnavailable
	}
	p, err := proxyLoad(ctx, s.pool, id)
	if err != nil {
		return p, err
	}
	requestKey, err := proxyRequestKey("revoke:"+grantID, key)
	if err != nil {
		return p, err
	}
	hash := domain.HostingDocumentHash(struct{ Grant, Key string }{grantID, key})
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true); err != nil {
		return p, err
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, p.DestinationID); err != nil {
		return p, err
	}
	p, err = proxyLock(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if replay, err := proxyReplay(ctx, tx, id, requestKey, hash); err != nil {
		return p, err
	} else if replay != "" {
		return p, proxyCheckLegacyToken(ctx, tx, actor)
	}
	grant, err := q.ProxyGetGrant(ctx, grantID)
	if err != nil {
		return p, hostingError(err)
	}
	if grant.ProxyID != id {
		return p, ErrConflict
	}
	if _, err = q.ProxyRevokeGrant(ctx, sqlcgen.ProxyRevokeGrantParams{ID: grantID, ProxyID: id}); err != nil {
		return p, err
	}
	if _, err = q.ProxyWithdrawGrantRoutes(ctx, grantID); err != nil {
		return p, err
	}
	if _, err = s.queueProxyGeneration(ctx, tx, p, "pop_"+strings.TrimPrefix(domain.HostingDocumentHash(audit.ID), "sha256:")[:24], p.ControlProjectID, "", actor, requestKey, hash, audit); err != nil {
		return p, err
	}
	p, err = proxyLoad(ctx, tx, id)
	if err != nil {
		return p, err
	}
	return p, proxyCommitMutation(ctx, tx, actor)
}

func proxyHealthyTarget(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, row sqlcgen.HostingProxyRoute) (domain.HostingProxyRouteTarget, error) {
	q := sqlcgen.New(tx)
	if _, err := q.ProxyLockResource(ctx, row.ResourceID); err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	placement, err := q.ProxyResourceTarget(ctx, row.ResourceID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if placement.ProjectID != row.ProjectID || placement.BindingID != row.BindingID || placement.DestinationID != p.DestinationID || placement.ServerID != p.ServerID || placement.RunnerID != p.RunnerID {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	grant, err := q.ProxyGetGrant(ctx, row.GrantID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if grant.ProxyID != p.ID || grant.ProjectID != row.ProjectID || grant.RevokedAt != nil || !domain.HostingHostnameGranted(grant.Hostname, grant.Wildcard, row.Hostname) {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	release, err := q.ProxyReleaseTarget(ctx, row.SelectedReleaseOperationID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	var healthy domain.HostingHealthy
	if err = json.Unmarshal(release.Body, &healthy); err != nil {
		return domain.HostingProxyRouteTarget{}, err
	}
	if (release.State != "healthy" && release.State != "retained") || !release.RetainedRunning || healthy.OperationID != row.SelectedReleaseOperationID || healthy.Snapshot.ResourceID != row.ResourceID || healthy.Snapshot.BindingID != row.BindingID || healthy.Snapshot.DestinationID != p.DestinationID || healthy.Snapshot.ServerID != p.ServerID || healthy.Snapshot.RunnerID != p.RunnerID || healthy.Snapshot.HostPort < 1024 || healthy.Snapshot.HostPort > 65535 || healthy.Snapshot.HostPort == int(p.Configuration.ManagementPort) || domain.ValidateHostingSnapshotRelease(healthy.Snapshot, healthy.Release) != nil || p.DaemonID != "" && p.DaemonID != healthy.Release.DaemonID {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	return domain.HostingProxyRouteTarget{RouteID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, ReleaseOperationID: row.SelectedReleaseOperationID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, HostPort: uint16(healthy.Snapshot.HostPort)}, nil
}

func (s *PostgresStore) queueProxyGeneration(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, operationID, project, routeID, actor, key, requestHash string, audit domain.AuditEvent) (domain.HostingProxyOperation, error) {
	q := sqlcgen.New(tx)
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	routes := make([]domain.HostingProxyRouteTarget, 0)
	if p.Enabled {
		for _, row := range rows {
			if !row.Enabled {
				continue
			}
			target, err := proxyHealthyTarget(ctx, tx, p, row)
			if err != nil {
				return domain.HostingProxyOperation{}, err
			}
			routes = append(routes, target)
		}
	}
	if len(routes) > domain.HostingProxyMaxRoutes {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	generation, err := q.ProxyNextGeneration(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	snapshot := domain.HostingProxySnapshot{ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID, RunnerID: p.RunnerID, Generation: generation, Configuration: p.Configuration, Routes: routes}
	if _, err = domain.CompileHostingProxyPlan(snapshot, p.LoadedGeneration, p.DaemonID); err != nil {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if len(raw) > 32<<10 {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if _, err = q.ProxySupersedeUnadmitted(ctx, p.ID); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = q.ProxyClearUnadmittedActive(ctx, p.ID); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = q.ProxyCreateOperation(ctx, sqlcgen.ProxyCreateOperationParams{ID: operationID, ProxyID: p.ID, ProjectID: proxyPointer(project), RouteID: proxyPointer(routeID), Generation: generation, IdempotencyKey: key, RequestHash: requestHash, Snapshot: raw, SnapshotHash: domain.HostingDocumentHash(snapshot), CreatedBy: actor}); err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	for _, route := range routes {
		if _, err = q.ProxyReferenceRelease(ctx, sqlcgen.ProxyReferenceReleaseParams{OperationID: operationID, RouteID: route.RouteID, ReleaseOperationID: route.ReleaseOperationID}); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	if _, err = s.hostingJobs.EnqueueProxyTx(ctx, tx, hostingjobs.PrepareProxyArgs{OperationID: operationID}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	audit.ActorID = actor
	audit.Action = "hosting.proxy_generation_requested"
	audit.TargetID = operationID
	audit.Metadata = map[string]any{"proxy_id": p.ID, "generation": generation, "route_id": routeID, "project_id": project}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return proxyOperation(ctx, tx, operationID)
}

// RequestHostingRoute selects only the current settled healthy release of an
// authorized resource. The complete new proxy generation retains other projects.
func (s *PostgresStore) RequestHostingRoute(ctx context.Context, resourceID, routeID, operationID, actor string, in domain.HostingRouteInput, audit domain.AuditEvent) (domain.HostingRoute, error) {
	if s.hostingJobs == nil {
		return domain.HostingRoute{}, hostingjobs.ErrUnavailable
	}
	placement, err := sqlcgen.New(s.pool).ProxyResourceTarget(ctx, resourceID)
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	if in.ExpectedRouteVersion == 0 {
		routeID = "route_" + strings.TrimPrefix(domain.HostingDocumentHash(struct{ Resource, Key string }{resourceID, in.IdempotencyKey}), "sha256:")[:24]
	}
	key, err := proxyRequestKey("route:"+resourceID+":"+routeID, in.IdempotencyKey)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	requestHash := domain.HostingDocumentHash(in)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, placement.ProjectID, domain.RoleMaintainer, false); err != nil {
		return domain.HostingRoute{}, err
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, placement.DestinationID); err != nil {
		return domain.HostingRoute{}, err
	}
	proxyRow, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	p, err := proxyLock(ctx, tx, proxyRow.ID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if replay, err := proxyReplay(ctx, tx, p.ID, key, requestHash); err != nil {
		return domain.HostingRoute{}, err
	} else if replay != "" {
		record, err := s.hostingRouteTx(ctx, tx, routeID)
		record.Route.OperationID = replay
		if err != nil {
			return domain.HostingRoute{}, err
		}
		return record.Route, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !p.Enabled || p.RestoreOwnershipHeld || in.ExpectedRouteVersion < 0 || in.ExpectedHealthyOperationID == "" {
		return domain.HostingRoute{}, ErrConflict
	}
	if _, err = q.ProxyLockResource(ctx, resourceID); err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	// A previously admitted fixed-port image replacement must settle before a
	// new route can select that port. Future reuse is blocked by these references.
	busy, err := q.HostingBindingBusy(ctx, placement.BindingID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if busy {
		return domain.HostingRoute{}, ErrConflict
	}
	grant, err := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: p.DestinationID, ProjectID: placement.ProjectID})
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	if grant.RevokedAt != nil {
		return domain.HostingRoute{}, ErrConflict
	}
	state, err := q.LockHostingPortReservation(ctx, placement.BindingID)
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	if !state.RestoreAdmitted || state.HoldOperationID != nil {
		return domain.HostingRoute{}, ErrHostingRestoreHold
	}
	if held, err := q.HostingBindingRestoreHeld(ctx, placement.BindingID); err != nil {
		return domain.HostingRoute{}, err
	} else if held {
		return domain.HostingRoute{}, ErrHostingRestoreHold
	}
	healthyRaw, err := q.GetHostingHealthy(ctx, placement.BindingID)
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	var healthy domain.HostingHealthy
	if err = json.Unmarshal(healthyRaw, &healthy); err != nil {
		return domain.HostingRoute{}, ErrConflict
	}
	if healthy.OperationID != in.ExpectedHealthyOperationID {
		return domain.HostingRoute{}, ErrConflict
	}
	host := in.Hostname
	if in.GenerateHostname {
		if in.Hostname != "" || p.Configuration.GeneratedDomain == "" {
			return domain.HostingRoute{}, ErrConflict
		}
		host = "r-" + strings.TrimPrefix(domain.HostingDocumentHash(resourceID), "sha256:")[:16] + "." + p.Configuration.GeneratedDomain
	}
	host, err = domain.NormalizeHostingHostname(host)
	if err != nil || domain.ValidateHostingRoutePath(in.PathPrefix) != nil {
		return domain.HostingRoute{}, ErrConflict
	}
	grants, err := q.ProxyListGrants(ctx, p.ID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	grantID := ""
	for _, candidate := range grants {
		if candidate.ProjectID == placement.ProjectID && candidate.RevokedAt == nil && domain.HostingHostnameGranted(candidate.Hostname, candidate.Wildcard, host) {
			if !in.GenerateHostname || candidate.Wildcard && candidate.Hostname == p.Configuration.GeneratedDomain {
				grantID = candidate.ID
				break
			}
		}
	}
	if grantID == "" {
		return domain.HostingRoute{}, auth.ErrForbidden
	}
	if in.ExpectedRouteVersion == 0 {
		if _, err = q.ProxyCreateRoute(ctx, sqlcgen.ProxyCreateRouteParams{ID: routeID, ProxyID: p.ID, ProjectID: placement.ProjectID, ResourceID: resourceID, BindingID: placement.BindingID, GrantID: grantID, Hostname: host, PathPrefix: in.PathPrefix, SelectedReleaseOperationID: healthy.OperationID, CreatedBy: actor}); err != nil {
			return domain.HostingRoute{}, hostingError(err)
		}
	} else {
		existing, err := q.ProxyGetRoute(ctx, routeID)
		if err != nil {
			return domain.HostingRoute{}, hostingError(err)
		}
		if existing.ResourceID != resourceID || existing.ProjectID != placement.ProjectID || existing.ProxyID != p.ID {
			return domain.HostingRoute{}, ErrConflict
		}
		changed, err := q.ProxyUpdateRoute(ctx, sqlcgen.ProxyUpdateRouteParams{ID: routeID, GrantID: grantID, Hostname: host, PathPrefix: in.PathPrefix, SelectedReleaseOperationID: healthy.OperationID, Version: in.ExpectedRouteVersion})
		if err != nil {
			return domain.HostingRoute{}, hostingError(err)
		}
		if changed != 1 {
			return domain.HostingRoute{}, ErrConflict
		}
	}
	op, err := s.queueProxyGeneration(ctx, tx, p, operationID, placement.ProjectID, routeID, actor, key, requestHash, audit)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	record, err := s.hostingRouteTx(ctx, tx, routeID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	record.Route.OperationID = op.ID
	return record.Route, proxyCommitMutation(ctx, tx, actor)
}

func (s *PostgresStore) hostingRouteTx(ctx context.Context, tx pgx.Tx, id string) (HostingProxyRouteRecord, error) {
	row, err := sqlcgen.New(tx).ProxyGetRoute(ctx, id)
	if err != nil {
		return HostingProxyRouteRecord{}, hostingError(err)
	}
	return proxyRouteStatus(ctx, tx, row)
}

// DeleteHostingRoute publishes a whole-generation withdrawal without deleting evidence.
func (s *PostgresStore) DeleteHostingRoute(ctx context.Context, id, operationID, actor string, in domain.HostingRouteDeletionInput, audit domain.AuditEvent) (domain.HostingRoute, error) {
	if s.hostingJobs == nil {
		return domain.HostingRoute{}, hostingjobs.ErrUnavailable
	}
	row, err := sqlcgen.New(s.pool).ProxyGetRoute(ctx, id)
	if err != nil {
		return domain.HostingRoute{}, hostingError(err)
	}
	p, err := proxyLoad(ctx, s.pool, row.ProxyID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	key, err := proxyRequestKey("delete:"+id, in.IdempotencyKey)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	hash := domain.HostingDocumentHash(in)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyAuthorize(ctx, tx, actor, row.ProjectID, domain.RoleMaintainer, false); err != nil {
		return domain.HostingRoute{}, err
	}
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, p.DestinationID); err != nil {
		return domain.HostingRoute{}, err
	}
	p, err = proxyLock(ctx, tx, p.ID)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if replay, err := proxyReplay(ctx, tx, p.ID, key, hash); err != nil {
		return domain.HostingRoute{}, err
	} else if replay != "" {
		record, err := s.hostingRouteTx(ctx, tx, id)
		record.Route.OperationID = replay
		if err != nil {
			return domain.HostingRoute{}, err
		}
		return record.Route, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if in.ExpectedRouteVersion < 1 {
		return domain.HostingRoute{}, ErrConflict
	}
	changed, err := q.ProxyDeleteRoute(ctx, sqlcgen.ProxyDeleteRouteParams{ID: id, Version: in.ExpectedRouteVersion})
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if changed != 1 {
		return domain.HostingRoute{}, ErrConflict
	}
	op, err := s.queueProxyGeneration(ctx, tx, p, operationID, row.ProjectID, id, actor, key, hash, audit)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	record, err := s.hostingRouteTx(ctx, tx, id)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	record.Route.OperationID = op.ID
	return record.Route, proxyCommitMutation(ctx, tx, actor)
}
