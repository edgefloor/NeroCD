import { expect, test } from "@playwright/test";

test("operator can sign in and inspect live control-plane data", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "NeroCD" })).toBeVisible();
  await page.getByRole("button", { name: "Sign in" }).click();

  const nav = page.getByRole("navigation");
  await expect(page.getByRole("cell", { name: "Platform Automation" })).toBeVisible();

  await nav.getByRole("button", { name: "Templates" }).click();
  await expect(page.getByRole("cell", { name: "Patch Linux Fleet" })).toBeVisible();

  await nav.getByRole("button", { name: "Runs" }).click();
  await expect(page.getByRole("cell", { name: "Terraform Plan" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "succeeded" })).toBeVisible();

  await nav.getByRole("button", { name: "Logs" }).click();
  await expect(page.getByText("Initializing OpenTofu working directory")).toBeVisible();

  await nav.getByRole("button", { name: "Audit" }).click();
  await expect(page.getByText("session.create")).toBeVisible();
});
