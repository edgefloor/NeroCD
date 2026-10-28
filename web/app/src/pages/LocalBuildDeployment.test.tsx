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

const composeSource = { ...source!, settings: { strategy: "compose" as const, context_dir: "app", compose_file: "compose.yaml", public_component: "web", container_port: 3000 } };
const composeResource: Resource = { ...resource, source: { kind: "git", git: composeSource } };
const composeBuild: HostingBuild = { ...build, artifact: undefined, snapshot: { ...build.snapshot, source: composeSource }, source_receipt: { commit: "resolved-commit", tree_digest: "tree" }, compose_result: {
  source: { original: "services:\n  web:\n    build: .\n", public_component: "web", container_port: 3000 },
  definition: { version: "git-compose.v1", original_hash: `sha256:${"a".repeat(64)}`, public_component: "web", container_port: 3000, volumes: ["data"], services: [
    { name: "database", image: "postgres:17", command: null, mounts: [{ volume: "data", target: "/var/lib/postgresql/data" }] },
    { name: "web", image: "local/web:built", build: { context: "app", dockerfile: "Dockerfile" }, command: null },
    { name: "worker", image: "local/web:built", command: ["worker"] },
  ] }, services: [{ component: "web", artifact: build.artifact! }], hash: `sha256:${"b".repeat(64)}`,
} };
const composeOperation = { ...operation, snapshot: { ...operation.snapshot, binding_id: "binding", local_build: undefined, git_compose: { build_id: "b", source_generation: 3, result_hash: composeBuild.compose_result!.hash }, stack: { generation: 2, hash: `sha256:${"c".repeat(64)}` }, container_port: 3000 } };
const initialComposeOptions = { resource_id: "r", resource_generation: 2, stack_ref: { generation: 2, hash: `sha256:${"c".repeat(64)}` }, storage_ref: null, previous_healthy_operation_id: null, host_port: null, container_port: null, readiness: null, available: true, reason: "initial_deployment", routes: [], storage_route_continuity: null };
const replacementComposeOptions = { ...initialComposeOptions, reason: "available", previous_healthy_operation_id: "prior", host_port: 30000, container_port: 3000, readiness: { path: "/", timeout_seconds: 30 } };
function composeBackend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  return backend((url, options) => handler?.(url, options) ?? (url.endsWith("/storage-deployment-options") ? json(initialComposeOptions) : url.endsWith("/stack-lifecycle") ? json({ resource_id: "r", resource_generation: 2, state: "unavailable" }) : url === "/api/v1/hosting-operations/prior" ? json({ ...composeOperation, id: "prior", state: "succeeded", mutation_hold: false }) : url.includes("/builds?") ? json({ items: [composeBuild], limit: 20, offset: 0, count: 1, total: 1 }) : url.includes("/hosting-builds/") ? json({ build: composeBuild }) : url.endsWith("/from-build") ? json(composeOperation, 202) : undefined));
}
async function composeSubmit(button = "Deploy selected build") {
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful Compose build/).disabled).toBe(false));
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: button }).disabled).toBe(false));
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
}
test("prepared mixed Compose shows source and per-service image origin, then admits the selected build", async () => {
  const fetch = composeBackend(); await setup(true, composeResource);
  const summary = screen.getByRole("region", { name: "Prepared Compose result" });
  expect(summary.textContent).toContain("app/compose.yaml");
  expect(summary.textContent).toContain("web: Built from this Git commit");
  expect(summary.textContent).toContain("worker: Uses the image built for web");
  expect(summary.textContent).toContain("database: Public registry image");
  expect(screen.getByText("resolved-commit")).toBeTruthy();
  expect(screen.getByLabelText<HTMLInputElement>("Build container port").value).toBe("3000");
  expect(screen.getByLabelText<HTMLInputElement>("Build container port").disabled).toBe(true);
  await composeSubmit(); await screen.findByRole("region", { name: "Deployment operation detail" });
  const body = JSON.parse(String(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body));
  expect(body).toMatchObject({ build_id: "b", expected_generation: 2, expected_source_generation: 3, container_port: 3000, managed_stack: { acknowledge_interruption: true } });
  expect(body).not.toHaveProperty("registry_credential");
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/storage-deployment-options"))).toBe(false);
});
test("Compose response loss retains the exact reviewed build/result request and runtime generation", async () => {
  let writes = 0, runtimeReads = 0;
  const fetch = composeBackend((url) => url.endsWith("/from-build") && ++writes === 1 ? Promise.reject(new TypeError("lost")) : url.endsWith("/runtime-configuration") ? (runtimeReads++, json({ runtime_configuration: { resource_id: "r", project_id: "p", resource_generation: 2, runtime_generation: 4, entries: [] } })) : url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, runtime_configuration: { generation: 4, hash: "runtime" } } }, 202) : undefined);
  await setup(true, composeResource); await composeSubmit(); await screen.findByText(/Deployment delivery is unconfirmed/);
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" })); await screen.findByRole("region", { name: "Deployment operation detail" });
  const requests = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"));
  expect(requests).toHaveLength(2); expect(requests[1]![1].body).toBe(requests[0]![1].body); expect(runtimeReads).toBe(1);
});
test("substituted Compose result hash in deployment receipt cannot select an operation", async () => {
  composeBackend(url => url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, git_compose: { ...composeOperation.snapshot.git_compose, result_hash: "other" } } }, 202) : undefined);
  await setup(true, composeResource); await composeSubmit(); await screen.findByText(/Deployment delivery is unconfirmed/);
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});
test("starting a committed Git Compose build captures and retains the exact stopped operation", async () => {
  let lifecycleReads = 0;
  const deployed = { ...composeOperation, id: "prior", state: "succeeded", mutation_hold: false };
  const fetch = composeBackend(url => url.includes("/deployments?") ? json({ items: [deployed], limit: 20, offset: 0, count: 1, total: 1 }) : url.endsWith("/storage-deployment-options") ? json({ ...replacementComposeOptions, stopped_operation_id: "stop" }) : url.endsWith("/stack-lifecycle") ? (lifecycleReads++, json({ resource_id: "r", resource_generation: 2, state: "stopped", deployed_operation_id: "prior", stop_operation_id: "stop" })) : url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, stack_stopped_operation_id: "stop" } }, 202) : undefined);
  await setup(true, composeResource); await screen.findByText(/Start recreates this selected build/);
  await composeSubmit("Start selected Compose build"); await screen.findByRole("region", { name: "Deployment operation detail" });
  const body = JSON.parse(String(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body));
  expect(body.managed_stack).toEqual({ acknowledge_interruption: true, expected_stopped_operation_id: "stop" });
  expect(lifecycleReads).toBe(3); // Initial proof, fresh admission proof, then post-admission revalidation.
});

