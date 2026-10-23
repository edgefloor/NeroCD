import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import type { components } from "./generated";

export type StorageDeploymentOptions =
  components["schemas"]["HostingStorageDeploymentOptions"];
export type StorageContinuityStatus =
  components["schemas"]["HostingStorageRouteContinuityStatus"];
export type StorageDeploymentScope = {
  actor: string;
  project: string;
  resource: string;
};

// Race the response as well as aborting fetch: a retired or stalled response must
// never keep the review busy indefinitely or publish data into a later scope.
export async function boundedStorageRequest<T>(
  run: (signal: AbortSignal) => Promise<T>,
  signal?: AbortSignal,
): Promise<T> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let abort: (() => void) | undefined;
  try {
    return await new Promise<T>((resolve, reject) => {
      const retire = (error: Error) => {
        controller.abort();
        reject(error);
      };
      abort = () =>
        retire(new DOMException("Deployment request retired", "AbortError"));
      timer = setTimeout(
        () =>
          retire(new Error("Deployment response timed out after 30 seconds")),
        30_000,
      );
      if (signal?.aborted) {
        abort();
        return;
      }
      signal?.addEventListener("abort", abort, { once: true });
      void run(controller.signal).then((value) => {
        if (!controller.signal.aborted) resolve(value);
      }, reject);
    });
  } finally {
    clearTimeout(timer);
    if (abort) signal?.removeEventListener("abort", abort);
  }
}
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}`;
export async function getStorageDeploymentOptions(
  scope: StorageDeploymentScope,
  signal?: AbortSignal,
): Promise<StorageDeploymentOptions> {
  const value = await boundedStorageRequest(
    (signal) =>
      request<StorageDeploymentOptions>(
        `${path(scope.resource)}/storage-deployment-options`,
        { signal, cache: "no-store" },
      ),
    signal,
  );
  if (
    value.resource_id !== scope.resource ||
    !Number.isSafeInteger(value.resource_generation) ||
    value.resource_generation < 1 ||
    typeof value.available !== "boolean" ||
    value.available !== (value.reason === "available") ||
    !Array.isArray(value.routes)
  )
    throw new Error("Storage deployment options scope or evidence mismatch");
  if (value.available) {
    if (
      !value.storage_ref?.storage_id ||
      value.storage_ref.generation !== 1 ||
      !value.storage_ref.hash ||
      !value.previous_healthy_operation_id ||
      !Number.isInteger(value.host_port) ||
      value.host_port! < 1024 ||
      value.host_port! > 65535 ||
      !Number.isInteger(value.container_port) ||
      value.container_port! < 1 ||
      value.container_port! > 65535 ||
      !value.readiness?.path
    )
      throw new Error("Incomplete storage deployment evidence");
    const continuity = value.storage_route_continuity;
    if (
      value.routes.length !== (continuity?.routes.length ?? 0) ||
      new Set(value.routes.map((route) => route.id)).size !==
        value.routes.length ||
      (continuity &&
        (!Number.isSafeInteger(continuity.expected_loaded_generation) ||
          continuity.expected_loaded_generation < 1 ||
          !continuity.expected_loaded_proxy_operation_id ||
          value.routes.some(
            (route) =>
              !continuity.routes.some(
                (selection) =>
                  selection.route_id === route.id &&
                  selection.expected_version === route.version &&
                  selection.expected_selected_release_operation_id ===
                    route.selected_release_operation_id,
              ),
          )))
    )
      throw new Error("Storage route selection mismatch");
  }
  return value;
}
export const storageDeploymentOptionsQuery = (scope: StorageDeploymentScope) =>
  queryOptions({
    queryKey: [
      "storageDeploymentOptions",
      scope.actor,
      scope.project,
      scope.resource,
    ],
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    queryFn: ({ signal }) => getStorageDeploymentOptions(scope, signal),
  });
export async function getStorageContinuityStatus(
  scope: StorageDeploymentScope,
  operation: string,
  signal?: AbortSignal,
): Promise<StorageContinuityStatus> {
  const value = await boundedStorageRequest(
    (signal) =>
      request<StorageContinuityStatus>(
        `${path(scope.resource)}/storage-route-continuity/${encodeURIComponent(operation)}`,
        { signal, cache: "no-store" },
      ),
    signal,
  );
  if (
    value.resource_id !== scope.resource ||
    value.operation_id !== operation ||
    ![
      "reserved",
      "unresolved",
      "proxy_pending",
      "loaded",
      "proxy_failed",
      "released",
    ].includes(value.state) ||
    ![
      "pending",
      "reconciliation_required",
      "loaded_historical",
      "released",
    ].includes(value.reason) ||
    (value.state === "loaded" &&
      (!Number.isSafeInteger(value.loaded_generation) ||
        value.loaded_generation! < 1 ||
        value.reason !== "loaded_historical"))
  )
    throw new Error("Storage continuity scope or evidence mismatch");
  return value;
}
