import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { buildKeys, type HostingBuild } from "@/api/hosting-builds";
import { ResourceBuilds } from "./ResourceBuilds";
const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/example/web.git", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } }, generation: 2, created_by: "user", created_at: "", updated_at: "" };
const build: HostingBuild = { id: "build-one", project_id: "project", resource_id: "resource", state: "queued", revision: 1, created_by: "user", created_at: "2026-10-22T10:00:00Z", updated_at: "", snapshot: { resource_generation: 2, source: { source_id: "source", generation: 3, repository_url: "https://github.com/example/web.git", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } }, target: { binding_id: "binding", destination_id: "dest", server_id: "server", runner_id: "runner" } } };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const pushPolicy = { id: "", can_manage: false, resource_id: "resource", project_id: "project", generation: 0, resource_generation: 2, source_generation: 3, enabled: false, status: "unconfigured", reason: "unverified", repository_id: 0, repository: "example/web", branch: "main", desired_commit: "", last_build_id: "", callback_url: "", provider_read_configured: false };
const pushMetadata = (url: string) => url.includes("/github-push-policy/deliveries?") ? json({ items: [], next_cursor: "", has_more: false }) : url.endsWith("/github-push-policy") ? json(pushPolicy) : undefined;
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(writable = true) { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles: [] }); client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "user", role: "maintainer" }]); return render(<QueryClientProvider client={client}><ResourceBuilds resource={resource} writable={writable} blocked={false} /></QueryClientProvider>); }
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response | undefined> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? pushMetadata(url) ?? (url.endsWith("/binding") ? json({ id: "binding", resource_id: "resource" }) : url.includes("/builds?") ? json({ items: [build], limit: 20, offset: Number(new URL(url, "http://local").searchParams.get("offset")), count: 1, total: 21 }) : url.includes("/hosting-builds/") || options.method === "POST" ? json({ build }) : json({ ...resource, generation: 4, source: { ...resource.source, git: { ...(resource.source.kind === "git" ? resource.source.git : {}), generation: 5, requested_ref: "next" } } })));
  vi.stubGlobal("fetch", fetch); return fetch;
}
async function request() { fireEvent.click(screen.getByLabelText(/I reviewed this exact public Git source/)); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request public Git build" }).disabled).toBe(false)); fireEvent.click(screen.getByRole("button", { name: "Request public Git build" })); }
test("viewer reads history and detail without action controls or deployment claim", async () => { backend(); setup(false); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); await screen.findByRole("region", { name: "Build operation detail" }); expect(screen.queryByLabelText(/I confirm cancellation/)).toBeNull(); expect(screen.queryByRole("button", { name: "Request public Git build" })).toBeNull(); expect(await screen.findByRole("button", { name: "Show build output" })).toBeTruthy(); });
test.each(["lost", "503"])("%s keeps identical generations/key and does not fabricate success", async (failure) => { let count = 0; const fetch = backend((url, options) => options.method === "POST" && url.endsWith("/builds") ? ++count === 1 ? failure === "lost" ? Promise.reject(new TypeError("lost")) : json({}, 503) : json({ build }, 202) : undefined); setup(); await screen.findByText("Reviewed source:", { exact: false }); await request(); fireEvent.click(await screen.findByRole("button", { name: "Retry identical build request" })); await screen.findByRole("region", { name: "Build operation detail" }); const writes = fetch.mock.calls.filter(([, o]) => o.method === "POST"); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body); expect(JSON.parse(writes[0]![1].body as string)).toMatchObject({ expected_resource_generation: 2, expected_source_generation: 3 }); });
test("generation conflict refetches source and requires explicit review and new acknowledgment", async () => { let count = 0; const fetch = backend((url, options) => options.method === "POST" ? ++count === 1 ? json({}, 409) : json({ build }, 202) : undefined); setup(); await request(); await screen.findByText(/Current saved configuration: resource generation 4/); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical build request" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(1); fireEvent.click(screen.getByRole("button", { name: "Use reviewed source for a separate build" })); expect(screen.getByLabelText<HTMLInputElement>(/I reviewed this exact/).checked).toBe(false); await request(); await screen.findByRole("region", { name: "Build operation detail" }); const inputs = fetch.mock.calls.filter(([, o]) => o.method === "POST").map(([, o]) => JSON.parse(o.body as string)); expect(inputs[1]).toMatchObject({ expected_resource_generation: 4, expected_source_generation: 5 }); expect(inputs[1].idempotency_key).not.toBe(inputs[0].idempotency_key); });
test("newer resource props cannot silently change an acknowledged build source", async () => { const fetch = backend(); const view = setup(); await screen.findByText(/Reviewed source:/); fireEvent.click(screen.getByLabelText(/I reviewed this exact/)); view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={{ ...resource, generation: 3 }} writable blocked={false} /></QueryClientProvider>); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request public Git build" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(0); });
test.each(["principal", "permission", "resource"])("late response and frozen intent do not survive %s changes", async (scope) => { let resolve!: (response: Response) => void; backend((url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined); const view = setup(); await request(); await waitFor(() => expect(resolve).toBeTypeOf("function")); if (scope === "principal") { await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); }); await waitFor(() => expect(screen.queryByRole("button", { name: "Retry identical build request" })).toBeNull()); } else view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={scope === "resource" ? { ...resource, id: "other" } : resource} writable={scope !== "permission"} blocked={false} /></QueryClientProvider>); await act(async () => { resolve(json({ build }, 202)); }); expect(screen.queryByRole("region", { name: "Build operation detail" })).toBeNull(); });
test("cancellation freezes the confirmed revision across polling and conflict needs explicit reconfirmation", async () => { const fetch = backend((url, options) => url.endsWith("/cancel") ? json({}, 409) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); fireEvent.click(await screen.findByLabelText(/I confirm cancellation/)); await act(async () => { client.setQueryData(buildKeys.detail(JSON.stringify(["oidc", "user"]), "project", "resource", build.id), { ...build, revision: 2 }); }); fireEvent.click(screen.getByRole("button", { name: "Request build cancellation" })); await screen.findByText(/Cancellation revision changed/); expect(screen.getByRole("alert").textContent).toMatch(/Cancellation conflicts with the retained build revision/); expect(JSON.parse(fetch.mock.calls.find(([url]) => url.endsWith("/cancel"))![1].body as string)).toEqual({ expected_revision: 1 }); fireEvent.click(screen.getByRole("button", { name: "Review refreshed cancellation revision" })); expect(screen.getByLabelText<HTMLInputElement>(/I confirm cancellation/).checked).toBe(false); });
test("history pagination is bounded and read errors disable mutations", async () => { const fetch = backend((url) => url.includes("offset=20") ? json({}, 503) : undefined); setup(); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next build history" }).disabled).toBe(false)); fireEvent.click(screen.getByRole("button", { name: "Next build history" })); await screen.findByText(/Unable to refresh build history/); expect(fetch.mock.calls.some(([url]) => url.includes("limit=20&offset=20"))).toBe(true); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request public Git build" }).disabled).toBe(true); });
test("success and cleanup states describe only returned artifact/cleanup metadata", async () => { backend((url) => url.includes("/hosting-builds/") ? json({ build: { ...build, state: "succeeded", source_receipt: { commit: "commit", tree_digest: "tree" }, artifact: { origin: "local_build", image_id: "image", index_digest: "index", manifest_digest: "manifest", config_digest: "config", platform: "linux/amd64" } } }) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); await screen.findByText("Daemon image ID"); expect(screen.getByText(/does not mean the application is deployed or running/)).toBeTruthy(); expect(await screen.findByRole("form", { name: "Deploy selected build" })).toBeTruthy(); });
test("image resources do not mount build controls or fetch", () => { const fetch = backend(); const view = setup(); view.unmount(); fetch.mockClear(); render(<QueryClientProvider client={client}><ResourceBuilds resource={{ ...resource, source: { kind: "image", image: { reference: "web:1" } } }} writable blocked={false} /></QueryClientProvider>); expect(fetch).not.toHaveBeenCalled(); });
test("uncertain cancellation keeps confirmation and revision frozen across polling", async () => {
  let count = 0; const fetch = backend((url) => url.endsWith("/cancel") ? ++count === 1 ? json({}, 503) : json({ build: { ...build, state: "canceled" } }) : count > 1 && url.includes("/hosting-builds/") ? json({ build: { ...build, state: "canceled" } }) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); fireEvent.click(await screen.findByLabelText(/I confirm cancellation/)); fireEvent.click(screen.getByRole("button", { name: "Request build cancellation" })); await screen.findByRole("button", { name: "Retry cancellation at retained revision" });
  await act(async () => { client.setQueryData(buildKeys.detail(JSON.stringify(["oidc", "user"]), "project", "resource", build.id), { ...build, revision: 2 }); });
  expect(screen.getByLabelText<HTMLInputElement>(/I confirm cancellation/).disabled).toBe(true); fireEvent.click(screen.getByRole("button", { name: "Retry cancellation at retained revision" })); await screen.findByText("Canceled"); const writes = fetch.mock.calls.filter(([url]) => url.endsWith("/cancel")); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("permission denial pauses new requests and cancellation until access is refreshed", async () => { backend((url) => url.endsWith("/cancel") ? json({}, 403) : undefined); setup(); await screen.findByText(/Reviewed source:/); fireEvent.click(screen.getByLabelText(/I reviewed this exact/)); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); fireEvent.click(await screen.findByLabelText(/I confirm cancellation/)); fireEvent.click(screen.getByRole("button", { name: "Request build cancellation" })); await screen.findByText(/Build permission denied/); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request public Git build" }).disabled).toBe(true); });

test("only scoped build detail mounts output and parent blocking clears plaintext", async () => {
  const fetch = backend((url) => url.includes("/logs?") ? json({ items: [{ sequence: 1, component: "build", stream: "system", message: "persisted scoped output", created_at: "" }], next_sequence: 1, earliest_available_sequence: 1, deleted_through_sequence: 0, terminal: true, truncated: false, gap: false, history_complete: true }) : undefined);
  const view = setup(false); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" }));
  fireEvent.click(await screen.findByRole("button", { name: "Show build output" })); await screen.findByText("persisted scoped output");
  expect(client.getQueryCache().getAll().some((query) => JSON.stringify(query.state.data)?.includes("persisted scoped output"))).toBe(false);
  view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={resource} writable={false} blocked /></QueryClientProvider>);
  expect(screen.queryByText("persisted scoped output")).toBeNull(); expect(fetch.mock.calls.filter(([url]) => url.includes("/logs?"))).toHaveLength(1);
});
test("foreign build detail never offers output", async () => {
  const fetch = backend((url) => url.includes("/hosting-builds/") ? json({ build: { ...build, resource_id: "foreign" } }) : undefined); setup(false);
  fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); await screen.findByText(/Unable to refresh build detail/);
  expect(screen.queryByRole("button", { name: "Show build output" })).toBeNull(); expect(fetch.mock.calls.some(([url]) => url.includes("/logs?"))).toBe(false);
});

const privateResource: Resource = { ...resource, source: { kind: "git", git: { ...(resource.source.kind === "git" ? resource.source.git : {}), credential_kind: "https_basic" } } } as Resource;
const privateGrant = { id: "grant-one", resource_id: "resource", source_id: "source", source_generation: 3, version: 1, kind: "https_basic", revoked: false };
function privateBackend(handler?: (url: string, options: RequestInit) => Response | Promise<Response | undefined> | undefined) {
 return backend(async (url, options) => await handler?.(url, options) ?? (url.includes("/source-grants?") ? json({ items: [privateGrant, { ...privateGrant, id: "legacy", source_generation: 0 }, { ...privateGrant, id: "revoked", revoked: true }], count: 3, total: 3, limit: 20, offset: 0 }) : url.includes("/project-members?") ? json({ items: [{ project_id: "project", user_id: "user", role: "maintainer" }], count: 1, total: 1, limit: 100, offset: 0 }) : undefined));
}
function setupPrivate(writable = true) { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles: [] }); return render(<QueryClientProvider client={client}><ResourceBuilds resource={privateResource} writable={writable} blocked={false} /></QueryClientProvider>); }
async function privateReview() { await screen.findByRole("option", { name: "grant-one · source 3 · eligible" }); fireEvent.change(screen.getByLabelText("Private source grant"), { target: { value: "grant-one" } }); fireEvent.click(screen.getByLabelText(/I reviewed this exact private HTTPS Git source/)); }
test.each(["pending", "ready"])("external private grant becomes selectable after history refresh with %s access", async (access) => {
 let available = false;
 let resolveMembers!: (response: Response) => void;
 const fetch = privateBackend((url) => url.includes("/source-grants?") ? json({ items: available ? [privateGrant] : [], count: available ? 1 : 0, total: available ? 1 : 0, limit: 20, offset: 0 }) : url.includes("/project-members?") && access === "pending" ? new Promise<Response>((resolve) => { resolveMembers = resolve; }) : undefined);
 setupPrivate();
 const refresh = await screen.findByRole("button", { name: "Refresh build history" });
 if (access === "pending") {
  await waitFor(() => expect(resolveMembers).toBeTypeOf("function"));
  expect(fetch.mock.calls.filter(([url]) => url.includes("/source-grants?"))).toHaveLength(0);
 } else {
  await waitFor(() => expect(fetch.mock.calls.filter(([url]) => url.includes("/source-grants?"))).toHaveLength(1));
 }
 available = true;
 fireEvent.click(refresh);
 if (access === "pending") await act(async () => resolveMembers(json({ items: [{ project_id: "project", user_id: "user", role: "maintainer" }], count: 1, total: 1, limit: 100, offset: 0 })));
 await privateReview();
 await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request private Git build" }).disabled).toBe(false));
 expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
 expect(screen.getByLabelText<HTMLSelectElement>("Private source grant").value).toBe("grant-one");
});
test("history refresh during the initial grant request retains that request's empty result", async () => {
 let available = false;
 let resolveGrants!: (response: Response) => void;
 const fetch = privateBackend((url) => url.includes("/source-grants?") ? available ? json({ items: [privateGrant], count: 1, total: 1, limit: 20, offset: 0 }) : new Promise<Response>((resolve) => { resolveGrants = resolve; }) : undefined);
 setupPrivate();
 await waitFor(() => expect(resolveGrants).toBeTypeOf("function"));
 available = true;
 fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
 await act(async () => resolveGrants(json({ items: [], count: 0, total: 0, limit: 20, offset: 0 })));
 expect(fetch.mock.calls.filter(([url]) => url.includes("/source-grants?"))).toHaveLength(1);
 expect(screen.queryByRole("option", { name: "grant-one · source 3 · eligible" })).toBeNull();
 fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
 await privateReview();
 await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request private Git build" }).disabled).toBe(false));
});
test.each(["lost", "503"])("private %s retry freezes exact grant ID, key and generations", async (failure) => {
 let count = 0; const fetch = privateBackend((url, options) => url.endsWith("/builds") && options.method === "POST" ? ++count === 1 ? failure === "lost" ? Promise.reject(new TypeError("lost")) : json({}, 503) : json({ build }, 202) : undefined); const view = setupPrivate(); await privateReview();
 expect(screen.getByRole<HTMLOptionElement>("option", { name: "legacy · source 0 · ineligible" }).disabled).toBe(true); expect(screen.getByRole<HTMLOptionElement>("option", { name: "revoked · source 3 · revoked" }).disabled).toBe(true);
 fireEvent.click(screen.getByRole("button", { name: "Request private Git build" })); await screen.findByRole("button", { name: "Retry identical build request" });
 expect(screen.getByLabelText<HTMLSelectElement>("Private source grant").disabled).toBe(true);
 view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={{ ...privateResource, generation: 4, source: { kind: "git", git: { ...(privateResource.source.kind === "git" ? privateResource.source.git : {}), generation: 5 } } } as Resource} writable blocked={false} /></QueryClientProvider>);
 fireEvent.click(screen.getByRole("button", { name: "Retry identical build request" })); await screen.findByRole("region", { name: "Build operation detail" }); const writes = fetch.mock.calls.filter(([url, options]) => url.endsWith("/builds") && options.method === "POST"); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body); expect(JSON.parse(writes[0]![1].body as string)).toMatchObject({ grant_id: "grant-one", expected_resource_generation: 2, expected_source_generation: 3 });
});
test("a revoked selected grant blocks a new private request and viewer reads skip grant endpoints", async () => {
 const fetch = privateBackend(); const view = setupPrivate(); await privateReview();
 await act(async () => client.setQueryData(["hostingSourceGrants", JSON.stringify(["oidc", "user"]), "project", "resource", 0], { items: [{ ...privateGrant, revoked: true }], limit: 20, offset: 0, count: 1, total: 1 }));
 await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request private Git build" }).disabled).toBe(true)); expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
 view.unmount(); fetch.mockClear(); setupPrivate(false); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); await screen.findByRole("button", { name: "Show build output" }); expect(fetch.mock.calls.some(([url]) => url.includes("/source-grants") || url.includes("/managed-secrets"))).toBe(false);
});

