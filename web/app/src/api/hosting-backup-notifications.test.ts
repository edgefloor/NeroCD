import { afterEach, expect, test, vi } from "vitest";
import { request } from "./client";
import {
  getNotificationDelivery,
  listNotificationChannels,
  listNotificationDeliveries,
  parseBackupRecoveryLocator,
  parseNotificationChannel,
  parseNotificationDelivery,
  safeRecoveryURL,
  saveNotificationChannel,
  testNotificationChannel,
} from "./hosting-backup-notifications";
vi.mock("./client", () => ({ request: vi.fn() }));
afterEach(() => vi.resetAllMocks());
const time = "2026-10-26T12:00:00Z";
const channel = {
  id: "channel",
  project_id: "project",
  name: "Backup alerts",
  type: "webhook" as const,
  enabled: true,
  generation: 1,
  events: ["backup_failed" as const, "upload_failed" as const],
  configured: true,
  consecutive_failures: 0,
  created_at: time,
  updated_at: time,
};
const delivery = {
  id: "delivery",
  event_id: "event",
  project_id: "project",
  channel_id: "channel",
  channel_generation: 1,
  event: "upload_failed",
  source_kind: "transfer",
  source_id: "upload",
  source_state: "failed",
  state: "failed",
  attempt_count: 5,
  max_attempts: 5,
  failure_code: "transport_failed",
  local_copy_available: true,
  recovery_url:
    "/projects?project_id=project&environment_id=env&resource_id=db&backup_id=backup&transfer_id=upload",
  created_at: time,
  updated_at: time,
};
test("closed channel metadata rejects credentials, foreign scope and invalid subscriptions", () => {
  expect(parseNotificationChannel(channel, "project")).toEqual(channel);
  for (const patch of [
    { configuration: { smtp_password: "canary" } },
    { webhook_url: "secret" },
    { project_id: "foreign" },
    { generation: 0 },
    { events: ["test"] },
    { events: ["backup_failed", "backup_failed"] },
  ])
    expect(() =>
      parseNotificationChannel({ ...channel, ...patch }, "project"),
    ).toThrow();
});
test("delivery parser drops unrecognized failure text and validates source and locator scope", () => {
  const out = parseNotificationDelivery(
    { ...delivery, failure_code: "<script>canary</script>" },
    "project",
  );
  expect(out.failure_code).toBe("attention");
  expect(JSON.stringify(out)).not.toContain("canary");
  for (const patch of [
    { project_id: "foreign" },
    { source_kind: "backup" },
    { max_attempts: 6 },
    { attempt_count: 6 },
    { smtp_password: "canary" },
    { recovery_url: "https://evil.test" },
    {
      recovery_url: delivery.recovery_url.replace(
        "project_id=project",
        "project_id=foreign",
      ),
    },
  ])
    expect(() =>
      parseNotificationDelivery({ ...delivery, ...patch }, "project"),
    ).toThrow();
});
test("recovery links accept bounded internal locators only", () => {
  expect(safeRecoveryURL(delivery.recovery_url, "project")).toBe(
    delivery.recovery_url,
  );
  for (const value of [
    "//evil.test/projects?",
    delivery.recovery_url + "&backup_id=other",
    delivery.recovery_url + "#bad",
    delivery.recovery_url + "&redirect=https://evil.test",
    delivery.recovery_url.replace("backup_id=backup", "backup_id=../bad"),
    delivery.recovery_url.replace("db", "x".repeat(129)),
  ])
    expect(safeRecoveryURL(value, "project")).toBeUndefined();
  expect(
    parseBackupRecoveryLocator({
      project_id: "p",
      environment_id: "e",
      resource_id: "r",
      backup_id: "b",
    }),
  ).toEqual({
    project_id: "p",
    environment_id: "e",
    resource_id: "r",
    backup_id: "b",
  });
});
test("bounded lists reject duplicate, wrapped or leaked data", async () => {
  vi.mocked(request).mockResolvedValue([channel]);
  expect(await listNotificationChannels("project")).toEqual([channel]);
  for (const raw of [
    { data: [channel] },
    [channel, channel],
    Array(101).fill(channel),
    { data: [channel], secret: "canary" },
  ]) {
    vi.mocked(request).mockResolvedValue(raw);
    await expect(listNotificationChannels("project")).rejects.toThrow();
  }
  vi.mocked(request).mockResolvedValue([delivery]);
  expect(await listNotificationDeliveries("project")).toHaveLength(1);
});
test("save and test preserve caller request identity and expected generation without inventing configuration", async () => {
  const input = {
    request_id: "stable",
    expected_generation: 0,
    name: channel.name,
    type: channel.type,
    enabled: true,
    events: channel.events,
    configuration: { webhook_url: "https://fixture.invalid/private" },
  };
  vi.mocked(request).mockResolvedValue(channel);
  await saveNotificationChannel("project", input);
  expect(request).toHaveBeenLastCalledWith(
    "/api/v1/projects/project/notification-channels",
    expect.objectContaining({
      method: "POST",
      body: input,
      requestID: "stable",
      cache: "no-store",
    }),
  );
  const update = { ...input, expected_generation: 1 };
  delete (update as { configuration?: unknown }).configuration;
  vi.mocked(request).mockResolvedValue({ ...channel, generation: 2 });
  await saveNotificationChannel("project", update, "channel");
  expect(request).toHaveBeenLastCalledWith(
    "/api/v1/projects/project/notification-channels/channel",
    expect.objectContaining({ method: "PUT", body: update }),
  );
  expect(vi.mocked(request).mock.calls.at(-1)?.[1]?.body).not.toHaveProperty(
    "configuration",
  );
  vi.mocked(request).mockResolvedValue({
    ...delivery,
    event: "test",
    source_kind: "test",
    source_state: "test",
    state: "pending",
    attempt_count: 0,
  });
  await testNotificationChannel("project", "channel", {
    request_id: "teststable",
    expected_generation: 1,
  });
  expect(request).toHaveBeenLastCalledWith(
    "/api/v1/projects/project/notification-channels/channel/tests",
    expect.objectContaining({
      method: "POST",
      requestID: "teststable",
      body: { request_id: "teststable", expected_generation: 1 },
    }),
  );
});
test("delivery detail validates identity and bounded attempts", async () => {
  vi.mocked(request).mockResolvedValue({
    ...delivery,
    attempts: [
      { number: 1, state: "failed", started_at: time, failure_code: "private" },
    ],
  });
  expect(
    (await getNotificationDelivery("project", "delivery")).attempts[0]
      .failure_code,
  ).toBe("attention");
  await expect(getNotificationDelivery("project", "foreign")).rejects.toThrow();
});

