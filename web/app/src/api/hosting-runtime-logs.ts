import type { components } from "./generated";
import { request } from "./client";

export type LogSession = components["schemas"]["HostingRuntimeLogSession"];
export type LogPage = components["schemas"]["HostingRuntimeLogPage"];
export type LogRow = components["schemas"]["HostingRuntimeLogItem"];
export type LogOpen = components["schemas"]["HostingRuntimeLogOpen"];
export type LogScope = { actor: string; project: string; resource: string };
export type LogLease = { remaining: number; monotonic: number; wall: number };
const encoder = new TextEncoder();
function fail(): never { throw new Error("Runtime log response is malformed or belongs to another scope"); }
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
function keys(value: unknown, required: string[], optional: string[] = []): asserts value is Record<string, unknown> {
  if (!object(value) || required.some((key) => !(key in value)) || Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key))) fail();
}
const id = (value: unknown) => typeof value === "string" && value.length > 0 && value.length <= 256 && /^[\x21-\x7e]+$/.test(value);
const random = (value: unknown) => typeof value === "string" && /^[0-9a-f]{32}$/.test(value);
const integer = (value: unknown): value is number => Number.isInteger(value) && Number(value) >= 0 && Number(value) <= 2147483647;
export function logTime(value: unknown): number {
  if (typeof value !== "string" || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(value)) return NaN;
  const year = Number(value.slice(0, 4)), month = Number(value.slice(5, 7)), day = Number(value.slice(8, 10));
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate() || Number(value.slice(11, 13)) > 23 || Number(value.slice(14, 16)) > 59 || Number(value.slice(17, 19)) > 59) return NaN;
  return Date.parse(value);
}
const targetKeys = ["resource_id", "binding_id", "destination_id", "server_id", "runner_id", "operation_id", "daemon_id", "container_id", "image_id", "release_hash", "plan_hash", "runtime_namespace"];
const sessionKeys = ["id", "request_id", "resource_id", "project_id", "actor_id", "target", "state", "created_at", "demand_expires_at", "ended_at", "purge_after", "server_time", "sensitive"];
export function validateLogSession(value: unknown, scope: LogScope, original?: LogSession, requestID?: string): asserts value is LogSession {
  keys(value, sessionKeys); keys(value.target, targetKeys); const target = value.target;
  if (!/^rls_[0-9a-f]{32}$/.test(String(value.id)) || !random(value.request_id) || value.actor_id !== scope.actor || value.project_id !== scope.project || value.resource_id !== scope.resource || value.target.resource_id !== scope.resource || value.sensitive !== true || !["waiting", "following", "disconnected", "ended", "target_changed", "authority_unavailable", "unsupported_driver"].includes(String(value.state))) fail();
  for (const key of targetKeys) if (!id(value.target[key])) fail();
  for (const key of ["image_id", "release_hash", "plan_hash"]) if (!/^sha256:[0-9a-f]{64}$/.test(String(value.target[key]))) fail();
  for (const key of ["created_at", "demand_expires_at", "purge_after", "server_time"]) if (!Number.isFinite(logTime(value[key]))) fail();
  if (value.ended_at !== null && !Number.isFinite(logTime(value.ended_at))) fail();
  if (logTime(value.demand_expires_at) - logTime(value.server_time) > 30_002 || logTime(value.purge_after) < logTime(value.created_at) || logTime(value.demand_expires_at) < logTime(value.created_at)) fail();
  if (requestID && value.request_id !== requestID) fail();
  if (original && (value.id !== original.id || value.request_id !== original.request_id || value.created_at !== original.created_at || targetKeys.some((key) => target[key] !== original.target[key as keyof LogSession["target"]]))) fail();
}
function validText(value: unknown): value is string {
  if (typeof value !== "string" || encoder.encode(value).length > 16384 || value.includes("\0")) return false;
  for (const point of value) { const code = point.codePointAt(0)!; if (code >= 0xd800 && code <= 0xdfff) return false; }
  return true;
}
export function validateLogPage(value: unknown, scope: LogScope, original: LogSession, after: number): asserts value is LogPage {
  keys(value, ["session", "items", "next_sequence", "earliest_available_sequence", "deleted_through_sequence", "gap", "truncated", "markers", "sensitive"]);
  validateLogSession(value.session, scope, original);
  if (!Array.isArray(value.items) || value.items.length > 200 || !integer(value.next_sequence) || !integer(value.earliest_available_sequence) || !integer(value.deleted_through_sequence) || typeof value.gap !== "boolean" || typeof value.truncated !== "boolean" || value.sensitive !== true || !Array.isArray(value.markers) || value.markers.some((marker) => marker !== "source_gap" && marker !== "prefix_evicted") || new Set(value.markers).size !== value.markers.length) fail();
  if (value.deleted_through_sequence > 0 && !value.markers.includes("prefix_evicted") || value.markers.includes("prefix_evicted") && value.deleted_through_sequence === 0 || value.gap !== (value.markers.includes("source_gap") || after < value.deleted_through_sequence) || value.earliest_available_sequence !== 0 && value.earliest_available_sequence !== value.deleted_through_sequence + 1 || value.truncated && value.items.length === 0) fail();
  let previous = after;
  for (const row of value.items) {
    keys(row, ["sequence", "component", "stream", "message", "received_at"], ["source_timestamp", "message_truncated"]);
    if (!integer(row.sequence) || row.sequence <= previous || row.sequence <= value.deleted_through_sequence || row.component !== "application" || !["stdout", "stderr", "combined"].includes(String(row.stream)) || !validText(row.message) || !Number.isFinite(logTime(row.received_at)) || row.source_timestamp !== undefined && !Number.isFinite(logTime(row.source_timestamp)) || row.message_truncated !== undefined && typeof row.message_truncated !== "boolean") fail();
    if (row.sequence !== previous + 1 && !(previous < value.deleted_through_sequence && row.sequence === value.deleted_through_sequence + 1)) fail();
    previous = row.sequence;
  }
  const expected = value.items.length ? previous : Math.max(after, value.deleted_through_sequence);
  if (value.next_sequence !== expected || value.items.length && value.earliest_available_sequence === 0 || encoder.encode(JSON.stringify(value)).length > 262144) fail();
}
export function newLogOpen(now = Date.now()): LogOpen {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Object.freeze({ request_id: Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join(""), request_expires_at: new Date(now + 25_000).toISOString() });
}
const sessionPath = (id: string) => `/api/v1/runtime-log-sessions/${encodeURIComponent(id)}`;
export async function openRuntimeLogs(scope: LogScope, body: LogOpen, signal: AbortSignal) {
  const value: unknown = await request(`/api/v1/resources/${encodeURIComponent(scope.resource)}/runtime-log-sessions`, { method: "POST", body, signal, cache: "no-store" }); validateLogSession(value, scope, undefined, body.request_id); return value;
}
export async function renewRuntimeLogs(scope: LogScope, session: LogSession, signal: AbortSignal) {
  const value: unknown = await request(sessionPath(session.id) + "/renew", { method: "POST", body: {}, signal, cache: "no-store" }); validateLogSession(value, scope, session); return value;
}
export async function closeRuntimeLogs(scope: LogScope, session: LogSession, signal: AbortSignal) {
  const value: unknown = await request(sessionPath(session.id), { method: "DELETE", signal, cache: "no-store" }); validateLogSession(value, scope, session); return value;
}
export async function readRuntimeLogs(scope: LogScope, session: LogSession, after: number, signal: AbortSignal) {
  const value: unknown = await request(sessionPath(session.id) + "/logs", { query: { after_sequence: after, limit: 200 }, signal, cache: "no-store" }); validateLogPage(value, scope, session, after); return value;
}
export function leaseRemaining(lease: LogLease, monotonic = performance.now(), wall = Date.now()) {
  if (![lease.remaining, lease.monotonic, lease.wall, monotonic, wall].every(Number.isFinite) || monotonic < lease.monotonic) return 0;
  return Math.max(0, lease.remaining - Math.max(0, monotonic - lease.monotonic, wall - lease.wall));
}
export function logLease(session: LogSession, duration: number, previous?: LogLease): LogLease {
  const remaining = Math.max(0, Math.min(30_000, logTime(session.demand_expires_at) - logTime(session.server_time) - Math.max(0, duration) - 2, previous ? leaseRemaining(previous) : Infinity));
  return { remaining, monotonic: performance.now(), wall: Date.now() };
}
export function appendLogRows(previous: LogRow[], next: LogRow[]) {
  const rows = [...previous, ...next]; let bytes = rows.reduce((sum, row) => sum + encoder.encode(JSON.stringify(row)).length, 0), dropped = 0;
  while (rows.length > 500 || bytes > 1048576) { bytes -= encoder.encode(JSON.stringify(rows.shift()!)).length; dropped++; }
  return { rows, dropped };
}
export const displayLogText = (message: string) => message.replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, (value) => `\\u${value.charCodeAt(0).toString(16).padStart(4, "0")}`);
