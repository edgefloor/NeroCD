import { request } from "./client";
import type { components } from "./generated";
import { boundedStorageRequest } from "./hosting-storage-deployment";
import type { ArchiveScope, Backup } from "./hosting-storage-archives";

type Options = components["schemas"]["HostingStorageBackupDownloadOptions"];
const id = (value: string) => /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const reasons = [
  "disabled",
  "runner_unavailable",
  "local_copy_unavailable",
  "backup_not_succeeded",
];
export function archiveDownloadPath(backup: string) {
  if (!id(backup)) throw new Error("Invalid archive backup identity");
  return `/api/v1/storage-backups/${encodeURIComponent(backup)}/archive`;
}
export async function getArchiveDownloadOptions(
  scope: ArchiveScope,
  backup: Backup,
  signal?: AbortSignal,
): Promise<Options> {
  if (
    typeof scope.actor !== "string" ||
    !scope.actor ||
    ![
      scope.project,
      scope.resource,
      scope.destination,
      scope.binding,
      backup.id,
    ].every(id) ||
    backup.project_id !== scope.project ||
    backup.resource_id !== scope.resource
  )
    throw new Error("Archive download scope mismatch");
  const value = await boundedStorageRequest(
    (signal) =>
      request<Options>(`${archiveDownloadPath(backup.id)}-options`, {
        signal,
        cache: "no-store",
      }),
    signal,
  );
  const blob = value?.blob;
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).some(
      (key) => !["available", "reason_code", "backup_id", "blob"].includes(key),
    ) ||
    typeof value.available !== "boolean" ||
    value.backup_id !== backup.id ||
    (value.available
      ? value.reason_code !== "" || !blob || backup.state !== "succeeded"
      : !reasons.includes(value.reason_code)) ||
    (blob !== undefined &&
      (!backup.blob ||
        Object.keys(blob).length !== 6 ||
        blob.backup_id !== backup.id ||
        blob.destination_id !== scope.destination ||
        blob.daemon_id !== backup.blob.daemon_id ||
        blob.format !== "nerocd-storage-v1" ||
        blob.format !== backup.blob.format ||
        blob.sha256 !== backup.blob.sha256 ||
        !/^sha256:[a-f0-9]{64}$/.test(blob.sha256) ||
        !Number.isSafeInteger(blob.size_bytes) ||
        blob.size_bytes < 1 ||
        blob.size_bytes > 1073741824 ||
        blob.size_bytes !== backup.blob.size_bytes))
  )
    throw new Error("Archive download evidence mismatch");
  return value;
}
