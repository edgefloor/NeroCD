import { request } from "./client";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
} from "./hosting-databases";
import type { DatabaseBackup } from "./hosting-database-backups";

export type RemoteBackupTarget = { target_id: string; revision: number };
export type DatabaseBackupTarget = {
  id: string;
  project_id: string;
  version: "postgresql.backup-target.v1";
  revision: number;
  name: string;
  endpoint: string;
  bucket: string;
  region: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};
export type DatabaseBackupTargetInput = {
  request_id: string;
  expected_revision: number;
  name: string;
  endpoint: string;
  bucket: string;
  region: string;
  enabled: boolean;
  credentials?: { access_key: string; secret_key: string };
};
export type DatabaseBackupTransfer = {
  id: string;
  version: "postgresql.logical-s3-transfer.v1" | "mysql.logical-s3-transfer.v1";
  project_id: string;
  target_id: string;
  target_revision: number;
  destination_id: string;
  mode: "validate" | "upload" | "download";
  backup_id?: string;
  restore_operation_id?: string;
  state:
    "queued" | "dispatched" | "applying" | "succeeded" | "failed" | "uncertain";
  failure_code:
    | ""
    | "access_denied"
    | "network_denied"
    | "remote_unavailable"
    | "object_mismatch"
    | "archive_unavailable"
    | "interrupted"
    | "authority_denied";
  blob?: DatabaseBackup["blob"];
  created_at: string;
  updated_at: string;
};
const identifier = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const positive = (v: unknown): v is number =>
  Number.isSafeInteger(v) && Number(v) > 0;
const text = (v: unknown, max = 256): v is string =>
  typeof v === "string" &&
  v.length > 0 &&
  v.length <= max &&
  v.trim() === v &&
  !/[\x00-\x1f\x7f]/.test(v);
const date = (v: unknown): v is string =>
  typeof v === "string" && v.length <= 64 && Number.isFinite(Date.parse(v));