test("stopped Git Compose with changed desired generations requires a new build and preserves the stop", async () => {
  const deployed = { ...composeOperation, id: "prior", state: "succeeded", mutation_hold: false };
  const fetch = composeBackend(url => url.includes("/deployments?") ? json({ items: [deployed], limit: 20, offset: 0, count: 1, total: 1 }) : url.endsWith("/stack-lifecycle") ? json({ resource_id: "r", resource_generation: 3, state: "stopped", deployed_operation_id: "prior", stop_operation_id: "stop-retained" }) : undefined);
  const latest: Resource = { ...composeResource, generation: 3, source: { kind: "git", git: { ...composeSource, generation: 4 } } };
  await setup(true, latest); await screen.findByText(/Start recreates this selected build/);
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Start selected Compose build" }).disabled).toBe(true);
  expect(screen.getByText(/A new build is required for the current configuration/)).toBeTruthy();
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});

const routedComposeOptions = {
  ...replacementComposeOptions,
  routes: [{ id: "route", hostname: "app.example.test", path_prefix: "/", version: 2, selected_release_operation_id: "prior" }],
  storage_route_continuity: { expected_loaded_generation: 4, expected_loaded_proxy_operation_id: "proxy_prior", routes: [{ route_id: "route", expected_version: 2, expected_selected_release_operation_id: "prior" }] },
};
function runningCompose(url: string) {
  if (url.endsWith("/stack-lifecycle")) return json({ resource_id: "r", resource_generation: 2, state: "running", deployed_operation_id: "prior" });
  if (url.endsWith("/storage-deployment-options")) return json(routedComposeOptions);
}
test.each(["replacement", "start"])("routed Git Compose %s freezes routes and stable port across response loss and refresh", async (mode) => {
  let writes = 0, refreshed = false;
  const fetch = composeBackend((url) => {
    if (url.endsWith("/stack-lifecycle") && mode === "start") return json({ resource_id: "r", resource_generation: 2, state: "stopped", deployed_operation_id: "prior", stop_operation_id: "stop" });
    if (url.endsWith("/storage-deployment-options")) return json({ ...routedComposeOptions, ...(mode === "start" ? { stopped_operation_id: "stop" } : {}), ...(refreshed ? { routes: [{ ...routedComposeOptions.routes[0]!, version: 3 }], storage_route_continuity: { ...routedComposeOptions.storage_route_continuity, expected_loaded_generation: 9, routes: [{ ...routedComposeOptions.storage_route_continuity.routes[0]!, expected_version: 3 }] } } : {}) });
    if (url.endsWith("/from-build")) {
      if (++writes === 1) throw new TypeError("lost");
      return json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, ...(mode === "start" ? { stack_stopped_operation_id: "stop" } : {}), storage_route_continuity: { hash: "continuity-hash" } } }, 202);
    }
    return runningCompose(url);
  });
  await setup(true, composeResource);
  await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  expect(screen.getByLabelText<HTMLSelectElement>("Build host port allocation").disabled).toBe(true);
  await composeSubmit(mode === "start" ? "Start selected Compose build" : "Deploy selected build");
  await screen.findByText(/Deployment delivery is unconfirmed/);
  refreshed = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh local deployment prerequisites" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([url]) => url.endsWith("/storage-deployment-options")).length).toBeGreaterThan(2));
  expect(screen.getByText("Keep HTTPS route app.example.test/ · version 2.")).toBeTruthy();
  expect(screen.queryByText("Keep HTTPS route app.example.test/ · version 3.")).toBeNull();
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const requests = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"));
  expect(requests).toHaveLength(2); expect(requests[1]![1].body).toBe(requests[0]![1].body);
  expect(JSON.parse(String(requests[0]![1].body))).toMatchObject({ host_port: 30000, storage_route_continuity: routedComposeOptions.storage_route_continuity, managed_stack: { acknowledge_interruption: true, ...(mode === "start" ? { expected_stopped_operation_id: "stop" } : {}) } });
});
test("route changes during admission preparation require refreshed review before any deployment", async () => {
  let reads = 0;
  const fetch = composeBackend(url => url.endsWith("/storage-deployment-options") ? json({ ...routedComposeOptions, ...(++reads > 1 ? { storage_route_continuity: { ...routedComposeOptions.storage_route_continuity, expected_loaded_generation: 5 } } : {}) }) : runningCompose(url));
  await setup(true, composeResource); await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  await composeSubmit(); await screen.findByText(/Unable to verify Compose deployment settings and routes/);
  expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful Compose build/).checked).toBe(false);
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test("route prerequisite permission denial clears the deployment intent", async () => {
  const fetch = composeBackend(url => url.endsWith("/storage-deployment-options") ? json({}, 403) : runningCompose(url));
  await setup(true, composeResource); await screen.findByText(/Local deployment access is pending or unavailable/);
  expect(screen.queryByRole("form", { name: "Deploy selected build" })).toBeNull();
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test.each(["missing-continuity", "wrong-port", "wrong-readiness"])("routed replacement rejects %s receipt and retains exact request", async (mismatch) => {
  const fetch = composeBackend(url => url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, ...(mismatch !== "missing-continuity" ? { storage_route_continuity: { hash: "continuity" } } : {}), ...(mismatch === "wrong-port" ? { host_port: 31000 } : {}), ...(mismatch === "wrong-readiness" ? { readiness: { path: "/other", timeout_seconds: 30 } } : {}) } }, 202) : runningCompose(url));
  await setup(true, composeResource); await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  await composeSubmit(); await screen.findByText(/Deployment delivery is unconfirmed/);
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical local build deployment" }).disabled).toBe(false);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(1);
});
test("routed replacement retains an explicitly reviewed failing readiness path", async () => {
  const fetch = composeBackend((url, init) => url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, readiness: JSON.parse(String(init.body)).readiness, storage_route_continuity: { hash: "continuity" } } }, 202) : runningCompose(url));
  await setup(true, composeResource); await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  fireEvent.change(screen.getByLabelText("Build readiness path"), { target: { value: "/not-ready" } });
  await composeSubmit(); await screen.findByRole("region", { name: "Deployment operation detail" });
  const body = JSON.parse(String(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body));
  expect(body).toMatchObject({ readiness: { path: "/not-ready", timeout_seconds: 30 }, storage_route_continuity: routedComposeOptions.storage_route_continuity });
});

