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

for (const viewer of [false, true]) test(`exact retained recovery beyond recent history for ${viewer ? "viewer" : "owner"}`, async ({ page }) => {
  await mockAPI(page, viewer);
  const resource = { id: "db", project_id: "project", project_environment_id: "env", name: "Data", kind: "database", source: { kind: "image", image: { reference: "postgres:17" } }, generation: 1, created_by: "owner", created_at: time, updated_at: time };
  const database = { resource, binding: { id: "binding", resource_id: "db", destination_id: "runner", runtime_namespace: "ncd_db", created_by: "owner", created_at: time }, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, engine: "postgresql", recipe_version: "postgresql-17.11-bookworm.v1", runtime_state: "adapter_unqualified", credentials: [{ role: "admin", username: "postgres", secret_id: "admin_secret", version: 1 }, { role: "app", username: "ncd_app", secret_id: "app_secret", version: 1 }] };
  const backup = { id: "backup", backup_id: "backup", project_id: "project", source_resource_id: "db", source_generation: 1, max_bytes: 1073741824, timeout_seconds: 900, version: "postgresql.logical-local.v1", mode: "backup", state: "succeeded", consistency: "logical_snapshot_source_writable", ownership_held: false, created_at: time, updated_at: time, blob: { bytes: 65537, digest: "sha256:" + "a".repeat(64), format: "postgresql_custom" } };
  const transfer = { id: "upload", version: "postgresql.logical-s3-transfer.v1", backup_id: "backup", project_id: "project", mode: "upload", state: "failed", failure_code: "remote_unavailable", destination_id: "runner", created_at: time, updated_at: time, target_id: "target", target_revision: 1, blob: backup.blob };
  const mutations: string[] = [];
  page.on("request", req => { if (req.url().includes("/api/v1/") && req.method() !== "GET") mutations.push(req.url()); });
  await page.route("**/api/v1/**", async route => {
    const path = new URL(route.request().url()).pathname; let body: unknown;
    if (path === "/api/v1/project-environments") body = { items: [{ id: "env", project_id: "project", name: "Production", created_at: time }], count: 1, total: 1, limit: 100, offset: 0 };
    else if (path === "/api/v1/resources/db/database") body = database;
    else if (path === "/api/v1/database-backups/backup") body = backup;
    else if (path === "/api/v1/database-backup-transfers/upload") body = transfer;
    else if (path === "/api/v1/resources/db/database-backups") body = Array.from({ length: 100 }, (_, n) => ({ ...backup, id: `newer_${n}`, backup_id: `newer_${n}` }));
    else if (path.endsWith("/transfers")) body = path === "/api/v1/database-backups/backup/transfers" ? Array.from({ length: 100 }, (_, n) => ({ ...transfer, id: `upload_${n}`, created_at: "2026-10-26T13:00:00Z" })) : [];
    else if (path.endsWith("/database-backup-retention")) body = null;
    else if (path.includes("database-backup-")) body = [];
    else return route.fallback();
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/projects?project_id=project&environment_id=env&resource_id=db&backup_id=backup&transfer_id=upload");
  await expect(page.getByRole("listitem", { name: "Linked backup history", exact: true })).toContainText("Local logical snapshot published.");
  await expect(page.getByRole("listitem", { name: "Linked upload history", exact: true })).toContainText("Upload upload");
  if (viewer) await expect(page.getByRole("button", { name: "Restore into new database" })).toHaveCount(0);
  else { await expect(page.getByLabel("Completed backup")).toHaveValue("backup"); await expect(page.getByRole("button", { name: "Restore into new database" })).toBeVisible(); }
  expect(mutations).toEqual([]);
});

test("known missing notification origin displays an actionable setup prerequisite", async ({ page }) => {
  await mockAPI(page);
  await page.route("**/api/v1/projects/project/notification-channels", async route => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "backup_notifications_unavailable", error: "backup notifications require NEROCD_PUBLIC_ORIGIN as an HTTPS origin and configured managed secrets" }) });
  });
  await page.goto("/projects");
  const panel = page.getByRole("region", { name: "Project backup notifications", exact: true });
  await panel.getByLabel("Notifications project").selectOption("project");
  await panel.getByRole("button", { name: "Add notification channel" }).click();
  await panel.getByLabel("Channel name").fill("Setup check"); await panel.getByLabel("Channel type").selectOption("webhook"); await panel.getByLabel("Webhook URL").fill("https://fixture.invalid/private");
  await panel.getByRole("button", { name: "Save notification channel" }).click();
  await expect(panel.getByRole("alert")).toContainText("set NEROCD_PUBLIC_ORIGIN to an absolute HTTPS origin");
  await expect(panel.getByLabel("Webhook URL")).toHaveValue("");
});
