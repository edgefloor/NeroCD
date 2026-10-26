import { expect, test, type Page, chromium } from "@playwright/test";

// This profile consumes actual SMTP/HTTPS receipts and runner events. The
// private configuration response never enters a trace, screenshot, or report.
test.use({ trace: "off", screenshot: "off", video: "off" });
const raw = process.env.NEROCD_DATABASE_CONNECTION_BROWSER_META;
const meta = raw ? JSON.parse(raw) as {
  origin: string; project_id: string; environment: string;
  database_resource_id: string; control_path: string; profile: string; tls_spki: string;
} : undefined;

test("actual backup notifications configure both transports and recover the retained local archive", async ({ page, browser }) => {
  test.skip(!meta || meta.profile !== "backup_notifications", "requires the separately authorized notification fixture");
  test.setTimeout(600_000);
  const m = meta!;
  const headers = { Origin: m.origin, "X-NeroCD-CSRF": "1" };
  const signIn = async (target: Page, email: string, password: string) => {
    await target.goto("/sign-in");
    await target.getByLabel("Email", { exact: true }).fill(email);
    await target.getByLabel("Password", { exact: true }).fill(password);
    await target.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(target.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  };
  const control = async (suffix: string) => {
    try {
      return await page.evaluate(async ({ path, headers }) => {
        const response = await fetch(path, { method: "POST", headers, credentials: "same-origin", cache: "no-store" });
        return { status: response.status, value: response.status === 200 ? await response.json() : null };
      }, { path: m.control_path + "/" + suffix, headers });
    } catch { throw new Error("notification fixture control unavailable"); }
  };
  const state = async () => {
    const response = await control("state");
    if (response.status !== 200) throw new Error("notification fixture state unavailable");
    return response.value as { phase: string; backup_id: string; recovery_url: string; failed_backup_id: string };
  };
  const acknowledge = async (phase: string) => {
    expect((await control(phase)).status).toBe(204);
  };
  // A different public-key pin must not turn this disposable certificate into
  // a trusted endpoint. The correctly pinned browser is supplied by the config.
  const wrongPin = Buffer.alloc(32, 0).toString("base64");
  expect(m.tls_spki).not.toBe(wrongPin);
  const untrusted = await chromium.launch({ args: [`--ignore-certificate-errors-spki-list=${wrongPin}`] });
  try {
    const untrustedPage = await untrusted.newPage();
    await expect(untrustedPage.goto(m.origin, { timeout: 10_000 })).rejects.toThrow(/ERR_CERT/);
  } finally { await untrusted.close(); }
  await signIn(page, "admin@example.local", "admin");
  await page.goto("/projects");
  const panel = page.getByRole("region", { name: "Project backup notifications", exact: true });
  await expect(panel).toBeVisible();
  await panel.getByLabel("Notifications project", { exact: true }).selectOption(m.project_id);
  let configuration: {
    smtp_host: string; smtp_port: number; smtp_security: string;
    smtp_username: string; smtp_password: string; from_address: string;
    recipient_email: string; webhook_url: string;
  };
  try {
    const response = await control("configuration");
    if (response.status !== 200) throw new Error();
    configuration = response.value;
  } catch { throw new Error("private notification configuration unavailable"); }
  for (const type of ["email", "webhook"] as const) {
    await panel.getByRole("button", { name: "Add notification channel", exact: true }).click();
    const form = panel.getByRole("form", { name: "Create notification channel", exact: true });
    await form.getByLabel("Channel name", { exact: true }).fill(`Runtime ${type}`);
    await form.getByLabel("Channel type", { exact: true }).selectOption(type);
    await form.getByLabel("Channel enabled", { exact: true }).check();
    await form.getByLabel("Backup failed", { exact: true }).check();
    await form.getByLabel("Upload failed, local backup available", { exact: true }).check();
    // Do not put secret values in assertion messages or a failed request dump.
    try {
      if (type === "email") {
        await form.getByLabel("SMTP host", { exact: true }).fill(configuration.smtp_host);
        await form.getByLabel("SMTP port", { exact: true }).fill(String(configuration.smtp_port));
        await form.getByLabel("SMTP security", { exact: true }).selectOption(configuration.smtp_security);
        await form.getByLabel("SMTP username", { exact: true }).fill(configuration.smtp_username);
        await form.getByLabel("SMTP password", { exact: true }).fill(configuration.smtp_password);
        await form.getByLabel("From address", { exact: true }).fill(configuration.from_address);
        await form.getByLabel(/ · admin@example\.local$/).check();
      } else {
        await form.getByLabel("Webhook URL", { exact: true }).fill(configuration.webhook_url);
      }
    } catch { throw new Error("notification channel form unavailable"); }
    const saved = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`/api/v1/projects/${m.project_id}/notification-channels`));
    await form.getByRole("button", { name: "Save notification channel", exact: true }).click();
    expect((await saved).status()).toBe(200);
    await expect(form).toHaveCount(0);
    const tested = page.waitForResponse(response => response.request().method() === "POST" && /\/notification-channels\/[^/]+\/tests$/.test(new URL(response.url()).pathname));
    await panel.getByRole("button", { name: `Send test for Runtime ${type}`, exact: true }).click();
    expect((await tested).status()).toBe(202);
  }
  configuration.smtp_password = "";
  configuration.smtp_username = "";
  await acknowledge("configured");
  await expect.poll(async () => (await state()).phase, { timeout: 180_000, intervals: [250, 500] }).toBe("recovery");
  const recoveryState = await state();
  expect(recoveryState.backup_id).toMatch(/^dbb_/);
  const recovery = recoveryState.recovery_url;
  const parsed = new URL(recovery, m.origin);
  expect(parsed.origin).toBe(m.origin);
  expect(parsed.pathname).toBe("/projects");
  expect(parsed.searchParams.get("project_id")).toBe(m.project_id);
  expect(parsed.searchParams.get("resource_id")).toBe(m.database_resource_id);
  expect(parsed.searchParams.get("backup_id")).toBe(recoveryState.backup_id);
  expect(parsed.searchParams.get("transfer_id")).toMatch(/^[A-Za-z0-9_-]+$/);
  await page.reload();
  await panel.getByLabel("Notifications project", { exact: true }).selectOption(m.project_id);
  const history = panel.getByRole("region", { name: "Notification delivery history", exact: true });
  const links = history.getByRole("link", { name: "Open authenticated backup recovery", exact: true });
  await expect.poll(async () => {
    for (const link of await links.all()) if (await link.evaluate(node => (node as HTMLAnchorElement).href) === recovery) return true;
    return false;
  }, { timeout: 30_000 }).toBe(true);
  let sourceMutations = 0;
  page.on("request", request => {
    if (request.method() === "POST" && /\/database-backups\/[^/]+\/(restores|transfers)$/.test(new URL(request.url()).pathname)) sourceMutations++;
  });
  // A viewer can inspect the current scoped history but cannot retry or restore.
  const viewer = await browser.newContext({ baseURL: m.origin });
  try {
    const viewerPage = await viewer.newPage();
    await signIn(viewerPage, "viewer@example.local", "viewer");
    await viewerPage.goto(recovery);
    const viewerRecovery = viewerPage.getByRole("region", { name: "Backup notification recovery", exact: true });
    await expect(viewerRecovery.getByRole("listitem", { name: "Linked backup history", exact: true })).toBeVisible();
    await expect(viewerRecovery.getByRole("button", { name: "Restore into new database", exact: true })).toHaveCount(0);
    await expect(viewerRecovery.getByRole("button", { name: `Retry upload for ${recoveryState.backup_id}`, exact: true })).toHaveCount(0);
    const forged = new URL(recovery, m.origin);
    forged.searchParams.set("project_id", "proj_security");
    await viewerPage.goto(forged.pathname + forged.search);
    await expect(viewerPage.getByRole("listitem", { name: "Linked backup history", exact: true })).toHaveCount(0);
    await expect(viewerPage.getByRole("button", { name: "Restore into new database", exact: true })).toHaveCount(0);
  } finally { await viewer.close(); }
  await page.goto(recovery);
  const recover = page.getByRole("region", { name: "Backup notification recovery", exact: true });
  await expect(recover.getByRole("listitem", { name: "Linked backup history", exact: true })).toBeVisible();
  await expect(recover.getByText("Local logical snapshot published.", { exact: true })).toBeVisible();
  await expect(recover.getByRole("button", { name: `Retry upload for ${recoveryState.backup_id}`, exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(recover).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  expect(sourceMutations).toBe(0);
  await acknowledge("observed");
  await expect(recover.getByLabel("Completed backup", { exact: true })).toHaveValue(recoveryState.backup_id);
  await recover.getByLabel("New database name", { exact: true }).fill("Notification recovered database");
  await recover.getByRole("button", { name: "Restore into new database", exact: true }).click();
  await expect.poll(async () => (await state()).phase, { timeout: 240_000, intervals: [250, 500] }).toBe("complete");
  expect(sourceMutations).toBe(1);
  const complete = await state();
  expect(complete.failed_backup_id).toMatch(/^dbb_/);
  await page.goto("/projects");
  await panel.getByLabel("Notifications project", { exact: true }).selectOption(m.project_id);
  await expect(history).toContainText("Backup failed");
  await expect(history).toContainText("Upload failed, local backup available");
  await acknowledge("done");
});
