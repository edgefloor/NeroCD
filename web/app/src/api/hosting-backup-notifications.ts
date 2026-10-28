import { parseDatabaseBackupTransfer } from "./hosting-database-backup-targets";
import { request } from "./client";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
  type HostingDatabase,
} from "./hosting-databases";
import { parseDatabaseBackup } from "./hosting-database-backups";

export type BackupEvent = "backup_failed" | "upload_failed";
export type NotificationConfiguration = {
  webhook_url?: string;
  smtp_host?: string;
  smtp_port?: number;
  smtp_security?: "starttls" | "tls";
  smtp_username?: string;
  smtp_password?: string;
  from_address?: string;
  recipient_user_ids?: string[];
};
export type NotificationChannel = {
  id: string;
  project_id: string;
  name: string;
  type: "email" | "webhook";
  enabled: boolean;
  generation: number;
  events: BackupEvent[];
  configured: boolean;
  consecutive_failures: number;
  created_at: string;
  updated_at: string;
};
export type NotificationChannelInput = {
  request_id: string;
  expected_generation: number;
  name: string;
  type: NotificationChannel["type"];
  enabled: boolean;
  events: BackupEvent[];
  configuration?: NotificationConfiguration;
};
export type NotificationDelivery = {
  id: string;
  event_id: string;
  project_id: string;
  channel_id: string;
  channel_generation: number;
  event: BackupEvent | "test";
  source_kind: "backup" | "transfer" | "test" | "storage_backup" | "storage_transfer";
  source_id: string;
  source_state: "failed" | "uncertain" | "test";
  state: "pending" | "sending" | "delivered" | "failed" | "suppressed";
  attempt_count: number;
  max_attempts: number;
  failure_code?: string;
  local_copy_available: boolean;
  recovery_url?: string;
  created_at: string;
  updated_at: string;
};
export type NotificationDeliveryDetail = NotificationDelivery & {
  attempts: {
    number: number;
    state: string;
    failure_code?: string;
    started_at: string;
    finished_at?: string;
  }[];
};
export type BackupRecoveryLocator = {
  project_id: string;
  environment_id: string;
  resource_id: string;
  backup_id: string;
  transfer_id?: string;
  source_kind?: "storage_backup" | "storage_transfer";
};
export const notificationID = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const fail = (): never => {
  throw new Error("Notification metadata is unavailable");
};
const object = (v: unknown): Record<string, unknown> =>
  v && typeof v === "object" && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : fail();
const integer = (v: unknown, minimum = 0): v is number =>
  Number.isSafeInteger(v) && Number(v) >= minimum;
const date = (v: unknown): v is string =>
  typeof v === "string" && Number.isFinite(Date.parse(v));
