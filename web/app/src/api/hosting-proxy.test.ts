import { afterEach, expect, test, vi } from "vitest";
import { configureProxy, createRoute, deleteRoute, grantHostname, proxyQuery, revokeHostname, routeOperationQuery, routesQuery, updateRoute } from "./hosting-proxy";
afterEach(() => vi.unstubAllGlobals());
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
test("proxy wrappers encode identities, preserve exact bodies and propagate signal/CSRF", async () => {
  const fetch = vi.fn(async (_url: string, _options: RequestInit) => response({})); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  const configure = { control_project_id: "project", configuration: { email: "ops@example.test", ca: "letsencrypt-staging" as const, management_port: 18080 }, idempotency_key: "same" };
  await configureProxy("server/one", configure, { signal });
  expect(fetch.mock.calls[0]).toEqual(["/api/v1/servers/server%2Fone/proxy", expect.objectContaining({ method: "POST", signal, credentials: "include", body: JSON.stringify(configure), headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }) })]);
  const route = { expected_route_version: 2, idempotency_key: "route-key", hostname: "web.example.test", path_prefix: "/", expected_healthy_operation_id: "hop_one" };
  await createRoute("resource/one", route); await updateRoute("route/one", route); await deleteRoute("route/one", { expected_route_version: 2, idempotency_key: "withdraw" });
  await grantHostname("server/one", { project_id: "project", hostname: "example.test", wildcard: true }); await revokeHostname("server/one", "grant/one", { idempotency_key: "revoke" });
  expect(fetch.mock.calls.map(([path]) => path)).toContain("/api/v1/servers/server%2Fone/proxy/hostname-grants/grant%2Fone/revoke");
  expect(fetch.mock.calls[3]?.[1]).toMatchObject({ method: "DELETE", body: '{"expected_route_version":2,"idempotency_key":"withdraw"}' });
});
test("only proxy 404 means unconfigured; unavailable and forbidden fail closed", async () => {
  for (const status of [404, 403, 503]) {
    vi.stubGlobal("fetch", vi.fn(async () => response({}, status)));
    const query = proxyQuery("actor", "server");
    const result = query.queryFn!({ signal: new AbortController().signal } as never);
    if (status === 404) expect(await result).toBeNull(); else await expect(result).rejects.toMatchObject({ status });
  }
});
test("resource operation projection excludes full admin snapshots and other resources", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => response({ id: "op", state: "queued", mutation_hold: false, restore_ownership_held: false, snapshot: { configuration: { email: "private@example.test" }, routes: [{ resource_id: "foreign" }] }, plan: { sensitive: "admin-only" } })));
  const query = routeOperationQuery("actor", "project", "own", "op");
  expect(await query.queryFn!({ signal: new AbortController().signal } as never)).toEqual({ id: "op", state: "queued", mutation_hold: false, restore_ownership_held: false, routes: [] });
  vi.stubGlobal("fetch", vi.fn(async () => response({ id: "op", state: "queued", routes: [{ resource_id: "own" }, { resource_id: "foreign" }] })));
  expect((await query.queryFn!({ signal: new AbortController().signal } as never)).routes).toEqual([{ resource_id: "own" }]);
});
test("route lists reject foreign resource payloads and scope keys include actor/project", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => response([{ resource_id: "foreign" }])));
  const query = routesQuery("actor", "project", "own");
  expect(query.queryKey).toEqual(["hostingRoutes", "actor", "project", "own"]);
  await expect(query.queryFn!({ signal: new AbortController().signal } as never)).rejects.toThrow("scope mismatch");
});

test("safe projection retains metadata conflict and original engine outcome without snapshots", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => response({ id: "op", state: "uncertain", metadata_conflict: true, mutation_hold: true, restore_ownership_held: false, result: { outcome: "rolled_back" }, snapshot: { secret: "excluded" } })));
  const query = routeOperationQuery("actor", "project", "own", "op"); expect(await query.queryFn!({ signal: new AbortController().signal } as never)).toEqual({ id: "op", state: "uncertain", metadata_conflict: true, engine_outcome: "rolled_back", mutation_hold: true, restore_ownership_held: false, routes: [] });
});
