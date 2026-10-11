import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { allProjectsQuery, auditEventsQuery, projectsQuery, runLogsQuery, runQuery, runsQuery } from "./queries";
import { useProjectMutations, useRunMutations } from "./mutations";
import { useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "./polling";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; vi.useRealTimers(); });
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
function setup() { const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>; return { client, wrapper }; }

test("mounted project mutations target only project cache", async () => {
  const counts = new Map<string, number>();
  globalThis.fetch = vi.fn<typeof fetch>((path, init) => { const key = String(path); counts.set(key, (counts.get(key) ?? 0) + 1); if (init?.method === "POST") return Promise.resolve(json({ id: "proj_new", name: "New", description: "" })); return Promise.resolve(json({ items: [], count: 0, total: 0, limit: 100, offset: 0 })); });
  const { wrapper } = setup();
  function Observer() { useQuery(projectsQuery()); useQuery(allProjectsQuery()); useQuery(runsQuery()); useQuery(runLogsQuery({ run_id: "run_1", limit: 100, offset: 0 })); useQuery(auditEventsQuery()); return useProjectMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBeGreaterThanOrEqual(1));
  const initialProjectRequests = counts.get("/api/v1/projects") ?? 0;
  await act(async () => { await mounted.result.current.create.mutateAsync({ name: "New", description: "" }); });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(initialProjectRequests + 2));
  await act(async () => { await mounted.result.current.update.mutateAsync({ id: "proj_new", name: "Renamed", description: "" }); });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(initialProjectRequests + 4));
  expect(counts.get("/api/v1/runs")).toBe(1); expect(counts.get("/api/v1/audit-events")).toBe(1); expect(counts.get("/api/v1/run-logs?run_id=run_1&limit=100&offset=0")).toBe(1);
});

test("project mutations refresh the all-project lookup cache", async () => {
  let requests = 0;
  globalThis.fetch = vi.fn<typeof fetch>((_path, init) => {
    requests += 1;
    if (init?.method === "POST") return Promise.resolve(json({ id: "proj_new", name: "New", description: "" }));
    return Promise.resolve(json({ items: [], count: 0, total: 0, limit: 100, offset: 0 }));
  });
  const { wrapper } = setup();
  function Observer() { useQuery(allProjectsQuery()); return useProjectMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(requests).toBe(1));
  await act(async () => { await mounted.result.current.create.mutateAsync({ name: "New", description: "" }); });
  await waitFor(() => expect(requests).toBe(3));
});

test("mounted failed project mutation performs one request without refresh", async () => {
  const counts = new Map<string, number>();
  globalThis.fetch = vi.fn<typeof fetch>((path, init) => {
    const key = String(path); counts.set(key, (counts.get(key) ?? 0) + 1);
    if (init?.method === "POST") return Promise.resolve(new Response(JSON.stringify({ message: "invalid project" }), { status: 422, headers: { "Content-Type": "application/json" } }));
    return Promise.resolve(json({ items: [], count: 0, total: 0, limit: 100, offset: 0 }));
  });
  const { wrapper } = setup();
  function Observer() { useQuery(projectsQuery()); useQuery(runsQuery()); useQuery(auditEventsQuery()); return useProjectMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(1));
  await expect(mounted.result.current.create.mutateAsync({ name: "Invalid", description: "" })).rejects.toThrow();
  expect(counts.get("/api/v1/projects")).toBe(2);
  expect(counts.get("/api/v1/runs")).toBe(1); expect(counts.get("/api/v1/audit-events")).toBe(1);
});

test("mounted run mutation targets runs and direct support only", async () => {
  const counts = new Map<string, number>();
  globalThis.fetch = vi.fn<typeof fetch>((path, init) => { const key = String(path); counts.set(key, (counts.get(key) ?? 0) + 1); if (key === "/api/v1/runs/cancel") return Promise.resolve(json({ id: "run_1" })); return Promise.resolve(json({ items: [], count: 0, total: 0, limit: 100, offset: 0 })); });
  const { wrapper } = setup();
  function Observer() { useQuery(runsQuery()); useQuery(runQuery("run_1")); useQuery(runLogsQuery({ run_id: "run_1", limit: 100, offset: 0 })); useQuery(projectsQuery()); useQuery(auditEventsQuery()); return useRunMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(counts.get("/api/v1/runs")).toBe(1));
  await act(async () => { await mounted.result.current.cancel.mutateAsync("run_1"); });
  await waitFor(() => expect(counts.get("/api/v1/runs")).toBe(2));
  expect(counts.get("/api/v1/run-logs?run_id=run_1&limit=100&offset=0")).toBe(2); expect(counts.get("/api/v1/projects")).toBe(1); expect(counts.get("/api/v1/audit-events")).toBe(1);
  expect(counts.get("/api/v1/runs/run_1")).toBe(2);
});

