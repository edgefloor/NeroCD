import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { auditEventsQuery, projectsQuery, queryKeys, runLogsQuery, runsQuery } from "./queries";
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
  function Observer() { useQuery(projectsQuery()); useQuery(runsQuery()); useQuery(runLogsQuery({ run_id: "run_1", limit: 100, offset: 0 })); useQuery(auditEventsQuery()); return useProjectMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(1));
  await act(async () => { await mounted.result.current.create.mutateAsync({ name: "New", description: "" }); });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(3));
  await act(async () => { await mounted.result.current.update.mutateAsync({ id: "proj_new", name: "Renamed", description: "" }); });
  await waitFor(() => expect(counts.get("/api/v1/projects")).toBe(5));
  expect(counts.get("/api/v1/runs")).toBe(1); expect(counts.get("/api/v1/audit-events")).toBe(1); expect(counts.get("/api/v1/run-logs?run_id=run_1&limit=100&offset=0")).toBe(1);
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
  function Observer() { useQuery(runsQuery()); useQuery(runLogsQuery({ run_id: "run_1", limit: 100, offset: 0 })); useQuery(projectsQuery()); useQuery(auditEventsQuery()); return useRunMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(counts.get("/api/v1/runs")).toBe(1));
  await act(async () => { await mounted.result.current.cancel.mutateAsync("run_1"); });
  await waitFor(() => expect(counts.get("/api/v1/runs")).toBe(2));
  expect(counts.get("/api/v1/run-logs?run_id=run_1&limit=100&offset=0")).toBe(2); expect(counts.get("/api/v1/projects")).toBe(1); expect(counts.get("/api/v1/audit-events")).toBe(1);
});

test("mounted runs polling stops after terminal", async () => {
  vi.useFakeTimers(); let calls = 0;
  globalThis.fetch = vi.fn<typeof fetch>(() => Promise.resolve(json({ items: [{ id: "run_1", status: calls++ ? "succeeded" : "running" }], count: 1, total: 1, limit: 100, offset: 0 })));
  const { wrapper } = setup(); const mounted = renderHook(() => useRunsPollingQuery(), { wrapper });
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); }); expect(calls).toBeGreaterThanOrEqual(2); const terminalCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); }); expect(calls).toBe(terminalCalls); mounted.unmount();
});

test("mounted selected-log polling stops after terminal", async () => {
  vi.useFakeTimers(); let logCalls = 0; const { client, wrapper } = setup(); client.setQueryData(queryKeys.runs(), [{ id: "run_1", status: "running" }]);
  globalThis.fetch = vi.fn<typeof fetch>((path) => Promise.resolve(json(String(path).includes("run-logs") ? { items: [{ id: `log_${++logCalls}`, run_id: "run_1", sequence: logCalls, stream: "stdout", message: "ok" }], count: 1, total: 1, limit: 100, offset: 0 } : { items: [], count: 0, total: 0, limit: 100, offset: 0 })));
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery("run_1", client.getQueryData(queryKeys.runs()) as never), { wrapper }); await act(async () => { await vi.advanceTimersByTimeAsync(3_000); }); expect(logCalls).toBeGreaterThanOrEqual(2); client.setQueryData(queryKeys.runs(), [{ id: "run_1", status: "succeeded" }]); mounted.rerender(); const terminalCalls = logCalls; await act(async () => { await vi.advanceTimersByTimeAsync(6_000); }); expect(logCalls).toBe(terminalCalls); mounted.unmount();
});
