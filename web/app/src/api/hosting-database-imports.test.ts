import { afterEach, expect, test, vi } from "vitest";
import { request } from "./client";
import {
  getDatabaseImport,
  getDatabaseImportStage,
  inspectDatabaseImport,
  parseDatabaseImportOperation,
  parseDatabaseImportStage,
  requestDatabaseImport,
  validImportObjectKey,
} from "./hosting-database-imports";
vi.mock("./client", () => ({ request: vi.fn() }));
afterEach(() => vi.resetAllMocks());
const target = {
  resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
};
const s3 = {
  target_id: "storage",
  revision: 2,
  object_key: "/imports/é file%2F.sql.gz",
};
const stage = {
  id: "stage",
  version: "mysql.compose-external-s3-stage.v1",
  target_resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
  s3_source: s3,
  state: "ready",
  source_bytes: 42,
  compression: "gzip",
  staged_blob: {
    digest: "sha256:" + "a".repeat(64),
    bytes: 100,
    format: "mysql_sql",
  },
  stage_hash: "sha256:" + "b".repeat(64),
  expires_at: "2099-01-01T01:00:00Z",
  created_at: "2099-01-01T00:00:00Z",
  updated_at: "2099-01-01T00:00:00Z",
};
const input = {
  request_id: "import_request",
  stage_id: "stage",
  expected_stage_hash: stage.stage_hash,
  component_target: { ...target, acknowledge_replacement: true as const },
};
const operation = {
  id: "import",
  version: "mysql.compose-external-import.v1",
  stage_id: "stage",
  target_resource_id: "target",
  target_component: "mysql",
  target_deployed_operation_id: "deployment",
  state: "queued",
  ownership_held: true,
  writers_resumed: false,
  created_at: stage.created_at,
  updated_at: stage.updated_at,
};
test("ready receipt preserves literal object key and does not require provider identity", () => {
  expect(parseDatabaseImportStage(stage, target, s3)).toEqual(stage);
  expect(
    parseDatabaseImportStage(
      { ...stage, source_identity: { etag: '"opaque"' } },
      target,
      s3,
    ).source_identity,
  ).toEqual({ etag: '"opaque"' });
});
for (const [name, patch] of Object.entries({
  foreign: { target_resource_id: "other" },
  engine: { component: "postgresql" },
  deployment: { expected_deployed_operation_id: "other" },
  generation: { expected_resource_generation: 4 },
  version: { version: "mysql.logical-local.v1" },
  credentials: { password: "canary" },
  state: { state: "succeeded" },
  source: { s3_source: { ...s3, revision: 3 } },
  sourceSize: { source_bytes: 10737418241 },
  emptySize: { source_bytes: 0 },
  format: { compression: "zip" },
  blob: { staged_blob: { ...stage.staged_blob, format: "postgresql_custom" } },
  blobPath: { staged_blob: { ...stage.staged_blob, path: "/private" } },
  blobSize: { staged_blob: { ...stage.staged_blob, bytes: 10737418241 } },
  hash: { stage_hash: "invalid" },
  expiry: { expires_at: "never" },
  timestamp: { created_at: "never" },
  provider: { source_identity: {} },
  providerPath: { source_identity: { etag: "ok", path: "/private" } },
  failure: { failure_code: "arbitrary" },
}))
  test(`rejects mismatched or unbounded inspection ${name}`, () =>
    expect(() =>
      parseDatabaseImportStage({ ...stage, ...patch }, target, s3),
    ).toThrow());
for (const field of [
  "source_bytes",
  "compression",
  "staged_blob",
  "stage_hash",
  "expires_at",
])
  test(`ready requires ${field}`, () => {
    const incomplete = { ...stage } as Record<string, unknown>;
    delete incomplete[field];
    expect(() => parseDatabaseImportStage(incomplete, target, s3)).toThrow();
  });
