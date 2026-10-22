import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import type { HostingRuntimeStatus } from "@/api/hosting-runtime-status";
import { ResourceRuntimeStatus } from "./ResourceRuntimeStatus";
const resource: Resource = { id: "r", project_id: "p", project_environment_id: "e", name: "Web", kind: "application", generation: 1, source: { kind: "image", image: { reference: "web:1" } }, created_by: "u", created_at: "", updated_at: "" };
const time = "2026-10-23T00:00:00Z";
const status: HostingRuntimeStatus = { resource_id: "r", binding_id: "binding", destination_id: "d", server_id: "s", runner_id: "a", current_operation_id: "op", current_container_id: "container", observed_operation_id: "op", observed_container_id: "container", observed_at: time, server_time: time, stale_after_seconds: 30, target_interval_seconds: 10, sample_timeout_seconds: 5, freshness: "fresh", reason: "observed", agent_reachability: "reachable", last_heartbeat_at: time, container_state: "running", readiness: "ready", collection_error: "", mutation_hold: false, restore_ownership_held: false, last_observation: { operation_id: "op", container_id: "container", observed_at: time, observation: { binding_id: "binding", sample_id: "sample", sample_duration_ms: 3, container_state: "running", readiness: "ready", collection_error: "" } } };
const json = (value: unknown, code = 200) => new Response(JSON.stringify(value), { status: code });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? (url.includes("/project-members") ? json({ items: [{ project_id: "p", user_id: "u", role: "viewer" }], count: 1, total: 1, offset: 0 }) : json(status)));
  vi.stubGlobal("fetch", fetch); return fetch;
}
function setup() {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "u", provider: "oidc", roles: [] });
  return render(<QueryClientProvider client={client}><ResourceRuntimeStatus resource={resource} blocked={false} /></QueryClientProvider>);
}
const current = () => within(screen.getByRole("region", { name: "Current runtime observation" }));
async function loaded() { await waitFor(() => expect(current().getByText("Container state: Running")).toBeTruthy()); }
test("viewer reads independent current observation, heartbeat, placement and historical evidence without writes", async () => {
  const fetch = backend(); setup(); await loaded(); expect(current().getByText("Readiness: Ready")).toBeTruthy(); expect(screen.getByText("Agent reachability: Reachable, recent heartbeat")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Saved status placement" })).toBeTruthy(); expect(screen.getByRole("region", { name: "Settled deployment target" })).toBeTruthy(); expect(screen.getByRole("region", { name: "Historical runtime evidence" })).toBeTruthy(); expect(screen.queryByRole("form")).toBeNull(); expect(fetch.mock.calls.every(([, options]) => options.method === "GET")).toBe(true);
});
test("changed target keeps old evidence historical and current readiness unknown", async () => {
  const fetch = backend((url) => url.includes("runtime-status") ? json({ ...status, current_container_id: "replacement", current_operation_id: "new-op", freshness: "unknown", reason: "target_changed", container_state: "unknown", readiness: "unknown" }) : undefined);
  setup(); await screen.findByText("The settled target changed. Earlier evidence is historical."); expect(current().getByText("Container state: Unknown")).toBeTruthy(); expect(within(screen.getByRole("region", { name: "Settled deployment target" })).queryByText("container", { exact: true })).toBeNull(); expect(within(screen.getByRole("region", { name: "Historical runtime evidence" })).getByText("container", { exact: true })).toBeTruthy(); expect(fetch.mock.calls.length).toBeGreaterThan(0);
});
test("HTTP error preserves only historical evidence and requires explicit retry without focus hot retries", async () => {
  let fail = false; const fetch = backend((url) => fail && url.includes("runtime-status") ? json({}, 503) : undefined); setup(); await loaded(); fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh runtime status" })); await screen.findByText(/Unable to read current runtime status/); expect(current().getByText("Readiness: Unknown")).toBeTruthy(); expect(screen.getByText("Recorded container state")).toBeTruthy();
  const count = fetch.mock.calls.length; fireEvent(window, new Event("focus")); fireEvent(document, new Event("visibilitychange")); await act(async () => {}); expect(fetch.mock.calls).toHaveLength(count);
  fail = false; fireEvent.click(screen.getByRole("button", { name: "Retry runtime status" })); await loaded();
});
test.each([401, 403, 404])("access HTTP %s clears current and historical evidence", async (code) => {
  let fail = false; backend((url) => fail && url.includes("runtime-status") ? json({}, code) : undefined); setup(); await loaded(); fail = true; fireEvent.click(screen.getByRole("button", { name: "Refresh runtime status" })); await screen.findByText(/Runtime status access is unavailable/); expect(screen.queryByText("container", { exact: true })).toBeNull(); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Refresh runtime status" }).disabled).toBe(true);
});
test("scope change aborts pending reads and discards late responses", async () => {
  let resolve!: (response: Response) => void; const fetch = backend((url) => url.includes("runtime-status") ? new Promise<Response>((done) => { resolve = done; }) : undefined); const view = setup(); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  const request = fetch.mock.calls.find(([url]) => url.includes("runtime-status"))![1]; view.rerender(<QueryClientProvider client={client}><ResourceRuntimeStatus resource={{ ...resource, project_id: "other", id: "other" }} blocked /></QueryClientProvider>); expect(request.signal?.aborted).toBe(true); await act(async () => resolve(json(status))); expect(screen.queryByText("container", { exact: true })).toBeNull();
});
test("membership refresh clears evidence while cached viewer access is pending", async () => {
  let pending = false; backend((url) => pending && url.includes("/project-members") ? new Promise<Response>(() => {}) : undefined); setup(); await loaded(); pending = true; fireEvent.click(screen.getByRole("button", { name: "Refresh runtime status access" })); await screen.findByText(/Current runtime status access cannot be verified/); expect(screen.queryByText("container", { exact: true })).toBeNull();
});
test("revoked membership removes evidence and stops further status reads", async () => {
  let revoked = false; const fetch = backend((url) => revoked && url.includes("/project-members") ? json({ items: [], count: 0, total: 0, offset: 0 }) : undefined); setup(); await loaded(); revoked = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh runtime status access" })); await screen.findByText("Project viewer access is required to read runtime status.");
  expect(screen.queryByText("container", { exact: true })).toBeNull(); const reads = fetch.mock.calls.filter(([url]) => url.includes("runtime-status")).length;
  fireEvent(window, new Event("focus")); fireEvent(document, new Event("visibilitychange")); await act(async () => {}); expect(fetch.mock.calls.filter(([url]) => url.includes("runtime-status"))).toHaveLength(reads);
});
test("principal revalidation clears protected evidence before cached identity is trusted", async () => {
  backend((url) => url === "/api/v1/me" ? new Promise<Response>(() => {}) : undefined); setup(); await loaded();
  await act(async () => { void client.invalidateQueries({ queryKey: queryKeys.principal }); });
  await screen.findByText("Runtime status access is pending or unavailable."); expect(screen.queryByText("container", { exact: true })).toBeNull();
});
test("local age expires during a disconnected poll and hidden-tab return does not revive freshness", async () => {
  let now = 1000, wall = Date.parse(time), calls = 0; vi.spyOn(performance, "now").mockImplementation(() => now); vi.spyOn(Date, "now").mockImplementation(() => wall);
  backend((url) => url.includes("runtime-status") && ++calls > 1 ? new Promise<Response>(() => {}) : undefined); setup(); await loaded();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }); fireEvent(document, new Event("visibilitychange")); fireEvent.click(screen.getByRole("button", { name: "Refresh runtime status" })); now += 29_000; wall += 29_000; await act(async () => { await vi.advanceTimersByTimeAsync(29_000); }); expect(current().getByText("Container state: Running")).toBeTruthy();
  now += 1000; wall += 1000; await act(async () => { await vi.advanceTimersByTimeAsync(1000); }); expect(current().getByText("Container state: Unknown")).toBeTruthy(); expect(screen.getByText("Agent reachability: Unknown")).toBeTruthy();
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden"); fireEvent(document, new Event("visibilitychange")); now += 100_000; wall += 100_000; vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); fireEvent(document, new Event("visibilitychange")); expect(current().getByText("Readiness: Unknown")).toBeTruthy();
});
