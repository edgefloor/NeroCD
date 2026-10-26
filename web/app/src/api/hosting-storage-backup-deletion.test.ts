import { expect, test } from "vitest";
import {
  parseDeletionPreview,
  parseDeletionExecution,
  deletionVersion,
} from "./hosting-storage-backup-deletion";
import type { Resource } from "./hosting";
const scope = {
    actor: "owner",
    project: "project",
    resource: "resource",
    destination: "destination",
    binding: "binding",
  },
  resource = {
    id: "resource",
    project_id: "project",
    generation: 2,
  } as Resource;
const at = "2026-10-26T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const item = {
  copy_id: "copy",
  backup_id: "backup",
  location: "local",
  digest: hash,
  bytes: 100,
  created_at: at,
  availability: "available",
  selected: true,
  protected_reasons: [],
};
const preview = {
  id: "preview",
  version: deletionVersion,
  origin: "manual_selection",
  project_id: "project",
  resource_id: "resource",
  backup_id: "backup",
  resource_generation: 2,
  policy_revision: 0,
  plan_hash: hash,
  final_copy: true,
  evaluated_at: at,
  created_at: at,
  local_reference_at: null,
  remote_reference_at: null,
  selection_truncated: false,
  items: [item],
};
test("manual preview validates version, scope and explicit safe selection", () => {
  expect(
    parseDeletionPreview(preview, scope, resource, "backup").final_copy,
  ).toBe(true);
  for (const patch of [
    { origin: "policy" },
    { backup_id: "foreign" },
    { version: "owned-upload.backup-retention.v1" },
    { policy_revision: 1 },
    { items: [{ ...item, selected: false }] },
    { items: [{ ...item, backup_id: "foreign" }] },
    { items: [{ ...item, protected_reasons: ["shared"] }] },
    { unexpected: "target" },
  ])
    expect(() =>
      parseDeletionPreview({ ...preview, ...patch }, scope, resource, "backup"),
    ).toThrow();
});
test("manual execution validates backup identity for each progress row", () => {
  const execution = {
    id: "execution",
    version: deletionVersion,
    origin: "manual_selection",
    project_id: "project",
    resource_id: "resource",
    backup_id: "backup",
    resource_generation: 2,
    policy_revision: 0,
    preview_id: "preview",
    plan_hash: hash,
    state: "queued",
    reservation_held: true,
    local_state: "pending",
    remote_state: "not_selected",
    items: [
      {
        copy_id: "copy",
        backup_id: "backup",
        location: "local",
        state: "pending",
        failure_code: "",
        reservation_held: true,
      },
    ],
    created_at: at,
    updated_at: at,
  };
  expect(parseDeletionExecution(execution, scope, resource, "backup").id).toBe(
    "execution",
  );
  expect(() =>
    parseDeletionExecution(
      {
        ...execution,
        items: [{ ...execution.items[0], backup_id: "foreign" }],
      },
      scope,
      resource,
      "backup",
    ),
  ).toThrow();
});
