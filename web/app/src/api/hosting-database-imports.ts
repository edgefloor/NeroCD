import { request } from "./client";
import type { components } from "./generated";
import { boundedDatabaseRequest } from "./hosting-databases";

export type DatabaseImportStage =
  components["schemas"]["HostingDatabaseImportStage"];
export type DatabaseImportStageInput =
  components["schemas"]["HostingDatabaseImportStageInput"];
export type DatabaseImportOperation =
  components["schemas"]["HostingDatabaseImportOperation"];
export type DatabaseImportInput =
  components["schemas"]["HostingDatabaseImportInput"];
export type DatabaseImportTarget = Pick<
  DatabaseImportInput["component_target"],
  | "resource_id"
  | "component"
  | "expected_deployed_operation_id"
  | "expected_resource_generation"
>;
const maxBytes = 10 * 1024 ** 3;
const id = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
const digest = (v: unknown) =>
  typeof v === "string" && /^sha256:[a-f0-9]{64}$/.test(v);
const positive = (v: unknown, max = Number.MAX_SAFE_INTEGER) =>
  Number.isSafeInteger(v) && Number(v) > 0 && Number(v) <= max;
const date = (v: unknown) =>
  typeof v === "string" && Number.isFinite(Date.parse(v));
const text = (v: unknown) =>
  typeof v === "string" &&
  v.length > 0 &&
  new TextEncoder().encode(v).length <= 1024 &&
  !/[\p{Cc}\p{Surrogate}]/u.test(v);
