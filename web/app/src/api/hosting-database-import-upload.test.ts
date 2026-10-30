import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { request } from "./client";
import { ApiError } from "./errors";
import {
  admitDatabaseImportUpload,
  getDatabaseImportUploadStage,
  parseDatabaseImportStage,
  parseDatabaseImportUploadStage,
  sendDatabaseImportFile,
  validImportFile,
  validImportFilename,
  type DatabaseImportUploadStage,
} from "./hosting-database-imports";
vi.mock("./client", () => ({ request: vi.fn() }));
const target = {
  resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
};
const file = new File(["SELECT 1;"], "proof.sql", {
  type: "application/octet-stream",
});
const created = new Date().toISOString();
const stage: DatabaseImportUploadStage = {
  id: "upload_stage",
  version: "mysql.compose-local-upload-stage.v1",
  target_resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
  upload_source: { filename: file.name, bytes: file.size },
  upload_hash: "sha256:" + "a".repeat(64),
  upload_deadline: new Date(Date.parse(created) + 3600000).toISOString(),
  state: "queued",
  created_at: created,
  updated_at: created,
};
const ready = {
  ...stage,
  state: "ready" as const,
  source_bytes: file.size,
  compression: "plain" as const,
  staged_blob: {
    digest: "sha256:" + "b".repeat(64),
    bytes: file.size,
    format: "mysql_sql" as const,
  },
  stage_hash: "sha256:" + "c".repeat(64),
  expires_at: new Date(Date.parse(created) + 3600000).toISOString(),
};
const input = {
  request_id: "admit",
  component: "mysql" as const,
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
  filename: file.name,
  source_bytes: file.size,
};
class FakeXHR {
  static instances: FakeXHR[] = [];
  headers: Record<string, string> = {};
  method = "";
  url = "";
  withCredentials = false;
  timeout = 0;
  status = 0;
  readyState = 0;
  body: unknown;
  sent = 0;
  aborted = false;
  upload: { onprogress: ((event: { loaded: number }) => void) | null } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  onabort: (() => void) | null = null;
  onreadystatechange: (() => void) | null = null;
  constructor() {
    FakeXHR.instances.push(this);
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(key: string, value: string) {
    this.headers[key] = value;
  }
  getResponseHeader() {
    return "request_response";
  }
  send(body: unknown) {
    this.body = body;
    this.sent++;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
}
beforeEach(() => {
  FakeXHR.instances = [];
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
});
afterEach(() => {
  vi.resetAllMocks();
  vi.unstubAllGlobals();
});
test("local admission is closed, target-bound metadata and returns a stable stage before bytes", async () => {
  vi.mocked(request).mockResolvedValue(stage);
  expect(
    await admitDatabaseImportUpload(
      "target",
      input,
      new AbortController().signal,
    ),
  ).toEqual(stage);
  expect(request).toHaveBeenCalledWith(
    "/api/v1/resources/target/database-import-upload-stages",
    expect.objectContaining({
      method: "POST",
      body: input,
      requestID: "admit",
    }),
  );
  expect(FakeXHR.instances).toHaveLength(0);
});
test("local receipt contains no fabricated S3 identity and exact filename/size", () => {
  expect(
    parseDatabaseImportUploadStage(ready, target, {
      filename: file.name,
      bytes: file.size,
    }),
  ).toEqual(ready);
  expect(() => parseDatabaseImportStage(ready, target)).toThrow();
});
for (const patch of [
  { s3_source: { target_id: "storage", revision: 1, object_key: "x" } },
  { source_identity: { etag: "fake" } },
  { upload_source: { filename: "../x.sql", bytes: 1 } },
  { upload_source: { filename: ["proof.sql"], bytes: 1 } },
  { upload_source: { filename: "proof.sql", bytes: 0 } },
  { upload_source: { filename: "proof.sql", bytes: 10737418241 } },
  { upload_hash: [stage.upload_hash] },
  { upload_deadline: "never" },
  { upload_deadline: created },
  { upload_deadline: new Date(Date.parse(created) + 3600001).toISOString() },
  { version: "mysql.compose-external-s3-stage.v1" },
  { target_resource_id: "foreign" },
  { expected_deployed_operation_id: "stale" },
  { state: ["ready"] },
  { compression: ["gzip"] },
  { failure_code: ["interrupted"] },
  { source_bytes: 1 },
])
  test(`rejects unsafe upload receipt ${JSON.stringify(patch)}`, () =>
    expect(() =>
      parseDatabaseImportUploadStage({ ...ready, ...patch }, target),
    ).toThrow());
test("filenames are display data bounded in UTF-8 bytes, and content type is not inferred from extension", () => {
  for (const name of ["proof.sql", "gzip.data", "é".repeat(127), " space.sql"])
    expect(validImportFilename(name)).toBe(true);
  for (const name of [
    "",
    "é".repeat(128),
    "a".repeat(256),
    "a/b.sql",
    "a\\b.sql",
    "a\n.sql",
    "\ud800",
  ])
    expect(validImportFilename(name)).toBe(false);
  expect(validImportFile({ name: "empty.sql", size: 0 })).toBe(false);
  expect(validImportFile({ name: "limit.data", size: 10737418240 })).toBe(true);
  expect(validImportFile({ name: "limit.data", size: 10737418241 })).toBe(
    false,
  );
});
test("stage GET pins upload hash as well as target and file metadata", async () => {
  vi.mocked(request).mockResolvedValue({
    ...stage,
    upload_hash: "sha256:" + "d".repeat(64),
  });
  await expect(
    getDatabaseImportUploadStage(
      stage.id,
      target,
      { ...stage.upload_source, upload_hash: stage.upload_hash },
      new AbortController().signal,
    ),
  ).rejects.toThrow();
});
test("native transfer sends the original File once with strong If-Match, session CSRF and sending-only progress", async () => {
  const buffered = vi.fn();
  Object.defineProperty(file, "arrayBuffer", {
    value: buffered,
    configurable: true,
  });
  const progress = vi.fn();
  const result = sendDatabaseImportFile(
    stage,
    file,
    new AbortController().signal,
    progress,
  );
  const xhr = FakeXHR.instances[0]!;
  expect(xhr.method).toBe("PUT");
  expect(xhr.url).toBe("/api/v1/database-import-stages/upload_stage/content");
  expect(xhr.withCredentials).toBe(true);
  expect(xhr.headers).toEqual({
    "Content-Type": "application/octet-stream",
    Accept: "application/json",
    "X-NeroCD-CSRF": "1",
    "If-Match": `"${stage.upload_hash}"`,
  });
  expect(xhr.body).toBe(file);
  expect(buffered).not.toHaveBeenCalled();
  expect(xhr.timeout).toBeGreaterThan(0);
  expect(xhr.timeout).toBeLessThanOrEqual(3600000);
  xhr.upload.onprogress?.({ loaded: 4 });
  expect(progress).toHaveBeenLastCalledWith(4);
  xhr.status = 202;
  xhr.onload?.();
  await result;
  expect(xhr.sent).toBe(1);
});
test.each([409, 412, 500])(
  "HTTP %s rejects without resending the File",
  async (status) => {
    const result = sendDatabaseImportFile(
      stage,
      file,
      new AbortController().signal,
      vi.fn(),
    );
    const xhr = FakeXHR.instances[0]!;
    xhr.status = status;
    xhr.onload?.();
    await expect(result).rejects.toMatchObject({ status });
    expect(xhr.sent).toBe(1);
    expect(FakeXHR.instances).toHaveLength(1);
  },
);
test.each([401, 403])(
  "HTTP %s headers retire access without waiting for response body",
  async (status) => {
    const result = sendDatabaseImportFile(
      stage,
      file,
      new AbortController().signal,
      vi.fn(),
    );
    const xhr = FakeXHR.instances[0]!;
    xhr.status = status;
    xhr.readyState = 2;
    xhr.onreadystatechange?.();
    await expect(result).rejects.toBeInstanceOf(ApiError);
    expect(xhr.aborted).toBe(true);
    expect(xhr.onload).toBeNull();
  },
);
test("ambiguous network failure and late callback never retry the content", async () => {
  const result = sendDatabaseImportFile(
    stage,
    file,
    new AbortController().signal,
    vi.fn(),
  );
  const xhr = FakeXHR.instances[0]!;
  const late = xhr.onerror;
  xhr.onerror?.();
  await expect(result).rejects.toThrow(/unconfirmed/);
  late?.();
  expect(FakeXHR.instances).toHaveLength(1);
  expect(xhr.sent).toBe(1);
});
test("scope cancellation aborts the owned XHR", async () => {
  const controller = new AbortController();
  const result = sendDatabaseImportFile(
    stage,
    file,
    controller.signal,
    vi.fn(),
  );
  controller.abort();
  await expect(result).rejects.toMatchObject({ name: "AbortError" });
  expect(FakeXHR.instances[0]!.aborted).toBe(true);
});
test("expired or mismatched admission never starts a body request", async () => {
  await expect(
    sendDatabaseImportFile(
      { ...stage, upload_deadline: created },
      file,
      new AbortController().signal,
      vi.fn(),
    ),
  ).rejects.toThrow();
  await expect(
    sendDatabaseImportFile(
      stage,
      new File(["different"], "changed.sql"),
      new AbortController().signal,
      vi.fn(),
    ),
  ).rejects.toThrow();
  expect(FakeXHR.instances).toHaveLength(0);
});
