import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ApiError, queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import type { HostingOperation } from "@/api/hosting-deployments";
import { ResourceDeployments } from "./ResourceDeployments";

const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "team/web:1" } }, generation: 1, created_by: "user", created_at: "", updated_at: "" };
const binding = { id: "binding", resource_id: "resource", destination_id: "dest", runtime_namespace: "ncd_resource", created_by: "user", created_at: "" };
const operation: HostingOperation = { id: "hop_one", project_id: "project", resource_id: "resource", binding_id: "binding", state: "queued", snapshot: { resource_id: "resource", resource_generation: 1, image_reference: "team/web:1", binding_id: "binding", destination_id: "dest", server_id: "server", runner_id: "runner", runtime_namespace: "ncd_resource", container_port: 80, host_port: 8080, readiness: { path: "/", timeout_seconds: 30 }, policy_version: "public-image.loopback.v1" }, restore_ownership_held: false, mutation_hold: false, reconcile_requested: false, created_by: "user", created_at: "2026-10-22T10:00:00Z", updated_at: "2026-10-22T10:00:00Z" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const page = (items: HostingOperation[] = [], offset = 0, total = items.length) => ({ items, count: items.length, total, limit: 20, offset });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); vi.useRealTimers(); });
function setup(role = "maintainer", globalRoles: string[] = [], automatic = false, saved = resource) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(queryKeys.principal, { id: "user", name: "Operator", email: "operator@example.test", provider: "oidc", roles: globalRoles });
  client.setQueryData(queryKeys.allProjectMembers, role ? [{ id: "member", project_id: "project", user_id: "user", email: "operator@example.test", role, created_at: "" }] : []);
  const view = render(<QueryClientProvider client={client}><ResourceDeployments resource={saved} writable={["owner", "maintainer"].includes(role) || globalRoles.includes("system_admin")} blocked={false} /></QueryClientProvider>);
  const mode = screen.queryByLabelText("Host port allocation");
  if (mode && !automatic) fireEvent.change(mode, { target: { value: "fixed" } });
  return view;
}
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const response = await handler?.(url, options);
    if (response) return response;
    if (url.includes("/project-members")) return json({ items: client.getQueryData(queryKeys.allProjectMembers) ?? [], limit: 100, offset: 0, count: (client.getQueryData<unknown[]>(queryKeys.allProjectMembers) ?? []).length, total: (client.getQueryData<unknown[]>(queryKeys.allProjectMembers) ?? []).length });
    if (url.endsWith("/me")) return json(client.getQueryData(queryKeys.principal));
    if (url.endsWith("/storage-deployment-options")) return json({ resource_id: "resource", resource_generation: 1, storage_ref: null, previous_healthy_operation_id: null, host_port: null, container_port: null, readiness: null, available: false, reason: "storage_unconfigured", routes: [], storage_route_continuity: null });
    if (url.endsWith("/storage")) return json({ id: "storage", project_id: "project", resource_id: "resource", binding_id: "binding", generation: 1, hash: "hash", kind: "platform_managed", driver: "local", access_mode: "read_write", retention: "retain", volume_name: "ncd_uploads", mount_path: "/app/uploads" });
    if (url.endsWith("/runtime-configuration")) return json({ runtime_configuration: { resource_id: "resource", project_id: resource.project_id, resource_generation: 1, runtime_generation: 0, entries: [] } });
    if (url.endsWith("/binding")) return json(binding);
    if (url.includes("/retained-releases")) return json([]);
    if (url.includes("/deployments")) return options.method === "POST" ? json({ ...operation, snapshot: { ...operation.snapshot, ...(JSON.parse(options.body as string).expected_runtime_configuration_generation ? { runtime_configuration: { generation: JSON.parse(options.body as string).expected_runtime_configuration_generation, hash: "reference-hash" } } : {}) } }, 202) : json(page());
    if (url.includes("/hosting-operations/")) return json(operation);
    if (url === "/api/v1/resources/resource") return json({ ...resource, generation: 2 });
    throw new Error(`Unexpected test endpoint ${url}`);
  });
  vi.stubGlobal("fetch", fetch); return fetch;
}
async function request() {
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(false));
  fireEvent.submit(screen.getByRole("form", { name: "Deploy image for Web" }));
  await act(async () => { await Promise.resolve(); });
}
function acknowledge() { fireEvent.click(screen.getByLabelText(/I understand that/)); }
test("storage authority failure never falls back to stateless admission", async () => {
  const fetch = backend((url) => url.endsWith("/storage-deployment-options") ? json({ error: "authority unavailable" }, 503) : undefined);
  setup();
  await screen.findByText(/Storage deployment options are unavailable/);
  expect(screen.queryByRole("form", { name: "Deploy image for Web" })).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(0);
});
test("new storage created after opening a stateless draft requires storage review before any admission", async () => {
  let configured = false;
  const fetch = backend((url) => configured && url.endsWith("/storage-deployment-options") ? json({ resource_id: "resource", resource_generation: 1, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, previous_healthy_operation_id: null, host_port: null, container_port: null, readiness: null, available: false, reason: "no_previous_healthy", routes: [], storage_route_continuity: null }) : undefined);
  setup(); acknowledge();
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(false));
  configured = true;
  fireEvent.submit(screen.getByRole("form", { name: "Deploy image for Web" }));
  await screen.findByRole("region", { name: "Persistent upload deployment review" });
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(0);
});
test("configured storage uses server review and clears it on resource generation change", async () => {
  const options = { resource_id: "resource", resource_generation: 1, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null };
  const fetch = backend((url) => url.endsWith("/storage-deployment-options") ? json(options) : undefined);
  const view = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
  await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
  expect(screen.queryByLabelText("Host port allocation")).toBeNull();
  expect(screen.queryByRole("form", { name: "Deploy image for Web" })).toBeNull();
  view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={{ ...resource, generation: 2 }} writable blocked={false} /></QueryClientProvider>);
  expect(screen.queryByRole("button", { name: "Deploy reviewed image with uploads" })).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(0);
});
test("storage receipt retry survives history acquiring the admitted operation mutation hold", async () => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  const options = { resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null };
  const admitted = { ...operation, mutation_hold: true, snapshot: { ...operation.snapshot, storage } };
  let held = false, submissions = 0;
  const fetch = backend((url, init) => {
    if (url.endsWith("/stack-lifecycle")) return json({ resource_id: "resource", resource_generation: 1, state: "unavailable" });
    if (url.endsWith("/storage-deployment-options")) return json(options);
    if (url.endsWith("/deployments") && init.method === "POST") {
      if (++submissions === 1) { held = true; throw new TypeError("accepted response lost"); }
      return json(admitted, 202);
    }
    if (url.includes("/deployments") && init.method !== "POST") return json(page(held ? [admitted] : []));
    return undefined;
  });
  setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
  await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
  fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" }));
  await screen.findByRole("button", { name: "Retry identical storage deployment request" });
  fireEvent.click(screen.getByRole("button", { name: "Refresh deployment history" }));
  await screen.findByText(/New deployment requests are paused/);
  const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical storage deployment request" });
  expect(retry.disabled).toBe(false);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review current storage deployment" }).disabled).toBe(true);
  fireEvent.click(retry);
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("stateless accepted receipt remains selected when the shared history refresh rejects", async () => {
  const fetch = backend(); setup(); acknowledge();
  vi.spyOn(client, "invalidateQueries").mockRejectedValue(new Error("history refresh failed"));
  await request();
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  await screen.findByText(/was accepted, but history could not be refreshed/);
  expect(screen.getByLabelText<HTMLInputElement>("Container port").disabled).toBe(false);
  expect(screen.queryByText(/connection ended without a confirmed response/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry identical deployment request" })).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});
const refreshDenials = ["storage", "stateless"].flatMap((mode) => ["history", "retained"].flatMap((endpoint) => [401, 403, 404].map((status) => ({ mode, endpoint, status }))));
test.each(refreshDenials)("accepted $mode refresh preserves later denial $status after earlier $endpoint 503", async ({ mode, endpoint, status }) => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  let admitted = false;
  let completeDenial!: (response: Response) => void;
  const reads = { history: 0, retained: 0 };
  const fetch = backend((url, init) => {
    if (mode === "storage" && url.endsWith("/storage-deployment-options")) return json({ resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null });
    if (url.endsWith("/deployments") && init.method === "POST") { admitted = true; return json({ ...operation, snapshot: { ...operation.snapshot, ...(mode === "storage" ? { storage } : {}) } }, 202); }
    if (admitted && (url.includes("/deployments") && init.method !== "POST" || url.endsWith("/retained-releases"))) {
      const read = url.includes("/deployments") ? "history" : "retained";
      reads[read]++;
      if (read === endpoint) return json({ error: "ordinary first failure" }, 503);
      return new Promise<Response>((resolve) => { completeDenial = resolve; });
    }
    return undefined;
  });
  setup();
  if (mode === "storage") {
    fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
    await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
    fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
    fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" }));
  } else { acknowledge(); await request(); }
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  await waitFor(() => expect(reads).toEqual({ history: 1, retained: 1 }));
  await waitFor(() => expect(client.getQueryCache().getAll().some((query) => query.state.error instanceof ApiError && query.state.error.status === 503)).toBe(true));
  await act(async () => { completeDenial(json({ error: "later current denial" }, status)); });
  await screen.findByText(/Deployment permission is unavailable. Reload to verify current project access/);
  expect(screen.queryByRole("region", { name: "Image deployments for Web" })).toBeNull();
  expect(screen.queryByText(/Delivery is unconfirmed/)).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
  expect(reads).toEqual({ history: 1, retained: 1 });
});
test.each(["deadline", "actor", "resource"])("mixed refresh ignores late denial after %s retirement without extra reads", async (retirement) => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  let admitted = false;
  let completeDenial!: (response: Response) => void;
  let pendingSignal: AbortSignal | null | undefined;
  const reads = { history: 0, retained: 0 };
  const fetch = backend((url, init) => {
    if (url.endsWith("/storage-deployment-options")) return json({ resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null });
    if (url.endsWith("/deployments") && init.method === "POST") { admitted = true; return json({ ...operation, snapshot: { ...operation.snapshot, storage } }, 202); }
    if (admitted && url.includes("/deployments") && init.method !== "POST") { reads.history++; return json({ error: "first ordinary failure" }, 503); }
    if (admitted && url.endsWith("/retained-releases")) { reads.retained++; pendingSignal = init.signal; return new Promise<Response>((resolve) => { completeDenial = resolve; }); }
    return undefined;
  });
  const view = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
  await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
  fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
  vi.useFakeTimers();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" })); });
  expect(reads).toEqual({ history: 1, retained: 1 });
  if (retirement === "deadline") {
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(screen.getByText(/was accepted, but history could not be refreshed/)).toBeTruthy();
  }
  vi.useRealTimers();
  if (retirement === "resource") view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={{ ...resource, id: "other" }} writable blocked={false} /></QueryClientProvider>);
  if (retirement === "actor") await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); });
  // Actor query notifications can retire the keyed panel after the receipt text
  // is already absent. Wait for request retirement itself before the late reply.
  await waitFor(() => expect(pendingSignal?.aborted).toBe(true));
  if (retirement !== "deadline") expect(screen.queryByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeNull();
  await act(async () => { completeDenial(json({ error: "retired denial" }, 403)); });
  expect(screen.queryByText(/Deployment permission is unavailable. Reload to verify current project access/)).toBeNull();
  if (retirement === "deadline") expect(screen.getByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeTruthy();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
  if (retirement === "deadline") expect(reads).toEqual({ history: 1, retained: 1 });
});
test.each(refreshDenials)("accepted $mode POST retires protected panel after $endpoint refresh $status", async ({ mode, endpoint, status }) => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  const options = { resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null };
  let admitted = false;
  let siblingSignal: AbortSignal | null | undefined;
  let completeSibling!: (response: Response) => void;
  const fetch = backend((url, init) => {
    if (mode === "storage" && url.endsWith("/storage-deployment-options")) return json(options);
    if (url.endsWith("/deployments") && init.method === "POST") { admitted = true; return json({ ...operation, snapshot: { ...operation.snapshot, ...(mode === "storage" ? { storage } : {}) } }, 202); }
    if (admitted && (url.includes("/deployments") && init.method !== "POST" || url.endsWith("/retained-releases"))) {
      const history = url.includes("/deployments");
      if (history === (endpoint === "history")) return json({ error: "current scope denied" }, status);
      siblingSignal = init.signal;
      return new Promise<Response>((resolve) => { completeSibling = resolve; });
    }
    return undefined;
  });
  setup();
  if (mode === "storage") {
    fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
    await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
    fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
    fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" }));
  } else { acknowledge(); await request(); }
  await screen.findByText(/Deployment permission is unavailable. Reload to verify current project access/);
  expect(screen.queryByRole("region", { name: "Image deployments for Web" })).toBeNull();
  expect(screen.queryByText(/Delivery is unconfirmed/)).toBeNull();
  expect(siblingSignal?.aborted).toBe(true);
  await act(async () => { completeSibling(json(endpoint === "history" ? [] : page([operation]))); });
  expect(screen.getByText(/Deployment permission is unavailable. Reload to verify current project access/)).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Retry identical/ })).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});
