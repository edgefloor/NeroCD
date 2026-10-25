import { request, type RequestOptions } from "./client";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
  type HostingDatabase,
} from "./hosting-databases";

export const retentionVersion = "postgresql.backup-retention.v1" as const;
export type RetentionLimits = {
  keep_count: number;
  keep_days: number;
  max_bytes: number;
};
export type RetentionPolicy = {
  version: typeof retentionVersion;
  project_id: string;
  resource_id: string;
  revision: number;
  resource_generation: number;
  enabled: boolean;
  automatic: boolean;
  local: RetentionLimits;
  remote: RetentionLimits;
  created_at: string;
  updated_at: string;
};
export type RetentionPolicyInput = Pick<
  RetentionPolicy,
  "enabled" | "automatic" | "local" | "remote"
> & {
  request_id: string;
  expected_revision: number;
  expected_resource_generation: number;
};
export const protectedReasons = [
  "newest",
  "shared",
  "active",
  "upload_pending",
  "unproven_ownership",
  "external",
  "unsupported_conditional",
  "reserved",
  "unavailable",
  "batch_limit",
] as const;
export type RetentionCopy = {
  copy_id: string;
  backup_id: string;
  transfer_id?: string;
  location: "local" | "remote";
  digest: string;
  bytes: number;
  created_at: string;
  availability: "available" | "reserved" | "deleted" | "uncertain" | "unproven";
  selected: boolean;
  protected_reasons: (typeof protectedReasons)[number][];
};
export type RetentionPreview = {
  id: string;
  version: typeof retentionVersion;
  project_id: string;
  resource_id: string;
  policy_revision: number;
  plan_hash: string;
  local_reference_at: string | null;
  remote_reference_at: string | null;
  selection_truncated: boolean;
  items: RetentionCopy[];
  created_at: string;
};
export type RetentionPreviewInput = {
  request_id: string;
  expected_policy_revision: number;
};
export type RetentionExecutionInput = RetentionPreviewInput & {
  preview_id: string;
  expected_plan_hash: string;
};
export type RetentionExecution = {
  id: string;
  version: typeof retentionVersion;
  project_id: string;
  resource_id: string;
  preview_id: string;
  policy_revision: number;
  plan_hash: string;
  state:
    "queued" | "dispatched" | "applying" | "succeeded" | "failed" | "uncertain";
  reservation_held: boolean;
  local_state:
    "not_selected" | "pending" | "succeeded" | "failed" | "uncertain";
  remote_state:
    "not_selected" | "pending" | "succeeded" | "failed" | "uncertain";
  items: {
    copy_id: string;
    backup_id: string;
    location: "local" | "remote";
    state: "pending" | "intent" | "deleted" | "absent" | "failed" | "uncertain";
    failure_code:
      | ""
      | "authority_denied"
      | "scope_changed"
      | "object_changed"
      | "conditional_unsupported"
      | "interrupted"
      | "storage_unavailable"
      | "remote_unavailable";
    reservation_held: boolean;
  }[];
  created_at: string;
  updated_at: string;
};
const fail = (): never => {
  throw new Error("Invalid database backup retention metadata");
};
const id = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const hash = (v: unknown): v is string =>
  typeof v === "string" && /^sha256:[a-f0-9]{64}$/.test(v);
const date = (v: unknown): v is string =>
  typeof v === "string" && v.length <= 64 && Number.isFinite(Date.parse(v));