test("viewer history refresh never fetches private source grants", async () => {
 const fetch = privateBackend(); setupPrivate(false); await screen.findByRole("button", { name: "View build build-one" });
 const historyReads = () => fetch.mock.calls.filter(([url]) => url.includes("/builds?")).length;
 const before = historyReads(); fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
 await waitFor(() => expect(historyReads()).toBe(before + 1));
 expect(fetch.mock.calls.some(([url]) => url.includes("/source-grants") || url.includes("/managed-secrets"))).toBe(false);
});
test.each([401, 403, 404])("grant read %s locks manual history refresh until access reset", async (status) => {
 const fetch = privateBackend((url) => url.includes("/source-grants?") ? json({}, status) : undefined); setupPrivate();
 const refresh = await screen.findByRole<HTMLButtonElement>("button", { name: "Refresh build history" });
 await waitFor(() => expect(refresh.disabled).toBe(true)); const calls = fetch.mock.calls.length;
 fireEvent.click(refresh); await act(async () => {}); expect(fetch.mock.calls).toHaveLength(calls);
});
test("blocked private history refresh cannot manually enter disabled grant queries", async () => {
 const fetch = privateBackend(); const view = setupPrivate(); await privateReview();
 view.rerender(<QueryClientProvider client={client}><ResourceBuilds resource={privateResource} writable blocked /></QueryClientProvider>);
 const calls = fetch.mock.calls.length; const refresh = screen.getByRole<HTMLButtonElement>("button", { name: "Refresh build history" }); expect(refresh.disabled).toBe(true); fireEvent.click(refresh); await act(async () => {}); expect(fetch.mock.calls).toHaveLength(calls);
});
test("private sourcing state uses a source-neutral label in history and detail", async () => {
 privateBackend((url) => url.includes("/builds?") || url.includes("/hosting-builds/") ? url.includes("/builds?") ? json({ items: [{ ...build, state: "sourcing" }], count: 1, total: 1, limit: 20, offset: 0 }) : json({ build: { ...build, state: "sourcing", snapshot: { ...build.snapshot, source: { ...build.snapshot.source, credential_kind: "https_basic" } } } }) : undefined); setupPrivate();
 fireEvent.click(await screen.findByRole("button", { name: "View build build-one" })); await screen.findByText("Acquiring source", { exact: true }); expect(screen.queryByText(/Acquiring public source/)).toBeNull();
});