test.each(["storage", "stateless"].flatMap((mode) => ["history", "retained"].map((endpoint) => ({ mode, endpoint }))))("accepted $mode receipt remains definitive after ordinary $endpoint refresh 503", async ({ mode, endpoint }) => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  let admitted = false;
  const fetch = backend((url, init) => {
    if (mode === "storage" && url.endsWith("/storage-deployment-options")) return json({ resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null });
    if (url.endsWith("/deployments") && init.method === "POST") { admitted = true; return json({ ...operation, snapshot: { ...operation.snapshot, ...(mode === "storage" ? { storage } : {}) } }, 202); }
    if (admitted && (endpoint === "history" ? url.includes("/deployments") && init.method !== "POST" : url.endsWith("/retained-releases"))) return json({ error: "temporarily unavailable" }, 503);
    return undefined;
  });
  setup();
  if (mode === "storage") {
    fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
    await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
    fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
    fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" }));
  } else { acknowledge(); await request(); }
  await screen.findByText(/was accepted, but history could not be refreshed/);
  expect(screen.getByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeTruthy();
  expect(screen.queryByText(/Deployment permission is unavailable. Reload to verify current project access/)).toBeNull();
  expect(screen.queryByText(/Delivery is unconfirmed/)).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});
test.each(["actor", "resource"])("accepted storage refresh times out and fences late reads after %s changes", async (change) => {
  const storage: NonNullable<HostingOperation["snapshot"]["storage"]> = { storage_id: "storage", generation: 1, hash: "hash" };
  const options = { resource_id: "resource", resource_generation: 1, storage_ref: storage, previous_healthy_operation_id: "old", host_port: 8080, container_port: 80, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [], storage_route_continuity: null };
  let admitted = false;
  let historySignal: AbortSignal | null | undefined;
  let resolve!: (response: Response) => void;
  const fetch = backend((url, init) => {
    if (url.endsWith("/storage-deployment-options")) return json(options);
    if (url.endsWith("/deployments") && init.method === "POST") { admitted = true; return json({ ...operation, snapshot: { ...operation.snapshot, storage } }, 202); }
    if (admitted && url.includes("/deployments") && init.method !== "POST") { historySignal = init.signal; return new Promise<Response>((done) => { resolve = done; }); }
    return undefined;
  });
  const view = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review current storage deployment" }));
  await screen.findByRole("button", { name: "Deploy reviewed image with uploads" });
  fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
  vi.useFakeTimers();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" })); });
  expect(screen.getByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
  expect(screen.getByText(/was accepted, but history could not be refreshed/)).toBeTruthy();
  expect(historySignal?.aborted).toBe(true);
  vi.useRealTimers();
  if (change === "resource") view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={{ ...resource, id: "other" }} writable blocked={false} /></QueryClientProvider>);
  else await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); });
  await waitFor(() => expect(screen.queryByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeNull());
  await act(async () => { resolve(json(page([{ ...operation, mutation_hold: true }]))); });
  expect(screen.queryByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeNull();
  expect(screen.queryByText(/was accepted, but history could not be refreshed/)).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});
