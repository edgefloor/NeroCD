import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { templatesQuery, allProjectsQuery, approvalsQuery, auditEventsQuery, deploymentQuery, deploymentsQuery, projectsQuery, queryKeys, runLogsQuery, runQuery, runsQuery } from "./queries";
import { useApprovalDecision, useDeploymentActions, useDeploymentMutations, useProjectMutations, useProjectActions, useTemplateActions, useRunMutations } from "./mutations";
import { useDeploymentPollingQuery, useRunsPollingQuery, useSelectedRunLogsPollingQuery } from "./polling";

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

test("failed earlier-log requests preserve current output and can be retried", async () => {
  let failHistory = true;
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    const url = new URL(String(path), "http://local");
    const offset = Number(url.searchParams.get("offset"));
    const limit = Number(url.searchParams.get("limit"));
    if (limit === 50 && failHistory) return Promise.resolve(new Response(JSON.stringify({ error: "History unavailable" }), { status: 503, headers: { "Content-Type": "application/json" } }));
    const items = Array.from({ length: Math.min(limit, 150 - offset) }, (_, index) => ({ id: `log_${offset + index}`, run_id: "run_history", sequence: offset + index, stream: "stdout", message: `line ${offset + index}` }));
    return Promise.resolve(json({ items, count: items.length, total: 150, limit, offset }));
  });
  const { wrapper } = setup();
  const mounted = renderHook(() => useSelectedRunLogsPollingQuery("run_history", false), { wrapper });
  await waitFor(() => expect(mounted.result.current.data).toHaveLength(100));
  await act(async () => { await mounted.result.current.loadPrevious(); });
  expect(mounted.result.current.previousError?.message).toBe("History unavailable");
  expect(mounted.result.current.data).toHaveLength(100);
  expect(mounted.result.current.hasPrevious).toBe(true);
  expect(mounted.result.current.loadingPrevious).toBe(false);
  failHistory = false;
  await act(async () => { await mounted.result.current.loadPrevious(); });
  expect(mounted.result.current.data).toHaveLength(150);
  expect(mounted.result.current.previousError).toBeUndefined();
  expect(mounted.result.current.hasPrevious).toBe(false);
  mounted.unmount();
});

test("run list polling pauses on failure and resumes after explicit recovery", async () => {
  vi.useFakeTimers();
  let fail = false;
  let calls = 0;
  globalThis.fetch = vi.fn<typeof fetch>(() => {
    calls++;
    return Promise.resolve(fail ? new Response(JSON.stringify({ error: "Temporary failure" }), { status: 503 }) : json({ items: [{ id: "run_1", status: "running" }], count: 1, total: 1, limit: 50, offset: 0 }));
  });
  const { wrapper } = setup();
  const mounted = renderHook(() => ({ ...useRunsPollingQuery() }), { wrapper });
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  fail = true;
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  expect(mounted.result.current.isError).toBe(true);
  const failedCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(9_000); });
  expect(calls).toBe(failedCalls);
  fail = false;
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  expect(mounted.result.current.isError).toBe(false);
  const recoveredCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
  expect(calls).toBeGreaterThan(recoveredCalls);
  mounted.unmount();
});

test("deployment detail polling pauses on failure and resumes after explicit recovery", async () => {
  vi.useFakeTimers();
  let fail = false;
  let calls = 0;
  globalThis.fetch = vi.fn<typeof fetch>(() => {
    calls++;
    return Promise.resolve(fail ? new Response(JSON.stringify({ error: "Temporary failure" }), { status: 503 }) : json({ id: "dep_1", status: "queued" }));
  });
  const { wrapper } = setup();
  const mounted = renderHook(() => ({ ...useDeploymentPollingQuery("dep_1") }), { wrapper });
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  fail = true;
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  expect(mounted.result.current.isError).toBe(true);
  const failedCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(9_000); });
  expect(calls).toBe(failedCalls);
  fail = false;
  await act(async () => { await mounted.result.current.refetch(); await vi.advanceTimersByTimeAsync(10); });
  expect(mounted.result.current.isError).toBe(false);
  const recoveredCalls = calls;
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
  expect(calls).toBeGreaterThan(recoveredCalls);
  mounted.unmount();
});


