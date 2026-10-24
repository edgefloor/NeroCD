import { queryOptions } from "@tanstack/react-query";
import { ApiError } from "./errors";
import { request } from "./client";
import { boundedDatabaseRequest } from "./hosting-databases";
import type { components } from "./generated";

type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type DatabaseConnection = Schema<"HostingDatabaseConnection">;
export type DatabaseConnectionInput = Schema<"HostingDatabaseConnectionInput">;
export type DatabaseConnectionOptions = Schema<"HostingDatabaseConnectionOptions">;
export type DatabaseConnectionScope = { actor: string; project: string; resource: string };
export type ConnectedDeploymentInput = Schema<"HostingDeploymentInput">;
export type DatabaseMaintenanceInput = Schema<"HostingDatabaseMaintenanceInput">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}`;
const identifier = (v: unknown): v is string => typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const integer = (v: unknown, min = 0) => Number.isSafeInteger(v) && Number(v) >= min;
const text = (v: unknown, max = 1024): v is string => typeof v === "string" && v.length <= max && !/[\x00-\x1f\x7f]/.test(v);
function closed(value: unknown, required: string[], optional: string[] = []): boolean {
  return Boolean(value && typeof value === "object" && !Array.isArray(value) && required.every((key) => Object.hasOwn(value, key)) && Object.keys(value).every((key) => [...required, ...optional].includes(key)));
}
export function validateConnection(value: DatabaseConnection, scope: DatabaseConnectionScope, database?: string): DatabaseConnection {
  if (!closed(value, ["id", "project_id", "application_resource_id", "database_resource_id", "binding_id", "binding_hash", "state", "reason_code", "capture_id", "upgrade_operation_id", "application_operation_id", "membership_revision", "created_at", "updated_at"], ["hold_operation_id", "recovery_limit"]) ||
    ![value.id, value.database_resource_id, value.binding_id].every(identifier) || value.application_resource_id !== scope.resource || value.project_id !== scope.project || (database !== undefined && value.database_resource_id !== database) ||
    !/^sha256:[a-f0-9]{64}$/.test(value.binding_hash) || !integer(value.membership_revision) ||
    ![value.state, value.reason_code, value.created_at, value.updated_at].every((v) => text(v)) || !value.state ||
    ![value.capture_id, value.upgrade_operation_id, value.application_operation_id, value.hold_operation_id ?? ""].every((v) => v === "" || identifier(v)) || !text(value.recovery_limit ?? "")) throw new Error("Database connection metadata scope mismatch");
  return value;
}
export function validateConnectionOptions(value: DatabaseConnectionOptions): DatabaseConnectionOptions {
  if (!closed(value, ["items"]) || !Array.isArray(value.items) || value.items.length > 1000 || new Set(value.items.map((v) => v.resource_id)).size !== value.items.length || value.items.some((v) =>
    !closed(v, ["resource_id", "name", "engine", "generation", "destination_id", "status", "reason_code"]) || !identifier(v.resource_id) || !identifier(v.destination_id) || !integer(v.generation, 1) || !text(v.name, 255) || !v.name || !["postgresql", "redis"].includes(v.engine) || !text(v.status, 128) || !text(v.reason_code, 128))) throw new Error("Invalid database connection options");
  return value;
}
export async function getDatabaseConnection(scope: DatabaseConnectionScope, signal?: AbortSignal): Promise<DatabaseConnection | null> {
  try { return validateConnection(await boundedDatabaseRequest((signal) => request<DatabaseConnection>(`${path(scope.resource)}/database-connection`, { signal, cache: "no-store" }), signal), scope); }
  catch (cause) { if (cause instanceof ApiError && cause.status === 404) return null; throw cause; }
}
export async function getDatabaseConnectionOptions(scope: DatabaseConnectionScope, signal?: AbortSignal): Promise<DatabaseConnectionOptions> {
  return validateConnectionOptions(await boundedDatabaseRequest((signal) => request<DatabaseConnectionOptions>(`${path(scope.resource)}/database-connections/options`, { signal, cache: "no-store" }), signal));
}
export const connectionKey = (scope: DatabaseConnectionScope) => ["databaseConnection", scope.actor, scope.project, scope.resource] as const;
export const connectionQuery = (scope: DatabaseConnectionScope) => queryOptions({ queryKey: connectionKey(scope), queryFn: ({ signal }) => getDatabaseConnection(scope, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false });
export const connectionOptionsQuery = (scope: DatabaseConnectionScope) => queryOptions({ queryKey: [...connectionKey(scope), "options"], queryFn: ({ signal }) => getDatabaseConnectionOptions(scope, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false });
export async function connectDatabase(scope: DatabaseConnectionScope, body: DatabaseConnectionInput, signal?: AbortSignal) {
  return validateConnection(await boundedDatabaseRequest((signal) => request<DatabaseConnection>(`${path(scope.resource)}/database-connections`, { method: "POST", body, signal, cache: "no-store" }), signal), scope, body.binding.database_resource_id);
}
async function operationMutation(scope: DatabaseConnectionScope, target: string, suffix: string, body: ConnectedDeploymentInput | DatabaseMaintenanceInput, signal?: AbortSignal) {
  const value = await boundedDatabaseRequest((signal) => request<Schema<"HostingOperation">>(`${path(target)}/${suffix}`, { method: "POST", body, signal, cache: "no-store" }), signal);
  if (!value || !identifier(value.id) || value.project_id !== scope.project || value.resource_id !== target || !value.snapshot || value.snapshot.resource_id !== target) throw new Error("Database operation scope mismatch");
  return value;
}
export const deployConnectedApplication = (scope: DatabaseConnectionScope, body: ConnectedDeploymentInput, signal?: AbortSignal) => operationMutation(scope, scope.resource, "database-connection/deployments", body, signal);
export const maintainConnectedDatabase = (scope: DatabaseConnectionScope, database: string, body: DatabaseMaintenanceInput, signal?: AbortSignal) => operationMutation(scope, database, "database/maintenance", body, signal);
export function connectionError(cause: unknown): string {
  if (!(cause instanceof ApiError)) return "No confirmed response. Keep the request and retry it identically to recover its result.";
  const messages: Record<number, string> = { 400: "Settings were rejected. Your draft is preserved for correction.", 401: "Access is unavailable. Refresh project access.", 403: "Access is unavailable. Refresh project access.", 404: "The selected connection or resource is unavailable.", 409: "Saved configuration or membership changed. Refresh and explicitly review before a new request.", 503: "Database connections are temporarily unavailable. A submitted request may be unconfirmed; retain it for an identical retry." };
  return messages[cause.status] ?? "The request failed. Review the recorded connection before retrying.";
}
