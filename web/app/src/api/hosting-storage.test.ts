import { afterEach, expect, test, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  createHostingStorage,
  storageQuery,
  storageRequestID,
} from "./hosting-storage";
afterEach(() => vi.unstubAllGlobals());
test("storage mutation encodes resource and retains exact body, signal, CSRF and identity", async () => {
  const fetch = vi.fn(async () => new Response("{}"));
  vi.stubGlobal("fetch", fetch);
  const body = {
    request_id: storageRequestID(),
    expected_resource_generation: 2,
    mount_path: "/app/uploads",
  };
  expect(body.request_id).toMatch(/^[a-f0-9]{32}$/);
  const signal = new AbortController().signal;
  await createHostingStorage("r/a", body, { signal });
  await createHostingStorage("r/a", body, { signal });
  expect(fetch.mock.calls[0]).toEqual(fetch.mock.calls[1]);
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/resources/r%2Fa/storage",
    expect.objectContaining({
      method: "POST",
      cache: "no-store",
      credentials: "include",
      signal,
      body: JSON.stringify(body),
      headers: expect.objectContaining({ "X-NeroCD-CSRF": "1" }),
    }),
  ]);
});
test("storage queries distinguish absent metadata from denial and reject cross-resource responses", async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const fetch = vi.fn(async () => new Response("{}", { status: 404 }));
  vi.stubGlobal("fetch", fetch);
  expect(await client.fetchQuery(storageQuery("actor", "p", "r"))).toBeNull();
  fetch.mockImplementation(async () => new Response("{}", { status: 403 }));
  await expect(
    client.fetchQuery(storageQuery("actor", "p", "r")),
  ).rejects.toMatchObject({ status: 403 });
  fetch.mockImplementation(
    async () =>
      new Response(JSON.stringify({ resource_id: "other", project_id: "p" })),
  );
  await expect(
    client.fetchQuery(storageQuery("actor", "p", "r")),
  ).rejects.toThrow("scope mismatch");
  expect(storageQuery("actor", "p", "r").queryKey).not.toEqual(
    storageQuery("other", "p", "r").queryKey,
  );
  client.clear();
});
