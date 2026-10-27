import { request } from "./client";
import { boundedStorageRequest } from "./hosting-storage-deployment";
import { archiveBackup, type ArchiveScope, type Backup } from "./hosting-storage-archives";

export type ComposeBackupVolumes = { resource_id: string; resource_generation: number; operation_id?: string; available: boolean; reason?: string; volumes: { volume: string; eligible: boolean; reason?: string; mounts: { component: string; mount_path: string; read_only: boolean }[] }[] };
export type ComposeBackupInput = { request_id: string; expected_resource_generation: number; volume: string; expected_deployed_operation_id: string; acknowledge_interruption: true };
const identifier = (v: unknown): v is string => typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
function exact(v: unknown, required: string[], optional: string[] = []): v is Record<string, unknown> { return !!v && typeof v === "object" && !Array.isArray(v) && required.every((k) => Object.hasOwn(v, k)) && Object.keys(v).every((k) => [...required, ...optional].includes(k)); }
export function validateComposeBackupVolumes(v: unknown, resource: string): asserts v is ComposeBackupVolumes {
  if (!exact(v, ["resource_id", "resource_generation", "available", "volumes"], ["operation_id", "reason"]) || v.resource_id !== resource || !Number.isSafeInteger(v.resource_generation) || Number(v.resource_generation) < 1 || typeof v.available !== "boolean" || v.reason !== undefined && typeof v.reason !== "string" || v.operation_id !== undefined && !identifier(v.operation_id) || v.available && !identifier(v.operation_id) || !Array.isArray(v.volumes) || v.volumes.length > 128) throw new Error("Compose backup scope mismatch");
  const names = new Set<string>();
  for (const row of v.volumes) {
    if (!exact(row, ["volume", "eligible", "mounts"], ["reason"]) || !identifier(row.volume) || names.has(row.volume) || typeof row.eligible !== "boolean" || row.reason !== undefined && typeof row.reason !== "string" || !Array.isArray(row.mounts) || row.mounts.length > 256 || row.mounts.some((m) => !exact(m, ["component", "mount_path", "read_only"]) || !identifier(m.component) || typeof m.mount_path !== "string" || !m.mount_path.startsWith("/") || typeof m.read_only !== "boolean")) throw new Error("Invalid Compose volume evidence");
    names.add(row.volume);
  }
}
export async function getComposeBackupVolumes(scope: ArchiveScope, signal?: AbortSignal) {
  const value: unknown = await boundedStorageRequest((signal) => request(`/api/v1/resources/${encodeURIComponent(scope.resource)}/storage-backup-volumes`, { signal, cache: "no-store" }), signal);
  validateComposeBackupVolumes(value, scope.resource); return value;
}
export async function requestComposeBackup(scope: ArchiveScope, body: ComposeBackupInput, signal?: AbortSignal) {
  if (!exact(body, ["request_id", "expected_resource_generation", "volume", "expected_deployed_operation_id", "acknowledge_interruption"]) || !/^[a-f0-9]{32}$/.test(body.request_id) || !Number.isSafeInteger(body.expected_resource_generation) || body.expected_resource_generation < 1 || !identifier(body.volume) || !identifier(body.expected_deployed_operation_id) || body.acknowledge_interruption !== true) throw new Error("Invalid Compose backup request");
  const raw = await boundedStorageRequest((signal) => request<Backup & { volume?: string; source_deployed_operation_id?: string }>(`/api/v1/resources/${encodeURIComponent(scope.resource)}/storage-backups`, { method: "POST", body, signal, cache: "no-store" }), signal);
  const backup = archiveBackup(raw, scope);
  if (raw.volume !== body.volume || raw.source_deployed_operation_id !== body.expected_deployed_operation_id) throw new Error("Compose backup receipt mismatch");
  return backup;
}