const fail = (): never => {
  throw new Error("Invalid remote backup metadata");
};
function document(raw: unknown, required: string[], optional: string[] = []) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return fail();
  const v = raw as Record<string, unknown>;
  if (
    required.some((k) => !Object.hasOwn(v, k)) ||
    Object.keys(v).some((k) => !required.includes(k) && !optional.includes(k))
  )
    return fail();
  return v;
}
export function validBackupEndpoint(value: string) {
  if (!text(value, 2048)) return false;
  try {
    const u = new URL(value);
    return (
      ["https:", "http:"].includes(u.protocol) &&
      Boolean(u.hostname) &&
      !u.username &&
      !u.password &&
      !u.search &&
      !u.hash &&
      ["", "/"].includes(u.pathname)
    );
  } catch {
    return false;
  }
}
export function parseRemoteBackupTarget(raw: unknown): RemoteBackupTarget {
  const v = document(raw, ["target_id", "revision"]);
  if (!identifier(v.target_id) || !positive(v.revision)) return fail();
  return { target_id: v.target_id, revision: v.revision };
}
export function parseDatabaseBackupTarget(
  raw: unknown,
  scope: DatabaseScope,
): DatabaseBackupTarget {
  const v = document(raw, [
    "id",
    "project_id",
    "version",
    "revision",
    "name",
    "endpoint",
    "bucket",
    "region",
    "enabled",
    "created_at",
    "updated_at",
  ]);
  if (
    !identifier(v.id) ||
    v.project_id !== scope.project ||
    v.version !== "postgresql.backup-target.v1" ||
    !positive(v.revision) ||
    !text(v.name, 128) ||
    typeof v.endpoint !== "string" ||
    !validBackupEndpoint(v.endpoint) ||
    !text(v.bucket) ||
    !text(v.region, 128) ||
    typeof v.enabled !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at)
  )
    return fail();
  return {
    id: v.id,
    project_id: scope.project,
    version: "postgresql.backup-target.v1",
    revision: v.revision,
    name: v.name,
    endpoint: v.endpoint,
    bucket: v.bucket,
    region: v.region,
    enabled: v.enabled,
    created_at: v.created_at,
    updated_at: v.updated_at,
  };
}
export function parseDatabaseBackupTransfer(
  raw: unknown,
  scope: DatabaseScope,
): DatabaseBackupTransfer {
  const v = document(
    raw,
    [
      "id",
      "version",
      "project_id",
      "target_id",
      "target_revision",
      "destination_id",
      "mode",
      "state",
      "failure_code",
      "created_at",
      "updated_at",
    ],
    ["backup_id", "restore_operation_id", "blob"],
  );
  if (
    !identifier(v.id) ||
    !["postgresql.logical-s3-transfer.v1", "mysql.logical-s3-transfer.v1"].includes(String(v.version)) ||
    v.project_id !== scope.project ||
    !identifier(v.target_id) ||
    !positive(v.target_revision) ||
    !identifier(v.destination_id) ||
    !["validate", "upload", "download"].includes(String(v.mode)) ||
    ![
      "queued",
      "dispatched",
      "applying",
      "succeeded",
      "failed",
      "uncertain",
    ].includes(String(v.state)) ||
    ![
      "",
      "access_denied",
      "network_denied",
      "remote_unavailable",
      "object_mismatch",
      "archive_unavailable",
      "interrupted",
      "authority_denied",
    ].includes(String(v.failure_code)) ||
    !date(v.created_at) ||
    !date(v.updated_at)
  )
    return fail();
  if (
    v.mode === "validate"
      ? v.backup_id !== undefined ||
        v.restore_operation_id !== undefined ||
        v.blob !== undefined
      : !identifier(v.backup_id) ||
        (v.mode === "download"
          ? !identifier(v.restore_operation_id)
          : v.restore_operation_id !== undefined)
  )
    return fail();
  if (v.version === "mysql.logical-s3-transfer.v1" && v.mode !== "upload") return fail();
  if (v.blob !== undefined) {
    const b = document(v.blob, ["digest", "bytes", "format"]);
    if (
      typeof b.digest !== "string" ||
      !/^sha256:[a-f0-9]{64}$/.test(b.digest) ||
      !Number.isSafeInteger(b.bytes) ||
      Number(b.bytes) < 5 ||
      Number(b.bytes) > 1073741824 ||
      b.format !== (v.version === "mysql.logical-s3-transfer.v1" ? "mysql_sql" : "postgresql_custom")
    )
      return fail();
  }
  if (v.mode !== "validate" && !v.blob) return fail();
  return {
    id: v.id,
    version: v.version as DatabaseBackupTransfer["version"],
    project_id: scope.project,
    target_id: v.target_id,
    target_revision: v.target_revision,
    destination_id: v.destination_id,
    mode: v.mode as DatabaseBackupTransfer["mode"],
    state: v.state as DatabaseBackupTransfer["state"],
    failure_code: v.failure_code as DatabaseBackupTransfer["failure_code"],
    created_at: v.created_at,
    updated_at: v.updated_at,
    ...(v.backup_id !== undefined ? { backup_id: String(v.backup_id) } : {}),
    ...(v.restore_operation_id !== undefined
      ? { restore_operation_id: String(v.restore_operation_id) }
      : {}),
    ...(v.blob !== undefined
      ? { blob: v.blob as DatabaseBackupTransfer["blob"] }
      : {}),
  };
}
function list<T extends { id: string }>(
  raw: unknown,
  parse: (v: unknown) => T,
): T[] {
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map(parse);
  if (new Set(out.map((v) => v.id)).size !== out.length) return fail();
  return out;
}
const path = (scope: DatabaseScope) =>
  `/api/v1/projects/${encodeURIComponent(scope.project)}/database-backup-targets`;
export const databaseBackupTargetsKey = (scope: DatabaseScope) =>
  ["database-backup-targets", scope.actor, scope.project] as const;
