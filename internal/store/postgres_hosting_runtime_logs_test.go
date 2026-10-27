package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type runtimeLogFixture struct {
	pg           *store.PostgresStore
	db           *pgxpool.Pool
	resource     domain.Resource
	server       domain.Server
	destination  domain.Destination
	hash, bearer string
}

func newRuntimeLogFixture(t *testing.T) runtimeLogFixture {
	t.Helper()
	var stop func(context.Context) error
	pg, db, resource, server, destination := hostingExecutionFixture(t, &stop)
	bearer := "image-hosting-fixture-token"
	f := runtimeLogFixture{pg: pg, db: db, resource: resource, server: server, destination: destination, hash: fmt.Sprintf("%x", sha256.Sum256([]byte(bearer))), bearer: bearer}
	f.exec(t, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingRuntimeLogsCapability)
	op, err := pg.RequestHostingDeployment(t.Context(), "hop_runtime_log_target", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("runtime_log_deploy"))
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	admitted, err := pg.HostingApplyBegin(t.Context(), server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(t.Context(), server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "runtime_log_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "runtime_log_container", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	if err = stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f runtimeLogFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func runtimeLogOpenInput(n int) domain.HostingRuntimeLogOpen {
	return domain.HostingRuntimeLogOpen{RequestID: fmt.Sprintf("%032x", n), RequestExpiresAt: time.Now().UTC().Add(25 * time.Second)}
}
func (f runtimeLogFixture) open(t *testing.T, n int) domain.HostingRuntimeLogSession {
	t.Helper()
	v, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, runtimeLogOpenInput(n))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f runtimeLogFixture) claim(t *testing.T, instance string) domain.HostingRuntimeLogClaim {
	t.Helper()
	v, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: instance, AfterSessionID: "", Limit: 4})
	if err != nil || len(v.Targets) != 1 {
		t.Fatalf("claim %+v %v", v, err)
	}
	return v.Targets[0]
}
func runtimeLogBatch(c domain.HostingRuntimeLogClaim, n, expected int, message string) domain.HostingRuntimeLogBatch {
	return domain.HostingRuntimeLogBatch{SessionID: c.SessionID, InstanceID: c.InstanceID, CollectionEpoch: c.CollectionEpoch, BatchID: fmt.Sprintf("%032x", n), BatchSequence: expected + 1, ExpectedSequence: expected, State: "following", Events: []domain.HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: message}}}
}
func (f runtimeLogFixture) append(t *testing.T, b domain.HostingRuntimeLogBatch) domain.HostingRuntimeLogAck {
	t.Helper()
	v, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, b)
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL and JSON receipts can represent the same instant with different
	// time.Location pointers. Compare their stable UTC receipt values.
	v.ReceivedAt = v.ReceivedAt.UTC()
	return v
}
func (f runtimeLogFixture) page(t *testing.T, id string) domain.HostingRuntimeLogPage {
	t.Helper()
	v, err := f.pg.RuntimeLogPage(t.Context(), "usr_bootstrap", id, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f runtimeLogFixture) anotherResource(t *testing.T, n int) runtimeLogFixture {
	t.Helper()
	resource := createDestinationResource(t, f.pg, fmt.Sprintf("runtime_log_resource_%d", n), f.resource.ProjectID)
	_, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: fmt.Sprintf("binding_runtime_log_%d", n), ResourceID: resource.ID, DestinationID: f.destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now()}, destinationEvent(fmt.Sprintf("log_bind_%d", n)))
	if err != nil {
		t.Fatal(err)
	}
	in := hostingDeploymentRequest()
	in.IdempotencyKey = fmt.Sprintf("runtime_log_%d", n)
	in.HostPort += n
	op, err := f.pg.RequestHostingDeployment(t.Context(), fmt.Sprintf("hop_runtime_log_%d", n), resource.ID, "usr_bootstrap", in, destinationEvent(fmt.Sprintf("log_deploy_%d", n)))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	a, prepared := prepareHostingAttempt(t, f.pg, op, f.server.RunnerID)
	admitted, err := f.pg.HostingApplyBegin(t.Context(), f.server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingComplete(t.Context(), f.server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: fmt.Sprintf("log_result_%d", n), Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: fmt.Sprintf("runtime_container_%d", n), ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	f.resource = resource
	return f
}

func (f runtimeLogFixture) viewer(t *testing.T, id string) {
	t.Helper()
	f.exec(t, `INSERT INTO users(id,email,name,status,global_role,password_hash) VALUES($1,$1||'@example.invalid',$1,'active','user','unusable-test-fixture') ON CONFLICT(id) DO NOTHING`, id)
	if _, err := f.pg.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "runtime_log_member_" + id, ProjectID: f.resource.ProjectID, UserID: id, Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingRuntimeLogDemandEpochReceiptAndRedaction(t *testing.T) {
	f := newRuntimeLogFixture(t)
	input := runtimeLogOpenInput(1)
	session, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, input)
	if err != nil || retry.ID != session.ID || !retry.DemandExpiresAt.Equal(session.DemandExpiresAt) {
		t.Fatal(retry, err)
	}
	changed := input
	changed.RequestExpiresAt = changed.RequestExpiresAt.Add(time.Millisecond)
	if _, err = f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed open", err)
	}
	instance := strings.Repeat("a", 32)
	claim := f.claim(t, instance)
	renewed := f.claim(t, instance)
	if renewed.CollectionEpoch != claim.CollectionEpoch || !renewed.DemandExpiresAt.Equal(session.DemandExpiresAt) || renewed.EpochExpiresAt.Before(claim.EpochExpiresAt) {
		t.Fatal(claim, renewed)
	}
	other, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: strings.Repeat("b", 32), AfterSessionID: "", Limit: 4})
	if err != nil || len(other.Targets) != 0 {
		t.Fatal("competing instance", other, err)
	}
	body := runtimeLogBatch(claim, 1, 0, "hello "+f.bearer+" app-secret-can-remain")
	ack := f.append(t, body)
	if again := f.append(t, body); again != ack {
		t.Fatal("retry receipt changed", again, ack)
	}
	page := f.page(t, session.ID)
	if !page.Sensitive || len(page.Items) != 1 || strings.Contains(page.Items[0].Message, f.bearer) || !strings.Contains(page.Items[0].Message, "[REDACTED]") || !strings.Contains(page.Items[0].Message, "app-secret-can-remain") {
		t.Fatal(page)
	}
	body.Events[0].Message = "changed"
	if _, err = f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, body); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed retry", err)
	}
	body.Events[0].Message = "hello " + f.bearer + " app-secret-can-remain"
	f.append(t, runtimeLogBatch(claim, 2, 1, "next"))
	if _, err = f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, body); !errors.Is(err, store.ErrConflict) {
		t.Fatal("evicted retry", err)
	}
	if page = f.page(t, session.ID); !page.Gap || len(page.Items) != 2 || len(page.Markers) == 0 || page.Markers[0] != "source_gap" {
		t.Fatal("gap not committed", page)
	}
	var receipts int
	var audits int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE receipt IS NOT NULL) FROM hosting_runtime_log_sessions`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal(receipts, err)
	}
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%app-secret-can-remain%'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatal(audits, err)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET epoch_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, session.ID)
	newClaim := f.claim(t, strings.Repeat("b", 32))
	if newClaim.CollectionEpoch == claim.CollectionEpoch {
		t.Fatal("expired epoch reused")
	}
	if _, err = f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 3, 2, "stale instance")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale publication", err)
	}
}

func TestPostgresHostingRuntimeLogExpiryClosePurgeAndRequestDeadline(t *testing.T) {
	f := newRuntimeLogFixture(t)
	session := f.open(t, 1)
	claim := f.claim(t, strings.Repeat("a", 32))
	f.append(t, runtimeLogBatch(claim, 1, 0, "retained"))
	closed, err := f.pg.CloseRuntimeLogSession(t.Context(), "usr_bootstrap", session.ID)
	if err != nil || closed.State != "ended" {
		t.Fatal(closed, err)
	}
	if _, err = f.pg.RenewRuntimeLogSession(t.Context(), "usr_bootstrap", session.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("closed renew", err)
	}
	if _, err = f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 2, 1, "late")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("closed append", err)
	}
	if page := f.page(t, session.ID); len(page.Items) != 1 || page.Session.State != "ended" {
		t.Fatal(page)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET purge_after=clock_timestamp() WHERE id=$1`, session.ID)
	if _, err = f.pg.RuntimeLogPage(t.Context(), "usr_bootstrap", session.ID, 0, 200); !errors.Is(err, store.ErrRuntimeLogsExpired) {
		t.Fatal("logical expiry", err)
	}
	if count, e := f.pg.PurgeRuntimeLogs(t.Context()); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	var events int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_runtime_log_events`).Scan(&events); err != nil || events != 0 {
		t.Fatal(events, err)
	}
	input := runtimeLogOpenInput(1)
	input.RequestExpiresAt = time.Now().Add(-time.Second)
	if _, err = f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, input); !errors.Is(err, store.ErrRuntimeLogsExpired) {
		t.Fatal("purged request resurrected", err)
	}
	session = f.open(t, 2)
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET demand_expires_at=clock_timestamp()-interval '1 second',purge_after=clock_timestamp()+interval '298 seconds' WHERE id=$1`, session.ID)
	if _, err = f.pg.RenewRuntimeLogSession(t.Context(), "usr_bootstrap", session.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("expired demand renew", err)
	}
	if v := f.page(t, session.ID); v.Session.State != "ended" {
		t.Fatal(v)
	}
}

