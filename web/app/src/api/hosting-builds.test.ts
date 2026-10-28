import { afterEach, expect, test, vi } from "vitest";
import { buildsQuery, buildQuery, getHostingBuild, getHostingBuildLogs, cancelHostingBuild, requestHostingBuild, type HostingBuild } from "./hosting-builds";
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

test("output reads encode the build and preserve the server page without caching plaintext", async () => {
  const page = { items: [], next_sequence: 7, terminal: true };
  const fetch = vi.fn(async () => json(page)); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  expect(await getHostingBuildLogs("build/one", 4, { signal })).toEqual(page);
  expect(fetch).toHaveBeenCalledWith("/api/v1/hosting-builds/build%2Fone/logs?after_sequence=4&limit=200", expect.objectContaining({ method: "GET", credentials: "include", cache: "no-store", signal }));
});

test("only explicitly private requests forward the frozen grant ID", async () => {
 const fetch = vi.fn(async (_url: string, _options: RequestInit) => json({ build })); vi.stubGlobal("fetch", fetch);
 const input = { expected_resource_generation: 2, expected_source_generation: 3, idempotency_key: "same-private", grant_id: "grant-one" };
 await requestHostingBuild("resource", input, { credentialKind: "https_basic" }); await requestHostingBuild("resource", input, { credentialKind: "https_basic" }); await requestHostingBuild("resource", input, { credentialKind: "public" });
 expect(JSON.parse(fetch.mock.calls[0]![1].body as string)).toEqual(input); expect(fetch.mock.calls[0]![1].body).toBe(fetch.mock.calls[1]![1].body); expect(JSON.parse(fetch.mock.calls[2]![1].body as string)).not.toHaveProperty("grant_id");
});

const composeSettings = { strategy: "compose" as const, context_dir: "app", compose_file: "deploy/compose.yaml", public_component: "web", container_port: 3000 };
const composeResult: NonNullable<HostingBuild["compose_result"]> = {
  source: { original: "services:\n  web:\n    build: .\n", public_component: "web", container_port: 3000 },
  definition: { version: "git-compose.v1", original_hash: `sha256:${"a".repeat(64)}`, public_component: "web", container_port: 3000, volumes: ["db"], services: [
    { name: "db", image: "postgres:17", command: null, mounts: [{ volume: "db", target: "/var/lib/postgresql/data" }] },
    { name: "web", image: "local/web:built", command: null, build: { context: "app", dockerfile: "Dockerfile" } },
    { name: "worker", image: "local/web:built", command: ["worker"] },
  ] }, services: [{ component: "web", artifact: { origin: "local_build", image_id: "sha256:built", index_digest: "index", manifest_digest: "manifest", config_digest: "config", platform: "linux/amd64" } }], hash: `sha256:${"b".repeat(64)}`,
};
const composeBuild: HostingBuild = { ...build, state: "succeeded", snapshot: { ...build.snapshot, source: { ...build.snapshot.source, settings: composeSettings } }, source_receipt: { commit: "resolved-commit", tree_digest: "source-tree" }, compose_result: composeResult };
test("prepared Compose metadata retains its source, settings and component artifacts while stripping internal policy", async () => {
  const raw = { ...composeBuild, compose_result: { ...composeResult, source_receipt_hash: "internal-receipt", services: [{ ...composeResult.services[0], artifact: { ...composeResult.services[0]!.artifact, request_fingerprint: "internal-fingerprint" } }] } };
  vi.stubGlobal("fetch", vi.fn(async () => json({ build: raw })));
  const decoded = await buildQuery("actor", "project", "resource", build.id).queryFn!({ signal: new AbortController().signal } as never);
  expect(decoded.snapshot.source.settings).toMatchObject(composeSettings);
  expect(decoded.compose_result).toMatchObject(composeResult);
  expect(decoded.artifact).toBeUndefined();
  expect(JSON.stringify(decoded)).not.toMatch(/internal-receipt|internal-fingerprint|request_fingerprint/);
});
test.each([
  { ...composeBuild, artifact: composeResult.services[0]!.artifact },
  { ...composeBuild, compose_result: { ...composeResult, services: [] } },
  { ...composeBuild, compose_result: { ...composeResult, source: { ...composeResult.source, public_component: "db" } } },
  { ...composeBuild, compose_result: { ...composeResult, hash: "substituted" } },
])("invalid or substituted Compose result is rejected before display/deployment", async raw => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ build: raw })));
  await expect(buildQuery("actor", "project", "resource", build.id).queryFn!({ signal: new AbortController().signal } as never)).rejects.toThrow();
});

test("public build cache retains configured variables and resolved arguments while dropping unrelated metadata", async () => {
 const result = { ...composeResult, definition: { ...composeResult.definition, services: composeResult.definition.services.map(service => service.build ? { ...service, build: { ...service.build, args: { MODE: "release", EMPTY: "" } } } : service) } };
 const raw = { ...composeBuild, snapshot: { ...composeBuild.snapshot, source: { ...composeBuild.snapshot.source, settings: { ...composeSettings, build_variables: [{ name: "MODE", value: "release", secret_reference: "private" }] } } }, compose_result: result };
 vi.stubGlobal("fetch", vi.fn(async () => json({ build: raw })));
 const decoded = await getHostingBuild(build.id);
 expect(decoded.snapshot.source.settings.build_variables).toEqual([{ name: "MODE", value: "release" }]);
 expect(decoded.compose_result!.definition.services.find(service => service.build)?.build?.args).toEqual({ MODE: "release", EMPTY: "" });
 expect(JSON.stringify(decoded)).not.toContain("secret_reference");
});
