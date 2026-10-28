import { afterEach, expect, test, vi } from "vitest";
import {
  parseDatabaseBackupTarget,
  parseDatabaseBackupTransfer,
  listDatabaseBackupTargets,
  listDatabaseBackupValidations,
  listDatabaseBackupTransfers,
  saveDatabaseBackupTarget,
  validateDatabaseBackupTarget,
  retryDatabaseBackupUpload,
  validBackupEndpoint,
} from "./hosting-database-backup-targets";
import { request } from "./client";
vi.mock("./client", () => ({ request: vi.fn() }));
afterEach(() => vi.resetAllMocks());
const scope = { actor: "owner", project: "project", environment: "env" };
const time = "2026-10-25T12:00:00Z";
const target = {
  id: "storage",
  project_id: "project",
  version: "postgresql.backup-target.v1",
  revision: 1,
  name: "Daily archive",
  endpoint: "https://s3.example.invalid",
  bucket: "backups",
  region: "us-east-1",
  enabled: true,
  created_at: time,
  updated_at: time,
};
const transfer = {
  id: "transfer",
  version: "postgresql.logical-s3-transfer.v1",
  project_id: "project",
  target_id: "storage",
  target_revision: 1,
  destination_id: "runner",
  mode: "validate",
  state: "succeeded",
  failure_code: "",
  created_at: time,
  updated_at: time,
};
const blob = {
  digest: "sha256:" + "a".repeat(64),
  bytes: 65537,
  format: "postgresql_custom",
};
test("safe target is closed, project bound and contains no credential readback", () => {
  expect(parseDatabaseBackupTarget(target, scope)).toEqual(target);
  for (const patch of [
    { project_id: "foreign" },
    { revision: 0 },
    { version: "future" },
    { access_key: "canary" },
    { credentials: {} },
    { region: "" },
    { endpoint: "https://key:secret@s3.example.invalid" },
    { endpoint: "https://s3.example.invalid/private" },
  ])
    expect(() =>
      parseDatabaseBackupTarget({ ...target, ...patch }, scope),
    ).toThrow("Invalid remote backup metadata");
});
test("endpoint syntax leaves network authorization to server and rejects credential-bearing URLs", () => {
  expect(validBackupEndpoint("https://s3.example.invalid/")).toBe(true);
  expect(validBackupEndpoint("http://127.0.0.1:9000")).toBe(true);
  for (const value of [
    "ftp://s3.example.invalid",
    "https://s3.example.invalid?secret=x",
    "https://s3.example.invalid#x",
    "https://s3.example.invalid/prefix",
    "https://key:secret@s3.example.invalid",
    " https://s3.example.invalid",
  ])
    expect(validBackupEndpoint(value)).toBe(false);
});
test("transfer projection enforces modes, frozen identity and verified successful blob", () => {
  expect(parseDatabaseBackupTransfer(transfer, scope)).toEqual(transfer);
  const upload = { ...transfer, mode: "upload", backup_id: "backup", blob };
  expect(parseDatabaseBackupTransfer(upload, scope)).toEqual(upload);
  expect(
    parseDatabaseBackupTransfer(
      { ...upload, mode: "download", restore_operation_id: "restore" },
      scope,
    ).mode,
  ).toBe("download");
  for (const patch of [
    { project_id: "foreign" },
    { version: "future" },
    { object_key: "private" },
    { failure_code: "private_raw_error" },
    { target_revision: 0 },
    { blob },
    { backup_id: "backup" },
    { destination_id: "" },
  ])
    expect(() =>
      parseDatabaseBackupTransfer({ ...transfer, ...patch }, scope),
    ).toThrow();
  for (const state of ["queued", "failed", "succeeded"])
    expect(() =>
      parseDatabaseBackupTransfer({ ...upload, state, blob: undefined }, scope),
    ).toThrow();
  expect(() =>
    parseDatabaseBackupTransfer(
      { ...upload, blob: { ...blob, path: "/private" } },
      scope,
    ),
  ).toThrow();
  expect(() =>
    parseDatabaseBackupTransfer({ ...upload, mode: "download" }, scope),
  ).toThrow();
});
test("target lists reject duplicates, oversized lists and foreign metadata", async () => {
  for (const raw of [
    [target, target],
    Array(101).fill(target),
    [{ ...target, project_id: "foreign" }],
    { items: [] },
  ]) {
    vi.mocked(request).mockResolvedValueOnce(raw);
    await expect(listDatabaseBackupTargets(scope)).rejects.toThrow();
  }
});
test("save create requires keys and edit omission preserves existing credentials with exact replay body", async () => {
  const input = {
    request_id: "create",
    expected_revision: 0,
    name: target.name,
    endpoint: target.endpoint,
    bucket: target.bucket,
    region: target.region,
    enabled: true,
    credentials: { access_key: "key_canary", secret_key: "secret_canary" },
  };
  vi.mocked(request).mockResolvedValue(target);
  await saveDatabaseBackupTarget(
    scope,
    "",
    input,
    new AbortController().signal,
  );
  expect(vi.mocked(request).mock.calls[0]?.[1]).toMatchObject({
    method: "POST",
    body: input,
    requestID: "create",
  });
  await expect(
    saveDatabaseBackupTarget(
      scope,
      "",
      { ...input, credentials: undefined },
      new AbortController().signal,
    ),
  ).rejects.toThrow();
  vi.mocked(request).mockResolvedValue({ ...target, revision: 2 });
  const edit = {
    ...input,
    request_id: "edit",
    expected_revision: 1,
    credentials: undefined,
  };
  await saveDatabaseBackupTarget(
    scope,
    "storage",
    edit,
    new AbortController().signal,
  );
  expect(vi.mocked(request).mock.calls.at(-1)?.[1]).toMatchObject({
    method: "PUT",
    body: edit,
    requestID: "edit",
  });
  expect(
    JSON.stringify(
      await saveDatabaseBackupTarget(
        scope,
        "storage",
        edit,
        new AbortController().signal,
      ),
    ),
  ).not.toContain("canary");
});
test("validation request and response bind exact target revision and runner destination", async () => {
  const input = {
    request_id: "validate",
    expected_revision: 1,
    destination_id: "runner",
  };
  vi.mocked(request).mockResolvedValue(transfer);
  await validateDatabaseBackupTarget(
    scope,
    "storage",
    input,
    new AbortController().signal,
  );
  expect(vi.mocked(request).mock.calls[0]?.[0]).toBe(
    "/api/v1/projects/project/database-backup-targets/storage/validations",
  );
  vi.mocked(request).mockResolvedValue({
    ...transfer,
    destination_id: "foreign",
  });
  await expect(
    validateDatabaseBackupTarget(
      scope,
      "storage",
      input,
      new AbortController().signal,
    ),
  ).rejects.toThrow();
  vi.mocked(request).mockResolvedValue([{ ...transfer, target_id: "foreign" }]);
  await expect(
    listDatabaseBackupValidations(scope, "storage"),
  ).rejects.toThrow();
});
test("upload retry uses the existing backup and exact failed attempt fence", async () => {
  const upload = { ...transfer, mode: "upload", backup_id: "backup", blob };
  vi.mocked(request).mockResolvedValue(upload);
  const input = { request_id: "retry", expected_transfer_id: "failed" };
  await retryDatabaseBackupUpload(
    scope,
    "backup",
    input,
    new AbortController().signal,
  );
  expect(vi.mocked(request).mock.calls[0]?.[0]).toBe(
    "/api/v1/database-backups/backup/transfers/retry",
  );
  expect(vi.mocked(request).mock.calls[0]?.[1]).toMatchObject({
    method: "POST",
    body: input,
    requestID: "retry",
  });
  vi.mocked(request).mockResolvedValue([{ ...upload, backup_id: "foreign" }]);
  await expect(listDatabaseBackupTransfers(scope, "backup")).rejects.toThrow();
});


test("MySQL upload transfers bind their version to SQL format and exclude remote download", async () => {
 const mysql = { ...transfer, version: "mysql.logical-s3-transfer.v1", mode: "upload", backup_id: "backup", blob: { ...blob, format: "mysql_sql" } };
 expect(parseDatabaseBackupTransfer(mysql, scope)).toEqual(mysql);
 for (const invalid of [{ ...mysql, blob }, { ...mysql, version: transfer.version }, { ...mysql, mode: "validate", backup_id: undefined, blob: undefined }, { ...mysql, mode: "download", restore_operation_id: "restore" }]) expect(() => parseDatabaseBackupTransfer(invalid, scope)).toThrow();
 vi.mocked(request).mockResolvedValue(mysql);
 const input = { request_id: "stable", expected_transfer_id: "previous" };
 expect((await retryDatabaseBackupUpload(scope, "backup", input, new AbortController().signal)).blob).toEqual(mysql.blob);
 expect(vi.mocked(request).mock.calls.at(-1)![1]).toMatchObject({ method: "POST", body: input });
});
