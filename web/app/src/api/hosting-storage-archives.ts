import { request } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
import {
  boundedStorageRequest,
  getStorageDeploymentOptions,
} from "./hosting-storage-deployment";
import type { HostingStorage } from "./hosting-storage";

export type ArchiveScope = {
  actor: string;
  project: string;
  resource: string;
  destination: string;
  binding: string;
};
export type Backup = components["schemas"]["HostingStorageBackup"];
export type BackupInput = components["schemas"]["HostingStorageBackupInput"];
export type RestoreInput = components["schemas"]["HostingStorageRestoreInput"];
export type RecoveryInput =
  components["schemas"]["HostingStorageArchiveRecoveryInput"];
type Operation = components["schemas"]["HostingOperation"];
type Restore = components["schemas"]["HostingStorageRestore"];
export type ArchiveOperation = ReturnType<typeof archiveOperation>;
export type ArchiveRestore = ReturnType<typeof archiveRestore>;
const id = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const runtimeID = (value: unknown): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  new TextEncoder().encode(value).length <= 128 &&
  value.trim() === value &&
  !/[\p{Cc}]/u.test(value);
const displayName = (value: unknown): value is string => runtimeID(value);
const hash = (value: unknown) =>
  typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
const states = [
  "queued",
  "dispatched",
  "preparing",
  "prepared",
  "applying",
  "verifying",
  "succeeded",
  "failed",
  "canceled",
  "cancel_requested",
  "reconciliation_required",
];
const phases = [
  "writer_snapshot",
  "stop_intent",
  "stopped",
  "helper_intent",
  "helper_identified",
  "archive_published",
  "restore_verified",
  "resume_intent",
  "resumed",
];
function fail(): never {
  throw new Error("Archive scope or public evidence mismatch");
}
function keys(
  value: unknown,
  required: string[],
  optional: string[] = [],
): boolean {
  return (
    !!value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => [...required, ...optional].includes(key))
  );
}
function blob(value: Backup["blob"], scope: ArchiveScope, backup: string) {
  if (value === undefined) return;
  if (
    !keys(value, [
      "backup_id",
      "destination_id",
      "daemon_id",
      "sha256",
      "size_bytes",
      "format",
    ]) ||
    value.backup_id !== backup ||
    value.destination_id !== scope.destination ||
    !runtimeID(value.daemon_id) ||
    !hash(value.sha256) ||
    !Number.isSafeInteger(value.size_bytes) ||
    value.size_bytes < 1 ||
    value.size_bytes > 1073741824 ||
    value.format !== "nerocd-storage-v1"
  )
    fail();
}
export function archiveBackup(
  value: Backup,
  scope: ArchiveScope,
  expected?: string,
): Backup {
  if (
    !keys(
      value,
      [
        "id",
        "project_id",
        "resource_id",
        "operation_id",
        "state",
        "created_by",
        "created_at",
      ],
      ["blob"],
    ) ||
    !id(value.id) ||
    (expected !== undefined && value.id !== expected) ||
    value.project_id !== scope.project ||
    value.resource_id !== scope.resource ||
    !id(value.operation_id) ||
    !states.includes(value.state) ||
    !id(value.created_by) ||
    !Number.isFinite(Date.parse(value.created_at))
  )
    fail();
  blob(value.blob, scope, value.id);
  return value;
}
function storageScope(
  value: HostingStorage,
  scope: ArchiveScope,
  resource: string,
  binding?: string,
) {
  if (
    !value ||
    value.project_id !== scope.project ||
    value.resource_id !== resource ||
    value.destination_id !== scope.destination ||
    !id(value.id) ||
    !id(value.binding_id) ||
    (binding !== undefined && value.binding_id !== binding) ||
    value.kind !== "platform_managed" ||
    value.driver !== "local" ||
    value.access_mode !== "read_write" ||
    value.retention !== "retain" ||
    value.generation !== 1 ||
    !hash(value.hash)
  )
    fail();
}
export function archiveOperation(
  value: Operation,
  scope: ArchiveScope,
  expected: string,
  backup?: string,
) {
  const a = value?.storage_archive,
    p = a?.plan;
  if (
    !id(expected) ||
    value?.id !== expected ||
    value.project_id !== scope.project ||
    !id(value.resource_id) ||
    !id(value.binding_id) ||
    !states.includes(value.state) ||
    typeof value.mutation_hold !== "boolean" ||
    typeof value.restore_ownership_held !== "boolean" ||
    !a ||
    !p ||
    !["backup", "restore", "recover"].includes(a.mode) ||
    value.task_mode !== a.mode ||
    p.mode !== a.mode ||
    p.operation_id !== expected ||
    p.project_id !== scope.project ||
    p.destination_id !== scope.destination ||
    !hash(p.hash) ||
    !id(a.backup_id) ||
    p.backup_id !== a.backup_id ||
    (backup !== undefined && a.backup_id !== backup) ||
    !Array.isArray(a.progress) ||
    a.progress.length > 256 ||
    value.observation_lease !== undefined
  )
    fail();
  storageScope(p.source_storage, scope, scope.resource, scope.binding);
  if (p.target) {
    const t = p.target;
    if (
      !id(t.resource.id) ||
      t.resource.id === scope.resource ||
      t.resource.project_id !== scope.project ||
      t.resource.kind !== "application" ||
      t.resource.source.kind !== "image" ||
      value.resource_id !== t.resource.id ||
      value.binding_id !== t.storage.binding_id ||
      !id(a.restore_id) ||
      t.plan.creation_operation_id !== a.restore_id
    )
      fail();
    storageScope(t.storage, scope, t.resource.id);
  } else if (
    value.resource_id !== scope.resource ||
    value.binding_id !== scope.binding ||
    a.mode === "restore"
  )
    fail();
  if (
    a.mode === "recover" &&
    (!id(a.recovery_of_operation_id) ||
      p.recovery_of_operation_id !== a.recovery_of_operation_id)
  )
    fail();
  for (const evidence of a.progress) {
    if (
      !keys(
        evidence,
        ["receipt", "plan_hash", "phase", "restore_verified"],
        ["writers", "helper_container_id", "blob"],
      ) ||
      evidence.plan_hash !== p.hash ||
      !phases.includes(evidence.phase) ||
      typeof evidence.restore_verified !== "boolean" ||
      !keys(evidence.receipt, ["id", "body_hash", "created_at"]) ||
      !id(evidence.receipt.id) ||
      !hash(evidence.receipt.body_hash) ||
      !Number.isFinite(Date.parse(evidence.receipt.created_at))
    )
      fail();
    if (
      evidence.writers !== undefined &&
      (!Array.isArray(evidence.writers) ||
        evidence.writers.length > 64 ||
        evidence.writers.some(
          (w) =>
            !keys(w, [
              "container_id",
              "operation_id",
              "image_id",
              "plan_hash",
              "release_hash",
              "running",
            ]) ||
            !id(w.container_id) ||
            !id(w.operation_id) ||
            !hash(w.image_id) ||
            !hash(w.plan_hash) ||
            !hash(w.release_hash) ||
            typeof w.running !== "boolean",
        ))
    )
      fail();
    if (
      evidence.helper_container_id !== undefined &&
      !id(evidence.helper_container_id)
    )
      fail();
    blob(evidence.blob, scope, a.backup_id);
  }
  const result = a.result;
  if (result) {
    if (
      !keys(
        result,
        ["archive_outcome", "resume_outcome", "restore_verified", "uncertain"],
        ["blob", "failure_code"],
      ) ||
      !["succeeded", "failed", "not_attempted"].includes(
        result.archive_outcome,
      ) ||
      !["resumed", "not_needed", "uncertain"].includes(result.resume_outcome) ||
      typeof result.restore_verified !== "boolean" ||
      typeof result.uncertain !== "boolean" ||
      ![
        undefined,
        "",
        "authority_lost",
        "ownership_mismatch",
        "helper_failed",
        "archive_invalid",
        "limit_exceeded",
        "resume_failed",
        "canceled",
        "interrupted",
      ].includes(result.failure_code)
    )
      fail();
    blob(result.blob, scope, a.backup_id);
  }
  // Only a small public projection reaches query caches and rendering.
  return {
    id: value.id,
    resource: value.resource_id,
    state: value.state,
    mode: a.mode,
    backup: a.backup_id,
    planHash: p.hash,
    mutationHold: value.mutation_hold,
    ownershipHold: value.restore_ownership_held,
    phases: a.progress.map((e) => e.phase),
    result: result
      ? {
          archive: result.archive_outcome,
          resume: result.resume_outcome,
          verified: result.restore_verified,
          uncertain: result.uncertain,
          failure: result.failure_code,
        }
      : undefined,
  };
}
export function archiveRestore(
  value: Restore,
  scope: ArchiveScope,
  expected?: string,
  backup?: string,
) {
  if (
    !keys(value, ["resource", "storage", "operation"]) ||
    !value.resource ||
    value.resource.id === scope.resource ||
    value.resource.project_id !== scope.project ||
    value.resource.kind !== "application" ||
    value.resource.source.kind !== "image" ||
    !id(value.resource.id) ||
    !displayName(value.resource.name)
  )
    fail();
  storageScope(value.storage, scope, value.resource.id);
  const operation = archiveOperation(
    value.operation,
    scope,
    expected ?? value.operation.id,
    backup,
  );
  if (
    operation.mode !== "restore" ||
    operation.resource !== value.resource.id ||
    value.storage.binding_id !== value.operation.binding_id ||
    value.storage.hash !==
      value.operation.storage_archive?.plan.target?.storage.hash ||
    value.operation.storage_archive?.plan.target?.storage.id !==
      value.storage.id
  )
    fail();
  return { resource: value.resource.id, name: value.resource.name, operation };
}
const resourcePath = (s: ArchiveScope) =>
  `/api/v1/resources/${encodeURIComponent(s.resource)}/storage-backups`;
