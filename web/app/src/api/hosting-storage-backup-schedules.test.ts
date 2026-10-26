import { afterEach, expect, test, vi } from "vitest";
import type { Resource } from "./hosting";
import {
  listStorageBackupOccurrences,
  listStorageBackupSchedules,
  parseStorageBackupOccurrence,
  parseStorageBackupSchedule,
  saveStorageBackupSchedule,
  validStorageBackupScheduleFields,
} from "./hosting-storage-backup-schedules";
const db = { id: "database", generation: 2 } as Resource;
const scope = {
  actor: "owner",
  project: "project",
  destination: "destination",
  resource: "database",
  binding: "binding",
};
const schedule = {
  id: "schedule",
  version: "hosting.storage-backup.schedule.v1",
  project_id: "project",
  resource_id: "database",
  revision: 1,
  frequency: "daily",
  timezone: "UTC",
  enabled: true,
  created_at: "2026-10-25T12:00:00Z",
  updated_at: "2026-10-25T12:00:00Z",
};
const occurrence = {
  id: "occurrence",
  schedule_id: "schedule",
  project_id: "project",
  resource_id: "database",
  schedule_revision: 1,
  frequency: "daily",
  timezone: "UTC",
  scheduled_at: "2026-10-25T12:00:00Z",
  created_at: "2026-10-25T12:00:03Z",
  state: "succeeded",
  reason: "",
  ownership_held: false,
  operation_id: "backup",
};
afterEach(() => vi.unstubAllGlobals());
test("closed schedule and occurrence projections retain configuration and exact operation linkage", () => {
  expect(parseStorageBackupSchedule(schedule, scope, db)).toEqual(schedule);
  expect(
    parseStorageBackupOccurrence(occurrence, scope, db, "schedule"),
  ).toEqual(occurrence);
  expect(
    parseStorageBackupOccurrence(
      {
        ...occurrence,
        state: "reconciliation_required",
        reason: "interrupted",
        ownership_held: true,
      },
      scope,
      db,
      "schedule",
    ).ownership_held,
  ).toBe(true);
});
for (const [name, change] of Object.entries({
  project: { project_id: "foreign" },
  resource: { resource_id: "foreign" },
  missingVersion: { version: undefined },
  revision: { revision: 0 },
  flag: { enabled: "true" },
  timezone: { timezone: "private\ncanary" },
  path: { path: "/private" },
  token: { token: "private_canary" },
  authority: { lease_id: "private_canary" },
}))
  test(`schedule rejects ${name}`, () => {
    expect(() =>
      parseStorageBackupSchedule({ ...schedule, ...change }, scope, db),
    ).toThrow("Invalid storage backup schedule metadata");
  });
for (const [name, change] of Object.entries({
  project: { project_id: "foreign" },
  resource: { resource_id: "foreign" },
  schedule: { schedule_id: "foreign" },
  revision: { schedule_revision: 0 },
  flag: { ownership_held: undefined },
  readyHeld: { ownership_held: true },
  uncertainUnlocked: { state: "reconciliation_required" },
  missingOperation: { operation_id: undefined },
  deniedEffect: { state: "denied", reason: "authority_denied" },
  reason: { reason: "private_canary" },
  state: { state: "private_canary" },
  authority: { fence: "private_canary" },
}))
  test(`occurrence rejects ${name}`, () => {
    expect(() =>
      parseStorageBackupOccurrence(
        { ...occurrence, ...change },
        scope,
        db,
        "schedule",
      ),
    ).toThrow("Invalid storage backup schedule metadata");
  });
test("safe denial has no operation and explicit reason", () => {
  const { operation_id: _, ...denied } = occurrence;
  expect(
    parseStorageBackupOccurrence(
      { ...denied, state: "denied", reason: "authority_denied" },
      scope,
      db,
      "schedule",
    ).operation_id,
  ).toBeUndefined();
});
test("schedule requests correlate revision/configuration and retain stable request identity", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify(schedule)));
  vi.stubGlobal("fetch", fetch);
  const input = {
    request_id: "a".repeat(32),
    expected_resource_generation: 2,
    expected_version: 0,
    frequency: "daily",
    timezone: "UTC",
    enabled: true,
  };
  await expect(
    saveStorageBackupSchedule(
      scope,
      db,
      "",
      input,
      new AbortController().signal,
    ),
  ).resolves.toEqual(schedule);
  expect(fetch.mock.calls[0][0]).toBe(
    "/api/v1/resources/database/storage-backup-schedules",
  );
  expect(fetch.mock.calls[0][1].method).toBe("POST");
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(input);
  fetch.mockResolvedValue(
    new Response(JSON.stringify({ ...schedule, revision: 9 })),
  );
  await expect(
    saveStorageBackupSchedule(
      scope,
      db,
      "schedule",
      { ...input, expected_version: 1 },
      new AbortController().signal,
    ),
  ).rejects.toThrow();
  expect(fetch.mock.calls[1][1].method).toBe("PUT");
});
test("duplicate schedule identities and UTC occurrence slots fail closed", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify([schedule, schedule])));
  vi.stubGlobal("fetch", fetch);
  await expect(listStorageBackupSchedules(scope, db)).rejects.toThrow();
  fetch.mockResolvedValue(
    new Response(
      JSON.stringify([
        occurrence,
        {
          ...occurrence,
          id: "other",
          scheduled_at: "2026-10-25T13:00:00+01:00",
        },
      ]),
    ),
  );
  await expect(
    listStorageBackupOccurrences(scope, db, "schedule"),
  ).rejects.toThrow();
});
test("client field validation rejects inline zones, unsupported intervals and implicit local time", () => {
  expect(
    validStorageBackupScheduleFields("*/5 * * * *", "Europe/Budapest"),
  ).toBe(true);
  for (const [frequency, timezone] of [
    ["@every 1m", "UTC"],
    ["daily", "Local"],
    ["CRON_TZ=UTC * * * * *", "UTC"],
    ["0 * * * * *", "UTC"],
    ["daily", "Not/AZone"],
  ])
    expect(validStorageBackupScheduleFields(frequency, timezone)).toBe(false);
});

test("schedule safe view preserves the exact admitted remote destination revision", () => {
  const remote_target = { target_id: "storage", revision: 3 };
  expect(
    parseStorageBackupSchedule({ ...schedule, remote_target }, scope, db)
      .remote_target,
  ).toEqual(remote_target);
  for (const invalid of [
    { target_id: "storage", revision: 0 },
    { target_id: "storage", revision: 3, access_key: "private" },
  ])
    expect(() =>
      parseStorageBackupSchedule(
        { ...schedule, remote_target: invalid },
        scope,
        db,
      ),
    ).toThrow();
});

test("identical replay keeps its original generation after current metadata advances", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify(schedule)));
  vi.stubGlobal("fetch", fetch);
  const input = {
    request_id: "a".repeat(32),
    expected_resource_generation: 1,
    expected_version: 0,
    frequency: "daily",
    timezone: "UTC",
    enabled: true,
  };
  await expect(
    saveStorageBackupSchedule(
      scope,
      { ...db, generation: 9 },
      "",
      input,
      new AbortController().signal,
    ),
  ).resolves.toEqual(schedule);
  expect(
    JSON.parse(fetch.mock.calls[0][1].body).expected_resource_generation,
  ).toBe(1);
});
