import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import type { HostingBuild } from "@/api/hosting-builds";
import { ResourceBuilds } from "./ResourceBuilds";
const resource: Resource = { id: "r", project_id: "p", project_environment_id: "e", name: "Web", kind: "application", generation: 2, created_by: "u", created_at: "", updated_at: "", source: { kind: "git", git: { source_id: "s", generation: 3, repository_url: "https://github.com/example/web.git", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
const source = resource.source.kind === "git" ? resource.source.git : undefined;
const build: HostingBuild = { id: "b", project_id: "p", resource_id: "r", state: "succeeded", revision: 4, created_by: "u", created_at: "", updated_at: "", snapshot: { resource_generation: 2, source: source!, target: { binding_id: "binding", destination_id: "dest", server_id: "server", runner_id: "runner" } }, artifact: { origin: "local_build", image_id: "sha256:recorded", index_digest: "index", manifest_digest: "manifest", config_digest: "config", platform: "linux/amd64" } };
const operation = { id: "hop_local", project_id: "p", resource_id: "r", binding_id: "binding", state: "queued", mutation_hold: true, snapshot: { resource_id: "r", resource_generation: 2, local_build: { source_generation: 3, artifact_hash: "artifact-hash", artifact: { ...build.artifact, build_id: "b", source_id: "s" } }, image_reference: "", container_port: 80, host_port: 30000, readiness: { path: "/", timeout_seconds: 30 } } };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? (url.endsWith("/runtime-configuration") ? json({ runtime_configuration: { resource_id: "r", project_id: "p", resource_generation: 2, runtime_generation: 0, entries: [] } }) : url.endsWith("/binding") ? json({ id: "binding", resource_id: "r", destination_id: "dest" }) : url.includes("/builds?") ? json({ items: [build], limit: 20, offset: 0, count: 1, total: 1 }) : url.includes("/hosting-builds/") ? json({ build }) : url.includes("/deployments?") ? json({ items: [], limit: 20, offset: 0, count: 0, total: 0 }) : json(options.method === "POST" && JSON.parse(options.body as string).expected_runtime_configuration_generation ? { ...operation, snapshot: { ...operation.snapshot, runtime_configuration: { generation: JSON.parse(options.body as string).expected_runtime_configuration_generation, hash: "reference-hash" } } } : operation, options.method === "POST" ? 202 : 200)));
  vi.stubGlobal("fetch", fetch); return fetch;
}
async function setup(writable = true, currentResource = resource) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "u", provider: "oidc", roles: [] }); client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "p", user_id: "u", role: "maintainer" }]);
  const view = render(<QueryClientProvider client={client}><ResourceBuilds resource={currentResource} writable={writable} blocked={false} /></QueryClientProvider>);
  fireEvent.click(await screen.findByRole("button", { name: "View build b" })); await screen.findByRole("region", { name: "Deploy recorded local build" }); return view;
}
async function submit() {
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(false));
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
  await act(async () => { await Promise.resolve(); });
}
test("successful exact build submits only server-selected artifact admission inputs and shares operation detail", async () => {
  const fetch = backend(); await setup(); await submit(); await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([url, o]) => url.endsWith("/from-build") && o.method === "POST"); expect(writes).toHaveLength(1);
  const body = JSON.parse(writes[0]![1].body as string); expect(body).toEqual({ build_id: "b", expected_generation: 2, expected_source_generation: 3, idempotency_key: expect.any(String), container_port: 80, readiness: { path: "/", timeout_seconds: 30 } });
  expect(await screen.findByText("Local build / source generation")).toBeTruthy(); expect(screen.getByText("Recorded artifact image ID")).toBeTruthy();
});
test.each(["lost", "503"])("%s freezes settings and retains identical body/key even after history reveals mutation hold", async (failure) => {
  let writes = 0; const fetch = backend((url, o) => url.endsWith("/from-build") ? ++writes === 1 ? failure === "lost" ? Promise.reject(new TypeError("lost")) : json({}, 503) : json(operation, 202) : writes && url.includes("/deployments?") ? json({ items: [operation] }) : undefined);
  await setup(); fireEvent.change(screen.getByLabelText("Build container port"), { target: { value: "3000" } }); await submit();
  await screen.findByText(/Deployment delivery is unconfirmed/); expect(screen.getByLabelText<HTMLInputElement>("Build container port").disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh local deployment prerequisites" })); await screen.findByText(/A recorded mutation hold pauses/);
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" })); await screen.findByRole("region", { name: "Deployment operation detail" });
  const inputs = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build")); expect(inputs).toHaveLength(2); expect(inputs[0]![1].body).toBe(inputs[1]![1].body);
});
test.each([409, 403])("HTTP %s preserves intent and blocks further delivery without automatic refresh/reselection", async (status) => {
  const fetch = backend((url) => url.endsWith("/from-build") ? json({}, status) : undefined); await setup(); await submit();
  await screen.findByText(status === 409 ? /Deployment conflicts with current/ : /Local deployment access is pending or unavailable/);
  if (status === 409) { fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" })); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical local build deployment" }).disabled).toBe(true); }
  else expect(screen.queryByRole("form", { name: "Deploy selected build" })).toBeNull();
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(1);
});
test("restore hold cannot become a generation review/retry flow", async () => { const fetch = backend((url) => url.endsWith("/from-build") ? json({ code: "hosting_restore_held" }, 409) : undefined); await setup(); await submit(); await screen.findByText(/Deployment blocked after platform restore/); fireEvent.click(screen.getByRole("button", { name: "Refresh local deployment prerequisites" })); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical local build deployment" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(1); });
test.each(["actor", "permission", "resource"])("late response and request do not survive %s change", async (scope) => {
  let resolve!: (response: Response) => void; backend((url) => url.endsWith("/from-build") ? new Promise<Response>((done) => { resolve = done; }) : undefined); const view = await setup(); await submit();
  if (scope === "actor") await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); });
  else view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={scope === "resource" ? { ...resource, id: "other" } : resource} writable={scope !== "permission"} blocked={false} /></QueryClientProvider>);
  await act(async () => { resolve(json(operation, 202)); }); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull(); expect(screen.queryByRole("button", { name: "Retry identical local build deployment" })).toBeNull();
});
test("viewer sees artifact metadata but cannot deploy", async () => { backend(); await setup(false); expect(screen.getByText(/Project maintainer access is required to deploy/)).toBeTruthy(); expect(screen.queryByRole("form", { name: "Deploy selected build" })).toBeNull(); });
test("stale source and different destination cannot authorize initial deployment", async () => { backend((url) => url.endsWith("/binding") ? json({ id: "other-binding", destination_id: "other" }) : undefined); await setup(); fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/)); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true); expect(screen.getByText(/Restore the build's exact saved destination/)).toBeTruthy(); });

test.each([
 { change: "rename", latest: { ...resource, name: "Renamed Web", generation: 4 } },
 { change: "configuration generation", latest: { ...resource, generation: 4 } },
 { change: "source generation", latest: { ...resource, generation: 4, source: { kind: "git" as const, git: { ...source!, generation: 5 } } } },
])("$change requires a new build and preserves the conflicted request after review", async ({ latest }) => {
 const fetch = backend((url) => url.endsWith("/from-build") ? json({}, 409) : url === "/api/v1/resources/r" ? json(latest) : undefined);
 await setup(); fireEvent.change(screen.getByLabelText("Build container port"), { target: { value: "3000" } }); await submit(); await screen.findByText(/Deployment conflicts with current/);
 const original = JSON.parse(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body as string);
 fireEvent.click(screen.getByRole("button", { name: "Review this exact build for a separate request" }));
 await screen.findByText(/A new build is required for the current resource and source generations before a separate deployment/);
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical local build deployment" }).disabled).toBe(true);
 expect(screen.queryByRole("button", { name: "Deploy selected build" })).toBeNull();
 expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful build/).checked).toBe(true);
 expect(screen.getByLabelText<HTMLInputElement>("Build container port").value).toBe("3000");
 expect(screen.getByLabelText<HTMLInputElement>("Build container port").disabled).toBe(true);
 expect(screen.getByText(new RegExp(`Retained request key ${original.idempotency_key}`))).toBeTruthy();
 fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
 expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(1);
 expect(fetch.mock.calls.filter(([url, o]) => o.method === "POST" && url.endsWith("/builds"))).toHaveLength(0);
});
test.each([
 { change: "resource generation", latest: { ...resource, name: "Renamed Web", generation: 4 } },
 { change: "source generation", latest: { ...resource, source: { kind: "git" as const, git: { ...source!, generation: 4 } } } },
])("initial deployment is blocked when the build predates the $change", async ({ latest }) => {
 const fetch = backend(); await setup(true, latest);
 fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/));
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
 await screen.findByText(/A new build is required for the current configuration/);
 fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
 expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(0);
});
test("unchanged resource and source generations permit an explicitly acknowledged separate request", async () => {
 let count = 0; const fetch = backend((url) => url.endsWith("/from-build") ? ++count === 1 ? json({}, 409) : json(operation, 202) : url === "/api/v1/resources/r" ? json(resource) : undefined);
 await setup(); await submit(); await screen.findByText(/Deployment conflicts with current/);
 fireEvent.click(screen.getByRole("button", { name: "Review this exact build for a separate request" }));
 await screen.findByRole("button", { name: "Deploy selected build" });
 expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful build/).checked).toBe(false);
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
 await submit(); await screen.findByRole("region", { name: "Deployment operation detail" });
 const inputs = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build")).map(([, o]) => JSON.parse(o.body as string));
 expect(inputs[1]).toMatchObject({ build_id: "b", expected_generation: 2, expected_source_generation: 3 }); expect(inputs[1].idempotency_key).not.toBe(inputs[0].idempotency_key);
 expect(fetch.mock.calls.filter(([url, o]) => o.method === "POST" && url.endsWith("/builds"))).toHaveLength(0);
});
test("changed generations preserve an uncertain identical request for historical receipt recovery", async () => {
 let count = 0; const fetch = backend((url) => url.endsWith("/from-build") ? ++count === 1 ? json({}, 503) : json(operation, 202) : url === "/api/v1/resources/r" ? json({ ...resource, name: "Renamed Web", generation: 4 }) : undefined);
 const view = await setup(); await submit(); await screen.findByText(/Deployment delivery is unconfirmed/);
 fireEvent.click(screen.getByRole("button", { name: "Review this exact build for a separate request" }));
 await screen.findByText(/A new build is required for the current resource and source generations before a separate deployment/);
 view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={{ ...resource, name: "Renamed Web", generation: 4 }} writable blocked={false} /></QueryClientProvider>);
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical local build deployment" }).disabled).toBe(false);
 fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" })); await screen.findByRole("region", { name: "Deployment operation detail" });
 const writes = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build")); expect(writes).toHaveLength(2); expect(writes[1]![1].body).toBe(writes[0]![1].body);
 expect(JSON.parse(writes[1]![1].body as string)).toMatchObject({ build_id: "b", expected_generation: 2, expected_source_generation: 3 });
});
test("new source metadata cannot silently update an acknowledged deployment", async () => {
 const fetch = backend(); const view = await setup(); fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/));
 view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={{ ...resource, generation: 3, source: { kind: "git", git: { ...source!, generation: 4 } } }} writable blocked={false} /></QueryClientProvider>);
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
 expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(0);
});
test("foreign or substituted artifact in an accepted response never becomes a selected operation", async () => {
 backend((url) => url.endsWith("/from-build") ? json({ ...operation, snapshot: { ...operation.snapshot, local_build: { ...operation.snapshot.local_build, artifact: { ...operation.snapshot.local_build.artifact, build_id: "different" } } } }, 202) : undefined);
 await setup(); await submit(); await screen.findByText(/Deployment delivery is unconfirmed/); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});

