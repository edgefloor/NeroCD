import { afterEach, expect, test, vi } from "vitest";
import type { HostingDatabase } from "./hosting-databases";
import {
  listDatabaseBackupOccurrences,
  listDatabaseBackupSchedules,
  parseDatabaseBackupOccurrence,
  parseDatabaseBackupSchedule,
  saveDatabaseBackupSchedule,
  validDatabaseBackupScheduleFields,
} from "./hosting-database-backup-schedules";
const db = { resource: { id: "database", generation: 2 } } as HostingDatabase;
const scope = {
  actor: "owner",
  project: "project",
  environment: "environment",
};
const schedule = {
  id: "schedule",
  version: "postgresql.logical-local.schedule.v1",
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
  expect(parseDatabaseBackupSchedule(schedule, scope, db)).toEqual(schedule);
  expect(
    parseDatabaseBackupOccurrence(occurrence, scope, db, "schedule"),
  ).toEqual(occurrence);
  expect(
    parseDatabaseBackupOccurrence(
      {
        ...occurrence,
        state: "uncertain",
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
      parseDatabaseBackupSchedule({ ...schedule, ...change }, scope, db),
    ).toThrow("Invalid database backup schedule metadata");
  });
for (const [name, change] of Object.entries({
  project: { project_id: "foreign" },
  resource: { resource_id: "foreign" },
  schedule: { schedule_id: "foreign" },
  revision: { schedule_revision: 0 },
  flag: { ownership_held: undefined },
  readyHeld: { ownership_held: true },
  uncertainUnlocked: { state: "uncertain" },
  missingOperation: { operation_id: undefined },
  deniedEffect: { state: "denied", reason: "authority_denied" },
  reason: { reason: "private_canary" },
  state: { state: "private_canary" },
  authority: { fence: "private_canary" },
}))
  test(`occurrence rejects ${name}`, () => {
    expect(() =>
      parseDatabaseBackupOccurrence(
        { ...occurrence, ...change },
        scope,
        db,
        "schedule",
      ),
    ).toThrow("Invalid database backup schedule metadata");
  });
test("safe denial has no operation and explicit reason", () => {
  const { operation_id: _, ...denied } = occurrence;
  expect(
    parseDatabaseBackupOccurrence(
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
    request_id: "stable_request",
    expected_resource_generation: 2,
    expected_version: 0,
    frequency: "daily",
    timezone: "UTC",
    enabled: true,
  };
  await expect(
    saveDatabaseBackupSchedule(
      scope,
      db,
      "",
      input,
      new AbortController().signal,
    ),
  ).resolves.toEqual(schedule);
  expect(fetch.mock.calls[0][0]).toBe(
    "/api/v1/resources/database/database-backup-schedules",
  );
  expect(fetch.mock.calls[0][1].method).toBe("POST");
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(input);
  fetch.mockResolvedValue(
    new Response(JSON.stringify({ ...schedule, revision: 9 })),
  );
  await expect(
    saveDatabaseBackupSchedule(
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
  await expect(listDatabaseBackupSchedules(scope, db)).rejects.toThrow();
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
    listDatabaseBackupOccurrences(scope, db, "schedule"),
  ).rejects.toThrow();
});
test("client field validation rejects inline zones, unsupported intervals and implicit local time", () => {
  expect(
    validDatabaseBackupScheduleFields("*/5 * * * *", "Europe/Budapest"),
  ).toBe(true);
  for (const [frequency, timezone] of [
    ["@every 1m", "UTC"],
    ["daily", "Local"],
    ["CRON_TZ=UTC * * * * *", "UTC"],
    ["0 * * * * *", "UTC"],
    ["daily", "Not/AZone"],
  ])
    expect(validDatabaseBackupScheduleFields(frequency, timezone)).toBe(false);
});

test("schedule safe view preserves the exact admitted remote destination revision", () => {
  const remote_target = { target_id: "storage", revision: 3 };
  expect(
    parseDatabaseBackupSchedule({ ...schedule, remote_target }, scope, db)
      .remote_target,
  ).toEqual(remote_target);
  for (const invalid of [
    { target_id: "storage", revision: 0 },
    { target_id: "storage", revision: 3, access_key: "private" },
  ])
    expect(() =>
      parseDatabaseBackupSchedule(
        { ...schedule, remote_target: invalid },
        scope,
        db,
      ),
    ).toThrow();
});

test.each(["postgresql", "mysql"])(
  "Compose %s projections preserve component and reject inconsistent versions",
  (engine) => {
    const context = { resource: db.resource };
    const compose = {
      ...schedule,
      version: `${engine}.compose-logical-local.schedule.v1`,
      component: engine,
    };
    expect(parseDatabaseBackupSchedule(compose, scope, context)).toEqual(compose);
    const remote = { ...compose, remote_target: { target_id: "remote", revision: 1 } };
    if (engine === "mysql") expect(parseDatabaseBackupSchedule(remote, scope, context)).toEqual(remote);
    else expect(() => parseDatabaseBackupSchedule(remote, scope, context)).toThrow();
    expect(
      parseDatabaseBackupOccurrence(
        { ...occurrence, component: "old_database" },
        scope,
        context,
        "schedule",
      ).component,
    ).toBe("old_database");
    for (const invalid of [
      { ...compose, component: undefined },
      { ...compose, component: "../db" },
      { ...schedule, component: engine },
      { ...compose, version: "mysql.compose-logical-local.schedule.v2" },
    ])
      expect(() =>
        parseDatabaseBackupSchedule(invalid, scope, context),
      ).toThrow();
    for (const nativeEngine of ["postgresql", "mysql"] as const)
      expect(() =>
        parseDatabaseBackupSchedule(compose, scope, { ...db, engine: nativeEngine }),
      ).toThrow();
  },
);
test.each(["postgresql", "mysql"])(
  "Compose %s create requires deployment, disable omits it, replies retain component",
  async (engine) => {
    const compose = {
      ...schedule,
      version: `${engine}.compose-logical-local.schedule.v1`,
      component: engine,
    };
    const fetch = vi
      .fn()
      .mockImplementation(async () => new Response(JSON.stringify(compose)));
    vi.stubGlobal("fetch", fetch);
    const input = {
      request_id: "same_request",
      expected_resource_generation: 2,
      expected_version: 0,
      component: engine,
      expected_deployed_operation_id: "deployed",
      frequency: "daily",
      timezone: "UTC",
      enabled: true,
    };
    await expect(
      saveDatabaseBackupSchedule(
        scope,
        db,
        "",
        input,
        new AbortController().signal,
      ),
    ).resolves.toEqual(compose);
    for (const invalid of [
      { ...input, expected_deployed_operation_id: undefined },
      { ...input, component: undefined },
    ])
      await expect(
        saveDatabaseBackupSchedule(
          scope,
          db,
          "",
          invalid,
          new AbortController().signal,
        ),
      ).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
    fetch.mockImplementation(
      async () =>
        new Response(JSON.stringify({ ...compose, revision: 2, enabled: false })),
    );
    await expect(
      saveDatabaseBackupSchedule(
        scope,
        db,
        "schedule",
        {
          ...input,
          expected_version: 1,
          enabled: false,
          expected_deployed_operation_id: undefined,
        },
        new AbortController().signal,
      ),
    ).resolves.toMatchObject({ enabled: false, component: engine });
    fetch.mockImplementation(
      async () =>
        new Response(JSON.stringify({ ...compose, component: "other" })),
    );
    await expect(
      saveDatabaseBackupSchedule(
        scope,
        db,
        "",
        input,
        new AbortController().signal,
      ),
    ).rejects.toThrow();
  },
);


test("MySQL schedules require native engine evidence and reject PostgreSQL and component metadata", () => {
 const mysql = { ...db, engine: "mysql" as const };
 const local = { ...schedule, version: "mysql.logical-local.schedule.v1" };
 expect(parseDatabaseBackupSchedule(local, scope, mysql)).toEqual(local);
 expect(parseDatabaseBackupSchedule({ ...local, remote_target: { target_id: "remote", revision: 1 } }, scope, mysql).remote_target).toEqual({ target_id: "remote", revision: 1 });
 for (const context of [db, { ...db, engine: "postgresql" as const }]) expect(() => parseDatabaseBackupSchedule(local, scope, context)).toThrow();
 for (const invalid of [schedule, { ...local, component: "mysql" }]) expect(() => parseDatabaseBackupSchedule(invalid, scope, mysql)).toThrow();
 expect(parseDatabaseBackupOccurrence(occurrence, scope, mysql, "schedule")).toEqual(occurrence);
 expect(() => parseDatabaseBackupOccurrence({ ...occurrence, component: "mysql" }, scope, mysql, "schedule")).toThrow();
});
test("MySQL schedule transport keeps the native payload and rejects component requests before sending", async () => {
 const mysql = { ...db, engine: "mysql" as const };
 const local = { ...schedule, version: "mysql.logical-local.schedule.v1" };
 const input = { request_id: "stable_request", expected_resource_generation: 2, expected_version: 0, frequency: "daily", timezone: "UTC", enabled: true };
 const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(local)));
 vi.stubGlobal("fetch", fetch);
 expect(await saveDatabaseBackupSchedule(scope, mysql, "", input, new AbortController().signal)).toEqual(local);
 expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(input);
 fetch.mockClear();
 for (const change of [{ component: "mysql", expected_deployed_operation_id: "operation" }, { expected_deployed_operation_id: "operation" }]) await expect(saveDatabaseBackupSchedule(scope, mysql, "", { ...input, ...change }, new AbortController().signal)).rejects.toThrow();
 expect(fetch).not.toHaveBeenCalled();
});


test("MySQL remote schedules preserve the selected target revision in requests and responses", async () => {
 const mysql = { ...db, engine: "mysql" as const };
 const remote_target = { target_id: "remote", revision: 3 };
 const input = { request_id: "stable", expected_resource_generation: 2, expected_version: 0, frequency: "hourly", timezone: "UTC", enabled: true, remote_target };
 const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...schedule, version: "mysql.logical-local.schedule.v1", frequency: "hourly", remote_target })));
 vi.stubGlobal("fetch", fetch);
 expect((await saveDatabaseBackupSchedule(scope, mysql, "", input, new AbortController().signal)).remote_target).toEqual(remote_target);
 expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual(input);
});

