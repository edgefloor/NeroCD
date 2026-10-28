import { afterEach, expect, test, vi } from "vitest";
import { connectDatabase, connectionKey, getDatabaseConnection, getDatabaseConnectionOptions, validateConnection } from "./hosting-database-connections";
const scope = { actor: "actor", project: "project", resource: "app" };
export const connectionFixture = { id: "hcn_test", project_id: "project", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: `sha256:${"a".repeat(64)}`, state: "active", reason_code: "completed", capture_id: "capture", upgrade_operation_id: "upgrade", application_operation_id: "app_operation", membership_revision: 1, created_at: "2026-10-24T00:00:00Z", updated_at: "2026-10-24T00:00:00Z" };
afterEach(() => vi.unstubAllGlobals());
test("connection reads no-store metadata, distinguishes missing from unavailable, and keys actor scope", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(connectionFixture))).mockResolvedValueOnce(new Response("{}", { status: 404 })).mockResolvedValueOnce(new Response("{}", { status: 503 }));
  vi.stubGlobal("fetch", fetch);
  expect(await getDatabaseConnection(scope)).toEqual(connectionFixture);
  expect(fetch.mock.calls[0][1].cache).toBe("no-store");
  expect(await getDatabaseConnection(scope)).toBeNull();
  await expect(getDatabaseConnection(scope)).rejects.toMatchObject({ status: 503 });
  expect(connectionKey(scope)).not.toEqual(connectionKey({ ...scope, actor: "other" }));
});
test("cross-project/application/database metadata and unexpected secret values fail closed", () => {
  for (const change of [{ project_id: "other" }, { application_resource_id: "other" }, { database_resource_id: "other" }, { password: "must_not_display" }]) expect(() => validateConnection({ ...connectionFixture, ...change }, scope, "db")).toThrow();
  expect(validateConnection({ ...connectionFixture, state: "future" }, scope).state).toBe("future");
});
test.each(["redis", "mysql"])("%s options use server-authoritative items without generic database or secret reads", async (engine) => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [{ resource_id: "db", name: "Data", engine, generation: 2, destination_id: "dest", status: "available", reason_code: "eligible" }] })));
  vi.stubGlobal("fetch", fetch);
  expect((await getDatabaseConnectionOptions(scope)).items).toHaveLength(1);
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/resources/app/database-connections/options");
});
test("transport retries preserve exact connection request and both pre-injection generations", async () => {
  const body = { binding: { database_resource_id: "db", expected_resource_generation: 3, expected_runtime_generation: 5, expected_database_generation: 2, request_id: "a".repeat(32) }, deployment: { expected_generation: 3, expected_runtime_configuration_generation: 5, idempotency_key: "b".repeat(32), container_port: 8080, readiness: { path: "/", timeout_seconds: 30 } } };
  const fetch = vi.fn().mockRejectedValueOnce(new TypeError("lost response")).mockResolvedValueOnce(new Response(JSON.stringify(connectionFixture)));
  vi.stubGlobal("fetch", fetch);
  await expect(connectDatabase(scope, body)).rejects.toThrow();
  await connectDatabase(scope, body);
  expect(fetch.mock.calls[0][1].body).toBe(fetch.mock.calls[1][1].body);
  expect(fetch.mock.calls[1][1].headers["X-NeroCD-CSRF"]).toBe("1");
});
test("malformed options fail closed", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ items: [{ resource_id: "db", name: "Data", engine: "postgresql", generation: 0, destination_id: "dest", status: "available", reason_code: "eligible" }] }))).mockResolvedValueOnce(new Response(JSON.stringify({ items: [], password: "forbidden" })));
  vi.stubGlobal("fetch", fetch);
  await expect(getDatabaseConnectionOptions(scope)).rejects.toThrow();
  await expect(getDatabaseConnectionOptions(scope)).rejects.toThrow();
});

