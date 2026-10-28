import type { Principal, ProjectMember } from "@/api";
import { ApiError } from "@/api/errors";
import * as api from "@/api/hosting-backup-notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { webcrypto } from "node:crypto";
import { StrictMode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ProjectNotifications } from "./ProjectNotifications";
vi.mock("@/api/hosting-backup-notifications", async (importOriginal) => ({
  ...(await importOriginal<typeof api>()),
  listNotificationChannels: vi.fn(),
  saveNotificationChannel: vi.fn(),
  testNotificationChannel: vi.fn(),
  listNotificationDeliveries: vi.fn(),
  getNotificationDelivery: vi.fn(),
}));
const time = "2026-10-26T12:00:00Z";
const principal = {
  id: "user",
  provider: "local",
  roles: [],
} as unknown as Principal;
const member = {
  id: "member",
  user_id: "user",
  project_id: "project",
  name: "Project Owner",
  email: "owner@example.test",
  role: "owner",
  created_at: time,
  updated_at: time,
} as ProjectMember;
const projects = [
  { id: "project", name: "Production", description: "", created_at: time },
  { id: "other", name: "Other project", description: "", created_at: time },
];
const channel: api.NotificationChannel = {
  id: "channel",
  project_id: "project",
  name: "Alerts",
  type: "webhook",
  enabled: true,
  generation: 1,
  events: ["backup_failed", "upload_failed"],
  configured: true,
  consecutive_failures: 0,
  created_at: time,
  updated_at: time,
};
const delivery: api.NotificationDeliveryDetail = {
  id: "delivery",
  event_id: "event",
  project_id: "project",
  channel_id: "channel",
  channel_generation: 1,
  event: "test",
  source_kind: "test",
  source_id: "test",
  source_state: "test",
  state: "pending",
  attempt_count: 0,
  max_attempts: 5,
  local_copy_available: false,
  created_at: time,
  updated_at: time,
  attempts: [],
};
let clients: QueryClient[] = [];
beforeEach(() => {
  vi.resetAllMocks();
  Object.defineProperty(globalThis.crypto, "subtle", {
    configurable: true,
    value: webcrypto.subtle,
  });
  vi.mocked(api.listNotificationChannels).mockResolvedValue([channel]);
  vi.mocked(api.listNotificationDeliveries).mockResolvedValue([]);
  vi.mocked(api.getNotificationDelivery).mockResolvedValue(delivery);
});
afterEach(() => {
  cleanup();
  for (const c of clients) c.clear();
  clients = [];
});
function setup(role: ProjectMember["role"] = "owner", strict = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  clients.push(client);
  const ui = (access = role) => (
    <QueryClientProvider client={client}>
      <ProjectNotifications
        projects={projects}
        principal={principal}
        members={[{ ...member, role: access }]}
      />
    </QueryClientProvider>
  );
  const view = render(strict ? <StrictMode>{ui()}</StrictMode> : ui());
  fireEvent.change(screen.getByLabelText("Notifications project"), {
    target: { value: "project" },
  });
  return { ...view, client, ui };
}
async function newWebhook() {
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Add notification channel",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Add notification channel" }),
  );
  const form = screen.getByRole("form", {
    name: "Create notification channel",
  });
  fireEvent.change(within(form).getByLabelText("Channel name"), {
    target: { value: "Ops" },
  });
  fireEvent.change(within(form).getByLabelText("Channel type"), {
    target: { value: "webhook" },
  });
  fireEvent.change(within(form).getByLabelText("Webhook URL"), {
    target: { value: "https://fixture.invalid/private" },
  });
  return form;
}
test.each(["viewer", "maintainer"] as const)(
  "%s sees masked metadata/history without configuration or test actions",
  async (role) => {
    vi.mocked(api.listNotificationDeliveries).mockResolvedValue([
      { ...delivery, state: "suppressed" },
    ]);
    setup(role);
    await screen.findByRole("article", { name: "Channel Alerts" });
    expect(
      screen.queryByRole("button", { name: "Add notification channel" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Send test for Alerts" }),
    ).toBeNull();
    expect(screen.queryByLabelText("Webhook URL")).toBeNull();
    expect(await screen.findByText(/Test delivery · suppressed/)).toBeTruthy();
    expect(api.saveNotificationChannel).not.toHaveBeenCalled();
  },
);
test("StrictMode owner submits credentials once and clears immediately while keeping draft on response loss", async () => {
  let reject!: (reason: unknown) => void;
  vi.mocked(api.saveNotificationChannel).mockImplementation(
    () =>
      new Promise((_, no) => {
        reject = no;
      }),
  );
  setup("owner", true);
  const form = await newWebhook();
  fireEvent.submit(form);
  await waitFor(() =>
    expect(api.saveNotificationChannel).toHaveBeenCalledTimes(1),
  );
  expect(
    within(form).getByLabelText<HTMLInputElement>("Webhook URL").value,
  ).toBe("");
  reject(new Error("private backend body"));
  await screen.findByRole("button", { name: "Retry exact channel request" });
  expect(
    within(form).getByLabelText<HTMLInputElement>("Channel name").value,
  ).toBe("Ops");
  expect(screen.queryByText(/private backend body/)).toBeNull();
  const first = vi.mocked(api.saveNotificationChannel).mock.calls[0]![1];
  fireEvent.change(within(form).getByLabelText("Webhook URL"), {
    target: { value: "https://fixture.invalid/wrong" },
  });
  fireEvent.submit(form);
  await screen.findByText(/Re-enter the exact original credentials/);
  expect(api.saveNotificationChannel).toHaveBeenCalledTimes(1);
  vi.mocked(api.saveNotificationChannel).mockResolvedValue({
    ...channel,
    name: "Ops",
  });
  fireEvent.change(within(form).getByLabelText("Webhook URL"), {
    target: { value: "https://fixture.invalid/private" },
  });
  fireEvent.submit(form);
  await waitFor(() =>
    expect(api.saveNotificationChannel).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(api.saveNotificationChannel).mock.calls[1]![1]).toEqual(
    first,
  );
});
test("SMTP nonsecret draft survives rejected save and credentials do not", async () => {
  vi.mocked(api.saveNotificationChannel).mockRejectedValue(
    new ApiError(400, "secret-echo", null),
  );
  setup();
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Add notification channel",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Add notification channel" }),
  );
  const form = screen.getByRole("form", {
    name: "Create notification channel",
  });
  for (const [label, value] of [
    ["Channel name", "Mail"],
    ["SMTP host", "smtp.fixture.invalid"],
    ["SMTP username", "private-user"],
    ["SMTP password", "private-pass"],
    ["From address", "alerts@example.test"],
  ] as const)
    fireEvent.change(within(form).getByLabelText(label), { target: { value } });
  fireEvent.click(
    within(form).getByLabelText("Project Owner · owner@example.test"),
  );
  fireEvent.submit(form);
  await screen.findByText(/Settings were rejected/);
  expect(within(form).getByLabelText<HTMLInputElement>("SMTP host").value).toBe(
    "smtp.fixture.invalid",
  );
  expect(
    within(form).getByLabelText<HTMLInputElement>("From address").value,
  ).toBe("alerts@example.test");
  expect(
    within(form).getByLabelText<HTMLInputElement>("SMTP password").value,
  ).toBe("");
  expect(
    within(form).getByLabelText<HTMLInputElement>("SMTP username").value,
  ).toBe("");
  expect(screen.queryByText(/secret-echo/)).toBeNull();
});
test("metadata edit retains configuration and generation conflict requires explicit current-generation review", async () => {
  vi.mocked(api.saveNotificationChannel).mockRejectedValueOnce(
    new ApiError(409, "conflict", null),
  );
  setup();
  fireEvent.click(await screen.findByRole("button", { name: "Edit Alerts" }));
  const form = screen.getByRole("form", { name: "Edit Alerts" });
  fireEvent.change(within(form).getByLabelText("Channel name"), {
    target: { value: "New name" },
  });
  fireEvent.click(within(form).getByLabelText("Channel enabled"));
  fireEvent.submit(form);
  await screen
    .findByRole("region", { name: "Channel generation conflict" })
    .catch(() => screen.findByLabelText("Channel generation conflict"));
  expect(api.saveNotificationChannel).toHaveBeenCalledWith(
    "project",
    expect.objectContaining({
      expected_generation: 1,
      enabled: false,
      name: "New name",
    }),
    "channel",
    expect.any(AbortSignal),
  );
  expect(
    vi.mocked(api.saveNotificationChannel).mock.calls[0]![1],
  ).not.toHaveProperty("configuration");
  expect(
    within(form).getByRole<HTMLButtonElement>("button", {
      name: "Save notification channel",
    }).disabled,
  ).toBe(true);
  vi.mocked(api.listNotificationChannels).mockResolvedValue([
    { ...channel, generation: 2 },
  ]);
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh current channel" }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Use current generation 2" }),
  );
  vi.mocked(api.saveNotificationChannel).mockResolvedValue({
    ...channel,
    generation: 3,
    name: "New name",
    enabled: false,
  });
  fireEvent.submit(form);
  await waitFor(() =>
    expect(api.saveNotificationChannel).toHaveBeenCalledTimes(2),
  );
  expect(
    vi.mocked(api.saveNotificationChannel).mock.calls[1]![1],
  ).toMatchObject({ expected_generation: 2, name: "New name", enabled: false });
});
test("test response loss retries exact request and shows accepted pending outcome without claiming pipeline success", async () => {
  vi.mocked(api.testNotificationChannel)
    .mockRejectedValueOnce(new Error("lost"))
    .mockResolvedValueOnce(delivery);
  setup();
  fireEvent.click(
    await screen.findByRole("button", { name: "Send test for Alerts" }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Retry exact test request" }),
  );
  await screen.findByText(/Test accepted. Recorded outcome: pending/);
  expect(vi.mocked(api.testNotificationChannel).mock.calls[1]![2]).toEqual(
    vi.mocked(api.testNotificationChannel).mock.calls[0]![2],
  );
  expect(api.testNotificationChannel).toHaveBeenCalledTimes(2);
});
test("scope navigation and lost ownership dispose credential editor, 403 denies further controls", async () => {
  const { rerender, ui } = setup();
  await newWebhook();
  rerender(ui("viewer"));
  expect(screen.queryByLabelText("Webhook URL")).toBeNull();
  rerender(ui("owner"));
  await screen.findByRole("button", { name: "Add notification channel" });
  const form = await newWebhook();
  vi.mocked(api.saveNotificationChannel).mockRejectedValue(
    new ApiError(403, "private", null),
  );
  fireEvent.submit(form);
  await screen.findByText(
    "Notification access is unavailable. Refresh project access.",
  );
  expect(screen.queryByLabelText("Webhook URL")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Add notification channel" }),
  ).toBeNull();
});

test("uncertain source outcome is attention rather than definite backup failure", async () => {
  vi.mocked(api.listNotificationDeliveries).mockResolvedValue([
    {
      ...delivery,
      event: "backup_failed",
      source_kind: "backup",
      source_state: "uncertain",
      state: "delivered",
    },
  ]);
  setup("viewer");
  expect(
    await screen.findByText(
      /Backup outcome uncertain, needs attention · delivered/,
    ),
  ).toBeTruthy();
  expect(screen.queryByText(/Backup failed · delivered/)).toBeNull();
});

test.each([
  "backup_notifications_unavailable",
  "backup notifications require NEROCD_PUBLIC_ORIGIN as an HTTPS origin and configured managed secrets",
])(
  "known unavailable setup has an actionable fixed prerequisite: %s",
  async (message) => {
    vi.mocked(api.saveNotificationChannel).mockRejectedValue(
      new ApiError(503, message, null),
    );
    setup();
    const form = await newWebhook();
    fireEvent.submit(form);
    expect(
      await screen.findByText(
        /set NEROCD_PUBLIC_ORIGIN to an absolute HTTPS origin/,
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(/configure managed secrets and the notification sender/),
    ).toBeTruthy();
    expect(
      within(form).getByLabelText<HTMLInputElement>("Webhook URL").value,
    ).toBe("");
  },
);
test("unknown unavailable response keeps generic uncertain retry guidance and never renders its body", async () => {
  vi.mocked(api.saveNotificationChannel).mockRejectedValue(
    new ApiError(503, "private unknown backend body", null),
  );
  setup();
  const form = await newWebhook();
  fireEvent.submit(form);
  expect(await screen.findByText(/Response unconfirmed/)).toBeTruthy();
  expect(screen.queryByText(/private unknown backend body/)).toBeNull();
  expect(screen.queryByText(/set NEROCD_PUBLIC_ORIGIN/)).toBeNull();
});

test("refreshing channel authority clears credentials while preserving the public draft", async () => {
  setup();
  await newWebhook();
  let resolve!: (value: api.NotificationChannel[]) => void;
  vi.mocked(api.listNotificationChannels).mockImplementationOnce(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh notification history" }),
  );
  await waitFor(() =>
    expect(screen.getByLabelText<HTMLInputElement>("Webhook URL").value).toBe(
      "",
    ),
  );
  expect(screen.getByLabelText<HTMLInputElement>("Channel name").value).toBe(
    "Ops",
  );
  await act(async () => resolve([channel]));
  expect(screen.getByLabelText<HTMLInputElement>("Webhook URL").value).toBe("");
  expect(screen.getByLabelText<HTMLInputElement>("Channel name").value).toBe(
    "Ops",
  );
  expect(api.saveNotificationChannel).not.toHaveBeenCalled();
});
