import { expect, test, type Page } from "@playwright/test";
const time = "2026-10-26T12:00:00Z";
const project = {
  id: "project",
  name: "Production",
  description: "",
  created_at: time,
};
const member = {
  id: "membership",
  user_id: "current_owner",
  project_id: "project",
  name: "Current owner",
  email: "owner@example.test",
  role: "owner",
  created_at: time,
  updated_at: time,
};
const baseChannel = {
  id: "channel",
  project_id: "project",
  name: "Backup alerts",
  type: "webhook",
  enabled: true,
  generation: 1,
  events: ["backup_failed", "upload_failed"],
  configured: true,
  consecutive_failures: 0,
  created_at: time,
  updated_at: time,
};
async function mockAPI(page: Page, viewer = false) {
  const channels = [baseChannel];
  const mutations: string[] = [];
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    let body: unknown;
    if (req.method() !== "GET") mutations.push(path);
    const list = (items: unknown[]) => ({
      items,
      total: items.length,
      count: items.length,
      offset: 0,
      limit: 100,
    });
    if (path === "/api/v1/me")
      body = {
        id: "current_owner",
        provider: "local",
        email: member.email,
        name: member.name,
        roles: [],
      };
    else if (path === "/api/v1/bootstrap-status") body = { required: false };
    else if (path === "/api/v1/health")
      body = { status: "ok", version: "test" };
    else if (path === "/api/v1/projects") body = list([project]);
    else if (path === "/api/v1/project-members")
      body = list([{ ...member, role: viewer ? "viewer" : "owner" }]);
    else if (
      path.endsWith("/notification-channels") &&
      req.method() === "POST"
    ) {
      const input = req.postDataJSON();
      const saved = {
        ...baseChannel,
        id: `channel_${channels.length}`,
        name: input.name,
        type: input.type,
        events: input.events,
        enabled: input.enabled,
      };
      channels.push(saved);
      body = saved;
    } else if (path.endsWith("/notification-channels")) body = channels;
    else if (path.endsWith("/notification-deliveries"))
      body = [
        {
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
        },
      ];
    else body = list([]);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  return mutations;
}
for (const width of [320, 390, 430, 1440])
  test(`owner settings and history fit ${width}px and support keyboard entry`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const mutations = await mockAPI(page);
    await page.goto("/projects");
    const panel = page.getByRole("region", {
      name: "Project backup notifications",
      exact: true,
    });
    await panel.getByLabel("Notifications project").selectOption("project");
    await panel
      .getByRole("button", { name: "Add notification channel" })
      .click();
    await panel.getByLabel("Channel name").focus();
    await page.keyboard.type("Keyboard webhook");
    await page.keyboard.press("Tab");
    await expect(panel.getByLabel("Channel type")).toBeFocused();
    await panel.getByLabel("Channel type").selectOption("webhook");
    await panel
      .getByLabel("Webhook URL")
      .fill("https://fixture.invalid/private");
    await panel
      .getByRole("button", { name: "Save notification channel" })
      .click();
    await expect(
      panel.getByRole("article", { name: "Channel Keyboard webhook" }),
    ).toBeVisible();
    await expect(panel.getByLabel("Webhook URL")).toHaveCount(0);
    await expect(
      panel.getByText("Local backup remains available; remote upload failed."),
    ).toBeVisible();
    await expect(
      panel.getByRole("link", { name: "Open authenticated backup recovery" }),
    ).toHaveAttribute("href", /backup_id=backup&transfer_id=upload$/);
    const bounds = await panel.evaluate((element) => ({
      width: element.clientWidth,
      scroll: element.scrollWidth,
      right: element.getBoundingClientRect().right,
    }));
    expect(bounds.scroll).toBeLessThanOrEqual(bounds.width + 1);
    expect(bounds.right).toBeLessThanOrEqual(width);
    expect(mutations).toEqual([
      "/api/v1/projects/project/notification-channels",
    ]);
  });
test("viewer can inspect masked outcomes but cannot configure or send tests", async ({
  page,
}) => {
  const mutations = await mockAPI(page, true);
  await page.goto("/projects");
  const panel = page.getByRole("region", {
    name: "Project backup notifications",
    exact: true,
  });
  await panel.getByLabel("Notifications project").selectOption("project");
  await expect(
    panel.getByRole("article", { name: "Channel Backup alerts" }),
  ).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "Add notification channel" }),
  ).toHaveCount(0);
  await expect(panel.getByRole("button", { name: /Send test/ })).toHaveCount(0);
  await expect(
    panel.getByText(/Upload failed, local backup available · failed/),
  ).toBeVisible();
  expect(mutations).toEqual([]);
});
