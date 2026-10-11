import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const offboardingEmail = "oidc-offboarding@example.invalid";

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
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}

async function provisionFirstPageUsers(): Promise<void> {
  const runtimeDir = browserRuntimeDir();
  const databaseURL = await readFile(join(runtimeDir, "database-url"), "utf8");
  const provider = JSON.parse(await readFile(join(runtimeDir, "oidc-provider.json"), "utf8")) as { issuer?: string };
  if (!provider.issuer) throw new Error("browser OIDC issuer is unavailable");
  for (let index = 0; index < 10; index += 1) {
    execFileSync(join(runtimeDir, "nerocd"), [
      "oidc-provision",
      "--issuer", provider.issuer,
      "--subject", `pagination-subject-${index}`,
      "--email", `pagination-${index}@example.invalid`,
      "--name", `Pagination User ${index}`,
    ], { env: { ...process.env, NEROCD_DATABASE_URL: databaseURL }, stdio: "pipe" });
  }
}

async function selectOffboardingOIDCPersona(page: Page): Promise<{ used: boolean }> {
  const provider = JSON.parse(await readFile(join(browserRuntimeDir(), "oidc-provider.json"), "utf8")) as { issuer?: string };
  if (!provider.issuer) throw new Error("browser OIDC issuer is unavailable");
  const issuer = new URL(provider.issuer);
  const appOrigin = new URL(page.url()).origin;
  const escapedAppOrigin = appOrigin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const selection = { used: false };
  await page.route(
    new RegExp(`^${escapedAppOrigin}/api/v1/oidc/login(?:\\?|$)`),
    async (route) => {
      const response = await route.fetch({ maxRedirects: 0 });
      if (response.status() !== 302) throw new Error(`browser OIDC login returned ${response.status()}, want 302`);
      const location = response.headers().location;
      if (!location) throw new Error("browser OIDC login response has no authorization redirect");
      const authorizationURL = new URL(location, appOrigin);
      if (authorizationURL.origin !== issuer.origin || authorizationURL.pathname !== "/authorize") throw new Error("browser OIDC login response has an unexpected authorization endpoint");
      authorizationURL.searchParams.set("login_hint", offboardingEmail);
      selection.used = true;
      await route.fulfill({ response, status: response.status(), headers: { ...response.headers(), location: authorizationURL.toString() } });
    },
    { times: 1 },
  );
  return selection;
}

test("administrator offboards an OIDC user and the next provider login is denied", async ({ page }) => {
  // Allow time for ten serial CLI provisions and the full offboarding/OIDC flow.
  test.setTimeout(45_000);
  await page.goto("/settings");
  await signInAsAdministrator(page);
  await expect(page).toHaveURL(/\/settings/);
  await provisionFirstPageUsers();
  await page.reload({ waitUntil: "networkidle" });

  const userEmail = offboardingEmail;
  const userPages = page.getByRole("navigation", { name: "User pages" });
  await expect(page.getByText(userEmail, { exact: true })).toHaveCount(0);
  const firstPageSummary = await userPages.locator("p").innerText();
  expect(firstPageSummary).toMatch(/^Showing 1–10 of \d+$/);
  await expect(userPages.getByRole("button", { name: "Next users" })).toBeEnabled();
  await userPages.getByRole("button", { name: "Next users" }).click();
  await expect(page.getByText(userEmail, { exact: true })).toBeVisible();
  const laterPageSummary = await userPages.locator("p").innerText();
  expect(laterPageSummary).toMatch(/^Showing 11–\d+ of \d+$/);
  expect(laterPageSummary).not.toBe(firstPageSummary);
  const offboardButton = page.getByRole("button", { name: `Offboard ${userEmail}` });
  await expect(offboardButton).toBeEnabled();
  await offboardButton.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("disables local and OIDC sign-in");
  await expect(dialog).toContainText("disables every service account they own");
  await expect(dialog).toContainText("Identity mappings and history remain");
  const response = page.waitForResponse((candidate) => new URL(candidate.url()).pathname === "/api/v1/users/offboard" && candidate.request().method() === "POST");
  await dialog.getByRole("button", { name: "Offboard user" }).click();
  expect((await response).status()).toBe(200);

  const userRow = page.getByText(userEmail, { exact: true }).locator("xpath=ancestor::div[contains(@class, 'rounded')][1]");
  await expect(userRow.getByText("disabled", { exact: true })).toBeVisible();
  await expect(userRow.getByRole("button", { name: /offboard/i })).toHaveCount(0);
  await expect(userPages.locator("p")).toHaveText(laterPageSummary);
  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await expect(page.getByRole("navigation", { name: "User pages" })).toBeVisible();
  }

  const evidenceRoot = process.env.NEROCD_BROWSER_EVIDENCE_DIR;
  if (evidenceRoot) {
    const evidenceDir = resolve(evidenceRoot);
    await mkdir(evidenceDir, { recursive: true });
    await page.screenshot({ path: join(evidenceDir, "user-offboarded-mobile.png"), fullPage: true });
  }

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(/\/sign-in/);
  const personaSelection = await selectOffboardingOIDCPersona(page);
  await page.getByRole("link", { name: "Continue with SSO" }).click();
  expect(personaSelection.used).toBe(true);
  await expect(page).toHaveURL(/\/sign-in\?oidc_error=failed/);
  await expect(page.getByRole("alert")).toContainText("Single sign-on failed");
  const principal = await page.request.get("/api/v1/me");
  expect(principal.status()).toBe(401);
});
