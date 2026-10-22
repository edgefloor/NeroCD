import { afterEach, expect, test, vi } from "vitest";
import { getHostingRuntimeStatus, runtimeStatusExpiry, runtimeStatusView, validateRuntimeStatus, type HostingRuntimeStatus, type RuntimeStatusSnapshot } from "./hosting-runtime-status";
const time = "2026-10-23T00:00:00Z";
const status: HostingRuntimeStatus = { resource_id: "r/one", binding_id: "binding", destination_id: "d", server_id: "s", runner_id: "a", current_operation_id: "op", current_container_id: "container", observed_operation_id: "op", observed_container_id: "container", observed_at: time, server_time: time, stale_after_seconds: 30, target_interval_seconds: 10, sample_timeout_seconds: 5, freshness: "fresh", reason: "observed", agent_reachability: "reachable", last_heartbeat_at: time, container_state: "running", readiness: "ready", collection_error: "", mutation_hold: false, restore_ownership_held: false, last_observation: { operation_id: "op", container_id: "container", observed_at: time, observation: { binding_id: "binding", sample_id: "sample", sample_duration_ms: 3, container_state: "running", readiness: "ready", collection_error: "" } } };
const snapshot = (value = status, duration = 0): RuntimeStatusSnapshot => ({ status: value, receivedMonotonic: 100, receivedWall: 1000, requestDuration: duration });
afterEach(() => vi.unstubAllGlobals());
test("GET is resource scoped, encoded, abortable, no-store and read-only", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(status))); vi.stubGlobal("fetch", fetch); const signal = new AbortController().signal;
  expect(await getHostingRuntimeStatus("r/one", { signal })).toEqual(status); expect(fetch.mock.calls[0]).toMatchObject(["/api/v1/resources/r%2Fone/runtime-status", { method: "GET", signal, cache: "no-store", credentials: "include" }]);
});
test("legitimate unbound never-observed metadata remains unknown without a transport error", () => {
  const unbound = { ...status, binding_id: "", destination_id: "", server_id: "", runner_id: "", current_operation_id: "", current_container_id: "", observed_operation_id: "", observed_container_id: "", observed_at: null, last_heartbeat_at: null, last_observation: null, freshness: "unknown", reason: "never_observed", agent_reachability: "unknown", container_state: "unknown", readiness: "unknown" } as HostingRuntimeStatus;
  expect(() => validateRuntimeStatus(unbound, "r/one")).not.toThrow(); expect(runtimeStatusView(snapshot(unbound), 100, 1000)).toMatchObject({ fresh: false, reachable: false, container: "unknown" });
});
test.each([{ resource_id: "foreign" }, { server_time: "yesterday" }, { stale_after_seconds: 60 }, { container_state: "paused" }, { reason: "new_unsupported_reason" }, { last_observation: { ...status.last_observation, observation: { ...status.last_observation!.observation, readiness: "ready", container_state: "exited" } } }])("malformed or foreign metadata is rejected conservatively: %j", (change) => expect(() => validateRuntimeStatus({ ...status, ...change }, "r/one")).toThrow());
test("server receipt age and request duration shorten rather than extend local freshness", () => {
  const aged = { ...status, server_time: "2026-10-23T00:00:20Z" };
  expect(runtimeStatusView(snapshot(aged, 4000), 100, 1000).fresh).toBe(true); expect(runtimeStatusExpiry(snapshot(aged, 4000), 100, 1000)).toBe(5998);
  expect(runtimeStatusView(snapshot(aged, 4000), 6100, 7000)).toMatchObject({ fresh: false, reachable: false, container: "unknown", readiness: "unknown" });
  expect(runtimeStatusView(snapshot(aged, 11_000), 100, 1000).fresh).toBe(false);
});
test("monotonic elapsed handles wall rollback and wall elapsed covers suspended monotonic clocks", () => {
  expect(runtimeStatusView(snapshot(), 30_100, -5000).fresh).toBe(false); expect(runtimeStatusView(snapshot(), 100, 31_000).fresh).toBe(false);
  expect(runtimeStatusView(snapshot(), 50, 1000).fresh).toBe(false);
});
test("a changed target never reuses historical evidence as current", () => {
  expect(runtimeStatusView(snapshot({ ...status, current_container_id: "replacement" }), 100, 1000)).toMatchObject({ fresh: false, container: "unknown", reachable: true });
  expect(runtimeStatusView(snapshot({ ...status, observed_at: "2026-10-23T00:00:01Z" }), 100, 1000).fresh).toBe(false);
});
test("heartbeat and observation freshness are independent and errors suppress current claims", () => {
  const value = { ...status, server_time: "2026-10-23T00:00:20Z", last_heartbeat_at: "2026-10-23T00:00:19Z" };
  expect(runtimeStatusView(snapshot(value), 10_100, 11_000)).toMatchObject({ fresh: false, reachable: true, container: "unknown" });
  expect(runtimeStatusView(snapshot(), 100, 1000, true)).toMatchObject({ fresh: false, reachable: false });
  expect(runtimeStatusView(snapshot({ ...status, freshness: "stale" }), 100, 1000).fresh).toBe(false);
});
