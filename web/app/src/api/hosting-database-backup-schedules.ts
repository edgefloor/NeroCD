import {
  parseRemoteBackupTarget,
  type RemoteBackupTarget,
} from "./hosting-database-backup-targets";
import { request } from "./client";
import type { components } from "./generated";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
  type HostingDatabase,
} from "./hosting-databases";

export type DatabaseBackupSchedule =
  components["schemas"]["HostingDatabaseBackupSchedule"] & {
    remote_target?: RemoteBackupTarget;
  };
export type DatabaseBackupScheduleInput =
  components["schemas"]["HostingDatabaseBackupScheduleInput"] & {
    remote_target?: RemoteBackupTarget;
  };
export type DatabaseBackupOccurrence =
  components["schemas"]["HostingDatabaseBackupOccurrence"];
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
  throw new Error("Invalid database backup schedule metadata");
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
const occurrenceAllowed = new Set([...occurrenceRequired, "operation_id"]);
const occurrenceStates = new Set([
  "queued",
  "dispatched",
  "applying",
  "succeeded",
  "failed",
  "uncertain",
  "denied",
  "skipped",
  "missed",
]);
const occurrenceReasons = new Set([
  "",
  "authority_denied",
  "source_unavailable",
  "source_busy",
  "placement_changed",
  "newer_occurrence",
  "too_old",
  "queue_unavailable",
  "execution_failed",
  "publication_failed",
  "interrupted",
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
export function validDatabaseBackupScheduleFields(
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
export function parseDatabaseBackupSchedule(
  raw: unknown,
  scope: DatabaseScope,
  database: HostingDatabase,
): DatabaseBackupSchedule {
  const v = document(raw, scheduleRequired, scheduleAllowed);
  if (
    !identifier(v.id) ||
    v.version !== "postgresql.logical-local.schedule.v1" ||
    v.project_id !== scope.project ||
    v.resource_id !== database.resource.id ||
    !positive(v.revision) ||
    !text(v.frequency) ||
    !text(v.timezone) ||
    typeof v.enabled !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.last_scheduled_at !== undefined && !date(v.last_scheduled_at))
  )
    return fail();
  return {
    id: v.id,
    version: "postgresql.logical-local.schedule.v1",
    project_id: scope.project,
    resource_id: database.resource.id,
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
export function parseDatabaseBackupOccurrence(
  raw: unknown,
  scope: DatabaseScope,
  database: HostingDatabase,
  schedule: string,
): DatabaseBackupOccurrence {
  const v = document(raw, occurrenceRequired, occurrenceAllowed);
  if (
    !identifier(v.id) ||
    v.schedule_id !== schedule ||
    !identifier(v.schedule_id) ||
    v.project_id !== scope.project ||
    v.resource_id !== database.resource.id ||
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
    (v.operation_id !== undefined && !identifier(v.operation_id))
  )
    return fail();
  if (
    ["denied", "skipped"].includes(v.state)
      ? v.operation_id !== undefined || v.ownership_held || !v.reason
      : !identifier(v.operation_id)
  )
    return fail();
  if (
    (["succeeded", "missed"].includes(v.state) && v.ownership_held) ||
    (["queued", "dispatched", "applying", "uncertain"].includes(v.state) &&
      !v.ownership_held)
  )
    return fail();
  return {
    id: v.id,
    schedule_id: schedule,
    project_id: scope.project,
    resource_id: database.resource.id,
    schedule_revision: v.schedule_revision,
    scheduled_at: v.scheduled_at,
    created_at: v.created_at,
    frequency: v.frequency,
    timezone: v.timezone,
    state: v.state as DatabaseBackupOccurrence["state"],
    reason: v.reason as DatabaseBackupOccurrence["reason"],
    ownership_held: v.ownership_held,
    ...(v.operation_id !== undefined
      ? { operation_id: v.operation_id as string }
      : {}),
  };
}
const path = (database: HostingDatabase) =>
  `/api/v1/resources/${encodeURIComponent(database.resource.id)}/database-backup-schedules`;
export const databaseBackupSchedulesKey = (
  scope: DatabaseScope,
  resource: string,
) =>
  [
    "database-backup-schedules",
    scope.actor,
    scope.project,
    scope.environment,
    resource,
  ] as const;
export async function listDatabaseBackupSchedules(
  scope: DatabaseScope,
  database: HostingDatabase,
  signal?: AbortSignal,
) {
  const raw = await boundedDatabaseRequest(
    (s) => request<unknown>(path(database), { signal: s, cache: "no-store" }),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map((v) => parseDatabaseBackupSchedule(v, scope, database));
  if (new Set(out.map((v) => v.id)).size !== out.length) return fail();
  return out;
}
export async function saveDatabaseBackupSchedule(
  scope: DatabaseScope,
  database: HostingDatabase,
  schedule: string,
  input: DatabaseBackupScheduleInput,
  signal: AbortSignal,
) {
  if (
    !identifier(input.request_id) ||
    input.expected_resource_generation !== database.resource.generation ||
    !Number.isSafeInteger(input.expected_version) ||
    input.expected_version < (schedule ? 1 : 0) ||
    (!schedule && input.expected_version !== 0) ||
    (schedule && !identifier(schedule)) ||
    typeof input.enabled !== "boolean" ||
    !validDatabaseBackupScheduleFields(input.frequency, input.timezone)
  )
    return fail();
  if (input.remote_target !== undefined)
    parseRemoteBackupTarget(input.remote_target);
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        path(database) + (schedule ? `/${encodeURIComponent(schedule)}` : ""),
        {
          method: schedule ? "PUT" : "POST",
          body: input,
          signal: s,
          requestID: input.request_id,
        },
      ),
    signal,
  );
  const out = parseDatabaseBackupSchedule(raw, scope, database);
  if (
    (schedule && out.id !== schedule) ||
    out.revision !== input.expected_version + 1 ||
    out.frequency !== input.frequency ||
    out.timezone !== input.timezone ||
    out.enabled !== input.enabled ||
    out.remote_target?.target_id !== input.remote_target?.target_id ||
    out.remote_target?.revision !== input.remote_target?.revision
  )
    return fail();
  return out;
}
export async function listDatabaseBackupOccurrences(
  scope: DatabaseScope,
  database: HostingDatabase,
  schedule: string,
  signal?: AbortSignal,
) {
  if (!identifier(schedule)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `${path(database)}/${encodeURIComponent(schedule)}/occurrences`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map((v) =>
    parseDatabaseBackupOccurrence(v, scope, database, schedule),
  );
  if (
    new Set(out.map((v) => v.id)).size !== out.length ||
    new Set(out.map((v) => Date.parse(v.scheduled_at))).size !== out.length
  )
    return fail();
  return out;
}
