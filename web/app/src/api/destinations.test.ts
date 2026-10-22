import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { bindResourceDestination, createHostingServer, destinationGrantsQuery, destinationsQuery, getHostingServer, getResourceBinding, grantDestinationProject, listDestinationGrants, listDestinations, listHostingServers, resourceBindingQuery, revokeDestinationProject, type DestinationScope } from "./destinations";
import { ApiError } from "./errors";
afterEach(() => vi.unstubAllGlobals());
test("destination API scopes and encodes IDs, propagates signals, and protects writes with CSRF", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response("{}")); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await listHostingServers({ limit: 20, offset: 40 }, { signal });
  await getHostingServer("server/one", { signal });
  await listDestinations({ project_id: "project one", limit: 20, offset: 0 }, { signal });
  await listDestinations({ server_id: "server/one", limit: 20, offset: 20 }, { signal });
  await listDestinationGrants("dest/one", { limit: 20, offset: 0 }, { signal });
  await getResourceBinding("resource/one", { signal });
  await createHostingServer({ name: "Host", runner_id: "runner_one" }, { signal });
  await grantDestinationProject("dest/one", "project_one", { signal });
  await revokeDestinationProject("dest/one", "project_one", { signal });
  await bindResourceDestination("resource/one", "dest_one", { signal });
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/servers?limit=20&offset=40", "/api/v1/servers/server%2Fone", "/api/v1/destinations?project_id=project+one&limit=20&offset=0", "/api/v1/destinations?server_id=server%2Fone&limit=20&offset=20", "/api/v1/destinations/dest%2Fone/project-grants?limit=20&offset=0", "/api/v1/resources/resource%2Fone/binding", "/api/v1/servers", "/api/v1/destinations/dest%2Fone/project-grants", "/api/v1/destinations/dest%2Fone/project-grants/revoke", "/api/v1/resources/resource%2Fone/binding"]);
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ signal, credentials: "include" });
  expect(fetch.mock.calls.slice(6).map(([, options]) => JSON.parse(options.body))).toEqual([{ name: "Host", runner_id: "runner_one" }, { project_id: "project_one" }, { project_id: "project_one" }, { destination_id: "dest_one" }]);
  for (const [, options] of fetch.mock.calls.slice(6)) expect(options).toMatchObject({ method: "POST", headers: { "X-NeroCD-CSRF": "1" } });
});
test("destination queries keep server, project, resource, and pagination scopes distinct", () => {
  expect(destinationsQuery({ project_id: "p", limit: 20, offset: 0 }).queryKey).not.toEqual(destinationsQuery({ server_id: "p", limit: 20, offset: 0 }).queryKey);
  expect(destinationGrantsQuery("a", 0).queryKey).not.toEqual(destinationGrantsQuery("b", 0).queryKey);
  expect(resourceBindingQuery("a", "r").queryKey).not.toEqual(resourceBindingQuery("b", "r").queryKey);
  // @ts-expect-error exactly one scope is required
  const both: DestinationScope = { project_id: "p", server_id: "s", limit: 20, offset: 0 };
  // @ts-expect-error an unscoped list is forbidden
  const neither: DestinationScope = { limit: 20, offset: 0 };
  expect(both).not.toBe(neither);
});
test.each([404, 403, 503])("only binding 404 becomes an unbound state, HTTP %s", async (status) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "unavailable" }), { status })));
  const client = new QueryClient();
  if (status === 404) await expect(client.fetchQuery(resourceBindingQuery("p", "r"))).resolves.toBeNull();
  else await expect(client.fetchQuery(resourceBindingQuery("p", "r"))).rejects.toBeInstanceOf(ApiError);
  client.clear();
});
