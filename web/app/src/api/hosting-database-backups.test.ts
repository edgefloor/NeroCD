import { afterEach, expect, test, vi } from "vitest";
import { request } from "./client";
vi.mock("./client", () => ({ request: vi.fn() }));
afterEach(() => vi.resetAllMocks());
import {
  getDatabaseBackupComponents,
  parseDatabaseBackup,
  requestDatabaseBackup,
  restoreDatabaseBackup,
} from "./hosting-database-backups";
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

test("backup history retains the admitted remote target and rejects unbounded remote metadata", () => {
  const remote_target = { target_id: "storage", revision: 3 };
  expect(
    parseDatabaseBackup({ ...operation, remote_target }, scope, db)
      .remote_target,
  ).toEqual(remote_target);
  for (const invalid of [
    { target_id: "storage", revision: 0 },
    { target_id: "storage", revision: 3, object_key: "private" },
    { target_id: "foreign/path", revision: 3 },
  ])
    expect(() =>
      parseDatabaseBackup({ ...operation, remote_target: invalid }, scope, db),
    ).toThrow();
});
test("immediate admission must echo the exact selected remote revision", async () => {
  const remote_target = { target_id: "storage", revision: 3 };
  const input = {
    request_id: "backup_request",
    expected_resource_generation: 1,
    remote_target,
  };
  vi.mocked(request).mockResolvedValue({ ...operation, remote_target });
  expect(
    (
      await requestDatabaseBackup(
        scope,
        db,
        input,
        new AbortController().signal,
      )
    ).remote_target,
  ).toEqual(remote_target);
  vi.mocked(request).mockResolvedValue({
    ...operation,
    remote_target: { ...remote_target, revision: 4 },
  });
  await expect(
    requestDatabaseBackup(scope, db, input, new AbortController().signal),
  ).rejects.toThrow();
});
test("remote restore accepts a saved transfer reference and rejects arbitrary object inputs before sending", async () => {
  vi.mocked(request).mockResolvedValue({
    ...operation,
    id: "restore",
    mode: "restore",
    target_resource_id: "new_database",
  });
  const input = {
    request_id: "restore_request",
    name: "New database",
    remote_source: { transfer_id: "upload", expected_target_revision: 3 },
  };
  await restoreDatabaseBackup(
    scope,
    db,
    "backup",
    input,
    new AbortController().signal,
  );
  expect(vi.mocked(request).mock.calls[0]?.[1]).toMatchObject({
    body: input,
    requestID: "restore_request",
  });
  await expect(
    restoreDatabaseBackup(
      scope,
      db,
      "backup",
      {
        ...input,
        remote_source: {
          ...input.remote_source,
          url: "https://arbitrary.invalid",
        },
      } as typeof input,
      new AbortController().signal,
    ),
  ).rejects.toThrow();
  expect(request).toHaveBeenCalledTimes(1);
});


test("component backup response binds exact selected deployed operation and closed identity", async()=>{
 const component={...operation,version:"postgresql.compose-logical-local.v1",source_component:"postgresql",source_deployed_operation_id:"hop_current"};
 const input={request_id:"request",expected_resource_generation:1,component:"postgresql",expected_deployed_operation_id:"hop_current"};
 vi.mocked(request).mockResolvedValue(component);
 expect((await requestDatabaseBackup(scope,db,input,new AbortController().signal)).source_component).toBe("postgresql");
 vi.mocked(request).mockResolvedValue({...component,source_deployed_operation_id:"hop_other"});
 await expect(requestDatabaseBackup(scope,db,input,new AbortController().signal)).rejects.toThrow();
 expect(()=>parseDatabaseBackup({...operation,source_component:"postgresql"},scope,db)).toThrow();
});
test("component discovery rejects foreign resource and Docker identity fields",async()=>{
 const targets={resource_id:"db",resource_generation:1,operation_id:"hop_current",available:true,reason:"available",components:[{component:"postgresql",engine:"postgresql",major_version:16,running:true,eligible:true,reason:"requires_runtime_profile_verification"}]};
 vi.mocked(request).mockResolvedValue(targets);
 expect((await getDatabaseBackupComponents(scope,"db")).operation_id).toBe("hop_current");
 for(const value of [{...targets,resource_id:"foreign"},{...targets,components:[{...targets.components[0],container_id:"untrusted"}]}]){
  vi.mocked(request).mockResolvedValue(value);await expect(getDatabaseBackupComponents(scope,"db")).rejects.toThrow();
 }
});
