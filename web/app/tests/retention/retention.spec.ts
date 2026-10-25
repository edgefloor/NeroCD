import { expect, test } from "@playwright/test";
const at = "2026-10-25T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const policy = {
  version: "postgresql.backup-retention.v1",
  project_id: "project",
  resource_id: "database",
  revision: 1,
  resource_generation: 2,
  enabled: true,
  automatic: false,
  local: { keep_count: 5, keep_days: 7, max_bytes: 1073741824 },
  remote: { keep_count: 0, keep_days: 0, max_bytes: 0 },
  created_at: at,
  updated_at: at,
};
const preview = {
  id: "preview",
  version: policy.version,
  project_id: "project",
  resource_id: "database",
  policy_revision: 1,
  plan_hash: hash,
  local_reference_at: at,
  remote_reference_at: at,
  selection_truncated: true,
  items: [
    {
      copy_id: "local_copy",
      backup_id: "backup_old",
      location: "local",
      digest: hash,
      bytes: 1024,
      created_at: at,
      availability: "available",
      selected: true,
      protected_reasons: [],
    },
    {
      copy_id: "remote_copy",
      backup_id: "backup_new",
      location: "remote",
      digest: hash,
      bytes: 2048,
      created_at: at,
      availability: "available",
      selected: false,
      protected_reasons: ["newest", "shared"],
    },
  ],
  created_at: at,
};
const execution = {
  id: "execution",
  version: policy.version,
  project_id: "project",
  resource_id: "database",
  preview_id: "preview",
  policy_revision: 1,
  plan_hash: hash,
  state: "uncertain",
  reservation_held: true,
  local_state: "succeeded",
  remote_state: "uncertain",
  items: [
    {
      copy_id: "local_copy",
      backup_id: "backup_old",
      location: "local",
      state: "deleted",
      failure_code: "",
      reservation_held: false,
    },
  ],
  created_at: at,
  updated_at: at,
};
for (const width of [320, 390, 430, 1440])
  test(`retention confirms exact copies by keyboard at ${width}px`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width, height: 1000 });
    const attempts: unknown[] = [];
    let lost = true;
    let accepted = false;
    // Every API call is fulfilled locally. Any unexpected endpoint fails the test.
    await page.route("**/api/**", async (route) => {
      const req = route.request(),
        path = new URL(req.url()).pathname;
      if (path.endsWith("/database-backup-retention") && req.method() === "GET")
        return route.fulfill({ json: policy });
      if (path.endsWith("/previews") && req.method() === "POST")
        return route.fulfill({ json: preview });
      if (path.endsWith("/executions") && req.method() === "GET")
        return route.fulfill({ json: accepted ? [execution] : [] });
      if (path.endsWith("/executions") && req.method() === "POST") {
        attempts.push(req.postDataJSON());
        if (lost) {
          lost = false;
          return route.abort("failed");
        }
        accepted = true;
        return route.fulfill({ status: 202, json: execution });
      }
      throw new Error(`Unexpected fixture API request ${req.method()} ${path}`);
    });
    await page.goto("/tests/retention/index.html");
    await expect(page.getByLabel("Local maximum storage in GiB")).toHaveValue(
      "1",
    );
    await page
      .getByRole("button", { name: "Preview retention deletion" })
      .click();
    await expect(page.getByText("Protected: newest, shared")).toBeVisible();
    const remove = page.getByRole("button", {
      name: "Delete selected backup copies",
    });
    await expect(remove).toBeDisabled();
    await page
      .getByRole("checkbox", { name: /I confirm permanent deletion/ })
      .focus();
    await page.keyboard.press("Space");
    await remove.focus();
    await page.keyboard.press("Enter");
    await expect(
      page.getByRole("button", { name: "Retry the same retention request" }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Retry the same retention request" })
      .click();
    await expect(
      page.getByText("Local result: succeeded. Remote result: uncertain."),
    ).toBeVisible();
    expect(attempts).toHaveLength(2);
    expect(attempts[1]).toEqual(attempts[0]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`retention-${width}.png`),
      fullPage: true,
    });
  });
test("viewer sees results with no write controls", async ({ page }) => {
  await page.route("**/api/**", (route) => {
    expect(route.request().method()).toBe("GET");
    return route.fulfill({
      json: new URL(route.request().url()).pathname.endsWith("/executions")
        ? [execution]
        : policy,
    });
  });
  await page.goto("/tests/retention/index.html?role=viewer");
  await expect(
    page.getByText("Local result: succeeded. Remote result: uncertain."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", {
      name: /Save retention|Preview retention|Delete selected/,
    }),
  ).toHaveCount(0);
});
