import type { components } from "./generated";
import { request, type RequestOptions } from "./client";
export type HostingRuntimeStatus = components["schemas"]["HostingRuntimeStatus"];
export type RuntimeStatusSnapshot = { status: HostingRuntimeStatus; receivedMonotonic: number; receivedWall: number; requestDuration: number };
const states = ["running", "exited", "missing", "unknown"];
const readiness = ["ready", "not_ready", "not_checked", "unknown"];
const errors = ["", "daemon_unreachable", "daemon_mismatch", "target_mismatch", "inspect_failed", "readiness_timeout"];
const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const id = (value: unknown): value is string => typeof value === "string" && value.length <= 256 && /^[\x21-\x7e]*$/.test(value);
export function statusTimestamp(value: unknown): number {
  return typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) ? Date.parse(value) : NaN;
}
function observation(value: unknown): boolean {
  if (!record(value) || !id(value.binding_id) || !id(value.sample_id) || !Number.isInteger(value.sample_duration_ms) || Number(value.sample_duration_ms) < 0 || Number(value.sample_duration_ms) > 5000 || !states.includes(String(value.container_state)) || !readiness.includes(String(value.readiness)) || !errors.includes(String(value.collection_error))) return false;
  if (value.exit_code !== undefined && (!Number.isInteger(value.exit_code) || Number(value.exit_code) < 0 || Number(value.exit_code) > 255 || value.container_state !== "exited")) return false;
  if (value.readiness === "ready" && (value.container_state !== "running" || value.collection_error !== "")) return false;
  if (value.collection_error !== "" && value.collection_error !== "readiness_timeout" && (value.container_state !== "unknown" || value.readiness !== "unknown")) return false;
  if (value.collection_error === "readiness_timeout" && (value.container_state !== "running" || value.readiness !== "unknown")) return false;
  return value.container_state === "running" || ["not_checked", "unknown"].includes(String(value.readiness));
}
export function validateRuntimeStatus(value: unknown, resourceID: string): asserts value is HostingRuntimeStatus {
  if (!record(value) || value.resource_id !== resourceID || !["binding_id", "destination_id", "server_id", "runner_id", "current_operation_id", "current_container_id", "observed_operation_id", "observed_container_id"].every((key) => id(value[key])) || !Number.isFinite(statusTimestamp(value.server_time)) || value.stale_after_seconds !== 30 || value.target_interval_seconds !== 10 || value.sample_timeout_seconds !== 5 || !["unknown", "fresh", "stale", "blocked"].includes(String(value.freshness)) || !["never_observed", "target_changed", "stale", "restore_hold", "authority_unavailable", "observed"].includes(String(value.reason)) || !["reachable", "unknown"].includes(String(value.agent_reachability)) || ![value.mutation_hold, value.restore_ownership_held].every((flag) => typeof flag === "boolean") || !states.includes(String(value.container_state)) || !readiness.includes(String(value.readiness)) || !errors.includes(String(value.collection_error))) throw new Error("Runtime status is unavailable or malformed");
  for (const key of ["observed_at", "last_heartbeat_at"]) if (value[key] !== null && !Number.isFinite(statusTimestamp(value[key]))) throw new Error("Runtime status timestamp is malformed");
  if (value.exit_code !== undefined && (!Number.isInteger(value.exit_code) || Number(value.exit_code) < 0 || Number(value.exit_code) > 255 || value.container_state !== "exited")) throw new Error("Runtime exit evidence is malformed");
  const last = value.last_observation;
  if (last !== null && (!record(last) || !id(last.operation_id) || !id(last.container_id) || !Number.isFinite(statusTimestamp(last.observed_at)) || !observation(last.observation))) throw new Error("Historical runtime evidence is malformed");
}
export async function getHostingRuntimeStatus(resourceID: string, options?: Pick<RequestOptions, "signal">): Promise<HostingRuntimeStatus> {
  const value: unknown = await request(`/api/v1/resources/${encodeURIComponent(resourceID)}/runtime-status`, { ...options, cache: "no-store" });
  validateRuntimeStatus(value, resourceID); return value;
}
function elapsed(snapshot: RuntimeStatusSnapshot, monotonic: number, wall: number): number {
  const values = [monotonic, wall, snapshot.receivedMonotonic, snapshot.receivedWall, snapshot.requestDuration];
  if (!values.every(Number.isFinite) || snapshot.requestDuration < 0 || monotonic < snapshot.receivedMonotonic) return Infinity;
  // Monotonic elapsed handles wall-clock rollback. Wall elapsed conservatively
  // covers platforms whose monotonic clock pauses during system suspension.
  return Math.max(0, monotonic - snapshot.receivedMonotonic, wall - snapshot.receivedWall) + snapshot.requestDuration;
}
function age(snapshot: RuntimeStatusSnapshot, timestamp: string | null, monotonic: number, wall: number): number {
  const server = statusTimestamp(snapshot.status.server_time), observed = statusTimestamp(timestamp);
  // Round conservatively for timestamp parsing and millisecond clock resolution.
  return !Number.isFinite(server) || !Number.isFinite(observed) || observed > server ? Infinity : server - observed + elapsed(snapshot, monotonic, wall) + 2;
}
export function runtimeStatusView(snapshot: RuntimeStatusSnapshot, monotonic = performance.now(), wall = Date.now(), unavailable = false) {
  const status = snapshot.status, last = status.last_observation;
  const observationAge = age(snapshot, status.observed_at, monotonic, wall), heartbeatAge = age(snapshot, status.last_heartbeat_at, monotonic, wall);
  const matching = Boolean(status.current_operation_id && status.current_container_id && status.current_operation_id === status.observed_operation_id && status.current_container_id === status.observed_container_id && last && last.operation_id === status.current_operation_id && last.container_id === status.current_container_id && statusTimestamp(last.observed_at) === statusTimestamp(status.observed_at));
  const consistent = Boolean(last && status.container_state === last.observation.container_state && status.readiness === last.observation.readiness && status.collection_error === last.observation.collection_error && status.exit_code === last.observation.exit_code && (status.container_state !== "exited" || status.exit_code !== undefined));
  const fresh = !unavailable && status.freshness === "fresh" && status.reason === "observed" && matching && consistent && observationAge < 30_000;
  const reachable = !unavailable && status.agent_reachability === "reachable" && heartbeatAge < 30_000;
  return { fresh, reachable, observationAge, heartbeatAge, container: fresh ? status.container_state : "unknown", readiness: fresh ? status.readiness : "unknown", collectionError: fresh ? status.collection_error : "", exitCode: fresh ? status.exit_code : undefined };
}
export function runtimeStatusExpiry(snapshot: RuntimeStatusSnapshot, monotonic = performance.now(), wall = Date.now()): number | undefined {
  const view = runtimeStatusView(snapshot, monotonic, wall);
  const remaining = [view.fresh ? 30_000 - view.observationAge : Infinity, view.reachable ? 30_000 - view.heartbeatAge : Infinity];
  const next = Math.min(...remaining); return Number.isFinite(next) && next > 0 ? next : undefined;
}
