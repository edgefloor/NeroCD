import { afterEach, expect, test, vi } from "vitest";
import type { HostingDatabase } from "./hosting-databases";
import {
  createRetentionPreview,
  executeRetention,
  getRetentionExecution,
  getRetentionPolicy,
  listRetentionExecutions,
  parseRetentionExecution,
  parseRetentionPolicy,
  parseRetentionPreview,
  retentionBytesToGiB,
  retentionGiBToBytes,
  retentionVersion,
  saveRetentionPolicy,
} from "./hosting-database-backup-retention";
const scope = {
  actor: "owner",
  project: "project",
  environment: "environment",
};
const db = { resource: { id: "database", generation: 2 } } as HostingDatabase;
const at = "2026-10-25T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const policy = {
  version: retentionVersion,
  project_id: "project",
  resource_id: "database",
  revision: 1,
  resource_generation: 2,
  enabled: true,
  automatic: false,
  local: { keep_count: 0, keep_days: 36500, max_bytes: 2 ** 50 },
  remote: { keep_count: 10000, keep_days: 0, max_bytes: 0 },
  created_at: at,
  updated_at: at,
};
const copy = {
  copy_id: "local_copy",
  backup_id: "backup",
  location: "local",
  digest: hash,
  bytes: 123,
  created_at: at,
  availability: "available",
  selected: true,
  protected_reasons: [],
};
const preview = {
  id: "preview",
  version: retentionVersion,
  project_id: "project",
  resource_id: "database",
  policy_revision: 1,
  plan_hash: hash,
  local_reference_at: at,
  remote_reference_at: null,
  selection_truncated: false,
  items: [copy],
  created_at: at,
};
const execution = {
  id: "execution",
  version: retentionVersion,
  project_id: "project",
  resource_id: "database",
  preview_id: "preview",
  policy_revision: 1,
  plan_hash: hash,
  state: "uncertain",
  reservation_held: true,
  local_state: "succeeded",
  remote_state: "uncertain",
  items: [
    {
      copy_id: "remote_copy",
      backup_id: "backup",
      location: "remote",
      state: "uncertain",
      failure_code: "interrupted",
      reservation_held: true,
    },
  ],
  created_at: at,
  updated_at: at,
};
const request_id = "a".repeat(32);
const respond = (value: unknown) => new Response(JSON.stringify(value));
afterEach(() => vi.unstubAllGlobals());
test("closed views preserve zero and upper bounds, protection and independent uncertain results", () => {
  expect(parseRetentionPolicy(policy, scope, db)).toEqual(policy);
  expect(parseRetentionPreview(preview, scope, db)).toEqual(preview);
  expect(parseRetentionExecution(execution, scope, db)).toEqual(execution);
  expect(
    parseRetentionPreview(
      {
        ...preview,
        items: [{ ...copy, selected: false, protected_reasons: ["newest"] }],
      },
      scope,
      db,
    ).items[0].selected,
  ).toBe(false);
});
for (const [name, patch] of Object.entries({
  project: { project_id: "other" },
  resource: { resource_id: "other" },
  version: { version: "unknown" },
  extra: { path: "/secret" },
  revision: { revision: 0 },
  unsafe: { local: { ...policy.local, max_bytes: 2 ** 50 + 1 } },
  count: { remote: { ...policy.remote, keep_count: 10001 } },
  days: { remote: { ...policy.remote, keep_days: -1 } },
  fractional: { remote: { ...policy.remote, keep_count: 0.5 } },
  nested: { local: { ...policy.local, key: "secret" } },
})) {
  test(`policy rejects ${name}`, () =>
    expect(() =>
      parseRetentionPolicy({ ...policy, ...patch }, scope, db),
    ).toThrow("Invalid database backup retention metadata"));
}
for (const [name, patch] of Object.entries({
  protected: { selected: true, protected_reasons: ["newest"] },
  uncertain: { availability: "uncertain" },
  unknownReason: { selected: false, protected_reasons: ["private"] },
  extra: { object_key: "private" },
  digest: { digest: "private" },
  unsafe: { bytes: Number.MAX_SAFE_INTEGER + 1 },
  location: { location: "elsewhere" },
})) {
  test(`preview rejects ${name}`, () =>
    expect(() =>
      parseRetentionPreview(
        { ...preview, items: [{ ...copy, ...patch }] },
        scope,
        db,
      ),
    ).toThrow());
}
test("preview and execution reject duplicate, oversized and foreign inventories", () => {
  for (const items of [
    [copy, copy],
    Array.from({ length: 101 }, (_, i) => ({ ...copy, copy_id: `copy_${i}` })),
    Array.from({ length: 1001 }, (_, i) => ({
      ...copy,
      copy_id: `copy_${i}`,
      selected: false,
    })),
  ])
    expect(() =>
      parseRetentionPreview({ ...preview, items }, scope, db),
    ).toThrow();
  expect(() =>
    parseRetentionPreview({ ...preview, project_id: "other" }, scope, db),
  ).toThrow();
  expect(() =>
    parseRetentionExecution(
      { ...execution, items: [execution.items[0], execution.items[0]] },
      scope,
      db,
    ),
  ).toThrow();
  expect(() =>
    parseRetentionExecution({ ...execution, resource_id: "other" }, scope, db),
  ).toThrow();
  expect(() =>
    parseRetentionExecution(
      {
        ...execution,
        items: [{ ...execution.items[0], failure_code: "private" }],
      },
      scope,
      db,
    ),
  ).toThrow();
});
test("GiB conversion is exact, bounded and round-trips every representative byte value", () => {
  for (const bytes of [0, 1, 123, 1073741823, 1073741824, 2 ** 50 - 1, 2 ** 50])
    expect(retentionGiBToBytes(retentionBytesToGiB(bytes))).toBe(bytes);
  expect(retentionGiBToBytes("0.5")).toBe(536870912);
  for (const value of [
    "",
    "-1",
    "1e3",
    " 1",
    "0.1",
    "1048576.5",
    "9007199254740992",
    "0.0000000001",
    "Infinity",
  ])
    expect(retentionGiBToBytes(value)).toBeUndefined();
});
test("requests use exact scoped routes, no-store, CSRF, abort and caller request identity", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(respond(null))
    .mockResolvedValueOnce(respond(policy))
    .mockResolvedValueOnce(respond(preview))
    .mockResolvedValueOnce(respond(execution))
    .mockResolvedValueOnce(respond([execution]))
    .mockResolvedValueOnce(respond(execution));
  vi.stubGlobal("fetch", fetch);
  expect(await getRetentionPolicy(scope, db)).toBeNull();
  await saveRetentionPolicy(scope, db, {
    request_id,
    expected_revision: 0,
    expected_resource_generation: 2,
    enabled: true,
    automatic: false,
    local: policy.local,
    remote: policy.remote,
  });
  await createRetentionPreview(scope, db, {
    request_id,
    expected_policy_revision: 1,
  });
  await executeRetention(scope, db, {
    request_id,
    expected_policy_revision: 1,
    preview_id: "preview",
    expected_plan_hash: hash,
  });
  await listRetentionExecutions(scope, db);
  await getRetentionExecution(scope, db, "execution");
  expect(fetch.mock.calls.map((c) => c[0])).toEqual(
    [
      "",
      "",
      "/previews",
      "/executions",
      "/executions",
      "/executions/execution",
    ].map((v) => `/api/v1/resources/database/database-backup-retention${v}`),
  );
  expect(fetch.mock.calls[1][1]).toMatchObject({
    method: "PUT",
    cache: "no-store",
    headers: { "X-NeroCD-CSRF": "1", "X-Request-ID": request_id },
    credentials: "include",
    signal: expect.any(AbortSignal),
  });
});
test("response correlation fails closed and oversized or duplicate history is rejected", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(respond({ ...preview, policy_revision: 2 }))
    .mockResolvedValueOnce(respond({ ...execution, preview_id: "other" }))
    .mockResolvedValueOnce(respond([execution, execution]))
    .mockResolvedValueOnce(respond({ ...execution, id: "other" }));
  vi.stubGlobal("fetch", fetch);
  await expect(
    createRetentionPreview(scope, db, {
      request_id,
      expected_policy_revision: 1,
    }),
  ).rejects.toThrow();
  await expect(
    executeRetention(scope, db, {
      request_id,
      expected_policy_revision: 1,
      preview_id: "preview",
      expected_plan_hash: hash,
    }),
  ).rejects.toThrow();
  await expect(listRetentionExecutions(scope, db)).rejects.toThrow();
  await expect(getRetentionExecution(scope, db, "execution")).rejects.toThrow();
});
test("retired requests abort without accepting late responses", async () => {
  const controller = new AbortController();
  let signal: AbortSignal | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn((_path, options) => {
      signal = options.signal;
      return new Promise(() => {});
    }),
  );
  const pending = getRetentionPolicy(scope, db, controller.signal);
  controller.abort();
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  expect(signal?.aborted).toBe(true);
});
