import { afterEach, expect, test, vi } from "vitest";
import { getSwitchOptions, getSwitchOperation, requestSwitch, switchOptionsQuery, validateSwitchOptions, validateSwitchOperation, type SwitchInput } from "./hosting-route-switch";
const scope = { actor: "actor", project: "project", resource: "resource" };
const route = { id: "route", hostname: "web.example.test", path_prefix: "/", version: 2, selected_release_operation_id: "hop_a" };
const options = { resource_id: "resource", loaded_generation: 7, available: true, reason: "available", pending_operation_id: undefined, next_after_operation_id: null, routes: [route], targets: [{ operation_id: "hop_b", kind: "current_healthy", resolved_image_identity: "sha256:recorded" }] };
const input: SwitchInput = { idempotency_key: "frozen", expected_loaded_generation: 7, target_release_operation_id: "hop_b", routes: [{ route_id: "route", expected_version: 2, expected_selected_release_operation_id: "hop_a" }] };
const operation = { id: "pop_one", state: "queued", mutation_hold: false, restore_ownership_held: false, routes: [], switch: { expected_loaded_generation: 7, routes: [{ route_id: "route", before_release_operation_id: "hop_a", after_release_operation_id: "hop_b" }] } };
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
test("scoped wrappers preserve exact request and CSRF, forward cancellation and use no-store", async () => {
  const fetch = vi.fn(async (url: string, init: RequestInit) => new Response(JSON.stringify(url.endsWith("/pop_one") || init.method === "POST" ? operation : options))); vi.stubGlobal("fetch", fetch); const controller = new AbortController();
  await getSwitchOptions(scope, "", controller.signal); await getSwitchOperation(scope, "pop_one", controller.signal); await requestSwitch(scope, input, controller.signal);
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/resources/resource/route-switches", "/api/v1/resources/resource/route-switches/pop_one", "/api/v1/resources/resource/route-switches"]);
  expect(fetch.mock.calls[2]![1]).toMatchObject({ cache: "no-store", method: "POST", body: JSON.stringify(input), headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }) });
  expect(switchOptionsQuery(scope).queryKey).toEqual(["routeSwitchOptions", "actor", "project", "resource", ""]);
});
test.each([{ ...options, resource_id: "foreign" }, { ...options, available: false }, { ...options, loaded_generation: 0 }, { ...options, routes: [route, route] }, { ...options, targets: Array.from({ length: 51 }, () => options.targets[0]) }, { ...options, targets: [{ operation_id: "hop_b", kind: "historical_success" }] }, { ...options, snapshot: { secret: "never-cache" } }, { ...options, next_after_operation_id: "hop_b" }])("malformed or out-of-scope choices fail closed %j", (value) => expect(() => validateSwitchOptions(value, "resource")).toThrow());
test("pagination accepts an empty continuation without inventing no targets and rejects regressing cursors", async () => {
  const page = { ...options, targets: [] }; expect(() => validateSwitchOptions(page, "resource", "hop_b")).not.toThrow(); expect(() => validateSwitchOptions(options, "resource", "hop_b")).toThrow();
  const fetch = vi.fn(async () => new Response(JSON.stringify(page))); vi.stubGlobal("fetch", fetch); await getSwitchOptions(scope, "hop_b"); expect(fetch.mock.calls[0]).toEqual(["/api/v1/resources/resource/route-switches?after_operation_id=hop_b", expect.anything()]);
});
test.each([{ ...operation, id: "foreign" }, { ...operation, snapshot: {} }, { ...operation, switch: undefined }, { ...operation, switch: { ...operation.switch, routes: [{ ...operation.switch.routes[0], after_release_operation_id: "hop_wrong" }] } }, { ...operation, routes: [{ ...route, resource_id: "foreign", enabled: true, state: "desired", http_ready: false, https_ready: false, certificate: { hostname: route.hostname, status: "pending" } }] }])("strict recovery rejects a different operation, resource or reviewed intent %j", (value) => expect(() => validateSwitchOperation(value, "resource", "pop_one", input)).toThrow());
test("original engine outcome remains independent of metadata and restore ownership", () => {
  expect(() => validateSwitchOperation({ ...operation, state: "uncertain", engine_outcome: "rolled_back", metadata_conflict: true, mutation_hold: true, restore_ownership_held: true }, "resource", "pop_one", input)).not.toThrow();
});
test("a deadline retires ignored transport and cannot be revived by a late response", async () => {
  vi.useFakeTimers(); let resolve!: (value: Response) => void; const fetch = vi.fn((_url: string, _options: RequestInit) => new Promise<Response>((done) => { resolve = done; })); vi.stubGlobal("fetch", fetch);
  const read = getSwitchOptions(scope); const rejected = expect(read).rejects.toThrow("timed out"); await vi.advanceTimersByTimeAsync(5000); await rejected; expect(fetch.mock.calls[0]![1].signal?.aborted).toBe(true); resolve(new Response(JSON.stringify(options))); await Promise.resolve();
});
