import { request } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
import { boundedDatabaseRequest } from "./hosting-databases";

export type DatabaseImportStage =
  components["schemas"]["HostingDatabaseImportStage"];
export type DatabaseImportStageInput =
  components["schemas"]["HostingDatabaseImportStageInput"];
export type DatabaseImportUploadStage =
  components["schemas"]["HostingDatabaseImportUploadStage"];
export type DatabaseImportUploadInput =
  components["schemas"]["HostingDatabaseImportUploadInput"];
export type DatabaseImportUploadSource =
  components["schemas"]["HostingDatabaseImportUploadSource"];
export type InspectedDatabaseImportStage =
  DatabaseImportStage | DatabaseImportUploadStage;
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
const enumValue = (v: unknown, choices: readonly string[]): v is string =>
  typeof v === "string" && choices.includes(v);
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
export const validImportFilename = (v: unknown): v is string =>
  text(v) &&
  new TextEncoder().encode(String(v)).length <= 255 &&
  !/[\\/]/.test(String(v));
export const validImportFile = (file: Pick<File, "name" | "size">) =>
  validImportFilename(file.name) && positive(file.size, maxBytes);
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
function parseStage(
  raw: unknown,
  expectedTarget: DatabaseImportTarget,
  upload: boolean,
) {
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
      "state",
      "created_at",
      "updated_at",
      ...(upload
        ? ["upload_source", "upload_hash", "upload_deadline"]
        : ["s3_source"]),
    ],
    [
      "failure_code",
      ...(!upload ? ["source_identity"] : []),
      "source_bytes",
      "compression",
      "staged_blob",
      "stage_hash",
      "expires_at",
    ],
  );
  if (
    !id(v.id) ||
    v.version !==
      (upload
        ? "mysql.compose-local-upload-stage.v1"
        : "mysql.compose-external-s3-stage.v1") ||
    v.target_resource_id !== expectedTarget.resource_id ||
    v.component !== "mysql" ||
    v.expected_deployed_operation_id !==
      expectedTarget.expected_deployed_operation_id ||
    v.expected_resource_generation !==
      expectedTarget.expected_resource_generation ||
    !enumValue(v.state, [
      "queued",
      "running",
      "ready",
      "failed",
      "expired",
      "consumed",
    ]) ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.failure_code !== undefined &&
      (typeof v.failure_code !== "string" || !failureCodes.has(v.failure_code)))
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
      !enumValue(v.compression, ["plain", "gzip"])) ||
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
  return v;
}
export function parseDatabaseImportStage(
  raw: unknown,
  expectedTarget: DatabaseImportTarget,
  expectedSource?: DatabaseImportStageInput["s3_source"],
): DatabaseImportStage {
  const v = parseStage(raw, expectedTarget, false);
  const s = source(v.s3_source);
  if (
    expectedSource &&
    (s.target_id !== expectedSource.target_id ||
      s.revision !== expectedSource.revision ||
      s.object_key !== expectedSource.object_key)
  )
    return fail();
  return v as DatabaseImportStage;
}
export function parseDatabaseImportUploadStage(
  raw: unknown,
  expectedTarget: DatabaseImportTarget,
  expectedSource?: DatabaseImportUploadSource & { upload_hash?: string },
): DatabaseImportUploadStage {
  const v = parseStage(raw, expectedTarget, true);
  const upload = object(v.upload_source, ["filename", "bytes"]);
  if (
    !validImportFilename(upload.filename) ||
    !positive(upload.bytes, maxBytes) ||
    !digest(v.upload_hash) ||
    !date(v.upload_deadline)
  )
    return fail();
  const deadline = Date.parse(String(v.upload_deadline)),
    created = Date.parse(String(v.created_at));
  if (
    deadline <= created ||
    deadline - created > 3600_000 ||
    (v.state === "ready" && v.source_bytes !== upload.bytes)
  )
    return fail();
  if (
    expectedSource &&
    (upload.filename !== expectedSource.filename ||
      upload.bytes !== expectedSource.bytes ||
      (expectedSource.upload_hash !== undefined &&
        v.upload_hash !== expectedSource.upload_hash))
  )
    return fail();
  return v as DatabaseImportUploadStage;
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
    !enumValue(v.state, [
      "queued",
      "running",
      "succeeded",
      "failed",
      "uncertain",
    ]) ||
    typeof v.ownership_held !== "boolean" ||
    typeof v.writers_resumed !== "boolean" ||
    !date(v.created_at) ||
    !date(v.updated_at) ||
    (v.failure_code !== undefined &&
      (typeof v.failure_code !== "string" ||
        !failureCodes.has(v.failure_code))) ||
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

export async function admitDatabaseImportUpload(
  resourceID: string,
  input: DatabaseImportUploadInput,
  signal: AbortSignal,
) {
  object(input, [
    "request_id",
    "component",
    "expected_deployed_operation_id",
    "expected_resource_generation",
    "filename",
    "source_bytes",
  ]);
  const t = {
    resource_id: resourceID,
    component: input.component,
    expected_deployed_operation_id: input.expected_deployed_operation_id,
    expected_resource_generation: input.expected_resource_generation,
  };
  target(t);
  if (
    !id(input.request_id) ||
    !validImportFilename(input.filename) ||
    !positive(input.source_bytes, maxBytes)
  )
    return fail();
  const raw = await boundedDatabaseRequest(
    (s) =>
      request<unknown>(
        `/api/v1/resources/${encodeURIComponent(resourceID)}/database-import-upload-stages`,
        { method: "POST", body: input, signal: s, requestID: input.request_id },
      ),
    signal,
  );
  return parseDatabaseImportUploadStage(raw, t, {
    filename: input.filename,
    bytes: input.source_bytes,
  });
}
export async function getDatabaseImportUploadStage(
  stageID: string,
  t: DatabaseImportTarget,
  upload: DatabaseImportUploadSource & { upload_hash?: string },
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
  const out = parseDatabaseImportUploadStage(raw, t, upload);
  if (out.id !== stageID) return fail();
  return out;
}
// Sends one native File body. A failed/ambiguous transfer is resolved by stage GET,
// never by this transport retrying or reading the entire file into JavaScript.
export function sendDatabaseImportFile(
  stage: DatabaseImportUploadStage,
  file: File,
  signal: AbortSignal,
  onProgress: (sentBytes: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("Upload scope retired", "AbortError"));
      return;
    }
    if (
      !id(stage.id) ||
      !digest(stage.upload_hash) ||
      !validImportFile(file) ||
      file.name !== stage.upload_source.filename ||
      file.size !== stage.upload_source.bytes ||
      !["queued", "running"].includes(stage.state) ||
      !date(stage.upload_deadline)
    ) {
      reject(new Error("Upload admission does not match selected file"));
      return;
    }
    const remaining = Date.parse(stage.upload_deadline) - Date.now();
    if (remaining <= 0 || remaining > 3600_000) {
      reject(new Error("Upload deadline requires fresh admission"));
      return;
    }
    const xhr = new XMLHttpRequest();
    let settled = false;
    const finish = (cause?: unknown) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", abort);
      xhr.onreadystatechange = null;
      xhr.onload = null;
      xhr.onerror = null;
      xhr.ontimeout = null;
      xhr.onabort = null;
      xhr.upload.onprogress = null;
      if (cause) reject(cause);
      else resolve();
    };
    const abort = () => {
      xhr.abort();
      finish(new DOMException("File sending stopped", "AbortError"));
    };
    try {
      xhr.open(
        "PUT",
        `/api/v1/database-import-stages/${encodeURIComponent(stage.id)}/content`,
      );
      xhr.withCredentials = true;
      xhr.timeout = remaining;
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      xhr.setRequestHeader("Accept", "application/json");
      xhr.setRequestHeader("X-NeroCD-CSRF", "1");
      xhr.setRequestHeader("If-Match", `"${stage.upload_hash}"`);
      xhr.onreadystatechange = () => {
        if (xhr.readyState >= 2 && [401, 403].includes(xhr.status)) {
          const denied = new ApiError(
            xhr.status,
            "Upload access unavailable",
            xhr.getResponseHeader("X-Request-ID"),
          );
          finish(denied);
          xhr.abort();
        }
      };
      xhr.onload = () =>
        xhr.status === 202
          ? finish()
          : finish(
              new ApiError(
                xhr.status,
                "File transfer response requires stage status review",
                xhr.getResponseHeader("X-Request-ID"),
              ),
            );
      xhr.onerror = () =>
        finish(new Error("File transfer response unconfirmed"));
      xhr.ontimeout = () =>
        finish(new Error("File transfer timed out; inspect stage status"));
      xhr.onabort = () =>
        finish(new DOMException("File sending stopped", "AbortError"));
      xhr.upload.onprogress = (event) => {
        if (!settled && !signal.aborted)
          onProgress(Math.max(0, Math.min(file.size, event.loaded)));
      };
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) {
        abort();
        return;
      }
      xhr.send(file);
    } catch (cause) {
      finish(cause);
    }
  });
}
