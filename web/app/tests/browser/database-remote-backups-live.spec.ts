import { expect, test } from "@playwright/test";
// Real cookie API, embedded UI, River, pinned PostgreSQL and compiled runner.
// No API interception or fabricated completion is permitted in this profile.
test.use({ trace: "off", screenshot: "off", video: "off" });
const raw = process.env.NEROCD_DATABASE_CONNECTION_BROWSER_META;
const meta = raw
  ? (JSON.parse(raw) as {
      origin: string;
      engine: "postgresql" | "mysql";
      project_id: string;
      environment: string;
      application_name: string;
      database_resource_id: string;
      control_path: string;
      profile: string;
    })
  : undefined;
test("real remote database denial retry and supported restore through embedded UI", async ({
  page,
}) => {
  test.skip(
    !meta || meta.profile !== "backup_remote",
    "requires exact authorized backup browser fixture",
  );
  const m = meta!;
  test.setTimeout(300_000);
  const headers = { Origin: m.origin, "X-NeroCD-CSRF": "1" };
  const safe = async (path: string) => {
    try {
      return await page.request.get(path, { headers, timeout: 5000 });
    } catch {
      throw new Error("live backup control request failed");
    }
  };
  const status = await safe("/api/v1/bootstrap-status");
  expect(status.status()).toBe(200);
  expect(await status.json()).toEqual({ status: "complete" });
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill("admin@example.local");
  await page.getByLabel("Password", { exact: true }).fill("admin");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Account", exact: true }),
  ).toBeVisible();
  await page.goto("/projects");
  const hosting = page.getByRole("region", {
    name: "Project hosting",
    exact: true,
  });
  await hosting
    .getByLabel("Hosting project", { exact: true })
    .selectOption(m.project_id);
  await hosting
    .getByRole("button", { name: m.environment, exact: true })
    .click();
  const detail = hosting.getByRole("region", {
    name: `Database detail for ${m.application_name}`,
    exact: true,
  });
  const metadata = detail.getByRole("region", {
    name: `Database metadata for ${m.application_name}`,
    exact: true,
  });
  await expect(metadata).toBeVisible();
  const panel = detail.getByRole("region", {
    name: "Database backups",
    exact: true,
  });
  await expect(panel).toBeVisible();
  const destination = panel.getByLabel("Immediate backup destination", { exact: true });
  await expect(destination.locator("option").filter({ hasText: "Runtime S3" })).toHaveCount(1);
  const target = await destination.locator("option").filter({ hasText: "Runtime S3" }).getAttribute("value");
  expect(target).toMatch(/^[A-Za-z0-9_-]+:1$/);
  if (m.engine === "mysql") {
    const schedules = panel.getByRole("region", { name: "Database backup schedules", exact: true });
    await schedules.getByLabel("Backup frequency", { exact: true }).fill("every_minute");
    await schedules.getByLabel("Backup timezone", { exact: true }).fill("UTC");
    await schedules.getByLabel("Scheduled backup destination", { exact: true }).selectOption(target!);
    const schedulePath = `/api/v1/resources/${m.database_resource_id}/database-backup-schedules`;
    const create = page.waitForRequest(r => new URL(r.url()).pathname === schedulePath && r.method() === "POST");
    await schedules.getByRole("button", { name: "Create backup schedule", exact: true }).click();
    const body = (await create).postDataJSON();
    expect(body.remote_target).toEqual({ target_id: target!.split(":")[0], revision: 1 });
    expect(body).not.toHaveProperty("source_component");
    expect(body).not.toHaveProperty("expected_deployed_operation_id");
    await schedules.getByRole("button", { name: "Retry the same schedule request", exact: true }).click();
    await expect(schedules.getByText(/Saved schedule .* revision 1\./)).toBeVisible();
    await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 120_000 }).toBe("scheduled");
    await schedules.getByRole("button", { name: "Disable schedule", exact: true }).click();
    await expect(schedules.getByText(/Saved schedule .* revision 2\./)).toBeVisible();
    await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 90_000 }).toBe("backup");
  }
  await destination.selectOption(target!);
  await panel.getByRole("button", { name: "Back up now", exact: true }).click();
  let backup = "";
  await expect
    .poll(
      async () => {
        const r = await safe(m.control_path + "/state");
        if (r.status() !== 200) return "unavailable";
        const v = await r.json();
        backup = v.operation_id;
        return v.phase;
      },
      { timeout: 120_000, intervals: [100, 250, 500] },
    )
    .toBe("denied");
  expect(backup).toMatch(/^dbb_/);
  await panel
    .getByRole("button", { name: "Refresh backup history", exact: true })
    .click();
  await expect(
    panel.getByText("Local logical snapshot published.", { exact: true }).first(),
  ).toBeVisible({ timeout: 30_000 });
  const warning = panel.getByRole("alert").filter({ hasText: "Remote upload failed: access_denied" });
  await expect(warning).toContainText("Retry uploads the same archive and object key without another dump.");
  await page.reload();
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(m.project_id);
  await hosting.getByRole("button", { name: m.environment, exact: true }).click();
  await expect(warning).toBeVisible({ timeout: 30_000 });
  await expect(panel.getByText("Local logical snapshot published.", { exact: true }).first()).toBeVisible();
  const acknowledged = await page.request.post(m.control_path + "/warning-seen", { headers, timeout: 5000 });
  expect(acknowledged.status()).toBe(204);
  await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 90_000 }).toBe("retry");
  await panel.getByRole("button", { name: `Retry upload for ${backup}`, exact: true }).click();
  await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 120_000 }).toBe(m.engine === "mysql" ? "complete" : "restore");
  if (m.engine === "mysql") {
    const uploads = panel.getByLabel(`Remote uploads for ${backup}`, { exact: true });
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await panel.getByRole("button", { name: "Refresh backup history", exact: true }).click();
      await expect(uploads.getByText("Local dump: succeeded. Latest remote upload: succeeded.", { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
    await panel.getByLabel("Completed backup", { exact: true }).selectOption(backup);
    await expect(panel.getByLabel("Restore archive source", { exact: true })).toHaveCount(0);
    expect((await page.request.post(m.control_path + "/done", { headers, timeout: 5000 })).status()).toBe(204);
    return;
  }
  await panel.getByRole("button", { name: "Refresh backup history", exact: true }).click();
  await panel
    .getByLabel("Completed backup", { exact: true })
    .selectOption(backup);
  const archive = panel.getByLabel("Restore archive source", { exact: true });
  const saved = archive.locator("option").filter({ hasText: "Saved remote object" });
  await expect(saved).toHaveCount(1);
  const transfer = await saved.getAttribute("value");
  expect(transfer).toMatch(/^[A-Za-z0-9_-]+$/);
  await archive.selectOption(transfer!);
  await panel
    .getByLabel("New database name", { exact: true })
    .fill("Restored application database");
  await panel
    .getByRole("button", { name: "Restore into new database", exact: true })
    .click();
  let restored = "";
  await expect
    .poll(
      async () => {
        const r = await safe(m.control_path + "/state");
        if (r.status() !== 200) return "unavailable";
        const v = await r.json();
        restored = v.operation_id;
        return v.phase;
      },
      { timeout: 120_000, intervals: [100, 250, 500] },
    )
    .toBe("complete");
  expect(restored).toMatch(/^dbr_/);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await panel
      .getByRole("button", { name: "Refresh backup history", exact: true })
      .click();
    await expect(
      panel.getByText(
        "Import and readiness succeeded. Refresh project resources to select the restored database for a distinct application.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      panel.getByText(
        new RegExp(`New-database restore · succeeded · ${restored}`),
      ),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  try {
    const response = await page.request.post(m.control_path + "/done", {
      headers,
      timeout: 5000,
    });
    expect(response.status()).toBe(204);
  } catch {
    throw new Error("live backup completion acknowledgement failed");
  }
});
