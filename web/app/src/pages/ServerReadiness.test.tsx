import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import { ServerReadiness } from "./ServerReadiness";
import { readinessReasons } from "./serverReadinessReasons";
const status = { server_id: "s", profile: "basic_image_hosting.v1", status: "unknown", reason: "no_sample" };
const json = (value: unknown, code = 200) => new Response(JSON.stringify(value), { status: code });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response>) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => handler ? await handler(url, options) : json(status)); vi.stubGlobal("fetch", fetch); return fetch;
}
function setup(roles = ["runner_admin"], blocked = false) {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "u", provider: "oidc", roles });
  return render(<QueryClientProvider client={client}><ServerReadiness serverID="s" blocked={blocked} /></QueryClientProvider>);
}
async function loaded() { await screen.findByText(/Last successful refresh, local receipt/); }
test("administrator sees actionable prerequisites and independently labeled receipt and server status", async () => {
  const fetch = backend(); setup(); await loaded(); expect(screen.getByText("Server-reported status: unknown")).toBeTruthy(); expect(screen.getByText(/Linux and matching native amd64 or arm64/)).toBeTruthy(); expect(fetch.mock.calls.every(([, options]) => options.method === "GET")).toBe(true);
  for (const reason of Object.values(readinessReasons)) expect(reason.length).toBeGreaterThan(40);
});
test.each([[], ["viewer"]].map((roles) => ({ roles })))("non-administrators never read readiness %j", ({ roles }) => { const fetch = backend(); setup(roles); expect(fetch).not.toHaveBeenCalled(); });
test("parent block prevents reads", () => { const fetch = backend(); setup(["system_admin"], true); expect(fetch).not.toHaveBeenCalled(); });
test("errors pause until explicit retry and preserve only historical diagnosis", async () => {
  let fail = false; const fetch = backend(() => fail ? json({}, 503) : json(status)); setup(); await loaded(); fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Refresh server readiness" })); await screen.findByRole("alert");
  const count = fetch.mock.calls.length; fireEvent(window, new Event("focus")); fireEvent(document, new Event("visibilitychange")); await act(async () => {}); expect(fetch.mock.calls).toHaveLength(count);
  expect(screen.getByRole("region", { name: "Historical server readiness details" })).toBeTruthy(); fail = false; fireEvent.click(screen.getByRole("button", { name: "Retry server readiness" })); await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
});
test.each([401, 403, 404])("denial %s clears evidence", async (code) => {
  let fail = false; backend(() => fail ? json({}, code) : json(status)); setup(); await loaded(); fail = true; fireEvent.click(screen.getByRole("button", { name: "Refresh server readiness" })); await screen.findByRole("alert"); expect(screen.queryByRole("region", { name: "Historical server readiness details" })).toBeNull();
});
test.each(["actor", "provider", "role", "fetch", "error", "parent", "server"])("access or scope change retires reads and clears evidence: %s", async (change) => {
  let resolve!: (value: Response) => void; const fetch = backend(() => new Promise<Response>((done) => { resolve = done; })); const view = setup(); await waitFor(() => expect(resolve).toBeTypeOf("function")); const signal = fetch.mock.calls[0]![1].signal; const oldResolve = resolve;
  if (change === "parent" || change === "server") view.rerender(<QueryClientProvider client={client}><ServerReadiness serverID={change === "server" ? "other" : "s"} blocked={change === "parent"} /></QueryClientProvider>);
  else if (change === "fetch" || change === "error") { if (change === "error") backend(() => json({}, 403)); await act(async () => { void client.invalidateQueries({ queryKey: queryKeys.principal }); }); }
  else await act(async () => { client.setQueryData(queryKeys.principal, { id: change === "actor" ? "other" : "u", provider: change === "provider" ? "local" : "oidc", roles: change === "role" ? [] : ["runner_admin"] }); });
  await waitFor(() => expect(signal?.aborted).toBe(true)); await act(async () => oldResolve(json(status))); expect(screen.queryByText(/Last successful refresh/)).toBeNull();
});
test.each(["resolve-before", "reject-before", "resolve-after", "reject-after"])("timeout retires hung transport before explicit retry and ignores late completion %s", async (completion) => {
  let count = 0, resolveOld!: (value: Response) => void, rejectOld!: (value: Error) => void, resolveNew!: (value: Response) => void;
  const fetch = backend(() => { count++; if (count === 2) return new Promise<Response>((resolve, reject) => { resolveOld = resolve; rejectOld = reject; }); if (count === 3) return new Promise<Response>((resolve) => { resolveNew = resolve; }); return json(status); });
  setup(); await loaded(); vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }); fireEvent.click(screen.getByRole("button", { name: "Refresh server readiness" }));
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); }); expect(fetch.mock.calls[1]![1].signal?.aborted).toBe(true); expect(screen.getByText(/timed out after 5 seconds/)).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(20000); }); expect(count).toBe(2); fireEvent.click(screen.getByRole("button", { name: "Retry server readiness" })); expect(count).toBe(3);
  const old = async () => { await act(async () => { if (completion.startsWith("resolve")) resolveOld(json({ ...status, reason: "authority_unavailable" })); else rejectOld(new Error("late")); }); };
  if (completion.endsWith("before")) { await old(); expect(screen.getByText("Reading server readiness...")).toBeTruthy(); }
  await act(async () => resolveNew(json(status))); if (completion.endsWith("after")) await old(); expect(screen.queryByRole("alert")).toBeNull(); expect(screen.queryByText(readinessReasons.authority_unavailable)).toBeNull();
});
test("hidden document cancels without timeout and local age cannot revive on return", async () => {
  let calls = 0; const fetch = backend(() => ++calls === 1 ? json(status) : new Promise<Response>(() => {})); setup(); await loaded(); vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }); fireEvent.click(screen.getByRole("button", { name: "Refresh server readiness" }));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden"); fireEvent(document, new Event("visibilitychange")); expect(fetch.mock.calls[1]![1].signal?.aborted).toBe(true); await act(async () => { await vi.advanceTimersByTimeAsync(20000); }); expect(screen.queryByRole("alert")).toBeNull(); expect(calls).toBe(2);
});
const sample = "srd_" + "a".repeat(32);
const ready = { ...status, status: "ready", reason: "qualified", sample_id: sample, observed_at: "2026-10-23T00:00:00Z", report: { sample_id: sample, profile: "basic_image_hosting.v1", duration_ms: 1, os: "linux", architecture: "amd64", execution: "enabled", configuration: "matched", docker: "available", docker_api_version: "1.54", daemon_id: "daemon", daemon_os: "linux", daemon_architecture: "amd64", cli: "available", work_root: "accessible", journal_root: "accessible", work_free_bytes: 1073741824, journal_free_bytes: 1073741824, error: "" } };
test.each(Object.keys(readinessReasons))("server diagnosis is preserved with actionable label %s", async (reason) => {
  const state = reason === "qualified" ? "ready" : ["restore_hold", "daemon_mismatch", "prerequisite_failed"].includes(reason) ? "blocked" : "unknown";
  backend(() => json({ ...ready, status: state, reason })); setup(); await loaded(); expect(screen.getByText(`Server-reported status: ${state}`)).toBeTruthy(); expect(screen.getAllByText(readinessReasons[reason as keyof typeof readinessReasons]).length).toBeGreaterThan(0); expect(screen.getByText(/Server observed_at timestamp/)).toBeTruthy();
});
test("local expiry removes positive status while one visible poll remains in flight", async () => {
  let now = 100, wall = 1000, calls = 0; vi.spyOn(performance, "now").mockImplementation(() => now); vi.spyOn(Date, "now").mockImplementation(() => wall);
  backend(() => ++calls === 1 ? json(ready) : new Promise<Response>(() => {})); setup(); await loaded(); expect(screen.getByText("Server-reported status: ready")).toBeTruthy();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }); fireEvent.click(screen.getByRole("button", { name: "Refresh server readiness" })); now += 5000; wall += 5000; await act(async () => { await vi.advanceTimersByTimeAsync(5000); }); expect(screen.getByText("Server-reported status: unknown")).toBeTruthy(); expect(calls).toBe(2);
});
test("visible polling waits five seconds and never overlaps the pending read", async () => {
  let now = 100, wall = 1000, calls = 0; vi.spyOn(performance, "now").mockImplementation(() => now); vi.spyOn(Date, "now").mockImplementation(() => wall);
  backend(() => ++calls === 1 ? json(status) : new Promise<Response>(() => {})); setup(); await loaded(); vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  // Re-render the visibility effect so its existing real-clock timer is replaced.
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden"); fireEvent(document, new Event("visibilitychange")); vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); fireEvent(document, new Event("visibilitychange"));
  now += 4999; wall += 4999; await act(async () => { await vi.advanceTimersByTimeAsync(4999); }); expect(calls).toBe(1);
  now++; wall++; await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(calls).toBe(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(4999); }); expect(calls).toBe(2);
});
