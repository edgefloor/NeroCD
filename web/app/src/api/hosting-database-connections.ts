import { queryOptions } from "@tanstack/react-query";
import { ApiError } from "./errors";
import { request } from "./client";
import { boundedDatabaseRequest } from "./hosting-databases";
import type { components } from "./generated";
import type { SwitchOptions } from "./hosting-route-switch";
import { getResourceBinding } from "./destinations";
import { getStorageDeploymentOptions, getStorageDeploymentStorage } from "./hosting-storage-deployment";

type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type DatabaseConnection = Schema<"HostingDatabaseConnection">;
export type DatabaseConnectionInput = Schema<"HostingDatabaseConnectionInput">;
export type DatabaseConnectionOptions = Schema<"HostingDatabaseConnectionOptions">;
export type DatabaseConnectionScope = { actor: string; project: string; resource: string };
export type ConnectedDeploymentInput = Schema<"HostingDeploymentInput">;
export async function getConnectionStorageReview(scope: DatabaseConnectionScope, signal?: AbortSignal) {
  const options = await getStorageDeploymentOptions(scope, signal);
  if (!options.storage_ref) return { options, storage: null };
  const binding = await boundedDatabaseRequest((signal) => getResourceBinding(scope.resource, { signal }), signal);
  if (binding.resource_id !== scope.resource) throw new Error("Storage binding scope mismatch");
  const storage = await getStorageDeploymentStorage(scope, options, binding.id, signal);
  if (storage.destination_id !== binding.destination_id) throw new Error("Storage destination scope mismatch");
  return { options, storage: { id: storage.id, project_id: storage.project_id, resource_id: storage.resource_id, binding_id: storage.binding_id, destination_id: storage.destination_id, generation: storage.generation, hash: storage.hash, kind: storage.kind, driver: storage.driver, access_mode: storage.access_mode, retention: storage.retention, volume_name: storage.volume_name, mount_path: storage.mount_path } };
}
export const connectionStorageReviewQuery = (scope: DatabaseConnectionScope) => queryOptions({ queryKey: [...connectionKey(scope), "storageReview"], retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: ({ signal }) => getConnectionStorageReview(scope, signal) });
export function connectionRouteIntent(options: SwitchOptions): ConnectedDeploymentInput["application_database_route_continuity"] {
  const healthy = options.targets.find((target) => target.kind === "current_healthy");
  if (!options.available || options.pending_operation_id || !options.loaded_generation || !options.loaded_proxy_operation_id || !healthy || !options.routes.length || options.routes.some((route) => route.selected_release_operation_id !== healthy.operation_id)) return undefined;
  return { expected_loaded_generation: options.loaded_generation, expected_loaded_proxy_operation_id: options.loaded_proxy_operation_id, routes: options.routes.map((route) => ({ route_id: route.id, expected_version: route.version, expected_selected_release_operation_id: route.selected_release_operation_id })) };
}
export type StorageRecoveryAction = "resume_predecessor" | "complete_replacement";
export type ConnectionStorageRecoveryInput = Schema<"HostingDatabaseConnectionRecoveryInput"> & { action: StorageRecoveryAction; storage_state_hash: string; application_database_storage: Schema<"HostingApplicationDatabaseStorageInput"> };
export type ConnectedProgress = { storageState?: { recovery_action: "" | StorageRecoveryAction; recovery_state: "none" | "queued" | "running" | "succeeded" | "uncertain"; phase: string; state_hash: string; stage_hash: string; candidate_hash: string; recovery_actions: StorageRecoveryAction[] }; storage?: { strategy: "replacement_with_interruption"; storage_id: string; generation: 1; hash: string }; storagePhase?: string; id: string; state: string; mutation_hold: boolean; restore_ownership_held: boolean; proxy_operation_id?: string; yielded: boolean };
export const connectedProgressQuery = (scope: DatabaseConnectionScope, operation: string) => queryOptions({
  queryKey: [...connectionKey(scope), "operation", operation], retry: false, gcTime: 0, refetchOnWindowFocus: false,
  queryFn: async ({ signal }): Promise<ConnectedProgress> => {
    const value = await boundedDatabaseRequest((signal) => request<Record<string, unknown>>(`/api/v1/hosting-operations/${encodeURIComponent(operation)}`, { signal, cache: "no-store" }), signal);
    if (!value || value.id !== operation || value.project_id !== scope.project || value.resource_id !== scope.resource || !text(value.state, 64) || !value.state || typeof value.mutation_hold !== "boolean" || typeof value.restore_ownership_held !== "boolean") throw new Error("Database operation scope mismatch");
    const continuity = value.application_database_route_continuity;
    let proxy: string | undefined;
    if (continuity !== undefined) {
      if (!continuity || typeof continuity !== "object" || Array.isArray(continuity) || !("operation_id" in continuity) || continuity.operation_id !== operation || !("proxy_operation_id" in continuity) || !identifier(continuity.proxy_operation_id)) throw new Error("Database continuation scope mismatch");
      proxy = continuity.proxy_operation_id;
    }
    if (value.application_database_yield !== undefined) {
      const yielded = value.application_database_yield as Record<string, unknown>;
      if (!closed(yielded, ["version", "receipt_id", "plan_hash", "stage_hash", "candidate_hash", "continuation_hash"]) || yielded.version !== "application-database.route-continuity.v1" || !identifier(yielded.receipt_id) || [yielded.plan_hash, yielded.stage_hash, yielded.candidate_hash, yielded.continuation_hash].some((hash) => typeof hash !== "string" || !/^sha256:[a-f0-9]{64}$/.test(hash))) throw new Error("Database candidate evidence is malformed");
    }
    let storage: ConnectedProgress["storage"];
    const descriptor = value.application_database_storage;
    const hash = (value: unknown) => typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
    if (descriptor !== undefined) {
      if (!closed(descriptor, ["version", "strategy", "storage_ref", "hash"])) throw new Error("Database storage descriptor is malformed");
      const d = descriptor as Record<string, unknown>, ref = d.storage_ref as Record<string, unknown>;
      if (d.version !== "application-database.storage.v1" || d.strategy !== "replacement_with_interruption" || !hash(d.hash) || !closed(ref, ["storage_id", "generation", "hash"]) || !identifier(ref.storage_id) || ref.generation !== 1 || !hash(ref.hash)) throw new Error("Database storage descriptor is malformed");
      storage = { strategy: "replacement_with_interruption", storage_id: ref.storage_id, generation: 1, hash: String(ref.hash) };
    }
    let storagePhase: string | undefined;
    let storageState: ConnectedProgress["storageState"];
    if (value.application_database_storage_state !== undefined) {
      const s = value.application_database_storage_state as Record<string, unknown>;
      if (!storage || !closed(s, ["version", "phase", "receipt_id", "state_hash", "previous_state_hash", "candidate_hash", "stage_hash", "recovery_actions", "recovery_action", "recovery_state"]) || s.version !== "application-database.storage.v1" || !["stop_intent", "stopped", "candidate_stopped", "replacement_failed", "resume_intent", "resumed"].includes(String(s.phase)) || !["", "resume_predecessor", "complete_replacement"].includes(String(s.recovery_action)) || !["none", "queued", "running", "succeeded", "uncertain"].includes(String(s.recovery_state)) || (s.recovery_action === "") !== (s.recovery_state === "none") || !Array.isArray(s.recovery_actions) || s.recovery_actions.length > 1 || new Set(s.recovery_actions).size !== s.recovery_actions.length || s.recovery_actions.some((action) => !["resume_predecessor", "complete_replacement"].includes(String(action))) || !identifier(s.receipt_id) || !hash(s.state_hash) || s.previous_state_hash !== "" && !hash(s.previous_state_hash) || s.candidate_hash !== "" && !hash(s.candidate_hash) || s.stage_hash !== "" && !hash(s.stage_hash) || (s.stage_hash === "") !== (s.candidate_hash === "")) throw new Error("Database storage state is malformed");
      storagePhase = String(s.phase);
      storageState = { recovery_action: s.recovery_action as "" | StorageRecoveryAction, recovery_state: s.recovery_state as NonNullable<ConnectedProgress["storageState"]>["recovery_state"], phase: storagePhase, state_hash: String(s.state_hash), stage_hash: String(s.stage_hash), candidate_hash: String(s.candidate_hash), recovery_actions: s.recovery_actions as StorageRecoveryAction[] };
    }
    return { ...(storageState ? { storageState } : {}), ...(storage ? { storage } : {}), ...(storagePhase ? { storagePhase } : {}), id: operation, state: value.state, mutation_hold: value.mutation_hold, restore_ownership_held: value.restore_ownership_held, proxy_operation_id: proxy, yielded: value.application_database_yield !== undefined };
  },
});
export type DatabaseMaintenanceInput = Schema<"HostingDatabaseMaintenanceInput">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}`;
const identifier = (v: unknown): v is string => typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const integer = (v: unknown, min = 0) => Number.isSafeInteger(v) && Number(v) >= min;
const text = (v: unknown, max = 1024): v is string => typeof v === "string" && v.length <= max && !/[\x00-\x1f\x7f]/.test(v);
function closed(value: unknown, required: string[], optional: string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return Boolean(value && typeof value === "object" && !Array.isArray(value) && required.every((key) => Object.hasOwn(value, key)) && Object.keys(value).every((key) => allowed.has(key)));
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
    !closed(v, ["resource_id", "name", "engine", "generation", "destination_id", "status", "reason_code"]) || !identifier(v.resource_id) || !identifier(v.destination_id) || !integer(v.generation, 1) || !text(v.name, 255) || !v.name || !["postgresql", "mysql", "redis"].includes(v.engine) || !text(v.status, 128) || !text(v.reason_code, 128))) throw new Error("Invalid database connection options");
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
async function operationMutation(scope: DatabaseConnectionScope, target: string, suffix: string, body: ConnectedDeploymentInput | DatabaseMaintenanceInput | ConnectionStorageRecoveryInput, signal?: AbortSignal) {
  const value = await boundedDatabaseRequest((signal) => request<Schema<"HostingOperation">>(`${path(target)}/${suffix}`, { method: "POST", body, signal, cache: "no-store" }), signal);
  if (!value || !identifier(value.id) || value.project_id !== scope.project || value.resource_id !== target || !value.snapshot || value.snapshot.resource_id !== target) throw new Error("Database operation scope mismatch");
  return value;
}
export const deployConnectedApplication = (scope: DatabaseConnectionScope, body: ConnectedDeploymentInput, signal?: AbortSignal) => operationMutation(scope, scope.resource, "database-connection/deployments", body, signal);
export async function recoverConnectionStorage(scope: DatabaseConnectionScope, body: ConnectionStorageRecoveryInput, signal?: AbortSignal) {
  const value = await operationMutation(scope, scope.resource, "database-connection/recovery", body, signal);
  if (value.id !== body.operation_id) throw new Error("Recovery operation scope mismatch");
  return value;
}
export function storageRecoveryInput(progress: ConnectedProgress, connection: DatabaseConnection, action: StorageRecoveryAction, key: string): ConnectionStorageRecoveryInput {
  if (!progress.storage || !progress.storageState?.recovery_actions.includes(action) || connection.hold_operation_id !== progress.id || !integer(connection.membership_revision, 1)) throw new ApiError(409, "Recovery eligibility changed", null);
  return { action, operation_id: progress.id, stage_hash: progress.storageState.stage_hash, candidate_hash: progress.storageState.candidate_hash, expected_membership_revision: connection.membership_revision, idempotency_key: key, storage_state_hash: progress.storageState.state_hash, application_database_storage: { expected_storage_generation: progress.storage.generation, expected_storage_hash: progress.storage.hash, acknowledge_interruption: true } };
}
export const maintainConnectedDatabase = (scope: DatabaseConnectionScope, database: string, body: DatabaseMaintenanceInput, signal?: AbortSignal) => operationMutation(scope, database, "database/maintenance", body, signal);
export function connectionError(cause: unknown): string {
  if (!(cause instanceof ApiError)) return "No confirmed response. Keep the request and retry it identically to recover its result.";
  const messages: Record<number, string> = { 400: "Settings were rejected. Your draft is preserved for correction.", 401: "Access is unavailable. Refresh project access.", 403: "Access is unavailable. Refresh project access.", 404: "The selected connection or resource is unavailable.", 409: "Saved configuration or membership changed. Refresh and explicitly review before a new request.", 503: "Database connections are temporarily unavailable. A submitted request may be unconfirmed; retain it for an identical retry." };
  return messages[cause.status] ?? "The request failed. Review the recorded connection before retrying.";
}
