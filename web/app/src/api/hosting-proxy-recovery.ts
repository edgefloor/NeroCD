import { queryOptions } from "@tanstack/react-query";
import type { components } from "./generated";
import { request, type RequestOptions } from "./client";
type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type RecoveryStatus = Schema<"HostingProxyRecoveryStatus">;
export type RecoveryReview = Schema<"HostingProxyRecoveryReview">;
export type RecoveryPlan = Schema<"HostingProxyRecoveryPlan">;
export type RecoveryResolution = Schema<"HostingProxyRecoveryResolution">;
export type RecoveryInput = Schema<"HostingProxyRecoveryInput">;
export type RecoveryApply = Schema<"HostingProxyRecoveryApplyInput">;
export type RecoveryScope = { actor: string; actorID: string; server: string; project: string; proxy: string; operation: string };
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
export const recoveryID = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0;
const hash = (value: unknown): value is string => typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
const text = (value: unknown, limit = 512): value is string => typeof value === "string" && value.length <= limit && !/[\x00-\x1f\x7f]/.test(value);
const date = (value: unknown): value is string => typeof value === "string" && value.length <= 64 && Number.isFinite(Date.parse(value));
function fail(): never { throw new Error("Recovery evidence is malformed or belongs to another scope"); }
function keys(value: unknown, required: string[], optional: string[] = []): asserts value is Record<string, unknown> {
  const allowed = new Set([...required, ...optional]);
  if (!object(value) || required.some(key => !(key in value)) || Object.keys(value).some(key => !allowed.has(key))) fail();
}
function target(value: unknown): asserts value is Schema<"HostingProxyRouteTarget"> {
  keys(value, ["route_id", "project_id", "resource_id", "binding_id", "release_operation_id", "hostname", "path_prefix", "host_port"]);
  if ([value.route_id, value.project_id, value.resource_id, value.binding_id, value.release_operation_id].some(id => !recoveryID(id)) || !text(value.hostname, 253) || !value.hostname || !text(value.path_prefix, 1024) || !value.path_prefix.startsWith("/") || !positive(value.host_port) || value.host_port > 65535) fail();
}
function same(a: unknown, b: unknown): boolean {
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((item, index) => same(item, b[index]));
  if (object(a) && object(b)) return Object.keys(a).length === Object.keys(b).length && Object.keys(a).every(key => same(a[key], b[key]));
  return a === b;
}
export function validateRecoveryReview(value: unknown, scope: RecoveryScope): asserts value is RecoveryReview {
  keys(value, ["version", "operation_id", "proxy_id", "actor_id", "loaded_generation", "desired_generation", "prior_operation_id", "prior_snapshot_hash", "complete_receipt_id", "complete_receipt_hash", "engine_outcome", "facts_hash", "routes", "convergence_routes"]);
  if (value.version !== 1 || value.operation_id !== scope.operation || value.proxy_id !== scope.proxy || value.actor_id !== scope.actorID || !positive(value.loaded_generation) || !positive(value.desired_generation) || value.desired_generation < value.loaded_generation || !recoveryID(value.prior_operation_id) || !recoveryID(value.complete_receipt_id) || !hash(value.prior_snapshot_hash) || !hash(value.complete_receipt_hash) || !hash(value.facts_hash) || !["rolled_back", "refused"].includes(String(value.engine_outcome)) || !Array.isArray(value.routes) || value.routes.length < 1 || value.routes.length > 32 || !Array.isArray(value.convergence_routes) || value.convergence_routes.length > 32) fail();
  const seen = new Set();
  for (const route of value.routes) {
    keys(route, ["route_id", "version", "selected_release_operation_id", "enabled", "authorized", "prior_target"]); target(route.prior_target);
    if (!recoveryID(route.route_id) || seen.has(route.route_id) || !positive(route.version) || !recoveryID(route.selected_release_operation_id) || typeof route.enabled !== "boolean" || typeof route.authorized !== "boolean" || route.prior_target.route_id !== route.route_id) fail(); seen.add(route.route_id);
  }
  const convergence = new Set();
  for (const route of value.convergence_routes) { target(route); if (convergence.has(route.route_id)) fail(); convergence.add(route.route_id); }
}
export function validateRecoveryResolution(value: unknown, scope: RecoveryScope, plan?: RecoveryPlan): asserts value is RecoveryResolution {
  keys(value, ["id", "operation_id", "plan_id", "plan_hash", "actor_id", "engine_outcome", "complete_receipt_id", "complete_receipt_hash", "convergence_operation_id", "routes", "created_at"]);
  if (!recoveryID(value.id) || value.operation_id !== scope.operation || !recoveryID(value.plan_id) || !hash(value.plan_hash) || !recoveryID(value.actor_id) || !["rolled_back", "refused"].includes(String(value.engine_outcome)) || !recoveryID(value.complete_receipt_id) || !hash(value.complete_receipt_hash) || !recoveryID(value.convergence_operation_id) || !date(value.created_at) || !Array.isArray(value.routes) || value.routes.length < 1 || value.routes.length > 32) fail();
  const seen = new Set();
  for (const route of value.routes) {
    keys(route, ["route_id", "before_version", "after_version", "before_release_operation_id", "after_release_operation_id", "enabled"]);
    if (!recoveryID(route.route_id) || seen.has(route.route_id) || !positive(route.before_version) || !positive(route.after_version) || !recoveryID(route.before_release_operation_id) || !recoveryID(route.after_release_operation_id) || typeof route.enabled !== "boolean" || route.after_version !== route.before_version + (route.before_release_operation_id === route.after_release_operation_id ? 0 : 1)) fail(); seen.add(route.route_id);
  }
  if (plan && (value.plan_id !== plan.id || value.plan_hash !== plan.plan_hash || value.actor_id !== scope.actorID || value.engine_outcome !== plan.review.engine_outcome || value.complete_receipt_id !== plan.review.complete_receipt_id || value.complete_receipt_hash !== plan.review.complete_receipt_hash || value.routes.length !== plan.review.routes.length || value.routes.some(change => !plan.review.routes.some(route => change.route_id === route.route_id && change.before_version === route.version && change.before_release_operation_id === route.selected_release_operation_id && change.after_release_operation_id === route.prior_target.release_operation_id && change.enabled === route.enabled)))) fail();
}
export function validateRecoveryStatus(value: unknown, scope: RecoveryScope): asserts value is RecoveryStatus {
  keys(value, ["operation_id", "eligible", "reason"], ["review", "resolution"]);
  if (value.operation_id !== scope.operation || typeof value.eligible !== "boolean" || !["unsupported_or_unfinished", "completed_metadata_conflict", "resolved"].includes(String(value.reason))) fail();
  if (value.reason === "completed_metadata_conflict") { if (!value.eligible || value.resolution !== undefined) fail(); validateRecoveryReview(value.review, scope); }
  else if (value.reason === "resolved") { if (value.eligible || value.review !== undefined) fail(); validateRecoveryResolution(value.resolution, scope); }
  else if (value.eligible || value.review !== undefined || value.resolution !== undefined) fail();
}
export function validateRecoveryPlan(value: unknown, scope: RecoveryScope, review: RecoveryReview): asserts value is RecoveryPlan {
  keys(value, ["id", "review", "plan_hash", "created_at", "expires_at"]); validateRecoveryReview(value.review, scope);
  if (!recoveryID(value.id) || !hash(value.plan_hash) || !date(value.created_at) || !date(value.expires_at) || Date.parse(value.expires_at) - Date.parse(value.created_at) !== 30_000 || !same(value.review, review)) fail();
}
async function bounded(path: string, options: RequestOptions): Promise<unknown> {
  const controller = new AbortController(); let timer: ReturnType<typeof setTimeout>; let abort: () => void;
  try { return await new Promise((resolve, reject) => {
    const retire = (error: Error) => { reject(error); controller.abort(); };
    abort = () => retire(new DOMException("Recovery request retired", "AbortError")); timer = setTimeout(() => retire(new Error("Recovery response timed out after 5 seconds")), 5000);
    if (options.signal?.aborted) { abort(); return; } options.signal?.addEventListener("abort", abort, { once: true });
    void request(path, { ...options, cache: "no-store", signal: controller.signal }).then(value => { if (!controller.signal.aborted) resolve(value); }, reject);
  }); } finally { clearTimeout(timer!); options.signal?.removeEventListener("abort", abort!); }
}
const path = (scope: RecoveryScope) => `/api/v1/proxy-operations/${encodeURIComponent(scope.operation)}`;
export async function readRecovery(scope: RecoveryScope, signal?: AbortSignal) {
  // A resolution DTO has no proxy field. Verify its exact administrator-only
  // operation first, without placing the whole snapshot into this query cache.
  const operation = await bounded(path(scope), { signal });
  if (!object(operation) || operation.id !== scope.operation || operation.proxy_id !== scope.proxy || !object(operation.snapshot) || operation.snapshot.proxy_id !== scope.proxy || operation.snapshot.server_id !== scope.server) fail();
  const value = await bounded(`${path(scope)}/recovery`, { signal }); validateRecoveryStatus(value, scope); return value; }
export async function planRecovery(scope: RecoveryScope, body: RecoveryInput, review: RecoveryReview, signal?: AbortSignal) { const value = await bounded(`${path(scope)}/recovery-plans`, { method: "POST", body, signal }); validateRecoveryPlan(value, scope, review); return value; }
export async function applyRecovery(scope: RecoveryScope, plan: RecoveryPlan, body: RecoveryApply, signal?: AbortSignal) { const value = await bounded(`${path(scope)}/recovery-plans/${encodeURIComponent(plan.id)}/apply`, { method: "POST", body, signal }); validateRecoveryResolution(value, scope, plan); return value; }
export const recoveryKey = (scope: RecoveryScope) => ["proxyRecovery", scope.actor, scope.project, scope.proxy, scope.operation] as const;
export const recoveryQuery = (scope: RecoveryScope) => queryOptions({ queryKey: recoveryKey(scope), retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false, queryFn: ({ signal }) => readRecovery(scope, signal) });