test("confirmed cancellation remains in run list and detail when their refreshes fail", async () => {
  let canceled = false;
  const queued = { id: "run_1", project_id: "project_one", status: "queued" };
  const result = { ...queued, status: "canceled" };
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    if (String(path) === "/api/v1/runs/cancel") {
      canceled = true;
      return Promise.resolve(json(result));
    }
    if (canceled) return Promise.resolve(new Response(JSON.stringify({ error: "Refresh unavailable" }), { status: 503 }));
    return Promise.resolve(json(String(path) === "/api/v1/runs/run_1" ? queued : { items: [queued], count: 1, total: 1, limit: 50, offset: 0 }));
  });
  const { client, wrapper } = setup();
  function Observer() { useQuery(runsQuery()); useQuery(runQuery("run_1")); return useRunMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(client.getQueryState(queryKeys.run("run_1"))?.status).toBe("success"));
  await act(async () => { await mounted.result.current.cancel.mutateAsync("run_1"); });
  expect(client.getQueryData(queryKeys.run("run_1"))).toEqual(result);
  expect(client.getQueryData(queryKeys.runs())).toMatchObject({ items: [result], count: 1, total: 1 });
  expect(client.getQueryState(queryKeys.run("run_1"))?.status).toBe("error");
  expect(client.getQueryState(queryKeys.runs())?.status).toBe("error");
  mounted.unmount();
});


test.each(["confirm", "cancel"] as const)("deployment %s retains its acknowledged state across failed refreshes", async (action) => {
  let changed = false;
  const original = { id: "dep_1", environment_id: "env_one", status: action === "confirm" ? "waiting_confirmation" : "applying" };
  const acknowledged = { ...original, status: action === "confirm" ? "assigned" : "cancel_requested" };
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    if (String(path) === `/api/v1/deployments/${action}`) {
      changed = true;
      return Promise.resolve(json(acknowledged));
    }
    if (changed) return Promise.resolve(new Response(JSON.stringify({ error: "Refresh unavailable" }), { status: 503 }));
    return Promise.resolve(json(String(path) === "/api/v1/deployments/dep_1" ? original : { items: [original], count: 1, total: 1, limit: 50, offset: 0 }));
  });
  const { client, wrapper } = setup();
  client.setQueryData(queryKeys.deploymentHistory("env_one"), [original]);
  function Observer() { useQuery(deploymentsQuery()); useQuery(deploymentQuery("dep_1")); return useDeploymentMutations(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(client.getQueryState(queryKeys.deployment("dep_1"))?.status).toBe("success"));
  await act(async () => {
    if (action === "confirm") await mounted.result.current.confirm.mutateAsync({ id: "dep_1" });
    else await mounted.result.current.cancel.mutateAsync({ deployment_id: "dep_1", request_id: "cancel_one" });
  });
  expect(client.getQueryData(queryKeys.deployment("dep_1"))).toEqual(acknowledged);
  expect(client.getQueryData(queryKeys.deployments())).toMatchObject({ items: [acknowledged], count: 1, total: 1 });
  expect(client.getQueryData(queryKeys.deploymentHistory("env_one"))).toEqual([acknowledged]);
  expect(client.getQueryState(queryKeys.deployment("dep_1"))?.status).toBe("error");
  expect(client.getQueryState(queryKeys.deployments())?.status).toBe("error");
  mounted.unmount();
});


test.each(["approve", "reject"] as const)("acknowledged %s decisions survive refresh failure without refreshing unrelated projects", async (decision) => {
  let decided = false;
  let projectRequests = 0;
  const original = { id: "approval_one", run_id: "run_one", status: "pending", requested_by: "operator", created_at: "2026-10-11T00:00:00Z" };
  const acknowledged = { ...original, status: decision === "approve" ? "approved" : "rejected", approved_by: "maintainer" };
  globalThis.fetch = vi.fn<typeof fetch>((path) => {
    const url = String(path);
    if (url === `/api/v1/runs/${decision}`) {
      decided = true;
      return Promise.resolve(json(acknowledged));
    }
    if (url === "/api/v1/projects") {
      projectRequests += 1;
      return Promise.resolve(json({ items: [], count: 0, total: 0, limit: 50, offset: 0 }));
    }
    if (decided) return Promise.resolve(new Response(JSON.stringify({ error: "Refresh unavailable" }), { status: 503 }));
    return Promise.resolve(json({ items: [original], count: 1, total: 1, limit: 50, offset: 0 }));
  });
  const { client, wrapper } = setup();
  function Observer() { useQuery(approvalsQuery()); useQuery(projectsQuery()); return useApprovalDecision(); }
  const mounted = renderHook(() => Observer(), { wrapper });
  await waitFor(() => expect(client.getQueryState(queryKeys.approvals())?.status).toBe("success"));
  await act(async () => { mounted.result.current.decide("run_one", decision); });
  await waitFor(() => expect(client.getQueryState(queryKeys.approvals())?.status).toBe("error"));
  expect(client.getQueryData(queryKeys.approvals())).toMatchObject({ items: [acknowledged], count: 1, total: 1 });
  expect(client.getQueryState(queryKeys.approvals())?.status).toBe("error");
  expect(projectRequests).toBe(1);
  mounted.unmount();
});