test("access denial while reviewing a separate intent also pauses the retained request", async () => {
 backend((url) => url.endsWith("/from-build") ? json({}, 503) : url === "/api/v1/resources/r" ? json({}, 403) : undefined);
 await setup(); await submit(); await screen.findByText(/Deployment delivery is unconfirmed/); fireEvent.click(screen.getByRole("button", { name: "Review this exact build for a separate request" }));
 await screen.findByText(/Local deployment access is pending or unavailable/); expect(screen.queryByRole("button", { name: "Retry identical local build deployment" })).toBeNull();
});

const privateResource: Resource = { ...resource, source: { kind: "git", git: { ...source!, credential_kind: "https_basic" } } };
const privateBuild: HostingBuild = { ...build, snapshot: { ...build.snapshot, source: { ...build.snapshot.source, credential_kind: "https_basic" } } };
function privateBackend(snapshot = privateBuild) { return backend((url) => url.includes("/source-grants?") ? json({ items: [], limit: 20, offset: 0, count: 0, total: 0 }) : url.includes("/project-members?") ? json({ items: [{ project_id: "p", user_id: "u", role: "maintainer" }], count: 1, total: 1, offset: 0 }) : url.includes("/hosting-builds/") ? json({ build: snapshot }) : undefined); }
test("a private HTTPS artifact with exact source identity and generations can be deployed", async () => { const fetch = privateBackend(); await setup(true, privateResource); await submit(); await screen.findByRole("region", { name: "Deployment operation detail" }); expect(JSON.parse(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body as string)).toMatchObject({ build_id: "b", expected_generation: 2, expected_source_generation: 3 }); });
test.each(["credential", "resource", "source"])("private artifact %s mismatch requires a new build without deployment admission", async (mismatch) => { const snapshot = mismatch === "credential" ? build : mismatch === "resource" ? { ...privateBuild, snapshot: { ...privateBuild.snapshot, resource_generation: 1 } } : { ...privateBuild, snapshot: { ...privateBuild.snapshot, source: { ...privateBuild.snapshot.source, generation: 2 } } }; const fetch = privateBackend(snapshot); await setup(true, privateResource); fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/)); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true); expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false); });

