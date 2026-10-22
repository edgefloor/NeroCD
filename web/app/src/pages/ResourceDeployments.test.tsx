import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import type { HostingOperation } from "@/api/hosting-deployments";
import { ResourceDeployments } from "./ResourceDeployments";

const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "team/web:1" } }, generation: 1, created_by: "user", created_at: "", updated_at: "" };
const binding = { id: "binding", resource_id: "resource", destination_id: "dest", runtime_namespace: "ncd_resource", created_by: "user", created_at: "" };
const operation: HostingOperation = { id: "hop_one", project_id: "project", resource_id: "resource", binding_id: "binding", state: "queued", snapshot: { resource_id: "resource", resource_generation: 1, image_reference: "team/web:1", binding_id: "binding", destination_id: "dest", server_id: "server", runner_id: "runner", runtime_namespace: "ncd_resource", container_port: 80, host_port: 8080, readiness: { path: "/", timeout_seconds: 30 }, policy_version: "public-image.loopback.v1" }, restore_ownership_held: false, mutation_hold: false, reconcile_requested: false, created_by: "user", created_at: "2026-10-22T10:00:00Z", updated_at: "2026-10-22T10:00:00Z" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const page = (items: HostingOperation[] = [], offset = 0, total = items.length) => ({ items, count: items.length, total, limit: 20, offset });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(role = "maintainer", globalRoles: string[] = [], automatic = false) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(queryKeys.principal, { id: "user", name: "Operator", email: "operator@example.test", provider: "oidc", roles: globalRoles });
  client.setQueryData(queryKeys.allProjectMembers, role ? [{ id: "member", project_id: "project", user_id: "user", email: "operator@example.test", role, created_at: "" }] : []);
  const view = render(<QueryClientProvider client={client}><ResourceDeployments resource={resource} writable={["owner", "maintainer"].includes(role) || globalRoles.includes("system_admin")} blocked={false} /></QueryClientProvider>);
  const mode = screen.queryByLabelText("Host port allocation");
  if (mode && !automatic) fireEvent.change(mode, { target: { value: "fixed" } });
  return view;
}
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const response = await handler?.(url, options);
    if (response) return response;
    if (url.endsWith("/binding")) return json(binding);
    if (url.includes("/retained-releases")) return json([]);
    if (url.includes("/deployments")) return options.method === "POST" ? json(operation, 202) : json(page());
    if (url.includes("/hosting-operations/")) return json(operation);
    if (url === "/api/v1/resources/resource") return json({ ...resource, generation: 2 });
    throw new Error(`Unexpected test endpoint ${url}`);
  });
  vi.stubGlobal("fetch", fetch); return fetch;
}
async function request() {
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request image deployment" }).disabled).toBe(false));
  fireEvent.submit(screen.getByRole("form", { name: "Deploy image for Web" }));
}
function acknowledge() { fireEvent.click(screen.getByLabelText(/I understand that/)); }
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
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
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
  expect(screen.getByLabelText<HTMLInputElement>(/I understand that/).checked).toBe(false);
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
  await screen.findByRole("region", { name: "Deployment operation detail" });
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST");
  expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
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
    await act(async () => { client.setQueryData(queryKeys.principal, { id: "another-maintainer", provider: "oidc", roles: [] }); });
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
    await act(async () => { client.setQueryData(queryKeys.principal, { id: "another-maintainer", provider: "oidc", roles: [] }); });
    await waitFor(() => expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull());
  }
  await act(async () => { resolve(json(cleanupOperation, 202)); });
  expect(screen.queryByRole("region", { name: "Deployment operation detail" })).toBeNull();
  expect(screen.queryByRole("form", { name: "Confirm retained release cleanup" })).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "Review cleanup hop_old" }));
  expect(screen.getByLabelText<HTMLInputElement>(/I confirm cleanup/).checked).toBe(false);
});
