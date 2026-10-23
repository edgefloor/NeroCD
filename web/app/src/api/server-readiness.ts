import type { components } from "./generated";
import { request, type RequestOptions } from "./client";
import { statusTimestamp } from "./hosting-runtime-status";
export type ServerReadinessStatus = components["schemas"]["ServerReadiness"];
export type ReadinessSnapshot = { status: ServerReadinessStatus; monotonic: number; wall: number };
export const localReadLifetime = 5000;
const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const member = (value: unknown, values: readonly string[]) => typeof value === "string" && values.includes(value);
const sample = (value: unknown) => typeof value === "string" && /^srd_[a-f0-9]{32}$/.test(value);
export function validateServerReadiness(value: unknown, serverID: string): asserts value is ServerReadinessStatus {
  if (!record(value) || value.server_id !== serverID || value.profile !== "basic_image_hosting.v1" || !member(value.status, ["unknown", "blocked", "ready"]) || !member(value.reason, ["no_sample", "stale", "authority_unavailable", "restore_hold", "daemon_mismatch", "prerequisite_failed", "qualified"])) throw new Error("Invalid server readiness response");
  const expected = value.reason === "qualified" ? "ready" : ["restore_hold", "daemon_mismatch", "prerequisite_failed"].includes(String(value.reason)) ? "blocked" : "unknown";
  if (value.status !== expected) throw new Error("Inconsistent server readiness diagnosis");
  const hasSample = value.sample_id !== undefined || value.observed_at !== undefined || value.report !== undefined;
  if (!hasSample) {
    if (["qualified", "prerequisite_failed", "daemon_mismatch", "stale"].includes(String(value.reason))) throw new Error("Missing readiness evidence");
    return;
  }
  const report = value.report;
  if (!sample(value.sample_id) || !Number.isFinite(statusTimestamp(value.observed_at)) || !record(report) || report.sample_id !== value.sample_id || report.profile !== value.profile) throw new Error("Invalid readiness sample");
  const enums: Record<string, string[]> = { os: ["linux", "unsupported", "unknown"], architecture: ["amd64", "arm64", "unsupported", "unknown"], execution: ["enabled", "disabled"], configuration: ["matched", "mismatch"], docker: ["available", "unreachable", "unsupported_api", "invalid_response"], daemon_os: ["linux", "unsupported", "unknown"], daemon_architecture: ["amd64", "arm64", "unsupported", "unknown"], cli: ["available", "missing", "unsafe", "unknown"], work_root: ["accessible", "missing", "unsafe", "inaccessible", "unknown"], journal_root: ["accessible", "missing", "unsafe", "inaccessible", "unknown"], error: ["", "timeout", "daemon_changed", "local_check_failed"] };
  if (Object.entries(enums).some(([key, values]) => !member(report[key], values)) || !Number.isInteger(report.duration_ms) || Number(report.duration_ms) < 0 || Number(report.duration_ms) > 5000 || [report.work_free_bytes, report.journal_free_bytes].some((n) => !Number.isInteger(n) || Number(n) < 0 || Number(n) > 9223372036854775807) || typeof report.docker_api_version !== "string" || !/^(?:\d{1,3}\.\d{1,3})?$/.test(report.docker_api_version) || typeof report.daemon_id !== "string" || !/^[\x20-\x7e]{0,128}$/.test(report.daemon_id)) throw new Error("Invalid readiness report");
}
export async function getServerReadiness(serverID: string, options?: Pick<RequestOptions, "signal">): Promise<ServerReadinessStatus> {
  const value: unknown = await request(`/api/v1/servers/${encodeURIComponent(serverID)}/readiness`, { ...options, cache: "no-store" });
  validateServerReadiness(value, serverID); return value;
}
// This bounds the local display only. The DTO has no server clock or remaining TTL.
export function currentRead(snapshot: ReadinessSnapshot, monotonic = performance.now(), wall = Date.now()): boolean {
  return [snapshot.monotonic, snapshot.wall, monotonic, wall].every(Number.isFinite) && monotonic >= snapshot.monotonic && Math.max(monotonic - snapshot.monotonic, wall - snapshot.wall) < localReadLifetime;
}