test("mounted runs polling stops after terminal", async () => {
  vi.useFakeTimers(); let calls = 0;
  globalThis.fetch = vi.fn<typeof fetch>(() => Promise.resolve(json({ items: [{ id: "run_1", status: calls++ ? "succeeded" : "running" }], count: 1, total: 1, limit: 100, offset: 0 })));
  const { wrapper } = setup(); const mounted = renderHook(() => useRunsPollingQuery(), { wrapper });
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); }); expect(calls).toBeGreaterThanOrEqual(2); const terminalCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); }); expect(calls).toBe(terminalCalls); mounted.unmount();
});

test("mounted selected-log polling stops after terminal", async () => {
  vi.useFakeTimers(); let logCalls = 0; const { wrapper } = setup();
  globalThis.fetch = vi.fn<typeof fetch>((path) => Promise.resolve(json(String(path).includes("run-logs") ? { items: [{ id: `log_${++logCalls}`, run_id: "run_1", sequence: logCalls, stream: "stdout", message: "ok" }], count: 1, total: 1, limit: 100, offset: 0 } : { items: [], count: 0, total: 0, limit: 100, offset: 0 })));
  let following = true;
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery("run_1", following), { wrapper });
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
  expect(logCalls).toBeGreaterThanOrEqual(2);
  following = false;
  mounted.rerender();
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  const terminalCalls = logCalls;
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
  expect(logCalls).toBe(terminalCalls);
  mounted.unmount();
});

test("selected logs advance beyond one hundred and can load bounded earlier history", async () => {
  vi.useFakeTimers();
  let tailRequests = 0;
  const log = (sequence: number) => ({ id: `log_${sequence}`, run_id: "run_1", sequence, stream: "stdout", message: `line ${sequence}` });
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    const url = String(path);
    if (url.includes("limit=1&offset=0")) return Promise.resolve(json({ items: [log(0)], count: 1, total: 150, limit: 1, offset: 0 }));
    if (url.includes("limit=100&offset=50")) return Promise.resolve(json({ items: Array.from({ length: 100 }, (_, index) => log(index + 50)), count: 100, total: 150, limit: 100, offset: 50 }));
    if (url.includes("limit=100&offset=150")) {
      tailRequests += 1;
      const items = tailRequests > 1 ? [log(150)] : [];
      return Promise.resolve(json({ items, count: items.length, total: 150 + items.length, limit: 100, offset: 150 }));
    }
    if (url.includes("limit=50&offset=0")) return Promise.resolve(json({ items: Array.from({ length: 50 }, (_, index) => log(index)), count: 50, total: 151, limit: 50, offset: 0 }));
    throw new Error(`unexpected request ${url}`);
  });
  const { wrapper } = setup();
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery("run_1", true), { wrapper });
  for (let index = 0; index < 5; index += 1) await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(mounted.result.current.data).toHaveLength(100);
  expect(mounted.result.current.hasPrevious).toBe(true);
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
  for (let index = 0; index < 3; index += 1) await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(mounted.result.current.data.at(-1)?.sequence).toBe(150);
  await act(async () => { await mounted.result.current.loadPrevious(); });
  expect(mounted.result.current.data).toHaveLength(151);
  expect(mounted.result.current.hasPrevious).toBe(false);
  mounted.unmount();
});

test("selected logs discard the previous run when selection changes", async () => {
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    const url = String(path);
    const runID = url.includes("run_id=run_second") ? "run_second" : "run_first";
    return Promise.resolve(json({ items: [{ id: `${runID}_log`, run_id: runID, sequence: 1, stream: "stdout", message: runID }], count: 1, total: 1, limit: url.includes("limit=1") ? 1 : 100, offset: 0 }));
  });
  const { wrapper } = setup();
  let runID = "run_first";
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery(runID, false), { wrapper });
  await waitFor(() => expect(mounted.result.current.data[0]?.run_id).toBe("run_first"));
  runID = "run_second";
  mounted.rerender();
  await waitFor(() => expect(mounted.result.current.data[0]?.run_id).toBe("run_second"));
  expect(mounted.result.current.data.every((entry) => entry.run_id === "run_second")).toBe(true);
  mounted.unmount();
});

test("a failed log probe stops loading and can be retried for a completed run", async () => {
  let fail = true;
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    if (fail) return Promise.resolve(new Response(JSON.stringify({ error: "Logs unavailable" }), { status: 503, headers: { "Content-Type": "application/json" } }));
    const url = new URL(String(path), "http://local");
    const offset = Number(url.searchParams.get("offset"));
    const items = offset === 0 ? [{ id: "log_final", run_id: "run_done", sequence: 1, stream: "stdout", message: "Complete" }] : [];
    return Promise.resolve(json({ items, count: items.length, total: 1, limit: Number(url.searchParams.get("limit")), offset }));
  });
  const { wrapper } = setup();
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery("run_done", false), { wrapper });
  await waitFor(() => expect(mounted.result.current.isError).toBe(true));
  expect(mounted.result.current.isPending).toBe(false);
  fail = false;
  await act(async () => { await mounted.result.current.retry(); });
  await waitFor(() => expect(mounted.result.current.data[0]?.message).toBe("Complete"));
  expect(mounted.result.current.isError).toBe(false);
  mounted.unmount();
});
