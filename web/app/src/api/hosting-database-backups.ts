import type { RemoteBackupTarget } from "./hosting-database-backup-targets";
import { parseRemoteBackupTarget } from "./hosting-database-backup-targets";
import { request } from "./client";
import type { components } from "./generated";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
  type HostingDatabase,
} from "./hosting-databases";
export type DatabaseBackup =
  components["schemas"]["HostingDatabaseBackupOperation"] & {
    remote_target?: RemoteBackupTarget;
  };
export type DatabaseBackupInput =
  components["schemas"]["HostingDatabaseBackupInput"] & {
    remote_target?: RemoteBackupTarget;
  };
export type DatabaseRestoreInput =
  components["schemas"]["HostingDatabaseBackupRestoreInput"] & {
    remote_source?: { transfer_id: string; expected_target_revision: number };
  };
const id = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const fail = (): never => {
  throw new Error("Invalid database backup metadata");
};
const required = [
  "max_bytes",
  "timeout_seconds",
  "id",
  "version",
  "project_id",
  "source_resource_id",
  "source_generation",
  "backup_id",
  "mode",
  "state",
  "consistency",
  "ownership_held",
  "created_at",
  "updated_at",
];
const allowed = new Set([
  ...required,
  "target_resource_id",
  "blob",
  "failure_code",
  "remote_target",
  "source_component",
  "source_deployed_operation_id",
  "target_component",
  "target_deployed_operation_id",
]);
export function parseDatabaseBackup(
  raw: unknown,
  scope: DatabaseScope,
  database: Pick<HostingDatabase, "resource"> & Partial<Pick<HostingDatabase, "engine">>,
): DatabaseBackup {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return fail();
  const v = raw as Record<string, unknown>;

  if (
    required.some((k) => !Object.hasOwn(v, k)) ||
    Object.keys(v).some((k) => !allowed.has(k))
  )
    return fail();
  if (
    !Number.isSafeInteger(v.max_bytes) ||
    Number(v.max_bytes) < 5 ||
    Number(v.max_bytes) > 1073741824 ||
    !Number.isSafeInteger(v.timeout_seconds) ||
    Number(v.timeout_seconds) < 1 ||
    Number(v.timeout_seconds) > 3600 ||
    !id(v.id) ||
    !["postgresql.logical-local.v1", "postgresql.compose-logical-local.v1", "mysql.logical-local.v1", "mysql.compose-logical-local.v1"].includes(String(v.version)) ||
    v.project_id !== scope.project ||
    v.source_resource_id !== database.resource.id ||
    !Number.isSafeInteger(v.source_generation) ||
    Number(v.source_generation) < 1 ||
    !id(v.backup_id) ||
    !["backup", "restore"].includes(String(v.mode)) ||
    ![
      "queued",
      "waiting_target",
      "dispatched",
      "applying",
      "succeeded",
      "failed",
      "uncertain",
    ].includes(String(v.state)) ||
    v.consistency !== "logical_snapshot_source_writable" ||
    typeof v.ownership_held !== "boolean" ||
    ![v.created_at, v.updated_at].every(
      (t) => typeof t === "string" && Number.isFinite(Date.parse(t)),
    )
  )
    return fail();
  if (
    v.mode === "backup"
      ? v.backup_id !== v.id || v.target_resource_id !== undefined
      : v.backup_id === v.id ||
        !id(v.target_resource_id) ||
        v.target_resource_id === v.source_resource_id
  )
    return fail();
  if (database.engine && ((v.version === "mysql.logical-local.v1") !== (database.engine === "mysql") || database.engine === "redis")) return fail();
  const componentVersion = v.version === "postgresql.compose-logical-local.v1" || v.version === "mysql.compose-logical-local.v1";
  const mysqlVersion = v.version === "mysql.logical-local.v1" || v.version === "mysql.compose-logical-local.v1";
  if (componentVersion && database.engine) return fail();
  if (v.version === "mysql.compose-logical-local.v1" && v.remote_target !== undefined) return fail();
  const component = (value: unknown): value is string => typeof value === "string" && /^[a-z][a-z0-9_-]{0,62}$/.test(value);
  if (componentVersion ? !component(v.source_component) || !id(v.source_deployed_operation_id) || (v.mode === "restore" ? !component(v.target_component) || !id(v.target_deployed_operation_id) : v.target_component !== undefined || v.target_deployed_operation_id !== undefined) : [v.source_component, v.source_deployed_operation_id, v.target_component, v.target_deployed_operation_id].some(value => value !== undefined)) return fail();
  if (v.blob !== undefined) {
    const b = v.blob as Record<string, unknown>;
    if (
      !b ||
      typeof b !== "object" ||
      Array.isArray(b) ||
      Object.keys(b).sort().join() !== "bytes,digest,format" ||
      typeof b.digest !== "string" ||
      !/^sha256:[a-f0-9]{64}$/.test(b.digest) ||
      !Number.isSafeInteger(b.bytes) ||
      Number(b.bytes) < 5 ||
      Number(b.bytes) > 1073741824 ||
      b.format !== (mysqlVersion ? "mysql_sql" : "postgresql_custom")
    )
      return fail();
  }
  if (
    v.failure_code !== undefined &&
    !["execution_failed", "publication_failed", "interrupted"].includes(
      String(v.failure_code),
    )
  )
    return fail();
  if (
    (v.state === "succeeded" && (!v.blob || v.ownership_held)) ||
    (v.state === "uncertain" && !v.ownership_held)
  )
    return fail();
  // Reconstruct the closed safe projection; never cache arbitrary response keys.
  return {
    max_bytes: Number(v.max_bytes),
    timeout_seconds: Number(v.timeout_seconds),
    id: v.id,
    version: v.version as DatabaseBackup["version"],
    ...(componentVersion ? { source_component: String(v.source_component), source_deployed_operation_id: String(v.source_deployed_operation_id), ...(v.mode === "restore" ? {target_component: String(v.target_component), target_deployed_operation_id: String(v.target_deployed_operation_id)} : {}) } : {}),
    project_id: scope.project,
    source_resource_id: database.resource.id,
    source_generation: Number(v.source_generation),
    backup_id: v.backup_id,
    mode: v.mode as DatabaseBackup["mode"],
    state: v.state as DatabaseBackup["state"],
    consistency: "logical_snapshot_source_writable",
    ownership_held: v.ownership_held,
    created_at: String(v.created_at),
    updated_at: String(v.updated_at),
    ...(v.remote_target !== undefined
      ? { remote_target: parseRemoteBackupTarget(v.remote_target) }
      : {}),
    ...(v.target_resource_id
      ? { target_resource_id: String(v.target_resource_id) }
      : {}),
    ...(v.blob ? { blob: v.blob as DatabaseBackup["blob"] } : {}),
    ...(v.failure_code
      ? { failure_code: v.failure_code as DatabaseBackup["failure_code"] }
      : {}),
  };
}
export const databaseBackupsKey = (scope: DatabaseScope, id: string) =>
  [
    "database-backups",
    scope.actor,
    scope.project,
    scope.environment,
    id,
  ] as const;
