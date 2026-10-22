import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "./errors";
import { cancelHostingOperation, getHostingOperation, HostingDeploymentError, hostingDeploymentsQuery, hostingOperationQuery, listHostingDeployments, reconcileHostingOperation, requestHostingDeployment } from "./hosting-deployments";

afterEach(() => vi.unstubAllGlobals());
const input = { expected_generation: 3, idempotency_key: "stable-request", container_port: 80, host_port: 8080, readiness: { path: "/health", timeout_seconds: 30 } };
test("hosting requests encode identifiers, bound pages, propagate cancellation and use exact mutation bodies", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response("{}"));
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await requestHostingDeployment("resource/one", input, { signal });
  await listHostingDeployments("resource/one", { limit: 20, offset: 40 }, { signal });
  await getHostingOperation("operation/one", { signal });
  await cancelHostingOperation("operation/one", { signal });
  await reconcileHostingOperation("operation/one", { signal });
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/resources/resource%2Fone/deployments", "/api/v1/resources/resource%2Fone/deployments?limit=20&offset=40", "/api/v1/hosting-operations/operation%2Fone", "/api/v1/hosting-operations/operation%2Fone/cancel", "/api/v1/hosting-operations/operation%2Fone/reconcile"]);
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(input);
  for (const index of [0, 3, 4]) expect(fetch.mock.calls[index][1]).toMatchObject({ method: "POST", headers: { "X-NeroCD-CSRF": "1" } });
  expect(fetch.mock.calls[3][1].body).toBe("{}"); expect(fetch.mock.calls[4][1].body).toBe("{}");
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ signal, credentials: "include" });
});
test("query identities isolate project, resource, operation and page", () => {
  expect(hostingDeploymentsQuery("p", "r", 0).queryKey).not.toEqual(hostingDeploymentsQuery("p", "r", 20).queryKey);
  expect(hostingDeploymentsQuery("p", "r", 0).queryKey).not.toEqual(hostingDeploymentsQuery("other", "r", 0).queryKey);
  expect(hostingOperationQuery("p", "r", "o").queryKey).not.toEqual(hostingOperationQuery("p", "other", "o").queryKey);
});
test("foreign operation and history responses cannot enter a resource's query data", async () => {
  const client = new QueryClient();
  const foreign = { id: "o", project_id: "other", resource_id: "r" };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(foreign))).mockResolvedValueOnce(new Response(JSON.stringify({ items: [foreign] }))));
  await expect(client.fetchQuery(hostingOperationQuery("p", "r", "o"))).rejects.toThrow("scope mismatch");
  await expect(client.fetchQuery(hostingDeploymentsQuery("p", "r", 0))).rejects.toThrow("scope mismatch");
  client.clear();
});
test.each([400, 401, 403, 404, 409, 503])("HTTP %s remains an actionable error rather than an empty history", async (status) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "conflict", error: "unavailable" }), { status })));
  await expect(listHostingDeployments("r", { limit: 20, offset: 0 })).rejects.toBeInstanceOf(ApiError);
});
test("deployment admission retains the restore hold code independently of generic conflict", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "hosting_restore_held", error: "recovery proof pending" }), { status: 409, headers: { "X-Request-ID": "restore-request" } })));
  await expect(requestHostingDeployment("r", input)).rejects.toMatchObject({ status: 409, code: "hosting_restore_held", requestID: "restore-request" });
  expect(new HostingDeploymentError(409, "held", null, "hosting_restore_held")).toBeInstanceOf(ApiError);
});
