import { expect, test } from "@playwright/test";
const at = "2026-10-26T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const preview = {
  id: "preview",
  origin: "manual_selection",
  version: "owned-upload.backup-deletion.v1",
  project_id: "project",
  resource_id: "resource",
  backup_id: "backup",
  resource_generation: 2,
  final_copy: true,
  policy_revision: 0,
  plan_hash: hash,
  evaluated_at: at,
  local_reference_at: null,
  remote_reference_at: null,
  selection_truncated: false,
  items: [
    {
      copy_id: "local",
      backup_id: "backup",
      location: "local",
      digest: hash,
      bytes: 1024,
      created_at: at,
      availability: "available",
      selected: true,
      protected_reasons: [],
    },
  ],
  created_at: at,
};
const execution = {
  id: "execution",
  origin: "manual_selection",
  version: preview.version,
  project_id: "project",
  resource_id: "resource",
  backup_id: "backup",
  resource_generation: 2,
  final_copy: true,
  policy_revision: 0,
  preview_id: "preview",
  plan_hash: hash,
  state: "succeeded",
  reservation_held: false,
  local_state: "succeeded",
  remote_state: "not_selected",
  items: [
    {
      copy_id: "local",
      backup_id: "backup",
      location: "local",
      state: "deleted",
      failure_code: "",
      reservation_held: false,
    },
  ],
  created_at: at,
  updated_at: at,
};
for (const width of [320, 390, 430, 1280])
  test(`manual copy deletion review and exact retry at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    let saved: unknown,
      posts = 0,
      settled = false;
    await page.route(
      "**/api/v1/resources/resource/storage-backups/backup/**",
      async (route) => {
        const req = route.request();
        if (req.url().endsWith("/deletion-previews")) {
          expect(req.postDataJSON().copy_ids).toEqual(["local"]);
          await route.fulfill({ json: preview });
          return;
        }
        if (req.method() === "GET") {
          await route.fulfill({ json: settled ? [execution] : [] });
          return;
        }
        posts++;
        if (posts === 1) {
          saved = req.postDataJSON();
          settled = true;
          await route.abort("failed");
          return;
        }
        expect(req.postDataJSON()).toEqual(saved);
        await route.fulfill({ status: 202, json: execution });
      },
    );
    await page.goto("/tests/retention/index.html?panel=deletion");
    await page.getByLabel("Delete local copy local").check();
    await page
      .getByRole("button", { name: "Review selected copy deletion" })
      .click();
    const execute = page.getByRole("button", {
      name: "Permanently delete reviewed copies",
    });
    await expect(execute).toBeDisabled();
    await page
      .getByLabel(
        "I understand this deletes the final recoverable copy of this backup.",
      )
      .check();
    await execute.click();
    await page
      .getByRole("button", { name: "Retry identical deletion request" })
      .click();
    await expect(
      page.getByText("Deletion execution: succeeded.", { exact: false }),
    ).toBeVisible();
    expect(posts).toBe(2);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
test("viewer reads deletion history without destructive controls", async ({
  page,
}) => {
  await page.route(
    "**/api/v1/resources/resource/storage-backups/backup/deletions",
    (route) => route.fulfill({ json: [execution] }),
  );
  await page.goto("/tests/retention/index.html?panel=deletion&role=viewer");
  await expect(
    page.getByText("Deletion execution: succeeded.", { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Review selected copy deletion" }),
  ).toHaveCount(0);
});
test("server denial removes review controls", async ({ page }) => {
  await page.route(
    "**/api/v1/resources/resource/storage-backups/backup/**",
    (route) => route.fulfill({ status: 403, json: { error: "denied" } }),
  );
  await page.goto("/tests/retention/index.html?panel=deletion");
  await expect(page.getByRole("alert")).toContainText("Deletion access denied");
  await expect(
    page.getByRole("button", { name: "Permanently delete reviewed copies" }),
  ).toHaveCount(0);
});
