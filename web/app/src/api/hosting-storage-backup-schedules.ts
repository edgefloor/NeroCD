import {
  parseRemoteBackupTarget,
  type RemoteBackupTarget,
} from "./hosting-database-backup-targets";
import type { Resource } from "./hosting";
import type { ArchiveScope } from "./hosting-storage-archives";
import { request } from "./client";
import type { components } from "./generated";
import { boundedDatabaseRequest } from "./hosting-databases";

export type StorageBackupSchedule = Omit<
  components["schemas"]["HostingStorageBackupSchedule"],
  "version"
> & {
  version:
    | "hosting.storage-backup.schedule.v1"
    | "managed-compose.local-volume-archive.schedule.v1";
  volume?: string;
  acknowledge_interruption?: true;
  remote_target?: RemoteBackupTarget;
};
export type StorageBackupScheduleInput =
  components["schemas"]["HostingStorageBackupScheduleInput"] & {
    volume?: string;
    expected_deployed_operation_id?: string;
    acknowledge_interruption?: boolean;
    remote_target?: RemoteBackupTarget;
  };
export type StorageBackupOccurrence =
  components["schemas"]["HostingStorageBackupOccurrence"] & { volume?: string };
type ScheduleResource = Pick<Resource, "id" | "generation">;
const identifier = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const text = (v: unknown): v is string =>
  typeof v === "string" &&
  v.length > 0 &&
  v.length <= 128 &&
  !/[\x00-\x1f\x7f]/.test(v);
const date = (v: unknown): v is string =>
  typeof v === "string" && v.length <= 64 && Number.isFinite(Date.parse(v));
const positive = (v: unknown): v is number =>
  Number.isSafeInteger(v) && Number(v) > 0;
