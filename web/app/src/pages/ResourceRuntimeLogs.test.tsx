import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import type { LogSession } from "@/api/hosting-runtime-logs";
import { ResourceRuntimeLogs } from "./ResourceRuntimeLogs";
const time = "2026-10-23T00:00:00Z", digest = "sha256:" + "a".repeat(64);
const resource: Resource = { id: "r", project_id: "p", project_environment_id: "e", name: "Web", kind: "application", generation: 1, source: { kind: "image", image: { reference: "web:1" } }, created_by: "u", created_at: "", updated_at: "" };
const original: LogSession = { id: "rls_" + "1".repeat(32), request_id: "a".repeat(32), resource_id: "r", project_id: "p", actor_id: "u", target: { resource_id: "r", binding_id: "b", destination_id: "d", server_id: "s", runner_id: "a", operation_id: "op", daemon_id: "daemon", container_id: "container", image_id: digest, release_hash: digest, plan_hash: digest, runtime_namespace: "test" }, state: "following", created_at: time, demand_expires_at: "2026-10-23T00:00:30Z", ended_at: null, purge_after: "2026-10-23T00:05:30Z", server_time: time, sensitive: true };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let client: QueryClient, session: LogSession, now = 100, wall = Date.parse(time);
beforeEach(() => { session = structuredClone(original); now = 100; wall = Date.parse(time); vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] }); vi.spyOn(performance, "now").mockImplementation(() => now); vi.spyOn(Date, "now").mockImplementation(() => wall); vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); });
afterEach(() => { cleanup(); client?.clear(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const custom = handler?.(url, options); if (custom !== undefined) return await custom;
    if (url.endsWith("/runtime-log-sessions")) { session.request_id = JSON.parse(String(options.body)).request_id; return json(session); }
    if (url.includes("/logs?")) { const after = Number(new URL(url, "http://local").searchParams.get("after_sequence")); return json({ session, items: after ? [] : [{ sequence: 1, component: "application", stream: "stdout", message: "<img src=x onerror=alert(1)>\u001b[31m literal", received_at: time }], next_sequence: Math.max(after, 1), earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true }); }
    if (options.method === "DELETE") { session = { ...session, state: "ended", ended_at: time }; return json(session); }
    return json(session);
  }); vi.stubGlobal("fetch", fetch); return fetch;
}
function setup() { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "u", provider: "oidc", roles: [] }); return render(<QueryClientProvider client={client}><ResourceRuntimeLogs resource={resource} blocked={false} /></QueryClientProvider>); }
async function click(name: string) { await act(async () => { fireEvent.click(screen.getByRole("button", { name })); }); }
async function advance(ms: number) { for (let remaining = ms; remaining > 0;) { const step = Math.min(1000, remaining); now += step; wall += step; await act(async () => { await vi.advanceTimersByTimeAsync(step); }); remaining -= step; } }
test("initial viewer render opens no demand or plaintext; explicit Follow escapes HTML and ANSI", async () => {
  const fetch = backend(); setup(); expect(fetch).not.toHaveBeenCalled(); expect(screen.getByText(/Sensitive application output may contain secrets/)).toBeTruthy();
  await click("Follow runtime logs"); expect(screen.getByText(/<img src=x onerror=alert\(1\)>\\u001b\[31m literal/)).toBeTruthy(); expect(document.querySelector("img")).toBeNull(); expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(true);
});
test("lost open response retries the exact frozen body and never automatically reopens", async () => {
  let lose = true; const fetch = backend((url) => url.endsWith("/runtime-log-sessions") && lose ? Promise.reject(new Error("response lost")) : undefined); setup(); await click("Follow runtime logs");
  const first = fetch.mock.calls[0]![1].body; await advance(10_000); expect(fetch).toHaveBeenCalledTimes(1); lose = false; await click("Retry runtime logs"); expect(fetch.mock.calls[1]![1].body).toBe(first); expect(screen.getByText(/Server sequence 1/)).toBeTruthy();
});
test("visibility cancellation of an ambiguous open preserves its frozen request for explicit retry", async () => {
  let pending = true; const fetch = backend((url) => pending && url.endsWith("/runtime-log-sessions") ? new Promise<Response>(() => {}) : undefined); setup(); await click("Follow runtime logs"); const first = fetch.mock.calls[0]![1];
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden"); fireEvent(document, new Event("visibilitychange")); expect(first.signal?.aborted).toBe(true);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); fireEvent(document, new Event("visibilitychange")); await advance(10_000); expect(fetch).toHaveBeenCalledTimes(1);
  pending = false; await click("Retry runtime logs"); expect(fetch.mock.calls[1]![1].body).toBe(first.body); expect(screen.getByText(/Server sequence 1/)).toBeTruthy();
});
test("unconfirmed close disables renewal while preserving historical rows and explicit close retry", async () => {
  let fail = true; const fetch = backend((_url, options) => fail && options.method === "DELETE" ? json({}, 503) : undefined); setup(); await click("Follow runtime logs"); await click("Stop following"); expect(screen.getByText(/Close is unconfirmed/)).toBeTruthy(); expect(screen.getByText(/Server sequence 1/)).toBeTruthy();
  const count = fetch.mock.calls.length; await advance(11_000); expect(fetch).toHaveBeenCalledTimes(count); fail = false; await click("Retry runtime logs"); expect(screen.queryByText(/Close is unconfirmed/)).toBeNull(); expect(screen.getByText(/Session state: ended/)).toBeTruthy();
});
test.each(["open", "read", "renew"])("late %s completion after Stop cannot restore demand, plaintext or busy state", async (kind) => {
  let resolve!: (response: Response) => void, pending = false;
  const fetch = backend((url) => pending && (kind === "open" ? url.endsWith("/runtime-log-sessions") : kind === "read" ? url.includes("/logs?") : url.endsWith("/renew")) ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  setup(); if (kind === "open") pending = true; await click("Follow runtime logs");
  if (kind !== "open") { pending = true; await advance(kind === "renew" ? 10_000 : 3000); }
  const attempt = fetch.mock.calls.filter(([url]) => kind === "open" ? url.endsWith("/runtime-log-sessions") : kind === "read" ? url.includes("/logs?") : url.endsWith("/renew")).at(-1)!;
  await click("Stop following"); expect(attempt[1].signal?.aborted).toBe(true); const before = screen.queryAllByText(/Server sequence/).length;
  await act(async () => resolve(json(kind === "read" ? { invalid: "late plaintext" } : original))); expect(screen.queryByText(/Reading or updating/)).toBeNull(); expect(screen.queryAllByText(/Server sequence/)).toHaveLength(before); expect(screen.queryByText("Following requested while visible.")).toBeNull();
});
test("hung read retires at5s, pauses renew and enables explicit retry while late response is ignored", async () => {
  let hang = false, resolve!: (response: Response) => void; const fetch = backend((url) => hang && url.includes("/logs?") ? new Promise<Response>((done) => { resolve = done; }) : undefined); setup(); await click("Follow runtime logs"); hang = true; await advance(3000);
  const signal = fetch.mock.calls.at(-1)![1].signal; await advance(5000); expect(signal?.aborted).toBe(true); expect(screen.getByRole("alert")).toBeTruthy(); expect(screen.queryByText(/Reading or updating/)).toBeNull(); const count = fetch.mock.calls.length;
  await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count); hang = false; await click("Retry runtime logs"); await act(async () => resolve(json({ malformed: "old" }))); expect(screen.queryByRole("alert")).toBeNull(); expect(screen.getByText(/Server sequence 1/)).toBeTruthy();
});
test("hidden return requires explicit Resume and cannot renew expired demand under suspension", async () => {
  const fetch = backend(); setup(); await click("Follow runtime logs"); vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden"); fireEvent(document, new Event("visibilitychange")); const count = fetch.mock.calls.length;
  await advance(5000); vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); fireEvent(document, new Event("visibilitychange")); await advance(5000); expect(fetch).toHaveBeenCalledTimes(count); expect(screen.getByRole("button", { name: "Resume runtime logs" })).toBeTruthy();
  wall += 40_000; fireEvent(document, new Event("visibilitychange")); await advance(1000); expect(screen.queryByRole("button", { name: "Resume runtime logs" })).toBeNull(); expect(screen.getByText(/Demand expired or ended/)).toBeTruthy(); expect(fetch).toHaveBeenCalledTimes(count);
});
test.each([401, 403, 404, 410])("HTTP%s clears all sensitive rows and session identities", async (code) => {
  let fail = false; backend((url) => fail && url.includes("/logs?") ? json({}, code) : undefined); setup(); await click("Follow runtime logs"); fail = true; await advance(3000); expect(screen.queryByText(/Server sequence/)).toBeNull(); expect(screen.queryByText(/container/)).toBeNull(); expect(screen.getByText(code === 410 ? /Runtime log cache expired/ : /Runtime log access is unavailable/)).toBeTruthy();
});
test("409 is a neutral conflict and rows remain historical without guessed quota diagnosis", async () => {
  backend(() => json({}, 409)); setup(); await click("Follow runtime logs"); expect(screen.getByText(/No quota or target cause is assumed/)).toBeTruthy();
});
test("resource scope and unknown principal clear plaintext and abort an in-flight read", async () => {
  let pending = false; const fetch = backend((url) => pending && url.includes("/logs?") ? new Promise<Response>(() => {}) : undefined); const view = setup(); await click("Follow runtime logs"); pending = true; await advance(3000);
  view.rerender(<QueryClientProvider client={client}><ResourceRuntimeLogs resource={resource} blocked /></QueryClientProvider>); expect(fetch.mock.calls.at(-1)![1].signal?.aborted).toBe(true); expect(screen.queryByText(/Server sequence/)).toBeNull();
});
test.each(["open", "read", "renew"])("scope change clears sensitive state and ignores a late %s result", async (kind) => {
  let resolve!: (response: Response) => void, pending = false;
  const fetch = backend((url) => pending && (kind === "open" ? url.endsWith("/runtime-log-sessions") : kind === "read" ? url.includes("/logs?") : url.endsWith("/renew")) ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  const view = setup(); if (kind === "open") pending = true; await click("Follow runtime logs"); if (kind !== "open") { pending = true; await advance(kind === "renew" ? 10_000 : 3000); }
  const attempt = fetch.mock.calls.filter(([url]) => kind === "open" ? url.endsWith("/runtime-log-sessions") : kind === "read" ? url.includes("/logs?") : url.endsWith("/renew")).at(-1)!;
  view.rerender(<QueryClientProvider client={client}><ResourceRuntimeLogs resource={{ ...resource, id: "another" }} blocked={false} /></QueryClientProvider>); expect(attempt[1].signal?.aborted).toBe(true);
  await act(async () => resolve(json(original))); expect(screen.queryByText(/Server sequence/)).toBeNull(); expect(screen.queryByText("op / container")).toBeNull(); const count = fetch.mock.calls.length; await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count);
});
test("Hide output clears plaintext and Show controls never reopens a session", async () => {
  const fetch = backend(); setup(); await click("Follow runtime logs"); const count = fetch.mock.calls.length; await click("Hide output"); expect(screen.queryByText(/Server sequence/)).toBeNull(); await click("Show runtime log controls"); await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count);
});
test("old-target closed history drains bounded pages and distinguishes source gap, retention and clipping", async () => {
  session.state = "target_changed"; session.ended_at = time;
  backend((url) => {
    if (!url.includes("/logs?")) return;
    const after = Number(new URL(url, "http://local").searchParams.get("after_sequence"));
    return json({ session, items: [{ sequence: after ? 3 : 2, component: "application", stream: "stderr", message: after ? "clipped historical row" : "earlier retained row", message_truncated: Boolean(after), source_timestamp: time, received_at: time }], next_sequence: after ? 3 : 2, earliest_available_sequence: 2, deleted_through_sequence: 1, gap: true, truncated: !after, markers: ["source_gap", "prefix_evicted"], sensitive: true });
  }); setup(); await click("Follow runtime logs"); expect(screen.getByText(/The target changed/)).toBeTruthy(); expect(screen.getByText(/Source gap:/)).toBeTruthy(); expect(screen.getByText(/Retention prefix removed/)).toBeTruthy(); expect(screen.getByText(/Page truncated:/)).toBeTruthy();
  await advance(3000); expect(screen.getByText(/Message clipped by producer or server/)).toBeTruthy(); expect(screen.getByText("Closed session history is fully drained.")).toBeTruthy(); expect(screen.getByText("op / container")).toBeTruthy(); expect(screen.queryByRole("button", { name: "Resume runtime logs" })).toBeNull();
});