export const databaseBackupTransfersKey = (
  scope: DatabaseScope,
  backup: string,
) => ["database-backup-transfers", scope.actor, scope.project, backup] as const;
export const databaseBackupValidationsKey = (
  scope: DatabaseScope,
  target: string,
) => [...databaseBackupTargetsKey(scope), target, "validations"] as const;
export async function listDatabaseBackupTargets(
  scope: DatabaseScope,
  signal?: AbortSignal,
) {
  const raw = await boundedDatabaseRequest(
    (s) => request<unknown>(path(scope), { signal: s, cache: "no-store" }),
    signal,
  );
  return list(raw, (v) => parseDatabaseBackupTarget(v, scope));
}
export class DatabaseBackupTargetInputError extends Error {
  constructor() {
    super("Invalid backup destination input");
    this.name = "DatabaseBackupTargetInputError";
  }
}
export function validDatabaseBackupTargetCredentials(
  credentials: DatabaseBackupTargetInput["credentials"],
  required: boolean,
) {
  return credentials === undefined
    ? !required
    : text(credentials.access_key, 4096) && text(credentials.secret_key, 4096);
}
export async function saveDatabaseBackupTarget(
  scope: DatabaseScope,
  target: string,
  input: DatabaseBackupTargetInput,
  signal: AbortSignal,
) {
  if (
    !identifier(input.request_id) ||
    (target
      ? !identifier(target) || !positive(input.expected_revision)
      : input.expected_revision !== 0 || !input.credentials) ||
    !text(input.name, 128) ||
    !validBackupEndpoint(input.endpoint) ||
    !text(input.bucket) ||
    !text(input.region, 128) ||
    typeof input.enabled !== "boolean" ||
    !validDatabaseBackupTargetCredentials(input.credentials, !target)
  )
    throw new DatabaseBackupTargetInputError();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        path(scope) + (target ? `/${encodeURIComponent(target)}` : ""),
        {
          method: target ? "PUT" : "POST",
          body: input,
          requestID: input.request_id,
          signal: s,
        },
      ),
    signal,
  );
  const out = parseDatabaseBackupTarget(raw, scope);
  if (
    (target && out.id !== target) ||
    out.revision !== input.expected_revision + 1 ||
    ["name", "endpoint", "bucket", "region", "enabled"].some(
      (k) =>
        out[k as keyof DatabaseBackupTarget] !==
        input[k as keyof DatabaseBackupTargetInput],
    )
  )
    return fail();
  return out;
}
export async function listDatabaseBackupValidations(
  scope: DatabaseScope,
  target: string,
  signal?: AbortSignal,
) {
  if (!identifier(target)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `${path(scope)}/${encodeURIComponent(target)}/validations`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  const out = list(raw, (v) => parseDatabaseBackupTransfer(v, scope));
  if (out.some((v) => v.target_id !== target || v.mode !== "validate"))
    return fail();
  return out;
}
export async function validateDatabaseBackupTarget(
  scope: DatabaseScope,
  target: string,
  input: {
    request_id: string;
    expected_revision: number;
    destination_id: string;
  },
  signal: AbortSignal,
) {
  if (
    !identifier(target) ||
    !identifier(input.request_id) ||
    !positive(input.expected_revision) ||
    !identifier(input.destination_id)
  )
    return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `${path(scope)}/${encodeURIComponent(target)}/validations`,
        { method: "POST", body: input, requestID: input.request_id, signal: s },
      ),
    signal,
  );
  const out = parseDatabaseBackupTransfer(raw, scope);
  if (
    out.target_id !== target ||
    out.target_revision !== input.expected_revision ||
    out.destination_id !== input.destination_id ||
    out.mode !== "validate"
  )
    return fail();
  return out;
}
export async function listDatabaseBackupTransfers(
  scope: DatabaseScope,
  backup: string,
  signal?: AbortSignal,
) {
  if (!identifier(backup)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-backups/${encodeURIComponent(backup)}/transfers`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  const out = list(raw, (v) => parseDatabaseBackupTransfer(v, scope));
  if (out.some((v) => v.backup_id !== backup || v.mode === "validate"))
    return fail();
  return out;
}
export async function retryDatabaseBackupUpload(
  scope: DatabaseScope,
  backup: string,
  input: { request_id: string; expected_transfer_id: string },
  signal: AbortSignal,
) {
  if (
    !identifier(backup) ||
    !identifier(input.request_id) ||
    !identifier(input.expected_transfer_id)
  )
    return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-backups/${encodeURIComponent(backup)}/transfers/retry`,
        { method: "POST", body: input, requestID: input.request_id, signal: s },
      ),
    signal,
  );
  const out = parseDatabaseBackupTransfer(raw, scope);
  if (out.backup_id !== backup || out.mode !== "upload") return fail();
  return out;
}