test("approval decisions serialize competing clicks and unlock after completion", async () => {
  let finish!: (response: Response) => void;
  const approval = { id: "approval_one", run_id: "run_one", status: "approved" };
  globalThis.fetch = vi.fn<typeof fetch>((path) => String(path).endsWith("/approve")
    ? new Promise<Response>((resolve) => { finish = resolve; })
    : Promise.resolve(json({ ...approval, status: "rejected" })));
  const { wrapper } = setup();
  const mounted = renderHook(() => useApprovalDecision(), { wrapper });
  await act(async () => {
    mounted.result.current.decide("run_one", "approve");
    mounted.result.current.decide("run_one", "reject");
  });
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  await waitFor(() => expect(mounted.result.current.busy).toBe(true));
  await act(async () => { finish(json(approval)); });
  await waitFor(() => expect(mounted.result.current.busy).toBe(false));
  await act(async () => { mounted.result.current.decide("run_two", "reject"); });
  await waitFor(() => expect(mounted.result.current.busy).toBe(false));
  expect(globalThis.fetch).toHaveBeenCalledTimes(2);
  mounted.unmount();
});


test("confirmed project edits survive a failed refresh in lists and lookup choices", async () => {
  let saved = false;
  const original = { id: "proj_one", name: "Original", description: "", created_at: "2026-01-01T00:00:00Z" };
  const updated = { ...original, name: "Saved name" };
  globalThis.fetch = vi.fn<typeof fetch>((_path, init) => {
    if (init?.method === "PATCH") { saved = true; return Promise.resolve(json(updated)); }
    if (saved) return Promise.resolve(new Response("Unavailable", { status: 503 }));
    return Promise.resolve(json({ items: [original], count: 1, total: 1, limit: 100, offset: 0 }));
  });
  const { client, wrapper } = setup();
  const mounted = renderHook(() => { useQuery(projectsQuery()); useQuery(allProjectsQuery()); return useProjectActions(); }, { wrapper });
  await waitFor(() => expect(client.getQueryData(queryKeys.allProjects)).toEqual([original]));
  await act(async () => { await mounted.result.current.actions.update({ id: original.id, name: updated.name, description: "" }); });
  expect(client.getQueryData(queryKeys.allProjects)).toEqual([updated]);
  expect(client.getQueryData(queryKeys.projects())).toMatchObject({ items: [updated], count: 1, total: 1 });
  expect(client.getQueryState(queryKeys.projects())?.status).toBe("error");
  expect(mounted.result.current.busy).toBe("");
});

test("project actions reject overlapping writes and recover after completion", async () => {
  let release = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const requests = vi.fn<typeof fetch>(async () => { await gate; return json({ id: "proj_one", name: "New", description: "" }); });
  globalThis.fetch = requests;
  const { wrapper } = setup();
  const mounted = renderHook(({ disabled }) => useProjectActions(disabled), { wrapper, initialProps: { disabled: false } });
  let first: Promise<unknown>;
  await act(async () => {
    first = mounted.result.current.actions.create({ name: "New", description: "" });
    expect(await mounted.result.current.actions.archive({ id: "proj_one" })).toBeUndefined();
  });
  await waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
  expect(mounted.result.current.busy).toBe("project");
  await act(async () => { release(); await first; });
  expect(mounted.result.current.busy).toBe("");
  mounted.rerender({ disabled: true });
  await act(async () => { expect(await mounted.result.current.actions.archive({ id: "proj_one" })).toBeUndefined(); });
  expect(requests).toHaveBeenCalledTimes(1);
  mounted.rerender({ disabled: false });
  await act(async () => { await mounted.result.current.actions.archive({ id: "proj_one" }); });
  expect(requests).toHaveBeenCalledTimes(2);
});


