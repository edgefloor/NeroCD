import { request } from "./client";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
} from "./hosting-databases";
import type { components } from "./generated";
export type StorageBackupTransfer =
  components["schemas"]["HostingStorageBackupTransfer"];
const id = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
function parse(
  v: StorageBackupTransfer,
  scope: DatabaseScope,
  backup?: string,
): StorageBackupTransfer {
  if (
    !v ||
    !id(v.id) ||
    v.project_id !== scope.project ||
    !id(v.target_id) ||
    !Number.isSafeInteger(v.target_revision) ||
    v.target_revision < 1 ||
    !id(v.destination_id) ||
    v.version !== "owned-upload.s3-transfer.v1" ||
    !["validate", "upload", "download"].includes(v.mode) ||
    ![
      "queued",
      "dispatched",
      "applying",
      "succeeded",
      "failed",
      "uncertain",
    ].includes(v.state) ||
    ![
      "",
      "access_denied",
      "network_denied",
      "remote_unavailable",
      "object_mismatch",
      "archive_unavailable",
      "interrupted",
      "authority_denied",
    ].includes(v.failure_code) ||
    !Number.isFinite(Date.parse(v.created_at)) ||
    !Number.isFinite(Date.parse(v.updated_at))
  )
    throw Error("Invalid storage transfer metadata");
  if (backup && v.backup_id !== backup)
    throw Error("Storage transfer backup mismatch");
  if (
    v.mode !== "validate" &&
    (!v.blob ||
      v.blob.backup_id !== v.backup_id ||
      v.blob.destination_id !== v.destination_id ||
      v.blob.format !== "nerocd-storage-v1" ||
      !/^sha256:[a-f0-9]{64}$/.test(v.blob.sha256) ||
      !Number.isSafeInteger(v.blob.size_bytes) ||
      v.blob.size_bytes < 1 ||
      v.blob.size_bytes > 1073741824)
  )
    throw Error("Invalid storage transfer blob");
  return v;
}
async function read(
  scope: DatabaseScope,
  path: string,
  signal?: AbortSignal,
  backup?: string,
) {
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<StorageBackupTransfer[]>(path, { signal: s, cache: "no-store" }),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100)
    throw Error("Invalid storage transfer list");
  return raw.map((v) => parse(v, scope, backup));
}
export function listStorageValidations(
  scope: DatabaseScope,
  target: string,
  signal?: AbortSignal,
) {
  return read(
    scope,
    `/api/v1/projects/${encodeURIComponent(scope.project)}/database-backup-targets/${encodeURIComponent(target)}/storage-validations`,
    signal,
  ).then((rows) => {
    if (rows.some((v) => v.mode !== "validate" || v.target_id !== target))
      throw Error("Storage validation mismatch");
    return rows;
  });
}
export async function validateStorageTarget(
  scope: DatabaseScope,
  target: string,
  input: {
    request_id: string;
    expected_revision: number;
    destination_id: string;
  },
  signal: AbortSignal,
) {
  const out = parse(
    await boundedDatabaseRequest(
      (s) =>
        request<StorageBackupTransfer>(
          `/api/v1/projects/${encodeURIComponent(scope.project)}/database-backup-targets/${encodeURIComponent(target)}/storage-validations`,
          {
            method: "POST",
            body: input,
            requestID: input.request_id,
            signal: s,
          },
        ),
      signal,
    ),
    scope,
  );
  if (
    out.target_id !== target ||
    out.target_revision !== input.expected_revision ||
    out.destination_id !== input.destination_id ||
    out.mode !== "validate"
  )
    throw Error("Storage validation mismatch");
  return out;
}
export function listStorageTransfers(
  scope: DatabaseScope,
  backup: string,
  signal?: AbortSignal,
) {
  return read(
    scope,
    `/api/v1/storage-backups/${encodeURIComponent(backup)}/transfers`,
    signal,
    backup,
  );
}
export async function retryStorageTransfer(
  scope: DatabaseScope,
  backup: string,
  input: { request_id: string; expected_transfer_id: string },
  signal?: AbortSignal,
) {
  return parse(
    await boundedDatabaseRequest(
      (s) =>
        request<StorageBackupTransfer>(
          `/api/v1/storage-backups/${encodeURIComponent(backup)}/transfers/retry`,
          {
            method: "POST",
            body: input,
            requestID: input.request_id,
            signal: s,
          },
        ),
      signal,
    ),
    scope,
    backup,
  );
}
