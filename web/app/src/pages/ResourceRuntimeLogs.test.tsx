import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { runtimeLogTargetsQuery, type LogSession } from "@/api/hosting-runtime-logs";
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
function setup(compose = false) { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "u", provider: "oidc", roles: [] }); if (compose) {
  client.setQueryData(runtimeLogTargetsQuery({ actor: "u", project: "p", resource: "r" }).queryKey, { resource_id: "r", operation_id: "op", components: ["app", "db", "init"].map((component) => ({ component, running: component !== "init", exit_code: 0 })) });
} return render(<QueryClientProvider client={client}><ResourceRuntimeLogs resource={compose ? { ...resource, source: { kind: "compose", compose: {} } } as Resource : resource} blocked={false} /></QueryClientProvider>); }
async function click(name: string) { await act(async () => { fireEvent.click(screen.getByRole("button", { name })); }); }
async function advance(ms: number) { for (let remaining = ms; remaining > 0;) { const step = Math.min(1000, remaining); now += step; wall += step; await act(async () => { await vi.advanceTimersByTimeAsync(step); }); remaining -= step; } }
function validLateResult(kind: string, options: RequestInit) {
  const late = structuredClone(session);
  if (kind === "open") late.request_id = JSON.parse(String(options.body)).request_id;
  return kind === "read" ? { session: late, items: [{ sequence: 2, component: "application", stream: "stdout", message: "late sensitive row must not return", received_at: time }], next_sequence: 2, earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true } : late;
}
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
  const late = validLateResult(kind, attempt[1]);
  await click("Stop following"); expect(attempt[1].signal?.aborted).toBe(true); const before = screen.queryAllByText(/Server sequence/).length;
  await act(async () => resolve(json(late))); expect(screen.queryByText(/Reading or updating/)).toBeNull(); expect(screen.queryAllByText(/Server sequence/)).toHaveLength(before); expect(screen.queryByText(/Following requested while visible/)).toBeNull(); expect(screen.queryByText("late sensitive row must not return")).toBeNull(); if (kind !== "open") expect(screen.getByText(/Session state: ended/)).toBeTruthy();
});
test("hung read retires at5s, pauses renew and enables explicit retry while late response is ignored", async () => {
  let hang = false, resolve!: (response: Response) => void; const fetch = backend((url) => hang && url.includes("/logs?") ? new Promise<Response>((done) => { resolve = done; }) : undefined); setup(); await click("Follow runtime logs"); hang = true; await advance(3000);
  const signal = fetch.mock.calls.at(-1)![1].signal, late = validLateResult("read", fetch.mock.calls.at(-1)![1]); await advance(5000); expect(signal?.aborted).toBe(true); expect(screen.getByRole("alert")).toBeTruthy(); expect(screen.queryByText(/Reading or updating/)).toBeNull(); const count = fetch.mock.calls.length;
  await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count); hang = false; await click("Retry runtime logs"); await act(async () => resolve(json(late))); expect(screen.queryByRole("alert")).toBeNull(); expect(screen.getByText(/Server sequence 1/)).toBeTruthy(); expect(screen.queryByText("late sensitive row must not return")).toBeNull();
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
  const late = validLateResult(kind, attempt[1]);
  view.rerender(<QueryClientProvider client={client}><ResourceRuntimeLogs resource={{ ...resource, id: "another" }} blocked={false} /></QueryClientProvider>); expect(attempt[1].signal?.aborted).toBe(true);
  await act(async () => resolve(json(late))); expect(screen.queryByText(/Server sequence/)).toBeNull(); expect(screen.queryByText("op / container")).toBeNull(); const count = fetch.mock.calls.length; await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count);
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

test("Compose selection opens no demand and switching closes, clears and follows only explicitly", async () => {
  const fetch = backend((url, options) => {
    if (url.endsWith("/runtime-log-sessions")) {
      const body = JSON.parse(String(options.body));
      session = { ...structuredClone(original), request_id: body.request_id, target: { ...original.target, component: body.component, container_name: "ncd_" + "a".repeat(32) } };
      return json(session);
    }
    if (url.includes("/logs?")) return json({ session, items: [{ sequence: 1, component: session.target.component, stream: "stdout", message: "private " + session.target.component, received_at: time }], next_sequence: 1, earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true });
  });
  setup(true); expect(fetch).not.toHaveBeenCalled();
  expect(screen.getByRole("option", { name: "init · stopped, exit 0" })).toBeTruthy();
  await act(async () => fireEvent.change(screen.getByRole("combobox", { name: "Compose component" }), { target: { value: "db" } }));
  expect(fetch).not.toHaveBeenCalled(); await click("Follow runtime logs");
  expect(JSON.parse(String(fetch.mock.calls[0]![1].body))).toMatchObject({ component: "db", expected_operation_id: "op" });
  expect(screen.getByText("private db")).toBeTruthy(); expect(screen.getByText("db / ncd_" + "a".repeat(32))).toBeTruthy();
  await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "init" } }));
  expect(fetch.mock.calls.filter(([, options]) => options.method === "DELETE")).toHaveLength(1);
  expect(screen.queryByText("private db")).toBeNull(); const count = fetch.mock.calls.length;
  await advance(4000); expect(fetch).toHaveBeenCalledTimes(count);
  await click("Follow runtime logs"); expect(screen.getByText("private init")).toBeTruthy();
});
test.each([403, 409])("target discovery HTTP%s removes choices without a legacy Follow fallback", async (status) => {
  const fetch = backend((url) => url.endsWith("/runtime-log-targets") ? json({}, status) : undefined); setup(true);
  await click("Refresh component targets"); await advance(1);
  expect(screen.queryByRole("button", { name: "Follow runtime logs" })).toBeNull();
  expect(screen.getByText(/Settled Compose component evidence is pending or unavailable/)).toBeTruthy();
  expect(fetch.mock.calls).toHaveLength(1);
});

