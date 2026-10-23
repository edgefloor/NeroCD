import { queryOptions } from "@tanstack/react-query";
import type { components } from "./generated";
import { request, type RequestOptions } from "./client";
export type SwitchOptions = components["schemas"]["HostingRouteSwitchOptions"];
export type SwitchInput = components["schemas"]["HostingRouteSwitchInput"];
export type SwitchOperation = components["schemas"]["HostingProxyOperationView"];
export type SwitchScope = { actor: string; project: string; resource: string };
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0;
const text = (value: unknown, limit = 512): value is string => typeof value === "string" && value.length <= limit && !/[\x00-\x1f\x7f]/.test(value);
function fail(): never { throw new Error("Route switch evidence is malformed or belongs to another scope"); }
function keys(value: unknown, required: string[], optional: string[] = []): asserts value is Record<string, unknown> {
  if (!object(value) || required.some((key) => !(key in value)) || Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key))) fail();
}
const reasons = ["available", "unbound", "proxy_unavailable", "busy", "restore_hold", "runner_ineligible", "no_loaded_generation", "stale_routes", "no_routes", "no_targets"];
export function validateSwitchOptions(value: unknown, resource: string, after = ""): asserts value is SwitchOptions {
  keys(value, ["resource_id", "loaded_generation", "available", "reason", "routes", "targets", "next_after_operation_id"], ["pending_operation_id"]);
  if (value.resource_id !== resource || value.loaded_generation !== null && !positive(value.loaded_generation) || typeof value.available !== "boolean" || !reasons.includes(String(value.reason)) || value.available !== (value.reason === "available") || value.available && (!positive(value.loaded_generation) || !Array.isArray(value.routes) || value.routes.length === 0) || value.pending_operation_id !== undefined && (!identifier(value.pending_operation_id) || value.available) || !Array.isArray(value.routes) || value.routes.length > 32 || !Array.isArray(value.targets) || value.targets.length > 50 || value.next_after_operation_id !== null && !identifier(value.next_after_operation_id)) fail();
  const routeIDs = new Set<string>();
  for (const route of value.routes) {
    keys(route, ["id", "hostname", "path_prefix", "version", "selected_release_operation_id"]);
    if (!identifier(route.id) || routeIDs.has(route.id) || !text(route.hostname, 253) || !route.hostname || !text(route.path_prefix, 1024) || !route.path_prefix.startsWith("/") || !positive(route.version) || !identifier(route.selected_release_operation_id)) fail(); routeIDs.add(route.id);
  }
  let previous = after;
  for (const target of value.targets) {
    keys(target, ["operation_id", "kind"], ["resolved_image_identity", "source_generation"]);
    if (!identifier(target.operation_id) || previous && target.operation_id <= previous || !["current_healthy", "retained"].includes(String(target.kind)) || target.resolved_image_identity !== undefined && !text(target.resolved_image_identity, 1024) || target.source_generation !== undefined && !positive(target.source_generation)) fail(); previous = target.operation_id;
  }
  if (value.next_after_operation_id !== null && (value.targets.length !== 50 || value.next_after_operation_id !== previous)) fail();
}
export function validateSwitchOperation(value: unknown, resource: string, operation?: string, intent?: SwitchInput): asserts value is SwitchOperation {
  keys(value, ["id", "state", "mutation_hold", "restore_ownership_held", "routes", "switch"], ["engine_outcome", "metadata_conflict"]);
  if (!identifier(value.id) || operation && value.id !== operation || !text(value.state, 64) || !value.state || typeof value.mutation_hold !== "boolean" || typeof value.restore_ownership_held !== "boolean" || value.metadata_conflict !== undefined && typeof value.metadata_conflict !== "boolean" || value.engine_outcome !== undefined && !["applied", "rolled_back", "refused", "uncertain"].includes(String(value.engine_outcome)) || !Array.isArray(value.routes) || value.routes.length > 32) fail();
  keys(value.switch, ["expected_loaded_generation", "routes"]);
  if (!positive(value.switch.expected_loaded_generation) || !Array.isArray(value.switch.routes) || value.switch.routes.length < 1 || value.switch.routes.length > 32) fail();
  const seen = new Set<string>();
  for (const change of value.switch.routes) {
    keys(change, ["route_id", "before_release_operation_id", "after_release_operation_id"]);
    if (!identifier(change.route_id) || seen.has(change.route_id) || !identifier(change.before_release_operation_id) || !identifier(change.after_release_operation_id)) fail(); seen.add(change.route_id);
  }
  const routeIDs = new Set<string>();
  for (const route of value.routes) {
    keys(route, ["id", "resource_id", "hostname", "path_prefix", "version", "selected_release_operation_id", "enabled", "state", "http_ready", "https_ready", "certificate"], ["operation_id"]);
    if (!identifier(route.id) || routeIDs.has(route.id) || route.resource_id !== resource || !positive(route.version) || !identifier(route.selected_release_operation_id) || !text(route.hostname, 253) || !text(route.path_prefix, 1024) || !text(route.state, 64) || [route.enabled, route.http_ready, route.https_ready].some((flag) => typeof flag !== "boolean") || route.operation_id !== undefined && !identifier(route.operation_id)) fail();
    keys(route.certificate, ["hostname", "status"], ["issuer", "fingerprint", "expires_at"]);
    const certificate = route.certificate;
    if (!text(certificate.hostname, 253) || !text(certificate.status, 64) || ["issuer", "fingerprint", "expires_at"].some((key) => certificate[key] !== undefined && !text(certificate[key], 1024))) fail(); routeIDs.add(route.id);
  }
  if (intent && (intent.expected_loaded_generation !== value.switch.expected_loaded_generation || intent.routes.length !== value.switch.routes.length || value.switch.routes.some((change) => !intent.routes.some((route) => route.route_id === change.route_id && route.expected_selected_release_operation_id === change.before_release_operation_id && intent.target_release_operation_id === change.after_release_operation_id)))) fail();
}
async function bounded(path: string, options: RequestOptions): Promise<unknown> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout>;
  let abort: () => void;
  try {
    return await new Promise((resolve, reject) => {
      const retire = (cause: Error) => { reject(cause); controller.abort(); };
      abort = () => retire(new DOMException("Route switch read retired", "AbortError"));
      timer = setTimeout(() => retire(new Error("Route switch response timed out after 5 seconds")), 5000);
      if (options.signal?.aborted) { abort(); return; }
      options.signal?.addEventListener("abort", abort, { once: true });
      void request(path, { ...options, cache: "no-store", signal: controller.signal }).then((value) => { if (!controller.signal.aborted) resolve(value); }, reject);
    });
  } finally { clearTimeout(timer!); options.signal?.removeEventListener("abort", abort!); }
}
const path = (resource: string) => `/api/v1/resources/${encodeURIComponent(resource)}/route-switches`;
export async function getSwitchOptions(scope: SwitchScope, after = "", signal?: AbortSignal) {
  const value = await bounded(path(scope.resource), { query: after ? { after_operation_id: after } : undefined, signal }); validateSwitchOptions(value, scope.resource, after); return value;
}
export async function getSwitchOperation(scope: SwitchScope, operation: string, signal?: AbortSignal) {
  const value = await bounded(`${path(scope.resource)}/${encodeURIComponent(operation)}`, { signal }); validateSwitchOperation(value, scope.resource, operation); return value;
}
export async function requestSwitch(scope: SwitchScope, body: SwitchInput, signal?: AbortSignal) {
  const value = await bounded(path(scope.resource), { method: "POST", body, signal }); validateSwitchOperation(value, scope.resource, undefined, body); return value;
}
export const switchKeys = { options: (scope: SwitchScope) => ["routeSwitchOptions", scope.actor, scope.project, scope.resource] as const, operation: (scope: SwitchScope, id: string) => ["routeSwitchOperation", scope.actor, scope.project, scope.resource, id] as const };
export const switchOptionsQuery = (scope: SwitchScope, after = "") => queryOptions({ queryKey: [...switchKeys.options(scope), after], retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false, queryFn: ({ signal }) => getSwitchOptions(scope, after, signal) });
export const switchOperationQuery = (scope: SwitchScope, id: string) => queryOptions({ queryKey: switchKeys.operation(scope, id), retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false, queryFn: ({ signal }) => getSwitchOperation(scope, id, signal) });
export function switchResolved(operation: SwitchOperation) {
  return ["applied", "rolled_back", "refused", "superseded", "canceled"].includes(operation.state) && !operation.mutation_hold && !operation.restore_ownership_held && !operation.metadata_conflict && operation.engine_outcome !== "uncertain";
}
