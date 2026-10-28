import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";

export type HostingDatabase = components["schemas"]["HostingDatabase"];
export type HostingDatabaseCreateInput =
  components["schemas"]["HostingDatabaseCreateInput"];
export type DatabaseScope = {
  actor: string;
  project: string;
  environment: string;
};
export const databaseRecipes = {
  postgresql: "postgresql-17.11-bookworm.v1",
  redis: "redis-8.2.10-bookworm.v1",
  mysql: "mysql-8.4.13-oraclelinux9.v1",
} as const;
const identifier = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const text = (value: unknown, max = 255): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[\x00-\x1f\x7f]/.test(value);
const closed = (value: unknown, keys: string[]) =>
  typeof value === "object" &&
  value !== null &&
  !Array.isArray(value) &&
  Object.keys(value).length === keys.length &&
  keys.every((key) => Object.hasOwn(value, key));
export const databaseDenied = (cause: unknown) =>
  cause instanceof ApiError && [401, 403].includes(cause.status);
export function databaseError(cause: unknown) {
  if (!(cause instanceof ApiError))
    return "Response unconfirmed or metadata invalid. Keep the request identity and review before retrying.";
  const message: Record<number, string> = {
    400: "Database settings were rejected.",
    401: "Database access is unavailable.",
    403: "Database access is unavailable.",
    404: "Database metadata or placement is unavailable.",
    409: "Database request conflicts with current configuration or its retained identity.",
    503: "Database service is temporarily unavailable. Delivery may be unconfirmed.",
  };
  return `${message[cause.status] ?? "Database request failed."} HTTP ${cause.status}${cause.requestID ? ` · request ${cause.requestID}` : ""}`;
}
export function databaseRequestID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}
export async function boundedDatabaseRequest<T>(
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
        retire(new DOMException("Database scope retired", "AbortError"));
      timer = setTimeout(
        () => retire(new Error("Database response timed out after 30 seconds")),
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
export function validateDatabase(
  value: HostingDatabase,
  scope?: Pick<DatabaseScope, "project" | "environment">,
  resourceID?: string,
  intent?: HostingDatabaseCreateInput,
): HostingDatabase {
  const fail = () => {
    throw new Error("Database metadata scope or recipe mismatch");
  };
  if (
    !closed(value, [
      "resource",
      "binding",
      "storage_ref",
      "engine",
      "recipe_version",
      "credentials",
      "runtime_state",
    ]) ||
    !Object.hasOwn(databaseRecipes, value.engine) ||
    databaseRecipes[value.engine] !== value.recipe_version ||
    value.runtime_state !== "adapter_unqualified"
  )
    fail();
  const r = value.resource,
    b = value.binding,
    s = value.storage_ref;
  if (
    !closed(r, [
      "id",
      "project_id",
      "project_environment_id",
      "name",
      "kind",
      "source",
      "generation",
      "created_by",
      "created_at",
      "updated_at",
    ]) ||
    !identifier(r.id) ||
    !identifier(r.project_id) ||
    !identifier(r.project_environment_id) ||
    !text(r.name) ||
    r.kind !== "database" ||
    !Number.isSafeInteger(r.generation) ||
    r.generation < 1 ||
    !r.source ||
    r.source.kind !== "image" ||
    !text(r.source.image?.reference, 1024) ||
    (scope &&
      (r.project_id !== scope.project ||
        r.project_environment_id !== scope.environment)) ||
    (resourceID && r.id !== resourceID)
  )
    fail();
  if (
    !closed(b, [
      "id",
      "resource_id",
      "destination_id",
      "runtime_namespace",
      "created_by",
      "created_at",
    ]) ||
    !identifier(b.id) ||
    b.resource_id !== r.id ||
    !identifier(b.destination_id) ||
    !identifier(b.runtime_namespace) ||
    !closed(s, ["storage_id", "generation", "hash"]) ||
    !identifier(s.storage_id) ||
    s.generation !== 1 ||
    !text(s.hash, 256)
  )
    fail();
  const roles =
    value.engine === "postgresql"
      ? { admin: "postgres", app: "ncd_app" }
      : value.engine === "mysql" ? { admin: "root", app: "ncd_app" }
      : { app: "ncd_app", probe: "ncd_probe" };
  if (
    !Array.isArray(value.credentials) ||
    value.credentials.length !== 2 ||
    new Set(value.credentials.map((credential) => credential.role)).size !== 2
  )
    fail();
  for (const credential of value.credentials)
    if (
      !closed(credential, ["role", "username", "secret_id", "version"]) ||
      !Object.hasOwn(roles, credential.role) ||
      roles[credential.role as keyof typeof roles] !== credential.username ||
      !identifier(credential.secret_id) ||
      credential.version !== 1
    )
      fail();
  if (
    intent &&
    (r.project_id !== intent.project_id ||
      r.project_environment_id !== intent.project_environment_id ||
      r.name !== intent.name ||
      b.destination_id !== intent.destination_id ||
      value.engine !== intent.engine ||
      value.recipe_version !== intent.recipe_version)
  )
    fail();
  return value;
}
export async function createHostingDatabase(
  input: HostingDatabaseCreateInput,
  options?: { signal?: AbortSignal },
): Promise<HostingDatabase> {
  if (
    !closed(input, [
      "request_id",
      "project_id",
      "project_environment_id",
      "name",
      "destination_id",
      "engine",
      "recipe_version",
    ]) ||
    !/^[a-f0-9]{32}$/.test(input.request_id) ||
    !identifier(input.project_id) ||
    !identifier(input.project_environment_id) ||
    !identifier(input.destination_id) ||
    !text(input.name) ||
    !Object.hasOwn(databaseRecipes, input.engine) ||
    databaseRecipes[input.engine] !== input.recipe_version
  )
    throw new Error("Invalid database creation intent");
  return validateDatabase(
    await boundedDatabaseRequest(
      (signal) =>
        request<HostingDatabase>("/api/v1/databases", {
          method: "POST",
          body: input,
          signal,
          cache: "no-store",
        }),
      options?.signal,
    ),
    undefined,
    undefined,
    input,
  );
}
export async function getHostingDatabase(
  resourceID: string,
  options?: { signal?: AbortSignal },
): Promise<HostingDatabase> {
  return validateDatabase(
    await boundedDatabaseRequest(
      (signal) =>
        request<HostingDatabase>(
          `/api/v1/resources/${encodeURIComponent(resourceID)}/database`,
          { signal, cache: "no-store" },
        ),
      options?.signal,
    ),
    undefined,
    resourceID,
  );
}
export const databaseKey = (scope: DatabaseScope, resource: string) =>
  [
    "hostingDatabase",
    scope.actor,
    scope.project,
    scope.environment,
    resource,
  ] as const;
export const databaseQuery = (scope: DatabaseScope, resource: string) =>
  queryOptions({
    queryKey: databaseKey(scope, resource),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) =>
      validateDatabase(
        await getHostingDatabase(resource, { signal }),
        scope,
        resource,
      ),
  });