test("key validation bounds exact UTF-8 bytes without normalizing slash, spaces or URL-like keys", () => {
  for (const key of [
    s3.object_key,
    "https://literal-object-key",
    "é".repeat(512),
    "a".repeat(1024),
  ])
    expect(validImportObjectKey(key)).toBe(true);
  for (const key of [
    "",
    "é".repeat(513),
    "a".repeat(1025),
    "bad\nkey",
    "bad\u0000key",
    "\ud800",
  ])
    expect(validImportObjectKey(key)).toBe(false);
});
test("inspection submits exact target and object authority with durable request identity", async () => {
  vi.mocked(request).mockResolvedValue(stage);
  const stageInput = {
    request_id: "inspect_request",
    component: "mysql" as const,
    expected_deployed_operation_id: "deployment",
    expected_resource_generation: 3,
    s3_source: s3,
  };
  await inspectDatabaseImport(
    "target",
    stageInput,
    new AbortController().signal,
  );
  expect(request).toHaveBeenCalledWith(
    "/api/v1/resources/target/database-import-stages",
    expect.objectContaining({
      method: "POST",
      body: stageInput,
      requestID: "inspect_request",
    }),
  );
});
test("inspection polling rejects another stage even at the same target", async () => {
  vi.mocked(request).mockResolvedValue({ ...stage, id: "other" });
  await expect(
    getDatabaseImportStage("stage", target, s3, new AbortController().signal),
  ).rejects.toThrow();
});
test("import submits reviewed immutable stage and separate target, then polls exact operation", async () => {
  vi.mocked(request).mockResolvedValue(operation);
  expect(
    await requestDatabaseImport(input, new AbortController().signal),
  ).toEqual(operation);
  expect(request).toHaveBeenCalledWith(
    "/api/v1/resources/target/database-imports",
    expect.objectContaining({
      method: "POST",
      body: input,
      requestID: "import_request",
    }),
  );
  await getDatabaseImport("import", input, new AbortController().signal);
  expect(request).toHaveBeenLastCalledWith(
    "/api/v1/database-imports/import",
    expect.objectContaining({ cache: "no-store" }),
  );
});
for (const patch of [
  { stage_id: "other" },
  { target_resource_id: "other" },
  { target_component: "postgresql" },
  { target_deployed_operation_id: "other" },
  { state: "succeeded", ownership_held: true, writers_resumed: true },
  { state: "succeeded", ownership_held: false, writers_resumed: false },
  { state: "uncertain", ownership_held: false },
  { private_path: "/private" },
])
  test(`rejects invalid import outcome ${JSON.stringify(patch)}`, () =>
    expect(() =>
      parseDatabaseImportOperation({ ...operation, ...patch }, input),
    ).toThrow());
test("invalid local input does not issue an import", async () => {
  await expect(
    requestDatabaseImport(
      {
        ...input,
        component_target: {
          ...input.component_target,
          acknowledge_replacement: false as unknown as true,
        },
      },
      new AbortController().signal,
    ),
  ).rejects.toThrow();
  expect(request).not.toHaveBeenCalled();
});

for (const value of [
  ["ready"],
  [["ready"]],
  { toString: () => "ready" },
  new String("ready"),
]) {
  test(`rejects coerced inspection state ${JSON.stringify(value)}`, () => {
    const {
      source_bytes: _source,
      compression: _compression,
      staged_blob: _blob,
      stage_hash: _hash,
      expires_at: _expiry,
      ...incomplete
    } = stage;
    expect(() =>
      parseDatabaseImportStage({ ...incomplete, state: value }, target, s3),
    ).toThrow();
  });
}
for (const state of ["uncertain", "succeeded", "running"]) {
  for (const value of [[state], { toString: () => state }, new String(state)]) {
    test(`rejects coerced import state ${state} ${JSON.stringify(value)}`, () => {
      expect(() =>
        parseDatabaseImportOperation(
          {
            ...operation,
            state: value,
            ownership_held: false,
            writers_resumed: false,
          },
          input,
        ),
      ).toThrow();
    });
  }
}
for (const value of [
  ["gzip"],
  { toString: () => "plain" },
  new String("gzip"),
]) {
  test(`rejects coerced inspection compression ${JSON.stringify(value)}`, () => {
    expect(() =>
      parseDatabaseImportStage({ ...stage, compression: value }, target, s3),
    ).toThrow();
  });
}
for (const value of [
  ["execution_failed"],
  { toString: () => "interrupted" },
  new String("execution_failed"),
]) {
  test(`rejects coerced failure code ${JSON.stringify(value)}`, () => {
    expect(() =>
      parseDatabaseImportStage({ ...stage, failure_code: value }, target, s3),
    ).toThrow();
    expect(() =>
      parseDatabaseImportOperation(
        { ...operation, failure_code: value },
        input,
      ),
    ).toThrow();
  });
}
test("strict literal states preserve ready and uncertain invariants", () => {
  expect(parseDatabaseImportStage(stage, target, s3).state).toBe("ready");
  expect(
    parseDatabaseImportOperation(
      { ...operation, state: "uncertain", ownership_held: true },
      input,
    ).state,
  ).toBe("uncertain");
  expect(() =>
    parseDatabaseImportOperation(
      { ...operation, state: "uncertain", ownership_held: false },
      input,
    ),
  ).toThrow();
});