const integer = (v: unknown, max: number, min = 0): v is number =>
  Number.isSafeInteger(v) && Number(v) >= min && Number(v) <= max;
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
function limits(raw: unknown): RetentionLimits {
  const v = document(raw, ["keep_count", "keep_days", "max_bytes"]);
  if (
    !integer(v.keep_count, 10000) ||
    !integer(v.keep_days, 36500) ||
    !integer(v.max_bytes, 2 ** 50)
  )
    return fail();
  return v as RetentionLimits;
}
function scoped(
  v: Record<string, unknown>,
  scope: DatabaseScope,
  db: HostingDatabase,
) {
  if (
    v.version !== retentionVersion ||
    v.project_id !== scope.project ||
    v.resource_id !== db.resource.id
  )
    fail();
}
function unique<T>(
  raw: unknown,
  max: number,
  parse: (v: unknown) => T,
  key: (v: T) => string,
): T[] {
  if (!Array.isArray(raw) || raw.length > max) return fail();
  const out = raw.map(parse);
  if (new Set(out.map(key)).size !== out.length) return fail();
  return out;
}
export function parseRetentionPolicy(
  raw: unknown,
  scope: DatabaseScope,
  db: HostingDatabase,
): RetentionPolicy {
  const v = document(raw, [
    "version",
    "project_id",
    "resource_id",
    "revision",
    "resource_generation",
    "enabled",
    "automatic",
    "local",
    "remote",
    "created_at",
    "updated_at",
  ]);
  scoped(v, scope, db);
  if (
    !integer(v.revision, Number.MAX_SAFE_INTEGER, 1) ||
    !integer(v.resource_generation, Number.MAX_SAFE_INTEGER, 1) ||
    typeof v.enabled !== "boolean" ||
    typeof v.automatic !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at)
  )
    return fail();
  return {
    ...v,
    local: limits(v.local),
    remote: limits(v.remote),
  } as RetentionPolicy;
}
export function parseRetentionPreview(
  raw: unknown,
  scope: DatabaseScope,
  db: HostingDatabase,
): RetentionPreview {
  const v = document(raw, [
    "id",
    "version",
    "project_id",
    "resource_id",
    "policy_revision",
    "plan_hash",
    "local_reference_at",
    "remote_reference_at",
    "selection_truncated",
    "items",
    "created_at",
  ]);
  scoped(v, scope, db);
  if (
    !id(v.id) ||
    !integer(v.policy_revision, Number.MAX_SAFE_INTEGER, 1) ||
    !hash(v.plan_hash) ||
    !date(v.created_at) ||
    typeof v.selection_truncated !== "boolean" ||
    ![v.local_reference_at, v.remote_reference_at].every(
      (x) => x === null || date(x),
    )
  )
    return fail();
  const items = unique(
    v.items,
    1000,
    (raw) => {
      const c = document(
        raw,
        [
          "copy_id",
          "backup_id",
          "location",
          "digest",
          "bytes",
          "created_at",
          "availability",
          "selected",
          "protected_reasons",
        ],
        ["transfer_id"],
      );
      if (
        !id(c.copy_id) ||
        !id(c.backup_id) ||
        (c.transfer_id !== undefined && !id(c.transfer_id)) ||
        !["local", "remote"].includes(String(c.location)) ||
        !hash(c.digest) ||
        !integer(c.bytes, Number.MAX_SAFE_INTEGER, 1) ||
        !date(c.created_at) ||
        !["available", "reserved", "deleted", "uncertain", "unproven"].includes(
          String(c.availability),
        ) ||
        typeof c.selected !== "boolean" ||
        !Array.isArray(c.protected_reasons) ||
        c.protected_reasons.some((r) => !protectedReasons.includes(r)) ||
        new Set(c.protected_reasons).size !== c.protected_reasons.length
      )
        return fail();
      if (
        c.selected &&
        (c.availability !== "available" || c.protected_reasons.length)
      )
        return fail();
      return c as RetentionCopy;
    },
    (c) => c.copy_id,
  );
  if (items.filter((c) => c.selected).length > 100) return fail();
  return { ...v, items } as RetentionPreview;
}
export function parseRetentionExecution(
  raw: unknown,
  scope: DatabaseScope,
  db: HostingDatabase,
): RetentionExecution {
  const v = document(raw, [
    "id",
    "version",
    "project_id",
    "resource_id",
    "preview_id",
    "policy_revision",
    "plan_hash",
    "state",
    "reservation_held",
    "local_state",
    "remote_state",
    "items",
    "created_at",
    "updated_at",
  ]);
  scoped(v, scope, db);
  if (
    !id(v.id) ||
    !id(v.preview_id) ||
    !integer(v.policy_revision, Number.MAX_SAFE_INTEGER, 1) ||
    !hash(v.plan_hash) ||
    ![
      "queued",
      "dispatched",
      "applying",
      "succeeded",
      "failed",
      "uncertain",
    ].includes(String(v.state)) ||
    typeof v.reservation_held !== "boolean" ||
    ![v.local_state, v.remote_state].every((x) =>
      ["not_selected", "pending", "succeeded", "failed", "uncertain"].includes(
        String(x),
      ),
    ) ||
    !date(v.created_at) ||
    !date(v.updated_at)
  )
    return fail();
  const items = unique(
    v.items,
    100,
    (raw) => {
      const c = document(raw, [
        "copy_id",
        "backup_id",
        "location",
        "state",
        "failure_code",
        "reservation_held",
      ]);
      if (
        !id(c.copy_id) ||
        !id(c.backup_id) ||
        !["local", "remote"].includes(String(c.location)) ||
        ![
          "pending",
          "intent",
          "deleted",
          "absent",
          "failed",
          "uncertain",
        ].includes(String(c.state)) ||
        ![
          "",
          "authority_denied",
          "scope_changed",
          "object_changed",
          "conditional_unsupported",
          "interrupted",
          "storage_unavailable",
          "remote_unavailable",
        ].includes(String(c.failure_code)) ||
        typeof c.reservation_held !== "boolean"
      )
        return fail();
      return c as RetentionExecution["items"][number];
    },
    (c) => c.copy_id,
  );
  return { ...v, items } as RetentionExecution;
}
export const retentionKey = (s: DatabaseScope, resource: string) =>
  [
    "database-backup-retention",
    s.actor,
    s.project,
    s.environment,
    resource,
  ] as const;
