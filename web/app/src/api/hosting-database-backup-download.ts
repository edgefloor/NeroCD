import { request } from "./client";
import type { components } from "./generated";
import type { DatabaseBackup } from "./hosting-database-backups";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
} from "./hosting-databases";

export type DatabaseDownloadContext = DatabaseScope & {
  resource: string;
  binding: string;
  destination: string;
};
type Options = components["schemas"]["HostingDatabaseBackupDownloadOptions"];
const id = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const versions = [
  "postgresql.logical-local.v1",
  "mysql.logical-local.v1",
  "postgresql.compose-logical-local.v1",
  "mysql.compose-logical-local.v1",
];
const reasons = [
  "disabled",
  "backup_not_succeeded",
  "local_copy_unavailable",
  "copy_busy",
  "runner_unavailable",
  "placement_changed",
];
export function databaseBackupDownloadPath(backup: string) {
  if (!id(backup)) throw new Error("Invalid database backup identity");
  return `/api/v1/database-backups/${encodeURIComponent(backup)}/archive`;
}
export async function getDatabaseBackupDownloadOptions(
  context: DatabaseDownloadContext,
  backup: DatabaseBackup,
  signal?: AbortSignal,
): Promise<Options> {
  if (
    typeof context.actor !== "string" ||
    !context.actor ||
    ![
      context.project,
      context.environment,
      context.resource,
      context.binding,
      context.destination,
      backup.id,
    ].every(id) ||
    backup.project_id !== context.project ||
    backup.source_resource_id !== context.resource ||
    backup.mode !== "backup" ||
    backup.backup_id !== backup.id ||
    typeof backup.version !== "string" ||
    !versions.includes(backup.version)
  )
    throw new Error("Database download scope mismatch");
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(`${databaseBackupDownloadPath(backup.id)}-options`, {
        signal: s,
        cache: "no-store",
      }),
    signal,
  );
  if (!raw || typeof raw !== "object" || Array.isArray(raw))
    throw new Error("Invalid database download metadata");
  const value = raw as Record<string, unknown>;
  const blob = value.blob as Record<string, unknown> | undefined;
  if (
    !["backup_id", "available", "reason_code"].every((k) =>
      Object.hasOwn(value, k),
    ) ||
    Object.keys(value).some(
      (k) => !["backup_id", "available", "reason_code", "blob"].includes(k),
    ) ||
    value.backup_id !== backup.id ||
    typeof value.available !== "boolean" ||
    typeof value.reason_code !== "string" ||
    (value.available
      ? value.reason_code !== "" || !blob || backup.state !== "succeeded"
      : !reasons.includes(value.reason_code)) ||
    (blob !== undefined &&
      (!blob ||
        typeof blob !== "object" ||
        Array.isArray(blob) ||
        Object.keys(blob).sort().join() !== "bytes,digest,format" ||
        !backup.blob ||
        typeof blob.digest !== "string" ||
        !/^sha256:[a-f0-9]{64}$/.test(blob.digest) ||
        blob.digest !== backup.blob.digest ||
        !Number.isSafeInteger(blob.bytes) ||
        Number(blob.bytes) < 5 ||
        Number(blob.bytes) > 1073741824 ||
        blob.bytes !== backup.blob.bytes ||
        blob.format !== backup.blob.format ||
        blob.format !==
          (backup.version.startsWith("mysql.")
            ? "mysql_sql"
            : "postgresql_custom")))
  )
    throw new Error("Database download evidence mismatch");
  return value as Options;
}