test("new Git Compose build keeps historical routed stack proof after desired source edits", async () => {
  const nextSource = { ...composeSource, generation: 4 };
  const latest: Resource = { ...composeResource, generation: 3, source: { kind: "git", git: nextSource } };
  const nextBuild = { ...composeBuild, snapshot: { ...composeBuild.snapshot, resource_generation: 3, source: nextSource } };
  const fetch = composeBackend(url => {
    if (url.includes("/hosting-builds/")) return json({ build: nextBuild });
    if (url.endsWith("/stack-lifecycle")) return json({ resource_id: "r", resource_generation: 3, state: "running", deployed_operation_id: "prior" });
    if (url.endsWith("/storage-deployment-options")) return json({ ...routedComposeOptions, resource_generation: 3 });
    if (url.endsWith("/from-build")) return json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, resource_generation: 3, stack: { ...composeOperation.snapshot.stack, generation: 3 }, git_compose: { ...composeOperation.snapshot.git_compose, source_generation: 4 }, storage_route_continuity: { hash: "continuity" } } }, 202);
  });
  await setup(true, latest); await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  await composeSubmit(); await screen.findByRole("region", { name: "Deployment operation detail" });
  expect(JSON.parse(String(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body))).toMatchObject({ expected_generation: 3, expected_source_generation: 4, storage_route_continuity: routedComposeOptions.storage_route_continuity, host_port: 30000 });
});

