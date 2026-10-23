import { afterEach, expect, test, vi } from "vitest";
import { appendLogRows, closeRuntimeLogs, displayLogText, leaseRemaining, logLease, newLogOpen, openRuntimeLogs, readRuntimeLogs, renewRuntimeLogs, validateLogPage, validateLogSession, type LogPage, type LogRow, type LogSession } from "./hosting-runtime-logs";
const time = "2026-10-23T00:00:00Z", digest = "sha256:" + "a".repeat(64), scope = { actor: "u", project: "p", resource: "r/one" };
const session: LogSession = { id: "rls_" + "1".repeat(32), request_id: "a".repeat(32), resource_id: scope.resource, project_id: "p", actor_id: "u", target: { resource_id: scope.resource, binding_id: "b", destination_id: "d", server_id: "s", runner_id: "a", operation_id: "op", daemon_id: "daemon", container_id: "container", image_id: digest, release_hash: digest, plan_hash: digest, runtime_namespace: "test" }, state: "waiting", created_at: time, demand_expires_at: "2026-10-23T00:00:30Z", ended_at: null, purge_after: "2026-10-23T00:05:30Z", server_time: time, sensitive: true };
const row = (sequence = 1, message = "literal <b>not HTML</b>\u001b[31m"): LogRow => ({ sequence, component: "application", stream: "stdout", message, received_at: time });
const page = (items = [row()]): LogPage => ({ session, items, next_sequence: items.at(-1)?.sequence ?? 0, earliest_available_sequence: items.length ? 1 : 0, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true });
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });
test("new opens freeze a cryptographic32hex identity and canonical UTC deadline under30s", () => {
  const body = newLogOpen(Date.parse(time)); expect(body.request_id).toMatch(/^[0-9a-f]{32}$/); expect(body.request_expires_at).toBe("2026-10-23T00:00:25.000Z"); expect(Object.isFrozen(body)).toBe(true); expect(newLogOpen().request_id).not.toBe(body.request_id);
});
test("all requests are scoped, no-store, abortable; open retries preserve the same exact body", async () => {
  const responses = [session, session, session, session, page()];
  const fetch = vi.fn(async (_url: string, _options: RequestInit) => new Response(JSON.stringify(responses.shift()))); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal, body = { request_id: session.request_id, request_expires_at: "2026-10-23T00:00:25.000Z" };
  await openRuntimeLogs(scope, body, signal); await openRuntimeLogs(scope, body, signal); await renewRuntimeLogs(scope, session, signal); await closeRuntimeLogs(scope, session, signal); await readRuntimeLogs(scope, session, 0, signal);
  expect(fetch.mock.calls[0]).toMatchObject(["/api/v1/resources/r%2Fone/runtime-log-sessions", { method: "POST", signal, cache: "no-store", body: JSON.stringify(body) }]); expect(fetch.mock.calls[1]).toEqual(fetch.mock.calls[0]);
  expect(fetch.mock.calls[2]).toMatchObject([expect.stringContaining("/renew"), { method: "POST", body: "{}" }]); expect(fetch.mock.calls[3]).toMatchObject([expect.any(String), { method: "DELETE" }]); expect(fetch.mock.calls[4]![0]).toContain("/logs?after_sequence=0&limit=200");
});
test.each([{ actor_id: "other" }, { project_id: "other" }, { resource_id: "other" }, { sensitive: false }, { state: "new_state" }, { server_time: "now" }, { server_time: "2026-02-31T00:00:00Z" }, { demand_expires_at: "2026-10-23T00:01:00Z" }, { extra: "plaintext" }])("rejects foreign, unknown or malformed session fields %j", (change) => expect(() => validateLogSession({ ...session, ...change }, scope)).toThrow());
test("response cannot change session, request identity or immutable target", () => {
  for (const change of [{ id: "rls_" + "2".repeat(32) }, { request_id: "b".repeat(32) }, { target: { ...session.target, container_id: "new" } }]) expect(() => validateLogSession({ ...session, ...change }, scope, session)).toThrow();
});
test("empty cursor advances only through a validated deleted prefix", () => {
  expect(() => validateLogPage({ ...page([]), next_sequence: 5 }, scope, session, 0)).toThrow();
  expect(() => validateLogPage({ ...page([]), next_sequence: 5, deleted_through_sequence: 5, gap: true, markers: ["prefix_evicted"] }, scope, session, 0)).not.toThrow();
  expect(() => validateLogPage({ ...page([row(6)]), deleted_through_sequence: 5, earliest_available_sequence: 6, gap: true, markers: ["prefix_evicted", "source_gap"] }, scope, session, 0)).not.toThrow();
});
test.each([
  { items: [row(2)], next_sequence: 2 }, { items: [row(), row()], next_sequence: 1 }, { items: [row(1, "\ud800")] }, { items: [row(1, "x\0")] }, { items: [row(1, "🙂".repeat(4097))] }, { items: [{ ...row(), stream: "terminal" }] }, { next_sequence: 2 }, { markers: ["unknown"] }, { sensitive: false }, { arbitrary: true }, { items: [{ ...row(), source_timestamp: null }] },
])("rejects invalid rows, cursors and closed markers %j", (change) => expect(() => validateLogPage({ ...page(), ...change }, scope, session, 0)).toThrow());
test("accepted UTF8 messages are complete rows and controls are displayed without ANSI interpretation", () => {
  expect(() => validateLogPage(page([row(1, "🙂".repeat(4096))]), scope, session, 0)).not.toThrow(); expect(displayLogText("<script>\u001b[31m\tline\n")).toBe("<script>\\u001b[31m\tline\n");
});
test("local retention drops whole oldest rows to satisfy both row and encoded-byte budgets", () => {
  const count = appendLogRows([], Array.from({ length: 501 }, (_, index) => row(index + 1))); expect(count.rows).toHaveLength(500); expect(count.rows[0]!.sequence).toBe(2); expect(count.dropped).toBe(1);
  const bytes = appendLogRows([], Array.from({ length: 100 }, (_, index) => row(index + 1, "🙂".repeat(4096)))); expect(new TextEncoder().encode(bytes.rows.map((item) => JSON.stringify(item)).join("")).length).toBeLessThanOrEqual(1048576); expect(bytes.dropped).toBeGreaterThan(0); expect(bytes.rows.every((item) => item.message.length === 8192)).toBe(true);
});
test("demand expires conservatively under request delay, wall rollback, suspension and reads", () => {
  vi.spyOn(performance, "now").mockReturnValue(100); vi.spyOn(Date, "now").mockReturnValue(1000);
  const lease = logLease(session, 4000); expect(lease.remaining).toBe(25998); expect(leaseRemaining(lease, 26100, -5000)).toBe(0); expect(leaseRemaining(lease, 100, 27000)).toBe(0); expect(leaseRemaining(lease, 99, 1000)).toBe(0);
  const newerClock = { ...session, server_time: "2026-10-22T23:59:59Z" }; expect(logLease(newerClock, 0, lease).remaining).toBe(lease.remaining);
});
