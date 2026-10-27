import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import type { components } from "./generated";
import { scopedStorage, type HostingStorage } from "./hosting-storage";

// Older controllers may omit the new review field. Missing evidence never
// enables a connected storage deployment.
export type StorageDeploymentOptions = Omit<components["schemas"]["HostingStorageDeploymentOptions"], "application_database_storage"> & {
  stopped_operation_id?: string;
  application_database_storage?: components["schemas"]["HostingStorageDeploymentOptions"]["application_database_storage"];
};
export type StorageRouteRecoveryInput = components["schemas"]["HostingStorageRouteRecoveryInput"];
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
  validateStorageDeploymentOptions(value, scope.resource);
  return value;
}
const reasons = [
  "available",
  "initial_deployment",
  "unsupported_source",
  "unbound",
  "storage_unconfigured",
  "runtime_configuration_unsupported",
  "no_previous_healthy",
  "stale_storage",
  "restore_hold",
  "busy",
  "runner_ineligible",
  "proxy_unavailable",
  "no_loaded_generation",
  "stale_routes",
  "https_unavailable",
  "foreign_port_reference",
  "continuity_unresolved",
];
function closed(value: unknown, keys: string[]): boolean {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length === keys.length &&
    keys.every((key) => Object.hasOwn(value, key))
  );
}
const identifier = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const positive = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) > 0;
const text = (value: unknown, limit: number): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= limit &&
  !/[\x00-\x1f\x7f]/.test(value);