test("project creation refreshes membership and source creation refreshes every repository page", async () => {
  globalThis.fetch = vi.fn<typeof fetch>(async () => json({ id: "created" }));
  const { client, wrapper } = setup();
  client.setQueryData(queryKeys.allProjectMembers, []);
  const repositories = queryKeys.repositories({ project_id: "proj_one", limit: 10, offset: 10 });
  client.setQueryData(repositories, { items: [], total: 0, count: 0, limit: 10, offset: 10 });
  const mounted = renderHook(() => useProjectActions(), { wrapper });
  await act(async () => { await mounted.result.current.actions.create({ name: "New", description: "" }); });
  expect(client.getQueryState(queryKeys.allProjectMembers)?.isInvalidated).toBe(true);
  expect(client.getQueryState(repositories)?.isInvalidated).toBe(false);
  await act(async () => { await mounted.result.current.actions.repository({ project_id: "proj_one", name: "Source", url: "https://example.com/repo.git", provider: "git", default_ref: "main" }); });
  expect(client.getQueryState(repositories)?.isInvalidated).toBe(true);
});


test("confirmed project archives remove stale cards and choices when refresh fails", async () => {
  let archived = false;
  const project = { id: "proj_one", name: "Archived project", description: "", created_at: "2026-01-01T00:00:00Z" };
  const other = { ...project, id: "proj_two", name: "Keep project" };
  globalThis.fetch = vi.fn<typeof fetch>((_path, init) => {
    if (init?.method === "POST") { archived = true; return Promise.resolve(json({ ...project, archived_at: "2026-10-11T10:00:00Z" })); }
    if (archived) return Promise.resolve(new Response("Unavailable", { status: 503 }));
    return Promise.resolve(json({ items: [project, other], count: 2, total: 2, limit: 100, offset: 0 }));
  });
  const { client, wrapper } = setup();
  const mounted = renderHook(() => { useQuery(projectsQuery()); useQuery(allProjectsQuery()); return useProjectActions(); }, { wrapper });
  await waitFor(() => expect(client.getQueryData(queryKeys.allProjects)).toEqual([project, other]));
  await act(async () => { await mounted.result.current.actions.archive({ id: project.id }); });
  expect(client.getQueryData(queryKeys.allProjects)).toEqual([other]);
  expect(client.getQueryData(queryKeys.projects())).toEqual({ items: [other], count: 1, total: 1, limit: 100, offset: 0 });
  expect(client.getQueryState(queryKeys.projects())?.status).toBe("error");
});


const templateFixture = { id: "template_one", project_id: "proj_one", name: "Original", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false };

test("confirmed template edits survive refresh failure in global and project lists", async () => {
  let saved = false;
  const updated = { ...templateFixture, name: "Saved" };
  globalThis.fetch = vi.fn<typeof fetch>((_path, init) => {
    if (init?.method === "PATCH") { saved = true; return Promise.resolve(json(updated)); }
    if (saved) return Promise.resolve(new Response("Unavailable", { status: 503 }));
    return Promise.resolve(json({ items: [templateFixture], count: 1, total: 1, limit: 100, offset: 0 }));
  });
  const { client, wrapper } = setup();
  const oldKey = queryKeys.templates({ project_id: "proj_one" });
  const mounted = renderHook(() => { useQuery(templatesQuery()); useQuery(templatesQuery({ project_id: "proj_one" })); return useTemplateActions(); }, { wrapper });
  await waitFor(() => expect(client.getQueryData(oldKey)).toMatchObject({ items: [templateFixture] }));
  await act(async () => { await mounted.result.current.actions.update(updated); });
  expect(client.getQueryData(queryKeys.templates())).toMatchObject({ items: [updated], count: 1, total: 1 });
  expect(client.getQueryData(oldKey)).toEqual({ items: [updated], count: 1, total: 1, limit: 100, offset: 0 });
  expect(client.getQueryState(oldKey)?.status).toBe("error");
});

test("template run requests recover from failure and refresh only run-related data", async () => {
  const run = { id: "run_new", status: "waiting_approval" };
  globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response("Unavailable", { status: 503 })).mockResolvedValueOnce(json(run));
  const { client, wrapper } = setup();
  const changed = [queryKeys.runs(), queryKeys.approvals(), queryKeys.runLogs()];
  const unchanged = [queryKeys.projects(), queryKeys.templates(), queryKeys.runLogs({ run_id: "run_other" })];
  for (const key of [...changed, ...unchanged]) client.setQueryData(key, { items: [] });
  const mounted = renderHook(() => useTemplateActions(), { wrapper });
  await act(async () => { expect(await mounted.result.current.actions.run({ template_id: templateFixture.id })).toBeUndefined(); });
  expect(mounted.result.current.busy).toBe("");
  expect(mounted.result.current.error).toMatchObject({ key: `template:run:${templateFixture.id}`, message: expect.any(String) });
  for (const key of changed) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
  await act(async () => { expect(await mounted.result.current.actions.run({ template_id: templateFixture.id })).toEqual(run); });
  expect(mounted.result.current.error).toBeUndefined();
  expect(client.getQueryData(queryKeys.run(run.id))).toEqual(run);
  for (const key of changed) expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  for (const key of unchanged) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
  expect(globalThis.fetch).toHaveBeenCalledTimes(2);
});