function registryMetadata(url: string) {
  if (!url.includes("managed-secrets")) return undefined;
  return json({ items: url.includes("versions") ? [{ secret_id: "credential", version: 1 }, { secret_id: "credential", version: 2 }] : [{ id: "credential", name: "Registry access", project_id: "p", status: "active" }, { id: "foreign", name: "Foreign", project_id: "other", status: "active" }, { id: "revoked", name: "Revoked", project_id: "p", status: "revoked" }], limit: 20, offset: 0, count: 2, total: 2 });
}
async function chooseGitComposeRegistry() {
  await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Image access").disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByRole("option", { name: "Registry access · credential" });
  expect(screen.queryByRole("option", { name: "Foreign · foreign" })).toBeNull();
  expect(screen.queryByRole("option", { name: "Revoked · revoked" })).toBeNull();
  fireEvent.change(screen.getByLabelText("Registry credential secret"), { target: { value: "credential" } });
  await screen.findByRole("option", { name: "Version 1" });
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "1" } });
  await screen.findByText("Image access: Registry credential credential, version 1.");
}
test.each(["initial", "replacement", "start"])("private Git Compose %s freezes exact credential with build, routes and stopped reference", async (mode) => {
  let writes = 0;
  const fetch = composeBackend((url) => {
    const metadata = registryMetadata(url); if (metadata) return metadata;
    if (mode === "start" && url.endsWith("/stack-lifecycle")) return json({ resource_id: "r", resource_generation: 2, state: "stopped", deployed_operation_id: "prior", stop_operation_id: "stop" });
    if (mode === "start" && url.endsWith("/storage-deployment-options")) return json({ ...routedComposeOptions, stopped_operation_id: "stop" });
    if (url.endsWith("/from-build")) {
      if (++writes === 1) throw new TypeError("lost receipt");
      return json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, registry_credential: { secret_id: "credential", version: 1 }, ...(mode !== "initial" ? { storage_route_continuity: { hash: "continuity" } } : {}), ...(mode === "start" ? { stack_stopped_operation_id: "stop" } : {}) } }, 202);
    }
    return mode === "initial" ? undefined : runningCompose(url);
  });
  await setup(true, composeResource); await chooseGitComposeRegistry();
  await composeSubmit(mode === "start" ? "Start selected Compose build" : "Deploy selected build");
  await screen.findByText(/Deployment delivery is unconfirmed/);
  expect(screen.getByLabelText<HTMLSelectElement>("Registry credential version").disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const requests = fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"));
  expect(requests).toHaveLength(2); expect(requests[1]![1].body).toBe(requests[0]![1].body);
  const input = JSON.parse(String(requests[0]![1].body));
  expect(input).toMatchObject({ build_id: "b", expected_generation: 2, expected_source_generation: 3, registry_credential: { secret_id: "credential", version: 1 } });
  expect(input).not.toHaveProperty("grant_id"); expect(input).not.toHaveProperty("password");
  if (mode !== "initial") expect(input).toMatchObject({ host_port: 30000, storage_route_continuity: routedComposeOptions.storage_route_continuity });
  if (mode === "start") expect(input.managed_stack.expected_stopped_operation_id).toBe("stop");
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
});
test("private Git Compose requires explicit version and credential changes clear acknowledgment", async () => {
  const fetch = composeBackend(registryMetadata); await setup(true, composeResource);
  await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Image access").disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
  await chooseGitComposeRegistry();
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "2" } });
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful Compose build/).checked).toBe(false));
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test.each([undefined, { secret_id: "other", version: 1 }, { secret_id: "credential", version: 2 }])("Git Compose rejects substituted registry receipt %j", async (registry_credential) => {
  composeBackend(url => registryMetadata(url) ?? (url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, registry_credential } }, 202) : undefined));
  await setup(true, composeResource); await chooseGitComposeRegistry(); await composeSubmit();
  await screen.findByText(/Deployment delivery is unconfirmed/);
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});
test("registry metadata denial clears Git Compose deployment controls", async () => {
  const fetch = composeBackend(url => url.includes("managed-secrets") ? json({}, 403) : undefined);
  await setup(true, composeResource); await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Image access").disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByText(/Local deployment access is pending or unavailable/);
  expect(screen.queryByRole("form", { name: "Deploy selected build" })).toBeNull();
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test("ordinary local artifact deployment has no registry credential control", async () => {
  const fetch = backend(); await setup();
  expect(screen.queryByLabelText("Image access")).toBeNull(); await submit();
  await screen.findByRole("region", { name: "Deployment operation detail" });
  expect(JSON.parse(String(fetch.mock.calls.find(([url]) => url.endsWith("/from-build"))![1].body))).not.toHaveProperty("registry_credential");
});

test("withdrawn registry version clears acknowledgment without selecting another version", async () => {
  let withdrawn = false;
  const fetch = composeBackend(url => withdrawn && url.includes("managed-secrets") && url.includes("versions") ? json({ items: [{ secret_id: "credential", version: 2 }], limit: 20, offset: 0, count: 1, total: 1 }) : registryMetadata(url));
  await setup(true, composeResource); await chooseGitComposeRegistry();
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
  withdrawn = true; fireEvent.click(screen.getByRole("button", { name: "Refresh registry credential metadata" }));
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact successful Compose build/).checked).toBe(false));
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
  expect(screen.getByLabelText<HTMLSelectElement>("Registry credential version").value).not.toBe("2");
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});
test("public Git Compose cannot accept an unexpected credential in its receipt", async () => {
  composeBackend(url => url.endsWith("/from-build") ? json({ ...composeOperation, snapshot: { ...composeOperation.snapshot, registry_credential: { secret_id: "unexpected", version: 1 } } }, 202) : undefined);
  await setup(true, composeResource); await composeSubmit(); await screen.findByText(/Deployment delivery is unconfirmed/);
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});

