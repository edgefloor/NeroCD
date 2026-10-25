import { expect, test } from "@playwright/test";
// Real cookie API, embedded UI, River, pinned PostgreSQL and compiled runner.
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
test("real PostgreSQL schedule creation replay edit disable and application-verified restore", async ({
  page,
}) => {
  test.skip(
    !meta || meta.profile !== "backup_schedule",
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
  const schedules = panel.getByRole("region", {
    name: "Database backup schedules",
    exact: true,
  });
  const schedulePath = `/api/v1/resources/${m.database_resource_id}/database-backup-schedules`;
  const submitted: unknown[] = [];
  page.on("request", (request) => {
    if (
      new URL(request.url()).pathname === schedulePath &&
      request.method() === "POST"
    )
      submitted.push(request.postDataJSON());
  });
  await schedules
    .getByLabel("Backup frequency", { exact: true })
    .fill("every_minute");
  await schedules.getByLabel("Backup timezone", { exact: true }).fill("UTC");
  await schedules
    .getByRole("button", { name: "Create backup schedule", exact: true })
    .click();
  await schedules
    .getByRole("button", {
      name: "Retry the same schedule request",
      exact: true,
    })
    .click();
  await expect(
    schedules.getByText(/Saved schedule .* revision 1\./),
  ).toBeVisible();
  expect(submitted).toHaveLength(2);
  expect(submitted[0]).toEqual(submitted[1]);
  // Leave the reviewed first revision enabled until the actual background
  // controller admits its operation. The Go fixture never drives a manual tick.
  await expect
    .poll(
      async () => {
        const response = await safe(m.control_path + "/state");
        if (response.status() !== 200) return "unavailable";
        return (await response.json()).operation_id;
      },
      { timeout: 120_000, intervals: [100, 250, 500] },
    )
    .toMatch(/^dbb_/);
  await schedules
    .getByRole("button", { name: "Edit schedule", exact: true })
    .click();
  await schedules
    .getByLabel("Backup frequency", { exact: true })
    .fill("* * * * *");
  await schedules
    .getByLabel("Backup timezone", { exact: true })
    .fill("Etc/UTC");
  await schedules
    .getByRole("button", { name: "Save schedule changes", exact: true })
    .click();
  await expect(
    schedules.getByText(/Saved schedule .* revision 2\./),
  ).toBeVisible();
  await schedules
    .getByRole("button", { name: "Disable schedule", exact: true })
    .click();
  await expect(
    schedules.getByText(/Saved schedule .* revision 3\./),
  ).toBeVisible();
  await expect(schedules.getByText(/revision 3 · Disabled/)).toBeVisible();
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
  const ownerCookies = await page.context().cookies();
  await page.context().clearCookies();
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill("viewer@example.local");
  await page.getByLabel("Password", { exact: true }).fill("viewer");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Account", exact: true }),
  ).toBeVisible();
  await page.goto("/projects");
  await hosting
    .getByLabel("Hosting project", { exact: true })
    .selectOption(m.project_id);
  await hosting
    .getByRole("button", { name: m.environment, exact: true })
    .click();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(schedules.getByText(/revision 3 · Disabled/)).toBeVisible();
    await expect(
      schedules.getByRole("button", { name: "Edit schedule", exact: true }),
    ).toHaveCount(0);
    await expect(
      schedules.getByRole("button", {
        name: "Create backup schedule",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  const before = await safe(schedulePath);
  expect(before.status()).toBe(200);
  const beforeBody: unknown = await before.json();
  const denied = await page.request.post(schedulePath, {
    headers,
    data: submitted[0],
    timeout: 5000,
  });
  expect(denied.status()).toBe(403);
  const after = await safe(schedulePath);
  expect(after.status()).toBe(200);
  expect(await after.json()).toEqual(beforeBody);
  await page.context().clearCookies();
  await page.context().addCookies(ownerCookies);
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
