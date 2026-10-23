import { afterEach, expect, test, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  bootstrapListQuery,
  bootstrapOperationQuery,
  bootstrapOptionsQuery,
  commandServerBootstrap,
  createServerBootstrap,
} from "./server-bootstrap";

afterEach(() => vi.unstubAllGlobals());
test("bootstrap API preserves exact request identities and explicit grant choice with CSRF", async () => {
  const fetch = vi.fn(async () => new Response("{}"));
  vi.stubGlobal("fetch", fetch);
  const body = {
    command_id: "2e382205-637d-423b-a1c8-61a1473c1ffb",
    expected_version: 4,
    manifest_sha256: "digest",
    acknowledge_install_and_start: true as const,
    grant_project: false,
  };
  await commandServerBootstrap("operation/one", "install", body);
  await commandServerBootstrap("operation/one", "install", body);
  expect(fetch.mock.calls).toHaveLength(2);
  expect(fetch.mock.calls[0]).toEqual(fetch.mock.calls[1]);
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/server-bootstraps/operation%2Fone/install",
    expect.objectContaining({
      method: "POST",
      cache: "no-store",
      credentials: "include",
      headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }),
      body: JSON.stringify(body),
    }),
  ]);
  await createServerBootstrap({
    request_id: "request",
    project_id: "project",
    name: "host",
    target: {
      ip: "192.0.2.1",
      port: 22,
      user: "root",
      host_fingerprint: "SHA256:verified",
    },
    ssh_secret_id: "key",
    ssh_secret_version: 2,
    artifact_id: "release",
  });
  expect(String(fetch.mock.calls[2]?.[1].body)).toContain(
    '"ssh_secret_version":2',
  );
});
test("scoped metadata queries forward cancellation and reject a different project or operation", async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const fetch = vi.fn(
    async (url: string) =>
      new Response(
        JSON.stringify(
          url.includes("options")
            ? { available: false }
            : url.includes("?")
              ? { items: [{ project_id: "other" }] }
              : { id: "wrong", project_id: "project" },
        ),
      ),
  );
  vi.stubGlobal("fetch", fetch);
  await client.fetchQuery(bootstrapOptionsQuery("actor", "project"));
  await expect(
    client.fetchQuery(bootstrapListQuery("actor", "project", 20)),
  ).rejects.toThrow("project mismatch");
  await expect(
    client.fetchQuery(bootstrapOperationQuery("actor", "project", "operation")),
  ).rejects.toThrow("scope mismatch");
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/server-bootstrap-options?project_id=project",
    expect.objectContaining({
      signal: expect.any(AbortSignal),
      cache: "no-store",
    }),
  ]);
  expect(fetch.mock.calls[1]?.[0]).toBe(
    "/api/v1/server-bootstraps?project_id=project&limit=20&offset=20",
  );
  expect(
    bootstrapOperationQuery("actor", "project", "operation").queryKey,
  ).not.toEqual(
    bootstrapOperationQuery("other", "project", "operation").queryKey,
  );
  client.clear();
});
