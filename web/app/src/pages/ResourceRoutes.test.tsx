import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { ResourceRoutes } from "./ResourceRoutes";
vi.mock("./ResourceRouteSwitch", () => ({ ResourceRouteSwitch: () => <div data-testid="route-switch-panel" /> }));
const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "web:1" } }, generation: 1, created_by: "user", created_at: "", updated_at: "" };
const route = { id: "route_one", resource_id: "resource", hostname: "web.example.test", path_prefix: "/", version: 1, selected_release_operation_id: "hop_healthy", enabled: true, state: "pending", operation_id: "pop_one", http_ready: false, https_ready: false, certificate: { hostname: "web.example.test", status: "pending" } };
const healthy = { id: "hop_healthy", project_id: "project", resource_id: "resource", state: "succeeded", snapshot: { image_reference: "web:1" }, result: { ready: true, uncertain: false }, mutation_hold: false, restore_ownership_held: false };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(writable = true) { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles: [] }); return render(<QueryClientProvider client={client}><ResourceRoutes resource={resource} writable={writable} blocked={false} /></QueryClientProvider>); }
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? (url.includes("/deployments") ? json({ items: [healthy], limit: 20, offset: 0, count: 1, total: 1 }) : url.includes("/proxy-operations/") ? json({ id: "pop_one", state: "queued", mutation_hold: false, restore_ownership_held: false, routes: [route] }) : json([route])));
  vi.stubGlobal("fetch", fetch); return fetch;
}
async function draft() { await screen.findByRole("option", { name: /hop_healthy/ }); fireEvent.change(screen.getByLabelText("Route hostname"), { target: { value: "new.example.test" } }); fireEvent.change(screen.getByLabelText("Expected settled healthy deployment"), { target: { value: "hop_healthy" } }); }
test("viewer reads desired/HTTP/HTTPS status without mutation form", async () => { backend(); setup(false); await screen.findByText("Trusted HTTPS readiness recorded"); expect(screen.getAllByText("not confirmed")).toHaveLength(2); expect(screen.queryByRole("form")).toBeNull(); fireEvent.click(screen.getByRole("button", { name: "View route operation pop_one" })); await screen.findByText("Operation state: queued"); });
test("create freezes exact request after response loss without caller upstream, including retry", async () => {
  let count = 0; const fetch = backend((url, options) => options.method === "POST" ? ++count === 1 ? Promise.reject(new TypeError("lost")) : json(route, 202) : undefined);
  setup(); await draft(); fireEvent.submit(screen.getByRole("form")); fireEvent.click(await screen.findByRole("button", { name: "Retry identical route request" }));
  await screen.findByRole("region", { name: "Route operation status" }); const writes = fetch.mock.calls.filter(([, o]) => o.method === "POST"); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body); expect(Object.keys(JSON.parse(writes[0]![1].body as string)).sort()).toEqual(["expected_healthy_operation_id", "expected_route_version", "hostname", "idempotency_key", "path_prefix"]);
});
test("409 preserves input and does not automatically resubmit a refreshed route version", async () => { const fetch = backend((url, options) => options.method === "PATCH" ? json({}, 409) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "Edit route route_one" })); fireEvent.change(screen.getByLabelText("Route hostname"), { target: { value: "draft.example.test" } }); fireEvent.submit(screen.getByRole("form")); await screen.findByText(/Conflict with current version/); expect(screen.getByLabelText<HTMLInputElement>("Route hostname").value).toBe("draft.example.test"); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical route request" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, o]) => o.method === "PATCH")).toHaveLength(1); });
test.each(["permission", "principal", "resource"])("late route mutation does not repopulate after %s scope change", async (scope) => {
  let resolve!: (r: Response) => void; backend((url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  const view = setup(); await draft(); fireEvent.submit(screen.getByRole("form")); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  if (scope === "principal") { await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); }); await waitFor(() => expect(screen.queryByRole("button", { name: "Retry identical route request" })).toBeNull()); }
  else view.rerender(<QueryClientProvider client={client}><ResourceRoutes resource={scope === "resource" ? { ...resource, id: "other-resource" } : resource} writable={scope !== "permission"} blocked={false} /></QueryClientProvider>);
  await act(async () => { resolve(json(route, 202)); }); expect(screen.queryByRole("region", { name: "Route operation status" })).toBeNull();
});
test("withdrawal requires named confirmation and exact route version/key", async () => { const fetch = backend((url, options) => options.method === "DELETE" ? json({ ...route, enabled: false }, 202) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "Review withdrawal route_one" })); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request route withdrawal" }).disabled).toBe(true); fireEvent.click(screen.getByLabelText("I confirm withdrawal of this exact route.")); fireEvent.submit(screen.getByRole("form")); await screen.findByRole("region", { name: "Route operation status" }); expect(JSON.parse(fetch.mock.calls.find(([, o]) => o.method === "DELETE")![1].body as string)).toMatchObject({ expected_route_version: 1 }); });
test("503 blocks new request prerequisites and preserves hostname draft", async () => { backend((url) => url.includes("/deployments") ? json({}, 503) : undefined); setup(); await screen.findByText(/Unable to refresh routes/); fireEvent.change(screen.getByLabelText("Route hostname"), { target: { value: "draft.example.test" } }); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save desired route" }).disabled).toBe(true); expect(screen.getByLabelText<HTMLInputElement>("Route hostname").value).toBe("draft.example.test"); });
test("API permission denial pauses exact retry until permission scope is refreshed", async () => {
  backend((url, options) => options.method === "POST" ? json({}, 403) : undefined); setup(); await draft(); fireEvent.submit(screen.getByRole("form")); await screen.findByText(/Route mutations paused after permission denial/); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical route request" }).disabled).toBe(true);
});
test("generated hostname request is explicit and sends no caller upstream or invented hostname", async () => {
  const fetch = backend((url, options) => options.method === "POST" ? json(route, 202) : undefined); setup(); await draft(); fireEvent.click(screen.getByLabelText("Request generated hostname")); expect(screen.queryByLabelText("Route hostname")).toBeNull(); fireEvent.submit(screen.getByRole("form")); await screen.findByRole("region", { name: "Route operation status" }); expect(JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "POST")![1].body as string)).toMatchObject({ hostname: "", generate_hostname: true, expected_healthy_operation_id: "hop_healthy" });
});