test.each(["503", "viewer"])("parent and child share verified grant access through cached owner %s", async (result) => {
  let phase = "owner";
  let resolve!: (response: Response) => void;
  const membership = (role: string) => json({ items: [{ project_id: "project", user_id: "user", role }], limit: 100, offset: 0, count: 1, total: 1 });
  const fetch = privateBackend((url) => {
    if (url.includes("/project-members?")) return phase === "pending" ? new Promise<Response>((done) => { resolve = done; }) : membership("owner");
    if (url.includes("/managed-secrets?")) return json({ items: [], limit: 20, offset: 0, count: 0, total: 0 });
    return undefined;
  });
  setupPrivate();
  await screen.findByRole("form", { name: "Request private source grant" });
  await screen.findByRole("option", { name: /grant-one · source 3 · eligible/ });
  const count = () => fetch.mock.calls.filter(([url]) => url.includes("/source-grants?")).length;
  const before = count();
  phase = "pending";
  let verification!: Promise<void>;
  act(() => { verification = client.refetchQueries({ queryKey: ["sourceGrantOwners", JSON.stringify(["oidc", "user"]), "project"] }); });
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Create source grant" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
  await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); });
  expect(count()).toBe(before);
  await act(async () => { resolve(result === "503" ? json({}, 503) : membership("viewer")); await verification; });
  if (result === "503") await screen.findByText(/Unable to verify source grant access/);
  else await screen.findByText(/Project maintainer access is required to list source grants/);
  fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
  await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true); });
  expect(count()).toBe(before);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request private Git build" }).disabled).toBe(true);
  if (result === "viewer") expect(screen.queryByRole("form", { name: "Request private source grant" })).toBeNull();
  phase = "owner";
  fireEvent.click(screen.getByRole("button", { name: "Refresh source grant access" }));
  await screen.findByRole("form", { name: "Request private source grant" });
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Refresh source grants" }).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Refresh build history" }));
  await waitFor(() => expect(count()).toBeGreaterThan(before));
  focusManager.setFocused(undefined);
});