test("Compose MySQL remote schedule create, edit and disable preserve exact target and request authority", async () => {
 const remote_target = { target_id: "storage", revision: 3 };
 const context = { resource: db.resource };
 const base = { request_id: "create_remote", expected_resource_generation: 2, expected_version: 0, component: "mysql", expected_deployed_operation_id: "deployed", frequency: "daily", timezone: "UTC", enabled: true, remote_target };
 const fetch = vi.fn();
 vi.stubGlobal("fetch", fetch);
 for (const input of [base, { ...base, request_id: "edit_remote", expected_version: 1, frequency: "hourly" }, { ...base, request_id: "disable_remote", expected_version: 2, enabled: false, expected_deployed_operation_id: undefined }]) {
  const value = { ...schedule, version: "mysql.compose-logical-local.schedule.v1", component: "mysql", revision: input.expected_version + 1, frequency: input.frequency, enabled: input.enabled, remote_target };
  fetch.mockResolvedValueOnce(new Response(JSON.stringify(value)));
  const result = await saveDatabaseBackupSchedule(scope, context, input.expected_version ? "schedule" : "", input, new AbortController().signal);
  expect(result.remote_target).toEqual(remote_target);
  expect(JSON.parse(fetch.mock.calls.at(-1)![1].body)).toEqual(JSON.parse(JSON.stringify(input)));
 }
 const mismatched = { ...schedule, version: "mysql.compose-logical-local.schedule.v1", component: "mysql", remote_target: { ...remote_target, revision: 4 } };
 fetch.mockResolvedValueOnce(new Response(JSON.stringify(mismatched)));
 await expect(saveDatabaseBackupSchedule(scope, context, "", base, new AbortController().signal)).rejects.toThrow();
});