func TestPostgresHostingRuntimeLogTargetReplacementRetainsOriginalOutput(t *testing.T) {
	f := newRuntimeLogFixture(t)
	session := f.open(t, 1)
	claim := f.claim(t, strings.Repeat("a", 32))
	f.append(t, runtimeLogBatch(claim, 1, 0, "old container output"))
	var before []byte
	if err := f.db.QueryRow(t.Context(), `SELECT to_jsonb(bs) FROM hosting_binding_state bs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{container_id}','"new_container"')`)
	if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 2, 1, "must reject")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	page := f.page(t, session.ID)
	if page.Session.State != "target_changed" || page.Session.Target.ContainerID != session.Target.ContainerID || len(page.Items) != 1 {
		t.Fatal(page)
	}
	if _, err := f.pg.RenewRuntimeLogSession(t.Context(), "usr_bootstrap", session.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	var state struct {
		Healthy domain.HostingHealthy `json:"healthy"`
	}
	if err := json.Unmarshal(before, &state); err != nil {
		t.Fatal(err)
	}
	var hold *string
	if err := f.db.QueryRow(t.Context(), `SELECT hold_operation_id FROM hosting_binding_state`).Scan(&hold); err != nil || hold != nil {
		t.Fatal("observation changed mutation hold", err)
	}
}

