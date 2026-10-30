import { afterEach, expect, test, vi } from "vitest";
import {
  databaseBackupDownloadPath,
  getDatabaseBackupDownloadOptions,
} from "./hosting-database-backup-download";
import type { DatabaseBackup } from "./hosting-database-backups";
const scope = {
  actor: "u",
  project: "p",
  environment: "env",
  resource: "r",
  destination: "d",
  binding: "b",
};
const blob = {
  digest: `sha256:${"a".repeat(64)}`,
  bytes: 12,
  format: "postgresql_custom" as const,
};
const backup = {
  id: "backup",
  backup_id: "backup",
  project_id: "p",
  source_resource_id: "r",
  version: "postgresql.logical-local.v1",
  mode: "backup",
  state: "succeeded",
  blob,
} as DatabaseBackup;
const options = { available: true, reason_code: "", backup_id: "backup", blob };
afterEach(() => vi.unstubAllGlobals());
test.each([
  { ...options, backup_id: "other" },
  { ...options, reason_code: "disabled" },
  { ...options, blob: undefined },
  { ...options, available: false },
  { ...options, available: "yes" },
  { ...options, extra: "secret" },
  { ...options, blob: { ...blob, path: "/private" } },
  { ...options, blob: { ...blob, extra: true } },
  { ...options, blob: { ...blob, digest: `sha256:${"b".repeat(64)}` } },
  { ...options, blob: { ...blob, bytes: 13 } },
  { ...options, blob: { ...blob, format: "mysql_sql" } },
  { ...options, blob: { ...blob, path: "/private" } },
  null,
])("rejects malformed or mismatched availability %j", async (value) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(value))),
  );
  await expect(
    getDatabaseBackupDownloadOptions(scope, backup),
  ).rejects.toThrow();
});
test("rejects foreign scope before network", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    getDatabaseBackupDownloadOptions({ ...scope, project: "foreign" }, backup),
  ).rejects.toThrow("scope mismatch");
  expect(fetch).not.toHaveBeenCalled();
});
test.each(["../x", "bad/id", "", "a?secret=x"])(
  "rejects unsafe archive identity %s",
  (id) => {
    expect(() => databaseBackupDownloadPath(id)).toThrow();
  },
);

test("actor is a client identity cache key, not a persisted identifier", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(options)));
  vi.stubGlobal("fetch", fetch);
  await expect(
    getDatabaseBackupDownloadOptions(
      {
        ...scope,
        actor: JSON.stringify([
          "local",
          "00000000-0000-4000-8000-000000000001",
        ]),
      },
      backup,
    ),
  ).resolves.toEqual(options);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/database-backups/backup/archive-options",
    expect.objectContaining({ cache: "no-store", credentials: "include" }),
  ]);
});

test.each([
  "actor",
  "project",
  "environment",
  "resource",
  "destination",
  "binding",
])("rejects empty %s scope before network", async (field) => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    getDatabaseBackupDownloadOptions({ ...scope, [field]: "" }, backup),
  ).rejects.toThrow("scope mismatch");
  expect(fetch).not.toHaveBeenCalled();
});

test.each([
  "postgresql.logical-local.v1",
  "mysql.logical-local.v1",
  "postgresql.compose-logical-local.v1",
  "mysql.compose-logical-local.v1",
] as const)("matches exact saved %s artifact", async (version) => {
  const actual = {
    ...backup,
    version,
    blob: {
      ...blob,
      format: version.startsWith("mysql.")
        ? ("mysql_sql" as const)
        : ("postgresql_custom" as const),
    },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ ...options, blob: actual.blob })),
    ),
  );
  await expect(
    getDatabaseBackupDownloadOptions(scope, actual),
  ).resolves.toMatchObject({ available: true, blob: actual.blob });
});
test.each([
  "disabled",
  "backup_not_succeeded",
  "local_copy_unavailable",
  "copy_busy",
  "runner_unavailable",
  "placement_changed",
])(
  "accepts closed unavailable reason %s despite saved remote target",
  async (reason) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              backup_id: "backup",
              available: false,
              reason_code: reason,
            }),
          ),
      ),
    );
    await expect(
      getDatabaseBackupDownloadOptions(scope, {
        ...backup,
        remote_target: { target_id: "remote", revision: 1 },
      }),
    ).resolves.toMatchObject({ available: false, reason_code: reason });
  },
);
test.each([null, ["disabled"], {}, "unknown", ""])(
  "rejects malformed unavailable reason %j",
  async (reason) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              backup_id: "backup",
              available: false,
              reason_code: reason,
            }),
          ),
      ),
    );
    await expect(
      getDatabaseBackupDownloadOptions(scope, backup),
    ).rejects.toThrow();
  },
);
test.each([
  { mode: "restore" },
  { backup_id: "other" },
  { version: ["mysql.logical-local.v1"] },
  { version: "redis.local.v1" },
])("rejects ineligible operation before options fetch %j", async (change) => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    getDatabaseBackupDownloadOptions(scope, {
      ...backup,
      ...change,
    } as DatabaseBackup),
  ).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