test("test delivery uses a project-only locator and absent detail attempts normalize safely", async () => {
  const testDelivery = {
    ...delivery,
    event: "test",
    source_kind: "test",
    source_state: "test",
    source_id: "ntt_test",
    recovery_url: "/projects?project_id=project",
    failure_code: "",
    state: "pending",
    attempt_count: 0,
  };
  vi.mocked(request).mockResolvedValue(testDelivery);
  expect(await getNotificationDelivery("project", "delivery")).toMatchObject({
    attempts: [],
    recovery_url: "/projects?project_id=project",
  });
  expect(() =>
    parseNotificationDelivery(
      { ...testDelivery, source_state: "failed" },
      "project",
    ),
  ).toThrow();
  expect(() =>
    parseNotificationDelivery(
      { ...testDelivery, recovery_url: "/projects?project_id=foreign" },
      "project",
    ),
  ).toThrow();
  expect(
    parseNotificationDelivery(
      {
        ...delivery,
        attempts: [{ number: 1, state: "uncertain", started_at: time }],
      },
      "project",
      true,
    ).attempts[0]?.state,
  ).toBe("uncertain");
});

test("storage recovery selector is closed and preserves source family",()=>{
 const q={project_id:"p",environment_id:"e",resource_id:"r",backup_id:"b",source_kind:"storage_transfer",transfer_id:"t"};
 expect(parseBackupRecoveryLocator(q)).toEqual(q);
 expect(parseBackupRecoveryLocator({...q,source_kind:"unknown"})).toBeUndefined();
 expect(parseBackupRecoveryLocator({...q,transfer_id:undefined})).toBeUndefined();
 expect(safeRecoveryURL("/projects?"+new URLSearchParams(q),"p")).toContain("source_kind=storage_transfer");
});
