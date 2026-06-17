import { afterEach, describe, expect, test } from "bun:test";

import { cancelRun, rejectRun, revokeSession, upsertProjectMember } from "./api";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function mockJSONFetch(body: unknown) {
  const calls: Array<{ path: string; init?: RequestInit }> = [];
  globalThis.fetch = ((path: string | URL | Request, init?: RequestInit) => {
    calls.push({ path: String(path), init });
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }) as typeof fetch;
  return calls;
}

describe("api mutation routes", () => {
  test("rejectRun posts the backend reject route", async () => {
    const calls = mockJSONFetch({ id: "apr_1", run_id: "run_1", status: "rejected", requested_by: "usr_1", created_at: "2026-06-06T00:00:00Z" });

    await rejectRun("ncd_token", "run_1");

    expect(calls[0].path).toBe("/api/v1/runs/reject");
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toEqual({
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: "Bearer ncd_token",
    });
    expect(calls[0].init?.body).toBe(JSON.stringify({ run_id: "run_1" }));
  });

  test("cancelRun posts the backend cancel route", async () => {
    const calls = mockJSONFetch({ id: "run_1", project_id: "proj_1", status: "canceled", requested_by: "usr_1", started_at: "2026-06-06T00:00:00Z", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] }, runner_tags: [] });

    await cancelRun("ncd_token", "run_1");

    expect(calls[0].path).toBe("/api/v1/runs/cancel");
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.body).toBe(JSON.stringify({ run_id: "run_1" }));
  });

  test("revokeSession deletes the current session", async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = [];
    globalThis.fetch = ((path: string | URL | Request, init?: RequestInit) => {
      calls.push({ path: String(path), init });
      return Promise.resolve(new Response(null, { status: 204 }));
    }) as typeof fetch;

    await revokeSession("ncd_token");

    expect(calls[0].path).toBe("/api/v1/sessions");
    expect(calls[0].init?.method).toBe("DELETE");
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
      Authorization: "Bearer ncd_token",
    });
  });

  test("upsertProjectMember posts project access updates", async () => {
    const calls = mockJSONFetch({
      id: "pm_1",
      project_id: "proj_platform",
      user_id: "usr_bootstrap",
      email: "admin@example.local",
      name: "Bootstrap Admin",
      role: "maintainer",
      created_at: "2026-06-06T00:00:00Z",
      updated_at: "2026-06-06T00:00:00Z",
    });

    await upsertProjectMember("ncd_token", {
      project_id: "proj_platform",
      email: "admin@example.local",
      role: "maintainer",
    });

    expect(calls[0].path).toBe("/api/v1/project-members");
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.body).toBe(JSON.stringify({ project_id: "proj_platform", email: "admin@example.local", role: "maintainer" }));
  });
});