test.each(["open", "read"])("component change retires a pending %s and rejects its late output", async (kind) => {
  let resolve!: (response: Response) => void, pending = false;
  const fetch = backend((url, options) => {
    if (pending && (kind === "open" ? url.endsWith("/runtime-log-sessions") : url.includes("/logs?"))) return new Promise<Response>((done) => { resolve = done; });
    if (url.endsWith("/runtime-log-sessions")) { const body = JSON.parse(String(options.body)); session = { ...structuredClone(original), request_id: body.request_id, target: { ...original.target, component: body.component, container_name: "ncd_" + "a".repeat(32) } }; return json(session); }
    if (url.includes("/logs?")) return json({ session, items: [], next_sequence: 0, earliest_available_sequence: 0, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true });
  });
  setup(true); await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "app" } }));
  pending = kind === "open"; await click("Follow runtime logs");
  if (kind === "read") { pending = true; await advance(3000); }
  const attempt = fetch.mock.calls.at(-1)!;
  const lateSession = { ...structuredClone(original), request_id: kind === "open" ? JSON.parse(String(attempt[1].body)).request_id : session.request_id, target: { ...original.target, component: "app", container_name: "ncd_" + "a".repeat(32) } };
  await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "db" } }));
  expect(attempt[1].signal?.aborted).toBe(true);
  const late = kind === "open" ? lateSession : { session: lateSession, items: [{ sequence: 1, component: "app", stream: "stdout", message: "late component secret", received_at: time }], next_sequence: 1, earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true };
  pending = false; await act(async () => resolve(json(late)));
  expect(screen.queryByText("late component secret")).toBeNull(); expect(screen.queryByText(/Immutable session/)).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "DELETE")).toHaveLength(1);
  const count = fetch.mock.calls.length; await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count);
});

test("component discovery uses settled runtime targets independently of redeploy availability", async () => {
  const fetch = backend((url) => url.endsWith("/runtime-log-targets") ? json({ resource_id: "r", operation_id: "settled", components: [{ component: "init", running: false, exit_code: 0 }] }) : json({ reason: "https_unavailable" }, 409));
  setup(true); await click("Refresh component targets");
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/resources/r/runtime-log-targets"]);
  expect(screen.getByRole("option", { name: "init · stopped, exit 0" })).toBeTruthy();
  await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "init" } }));
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "Follow runtime logs" })).toBeTruthy();
});
test("refreshing component targets closes the prior session and requires selection and Follow again", async () => {
  const fetch = backend((url, options) => {
    if (url.endsWith("/runtime-log-targets")) return json({ resource_id: "r", operation_id: "successor", components: [{ component: "db", running: true, exit_code: 0 }] });
    if (url.endsWith("/runtime-log-sessions")) { const body = JSON.parse(String(options.body)); session = { ...structuredClone(original), request_id: body.request_id, target: { ...original.target, component: body.component, container_name: "ncd_" + "a".repeat(32) } }; return json(session); }
    if (url.includes("/logs?")) return json({ session, items: [{ sequence: 1, component: "db", stream: "stdout", message: "old sensitive output", received_at: time }], next_sequence: 1, earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, truncated: false, markers: [], sensitive: true });
  });
  setup(true); await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "db" } })); await click("Follow runtime logs");
  expect(screen.getByText("old sensitive output")).toBeTruthy(); await click("Refresh component targets");
  expect(screen.queryByText("old sensitive output")).toBeNull(); expect(screen.queryByRole("button", { name: "Follow runtime logs" })).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "DELETE")).toHaveLength(1);
  const count = fetch.mock.calls.length; await advance(10_000); expect(fetch).toHaveBeenCalledTimes(count);
});

test.each(["app", "db", "init"])("Compose %s requires the selected reader to enter the viewport before Follow can POST", async component => {
  let intersect!: (visible: boolean) => void;
  vi.stubGlobal("IntersectionObserver", class {
    constructor(callback: (entries: { isIntersecting: boolean }[]) => void) { intersect = visible => callback([{ isIntersecting: visible }]); }
    observe() {}
    disconnect() {}
  });
  const fetch = backend(url => url.endsWith("/runtime-log-sessions") ? new Promise<Response>(() => {}) : undefined);
  setup(true);
  await act(async () => fireEvent.change(screen.getByRole("combobox", { name: "Compose component" }), { target: { value: component } }));
  const follow = screen.getByRole<HTMLButtonElement>("button", { name: "Follow runtime logs" });
  expect(follow.disabled).toBe(true);
  await act(async () => intersect(false));
  await click("Follow runtime logs");
  expect(fetch).not.toHaveBeenCalled();
  await act(async () => intersect(true));
  expect(follow.disabled).toBe(false);
  expect(fetch).not.toHaveBeenCalled();
  await click("Follow runtime logs");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(JSON.parse(String(fetch.mock.calls[0]![1].body))).toMatchObject({ component, expected_operation_id: "op" });
});