test("continuation freezes every route and requires the exact healthy loaded predecessor", async () => {
  const { connectionRouteIntent } = await import("./hosting-database-connections");
  const options = { resource_id: "app", loaded_generation: 7, loaded_proxy_operation_id: "pop_loaded", available: true, reason: "available" as const, next_after_operation_id: null, routes: [{ id: "route_a", hostname: "app.example.test", path_prefix: "/", version: 3, selected_release_operation_id: "hop_old" }, { id: "route_b", hostname: "other.example.test", path_prefix: "/", version: 4, selected_release_operation_id: "hop_old" }], targets: [{ operation_id: "hop_old", kind: "current_healthy" as const }] };
  expect(connectionRouteIntent(options)).toEqual({ expected_loaded_generation: 7, expected_loaded_proxy_operation_id: "pop_loaded", routes: options.routes.map((route) => ({ route_id: route.id, expected_version: route.version, expected_selected_release_operation_id: "hop_old" })) });
  for (const value of [{ ...options, loaded_proxy_operation_id: null }, { ...options, available: false }, { ...options, routes: [{ ...options.routes[0]!, selected_release_operation_id: "foreign" }] }]) expect(connectionRouteIntent(value)).toBeUndefined();
});
test("progress reads reject foreign scope and project only safe public status", async () => {
  const { connectedProgressQuery } = await import("./hosting-database-connections");
  const { QueryClient } = await import("@tanstack/react-query");
  const client = new QueryClient();
  const value = { id: "hop", project_id: "project", resource_id: "app", state: "verifying", mutation_hold: true, restore_ownership_held: false, application_database_route_continuity: { operation_id: "hop", proxy_operation_id: "pop" }, application_database_yield: { version: "application-database.route-continuity.v1", receipt_id: "receipt", plan_hash: `sha256:${"a".repeat(64)}`, stage_hash: `sha256:${"b".repeat(64)}`, candidate_hash: `sha256:${"c".repeat(64)}`, continuation_hash: `sha256:${"d".repeat(64)}` }, lease: "must_not_cache" };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(value))).mockResolvedValueOnce(new Response(JSON.stringify({ ...value, project_id: "foreign" }))));
  expect(await client.fetchQuery(connectedProgressQuery(scope, "hop"))).toEqual({ id: "hop", state: "verifying", mutation_hold: true, restore_ownership_held: false, proxy_operation_id: "pop", yielded: true });
  await expect(client.fetchQuery(connectedProgressQuery(scope, "hop"))).rejects.toThrow("scope mismatch");
  client.clear();
});

test("operation storage evidence is closed and projected without authority or secret fields", async () => {
  const { connectedProgressQuery } = await import("./hosting-database-connections");
  const { QueryClient } = await import("@tanstack/react-query");
  const client = new QueryClient();
  const hash = `sha256:${"a".repeat(64)}`;
  const descriptor = { version: "application-database.storage.v1", strategy: "replacement_with_interruption", storage_ref: { storage_id: "storage", generation: 1, hash }, hash };
  const state = { version: "application-database.storage.v1", phase: "stopped", receipt_id: "receipt", state_hash: hash, previous_state_hash: "", candidate_hash: "", stage_hash: "", recovery_actions: [], recovery_action: "", recovery_state: "none" };
  const base = { id: "hop", project_id: "project", resource_id: "app", state: "applying", mutation_hold: true, restore_ownership_held: false, application_database_storage: descriptor, application_database_storage_state: state, fence: "must_not_cache" };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(base))).mockResolvedValueOnce(new Response(JSON.stringify({ ...base, application_database_storage: { ...descriptor, password: "must_not_cache" } }))).mockResolvedValueOnce(new Response(JSON.stringify({ ...base, application_database_storage_state: { ...state, phase: "unlock" } }))));
  const result = await client.fetchQuery(connectedProgressQuery(scope, "hop"));
  expect(result.storage).toEqual({ strategy: "replacement_with_interruption", storage_id: "storage", generation: 1, hash });
  expect(result.storagePhase).toBe("stopped"); expect(result).not.toHaveProperty("fence");
  await expect(client.fetchQuery(connectedProgressQuery(scope, "hop"))).rejects.toThrow("descriptor");
  await expect(client.fetchQuery(connectedProgressQuery(scope, "hop"))).rejects.toThrow("state");
  client.clear();
});

test("missing recovery correlation and unknown offered actions fail closed", async () => {
  const { connectedProgressQuery } = await import("./hosting-database-connections");
  const { QueryClient } = await import("@tanstack/react-query");
  const client = new QueryClient();
  const hash = `sha256:${"a".repeat(64)}`;
  const state = { version: "application-database.storage.v1", phase: "replacement_failed", receipt_id: "receipt", state_hash: hash, previous_state_hash: hash, candidate_hash: hash, stage_hash: hash, recovery_actions: ["resume_predecessor"], recovery_action: "", recovery_state: "none" };
  const base = { id: "hop", project_id: "project", resource_id: "app", state: "failed", mutation_hold: true, restore_ownership_held: false, application_database_storage: { version: "application-database.storage.v1", strategy: "replacement_with_interruption", storage_ref: { storage_id: "storage", generation: 1, hash }, hash } };
  const { stage_hash: omitted, ...missing } = state; void omitted;
  const responses = [missing, { ...state, recovery_actions: ["unlock"] }, { ...state, stage_hash: "" }, { ...state, recovery_actions: ["resume_predecessor", "resume_predecessor"] }];
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ ...base, application_database_storage_state: responses.shift() }))));
  for (let i = 0; i < 4; i++) await expect(client.fetchQuery(connectedProgressQuery(scope, "hop"))).rejects.toThrow("state");
  client.clear();
});
