import { request } from "./client";
import { boundedStorageRequest } from "./hosting-storage-deployment";
import type { ArchiveScope } from "./hosting-storage-archives";
import type { Resource } from "./hosting";
import {
  parseRetentionPreview,
  parseRetentionExecution,
  retentionVersion,
  type RetentionPreview,
  type RetentionExecution,
} from "./hosting-storage-backup-retention";
import type { components } from "./generated";
export const deletionVersion = "owned-upload.backup-deletion.v1" as const;
export type DeletionPreviewInput =
  components["schemas"]["HostingStorageBackupDeletionPreviewInput"];
export type DeletionInput =
  components["schemas"]["HostingStorageBackupDeletionInput"];
type Selection = {
  origin: "manual_selection";
  backup_id: string;
  resource_generation: number;
  final_copy?: boolean;
  version: typeof deletionVersion;
  policy_revision: 0;
};
export type DeletionPreview = Omit<
  RetentionPreview,
  "version" | "policy_revision"
> &
  Selection;
export type DeletionExecution = Omit<
  RetentionExecution,
  "version" | "policy_revision"
> &
  Selection;
function selection(
  raw: unknown,
  backup: string,
): [Record<string, unknown>, Selection] {
  if (!raw || typeof raw !== "object" || Array.isArray(raw))
    throw new Error("Invalid deletion evidence");
  const v = raw as Record<string, unknown>;
  if (
    v.origin !== "manual_selection" ||
    v.backup_id !== backup ||
    v.version !== deletionVersion ||
    v.policy_revision !== 0 ||
    !Number.isSafeInteger(v.resource_generation) ||
    Number(v.resource_generation) < 1 ||
    (v.final_copy !== undefined && typeof v.final_copy !== "boolean")
  )
    throw new Error("Invalid deletion scope");
  const { origin, backup_id, resource_generation, final_copy, ...rest } = v;
  return [
    { ...rest, version: retentionVersion, policy_revision: 1 },
    {
      origin,
      backup_id,
      resource_generation,
      final_copy,
      version: deletionVersion,
      policy_revision: 0,
    } as Selection,
  ];
}
export function parseDeletionPreview(
  raw: unknown,
  scope: ArchiveScope,
  resource: Resource,
  backup: string,
): DeletionPreview {
  const [rest, selected] = selection(raw, backup);
  const v = parseRetentionPreview(rest, scope, resource);
  if (
    !v.items.length ||
    v.items.some(
      (c) =>
        c.backup_id !== backup ||
        !c.selected ||
        c.availability !== "available" ||
        c.protected_reasons.length,
    )
  )
    throw new Error("Invalid manual selection");
  return { ...v, ...selected };
}
export function parseDeletionExecution(
  raw: unknown,
  scope: ArchiveScope,
  resource: Resource,
  backup: string,
): DeletionExecution {
  const [rest, selected] = selection(raw, backup);
  const v = parseRetentionExecution(rest, scope, resource);
  if (!v.items.length || v.items.some((c) => c.backup_id !== backup))
    throw new Error("Invalid deletion progress");
  return { ...v, ...selected };
}
const path = (scope: ArchiveScope, backup: string) =>
  `/api/v1/resources/${encodeURIComponent(scope.resource)}/storage-backups/${encodeURIComponent(backup)}`;
export async function previewDeletion(
  scope: ArchiveScope,
  resource: Resource,
  backup: string,
  body: DeletionPreviewInput,
  signal?: AbortSignal,
) {
  const raw = await boundedStorageRequest(
    (s) =>
      request<unknown>(`${path(scope, backup)}/deletion-previews`, {
        method: "POST",
        body,
        signal: s,
        cache: "no-store",
        requestID: body.request_id,
      }),
    signal,
  );
  return parseDeletionPreview(raw, scope, resource, backup);
}
export async function executeDeletion(
  scope: ArchiveScope,
  resource: Resource,
  backup: string,
  body: DeletionInput,
  signal?: AbortSignal,
) {
  const raw = await boundedStorageRequest(
    (s) =>
      request<unknown>(`${path(scope, backup)}/deletions`, {
        method: "POST",
        body,
        signal: s,
        cache: "no-store",
        requestID: body.request_id,
      }),
    signal,
  );
  return parseDeletionExecution(raw, scope, resource, backup);
}
export async function listDeletions(
  scope: ArchiveScope,
  resource: Resource,
  backup: string,
  signal?: AbortSignal,
) {
  const raw = await boundedStorageRequest(
    (s) =>
      request<unknown>(`${path(scope, backup)}/deletions`, {
        signal: s,
        cache: "no-store",
      }),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100)
    throw new Error("Invalid deletion history");
  return raw.map((v) => parseDeletionExecution(v, scope, resource, backup));
}