func TestPostgresHostingRuntimeLogCurrentActorAndOriginalAuthority(t *testing.T) {
	for _, mutation := range []struct{ name, sql string }{
		{"grant", `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`},
		{"capability", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_runtime_logs_v1')`},
		{"credential", `UPDATE runners SET token_hash=repeat('a',64)`},
		{"enrollment", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE used_at IS NOT NULL`},
		{"archive", `UPDATE projects SET archived_at=clock_timestamp() WHERE id='proj_platform'`},
		{"restore", `UPDATE hosting_binding_state SET restore_admitted=false`},
		{"actor", `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newRuntimeLogFixture(t)
			session := f.open(t, 1)
			claim := f.claim(t, strings.Repeat("a", 32))
			f.append(t, runtimeLogBatch(claim, 1, 0, "private"))
			f.exec(t, mutation.sql)
			if _, err := f.pg.RuntimeLogPage(t.Context(), "usr_bootstrap", session.ID, 0, 200); err == nil {
				t.Fatal("revoked read accepted")
			}
			if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 2, 1, "revoked")); err == nil {
				t.Fatal("revoked append accepted")
			}
		})
	}
	f := newRuntimeLogFixture(t)
	session := f.open(t, 1)
	if _, err := f.pg.RuntimeLogSessionScope(t.Context(), "usr_viewer", session.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("scope disclosed", err)
	}
	if _, err := f.pg.RuntimeLogPage(t.Context(), "usr_viewer", session.ID, 0, 200); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("other actor disclosed", err)
	}
	if _, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_viewer", f.resource.ID, runtimeLogOpenInput(2)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("crossproject opened", err)
	}
}

