import { afterEach, expect, test, vi } from "vitest";
import { buildsQuery, buildQuery, cancelHostingBuild, requestHostingBuild, type HostingBuild } from "./hosting-builds";
const build: HostingBuild = { id: "build-one", project_id: "project", resource_id: "resource", state: "queued", revision: 1, created_by: "user", created_at: "", updated_at: "", snapshot: { resource_generation: 2, source: { source_id: "source", generation: 3, repository_url: "https://github.com/example/web.git", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } }, target: { binding_id: "binding", destination_id: "dest", server_id: "server", runner_id: "runner" } } };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
afterEach(() => vi.unstubAllGlobals());
test("build writes encode targets and send only public intent, retaining exact key and signal", async () => {
  const fetch = vi.fn(async (_url: string, _options: RequestInit) => json({ build })); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  const input = { expected_resource_generation: 2, expected_source_generation: 3, idempotency_key: "same", policy: { forbidden: true }, artifact: "forbidden" };
  await requestHostingBuild("resource/one", input, { signal }); await requestHostingBuild("resource/one", input, { signal }); await cancelHostingBuild("build/one", 4, { signal });
  expect(fetch.mock.calls[0]).toEqual(["/api/v1/resources/resource%2Fone/builds", expect.objectContaining({ signal, method: "POST", credentials: "include", body: '{"expected_resource_generation":2,"expected_source_generation":3,"idempotency_key":"same"}', headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }) })]);
  expect(fetch.mock.calls[1]![1].body).toBe(fetch.mock.calls[0]![1].body); expect(fetch.mock.calls[2]).toEqual(["/api/v1/hosting-builds/build%2Fone/cancel", expect.objectContaining({ body: '{"expected_revision":4}' })]);
});
test("query caches retain only whitelisted metadata and reject wrong scope", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ items: [{ ...build, internal_policy: "private", snapshot: { ...build.snapshot, policy: "private", source: { ...build.snapshot.source, grant: "private" } } }], limit: 20, offset: 0, count: 1, total: 1 })));
  const query = buildsQuery("actor", "project", "resource", 20);
  const result = await query.queryFn!({ signal: new AbortController().signal } as never);
  expect(JSON.stringify(result)).not.toMatch(/internal_policy|private|grant/); expect(query.queryKey).toEqual(["hostingBuilds", "actor", "project", "resource", 20]);
  vi.stubGlobal("fetch", vi.fn(async () => json({ build: { ...build, project_id: "foreign" } })));
  await expect(buildQuery("actor", "project", "resource", build.id).queryFn!({ signal: new AbortController().signal } as never)).rejects.toThrow("scope mismatch");
});
test("503 is preserved as an error rather than an empty successful history", async () => { vi.stubGlobal("fetch", vi.fn(async () => json({ error: "disabled" }, 503))); await expect(buildsQuery("actor", "project", "resource", 0).queryFn!({ signal: new AbortController().signal } as never)).rejects.toMatchObject({ status: 503 }); });
