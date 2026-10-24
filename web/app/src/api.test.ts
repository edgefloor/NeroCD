import { afterEach, describe, expect, test, vi } from "vitest";

import { ApiError, archiveProject, approveRun, cancelDeployment, cancelRun, confirmDeployment, createDeployment, createTemplate, getDeployment, listArtifacts, listProjects, listRepositories, listRunLogs, rejectRun, revokeBrowserSession, updateProject, updateTemplate } from "./api";
import { request } from "./api/client";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; vi.useRealTimers(); });

function mockFetch(response: Response) {
  const fetchMock = vi.fn<typeof fetch>().mockImplementation(() => Promise.resolve(response.clone()));
  globalThis.fetch = fetchMock;
  return fetchMock;
}

describe("native cookie API client", () => {
  test.each(
    [401, 403].flatMap((status) =>
      ["pending", "rejected", "throws"].map((cancel) => ({ status, cancel })),
    ),
  )(
    "denial $status uses safe header evidence without waiting for an unfinished body or $cancel cancellation",
    async ({ status, cancel }) => {
      vi.useFakeTimers();
      let finish!: () => void;
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode("{"));
          finish = () => {
            try {
              controller.close();
            } catch {
              /* Cancellation may have closed it already. */
            }
          };
        },
      });
      const response = new Response(stream, {
        status,
        statusText: "private diagnostic",
        headers: { "X-Request-ID": "denial-request" },
      });
      const cancelBody = vi
        .spyOn(response.body!, "cancel")
        .mockImplementation(() => {
          if (cancel === "throws") throw new Error("cancel failed synchronously");
          return cancel === "rejected"
            ? Promise.reject(new Error("cancel failed"))
            : new Promise<void>(() => {});
        });
      globalThis.fetch = vi.fn().mockResolvedValue(response);
      const observed = vi.fn();
      void request("/example").then(
        () => observed("success"),
        (cause) => observed(cause),
      );
      try {
        await vi.advanceTimersByTimeAsync(1);
        expect(observed).toHaveBeenCalledExactlyOnceWith(
          expect.objectContaining({
            name: "ApiError",
            status,
            message: status === 401 ? "401 Unauthorized" : "403 Forbidden",
            requestID: "denial-request",
          }),
        );
        expect(cancelBody).toHaveBeenCalledOnce();
      } finally {
        finish();
        await vi.advanceTimersByTimeAsync(1);
      }
    },
  );
  test.each([401, 403])(
    "denial %i remains a header-based ApiError when the body is already aborted",
    async (status) => {
      const response = new Response(
        new ReadableStream({
          start(controller) {
            controller.error(new DOMException("body aborted", "AbortError"));
          },
        }),
        { status, headers: { "X-Request-ID": "aborted-request" } },
      );
      globalThis.fetch = vi.fn().mockResolvedValue(response);
      const controller = new AbortController();
      const pending = request("/example", { signal: controller.signal });
      controller.abort();
      await expect(pending).rejects.toMatchObject({
        status,
        message: status === 401 ? "401 Unauthorized" : "403 Forbidden",
        requestID: "aborted-request",
      });
    },
  );

  test("encodes query parameters and includes browser credentials", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ items: [], limit: 10, offset: 2, count: 0, total: 0 }), { headers: { "Content-Type": "application/json" } }));
    await listProjects({ limit: 10, offset: 2 });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/projects?limit=10&offset=2", expect.objectContaining({ credentials: "include", headers: { Accept: "application/json" } }));
  });

  test("forwards generated project and run filters to request URLs", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ items: [], limit: 0, offset: 0, count: 0, total: 0 }), { headers: { "Content-Type": "application/json" } }));
    await listRepositories({ project_id: "proj platform", limit: 5, offset: 1 });
    await listRunLogs({ run_id: "run/one", limit: 2, offset: 3 });
    await listArtifacts({ run_id: "run/one", limit: 2, offset: 3 });

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/repositories?project_id=proj+platform&limit=5&offset=1",
      "/api/v1/run-logs?run_id=run%2Fone&limit=2&offset=3",
      "/api/v1/artifacts?run_id=run%2Fone&limit=2&offset=3",
    ]);
  });

  test("returns JSON and supports no-content responses", async () => {
    mockFetch(new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } }));
    await expect(request<{ ok: boolean }>("/example")).resolves.toEqual({ ok: true });
    mockFetch(new Response(null, { status: 204 }));
    await expect(revokeBrowserSession()).resolves.toBeUndefined();
  });

  test("exposes structured server failures and request IDs", async () => {
    mockFetch(new Response(JSON.stringify({ error: "project is locked" }), { status: 409, statusText: "Conflict", headers: { "Content-Type": "application/json", "X-Request-ID": "req_123" } }));
    await expect(request("/example")).rejects.toMatchObject<ApiError>({ name: "ApiError", status: 409, message: "project is locked", requestID: "req_123" });
  });

  test("passes AbortSignal and sends CSRF only for unsafe requests", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } }));
    const controller = new AbortController();
    await request("/get", { signal: controller.signal });
    await request("/post", { method: "POST", body: {} });
    await request("/patch", { method: "PATCH", body: {} });
    await revokeBrowserSession();
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
    expect(fetchMock.mock.calls.map(([, init]) => (init?.headers as Record<string, string>)["X-NeroCD-CSRF"])).toEqual([undefined, "1", "1", "1"]);
  });

  test("uses cookie-backed mutation resources", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ id: "run_1" }), { headers: { "Content-Type": "application/json" } }));
    await cancelRun({ run_id: "run_1" });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/runs/cancel", expect.objectContaining({ method: "POST", credentials: "include", headers: { Accept: "application/json", "Content-Type": "application/json", "X-NeroCD-CSRF": "1" } }));
  });

  test("sends generated mutation bodies without transport defaults", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ id: "resource_1" }), { headers: { "Content-Type": "application/json" } }));
    await updateProject({ id: "proj_1", name: "Platform", description: "Automation" });
    await archiveProject({ id: "proj_1" });
    await approveRun({ run_id: "run_1" });
    await rejectRun({ run_id: "run_1" });
    await updateTemplate({ id: "tpl_1", project_id: "proj_1", name: "Patch", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false });
    await createTemplate({ project_id: "proj_1", name: "Patch", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false });

    expect(fetchMock.mock.calls.map(([, init]) => init?.body)).toEqual([
      JSON.stringify({ id: "proj_1", name: "Platform", description: "Automation" }),
      JSON.stringify({ id: "proj_1" }),
      JSON.stringify({ run_id: "run_1" }),
      JSON.stringify({ run_id: "run_1" }),
      JSON.stringify({ id: "tpl_1", project_id: "proj_1", name: "Patch", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }),
      JSON.stringify({ project_id: "proj_1", name: "Patch", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }),
    ]);
  });

  test("uses only public typed deployment endpoints with exact stable intent bodies", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ id: "dep_1" }), { headers: { "Content-Type": "application/json" } }));
    await createDeployment({ environment_id: "env_1", desired_revision_id: "rev_2", idempotency_key: "intent_immutable" });
    await confirmDeployment({ id: "dep_1" });
    await cancelDeployment({ deployment_id: "dep_1", request_id: "cancel_immutable" });
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/deployments", "/api/v1/deployments/confirm", "/api/v1/deployments/cancel"]);
    expect(fetchMock.mock.calls.map(([, init]) => init?.body)).toEqual([
      JSON.stringify({ environment_id: "env_1", desired_revision_id: "rev_2", idempotency_key: "intent_immutable" }),
      JSON.stringify({ id: "dep_1" }),
      JSON.stringify({ deployment_id: "dep_1", request_id: "cancel_immutable" }),
    ]);
    expect(fetchMock.mock.calls.map(([path]) => String(path)).join(" ")).not.toContain("/api/v1/runners/");
  });

  test("gets a deployment by its encoded stable ID without list filters", async () => {
    const fetchMock = mockFetch(new Response(JSON.stringify({ id: "dep/one" }), { headers: { "Content-Type": "application/json" } }));
    await getDeployment("dep/one");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/deployments/dep%2Fone", expect.objectContaining({ method: "GET", credentials: "include" }));
  });
});