test("local deployment retains captured runtime generation and identical request after response loss", async () => {
  let generation = 4, reads = 0, writes = 0;
  const fetch = backend((url) => url.endsWith("/runtime-configuration") ? (reads++, json({ runtime_configuration: { resource_id: "r", project_id: "p", resource_generation: 2, runtime_generation: generation, entries: [] } })) : url.endsWith("/from-build") && ++writes === 1 ? Promise.reject(new TypeError("lost")) : undefined);
  await setup(); await submit(); await screen.findByText(/Deployment delivery is unconfirmed/); generation = 6;
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" })); await screen.findByRole("region", { name: "Deployment operation detail" });
  const bodies = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build")); expect(reads).toBe(1); expect(bodies[1]![1].body).toBe(bodies[0]![1].body); expect(JSON.parse(bodies[1]![1].body as string)).toMatchObject({ expected_runtime_configuration_generation: 4 });
});
test("failed runtime metadata lookup sends no local deployment request", async () => {
  const fetch = backend((url) => url.endsWith("/runtime-configuration") ? json({}, 503) : undefined); await setup(); await submit(); await screen.findByText(/Unable to verify saved runtime configuration/); expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test("membership downgrade aborts pending local deployment and clears its intent", async () => {
  let resolve!: (response: Response) => void;
  const fetch = backend((url) => url.endsWith("/from-build") ? new Promise<Response>((done) => { resolve = done; }) : undefined); await setup(); await submit(); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  const input = fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1];
  await act(async () => { client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "p", user_id: "u", role: "viewer" }]); });
  await screen.findByText("Project maintainer access is required to deploy this build."); expect(input.signal?.aborted).toBe(true);
  await act(async () => resolve(json(operation, 202))); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull(); expect(screen.queryByText(/Retained request key/)).toBeNull();
});
test("local deployment response with another runtime generation cannot select an operation", async () => {
  backend((url) => url.endsWith("/runtime-configuration") ? json({ runtime_configuration: { resource_id: "r", project_id: "p", resource_generation: 2, runtime_generation: 3, entries: [] } }) : url.endsWith("/from-build") ? json({ ...operation, snapshot: { ...operation.snapshot, runtime_configuration: { generation: 4, hash: "wrong" } } }, 202) : undefined);
  await setup(); await submit(); await screen.findByText(/Deployment delivery is unconfirmed/); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});