test("mounting routed operation continuity observes parent authority without retiring the selected deployment", async () => {
  let memberReads = 0, continuityReads = 0;
  const fetch = composeBackend(url => {
    if (url.includes("/project-members?")) { memberReads++; return new Promise<Response>(resolve => setTimeout(() => resolve(json({ items: [{ project_id: "p", user_id: "u", role: "maintainer" }], count: 1, total: 1, offset: 0 })), 30)); }
    if (url.endsWith("/storage-route-continuity/hop_local")) { continuityReads++; return json({ resource_id: "r", operation_id: "hop_local", state: "unresolved", reason: "reconciliation_required" }); }
    if (url.endsWith("/from-build") || url.endsWith("/hosting-operations/hop_local")) return json({ ...composeOperation, state: "succeeded", mutation_hold: false, restore_ownership_held: false, reconcile_requested: false, complete_receipt: { id: "complete_hop_local", body_hash: `sha256:${"d".repeat(64)}`, created_at: "" }, result: { outcome: "succeeded", ready: true, uncertain: false, restored: false }, snapshot: { ...composeOperation.snapshot, storage_route_continuity: { hash: `sha256:${"e".repeat(64)}` } } }, url.endsWith("/from-build") ? 202 : 200);
    return runningCompose(url);
  });
  await setup(true, composeResource);
  // The deployment parent has a fresh ten-second authority view. A newly
  // mounted descendant must not introduce a zero-staleness membership fetch.
  client.setQueryDefaults(queryKeys.allProjectMembers, { staleTime: 0 });
  await screen.findByText("Keep HTTPS route app.example.test/ · version 2.");
  await composeSubmit();
  await waitFor(() => expect(continuityReads).toBeGreaterThan(0));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect.soft(memberReads).toBe(0);
  expect(screen.getByRole("region", { name: "Deployment operation detail" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Refresh operation" })).toBeTruthy();
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/from-build"))).toHaveLength(1);
});

test("editing only saved Compose build variables requires a new build before deployment", async () => {
 const latest: Resource = { ...composeResource, generation: 3, source: { kind: "git", git: { ...composeSource, generation: 4, settings: { ...composeSource.settings, build_variables: [{ name: "MODE", value: "changed" }] } } } };
 const fetch = composeBackend(); await setup(true, latest);
 fireEvent.click(screen.getByLabelText(/I reviewed this exact successful Compose build/));
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
 expect(screen.getByText(/A new build is required for the current configuration/)).toBeTruthy();
 fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
 expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});

test("a Dockerfile variable or injection edit requires a new build before deployment", async () => {
 const latest: Resource = { ...resource, generation: 3, source: { kind: "git", git: { ...source!, generation: 4, settings: { strategy: "dockerfile", inject_build_args_to_dockerfile: false, build_variables: [{ name: "MODE", value: "changed" }] } } } };
 const fetch = backend(); await setup(true, latest);
 fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/));
 expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deploy selected build" }).disabled).toBe(true);
 expect(screen.getByText(/A new build is required for the current configuration/)).toBeTruthy();
 fireEvent.submit(screen.getByRole("form", { name: "Deploy selected build" }));
 expect(fetch.mock.calls.some(([url]) => url.endsWith("/from-build"))).toBe(false);
});

test.each(["current", "changed hash", "Git build"])("stored Dockerfile %s deployment verifies real source identity and permits port override", async mode => {
 const content_hash=`sha256:${"a".repeat(64)}`;
 const stored: Resource={...resource,source:{kind:"dockerfile",dockerfile:{source_id:"s",generation:3,content_hash:mode==="changed hash" ? `sha256:${"b".repeat(64)}` : content_hash,content:"FROM nginx\nEXPOSE 8081/tcp\n",settings:{strategy:"dockerfile"}}}};
 const storedBuild: HostingBuild=mode==="Git build" ? build : {...build,snapshot:{...build.snapshot,source:{kind:"dockerfile",source_id:"s",generation:3,content_hash,settings:{strategy:"dockerfile"}}}};
 const fetch=backend((url)=>url.includes("/builds?") ? json({items:[storedBuild],count:1,total:1,limit:20,offset:0}) : url.includes("/hosting-builds/") ? json({build:storedBuild}) : undefined);
 await setup(true,stored);
 expect(screen.getByLabelText<HTMLInputElement>("Build container port").value).toBe("8081");
 if (mode!=="current") {
  fireEvent.click(screen.getByLabelText(/I reviewed this exact successful build/));
  expect(screen.getByRole<HTMLButtonElement>("button",{name:"Deploy selected build"}).disabled).toBe(true);
  expect(fetch.mock.calls.filter(([url])=>url.endsWith("/from-build"))).toHaveLength(0);
  return;
 }
 fireEvent.change(screen.getByLabelText("Build container port"),{target:{value:"9090"}});
 await submit(); await screen.findByRole("region",{name:"Deployment operation detail"});
 const writes=fetch.mock.calls.filter(([url])=>url.endsWith("/from-build"));
 expect(writes).toHaveLength(1); expect(JSON.parse(writes[0]![1].body as string)).toMatchObject({expected_generation:2,expected_source_generation:3,container_port:9090});
});
