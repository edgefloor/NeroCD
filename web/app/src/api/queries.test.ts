import { QueryClient } from "@tanstack/react-query";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "./errors";
import { configureQueryClient, deploymentQuery, deploymentsQuery, environmentsQuery, healthQuery, projectsQuery, queryKeys, repositoriesQuery, retryRemote, revisionsQuery, runLogsQuery, runsQuery, servicesQuery, shouldPollDeploymentList, shouldPollRunList, shouldPollSelectedLogs, templatesQuery, approvalsQuery } from "./queries";
import { Route as HomeRoute } from "../routes/_authenticated.index";
import { Route as ProjectsRoute } from "../routes/_authenticated.projects";
import { Route as RunDetailRoute } from "../routes/_authenticated.runs.$runId";
import { Route as DeploymentsRoute } from "../routes/_authenticated.deployments.index";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const client = () => configureQueryClient(new QueryClient());
const list = (items: unknown[] = []) => new Response(JSON.stringify({ items, count: items.length, total: items.length, limit: 100, offset: 0 }), { headers: { "Content-Type": "application/json" } });

describe("resource query options", () => {
  test("real home, projects, and run-detail loaders request only their route-owned resources", async () => {
    const calls: string[] = [];
    globalThis.fetch = vi.fn<typeof fetch>((path) => { calls.push(String(path)); return Promise.resolve(String(path).includes("health") ? new Response(JSON.stringify({ status: "ok" }), { headers: { "Content-Type": "application/json" } }) : list()); });
    const homeClient = client();
    await HomeRoute.options.loader!({ context: { queryClient: homeClient } } as never);
    expect(calls).toEqual(["/api/v1/health", "/api/v1/projects", "/api/v1/templates", "/api/v1/runs", "/api/v1/approvals", "/api/v1/run-logs?limit=5&offset=0"]);
    calls.length = 0;
    await ProjectsRoute.options.loader!({ context: { queryClient: client() } } as never);
    expect(calls).toEqual(["/api/v1/projects", "/api/v1/repositories"]);
    calls.length = 0;
    await RunDetailRoute.options.loader!({ context: { queryClient: client() }, params: { runId: "run_1" } } as never);
    expect(calls).toEqual(["/api/v1/runs", "/api/v1/projects", "/api/v1/templates", "/api/v1/run-logs?run_id=run_1&limit=100&offset=0"]);
  });
  test("home ownership fetches its six resources and no snapshot-only collections", async () => {
    const calls: string[] = [];
    globalThis.fetch = vi.fn<typeof fetch>((path) => { calls.push(String(path)); return Promise.resolve(String(path).includes("health") ? new Response(JSON.stringify({ status: "ok" }), { headers: { "Content-Type": "application/json" } }) : list()); });
    const queryClient = client();
    await Promise.all([queryClient.ensureQueryData(healthQuery()), queryClient.ensureQueryData(projectsQuery()), queryClient.ensureQueryData(templatesQuery()), queryClient.ensureQueryData(runsQuery()), queryClient.ensureQueryData(approvalsQuery()), queryClient.ensureQueryData(runLogsQuery({ limit: 5, offset: 0 }))]);
    expect(calls).toEqual(["/api/v1/health", "/api/v1/projects", "/api/v1/templates", "/api/v1/runs", "/api/v1/approvals", "/api/v1/run-logs?limit=5&offset=0"]);
    expect(calls.join(" ")).not.toMatch(/project-members|repositories|audit-events|artifacts|capabilities/);
  });

  test("projects route data is direct and does not request other collections", async () => {
    const calls: string[] = [];
    globalThis.fetch = vi.fn<typeof fetch>((path) => { calls.push(String(path)); return Promise.resolve(list()); });
    const queryClient = client();
    await Promise.all([queryClient.ensureQueryData(projectsQuery()), queryClient.ensureQueryData(repositoriesQuery())]);
    expect(calls).toEqual(["/api/v1/projects", "/api/v1/repositories"]);
  });

  test("deployments route owns its public route-local resources and excludes runners", async () => {
    const calls: string[] = [];
    globalThis.fetch = vi.fn<typeof fetch>((path) => { calls.push(String(path)); return Promise.resolve(String(path).endsWith("/me") ? new Response(JSON.stringify({ id: "usr_1", email: "operator@example.test", name: "Operator", roles: ["system_admin"], provider: "local" }), { headers: { "Content-Type": "application/json" } }) : list()); });
    await DeploymentsRoute.options.loader!({ context: { queryClient: client() } } as never);
    expect(calls).toEqual(["/api/v1/projects", "/api/v1/services", "/api/v1/environments", "/api/v1/me"]);
    expect(calls.join(" ")).not.toContain("/api/v1/runners/");
  });

  test("targeted invalidation leaves logs and audit data fresh", async () => {
    const queryClient = client();
    queryClient.setQueryData(queryKeys.projects(), []);
    queryClient.setQueryData(queryKeys.runLogs({ run_id: "run_1", limit: 100, offset: 0 }), []);
    queryClient.setQueryData(queryKeys.auditEvents(), []);
    await queryClient.invalidateQueries({ queryKey: queryKeys.projects() });
    expect(queryClient.getQueryState(queryKeys.projects())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(queryKeys.runLogs({ run_id: "run_1", limit: 100, offset: 0 }))?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(queryKeys.auditEvents())?.isInvalidated).toBe(false);
  });

  test("query signals reach fetch and mutations do not retry", async () => {
    let signal: AbortSignal | undefined;
    globalThis.fetch = vi.fn<typeof fetch>((_path, init) => { signal = init?.signal ?? undefined; return Promise.resolve(list()); });
    const queryClient = client();
    await queryClient.fetchQuery(runsQuery());
    expect(signal).toBeInstanceOf(AbortSignal);
    let attempts = 0;
    const mutation = queryClient.getMutationCache().build(queryClient, { mutationFn: async () => { attempts += 1; throw new Error("offline"); } });
    await expect(mutation.execute(undefined)).rejects.toThrow("offline");
    expect(attempts).toBe(1);
  });

  test("direct deployment lookup maps the server's non-enumerating 404 to a cacheable unavailable state", async () => {
    globalThis.fetch = vi.fn<typeof fetch>(() => Promise.resolve(new Response(JSON.stringify({ error: "resource not found" }), { status: 404, headers: { "Content-Type": "application/json" } })));
    const queryClient = client();
    await expect(queryClient.fetchQuery(deploymentQuery("dep_missing"))).resolves.toBeNull();
    expect(globalThis.fetch).toHaveBeenCalledWith("/api/v1/deployments/dep_missing", expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  test("an actual mounted runs query aborts its in-flight fetch on unmount", async () => {
    let aborted = false;
    globalThis.fetch = vi.fn<typeof fetch>((_path, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => { aborted = true; reject(new DOMException("aborted", "AbortError")); });
    }));
    const queryClient = client();
    function RunsObserver() { useQuery(runsQuery()); return null; }
    const mounted = render(createElement(QueryClientProvider, { client: queryClient }, createElement(RunsObserver)));
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    mounted.unmount();
    await waitFor(() => expect(aborted).toBe(true));
  });

  test("retry and polling stop for terminal runs", () => {
    expect(retryRemote(0, new ApiError(401, "unauthenticated", null))).toBe(false);
    expect(retryRemote(0, new ApiError(404, "missing", null))).toBe(false);
    expect(retryRemote(0, new ApiError(503, "busy", null))).toBe(true);
    expect(retryRemote(2, new TypeError("offline"))).toBe(false);
    const terminal = [{ id: "run_1", status: "succeeded" }] as never;
    const active = [{ id: "run_1", status: "running" }] as never;
    expect(shouldPollRunList(terminal)).toBe(false);
    expect(shouldPollRunList(active)).toBe(true);
    expect(shouldPollSelectedLogs("run_1", terminal)).toBe(false);
    expect(shouldPollSelectedLogs("run_1", active)).toBe(true);
    expect(shouldPollDeploymentList([{ id: "dep_1", status: "rolled_back" }] as never)).toBe(false);
    expect(shouldPollDeploymentList([{ id: "dep_1", status: "verifying" }] as never)).toBe(true);
  });
});
