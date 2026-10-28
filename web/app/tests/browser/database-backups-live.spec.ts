import { expect, test } from "@playwright/test";
// Real cookie API, embedded UI, River, pinned database and compiled runner.
// No API interception or fabricated completion is permitted in this profile.
test.use({ trace: "off", screenshot: "off", video: "off" });
const raw = process.env.NEROCD_DATABASE_CONNECTION_BROWSER_META;
const meta = raw
  ? (JSON.parse(raw) as {
      origin: string;
      project_id: string;
      environment: string;
      application_name: string;
      database_resource_id: string;
      control_path: string;
      profile: string;
    })
  : undefined;
test("real hosted database backup and new-database restore through embedded UI", async ({
  page,
}) => {
  test.skip(
    !meta || meta.profile !== "backup",
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
    .toBe("restore");
  expect(backup).toMatch(/^dbb_/);
  await panel
    .getByRole("button", { name: "Refresh backup history", exact: true })
    .click();
  await expect(
    panel.getByText("Local logical snapshot published.", { exact: true }),
  ).toBeVisible({ timeout: 30_000 });
  await panel
    .getByLabel("Completed backup", { exact: true })
    .selectOption(backup);
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