const backupPath = (id: string) =>
  `/api/v1/storage-backups/${encodeURIComponent(id)}`;
const operationPath = (id: string) =>
  `/api/v1/hosting-operations/${encodeURIComponent(id)}`;
const read = <T>(path: string, signal?: AbortSignal) =>
  boundedStorageRequest(
    (s) => request<T>(path, { signal: s, cache: "no-store" }),
    signal,
  );
export async function refreshArchiveConfiguration(
  scope: ArchiveScope,
  storage: HostingStorage,
  signal?: AbortSignal,
) {
  const path = `/api/v1/resources/${encodeURIComponent(scope.resource)}`;
  const reads = [
    read<components["schemas"]["Resource"]>(path, signal),
    read<HostingStorage>(`${path}/storage`, signal),
    read<components["schemas"]["ResourceBinding"]>(`${path}/binding`, signal),
  ] as const;
  // Observe denials immediately, but retain ordinary failures until every sibling
  // has settled. Both race participants handle all later read rejections.
  const denied = new Promise<never>((_, reject) => {
    for (const reading of reads) {
      void reading.catch((cause) => {
        if (cause instanceof ApiError && [401, 403].includes(cause.status))
          reject(cause);
      });
    }
  });
  const settled = Promise.allSettled(reads).then((results) => {
    const [resource, currentStorage, binding] = results;
    if (resource.status === "rejected") throw resource.reason;
    if (currentStorage.status === "rejected") throw currentStorage.reason;
    if (binding.status === "rejected") throw binding.reason;
    return [resource.value, currentStorage.value, binding.value] as const;
  });
  const [resource, currentStorage, binding] = await Promise.race([
    denied,
    settled,
  ]);
  if (
    resource.id !== scope.resource ||
    resource.project_id !== scope.project ||
    resource.kind !== "application" ||
    resource.source.kind !== "image" ||
    !Number.isSafeInteger(resource.generation) ||
    resource.generation < 1 ||
    binding.id !== scope.binding ||
    binding.resource_id !== scope.resource ||
    binding.destination_id !== scope.destination
  )
    fail();
  storageScope(currentStorage, scope, scope.resource, scope.binding);
  if (currentStorage.id !== storage.id || currentStorage.hash !== storage.hash)
    fail();
  return resource;
}
function write<T>(
  path: string,
  body: BackupInput | RestoreInput | RecoveryInput,
  signal?: AbortSignal,
) {
  if (!/^[a-f0-9]{32}$/.test(body.request_id)) fail();
  return boundedStorageRequest(
    (s) =>
      request<T>(path, { method: "POST", body, signal: s, cache: "no-store" }),
    signal,
  );
}
export async function listArchiveBackups(
  scope: ArchiveScope,
  offset: number,
  signal?: AbortSignal,
) {
  const page = await read<components["schemas"]["HostingStorageBackupList"]>(
    `${resourcePath(scope)}?limit=20&offset=${offset}`,
    signal,
  );
  if (
    !keys(page, ["items", "limit", "offset", "count", "total"]) ||
    !Array.isArray(page.items) ||
    page.items.length > 20 ||
    page.limit !== 20 ||
    page.offset !== offset ||
    page.count !== page.items.length ||
    !Number.isSafeInteger(page.total) ||
    page.total < offset + page.count ||
    new Set(page.items.map((b) => b.id)).size !== page.items.length
  )
    fail();
  return { ...page, items: page.items.map((b) => archiveBackup(b, scope)) };
}
export async function getArchiveBackup(
  scope: ArchiveScope,
  backup: string,
  signal?: AbortSignal,
) {
  return archiveBackup(
    await read<Backup>(backupPath(backup), signal),
    scope,
    backup,
  );
}
export async function getArchiveOperation(
  scope: ArchiveScope,
  operation: string,
  backup?: string,
  signal?: AbortSignal,
) {
  return archiveOperation(
    await read<Operation>(operationPath(operation), signal),
    scope,
    operation,
    backup,
  );
}
export async function requestArchiveBackup(
  scope: ArchiveScope,
  body: BackupInput,
  signal?: AbortSignal,
) {
  if (
    !keys(
      body,
      ["request_id", "expected_resource_generation"],
      ["remote_target"],
    ) ||
    !Number.isSafeInteger(body.expected_resource_generation) ||
    body.expected_resource_generation < 1 ||
    (body.remote_target !== undefined &&
      (!keys(body.remote_target, ["target_id", "revision"]) ||
        !id(body.remote_target.target_id) ||
        !Number.isSafeInteger(body.remote_target.revision) ||
        body.remote_target.revision < 1))
  )
    fail();
  return archiveBackup(
    await write<Backup>(resourcePath(scope), body, signal),
    scope,
  );
}
export async function requestArchiveRestore(
  scope: ArchiveScope,
  backup: string,
  body: RestoreInput,
  signal?: AbortSignal,
) {
  if (
    !keys(body, ["request_id", "name"], ["remote_source"]) ||
    !displayName(body.name) ||
    (body.remote_source !== undefined &&
      (!keys(body.remote_source, ["transfer_id", "expected_target_revision"]) ||
        !id(body.remote_source.transfer_id) ||
        !Number.isSafeInteger(body.remote_source.expected_target_revision) ||
        body.remote_source.expected_target_revision < 1))
  )
    fail();
  const value = archiveRestore(
    await write<Restore>(`${backupPath(backup)}/restores`, body, signal),
    scope,
    undefined,
    backup,
  );
  if (value.name !== body.name) fail();
  return value;
}
export async function getArchiveRestore(
  scope: ArchiveScope,
  restore: string,
  signal?: AbortSignal,
) {
  return archiveRestore(
    await read<Restore>(
      `/api/v1/storage-restores/${encodeURIComponent(restore)}`,
      signal,
    ),
    scope,
    restore,
  );
}
export async function requestArchiveRecovery(
  scope: ArchiveScope,
  operation: string,
  backup: string,
  body: RecoveryInput,
  signal?: AbortSignal,
) {
  if (
    !keys(body, ["request_id", "expected_plan_hash"]) ||
    !hash(body.expected_plan_hash)
  )
    fail();
  const raw = await write<Operation>(
    `${operationPath(operation)}/storage-archive-recovery`,
    body,
    signal,
  );
  if (
    raw.id === operation ||
    raw.storage_archive?.mode !== "recover" ||
    raw.storage_archive?.recovery_of_operation_id !== operation ||
    raw.storage_archive?.plan.recovery_evidence?.original_plan_hash !==
      body.expected_plan_hash
  )
    fail();
  return archiveOperation(raw, scope, raw.id, backup);
}
export async function archiveBackupEligible(
  scope: ArchiveScope,
  storage: HostingStorage,
  generation: number,
  signal?: AbortSignal,
) {
  const options = await getStorageDeploymentOptions(scope, signal);
  if (
    !options.available ||
    options.reason !== "available" ||
    options.resource_generation !== generation ||
    options.storage_ref?.storage_id !== storage.id ||
    options.storage_ref.hash !== storage.hash ||
    !options.previous_healthy_operation_id
  )
    return false;
  const op = await read<Operation>(
    operationPath(options.previous_healthy_operation_id),
    signal,
  );
  const snapshot = op.snapshot;
  if (
    op.id !== options.previous_healthy_operation_id ||
    op.project_id !== scope.project ||
    op.resource_id !== scope.resource ||
    op.binding_id !== scope.binding ||
    snapshot.resource_id !== scope.resource ||
    snapshot.destination_id !== scope.destination
  )
    fail();
  return (
    op.state === "succeeded" &&
    !op.mutation_hold &&
    !op.restore_ownership_held &&
    snapshot.policy_version === "public-image.owned-storage.release-ports.v1" &&
    snapshot.storage?.storage_id === storage.id &&
    snapshot.storage.hash === storage.hash &&
    (!snapshot.local_build || !snapshot.local_build.artifact?.build_id) &&
    (!snapshot.database || !snapshot.database.engine) &&
    (!snapshot.runtime_configuration || !snapshot.runtime_configuration.hash)
  );
}