test("certificate expiry is labeled as a recorded observation", async () => { backend((url) => url.includes("/resources/resource/routes") ? json([{ ...route, certificate: { ...route.certificate, expires_at: "2026-11-01T00:00:00Z" } }]) : undefined); setup(false); await screen.findByText("Certificate expiry recorded"); expect(screen.getByText("2026-11-01T00:00:00Z")).toBeTruthy(); });
test.each(["mutation", "restore"])("recorded %s ownership hold pauses separate route admission without an unlock", async (hold) => {
  backend((url) => url.includes("/proxy-operations/") ? json({ id: "pop_one", state: "uncertain", mutation_hold: hold === "mutation", restore_ownership_held: hold === "restore", routes: [route] }) : undefined); setup(); await draft(); fireEvent.click(screen.getByRole("button", { name: "View route operation pop_one" }));
  await screen.findByText(hold === "mutation" ? /New route requests are paused/ : /Proxy ownership held after restore/);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save desired route" }).disabled).toBe(true);
  expect(screen.queryByRole("button", { name: /unlock|clear hold/i })).toBeNull();
});

test("settled local build route candidate displays recorded source and artifact identity", async () => {
  backend((url) => url.includes("/deployments") ? json({ items: [{ ...healthy, snapshot: { image_reference: "", local_build: { source_generation: 3, artifact: { build_id: "build-exact", image_id: "sha256:exact" } } } }], limit: 20, offset: 0, count: 1, total: 1 }) : undefined);
  setup(); await screen.findByRole("option", { name: "hop_healthy · Local build build-exact · source generation 3 · sha256:exact" });
});