const closed = (v: Record<string, unknown>, keys: string[]) => {
  const allowed = new Set(keys);
  return Object.keys(v).every((key) => allowed.has(key));
};
export function parseBackupRecoveryLocator(
  raw: Record<string, unknown>,
): BackupRecoveryLocator | undefined {
  const keys = [
    "project_id",
    "environment_id",
    "resource_id",
    "backup_id",
  ] as const;
  if (
    !keys.every((k) => notificationID(raw[k])) ||
    (raw.transfer_id !== undefined && !notificationID(raw.transfer_id)) ||
    (raw.source_kind !== undefined && !["storage_backup","storage_transfer"].includes(String(raw.source_kind))) ||
    (raw.source_kind === "storage_transfer" && !notificationID(raw.transfer_id))
  )
    return;
  return {
    project_id: String(raw.project_id),
    environment_id: String(raw.environment_id),
    resource_id: String(raw.resource_id),
    backup_id: String(raw.backup_id),
    ...(raw.transfer_id ? { transfer_id: String(raw.transfer_id) } : {}),
    ...(raw.source_kind ? {source_kind: raw.source_kind as "storage_backup" | "storage_transfer"}: {}),
  };
}
export function safeRecoveryURL(
  value: unknown,
  project: string,
): string | undefined {
  if (
    typeof value !== "string" ||
    value.length > 1200 ||
    !value.startsWith("/projects?")
  )
    return;
  const url = new URL(value, "https://local.invalid");
  const keys = [
    "project_id",
    "environment_id",
    "resource_id",
    "backup_id",
    "transfer_id",
    "source_kind",
  ];
  if (
    [...url.searchParams.keys()].some(
      (k) => !keys.includes(k) || url.searchParams.getAll(k).length !== 1,
    ) ||
    url.hash
  )
    return;
  const locator = parseBackupRecoveryLocator(
    Object.fromEntries(url.searchParams),
  );
  if (!locator || locator.project_id !== project) return;
  return `/projects?${new URLSearchParams(locator)}`;
}
export function parseNotificationChannel(
  raw: unknown,
  project: string,
): NotificationChannel {
  const v = object(raw);
  const keys = [
    "id",
    "project_id",
    "name",
    "type",
    "enabled",
    "generation",
    "events",
    "configured",
    "consecutive_failures",
    "created_at",
    "updated_at",
  ];
  if (
    !closed(v, keys) ||
    !notificationID(v.id) ||
    v.project_id !== project ||
    typeof v.name !== "string" ||
    !v.name.trim() ||
    v.name.length > 255 ||
    /[\x00-\x1f\x7f]/.test(v.name) ||
    !["email", "webhook"].includes(String(v.type)) ||
    typeof v.enabled !== "boolean" ||
    !integer(v.generation, 1) ||
    !Array.isArray(v.events) ||
    v.events.some(
      (e) => !["backup_failed", "upload_failed"].includes(String(e)),
    ) ||
    new Set(v.events).size !== v.events.length ||
    typeof v.configured !== "boolean" ||
    !integer(v.consecutive_failures) ||
    !date(v.created_at) ||
    !date(v.updated_at)
  )
    return fail();
  return {
    id: v.id,
    project_id: project,
    name: v.name,
    type: v.type as NotificationChannel["type"],
    enabled: v.enabled,
    generation: v.generation,
    events: v.events as BackupEvent[],
    configured: v.configured,
    consecutive_failures: v.consecutive_failures,
    created_at: v.created_at,
    updated_at: v.updated_at,
  };
}
// Unknown server codes never become operator-visible text.
export function notificationFailure(code?: string): string {
  return code
    ? "Delivery needs attention. Review channel configuration and current project access."
    : "No failure recorded.";
}
export function parseNotificationDelivery(
  raw: unknown,
  project: string,
  detail = false,
): NotificationDeliveryDetail {
  const v = object(raw);
  const keys = [
    "id",
    "event_id",
    "project_id",
    "channel_id",
    "channel_generation",
    "event",
    "source_kind",
    "source_id",
    "source_state",
    "state",
    "attempt_count",
    "max_attempts",
    "failure_code",
    "local_copy_available",
    "recovery_url",
    "created_at",
    "updated_at",
    ...(detail ? ["attempts"] : []),
  ];
  if (
    !closed(v, keys) ||
    ![v.id, v.event_id, v.channel_id, v.source_id].every(notificationID) ||
    v.project_id !== project ||
    !["failed", "uncertain", "test"].includes(String(v.source_state)) ||
    !integer(v.channel_generation, 1) ||
    !["backup_failed", "upload_failed", "test"].includes(String(v.event)) ||
    !(v.event === "test" ? v.source_kind === "test" : v.event === "backup_failed" ? ["backup","storage_backup"].includes(String(v.source_kind)) : ["transfer","storage_transfer"].includes(String(v.source_kind))) ||
    !["pending", "sending", "delivered", "failed", "suppressed"].includes(
      String(v.state),
    ) ||
    !integer(v.attempt_count) ||
    v.max_attempts !== 5 ||
    Number(v.attempt_count) > 5 ||
    typeof v.local_copy_available !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.failure_code !== undefined &&
      (typeof v.failure_code !== "string" || v.failure_code.length > 128))
  )
    return fail();
  const recovery = safeRecoveryURL(v.recovery_url, project);
  const testRecovery =
    v.event === "test" &&
    v.recovery_url === `/projects?project_id=${encodeURIComponent(project)}`
      ? String(v.recovery_url)
      : undefined;
  if (v.recovery_url && !recovery && !testRecovery) return fail();
  if ((v.event === "test") !== (v.source_state === "test")) return fail();
  const attempts: NotificationDeliveryDetail["attempts"] = [];
  if (detail) {
    if (
      v.attempts !== undefined &&
      (!Array.isArray(v.attempts) || v.attempts.length > 5)
    )
      return fail();
    for (const rawAttempt of (v.attempts ?? []) as unknown[]) {
      const a = object(rawAttempt);
      if (
        !closed(a, [
          "number",
          "state",
          "failure_code",
          "started_at",
          "finished_at",
        ]) ||
        !integer(a.number, 1) ||
        a.number > 5 ||
        !["sending", "delivered", "failed", "uncertain", "suppressed"].includes(
          String(a.state),
        ) ||
        !date(a.started_at) ||
        (a.finished_at !== undefined && !date(a.finished_at)) ||
        (a.failure_code !== undefined &&
          (typeof a.failure_code !== "string" || a.failure_code.length > 128))
      )
        return fail();
      attempts.push({
        number: a.number,
        state: String(a.state),
        ...(a.failure_code ? { failure_code: "attention" } : {}),
        started_at: a.started_at,
        ...(a.finished_at ? { finished_at: String(a.finished_at) } : {}),
      });
    }
    if (new Set(attempts.map((a) => a.number)).size !== attempts.length)
      return fail();
  }
  return {
    id: String(v.id),
    event_id: String(v.event_id),
    project_id: project,
    channel_id: String(v.channel_id),
    channel_generation: Number(v.channel_generation),
    event: v.event as NotificationDelivery["event"],
    source_kind: v.source_kind as NotificationDelivery["source_kind"],
    source_id: String(v.source_id),
    source_state: v.source_state as NotificationDelivery["source_state"],
    state: v.state as NotificationDelivery["state"],
    attempt_count: Number(v.attempt_count),
    max_attempts: 5,
    local_copy_available: v.local_copy_available as boolean,
    created_at: String(v.created_at),
    updated_at: String(v.updated_at),
    ...(v.failure_code ? { failure_code: "attention" } : {}),
    ...(recovery || testRecovery
      ? { recovery_url: recovery ?? testRecovery }
      : {}),
    attempts,
  };
}
const path = (project: string) => {
  if (!notificationID(project)) return fail();
  return `/api/v1/projects/${encodeURIComponent(project)}`;
};
async function run(
  project: string,
  suffix: string,
  signal?: AbortSignal,
  input?: unknown,
  method: "GET" | "POST" | "PUT" = "GET",
) {
  return await boundedDatabaseRequest(
    (s) =>
      request<unknown>(path(project) + suffix, {
        method,
        signal: s,
        cache: "no-store",
        ...(input
          ? {
              body: input,
              requestID: (input as { request_id: string }).request_id,
            }
          : {}),
      }),
    signal,
  );
}
function list<T>(raw: unknown, parse: (v: unknown) => T & { id: string }): T[] {
  if (!Array.isArray(raw) || raw.length > 100) return fail();
  const out = raw.map(parse);
  if (new Set(out.map((v) => v.id)).size !== out.length) return fail();
  return out;
}
export async function listNotificationChannels(
  project: string,
  signal?: AbortSignal,
) {
  return list(await run(project, "/notification-channels", signal), (v) =>
    parseNotificationChannel(v, project),
  );
}
export async function saveNotificationChannel(
  project: string,
  input: NotificationChannelInput,
  channel?: string,
  signal?: AbortSignal,
) {
  if (channel && !notificationID(channel)) return fail();
  const out = parseNotificationChannel(
    await run(
      project,
      `/notification-channels${channel ? `/${encodeURIComponent(channel)}` : ""}`,
      signal,
      input,
      channel ? "PUT" : "POST",
    ),
    project,
  );
  if (
    (channel && out.id !== channel) ||
    out.generation !== input.expected_generation + 1 ||
    out.type !== input.type ||
    out.name !== input.name ||
    out.enabled !== input.enabled ||
    out.events.slice().sort().join() !== input.events.slice().sort().join()
  )
    return fail();
  return out;
}
export async function testNotificationChannel(
  project: string,
  channel: string,
  input: { request_id: string; expected_generation: number },
  signal?: AbortSignal,
) {
  if (!notificationID(channel)) return fail();
  const out = parseNotificationDelivery(
    await run(
      project,
      `/notification-channels/${encodeURIComponent(channel)}/tests`,
      signal,
      input,
      "POST",
    ),
    project,
  );
  if (
    out.channel_id !== channel ||
    out.channel_generation !== input.expected_generation ||
    out.event !== "test"
  )
    return fail();
  return out;
}
export async function listNotificationDeliveries(
  project: string,
  signal?: AbortSignal,
) {
  return list(await run(project, "/notification-deliveries", signal), (v) =>
    parseNotificationDelivery(v, project),
  );
}
export async function getNotificationDelivery(
  project: string,
  delivery: string,
  signal?: AbortSignal,
) {
  if (!notificationID(delivery)) return fail();
  const out = parseNotificationDelivery(
    await run(
      project,
      `/notification-deliveries/${encodeURIComponent(delivery)}`,
      signal,
    ),
    project,
    true,
  );
  if (out.id !== delivery) return fail();
  return out;
}

export async function getNotificationRecoveryBackup(
  scope: DatabaseScope,
  database: HostingDatabase,
  backup: string,
  signal?: AbortSignal,
) {
  if (!notificationID(backup)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-backups/${encodeURIComponent(backup)}`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  const out = parseDatabaseBackup(raw, scope, database);
  if (out.id !== backup || out.mode !== "backup") return fail();
  return out;
}

export async function getNotificationRecoveryTransfer(scope: DatabaseScope, backup: string, transfer: string, signal?: AbortSignal) {
  if (!notificationID(backup) || !notificationID(transfer)) return fail();
  const raw = await boundedDatabaseRequest(s => request<unknown>(`/api/v1/database-backup-transfers/${encodeURIComponent(transfer)}`, { signal: s, cache: "no-store" }), signal);
  const out = parseDatabaseBackupTransfer(raw, scope);
  if (out.id !== transfer || out.backup_id !== backup || out.mode !== "upload") return fail();
  return out;
}