const fail = (): never => {
  throw new Error("Invalid storage backup schedule metadata");
};
const scheduleRequired = [
  "id",
  "version",
  "project_id",
  "resource_id",
  "revision",
  "frequency",
  "timezone",
  "enabled",
  "created_at",
  "updated_at",
];
const scheduleAllowed = new Set([
  ...scheduleRequired,
  "last_scheduled_at",
  "remote_target",
  "volume",
  "acknowledge_interruption",
]);
const occurrenceRequired = [
  "id",
  "schedule_id",
  "project_id",
  "resource_id",
  "schedule_revision",
  "scheduled_at",
  "frequency",
  "timezone",
  "state",
  "reason",
  "ownership_held",
  "created_at",
];
const occurrenceAllowed = new Set([
  ...occurrenceRequired,
  "operation_id",
  "backup_id",
  "volume",
]);
const occurrenceStates = new Set([
  "queued",
  "dispatched",
  "applying",
  "preparing",
  "prepared",
  "cancel_requested",
  "canceled",
  "reconciliation_required",
  "succeeded",
  "failed",
  "denied",
  "skipped",
]);
const occurrenceReasons = new Set([
  "",
  "authority_denied",
  "source_unavailable",
  "source_busy",
  "placement_changed",
  "interrupted",
  "authority_lost",
  "ownership_mismatch",
  "helper_failed",
  "archive_invalid",
  "limit_exceeded",
  "resume_failed",
  "canceled",
]);
const aliases = new Set([
  "every_minute",
  "hourly",
  "daily",
  "weekly",
  "monthly",
  "yearly",
  "@hourly",
  "@daily",
  "@weekly",
  "@monthly",
  "@yearly",
]);
function document(raw: unknown, required: string[], allowed: Set<string>) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return fail();
  const v = raw as Record<string, unknown>;
  if (
    required.some((key) => !Object.hasOwn(v, key)) ||
    Object.keys(v).some((key) => !allowed.has(key))
  )
    return fail();
  return v;
}
export function validStorageBackupScheduleFields(
  frequency: string,
  timezone: string,
) {
  if (
    !text(frequency) ||
    frequency.trim() !== frequency ||
    !text(timezone) ||
    timezone.trim() !== timezone ||
    timezone === "Local"
  )
    return false;
  if (
    !aliases.has(frequency) &&
    (frequency.split(/\s+/).length !== 5 || frequency.includes("="))
  )
    return false;
  try {
    new Intl.DateTimeFormat("en", { timeZone: timezone });
    return true;
  } catch {
    return false;
  }
}
export function parseStorageBackupSchedule(
  raw: unknown,
  scope: ArchiveScope,
  resource: ScheduleResource,
): StorageBackupSchedule {
  const v = document(raw, scheduleRequired, scheduleAllowed);
  if (
    !identifier(v.id) ||
    ![
      "hosting.storage-backup.schedule.v1",
      "managed-compose.local-volume-archive.schedule.v1",
    ].includes(String(v.version)) ||
    v.project_id !== scope.project ||
    v.resource_id !== resource.id ||
    !positive(v.revision) ||
    !text(v.frequency) ||
    !text(v.timezone) ||
    typeof v.enabled !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.last_scheduled_at !== undefined && !date(v.last_scheduled_at))
  )
    return fail();
  const compose =
    v.version === "managed-compose.local-volume-archive.schedule.v1";
  if (
    compose
      ? !identifier(v.volume) ||
        v.acknowledge_interruption !== true
      : v.volume !== undefined || v.acknowledge_interruption !== undefined
  )
    return fail();
  return {
    id: v.id,
    version: v.version as StorageBackupSchedule["version"],
    ...(compose
      ? { volume: v.volume as string, acknowledge_interruption: true as const }
      : {}),
    project_id: scope.project,
    resource_id: resource.id,
    revision: v.revision,
    frequency: v.frequency,
    timezone: v.timezone,
    enabled: v.enabled,
    created_at: v.created_at,
    updated_at: v.updated_at,
    ...(v.remote_target !== undefined
      ? { remote_target: parseRemoteBackupTarget(v.remote_target) }
      : {}),
    ...(v.last_scheduled_at !== undefined
      ? { last_scheduled_at: v.last_scheduled_at as string }
      : {}),
  };
}
export function parseStorageBackupOccurrence(
  raw: unknown,
  scope: ArchiveScope,
  resource: ScheduleResource,
  schedule: string,
): StorageBackupOccurrence {
  const v = document(raw, occurrenceRequired, occurrenceAllowed);
  if (
    !identifier(v.id) ||
    v.schedule_id !== schedule ||
    !identifier(v.schedule_id) ||
    v.project_id !== scope.project ||
    v.resource_id !== resource.id ||
    !positive(v.schedule_revision) ||
    !text(v.frequency) ||
    !text(v.timezone) ||
    !date(v.scheduled_at) ||
    !date(v.created_at) ||
    typeof v.state !== "string" ||
    !occurrenceStates.has(v.state) ||
    typeof v.reason !== "string" ||
    !occurrenceReasons.has(v.reason) ||
    typeof v.ownership_held !== "boolean" ||
    (v.backup_id !== undefined && !identifier(v.backup_id)) ||
    (v.operation_id !== undefined && !identifier(v.operation_id)) ||
    (v.volume !== undefined && !identifier(v.volume))
  )
    return fail();
  if (
    ["denied", "skipped"].includes(v.state)
      ? v.operation_id !== undefined || v.ownership_held || !v.reason
      : !identifier(v.operation_id)
  )
    return fail();
  if (
    (v.state === "succeeded" && v.ownership_held) ||
    (["queued", "dispatched", "applying", "reconciliation_required"].includes(
      v.state,
    ) &&
      !v.ownership_held)
  )
    return fail();
  return {
    id: v.id,
    schedule_id: schedule,
    ...(v.volume !== undefined ? { volume: v.volume as string } : {}),
    project_id: scope.project,
    resource_id: resource.id,
    schedule_revision: v.schedule_revision,
    scheduled_at: v.scheduled_at,
    created_at: v.created_at,
    frequency: v.frequency,
    timezone: v.timezone,
    state: v.state as StorageBackupOccurrence["state"],
    reason: v.reason as StorageBackupOccurrence["reason"],
    ownership_held: v.ownership_held,
    ...(v.backup_id !== undefined ? { backup_id: v.backup_id as string } : {}),
    ...(v.operation_id !== undefined
      ? { operation_id: v.operation_id as string }
      : {}),
  };
}
const path = (resource: ScheduleResource) =>
  `/api/v1/resources/${encodeURIComponent(resource.id)}/storage-backup-schedules`;
