import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import {
  boundedStorageRequest,
  getStorageContinuityStatus,
  getStorageDeploymentOptions,
  storageDeploymentOptionsQuery,
  type StorageDeploymentOptions,
} from "./hosting-storage-deployment";
const scope = { actor: "actor", project: "project", resource: "resource" };
export const storageOptions: StorageDeploymentOptions = {
  resource_id: "resource",
  resource_generation: 1,
  storage_ref: { storage_id: "storage", generation: 1, hash: "storage-hash" },
  previous_healthy_operation_id: "hop_old",
  host_port: 8080,
  container_port: 80,
  readiness: { path: "/health", timeout_seconds: 30 },
  available: true,
  reason: "available",
  routes: [
    {
      id: "route",
      hostname: "uploads.example.test",
      path_prefix: "/",
      version: 4,
      selected_release_operation_id: "hop_old",
    },
  ],
  storage_route_continuity: {
    expected_loaded_generation: 7,
    expected_loaded_proxy_operation_id: "proxy_old",
    routes: [
      {
        route_id: "route",
        expected_version: 4,
        expected_selected_release_operation_id: "hop_old",
      },
    ],
  },
};
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
test("storage reads encode targets, isolate actor and project, and disable caching", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response(
        JSON.stringify({ ...storageOptions, resource_id: "resource/one" }),
      ),
    );
  vi.stubGlobal("fetch", fetch);
  await getStorageDeploymentOptions({ ...scope, resource: "resource/one" });
  expect(fetch.mock.calls[0][0]).toBe(
    "/api/v1/resources/resource%2Fone/storage-deployment-options",
  );
  expect(fetch.mock.calls[0][1]).toMatchObject({
    credentials: "include",
    cache: "no-store",
    signal: expect.any(AbortSignal),
  });
  expect(storageDeploymentOptionsQuery(scope).queryKey).not.toEqual(
    storageDeploymentOptionsQuery({ ...scope, actor: "other" }).queryKey,
  );
  expect(storageDeploymentOptionsQuery(scope).queryKey).not.toEqual(
    storageDeploymentOptionsQuery({ ...scope, project: "other" }).queryKey,
  );
});
test.each([
  { ...storageOptions, resource_id: "foreign" },
  { ...storageOptions, host_port: null },
  { ...storageOptions, storage_ref: null },
  { ...storageOptions, routes: [] },
  {
    ...storageOptions,
    storage_route_continuity: {
      ...storageOptions.storage_route_continuity!,
      routes: [
        {
          route_id: "other",
          expected_version: 4,
          expected_selected_release_operation_id: "hop_old",
        },
      ],
    },
  },
])("rejects foreign or incomplete authoritative review %#", async (body) => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(JSON.stringify(body))),
  );
  const client = new QueryClient();
  await expect(
    client.fetchQuery(storageDeploymentOptionsQuery(scope)),
  ).rejects.toThrow();
  expect(
    client.getQueryData(storageDeploymentOptionsQuery(scope).queryKey),
  ).toBeUndefined();
  client.clear();
});
test("stalled fetch expires after 30 seconds even if transport ignores cancellation", async () => {
  vi.useFakeTimers();
  let signal: AbortSignal | undefined;
  const promise = boundedStorageRequest((current) => {
    signal = current;
    return new Promise(() => {});
  });
  const rejection = expect(promise).rejects.toThrow("30 seconds");
  await vi.advanceTimersByTimeAsync(30_000);
  await rejection;
  expect(signal?.aborted).toBe(true);
});
test("retired scope aborts a read and discards a late response", async () => {
  const controller = new AbortController();
  let resolve!: (value: string) => void;
  const promise = boundedStorageRequest(
    () =>
      new Promise<string>((done) => {
        resolve = done;
      }),
    controller.signal,
  );
  controller.abort();
  resolve("late");
  await expect(promise).rejects.toMatchObject({ name: "AbortError" });
});
test.each([
  "reserved",
  "unresolved",
  "proxy_pending",
  "loaded",
  "proxy_failed",
  "released",
] as const)(
  "accepts exact continuity state %s without making a health claim",
  async (state) => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({
              resource_id: "resource",
              operation_id: "hop/one",
              state,
              reason: state === "loaded" ? "loaded_historical" : "pending",
              ...(state === "loaded" ? { loaded_generation: 8 } : {}),
            }),
          ),
        ),
    );
    expect((await getStorageContinuityStatus(scope, "hop/one")).state).toBe(
      state,
    );
  },
);
test("continuity rejects a foreign operation or invented loaded receipt", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          resource_id: "resource",
          operation_id: "foreign",
          state: "reserved",
          reason: "pending",
        }),
      ),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          resource_id: "resource",
          operation_id: "hop",
          state: "loaded",
          reason: "pending",
        }),
      ),
    );
  vi.stubGlobal("fetch", fetch);
  await expect(getStorageContinuityStatus(scope, "hop")).rejects.toThrow();
  await expect(getStorageContinuityStatus(scope, "hop")).rejects.toThrow();
});