export function validStorageReadiness(path: string, timeout: number) {
  return (
    text(path, 1024) &&
    path.startsWith("/") &&
    !path.startsWith("//") &&
    !/[\\#\s]/.test(path) &&
    Number.isInteger(timeout) &&
    timeout >= 1 &&
    timeout <= 120
  );
}
export function validateStorageDeploymentOptions(
  value: StorageDeploymentOptions,
  resource: string,
) {
  const fail = () => {
    throw new Error("Storage deployment options scope or evidence mismatch");
  };
  if (
    !closed(value, [
      "resource_id",
      "resource_generation",
      "storage_ref",
      "previous_healthy_operation_id",
      "host_port",
      "container_port",
      "readiness",
      "available",
      "reason",
      "routes",
      "storage_route_continuity",
      ...(Object.hasOwn(value, "stopped_operation_id") ? ["stopped_operation_id"] : []),
      ...(Object.hasOwn(value, "stack_ref") ? ["stack_ref"] : []),
      ...(Object.hasOwn(value, "application_database_storage") ? ["application_database_storage"] : []),
    ]) ||
    value.resource_id !== resource ||
    !positive(value.resource_generation) ||
    typeof value.available !== "boolean" ||
    !reasons.includes(value.reason) ||
    value.available !==
      (value.reason === "available" || value.reason === "initial_deployment") ||
    !Array.isArray(value.routes) ||
    value.routes.length > 32
  )
    fail();
  if (value.stopped_operation_id !== undefined && (!identifier(value.stopped_operation_id) || !value.stack_ref || !value.previous_healthy_operation_id || value.reason === "initial_deployment")) fail();
  if (value.stack_ref !== undefined &&
    (!closed(value.stack_ref, ["generation", "hash"]) ||
      !positive(value.stack_ref.generation) ||
      !/^sha256:[a-f0-9]{64}$/.test(value.stack_ref.hash) ||
      value.storage_ref !== null || value.application_database_storage != null)) fail();
  if (
    value.storage_ref !== null &&
    (!closed(value.storage_ref, ["storage_id", "generation", "hash"]) ||
      !identifier(value.storage_ref.storage_id) ||
      value.storage_ref.generation !== 1 ||
      !text(value.storage_ref.hash, 256))
  )
    fail();
  if (
    (value.previous_healthy_operation_id !== null &&
      !identifier(value.previous_healthy_operation_id)) ||
    (value.host_port !== null &&
      (!Number.isInteger(value.host_port) ||
        value.host_port < 1024 ||
        value.host_port > 65535)) ||
    (value.container_port !== null &&
      (!Number.isInteger(value.container_port) ||
        value.container_port < 1 ||
        value.container_port > 65535)) ||
    (value.readiness !== null &&
      (!closed(value.readiness, ["path", "timeout_seconds"]) ||
        !validStorageReadiness(
          value.readiness.path,
          value.readiness.timeout_seconds,
        )))
  )
    fail();
  const seen = new Set<string>();
  for (const route of value.routes) {
    if (
      !closed(route, [
        "id",
        "hostname",
        "path_prefix",
        "version",
        "selected_release_operation_id",
      ]) ||
      !identifier(route.id) ||
      seen.has(route.id) ||
      !text(route.hostname, 253) ||
      !text(route.path_prefix, 1024) ||
      !route.path_prefix.startsWith("/") ||
      !positive(route.version) ||
      !identifier(route.selected_release_operation_id)
    )
      fail();
    seen.add(route.id);
  }
  const continuity = value.storage_route_continuity;
  const connectedStorage = value.application_database_storage;
  if (connectedStorage !== undefined && connectedStorage !== null &&
    (!closed(connectedStorage, ["version", "strategy", "expected_storage_generation", "expected_storage_hash", "acknowledge_interruption_required"]) ||
      connectedStorage.version !== "application-database.storage.v1" || connectedStorage.strategy !== "replacement_with_interruption" ||
      connectedStorage.acknowledge_interruption_required !== true || !positive(connectedStorage.expected_storage_generation) ||
      !/^sha256:[a-f0-9]{64}$/.test(connectedStorage.expected_storage_hash) ||
      connectedStorage.expected_storage_generation !== value.storage_ref?.generation || connectedStorage.expected_storage_hash !== value.storage_ref?.hash)) fail();
  if (connectedStorage && (continuity !== null || value.routes.some((route) => route.selected_release_operation_id !== value.previous_healthy_operation_id))) fail();
  if (
    continuity !== null &&
    (!closed(continuity, [
      "expected_loaded_generation",
      "expected_loaded_proxy_operation_id",
      "routes",
    ]) ||
      !positive(continuity.expected_loaded_generation) ||
      !identifier(continuity.expected_loaded_proxy_operation_id) ||
      !Array.isArray(continuity.routes) ||
      continuity.routes.length < 1 ||
      continuity.routes.length > 32 ||
      continuity.routes.some(
        (route) =>
          !closed(route, [
            "route_id",
            "expected_version",
            "expected_selected_release_operation_id",
          ]) ||
          !identifier(route.route_id) ||
          !positive(route.expected_version) ||
          !identifier(route.expected_selected_release_operation_id),
      ))
  )
    fail();
  if (value.reason === "initial_deployment") {
    if (
      !(value.storage_ref || value.stack_ref) ||
      value.previous_healthy_operation_id !== null ||
      value.host_port !== null ||
      value.container_port !== null ||
      value.readiness !== null ||
      continuity !== null ||
      value.routes.length !== 0
    )
      fail();
  } else if (value.available) {
    if (
      !(value.storage_ref || value.stack_ref) ||
      !value.previous_healthy_operation_id ||
      value.host_port === null ||
      value.container_port === null ||
      value.readiness === null ||
      !connectedStorage && value.routes.length !== (continuity?.routes.length ?? 0) ||
      value.routes.some(
        (route) =>
          !connectedStorage && !continuity?.routes.some(
            (selection) =>
              selection.route_id === route.id &&
              selection.expected_version === route.version &&
              selection.expected_selected_release_operation_id ===
                route.selected_release_operation_id,
          ),
      )
    )
      fail();
  } else if (continuity !== null) fail();
}
export async function getStorageDeploymentStorage(
  scope: StorageDeploymentScope,
  options: StorageDeploymentOptions,
  binding: string,
  signal?: AbortSignal,
): Promise<HostingStorage> {
  const storage = scopedStorage(
    await boundedStorageRequest(
      (signal) =>
        request<HostingStorage>(`${path(scope.resource)}/storage`, {
          signal,
          cache: "no-store",
        }),
      signal,
    ),
    scope.project,
    scope.resource,
  );
  if (
    storage.id !== options.storage_ref?.storage_id ||
    storage.generation !== options.storage_ref?.generation ||
    storage.hash !== options.storage_ref?.hash ||
    storage.binding_id !== binding ||
    storage.kind !== "platform_managed" ||
    storage.driver !== "local" ||
    storage.access_mode !== "read_write" ||
    storage.retention !== "retain" ||
    !text(storage.volume_name, 255) ||
    !text(storage.mount_path, 1024) ||
    !storage.mount_path.startsWith("/")
  )
    throw new Error("Configured storage does not match deployment authority");
  return storage;
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
  validateStorageContinuityStatus(value, scope, operation);
  return value;
}
function validateStorageContinuityStatus(value: StorageContinuityStatus, scope: StorageDeploymentScope, operation: string) {
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
}
export async function recoverStorageRouteContinuity(scope: StorageDeploymentScope, operation: string, input: StorageRouteRecoveryInput, signal?: AbortSignal): Promise<StorageContinuityStatus> {
  const value = await boundedStorageRequest(signal => request<StorageContinuityStatus>(`${path(scope.resource)}/storage-route-continuity/${encodeURIComponent(operation)}/recover`, { method: "POST", body: input, signal, cache: "no-store" }), signal);
  validateStorageContinuityStatus(value, scope, operation);
  return value;
}
