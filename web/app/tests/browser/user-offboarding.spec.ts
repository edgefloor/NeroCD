import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

function browserRuntimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

async function signInAsAdministrator(page: Page): Promise<void> {
  const runtimeDir = browserRuntimeDir();
  const [email, password] = (await readFile(join(runtimeDir, "credentials"), "utf8")).trim().split("\n");
  if (!email || !password) throw new Error("browser smoke credentials are unavailable");
  const status = await page.request.get("/api/v1/bootstrap-status");
  const bootstrap = await status.json() as { status?: string };
  if (bootstrap.status === "required") {
    const databaseURL = await readFile(join(runtimeDir, "database-url"), "utf8");
    execFileSync(join(runtimeDir, "nerocd"), ["bootstrap-admin", "--email", email, "--name", "Browser Administrator", "--password-file", join(runtimeDir, "password")], {
      env: { ...process.env, NEROCD_DATABASE_URL: databaseURL },
      stdio: "pipe",
    });
    await page.reload({ waitUntil: "networkidle" });
  }
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
}

test("administrator offboards an OIDC user and the next provider login is denied", async ({ page }) => {
  await page.goto("/settings");
  await signInAsAdministrator(page);
  await expect(page).toHaveURL(/\/settings/);

  const userEmail = "oidc-browser@example.invalid";
  await page.getByRole("button", { name: `Offboard ${userEmail}` }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("disables local and OIDC sign-in");
  await expect(dialog).toContainText("including automation credentials");
  await expect(dialog).toContainText("Identity mappings and history remain");
  const response = page.waitForResponse((candidate) => new URL(candidate.url()).pathname === "/api/v1/users/offboard" && candidate.request().method() === "POST");
  await dialog.getByRole("button", { name: "Offboard user" }).click();
  expect((await response).status()).toBe(200);

  const userRow = page.getByText(userEmail, { exact: true }).locator("xpath=ancestor::div[contains(@class, 'rounded')][1]");
  await expect(userRow.getByText("disabled", { exact: true })).toBeVisible();
  await expect(userRow.getByRole("button", { name: /offboard/i })).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);

  const evidenceRoot = process.env.NEROCD_BROWSER_EVIDENCE_DIR;
  if (evidenceRoot) {
    const evidenceDir = resolve(evidenceRoot);
    await mkdir(evidenceDir, { recursive: true });
    await page.screenshot({ path: join(evidenceDir, "user-offboarded-mobile.png"), fullPage: true });
  }

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/sign-in/);
  await page.getByRole("link", { name: "Continue with SSO" }).click();
  await expect(page).toHaveURL(/\/sign-in\?oidc_error=failed/);
  await expect(page.getByRole("alert")).toContainText("Single sign-on failed");
  const principal = await page.request.get("/api/v1/me");
  expect(principal.status()).toBe(401);
});