test("template actions stay locked through their follow-up refresh", async () => {
  let saved = false;
  let release = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  let writes = 0;
  globalThis.fetch = vi.fn<typeof fetch>(async (_path, init) => {
    if (init?.method === "POST") { writes++; saved = true; return json(templateFixture); }
    if (saved) await gate;
    return json({ items: [templateFixture], count: 1, total: 1, limit: 100, offset: 0 });
  });
  const { client, wrapper } = setup();
  const mounted = renderHook(() => { useQuery(templatesQuery()); return useTemplateActions(); }, { wrapper });
  await waitFor(() => expect(client.getQueryData(queryKeys.templates())).toBeDefined());
  let pending: Promise<unknown>;
  await act(async () => { pending = mounted.result.current.actions.create(templateFixture); });
  await waitFor(() => expect(saved).toBe(true));
  await act(async () => { expect(await mounted.result.current.actions.run({ template_id: templateFixture.id })).toBeUndefined(); });
  expect(writes).toBe(1);
  expect(mounted.result.current.busy).toBe("template");
  await act(async () => { release(); await pending; });
  expect(mounted.result.current.busy).toBe("");
});


test.each(["confirm", "cancel"] as const)("deployment %s blocks duplicate and competing writes and allows retry", async (action) => {
  let finish!: (response: Response) => void;
  globalThis.fetch = vi.fn<typeof fetch>(() => new Promise((resolve) => { finish = resolve; }));
  const { wrapper } = setup();
  const mounted = renderHook(({ disabled }) => useDeploymentActions(disabled), { wrapper, initialProps: { disabled: false } });
  const call = (choice: "confirm" | "cancel") => choice === "confirm"
    ? mounted.result.current.confirm("dep_1")
    : mounted.result.current.cancel({ deployment_id: "dep_1", request_id: "cancel_one" });
  let pending!: Promise<unknown>;
  await act(async () => {
    pending = call(action);
    void call("confirm");
    void call("cancel");
  });
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  await act(async () => { finish(new Response(JSON.stringify({ error: "Temporary failure" }), { status: 503 })); await pending; });
  expect(mounted.result.current.error).toMatchObject({ key: `${action}:dep_1` });
  mounted.rerender({ disabled: true });
  await act(async () => { await call("confirm"); });
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  mounted.rerender({ disabled: false });
  vi.mocked(globalThis.fetch).mockResolvedValueOnce(json({ id: "dep_1", status: "canceled" }));
  await act(async () => { await call("cancel"); });
  expect(globalThis.fetch).toHaveBeenCalledTimes(2);
  expect(mounted.result.current.error).toBeUndefined();
});


test("deployment creation blocks duplicate requests and retains retry input", async () => {
  let finish!: (response: Response) => void;
  globalThis.fetch = vi.fn<typeof fetch>(() => new Promise((resolve) => { finish = resolve; }));
  const { wrapper } = setup();
  const mounted = renderHook(() => useDeploymentActions(), { wrapper });
  const input = { environment_id: "env_1", desired_revision_id: "rev_1", idempotency_key: "intent_one" };
  let pending!: Promise<unknown>;
  await act(async () => {
    pending = mounted.result.current.create(input);
    void mounted.result.current.create(input);
  });
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  await act(async () => { finish(new Response(JSON.stringify({ error: "Temporary failure" }), { status: 503 })); await pending; });
  expect(mounted.result.current.creationError).toMatchObject({ ...input, message: expect.stringContaining("Temporary failure") });
  vi.mocked(globalThis.fetch).mockResolvedValueOnce(json({ id: "dep_1", status: "queued" }));
  await act(async () => { await mounted.result.current.create(input); });
  expect(globalThis.fetch).toHaveBeenCalledTimes(2);
  for (const [, options] of vi.mocked(globalThis.fetch).mock.calls) expect(JSON.parse(String(options?.body))).toEqual(input);
  expect(mounted.result.current.creationError).toBeUndefined();
});
