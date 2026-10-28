import { expect, test } from "@playwright/test";
// Real cookie API, embedded UI, River, pinned PostgreSQL/MySQL and compiled runner.
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
test("real independent local and remote retention through embedded UI", async ({
  page,
}) => {
  test.skip(
    !meta || !["backup_retention", "backup_retention_recovery"].includes(meta.profile),
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
  const retention = panel.getByRole("region", { name: "Database backup retention", exact: true });
  await expect(retention).toBeVisible();
  const responsive = async (history = false) => {
    if(m.engine!=="mysql")return;
    for(const width of [320,390,430,1440]) {
      await page.setViewportSize({width,height:900});
      await expect(retention).toBeVisible();
      await expect(retention.getByLabel("Local backups to keep",{exact:true})).toBeVisible();
      await expect(retention.getByLabel("Remote backups to keep",{exact:true})).toBeVisible();
      await expect(retention.getByRole("button",{name:"Refresh retention history",exact:true})).toBeVisible();
      if(history)await expect(retention.getByText("Local result: succeeded. Remote result: succeeded.",{exact:true})).toBeVisible();
      const fits=await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth);
      expect(fits).toBe(true);
    }
  };
  await responsive();
  if (m.profile === "backup_retention_recovery") {
    // Reload discards React's original preview and request state. The action
    // must reconstruct the exact saved batch from public execution history.
    await page.reload();
    await hosting.getByLabel("Hosting project", { exact: true }).selectOption(m.project_id);
    await hosting.getByRole("button", { name: m.environment, exact: true }).click();
    await expect(retention).toBeVisible();
    await retention.getByRole("button", { name: "Retry this batch", exact: true }).click();
    const confirm=retention.getByRole("button", { name:"Confirm retry of this batch", exact:true });
    await expect(confirm).toBeDisabled();
    await retention.getByLabel("I confirm continuing only this saved batch", { exact:true }).check();
    await confirm.click();
    await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout:120_000 }).toBe("complete");
    await retention.getByRole("button", { name:"Refresh retention history", exact:true }).click();
    await expect(retention.getByText("Local result: succeeded. Remote result: succeeded.", { exact:true })).toBeVisible();
    await responsive(true);
    const done=await page.request.post(m.control_path+"/done", {headers,timeout:5000});expect(done.status()).toBe(204);
    return;
  }
  await retention.getByLabel("Retention enabled", { exact: true }).check();
  await retention.getByLabel("Local backups to keep", { exact: true }).fill("2");
  await retention.getByLabel("Remote backups to keep", { exact: true }).fill("1");
  for (const label of ["Local days to keep", "Remote days to keep", "Local maximum storage in GiB", "Remote maximum storage in GiB"]) {
    await expect(retention.getByLabel(label, { exact: true })).toHaveValue("0");
  }
  await retention.getByRole("button", { name: "Save retention policy", exact: true }).click();
  await expect(retention.getByText("Saved retention revision 1.", { exact: true })).toBeVisible();
  await retention.getByRole("button", { name: "Preview retention deletion", exact: true }).click();
  const preview = retention.getByRole("region", { name: "Retention deletion preview", exact: true });
  await expect(preview).toBeVisible();
  await expect(preview.getByText("Selected for deletion:", { exact: false })).toHaveCount(5);
  await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 90_000 }).toBe("retention_execute");
  const execute = preview.getByRole("button", { name: "Delete selected backup copies", exact: true });
  await expect(execute).toBeDisabled();
  await preview.getByRole("checkbox", { name: "I confirm permanent deletion of exactly these 5 selected copies at their listed local or remote locations.", exact: true }).check();
  await execute.click();
  await expect.poll(async () => (await (await safe(m.control_path + "/state")).json()).phase, { timeout: 120_000 }).toBe("complete");
  await retention.getByRole("button", { name: "Refresh retention history", exact: true }).click();
  await expect(retention.getByText("Local result: succeeded. Remote result: succeeded.", { exact: true })).toBeVisible();
  await responsive(true);
  const done = await page.request.post(m.control_path + "/done", { headers, timeout: 5000 });
  expect(done.status()).toBe(204);
});