test("viewer reads persisted history and results without request, cancel, or reconcile controls", async () => {
  backend((url) => url.includes("/deployments") ? json(page([{ ...operation, state: "succeeded", result: { outcome: "succeeded", ready: true, restored: false, uncertain: false } }])) : url.includes("/hosting-operations/") ? json({ ...operation, state: "succeeded", result: { outcome: "succeeded", ready: true, restored: false, uncertain: false } }) : undefined);
  setup("viewer"); fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" }));
  await screen.findByText("Readiness recorded at completion: passed.");
  expect(screen.queryByRole("form")).toBeNull();
  expect(screen.queryByRole("button", { name: "Request cancellation" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Request reconciliation" })).toBeNull();
  expect(screen.getAllByText(/not continuous health monitoring/).length).toBeGreaterThan(0);
});
test("explicit interruption acknowledgment submits the captured generation and scoped intent once", async () => {
  const fetch = backend(); setup();
  await screen.findByText(/Deploy saved image/);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(true);
  acknowledge(); await request();
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const calls = fetch.mock.calls.filter(([, options]) => options.method === "POST");
  expect(calls).toHaveLength(1);
  expect(JSON.parse(calls[0]![1].body as string)).toMatchObject({ expected_generation: 1, container_port: 80, host_port: 8080, readiness: { path: "/", timeout_seconds: 30 } });
  expect(JSON.parse(calls[0]![1].body as string).idempotency_key).toHaveLength(36);
  expect(screen.getByLabelText<HTMLInputElement>(/I understand that/).checked).toBe(false);
});
test.each(["disconnect", "503"])("%s retains exactly the same request body and key on explicit retry", async (failure) => {
  let count = 0;
  const fetch = backend((url, options) => {
    if (url.includes("/deployments") && options.method === "POST" && ++count === 1) {
      if (failure === "disconnect") throw new TypeError("connection lost");
      return json({ code: "unavailable", error: "unavailable" }, 503);
    }
  });
  setup(); acknowledge(); await request();
  await screen.findByRole("button", { name: "Retry identical deployment request" });
  expect(screen.getByLabelText<HTMLInputElement>("Runner-local host port").disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST");
  expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("rapid duplicate submissions cannot issue separate deployment intents", async () => {
  let resolve!: (response: Response) => void;
  const fetch = backend((url, options) => url.includes("/deployments") && options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  setup(); acknowledge();
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(false));
  const form = screen.getByRole("form", { name: "Deploy image for Web" });
  fireEvent.submit(form); fireEvent.submit(form);
  await waitFor(() => expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1));
  await act(async () => resolve(json(operation, 202)));
  await screen.findByRole("region", { name: "Deployment operation detail" });
});
test("409 preserves settings, refetches generation and history, then requires an explicit separate intent", async () => {
  let count = 0;
  const fetch = backend((url, options) => url.includes("/deployments") && options.method === "POST" && ++count === 1 ? json({ code: "conflict", error: "conflict" }, 409) : undefined);
  setup(); fireEvent.change(screen.getByLabelText("Runner-local host port"), { target: { value: "9080" } }); acknowledge(); await request();
  await screen.findByText(/Current saved configuration: generation 2/);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
  expect(screen.getByLabelText<HTMLInputElement>("Runner-local host port").value).toBe("9080");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical deployment request" }).disabled).toBe(true);
  fireEvent.click(screen.getByLabelText(/I reviewed history/)); fireEvent.click(screen.getByRole("button", { name: "Use reviewed configuration for a new request" }));
  expect(screen.getByLabelText<HTMLInputElement>(/I understand that/).checked).toBe(false);
  acknowledge(); await request(); await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST").map(([, options]) => JSON.parse(options.body as string));
  expect(writes[1]).toMatchObject({ expected_generation: 2, host_port: 9080 }); expect(writes[1].idempotency_key).not.toBe(writes[0].idempotency_key);
});
test("503 history refresh preserves form settings and blocks request until retry succeeds", async () => {
  let failing = false;
  backend((url) => url.includes("/deployments") && failing ? json({}, 503) : undefined); setup();
  await screen.findByText("No deployment operations recorded.");
  fireEvent.change(screen.getByLabelText("Readiness path"), { target: { value: "/health?ready=1" } }); acknowledge();
  failing = true; await act(async () => { await client.invalidateQueries({ queryKey: ["hostingDeployments"] }); });
  await screen.findByRole("button", { name: "Retry deployment information" });
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/health?ready=1");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(true);
  failing = false; fireEvent.click(screen.getByRole("button", { name: "Retry deployment information" }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(false));
});
test.each([["owner", [], true], ["maintainer", [], false], ["", ["system_admin"], true], ["", ["runner_admin"], false], ["", ["owner"], false]] as const)("reconciliation follows persisted role %s and exact global exception %j", async (role, roles, expected) => {
  backend((url) => url.includes("/deployments") ? json(page([operation])) : undefined); setup(role, [...roles]);
  fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" })); await screen.findByText("Exact binding");
  expect(Boolean(screen.queryByRole("button", { name: "Request reconciliation" }))).toBe(expected);
});
test("post-admission cancellation keeps uncertainty and mutation hold visible", async () => {
  let canceled = false;
  const applying: HostingOperation = { ...operation, state: "applying", mutation_hold: true };
  const fetch = backend((url) => url.endsWith("/cancel") ? (canceled = true, json({ ...applying, state: "cancel_requested" })) : url.includes("/hosting-operations/") ? json(canceled ? { ...applying, state: "cancel_requested" } : applying) : url.includes("/deployments") ? json(page([applying])) : undefined);
  setup(); fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" }));
  fireEvent.click(await screen.findByRole("button", { name: "Request cancellation" }));
  await screen.findByText("Cancellation requested after admission"); await screen.findByText(/not proof of rollback/); await screen.findByText(/Mutation hold retained/);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/cancel"))).toHaveLength(1);
});
test("owner reconciliation requests observation separately and never replays a deployment", async () => {
  let reconciled = false;
  const fetch = backend((url) => url.endsWith("/reconcile") ? (reconciled = true, json({ ...operation, reconcile_requested: true }, 202)) : url.includes("/hosting-operations/") ? json({ ...operation, reconcile_requested: reconciled }) : url.includes("/deployments") ? json(page([operation])) : undefined);
  setup("owner"); fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" }));
  fireEvent.click(await screen.findByRole("button", { name: "Request reconciliation" })); await screen.findByText(/Observation does not authorize replaying/);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST").map(([url]) => url)).toEqual(["/api/v1/hosting-operations/hop_one/reconcile"]);
});
test("history pagination requests a bounded next page and retains server ordering", async () => {
  const fetch = backend((url) => url.includes("/deployments") ? json(page([{ ...operation, id: url.includes("offset=20") ? "hop_older" : "hop_newest" }], url.includes("offset=20") ? 20 : 0, 21)) : undefined);
  setup("viewer"); await screen.findByRole("button", { name: "View operation hop_newest" });
  fireEvent.click(screen.getByRole("button", { name: "Next deployments for Web" })); await screen.findByRole("button", { name: "View operation hop_older" });
  expect(fetch.mock.calls.some(([url]) => url.endsWith("deployments?limit=20&offset=20"))).toBe(true);
});
test("late mutation response cannot select an operation after switching project and resource", async () => {
  let resolve!: (response: Response) => void;
  backend((url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : url.includes("other") && url.includes("deployments") ? json(page()) : undefined);
  const view = setup(); acknowledge(); await request();
  view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={{ ...resource, id: "other", project_id: "other-project", name: "Other" }} writable blocked={false} /></QueryClientProvider>);
  await act(async () => resolve(json(operation, 202)));
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
  expect(screen.queryByText("hop_one")).toBeNull();
  expect(screen.queryByLabelText(/I understand that/)).toBeNull();
});
test("restore-held admission preserves input without a generation or retry flow", async () => {
  const fetch = backend((url, options) => url.includes("/deployments") && options.method === "POST" ? json({ code: "hosting_restore_held", error: "restored ownership is held" }, 409) : undefined);
  setup(); fireEvent.change(screen.getByLabelText("Runner-local host port"), { target: { value: "9081" } }); acknowledge(); await request();
  await screen.findByText(/Recovery proof is pending/);
  expect(screen.getByLabelText<HTMLInputElement>("Runner-local host port").value).toBe("9081");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deployment blocked after restore" }).disabled).toBe(true);
  expect(screen.queryByRole("button", { name: "Retry identical deployment request" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Use reviewed configuration for a new request" })).toBeNull();
  expect(fetch.mock.calls.some(([url]) => url === "/api/v1/resources/resource")).toBe(false);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
});
test("restored ownership is separate from mutation hold and observation never promises an unlock", async () => {
  const held = { ...operation, state: "succeeded" as const, restore_ownership_held: true, mutation_hold: false };
  backend((url) => url.includes("/deployments") ? json(page([held])) : url.includes("/hosting-operations/") ? json(held) : undefined);
  setup("owner"); fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" }));
  await screen.findByText(/Trusted local recovery proof is pending/);
  expect(screen.queryByText(/Mutation hold retained/)).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deployment blocked after restore" }).disabled).toBe(true);
  expect(screen.getByRole("button", { name: "Request reconciliation" })).toBeTruthy();
});
test("known mutation hold pauses a new request without claiming restoration or clearing the hold", async () => {
  const held: HostingOperation = { ...operation, state: "reconciliation_required", mutation_hold: true };
  backend((url) => url.includes("/deployments") ? json(page([held])) : undefined); setup();
  await screen.findByText(/New deployment requests are paused/); acknowledge();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deployment waiting for settlement" }).disabled).toBe(true);
  expect(screen.queryByText(/Recovery proof is pending/)).toBeNull();
});
test("lost response then held history permits only the authorized retained exact-key recovery", async () => {
  const held: HostingOperation = { ...operation, state: "reconciliation_required", mutation_hold: true };
  let accepted = false;
  const fetch = backend((url, options) => {
    if (url.includes("/deployments") && options.method === "POST") {
      if (!accepted) { accepted = true; throw new TypeError("response lost after acceptance"); }
      return json(held, 202);
    }
    if (url.includes("/deployments")) return json(page(accepted ? [held] : []));
    if (url.includes("/hosting-operations/")) return json(held);
  });
  const view = setup(); acknowledge(); await request();
  await screen.findByRole("button", { name: "Retry identical deployment request" });
  fireEvent.click(screen.getByRole("button", { name: "Refresh deployment history" }));
  await screen.findByText(/New deployment requests are paused/);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical deployment request" }).disabled).toBe(false);
  expect(screen.getByLabelText<HTMLInputElement>("Runner-local host port").disabled).toBe(true);
  const form = screen.getByRole("form");
  view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable={false} blocked={false} /></QueryClientProvider>);
  expect(screen.queryByRole("button", { name: "Retry identical deployment request" })).toBeNull();
  fireEvent.submit(form);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
  view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable blocked={false} /></QueryClientProvider>);
  fireEvent.submit(screen.getByRole("form"));
  expect(screen.queryByRole("button", { name: "Retry identical deployment request" })).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Deployment waiting for settlement" }).disabled).toBe(true);
});

test("automatic allocation is default and the frozen retry omits host_port", async () => {
  let count = 0;
  const fetch = backend((url, options) => url.includes("/deployments") && options.method === "POST" && ++count === 1 ? Promise.reject(new TypeError("lost")) : undefined);
  setup("maintainer", [], true);
  expect(screen.queryByLabelText("Runner-local host port")).toBeNull(); acknowledge(); await request();
  await screen.findByRole("button", { name: "Retry identical deployment request" });
  expect(screen.getByLabelText<HTMLSelectElement>("Host port allocation").disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form")); await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST");
  expect(JSON.parse(writes[0]![1].body as string)).not.toHaveProperty("host_port");
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
const retained = { healthy: { snapshot: operation.snapshot, operation_id: "hop_old", container_id: "container_old", release: { canonical_image: "team/web@sha256:old", image_id: "image_old", daemon_id: "daemon", hash: "release", os: "linux" as const, architecture: "amd64" }, plan_hash: "plan_old" }, state: "retained" as const, retained_running: true };
const cleanupOperation: HostingOperation = { ...operation, id: "hop_cleanup", task_mode: "cleanup", cleanup_plan: { target: retained.healthy, current_healthy_operation_id: "hop_current", compiler_version: "image-hosting.release-ports.v1", hash: "cleanup_plan" } };
test("maintainer confirms exact retained target and sends only generation and key", async () => {
  const fetch = backend((url, options) => url.endsWith("/cleanup") ? json(cleanupOperation, 202) : url.includes("/retained-releases") ? json([retained]) : url.includes("/hosting-operations/") ? json(cleanupOperation) : undefined);
  setup(); fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  expect(screen.getByText(/Remove only release hop_old, container container_old, at 127.0.0.1:8080/)).toBeTruthy();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request retained release cleanup" }).disabled).toBe(true);
  fireEvent.click(screen.getByLabelText(/I confirm cleanup/)); fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const write = fetch.mock.calls.find(([url]) => url.endsWith("/cleanup"))!;
  expect(write[0]).toBe("/api/v1/resources/resource/retained-releases/hop_old/cleanup");
  expect(Object.keys(JSON.parse(write[1].body as string)).sort()).toEqual(["expected_generation", "idempotency_key"]);
});
test("cleanup exact-key recovery survives uncertain delivery and a refreshed hold/pending release", async () => {
  let accepted = false;
  const fetch = backend((url, options) => {
    if (url.endsWith("/cleanup")) { if (!accepted) { accepted = true; throw new TypeError("response lost"); } return json(cleanupOperation, 202); }
    if (url.includes("/retained-releases")) return json([{ ...retained, state: accepted ? "cleanup_pending" : "retained" }]);
    if (url.includes("/deployments") && options.method !== "POST") return json(page(accepted ? [{ ...operation, mutation_hold: true }] : []));
    if (url.includes("/hosting-operations/")) return json(cleanupOperation);
  });
  setup(); fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" })); fireEvent.click(screen.getByLabelText(/I confirm cleanup/));
  fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" })); await screen.findByRole("button", { name: "Retry identical cleanup request" });
  await act(async () => { await client.invalidateQueries({ queryKey: ["hostingDeployments"] }); await client.invalidateQueries({ queryKey: ["hostingRetained"] }); });
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical cleanup request" }).disabled).toBe(false);
  fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" })); await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([url]) => url.endsWith("/cleanup")); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("cleanup conflicts refresh metadata and require fresh explicit confirmation with current generation", async () => {
  let count = 0;
  const fetch = backend((url) => url.endsWith("/cleanup") ? ++count === 1 ? json({}, 409) : json(cleanupOperation, 202) : url.includes("/retained-releases") ? json([retained]) : url.includes("/hosting-operations/") ? json(cleanupOperation) : undefined);
  setup(); fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" })); fireEvent.click(screen.getByLabelText(/I confirm cleanup/)); fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" }));
  fireEvent.click(await screen.findByRole("button", { name: "Review current cleanup generation" }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request retained release cleanup" }).disabled).toBe(true));
  expect(screen.getByLabelText<HTMLInputElement>(/I confirm cleanup/).checked).toBe(false);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/cleanup"))).toHaveLength(1);
  fireEvent.click(screen.getByLabelText(/I confirm cleanup/)); fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([url]) => url.endsWith("/cleanup")).map(([, options]) => JSON.parse(options.body as string)); expect(writes[1].expected_generation).toBe(2); expect(writes[1].idempotency_key).not.toBe(writes[0].idempotency_key);
});
test.each(["viewer", "restore", "error"])("retained cleanup fails closed for %s", async (mode) => {
  backend((url) => url.includes("/retained-releases") ? mode === "error" ? json({}, 503) : json([retained]) : url.includes("/deployments") && mode === "restore" ? json(page([{ ...operation, restore_ownership_held: true }])) : undefined);
  setup(mode === "viewer" ? "viewer" : "owner"); await screen.findByRole("region", { name: "Retained releases" });
  if (mode === "viewer") { await screen.findByText("container_old"); expect(screen.queryByRole("button", { name: "Review cleanup hop_old" })).toBeNull(); }
  else if (mode === "restore") expect((await screen.findByRole<HTMLButtonElement>("button", { name: "Review cleanup hop_old" })).disabled).toBe(true);
  else await screen.findByText(/Unable to refresh retained releases/);
});


test.each(["permission", "principal"])("cleanup intent and frozen retry end on %s scope change", async (scope) => {
  const fetch = backend((url) => url.endsWith("/cleanup") ? Promise.reject(new TypeError("response lost")) : url.includes("/retained-releases") ? json([retained]) : undefined);
  const view = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  fireEvent.click(screen.getByLabelText(/I confirm cleanup/));
  fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" }));
  await screen.findByRole("button", { name: "Retry identical cleanup request" });
  if (scope === "permission") {
    view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable={false} blocked={false} /></QueryClientProvider>);
    expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull();
    view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable blocked={false} /></QueryClientProvider>);
  } else {
    await act(async () => { client.setQueryData(queryKeys.principal, { id: "another-maintainer", provider: "oidc", roles: [] }); client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "another-maintainer", role: "maintainer" }]); });
    await waitFor(() => expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull());
  }
  expect(screen.queryByRole("button", { name: "Retry identical cleanup request" })).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  expect(screen.getByLabelText<HTMLInputElement>(/I confirm cleanup/).checked).toBe(false);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request retained release cleanup" }).disabled).toBe(true);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/cleanup"))).toHaveLength(1);
});