export async function listDatabaseBackups(
  scope: DatabaseScope,
  database: Pick<HostingDatabase, "resource"> & Partial<Pick<HostingDatabase, "engine">>,
  signal?: AbortSignal,
) {
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(database.resource.id)}/database-backups`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map((v) => parseDatabaseBackup(v, scope, database));
  if (new Set(out.map((v) => v.id)).size !== out.length) return fail();
  return out;
}
export async function requestDatabaseBackup(
  scope: DatabaseScope,
  database: Pick<HostingDatabase, "resource"> & Partial<Pick<HostingDatabase, "engine">>,
  input: DatabaseBackupInput,
  signal: AbortSignal,
) {
  if (input.remote_target !== undefined)
    parseRemoteBackupTarget(input.remote_target);
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(database.resource.id)}/database-backups`,
        { method: "POST", body: input, signal: s, requestID: input.request_id },
      ),
    signal,
  );
  const out = parseDatabaseBackup(raw, scope, database);
  if (
    out.mode !== "backup" ||
    out.source_component !== input.component || out.source_deployed_operation_id !== input.expected_deployed_operation_id ||
    out.remote_target?.target_id !== input.remote_target?.target_id ||
    out.remote_target?.revision !== input.remote_target?.revision
  )
    return fail();
  return out;
}
export async function restoreDatabaseBackup(
  scope: DatabaseScope,
  database: Pick<HostingDatabase, "resource"> & Partial<Pick<HostingDatabase, "engine">>,
  backup: string,
  input: DatabaseRestoreInput,
  signal: AbortSignal,
) {
  if (
    input.remote_source !== undefined &&
    (!id(input.remote_source.transfer_id) ||
      !Number.isSafeInteger(input.remote_source.expected_target_revision) ||
      input.remote_source.expected_target_revision < 1 ||
      Object.keys(input.remote_source).sort().join() !==
        "expected_target_revision,transfer_id")
  )
    return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-backups/${encodeURIComponent(backup)}/restores`,
        { method: "POST", body: input, signal: s, requestID: input.request_id },
      ),
    signal,
  );
  const out = parseDatabaseBackup(raw, scope, database);
  if (out.mode !== "restore" || out.backup_id !== backup || (input.component_target && (out.target_resource_id !== input.component_target.resource_id || out.target_component !== input.component_target.component || out.target_deployed_operation_id !== input.component_target.expected_deployed_operation_id))) return fail();
  return out;
}

export type DatabaseBackupComponents = components["schemas"]["HostingDatabaseBackupComponents"];
export async function getDatabaseBackupComponents(scope: DatabaseScope, resource: string, signal?: AbortSignal): Promise<DatabaseBackupComponents> {
 const raw = await boundedDatabaseRequest(s => request<unknown>(`/api/v1/resources/${encodeURIComponent(resource)}/database-backup-components`, {signal:s,cache:"no-store"}), signal);
 if (!raw || typeof raw !== "object" || Array.isArray(raw)) return fail();
 const v = raw as Record<string,unknown>;
 if (Object.keys(v).some(k => !["resource_id","resource_generation","operation_id","available","reason","components"].includes(k)) || v.resource_id !== resource || !Number.isSafeInteger(v.resource_generation) || Number(v.resource_generation)<1 || typeof v.available !== "boolean" || typeof v.reason !== "string" || (v.operation_id !== undefined && !id(v.operation_id)) || !Array.isArray(v.components) || v.components.length>16) return fail();
 const seen = new Set<string>();
 for (const item of v.components) {
  if (!item || typeof item !== "object" || Array.isArray(item) || Object.keys(item).sort().join() !== "component,eligible,engine,major_version,reason,running" || typeof item.component !== "string" || !/^[a-z][a-z0-9_-]{0,62}$/.test(item.component) || seen.has(item.component) || !((item.engine === "postgresql" && item.major_version === 16) || (item.engine === "mysql" && item.major_version === 8)) || typeof item.running !== "boolean" || typeof item.eligible !== "boolean" || typeof item.reason !== "string") return fail();
  seen.add(item.component);
 }
 if (v.available && (!id(v.operation_id) || v.components.length===0)) return fail();
 void scope;
 return raw as DatabaseBackupComponents;
}
