import { expect, test } from "vitest";
import { parseDatabaseBackup } from "./hosting-database-backups";
import type { HostingDatabase } from "./hosting-databases";
const db = { resource: { id: "db" } } as HostingDatabase;
const scope = { actor: "owner", project: "project", environment: "env" };
const operation = {
  max_bytes: 1073741824,
  timeout_seconds: 900,
  id: "backup",
  backup_id: "backup",
  version: "postgresql.logical-local.v1",
  project_id: "project",
  source_resource_id: "db",
  source_generation: 1,
  mode: "backup",
  state: "succeeded",
  consistency: "logical_snapshot_source_writable",
  ownership_held: false,
  created_at: "2026-10-25T12:00:00Z",
  updated_at: "2026-10-25T12:00:00Z",
  blob: {
    digest: "sha256:" + "a".repeat(64),
    bytes: 65537,
    format: "postgresql_custom",
  },
};
test("parses closed safe history with immutable digest and size", () => {
  expect(parseDatabaseBackup(operation, scope, db)).toEqual(operation);
});
for (const [name, patch] of Object.entries({
  foreign: { source_resource_id: "foreign" },
  project: { project_id: "foreign" },
  credential: { password: "canary" },
  path: { path: "/private" },
  authority: { fence: "token" },
  status: { state: "success" },
  hold: { ownership_held: true },
  blob: { blob: { ...operation.blob, digest: "wrong" } },
  extraBlob: { blob: { ...operation.blob, path: "/private" } },
  size: { blob: { ...operation.blob, bytes: 1073741825 } },
  target: { target_resource_id: "source" },
}))
  test(`rejects ${name}`, () => {
    expect(() =>
      parseDatabaseBackup({ ...operation, ...patch }, scope, db),
    ).toThrow();
  });
