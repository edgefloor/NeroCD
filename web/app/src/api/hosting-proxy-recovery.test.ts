import { afterEach, expect, test, vi } from "vitest";
import { applyRecovery, planRecovery, readRecovery, recoveryQuery, validateRecoveryPlan, validateRecoveryResolution, validateRecoveryStatus, type RecoveryReview, type RecoveryScope } from "./hosting-proxy-recovery";
const scope: RecoveryScope = { actor: '["local","user"]', actorID: "user", server: "server", project: "project", proxy: "proxy", operation: "pop_original" };
const hash = "sha256:" + "a".repeat(64);
const target = { route_id: "route", project_id: "project", resource_id: "resource", binding_id: "binding", release_operation_id: "hop_a", hostname: "web.example.test", path_prefix: "/", host_port: 20101 };
const review: RecoveryReview = { version: 1, operation_id: scope.operation, proxy_id: scope.proxy, actor_id: scope.actorID, loaded_generation: 3, desired_generation: 5, prior_operation_id: "pop_prior", prior_snapshot_hash: hash, complete_receipt_id: "receipt", complete_receipt_hash: hash, engine_outcome: "rolled_back", facts_hash: hash, routes: [{ route_id: "route", version: 4, selected_release_operation_id: "hop_b", enabled: false, authorized: false, prior_target: target }], convergence_routes: [] };
const plan = { id: "prp_one", review, plan_hash: hash, created_at: "2026-10-23T12:00:00Z", expires_at: "2026-10-23T12:00:30Z" };
const resolution = { id: "prr_one", operation_id: scope.operation, plan_id: plan.id, plan_hash: hash, actor_id: scope.actorID, engine_outcome: "rolled_back", complete_receipt_id: "receipt", complete_receipt_hash: hash, convergence_operation_id: "pop_converge", routes: [{ route_id: "route", before_version: 4, after_version: 5, before_release_operation_id: "hop_b", after_release_operation_id: "hop_a", enabled: false }], created_at: "2026-10-23T12:00:01Z" };
const status = { operation_id: scope.operation, eligible: true, reason: "completed_metadata_conflict", review };
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
test("scoped no-store requests preserve exact bodies and CSRF for both review and apply", async () => {
  const fetch = vi.fn(async (url: string, _init: RequestInit) => new Response(JSON.stringify(url.endsWith("/recovery") ? status : url.endsWith("/apply") ? resolution : url.endsWith("/recovery-plans") ? plan : { id: scope.operation, proxy_id: scope.proxy, snapshot: { proxy_id: scope.proxy, server_id: scope.server } }))); vi.stubGlobal("fetch", fetch);
  const input = { idempotency_key: "plan-key", expected_loaded_generation: 3, expected_desired_generation: 5, expected_complete_receipt_id: "receipt", expected_complete_receipt_hash: hash }, apply = { idempotency_key: "apply-key", plan_hash: hash };
  await readRecovery(scope); await planRecovery(scope, input, review); await applyRecovery(scope, plan, apply);
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/proxy-operations/pop_original", "/api/v1/proxy-operations/pop_original/recovery", "/api/v1/proxy-operations/pop_original/recovery-plans", "/api/v1/proxy-operations/pop_original/recovery-plans/prp_one/apply"]);
  expect(fetch.mock.calls[2]![1]).toMatchObject({ method: "POST", cache: "no-store", body: JSON.stringify(input), headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }) }); expect(fetch.mock.calls[3]![1].body).toBe(JSON.stringify(apply));
  expect(recoveryQuery(scope)).toMatchObject({ queryKey: ["proxyRecovery", scope.actor, "project", "proxy", "pop_original"], retry: false, gcTime: 0 });
});
test.each([{ ...status, operation_id: "foreign" }, { ...status, eligible: false }, { ...status, review: { ...review, proxy_id: "other" } }, { ...status, review: { ...review, actor_id: "other" } }, { ...status, review: { ...review, engine_outcome: "uncertain" } }, { ...status, review: { ...review, complete_receipt_hash: "wrong" } }, { ...status, review: { ...review, routes: [...review.routes, ...review.routes] } }, { ...status, review: { ...review, convergence_routes: Array.from({ length: 33 }, () => target) } }, { ...status, credentials: "never-cache" }])("malformed or mismatched review fails closed %j", value => expect(() => validateRecoveryStatus(value, scope)).toThrow());
test("unsupported and immutable resolved reads are distinct; historical resolution may belong to another authorized actor", () => {
  expect(() => validateRecoveryStatus({ operation_id: scope.operation, eligible: false, reason: "unsupported_or_unfinished" }, scope)).not.toThrow();
  expect(() => validateRecoveryStatus({ operation_id: scope.operation, eligible: false, reason: "resolved", resolution: { ...resolution, actor_id: "another-admin" } }, scope)).not.toThrow();
  expect(() => validateRecoveryStatus({ operation_id: scope.operation, eligible: true, reason: "resolved", resolution }, scope)).toThrow();
});
test.each([{ ...plan, expires_at: "2026-10-23T12:01:00Z" }, { ...plan, review: { ...review, desired_generation: 6 } }, { ...plan, plan_hash: "invalid" }])("plan binds original review and fixed lifetime %j", value => expect(() => validateRecoveryPlan(value, scope, review)).toThrow());
test("past plan and applied replay remain parseable without local clock authority", () => {
  expect(() => validateRecoveryPlan(plan, scope, review)).not.toThrow(); expect(() => validateRecoveryResolution(resolution, scope, plan)).not.toThrow();
  expect(() => validateRecoveryResolution({ ...resolution, plan_id: "different" }, scope, plan)).toThrow(); expect(() => validateRecoveryResolution({ ...resolution, routes: [{ ...resolution.routes[0], enabled: true }] }, scope, plan)).toThrow();
});
test("five-second timeout aborts ignored transport and late success cannot revive it", async () => {
  vi.useFakeTimers(); let resolve!: (response: Response) => void; const fetch = vi.fn((_url: string, _init: RequestInit) => new Promise<Response>(done => { resolve = done; })); vi.stubGlobal("fetch", fetch);
  const result = readRecovery(scope); const rejection = expect(result).rejects.toThrow("timed out"); await vi.advanceTimersByTimeAsync(5000); await rejection; expect(fetch.mock.calls[0]![1].signal?.aborted).toBe(true); resolve(new Response(JSON.stringify(status))); await Promise.resolve();
});
test("external cancellation fences a pending mutation", async () => {
  vi.stubGlobal("fetch", () => new Promise<Response>(() => {})); const controller = new AbortController(); const result = planRecovery(scope, { idempotency_key: "key", expected_loaded_generation: 3, expected_desired_generation: 5, expected_complete_receipt_id: "receipt", expected_complete_receipt_hash: hash }, review, controller.signal); controller.abort(); await expect(result).rejects.toThrow("retired");
});
test("resolved DTO cannot cross proxy scope through an exact operation locator", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify({ id: scope.operation, proxy_id: "foreign", snapshot: { proxy_id: "foreign", server_id: scope.server } }))); vi.stubGlobal("fetch", fetch);
  await expect(readRecovery(scope)).rejects.toThrow("another scope"); expect(fetch).toHaveBeenCalledTimes(1);
});
