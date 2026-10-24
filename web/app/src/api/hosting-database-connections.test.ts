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
test("options use server-authoritative items without generic database or secret reads", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [{ resource_id: "db", name: "Data", engine: "redis", generation: 2, destination_id: "dest", status: "available", reason_code: "eligible" }] })));
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