test("manual build maintainer access does not create push owner authority", async () => {
 const fetch = backend(); setup(true); await screen.findByRole("region", { name: "Build on push" }); await screen.findByText(/Maintainer build access does not grant policy management/); expect(screen.queryByRole("form", { name: "Configure GitHub build on push" })).toBeNull(); expect(fetch.mock.calls.some(([url]) => url.includes("/service-accounts?") || url.includes("/managed-secrets?"))).toBe(false);
});

test("failed Git Compose build shows its exact path/ref/commit without presenting a deployable result", async () => {
  const failed: HostingBuild = { ...build, state: "failed", failure_code: "compose_parse_failed", snapshot: { ...build.snapshot, source: { ...build.snapshot.source, settings: { strategy: "compose", context_dir: "app", compose_file: "deploy/compose.yaml", public_component: "web", container_port: 3000 } } }, source_receipt: { commit: "failed-build-commit", tree_digest: "source-tree" } };
  backend(url => url.includes("/hosting-builds/") ? json({ build: failed }) : undefined);
  setup(); fireEvent.click(await screen.findByRole("button", { name: "View build build-one" }));
  await screen.findByText("compose_parse_failed");
  expect(screen.getByText("app/deploy/compose.yaml")).toBeTruthy();
  expect(screen.getByText("failed-build-commit")).toBeTruthy();
  expect(screen.getByText(/deployment history for the last recorded release/)).toBeTruthy();
  expect(screen.queryByRole("form", { name: "Deploy selected build" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Prepared Compose result" })).toBeNull();
});