test.each(["permission", "principal"])("late cleanup response cannot restore intent or select an operation after %s scope change", async (scope) => {
  let resolve!: (response: Response) => void;
  backend((url) => url.endsWith("/cleanup") ? new Promise<Response>((done) => { resolve = done; }) : url.includes("/retained-releases") ? json([retained]) : undefined);
  const view = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  fireEvent.click(screen.getByLabelText(/I confirm cleanup/));
  fireEvent.submit(screen.getByRole("form", { name: "Confirm retained release cleanup" }));
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  if (scope === "permission") {
    view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable={false} blocked={false} /></QueryClientProvider>);
    view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable blocked={false} /></QueryClientProvider>);
  } else {
    await act(async () => { client.setQueryData(queryKeys.principal, { id: "another-maintainer", provider: "oidc", roles: [] }); client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "another-maintainer", role: "maintainer" }]); });
    await waitFor(() => expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull());
  }
  await act(async () => { resolve(json(cleanupOperation, 202)); });
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
  expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  expect(screen.getByLabelText<HTMLInputElement>(/I confirm cleanup/).checked).toBe(false);
});


test("fixed-port mode requires explicit interruption acknowledgment and resets it when settings change", async () => {
  backend(); setup("maintainer", [], true);
  expect(screen.queryByRole("note")).toBeNull();
  acknowledge();
  fireEvent.change(screen.getByLabelText("Host port allocation"), { target: { value: "fixed" } });
  expect(screen.getByRole("note").textContent).toMatch(/current release stops before the replacement is ready/);
  expect(screen.getByLabelText(/I understand that reusing.*interrupts service/)).toBeTruthy();
  for (const [label, value] of [["Runner-local host port", "9090"], ["Container port", "8080"], ["Readiness path", "/health"], ["Readiness timeout in seconds", "45"]]) {
    expect(screen.getByLabelText<HTMLInputElement>(/I understand that/).checked).toBe(false);
    acknowledge();
    fireEvent.change(screen.getByLabelText(label!), { target: { value } });
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(true);
  }
  acknowledge();
  fireEvent.change(screen.getByLabelText("Host port allocation"), { target: { value: "automatic" } });
  expect(screen.getByLabelText<HTMLInputElement>(/I understand that/).checked).toBe(false);
  expect(screen.queryByRole("note")).toBeNull();
});