export const storageBackupSchedulesKey = (
  scope: ArchiveScope,
  resource: string,
) =>
  [
    "storage-backup-schedules",
    scope.actor,
    scope.project,
    scope.destination,
    scope.binding,
    resource,
  ] as const;
export async function listStorageBackupSchedules(
  scope: ArchiveScope,
  resource: ScheduleResource,
  signal?: AbortSignal,
) {
  const raw = await boundedDatabaseRequest(
    (s) => request<unknown>(path(resource), { signal: s, cache: "no-store" }),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map((v) => parseStorageBackupSchedule(v, scope, resource));
  if (new Set(out.map((v) => v.id)).size !== out.length) return fail();
  return out;
}
export async function saveStorageBackupSchedule(
  scope: ArchiveScope,
  resource: ScheduleResource,
  schedule: string,
  input: StorageBackupScheduleInput,
  signal: AbortSignal,
) {
  if (
    !/^[0-9a-f]{32}$/.test(input.request_id) ||
    !positive(input.expected_resource_generation) ||
    !Number.isSafeInteger(input.expected_version) ||
    input.expected_version < (schedule ? 1 : 0) ||
    (!schedule && input.expected_version !== 0) ||
    (schedule && !identifier(schedule)) ||
    typeof input.enabled !== "boolean" ||
    !validStorageBackupScheduleFields(input.frequency, input.timezone)
  )
    return fail();
  const compose = input.volume !== undefined;
  if (
    compose
      ? !identifier(input.volume) ||
        ((input.enabled || !schedule) &&
          (!identifier(input.expected_deployed_operation_id) ||
            input.acknowledge_interruption !== true)) ||
        (input.expected_deployed_operation_id !== undefined &&
          !identifier(input.expected_deployed_operation_id)) ||
        (input.acknowledge_interruption !== undefined &&
          input.acknowledge_interruption !== true)
      : input.expected_deployed_operation_id !== undefined ||
        input.acknowledge_interruption !== undefined
  )
    return fail();
  if (input.remote_target !== undefined)
    parseRemoteBackupTarget(input.remote_target);
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        path(resource) + (schedule ? `/${encodeURIComponent(schedule)}` : ""),
        {
          method: schedule ? "PUT" : "POST",
          body: input,
          signal: s,
          requestID: input.request_id,
        },
      ),
    signal,
  );
  const out = parseStorageBackupSchedule(raw, scope, resource);
  if (
    (schedule && out.id !== schedule) ||
    out.revision !== input.expected_version + 1 ||
    out.frequency !== input.frequency ||
    out.timezone !== input.timezone ||
    out.enabled !== input.enabled ||
    out.volume !== input.volume ||
    (out.version === "managed-compose.local-volume-archive.schedule.v1") !==
      compose ||
    ((input.enabled || input.remote_target !== undefined) &&
      (out.remote_target?.target_id !== input.remote_target?.target_id ||
        out.remote_target?.revision !== input.remote_target?.revision))
  )
    return fail();
  return out;
}
export async function listStorageBackupOccurrences(
  scope: ArchiveScope,
  resource: ScheduleResource,
  schedule: string,
  signal?: AbortSignal,
) {
  if (!identifier(schedule)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `${path(resource)}/${encodeURIComponent(schedule)}/occurrences`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map((v) =>
    parseStorageBackupOccurrence(v, scope, resource, schedule),
  );
  if (
    new Set(out.map((v) => v.id)).size !== out.length ||
    new Set(out.map((v) => Date.parse(v.scheduled_at))).size !== out.length
  )
    return fail();
  return out;
}