func TestPostgresHostingRuntimeLogQuotaSerializationAndBoundedJanitor(t *testing.T) {
	f := newRuntimeLogFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 1; i <= 8; i++ {
		wg.Go(func() {
			_, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, runtimeLogOpenInput(i))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("active resource quota", accepted)
	}
	for i := 2; i <= 8; i++ {
		f.exec(t, `UPDATE hosting_runtime_log_sessions SET demand_expires_at=clock_timestamp()-interval '1 second',purge_after=clock_timestamp()+interval '298 seconds' WHERE ended_at IS NULL`)
		f.open(t, i+20)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET demand_expires_at=clock_timestamp()-interval '1 second',purge_after=clock_timestamp()+interval '298 seconds' WHERE ended_at IS NULL`)
	if _, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, runtimeLogOpenInput(99)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("retained actor quota", err)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET purge_after=clock_timestamp()-interval '1 second'`)
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { n, e := f.pg.PurgeRuntimeLogs(t.Context()); counts <- n; errs <- e })
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		if n > 8 {
			t.Fatal("unbounded purge", n)
		}
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 8 {
		t.Fatal("janitor race", total)
	}
}

func TestPostgresHostingRuntimeLogPrefixEvictionAndPageBound(t *testing.T) {
	f := newRuntimeLogFixture(t)
	session := f.open(t, 1)
	claim := f.claim(t, strings.Repeat("a", 32))
	next := 0
	for i := 1; i <= 18; i++ {
		body := runtimeLogBatch(claim, i, next, "")
		body.BatchSequence = i
		body.Events = []domain.HostingRuntimeLogEvent{}
		for range 64 {
			body.Events = append(body.Events, domain.HostingRuntimeLogEvent{Component: "application", Stream: "combined", Message: strings.Repeat("x", 1500)})
		}
		ack := f.append(t, body)
		next = ack.LastSequence
	}
	page := f.page(t, session.ID)
	raw, err := json.Marshal(page)
	if err != nil || len(raw) > domain.HostingRuntimeLogByteLimit || len(page.Items) > 200 || page.DeletedThroughSequence == 0 || !page.Gap || page.Items[0].Sequence != page.DeletedThroughSequence+1 {
		t.Fatal(len(raw), page.NextSequence, page.DeletedThroughSequence, err)
	}
	var rows, bytes int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*),sum(octet_length(message)) FROM hosting_runtime_log_events WHERE session_id=$1`, session.ID).Scan(&rows, &bytes); err != nil || rows > 1000 || bytes > 1<<20 {
		t.Fatal(rows, bytes, err)
	}
}

func TestPostgresHostingRuntimeLogProjectAndCollectorQuotas(t *testing.T) {
	f := newRuntimeLogFixture(t)
	second := f.anotherResource(t, 1)
	third := f.anotherResource(t, 2)
	firstSession := f.open(t, 1)
	secondSession := second.open(t, 2)
	if _, err := third.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", third.resource.ID, runtimeLogOpenInput(3)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("active actor quota", err)
	}
	if _, err := f.pg.CloseRuntimeLogSession(t.Context(), "usr_bootstrap", firstSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.CloseRuntimeLogSession(t.Context(), "usr_bootstrap", secondSession.ID); err != nil {
		t.Fatal(err)
	}
	actors := []string{"usr_bootstrap", "usr_log_viewer_1", "usr_log_viewer_2", "usr_log_viewer_3"}
	for _, actor := range actors[1:] {
		f.viewer(t, actor)
	}
	// Fill exactly32 retained sessions, without exceeding either active quota.
	for actorIndex, actor := range actors {
		start := 0
		if actorIndex == 0 {
			start = 2
		}
		for i := start; i < 8; i++ {
			v, err := f.pg.OpenRuntimeLogSession(t.Context(), actor, f.resource.ID, runtimeLogOpenInput(100+actorIndex*10+i))
			if err != nil {
				t.Fatal(actor, i, err)
			}
			if _, err = f.pg.CloseRuntimeLogSession(t.Context(), actor, v.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.viewer(t, "usr_log_viewer_4")
	if _, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_log_viewer_4", f.resource.ID, runtimeLogOpenInput(999)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("retained project quota", err)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET purge_after=clock_timestamp()-interval '1 second'`)
	locked, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	var lockedID string
	if err = locked.QueryRow(t.Context(), `SELECT id FROM hosting_runtime_log_sessions ORDER BY purge_after,id LIMIT 1 FOR UPDATE`).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	if n, e := f.pg.PurgeRuntimeLogs(t.Context()); e != nil || n != 8 {
		t.Fatal("skip locked purge", n, e)
	}
	var preserved bool
	if err = f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM hosting_runtime_log_sessions WHERE id=$1)`, lockedID).Scan(&preserved); err != nil || !preserved {
		t.Fatal("locked session removed", err)
	}
	if err = locked.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if n, err := f.pg.PurgeRuntimeLogs(t.Context()); err != nil || n != 8 {
			t.Fatal(n, err)
		}
	}
	fourth := f.anotherResource(t, 3)
	fifth := f.anotherResource(t, 4)
	resources := []runtimeLogFixture{f, second, third, fourth, fifth}
	owners := []string{actors[0], actors[0], actors[1], actors[1], actors[2]}
	for i, resource := range resources {
		if _, err := f.pg.OpenRuntimeLogSession(t.Context(), owners[i], resource.resource.ID, runtimeLogOpenInput(300+i)); err != nil {
			t.Fatal(err)
		}
	}
	instance := strings.Repeat("e", 32)
	page, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: instance, AfterSessionID: "", Limit: 4})
	if err != nil || len(page.Targets) != 4 {
		t.Fatal(len(page.Targets), err)
	}
	// A disconnected source retains its live lease slot, including while it
	// transitions back to following without restarting its epoch.
	disconnected := runtimeLogBatch(page.Targets[0], 700, 0, "disconnect")
	disconnected.State = "disconnected"
	f.append(t, disconnected)
	competing, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: strings.Repeat("f", 32), AfterSessionID: page.NextCursor, Limit: 4})
	if err != nil || len(competing.Targets) != 0 {
		t.Fatal("disconnected lease released collector slot", competing, err)
	}
	reused, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: instance, AfterSessionID: "", Limit: 4})
	if err != nil || len(reused.Targets) != 4 || reused.Targets[0].CollectionEpoch != page.Targets[0].CollectionEpoch {
		t.Fatal("same instance lost counted slot", reused, err)
	}
	f.append(t, runtimeLogBatch(page.Targets[0], 701, 1, "following again"))
	remaining, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: instance, AfterSessionID: page.NextCursor, Limit: 4})
	if err != nil || len(remaining.Targets) != 0 {
		t.Fatal("collector quota", remaining, err)
	}
}

func TestPostgresHostingRuntimeLogExpiryAfterBlockingWrites(t *testing.T) {
	for _, mode := range []string{"open", "renew", "claim", "append", "read"} {
		t.Run(mode, func(t *testing.T) {
			f := newRuntimeLogFixture(t)
			var session domain.HostingRuntimeLogSession
			var claim domain.HostingRuntimeLogClaim
			if mode != "open" {
				session = f.open(t, 1)
			}
			if mode == "claim" || mode == "append" {
				claim = f.claim(t, strings.Repeat("a", 32))
			}
			// Set a deadline before installing the delaying trigger so the tested
			// mutation, rather than fixture setup, is the blocking write.
			if mode == "renew" {
				f.exec(t, `UPDATE hosting_runtime_log_sessions SET demand_expires_at=clock_timestamp()+interval '150 milliseconds',purge_after=clock_timestamp()+interval '299 seconds' WHERE id=$1`, session.ID)
			}
			if mode == "claim" || mode == "append" {
				f.exec(t, `UPDATE hosting_runtime_log_sessions SET epoch_expires_at=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, session.ID)
			}
			if mode == "read" {
				f.exec(t, `UPDATE hosting_runtime_log_sessions SET purge_after=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, session.ID)
			}
			f.exec(t, `CREATE FUNCTION runtime_log_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3);RETURN NEW;END;$$`)
			started := time.Now()
			switch mode {
			case "open":
				f.exec(t, `CREATE TRIGGER runtime_log_delay BEFORE INSERT ON hosting_runtime_log_sessions FOR EACH ROW EXECUTE FUNCTION runtime_log_delay()`)
				input := runtimeLogOpenInput(1)
				input.RequestExpiresAt = time.Now().Add(150 * time.Millisecond)
				if _, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, input); !errors.Is(err, store.ErrRuntimeLogsExpired) {
					t.Fatal("late open", err)
				}
			case "renew":
				f.exec(t, `CREATE TRIGGER runtime_log_delay BEFORE UPDATE ON hosting_runtime_log_sessions FOR EACH ROW EXECUTE FUNCTION runtime_log_delay()`)
				if _, err := f.pg.RenewRuntimeLogSession(t.Context(), "usr_bootstrap", session.ID); !errors.Is(err, store.ErrConflict) {
					t.Fatal("late renew", err)
				}
			case "claim":
				f.exec(t, `CREATE TRIGGER runtime_log_delay BEFORE UPDATE ON hosting_runtime_log_sessions FOR EACH ROW EXECUTE FUNCTION runtime_log_delay()`)
				page, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: claim.InstanceID, AfterSessionID: "", Limit: 4})
				if err != nil || len(page.Targets) != 0 {
					t.Fatal("late lease renewal", page, err)
				}
			case "append":
				f.exec(t, `CREATE TRIGGER runtime_log_delay BEFORE INSERT ON hosting_runtime_log_events FOR EACH ROW EXECUTE FUNCTION runtime_log_delay()`)
				if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 1, 0, "late")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("late append", err)
				}
			case "read":
				blocker, err := f.db.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = blocker.Rollback(context.Background()) }()
				if _, err = blocker.Exec(t.Context(), `SELECT id FROM hosting_runtime_log_sessions WHERE id=$1 FOR UPDATE`, session.ID); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { _, e := f.pg.RuntimeLogPage(t.Context(), "usr_bootstrap", session.ID, 0, 200); done <- e }()
				time.Sleep(300 * time.Millisecond)
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if !errors.Is(err, store.ErrRuntimeLogsExpired) {
						t.Fatal("late read", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("read did not join")
				}
			}
			if mode != "read" && time.Since(started) < 250*time.Millisecond {
				t.Fatal("deadline expired before the blocking write was exercised")
			}
		})
	}
}

func TestPostgresHostingRuntimeLogAuthorityAfterBlockingResource(t *testing.T) {
	for _, mode := range []string{"credential", "actor", "grant", "restore", "replacement", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newRuntimeLogFixture(t)
			session := f.open(t, 1)
			claim := f.claim(t, strings.Repeat("a", 32))
			f.append(t, runtimeLogBatch(claim, 1, 0, "accepted before wait"))
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource.ID); err != nil {
				t.Fatal(err)
			}
			var mutation string
			switch mode {
			case "credential":
				mutation = `UPDATE runners SET token_hash=repeat('a',64)`
			case "actor":
				mutation = `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`
			case "grant":
				mutation = `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`
			case "restore":
				mutation = `UPDATE hosting_binding_state SET restore_admitted=false`
			case "replacement":
				mutation = `UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{container_id}','"replacement_after_wait"')`
			case "lease":
				mutation = `UPDATE hosting_runtime_log_sessions SET epoch_expires_at=clock_timestamp()+interval '100 milliseconds'`
			}
			if _, err = blocker.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.pg.AppendRuntimeLogs(ctx, f.server.RunnerID, f.hash, f.bearer, runtimeLogBatch(claim, 2, 1, "must be rejected"))
				done <- e
			}()
			joined := false
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				if !joined {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("publisher did not join")
					}
				}
			}()
			waitObservationBlock(t, ctx, observationFixture{db: f.db}, blocker.Conn().PgConn().PID(), "LockHostingRuntimeResource")
			if mode == "lease" {
				if _, err = f.db.Exec(ctx, `SELECT pg_sleep(0.15)`); err != nil {
					t.Fatal(err)
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
				if err == nil {
					t.Fatal("stale authority accepted", mode)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var rows int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_runtime_log_events WHERE session_id=$1`, session.ID).Scan(&rows); err != nil || rows != 1 {
				t.Fatal("unauthorized output persisted", rows, err)
			}
			if mode == "replacement" {
				if page := f.page(t, session.ID); page.Session.State != "target_changed" || len(page.Items) != 1 {
					t.Fatal(page)
				}
			}
		})
	}
}

func TestPostgresHostingRuntimeLogSchemaGuard(t *testing.T) {
	f := newRuntimeLogFixture(t)
	f.exec(t, `ALTER TABLE hosting_runtime_log_sessions DISABLE TRIGGER hosting_runtime_log_identity`)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || ok {
		t.Fatal(ok, err)
	}
	f.exec(t, `ALTER TABLE hosting_runtime_log_sessions ENABLE TRIGGER hosting_runtime_log_identity;DROP TABLE hosting_runtime_log_events`)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || ok {
		t.Fatal(ok, err)
	}
	if pg, err := store.OpenPostgres(t.Context(), f.db.Config().ConnConfig.ConnString()); err == nil {
		_ = pg.Close()
		t.Fatal("missing log schema accepted")
	}
}

func TestPostgresHostingRuntimeLogEvictedEmptyBatchReplay(t *testing.T) {
	f := newRuntimeLogFixture(t)
	session := f.open(t, 1)
	claim := f.claim(t, strings.Repeat("a", 32))
	if claim.NextBatchSequence != 1 {
		t.Fatal(claim)
	}
	a := runtimeLogBatch(claim, 1, 0, "")
	a.Events = []domain.HostingRuntimeLogEvent{}
	first := f.append(t, a)
	b := a
	b.BatchID = fmt.Sprintf("%032x", 2)
	b.BatchSequence = 2
	second := f.append(t, b)
	if first.BatchSequence != 1 || second.BatchSequence != 2 || first.LastSequence != 0 || second.LastSequence != 0 {
		t.Fatal(first, second)
	}
	if ack := f.append(t, b); ack != second {
		t.Fatal("latest empty retry refreshed ack", ack, second)
	}
	changed := b
	changed.State = "disconnected"
	if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed latest body accepted", err)
	}
	if ack, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, a); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("evicted empty replay accepted: %+v %v", ack, err)
	}
	if page := f.page(t, session.ID); !page.Gap || len(page.Items) != 0 {
		t.Fatal("replay gap was not committed", page)
	}
	skipped := b
	skipped.BatchID = fmt.Sprintf("%032x", 3)
	skipped.BatchSequence = 4
	if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, skipped); !errors.Is(err, store.ErrConflict) {
		t.Fatal("out of order accepted", err)
	}
	if ack := f.append(t, b); ack != second {
		t.Fatal("rejection changed latest receipt", ack)
	}
	nonempty := runtimeLogBatch(claim, 3, 0, "event after empty batches")
	nonempty.BatchSequence = 3
	ack := f.append(t, nonempty)
	if ack.BatchSequence != 3 || ack.FirstSequence != 1 || ack.LastSequence != 1 {
		t.Fatal(ack)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET epoch_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, session.ID)
	next := f.claim(t, strings.Repeat("b", 32))
	if next.CollectionEpoch == claim.CollectionEpoch || next.NextBatchSequence != 4 || next.NextSequence != 2 {
		t.Fatal("epoch reset counter", next)
	}
	old := b
	old.BatchSequence = 4
	old.ExpectedSequence = 1
	if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, old); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old epoch accepted", err)
	}
	current := runtimeLogBatch(next, 4, 1, "")
	current.BatchSequence = 4
	current.Events = []domain.HostingRuntimeLogEvent{}
	f.append(t, current)
	var batchCount, eventCount int
	if err := f.db.QueryRow(t.Context(), `SELECT last_batch_sequence,last_sequence FROM hosting_runtime_log_sessions WHERE id=$1`, session.ID).Scan(&batchCount, &eventCount); err != nil || batchCount != 4 || eventCount != 1 {
		t.Fatal(batchCount, eventCount, err)
	}
	f.exec(t, `UPDATE hosting_runtime_log_sessions SET last_batch_sequence=$2 WHERE id=$1`, session.ID, math.MaxInt32-1)
	boundary := f.claim(t, next.InstanceID)
	if boundary.NextBatchSequence != math.MaxInt32 {
		t.Fatal(boundary)
	}
	last := runtimeLogBatch(boundary, 5, 1, "")
	last.BatchSequence = math.MaxInt32
	last.Events = []domain.HostingRuntimeLogEvent{}
	final := f.append(t, last)
	if repeat := f.append(t, last); repeat != final {
		t.Fatal("exhausted latest retry changed", repeat, final)
	}
	last.BatchID = fmt.Sprintf("%032x", 6)
	if _, err := f.pg.AppendRuntimeLogs(t.Context(), f.server.RunnerID, f.hash, f.bearer, last); !errors.Is(err, store.ErrConflict) {
		t.Fatal("exhausted counter accepted", err)
	}
	claims, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.hash, domain.HostingRuntimeLogClaimInput{InstanceID: next.InstanceID, Limit: 4})
	if err != nil || len(claims.Targets) != 0 {
		t.Fatal("exhausted counter claimed", claims, err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT last_batch_sequence,last_sequence FROM hosting_runtime_log_sessions WHERE id=$1`, session.ID).Scan(&batchCount, &eventCount); err != nil || batchCount != math.MaxInt32 || eventCount != 1 {
		t.Fatal("counter overflow/reset", batchCount, eventCount, err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE hosting_runtime_log_sessions SET last_batch_sequence=0 WHERE id=$1`, session.ID); err == nil {
		t.Fatal("counter regression allowed")
	}
}