export const validImportObjectKey = (v: unknown): v is string => text(v);
const failureCodes = new Set([
  "access_denied",
  "authority_denied",
  "network_denied",
  "remote_unavailable",
  "object_mismatch",
  "unsupported_format",
  "too_large",
  "stage_unavailable",
  "stage_expired",
  "execution_failed",
  "interrupted",
]);
const fail = (): never => {
  throw new Error("Invalid external database import metadata");
};
function object(
  v: unknown,
  required: string[],
  optional: string[] = [],
): Record<string, unknown> {
  if (!v || typeof v !== "object" || Array.isArray(v)) return fail();
  const out = v as Record<string, unknown>;
  if (
    required.some((k) => !Object.hasOwn(out, k)) ||
    Object.keys(out).some((k) => !required.includes(k) && !optional.includes(k))
  )
    return fail();
  return out;
}
function source(raw: unknown) {
  const v = object(raw, ["target_id", "revision", "object_key"]);
  if (
    !id(v.target_id) ||
    !positive(v.revision) ||
    !validImportObjectKey(v.object_key)
  )
    return fail();
  return v;
}
function target(v: DatabaseImportTarget) {
  if (
    !id(v.resource_id) ||
    v.component !== "mysql" ||
    !id(v.expected_deployed_operation_id) ||
    !positive(v.expected_resource_generation)
  )
    return fail();
}
export function parseDatabaseImportStage(
  raw: unknown,
  expectedTarget: DatabaseImportTarget,
  expectedSource?: DatabaseImportStageInput["s3_source"],
): DatabaseImportStage {
  target(expectedTarget);
  const v = object(
    raw,
    [
      "id",
      "version",
      "target_resource_id",
      "component",
      "expected_deployed_operation_id",
      "expected_resource_generation",
      "s3_source",
      "state",
      "created_at",
      "updated_at",
    ],
    [
      "failure_code",
      "source_identity",
      "source_bytes",
      "compression",
      "staged_blob",
      "stage_hash",
      "expires_at",
    ],
  );
  const s = source(v.s3_source);
  if (
    !id(v.id) ||
    v.version !== "mysql.compose-external-s3-stage.v1" ||
    v.target_resource_id !== expectedTarget.resource_id ||
    v.component !== "mysql" ||
    v.expected_deployed_operation_id !==
      expectedTarget.expected_deployed_operation_id ||
    v.expected_resource_generation !==
      expectedTarget.expected_resource_generation ||
    !["queued", "running", "ready", "failed", "expired", "consumed"].includes(
      String(v.state),
    ) ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.failure_code !== undefined && !failureCodes.has(String(v.failure_code)))
  )
    return fail();
  if (
    expectedSource &&
    (s.target_id !== expectedSource.target_id ||
      s.revision !== expectedSource.revision ||
      s.object_key !== expectedSource.object_key)
  )
    return fail();
  if (v.source_identity !== undefined) {
    const identity = object(v.source_identity, [], ["version_id", "etag"]);
    if (
      !Object.keys(identity).length ||
      Object.values(identity).some((value) => !text(value))
    )
      return fail();
  }
  if (
    (v.source_bytes !== undefined && !positive(v.source_bytes, maxBytes)) ||
    (v.compression !== undefined &&
      !["plain", "gzip"].includes(String(v.compression))) ||
    (v.stage_hash !== undefined && !digest(v.stage_hash)) ||
    (v.expires_at !== undefined && !date(v.expires_at))
  )
    return fail();
  if (v.staged_blob !== undefined) {
    const blob = object(v.staged_blob, ["digest", "bytes", "format"]);
    if (
      !digest(blob.digest) ||
      !positive(blob.bytes, maxBytes) ||
      blob.format !== "mysql_sql"
    )
      return fail();
  }
  if (
    v.state === "ready" &&
    [
      "source_bytes",
      "compression",
      "staged_blob",
      "stage_hash",
      "expires_at",
    ].some((k) => v[k] === undefined)
  )
    return fail();
  return v as DatabaseImportStage;
}
export function parseDatabaseImportOperation(
  raw: unknown,
  input: DatabaseImportInput,
): DatabaseImportOperation {
  const v = object(
    raw,
    [
      "id",
      "version",
      "stage_id",
      "target_resource_id",
      "target_component",
      "target_deployed_operation_id",
      "state",
      "ownership_held",
      "writers_resumed",
      "created_at",
      "updated_at",
    ],
    ["failure_code"],
  );
  const t = input.component_target;
  if (
    !id(v.id) ||
    v.version !== "mysql.compose-external-import.v1" ||
    v.stage_id !== input.stage_id ||
    v.target_resource_id !== t.resource_id ||
    v.target_component !== "mysql" ||
    v.target_component !== t.component ||
    v.target_deployed_operation_id !== t.expected_deployed_operation_id ||
    !["queued", "running", "succeeded", "failed", "uncertain"].includes(
      String(v.state),
    ) ||
    typeof v.ownership_held !== "boolean" ||
    typeof v.writers_resumed !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.failure_code !== undefined &&
      !failureCodes.has(String(v.failure_code))) ||
    (v.state === "succeeded" && (v.ownership_held || !v.writers_resumed)) ||
    (v.state === "uncertain" && !v.ownership_held)
  )
    return fail();
  return v as DatabaseImportOperation;
}
export async function inspectDatabaseImport(
  resourceID: string,
  input: DatabaseImportStageInput,
  signal: AbortSignal,
) {
  const t: DatabaseImportTarget = {
    resource_id: resourceID,
    component: input.component,
    expected_deployed_operation_id: input.expected_deployed_operation_id,
    expected_resource_generation: input.expected_resource_generation,
  };
  target(t);
  source(input.s3_source);
  if (!id(input.request_id)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(resourceID)}/database-import-stages`,
        { method: "POST", body: input, signal: s, requestID: input.request_id },
      ),
    signal,
  );
  return parseDatabaseImportStage(raw, t, input.s3_source);
}
export async function getDatabaseImportStage(
  stageID: string,
  t: DatabaseImportTarget,
  s3: DatabaseImportStageInput["s3_source"],
  signal: AbortSignal,
) {
  if (!id(stageID)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-import-stages/${encodeURIComponent(stageID)}`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  const out = parseDatabaseImportStage(raw, t, s3);
  if (out.id !== stageID) return fail();
  return out;
}
export async function requestDatabaseImport(
  input: DatabaseImportInput,
  signal: AbortSignal,
) {
  target(input.component_target);
  if (
    !id(input.request_id) ||
    !id(input.stage_id) ||
    !digest(input.expected_stage_hash) ||
    input.component_target.acknowledge_replacement !== true
  )
    return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(input.component_target.resource_id)}/database-imports`,
        { method: "POST", body: input, signal: s, requestID: input.request_id },
      ),
    signal,
  );
  return parseDatabaseImportOperation(raw, input);
}
export async function getDatabaseImport(
  operationID: string,
  input: DatabaseImportInput,
  signal: AbortSignal,
) {
  if (!id(operationID)) return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/database-imports/${encodeURIComponent(operationID)}`,
        { signal: s, cache: "no-store" },
      ),
    signal,
  );
  const out = parseDatabaseImportOperation(raw, input);
  if (out.id !== operationID) return fail();
  return out;
}