test("Git configuration keeps operation history visible without image admission controls", async () => {
 const fetch = backend();
 client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
 client.setQueryData(queryKeys.principal, { id: "user", roles: ["system_admin"], provider: "local" });
 client.setQueryData(queryKeys.allProjectMembers, []);
 const git: Resource = { ...resource, source: { kind: "git", git: { source_id: "source", generation: 1, repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
 render(<QueryClientProvider client={client}><ResourceDeployments resource={git} writable blocked={false} /></QueryClientProvider>);
 await screen.findByText("No deployment operations recorded.");
 expect(screen.getByText(/Use Git builds/)).toBeTruthy();
 expect(screen.queryByRole("button", { name: "Request image deployment" })).toBeNull();
 expect(screen.queryByLabelText("Container port")).toBeNull();
 expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
});

test("nonzero runtime generation is freshly captured once and frozen across lost-response retries", async () => {
  let generation = 3, reads = 0, writes = 0;
  const fetch = backend((url, options) => url.endsWith("/runtime-configuration") ? (reads++, json({ runtime_configuration: { resource_id: "resource", project_id: "project", runtime_generation: generation, resource_generation: 1, entries: [] } })) : url.endsWith("/deployments") && options.method === "POST" && ++writes === 1 ? Promise.reject(new TypeError("lost")) : undefined);
  setup(); acknowledge(); await request(); await screen.findByText(/The connection ended without a confirmed response/);
  generation = 5; fireEvent.submit(screen.getByRole("form", { name: "Deploy image for Web" })); await screen.findByRole("region", { name: "Deployment operation detail" });
  const bodies = fetch.mock.calls.filter(([, options]) => options.method === "POST"); expect(reads).toBe(1); expect(bodies[0]![1].body).toBe(bodies[1]![1].body); expect(JSON.parse(bodies[1]![1].body as string)).toMatchObject({ expected_runtime_configuration_generation: 3 });
});
test.each([503, 403])("runtime metadata HTTP %s cannot create a deployment request", async (status) => {
  const fetch = backend((url) => url.endsWith("/runtime-configuration") ? json({}, status) : undefined); setup(); acknowledge(); await request(); await screen.findByText(status === 403 ? /Deployment permission is unavailable/ : /Unable to verify saved runtime configuration/); expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
});
test("pending membership refresh aborts deployment and clears its frozen request", async () => {
  let resolve!: (response: Response) => void;
  const fetch = backend((url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : url.includes("/project-members") ? new Promise<Response>(() => {}) : undefined);
  setup(); acknowledge(); await request(); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  const input = fetch.mock.calls.find(([, options]) => options.method === "POST")![1];
  await act(async () => { void client.invalidateQueries({ queryKey: queryKeys.allProjectMembers }); });
  await screen.findByText(/Deployment access is pending or unavailable/); expect(input.signal?.aborted).toBe(true);
  await act(async () => resolve(json(operation, 202))); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
});

test("stale runtime generation requires explicit separate review and fresh capture under a new key", async () => {
  let generation = 3, writes = 0;
  const fetch = backend((url, options) => url.endsWith("/runtime-configuration") ? json({ runtime_configuration: { resource_id: "resource", project_id: "project", resource_generation: 1, runtime_generation: generation, entries: [] } }) : url.endsWith("/deployments") && options.method === "POST" && ++writes === 1 ? json({}, 409) : undefined);
  setup(); acknowledge(); await request(); await screen.findByText(/Current saved configuration: generation 2/); generation = 4;
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
  fireEvent.click(screen.getByLabelText(/I reviewed history/)); fireEvent.click(screen.getByRole("button", { name: "Use reviewed configuration for a new request" })); acknowledge(); await request(); await screen.findByRole("region", { name: "Deployment operation detail" });
  const inputs = fetch.mock.calls.filter(([, options]) => options.method === "POST").map(([, options]) => JSON.parse(options.body as string)); expect(inputs[0].expected_runtime_configuration_generation).toBe(3); expect(inputs[1].expected_runtime_configuration_generation).toBe(4); expect(inputs[1].idempotency_key).not.toBe(inputs[0].idempotency_key);
});
test("deployment response with another runtime generation cannot select an operation", async () => {
  backend((url, options) => url.endsWith("/runtime-configuration") ? json({ runtime_configuration: { resource_id: "resource", project_id: "project", resource_generation: 1, runtime_generation: 3, entries: [] } }) : url.endsWith("/deployments") && options.method === "POST" ? json({ ...operation, snapshot: { ...operation.snapshot, runtime_configuration: { generation: 4, hash: "wrong" } } }, 202) : undefined);
  setup(); acknowledge(); await request(); await screen.findByText(/The connection ended without a confirmed response/); expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull(); expect(screen.getByText(/resource\/runtime generations 1\/3/)).toBeTruthy();
});

const compose: Resource = { ...resource, source: { kind: "compose", compose: { original: "services:\n  web:\n    image: team/web:1\n    environment:\n      - TOKEN=${TOKEN:?required}\n", public_component: "web", container_port: 3000 } } };
const stackRef = { generation: 1, hash: `sha256:${"a".repeat(64)}` };
const stackOptions = { resource_id: "resource", resource_generation: 1, stack_ref: stackRef, storage_ref: null, previous_healthy_operation_id: null, host_port: null, container_port: null, readiness: null, available: true, reason: "initial_deployment", routes: [], storage_route_continuity: null };
test("saving the first Compose destination loads current deployment options", async () => {
  let bound = false;
  backend((url) => {
    if (url.endsWith("/binding")) return bound ? json(binding) : json({}, 404);
    if (url.endsWith("/storage-deployment-options")) return json(bound ? stackOptions : { ...stackOptions, available: false, reason: "unbound" });
    return undefined;
  });
  setup("maintainer", [], false, compose);
  await screen.findByText("Save a desired destination before requesting deployment.");
  bound = true;
  await act(async () => { await client.invalidateQueries({ queryKey: ["resourceBinding", "project", "resource"] }); });
  await screen.findByLabelText("Readiness path");
});
function composeBackend(replacement = false, handler?: Parameters<typeof backend>[0]) {
  const continuity = { expected_loaded_generation: 4, expected_loaded_proxy_operation_id: "proxy_old", routes: [{ route_id: "route", expected_version: 2, expected_selected_release_operation_id: "hop_old" }] };
  const options = replacement ? { ...stackOptions, reason: "available", previous_healthy_operation_id: "hop_old", host_port: 8080, container_port: 3000, readiness: { path: "/ready", timeout_seconds: 20 }, routes: [{ id: "route", hostname: "app.example.test", path_prefix: "/", version: 2, selected_release_operation_id: "hop_old" }], storage_route_continuity: continuity } : stackOptions;
  return backend((url, init) => {
    const custom = handler?.(url, init);
    if (custom) return custom;
    if (url.endsWith("/storage-deployment-options")) return json(options);
    if (url.endsWith("/runtime-configuration")) return json({ runtime_configuration: { resource_id: "resource", project_id: "project", resource_generation: 1, runtime_generation: 3, entries: [{ name: "TOKEN", kind: "managed_secret", secret: { secret_id: "secret", version: 2 } }] } });
    if (url.endsWith("/deployments") && init.method === "POST") {
      const input = JSON.parse(init.body as string);
      return json({ ...operation, snapshot: { ...operation.snapshot, stack: stackRef, ...(input.registry_credential ? { registry_credential: input.registry_credential } : {}), host_port: input.host_port ?? 9090, container_port: input.container_port, readiness: input.readiness, runtime_configuration: { generation: 3, hash: "runtime" }, ...(input.storage_route_continuity ? { storage_route_continuity: { hash: "continuity" } } : {}) } }, 202);
    }
    return undefined;
  });
}
async function reviewCompose() {
  fireEvent.click(await screen.findByRole("button", { name: "Review current Compose deployment" }));
  await screen.findByRole("region", { name: "Reviewed Compose deployment" });
  fireEvent.click(screen.getByLabelText(/I reviewed this saved Compose configuration/));
}
test.each([false, true])("saved Compose captures runtime references and exact %s replacement route authority", async (replacement) => {
  const fetch = composeBackend(replacement); setup("maintainer", [], false, compose);
  await screen.findByRole("region", { name: "Saved Compose deployment review" });
  if (replacement) expect(screen.queryByLabelText("Readiness path")).toBeNull();
  else expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/");
  await reviewCompose();
  expect(screen.getByRole("region", { name: "Reviewed Compose deployment" }).textContent).toContain(`readiness ${replacement ? "/ready" : "/"}.`);
  expect(screen.getByText(/TOKEN \(managed secret reference\)/)).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Retained releases" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const writes = fetch.mock.calls.filter(([url, init]) => url.endsWith("/deployments") && init.method === "POST");
  expect(writes).toHaveLength(1);
  const input = JSON.parse(writes[0]![1].body as string);
  expect(input).toMatchObject({ expected_generation: 1, expected_runtime_configuration_generation: 3, container_port: 3000, managed_stack: { acknowledge_interruption: true } });
  expect(input).not.toHaveProperty("registry_credential");
  expect(fetch.mock.calls.some(([url]) => url.includes("managed-secrets"))).toBe(false);
  if (replacement) expect(input).toMatchObject({ host_port: 8080, readiness: { path: "/ready", timeout_seconds: 20 }, storage_route_continuity: { expected_loaded_generation: 4, expected_loaded_proxy_operation_id: "proxy_old", routes: [{ route_id: "route", expected_version: 2, expected_selected_release_operation_id: "hop_old" }] } });
  else { expect(input).not.toHaveProperty("host_port"); expect(input).not.toHaveProperty("storage_route_continuity"); }
});
test("Compose viewer has history without deployment or cleanup controls", async () => {
  const fetch = composeBackend(); setup("viewer", [], false, compose);
  await screen.findByText(/Project maintainer access is required to deploy saved Compose/);
  expect(screen.queryByRole("button", { name: "Review current Compose deployment" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Retained releases" })).toBeNull();
  expect(fetch.mock.calls.every(([, init]) => init.method === "GET")).toBe(true);
});
test("Compose server mutation hold blocks new deployment review", async () => {
  composeBackend(false, (url, init) => url.includes("/deployments") && init.method !== "POST" ? json(page([{ ...operation, mutation_hold: true }])) : undefined);
  setup("maintainer", [], false, compose);
  await screen.findByText(/New deployment requests are paused/);
  expect((await screen.findByRole<HTMLButtonElement>("button", { name: "Review current Compose deployment" })).disabled).toBe(true);
});
test("a retired Compose review response cannot enter a newer resource generation", async () => {
  let calls = 0; let resolve!: (value: Response) => void;
  const fetch = composeBackend(false, (url) => url.endsWith("/storage-deployment-options") && ++calls === 2 ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  const view = setup("maintainer", [], false, compose);
  fireEvent.click(await screen.findByRole("button", { name: "Review current Compose deployment" }));
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  view.rerender(<QueryClientProvider client={client}><ResourceDeployments resource={{ ...compose, generation: 2 }} writable blocked={false} /></QueryClientProvider>);
  await act(async () => { resolve(json(stackOptions)); });
  expect(screen.queryByRole("region", { name: "Reviewed Compose deployment" })).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("Compose recorded components distinguish successful init completion from stopped workloads", async () => {
  const observed = { ...operation, state: "succeeded", stack_source: { version: "managed-compose.v1", original_hash: "hash", public_component: "web", container_port: 3000, volumes: [], services: [{ name: "web", image: "web:1", depends_on: { migrate: "service_completed_successfully" } }] }, result: { outcome: "ready", ready: true, restored: false, uncertain: false, stack: { observation_complete: true, public_component: "web", network_id: "network", volumes: [], components: [{ component: "migrate", container_id: "init-container", image_id: "init-image", operation_id: "hop_one", running: false, exit_code: 0, health: "none" }, { component: "web", container_id: "web-container", image_id: "web-image", operation_id: "hop_one", running: true, exit_code: 0, health: "healthy" }] } } };
  composeBackend(false, (url, init) => url.includes("/hosting-operations/") ? json(observed) : url.includes("/deployments") && init.method !== "POST" ? json(page([operation])) : undefined);
  setup("maintainer", [], false, compose);
  fireEvent.click(await screen.findByRole("button", { name: "View operation hop_one" }));
  await screen.findByText(/Initialization completed successfully/);
  expect(screen.getByText(/Running · health healthy/)).toBeTruthy();
  expect(screen.getByText(/Observation complete · public component web/)).toBeTruthy();
});

test("same selected Compose operation needs detail refresh after history becomes healthy", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  let healthy = false;
  let detailReads = 0;
  const observed: HostingOperation = { ...operation, state: "succeeded", stack_source: { version: "managed-compose.v1", original_hash: "hash", public_component: "web", container_port: 3000, volumes: [], services: [{ name: "web", image: "web:1" }] }, result: { outcome: "succeeded", ready: true, restored: false, uncertain: false, stack: { observation_complete: true, public_component: "web", network_id: "network", volumes: [], components: [{ component: "web", container_id: "web-container", image_id: "web-image", operation_id: "hop_one", running: true, exit_code: 0, health: "healthy" }] } } };
  composeBackend(false, (url, init) => {
    if (url.includes("/hosting-operations/")) { detailReads++; return json(healthy ? observed : operation); }
    if (url.includes("/deployments") && init.method !== "POST") return json(page([healthy ? observed : operation]));
    return undefined;
  });
  setup("maintainer", [], false, compose);
  await reviewCompose();
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  await waitFor(() => expect(detailReads).toBeGreaterThan(0));
  const initialReads = detailReads;
  expect(screen.queryByRole("region", { name: "Recorded Compose components" })).toBeNull();
  healthy = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh deployment history" }));
  await screen.findByText(/^Succeeded ·/);
  fireEvent.click(screen.getByRole("button", { name: "View operation hop_one" }));
  await act(async () => { await Promise.resolve(); });
  expect(detailReads).toBe(initialReads);
  expect(screen.queryByRole("region", { name: "Recorded Compose components" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Refresh operation" }));
  await screen.findByRole("region", { name: "Recorded Compose components" });
  expect(detailReads).toBe(initialReads + 1);
  expect(screen.getByText(/Observation complete · public component web/)).toBeTruthy();
  expect(screen.getByText(/Running · health healthy/)).toBeTruthy();
});

test("uncertain Compose delivery retains its exact request while the server holds competing mutations", async () => {
  let submissions = 0; let held = false;
  const fetch = composeBackend(false, (url, init) => {
    if (url.endsWith("/deployments") && init.method === "POST" && ++submissions === 1) { held = true; throw new TypeError("response lost"); }
    if (url.includes("/deployments") && init.method !== "POST" && held) return json(page([{ ...operation, state: "reconciliation_required", mutation_hold: true, result: { outcome: "failed", container_id: "", image_id: "", ready: false, restored: false, uncertain: true } }]));
    return undefined;
  });
  setup("maintainer", [], false, compose); await reviewCompose();
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  await screen.findByText(/Delivery is unconfirmed/);
  fireEvent.click(screen.getByRole("button", { name: "Refresh deployment history" }));
  await screen.findByText(/New deployment requests are paused/);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review current Compose deployment" }).disabled).toBe(true);
  const retry = screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical Compose deployment request" });
  expect(retry.disabled).toBe(false); fireEvent.click(retry);
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("Compose review permission denial clears deployment drafts", async () => {
  let reads = 0;
  const fetch = composeBackend(false, (url) => url.endsWith("/storage-deployment-options") && ++reads === 2 ? json({ error: "denied" }, 403) : undefined);
  setup("maintainer", [], false, compose);
  fireEvent.click(await screen.findByRole("button", { name: "Review current Compose deployment" }));
  await screen.findByText(/Deployment permission is unavailable/);
  expect(screen.queryByRole("region", { name: "Saved Compose deployment review" })).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});

test("Start reviews saved configuration and freezes the exact stopped reference", async () => {
  let attempts = 0;
  const fetch = composeBackend(true, (url, init) => {
    if (url.endsWith("/stack-lifecycle")) return json({ resource_id: "resource", resource_generation: 1, state: "stopped", deployed_operation_id: "hop_old", stop_operation_id: "stop_one" });
    if (url.endsWith("/storage-deployment-options")) return json({ ...stackOptions, stopped_operation_id: "stop_one", reason: "available", previous_healthy_operation_id: "hop_old", host_port: 8080, container_port: 3000, readiness: { path: "/ready", timeout_seconds: 20 } });
    if (url.endsWith("/deployments") && init.method === "POST") {
      if (++attempts === 1) throw new TypeError("lost receipt");
      const input = JSON.parse(String(init.body));
      return json({ ...operation, snapshot: { ...operation.snapshot, stack: stackRef, stack_stopped_operation_id: "stop_one", host_port: input.host_port, container_port: input.container_port, readiness: input.readiness, runtime_configuration: { generation: 3, hash: "runtime" } } }, 202);
    }
  });
  setup("maintainer", [], false, compose);
  fireEvent.click(await screen.findByRole("button", { name: "Review current Compose start" }));
  await screen.findByText(/Starts from recorded stop operation stop_one/);
  expect(screen.queryByLabelText("Readiness path")).toBeNull();
  expect(screen.getByRole("region", { name: "Reviewed Compose deployment" }).textContent).toContain("readiness /ready.");
  fireEvent.click(screen.getByLabelText(/I reviewed this saved Compose configuration/));
  fireEvent.click(screen.getByRole("button", { name: "Start reviewed Compose" }));
  fireEvent.click(await screen.findByRole("button", { name: "Retry identical Compose deployment request" }));
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const writes = fetch.mock.calls.filter(([url, init]) => url.endsWith("/deployments") && init.method === "POST");
  expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
  expect(JSON.parse(String(writes[0]![1].body))).toMatchObject({ expected_generation: 1, expected_runtime_configuration_generation: 3, managed_stack: { acknowledge_interruption: true, expected_stopped_operation_id: "stop_one" }, readiness: { path: "/ready", timeout_seconds: 20 } });
});

test("private stateless image retry keeps the exact credential version and sends no secret values", async () => {
  let writes = 0;
  const fetch = backend((url, init) => {
    if (url.includes("managed-secrets")) return json({ items: url.includes("versions") ? [{ secret_id: "credential", version: 1 }, { secret_id: "credential", version: 2 }] : [{ id: "credential", name: "Private registry", project_id: "project", status: "active" }], limit: 20, offset: 0, count: 1, total: 1 });
    if (url.endsWith("deployments") && init.method === "POST" && ++writes === 1) throw new TypeError("lost");
  });
  setup();
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByRole("option", { name: "Private registry · credential" });
  fireEvent.change(screen.getByLabelText("Registry credential secret"), { target: { value: "credential" } });
  await screen.findByRole("option", { name: "Version 1" });
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "1" } });
  acknowledge(); await request();
  await screen.findByRole("button", { name: "Retry identical deployment request" });
  expect(screen.getByLabelText<HTMLSelectElement>("Registry credential version").disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Deploy image for Web" }));
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const calls = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(calls).toHaveLength(2);
  expect(calls[0]![1].body).toBe(calls[1]![1].body);
  expect(JSON.parse(calls[0]![1].body as string).registry_credential).toEqual({ secret_id: "credential", version: 1 });
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
});

function registryMetadata(url: string) {
  if (!url.includes("managed-secrets")) return undefined;
  return json({ items: url.includes("versions") ? [{ secret_id: "credential", version: 1 }, { secret_id: "credential", version: 2 }] : [{ id: "credential", name: "Private registry", project_id: "project", status: "active" }], limit: 20, offset: 0, count: 1, total: 1 });
}
async function chooseComposeRegistry() {
  fireEvent.change(await screen.findByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByRole("option", { name: "Private registry · credential" });
  fireEvent.change(screen.getByLabelText("Registry credential secret"), { target: { value: "credential" } });
  await screen.findByRole("option", { name: "Version 1" });
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "1" } });
}
test.each(["initial", "replacement", "start"])("private Compose %s freezes the selected credential through uncertain retry", async (mode) => {
  let writes = 0;
  const fetch = composeBackend(mode !== "initial", (url, init) => {
    const metadata = registryMetadata(url);
    if (metadata) return metadata;
    if (mode === "start" && url.endsWith("/stack-lifecycle")) return json({ resource_id: "resource", resource_generation: 1, state: "stopped", deployed_operation_id: "hop_old", stop_operation_id: "stop_one" });
    if (mode === "start" && url.endsWith("/storage-deployment-options")) return json({ ...stackOptions, stopped_operation_id: "stop_one", reason: "available", previous_healthy_operation_id: "hop_old", host_port: 8080, container_port: 3000, readiness: { path: "/ready", timeout_seconds: 20 } });
    if (url.endsWith("/deployments") && init.method === "POST") {
      if (++writes === 1) throw new TypeError("lost receipt");
      if (mode === "start") {
        const input = JSON.parse(String(init.body));
        return json({ ...operation, snapshot: { ...operation.snapshot, stack: stackRef, registry_credential: input.registry_credential, stack_stopped_operation_id: "stop_one", host_port: input.host_port, container_port: input.container_port, readiness: input.readiness, runtime_configuration: { generation: 3, hash: "runtime" } } }, 202);
      }
    }
  });
  setup("maintainer", [], false, compose);
  await chooseComposeRegistry();
  expect(screen.getByText(/Images from other registries are pulled anonymously/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: mode === "start" ? "Review current Compose start" : "Review current Compose deployment" }));
  await screen.findByText(/Image access: Registry credential credential, version 1/);
  expect(screen.getByLabelText<HTMLSelectElement>("Registry credential version").disabled).toBe(true);
  fireEvent.click(screen.getByLabelText(/I reviewed this saved Compose configuration/));
  fireEvent.click(screen.getByRole("button", { name: mode === "start" ? "Start reviewed Compose" : "Deploy reviewed Compose" }));
  fireEvent.click(await screen.findByRole("button", { name: "Retry identical Compose deployment request" }));
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const submissions = fetch.mock.calls.filter(([url, init]) => url.endsWith("/deployments") && init.method === "POST");
  expect(submissions).toHaveLength(2);
  expect(submissions[0]![1].body).toBe(submissions[1]![1].body);
  const input = JSON.parse(String(submissions[0]![1].body));
  expect(input.registry_credential).toEqual({ secret_id: "credential", version: 1 });
  if (mode === "start") expect(input.managed_stack.expected_stopped_operation_id).toBe("stop_one");
  if (mode === "replacement") expect(input.storage_route_continuity.expected_loaded_generation).toBe(4);
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
});
test("Compose private selection requires an explicit version before review", async () => {
  const fetch = composeBackend(false, registryMetadata);
  setup("maintainer", [], false, compose);
  fireEvent.change(await screen.findByLabelText("Image access"), { target: { value: "private" } });
  const review = screen.getByRole<HTMLButtonElement>("button", { name: "Review current Compose deployment" });
  expect(review.disabled).toBe(true);
  fireEvent.click(review);
  expect(screen.queryByRole("region", { name: "Reviewed Compose deployment" })).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test.each([undefined, { secret_id: "credential", version: 2 }, { secret_id: "other", version: 1 }])("Compose rejects an accepted receipt with mismatched credential %j", async (credential) => {
  const fetch = composeBackend(false, (url, init) => {
    const metadata = registryMetadata(url);
    if (metadata) return metadata;
    if (url.endsWith("/deployments") && init.method === "POST") {
      const input = JSON.parse(String(init.body));
      return json({ ...operation, snapshot: { ...operation.snapshot, registry_credential: credential, stack: stackRef, host_port: 9090, container_port: input.container_port, readiness: input.readiness, runtime_configuration: { generation: 3, hash: "runtime" } } }, 202);
    }
  });
  setup("maintainer", [], false, compose);
  await chooseComposeRegistry(); await reviewCompose();
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  await screen.findByText(/Delivery is unconfirmed/);
  expect(screen.queryByText(/Deployment requested. Check progress below. Request: hop_one/)).toBeNull();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});

test("catalog Compose prefills readiness while preserving operator review and saved runtime references", async () => {
  const fetch = composeBackend(false, url => url.endsWith("/catalog-installation") ? json({
    id: compose.id, resource: compose, binding, storage_ref: { storage_id: "", generation: 0, hash: "" },
    template: { definition: { container_port: 3000, readiness: { path: "/health", timeout_seconds: 120 }, compose: compose.source.kind === "compose" ? compose.source.compose : undefined } },
  }) : undefined);
  setup("maintainer", [], false, compose);
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/health"));
  expect(screen.getByLabelText<HTMLInputElement>("Readiness timeout in seconds").value).toBe("120");
  expect(fetch.mock.calls.some(([, init]) => init.method === "POST")).toBe(false);
  await reviewCompose();
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const write = fetch.mock.calls.find(([url, init]) => url.endsWith("/deployments") && init.method === "POST")!;
  expect(JSON.parse(write[1].body as string)).toMatchObject({ readiness: { path: "/health", timeout_seconds: 120 }, expected_runtime_configuration_generation: 3 });
});

test("late catalog readiness defaults preserve edits made by the operator", async () => {
  let finish!: (response: Response) => void;
  composeBackend(false, url => url.endsWith("/catalog-installation") ? new Promise<Response>(resolve => { finish = resolve; }) : undefined);
  setup("maintainer", [], false, compose);
  await screen.findByLabelText("Readiness path");
  fireEvent.change(screen.getByLabelText("Readiness path"), { target: { value: "/custom" } });
  await act(async () => finish(json({ id: compose.id, resource: compose, binding, storage_ref: { storage_id: "", generation: 0, hash: "" }, template: { definition: { container_port: 3000, readiness: { path: "/health", timeout_seconds: 120 }, compose: compose.source.kind === "compose" ? compose.source.compose : undefined } } })));
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/custom");
  expect(screen.getByLabelText<HTMLInputElement>("Readiness timeout in seconds").value).toBe("30");
});

test("late catalog metadata preserves captured Compose readiness and identical retry body", async () => {
  let finish!: (response: Response) => void;
  let attempts = 0;
  const fetch = composeBackend(false, (url, init) => {
    if (url.endsWith("/catalog-installation")) return new Promise<Response>(resolve => { finish = resolve; });
    if (url.endsWith("/deployments") && init.method === "POST" && ++attempts === 1) throw new TypeError("lost receipt");
    return undefined;
  });
  setup("maintainer", [], false, compose);
  await reviewCompose();
  await act(async () => finish(json({
    id: compose.id, resource: compose, binding, storage_ref: { storage_id: "", generation: 0, hash: "" },
    template: { definition: { container_port: 3000, readiness: { path: "/health", timeout_seconds: 120 }, compose: compose.source.kind === "compose" ? compose.source.compose : undefined } },
  })));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)); });
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/");
  expect(screen.getByLabelText<HTMLInputElement>("Readiness timeout in seconds").value).toBe("30");
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed Compose" }));
  fireEvent.click(await screen.findByRole("button", { name: "Retry identical Compose deployment request" }));
  await screen.findByText(/Deployment requested. Check progress below. Request: hop_one/);
  const writes = fetch.mock.calls.filter(([url, init]) => url.endsWith("/deployments") && init.method === "POST");
  expect(writes).toHaveLength(2);
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
  expect(JSON.parse(String(writes[0]![1].body)).readiness).toEqual({ path: "/", timeout_seconds: 30 });
});

test("Git draft Compose settings alone do not expose committed stack controls", async () => {
  const git: Resource = { ...resource, source: { kind: "git", git: { source_id: "source", generation: 2, repository_url: "https://github.com/example/app", requested_ref: "main", credential_kind: "public", settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 80 } } } };
  const fetch = backend(url => url.endsWith("/stack-lifecycle") ? json({ resource_id: "resource", resource_generation: 1, state: "unavailable" }) : undefined); setup("maintainer", [], false, git);
  await screen.findByText("No deployment operations recorded.");
  expect(screen.queryByRole("region", { name: "Compose lifecycle" })).toBeNull();
  expect(fetch.mock.calls.some(([url]) => url.endsWith("/stack-lifecycle"))).toBe(true);
});

test("committed Git Compose lifecycle remains available when later Git configuration/build changes", async () => {
  const git: Resource = { ...resource, generation: 2, source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/example/app", requested_ref: "new-failed-build", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
  const deployed = { ...operation, state: "succeeded" as const, snapshot: { ...operation.snapshot, git_compose: { build_id: "prior-build", source_generation: 2, result_hash: `sha256:${"a".repeat(64)}` }, stack: { generation: 1, hash: `sha256:${"b".repeat(64)}` } } } as HostingOperation;
  backend(url => url.includes("/deployments?") ? json(page()) : url.endsWith("/stack-lifecycle") ? json({ resource_id: "resource", resource_generation: 2, state: "running", deployed_operation_id: "hop_one" }) : url === "/api/v1/hosting-operations/hop_one" ? json(deployed) : undefined);
  setup("maintainer", [], false, git);
  const stop = await screen.findByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" });
  await waitFor(() => expect(stop.disabled).toBe(false));
  expect(screen.getByRole("region", { name: "Compose lifecycle" })).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Retained releases" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Saved Compose deployment review" })).toBeNull();
});


test("current Git stack controls and volume backups survive more than twenty later failed operations and paging", async () => {
  const git: Resource = { ...resource, generation: 2, source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/example/app", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
  const prior = { ...operation, id: "prior", state: "succeeded", snapshot: { ...operation.snapshot, git_compose: { build_id: "prior-build", source_generation: 2, result_hash: `sha256:${"a".repeat(64)}` }, stack: { generation: 1, hash: `sha256:${"b".repeat(64)}` } } };
  const fetch = backend(url => {
    if (url.includes("/deployments?")) { const offset=Number(new URL(url,"http://local").searchParams.get("offset")); return json(page(Array.from({length:20},(_,i)=>({...operation,id:`failed_${offset+i}`,state:"failed" as const})),offset,40)); }
    if (url.endsWith("/stack-lifecycle")) return json({ resource_id: "resource", resource_generation: 2, state: "running", deployed_operation_id: "prior" });
    if (url === "/api/v1/hosting-operations/prior") return json(prior);
    if (url.endsWith("/storage-backup-volumes")) return json({resource_id:"resource",resource_generation:2,operation_id:"prior",available:true,volumes:[]});
    if (url.includes("storage-backups?")) return json({items:[],limit:20,offset:0,count:0,total:0});
    if (url.includes("database-backup-targets")||url.includes("storage-backup-schedules")) return json([]);
  });
  setup("maintainer", [], false, git);
  await screen.findByRole("region",{name:"Compose lifecycle"});
  expect(screen.getByRole("region",{name:"Compose volume backups"})).toBeTruthy();
  fireEvent.click(screen.getByRole("button",{name:"Next deployments for Web"}));
  await waitFor(()=>expect(fetch.mock.calls.some(([url])=>url.includes("offset=20"))).toBe(true));
  expect(screen.getByRole("region",{name:"Compose lifecycle"})).toBeTruthy();
  expect(screen.getByRole("region",{name:"Compose volume backups"})).toBeTruthy();
});

test("historical Git Compose cannot expose stack controls over a newer single-image release", async () => {
  const git: Resource = { ...resource, source: { kind: "git", git: { source_id: "source", generation: 2, repository_url: "https://github.com/example/app", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
  const old = { ...operation, state: "succeeded", snapshot: { ...operation.snapshot, git_compose: { build_id: "old", source_generation: 1, result_hash: `sha256:${"a".repeat(64)}` }, stack: {generation:1,hash:`sha256:${"b".repeat(64)}`} } } as HostingOperation;
  backend(url=>url.includes("/deployments?")?json(page([old])):url.endsWith("/stack-lifecycle")?json({resource_id:"resource",resource_generation:1,state:"unavailable"}):undefined);
  setup("maintainer",[],false,git); await screen.findByRole("button",{name:"View operation hop_one"});
  expect(screen.queryByRole("region",{name:"Compose lifecycle"})).toBeNull();
  expect(screen.queryByRole("region",{name:"Compose volume backups"})).toBeNull();
});