async function call(
  db: HostingDatabase,
  suffix: string,
  options: RequestOptions,
  signal?: AbortSignal,
) {
  return boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(db.resource.id)}/database-backup-retention${suffix}`,
        { ...options, signal: s, cache: "no-store" },
      ),
    signal,
  );
}
function identity(v: string) {
  if (!/^[a-f0-9]{32}$/.test(v)) fail();
}
export async function getRetentionPolicy(
  scope: DatabaseScope,
  db: HostingDatabase,
  signal?: AbortSignal,
) {
  const raw = await call(db, "", {}, signal);
  return raw === null ? null : parseRetentionPolicy(raw, scope, db);
}
export async function saveRetentionPolicy(
  scope: DatabaseScope,
  db: HostingDatabase,
  input: RetentionPolicyInput,
  signal?: AbortSignal,
) {
  identity(input.request_id);
  limits(input.local);
  limits(input.remote);
  if (
    !integer(input.expected_revision, Number.MAX_SAFE_INTEGER) ||
    input.expected_resource_generation !== db.resource.generation ||
    typeof input.enabled !== "boolean" ||
    typeof input.automatic !== "boolean"
  )
    fail();
  const out = parseRetentionPolicy(
    await call(
      db,
      "",
      { method: "PUT", body: input, requestID: input.request_id },
      signal,
    ),
    scope,
    db,
  );
  if (
    out.revision !== input.expected_revision + 1 ||
    out.resource_generation !== input.expected_resource_generation ||
    out.enabled !== input.enabled ||
    out.automatic !== input.automatic ||
    ["local", "remote"].some((k) => {
      const key = k as "local" | "remote";
      return Object.keys(out[key]).some(
        (f) =>
          out[key][f as keyof RetentionLimits] !==
          input[key][f as keyof RetentionLimits],
      );
    })
  )
    fail();
  return out;
}
export async function createRetentionPreview(
  scope: DatabaseScope,
  db: HostingDatabase,
  input: RetentionPreviewInput,
  signal?: AbortSignal,
) {
  identity(input.request_id);
  if (!integer(input.expected_policy_revision, Number.MAX_SAFE_INTEGER, 1))
    fail();
  const out = parseRetentionPreview(
    await call(
      db,
      "/previews",
      { method: "POST", body: input, requestID: input.request_id },
      signal,
    ),
    scope,
    db,
  );
  if (out.policy_revision !== input.expected_policy_revision) fail();
  return out;
}
export async function executeRetention(
  scope: DatabaseScope,
  db: HostingDatabase,
  input: RetentionExecutionInput,
  signal?: AbortSignal,
  expectedExecutionID?: string,
) {
  identity(input.request_id);
  if (
    (expectedExecutionID !== undefined && !id(expectedExecutionID)) ||
    !id(input.preview_id) ||
    !hash(input.expected_plan_hash) ||
    !integer(input.expected_policy_revision, Number.MAX_SAFE_INTEGER, 1)
  )
    fail();
  const out = parseRetentionExecution(
    await call(
      db,
      "/executions",
      { method: "POST", body: input, requestID: input.request_id },
      signal,
    ),
    scope,
    db,
  );
  if (
    (expectedExecutionID !== undefined && out.id !== expectedExecutionID) ||
    out.preview_id !== input.preview_id ||
    out.plan_hash !== input.expected_plan_hash ||
    out.policy_revision !== input.expected_policy_revision
  )
    fail();
  return out;
}
export async function listRetentionExecutions(
  scope: DatabaseScope,
  db: HostingDatabase,
  signal?: AbortSignal,
) {
  return unique(
    await call(db, "/executions", {}, signal),
    100,
    (v) => parseRetentionExecution(v, scope, db),
    (v) => v.id,
  );
}
export async function getRetentionExecution(
  scope: DatabaseScope,
  db: HostingDatabase,
  execution: string,
  signal?: AbortSignal,
) {
  if (!id(execution)) fail();
  const out = parseRetentionExecution(
    await call(db, `/executions/${encodeURIComponent(execution)}`, {}, signal),
    scope,
    db,
  );
  if (out.id !== execution) fail();
  return out;
}
// Decimal parsing uses integer arithmetic so every accepted GiB value maps to exact bytes.
export function retentionGiBToBytes(value: string): number | undefined {
  if (!/^\d{1,7}(?:\.\d{1,30})?$/.test(value)) return undefined;
  const [whole = "0", fraction = ""] = value.split(".");
  const scale = 10n ** BigInt(fraction.length);
  const numerator =
    (BigInt(whole) * scale + BigInt(fraction || "0")) * 1073741824n;
  if (numerator % scale !== 0n || numerator / scale > 2n ** 50n)
    return undefined;
  return Number(numerator / scale);
}
export function retentionBytesToGiB(bytes: number): string {
  if (!integer(bytes, 2 ** 50)) return fail();
  const whole = Math.floor(bytes / 1073741824);
  const remainder = BigInt(bytes % 1073741824);
  return remainder
    ? `${whole}.${(remainder * 5n ** 30n).toString().padStart(30, "0").replace(/0+$/, "")}`
    : String(whole);
}
