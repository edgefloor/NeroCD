import { expect, test } from "vitest";
import {
  parseRetentionPolicy,
  parseRetentionPreview,
} from "./hosting-storage-backup-retention";
import type { Resource } from "./hosting";
const scope = {
  actor: "owner",
  project: "project",
  resource: "resource",
  destination: "destination",
  binding: "binding",
};
const resource = { id: "resource", generation: 3 } as Resource;
const hash = `sha256:${"a".repeat(64)}`;
const at = "2026-10-26T12:00:00Z";
const policy = {
  version: "owned-upload.backup-retention.v1",
  project_id: "project",
  resource_id: "resource",
  revision: 1,
  resource_generation: 2,
  enabled: true,
  automatic: false,
  local: { keep_count: 7, keep_days: 0, max_bytes: 0 },
  remote: { keep_count: 0, keep_days: 3, max_bytes: 0 },
  created_at: at,
  updated_at: at,
  storage: { storage_id: "storage", generation: 1, hash },
  binding_id: "binding",
  destination_id: "destination",
};
test("policy survives ordinary application generation change and validates frozen placement", () => {
  expect(
    parseRetentionPolicy(policy, scope, resource).resource_generation,
  ).toBe(2);
  for (const update of [
    { destination_id: "foreign" },
    { binding_id: "foreign" },
    { resource_id: "foreign" },
    { path: "/private" },
    { object_key: "protected" },
  ])
    expect(() =>
      parseRetentionPolicy({ ...policy, ...update }, scope, resource),
    ).toThrow();
});
const preview = {
  id: "preview",
  version: policy.version,
  project_id: "project",
  resource_id: "resource",
  policy_revision: 1,
  plan_hash: hash,
  evaluated_at: at,
  local_reference_at: null,
  remote_reference_at: null,
  selection_truncated: false,
  created_at: at,
  items: [
    {
      copy_id: "copy",
      backup_id: "backup",
      location: "remote",
      digest: hash,
      bytes: 192,
      created_at: at,
      availability: "unproven",
      selected: false,
      protected_reasons: ["unproven_ownership"],
    },
  ],
};
test("fixed evaluation instant and unproven ownership are visible without private proofs", () => {
  expect(parseRetentionPreview(preview, scope, resource).evaluated_at).toBe(at);
  for (const extra of [
    { local_copy: { inode: 1 } },
    { object_key: "private" },
    { credential_ref: "secret" },
  ])
    expect(() =>
      parseRetentionPreview(
        { ...preview, items: [{ ...preview.items[0], ...extra }] },
        scope,
        resource,
      ),
    ).toThrow();
  expect(() =>
    parseRetentionPreview(
      { ...preview, evaluated_at: "invalid" },
      scope,
      resource,
    ),
  ).toThrow();
});
