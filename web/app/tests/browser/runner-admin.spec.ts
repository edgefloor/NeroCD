import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function browserRuntimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

function bootstrapAdmin(): void {
  const runtimeDir = browserRuntimeDir();
  const [email] = readFileSync(join(runtimeDir, "credentials"), "utf8").trim().split("\n");
  const databaseURL = readFileSync(join(runtimeDir, "database-url"), "utf8");
  if (!email || !databaseURL) throw new Error("browser bootstrap fixture is unavailable");
  execFileSync(join(runtimeDir, "nerocd"), ["bootstrap-admin", "--email", email, "--name", "Browser Administrator", "--password-file", join(runtimeDir, "password")], {
    env: { ...process.env, NEROCD_DATABASE_URL: databaseURL },
    stdio: "pipe",
  });
}

async function signIn(page: Page): Promise<void> {
  await page.goto("/sign-in");
  const status = await page.request.get("/api/v1/bootstrap-status");
  const body = await status.json() as { status?: string };
  if (body.status === "required") {
    bootstrapAdmin();
    await page.reload({ waitUntil: "networkidle" });
  }
  const [email, password] = readFileSync(join(browserRuntimeDir(), "credentials"), "utf8").trim().split("\n");
  if (!email || !password) throw new Error("browser smoke credentials are unavailable");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}

async function postJSON<T>(page: Page, path: string, data: unknown): Promise<T> {
  const response = await page.request.post(path, { data, headers: { Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" } });
  if (!response.ok()) throw new Error(`browser fixture request ${path} failed with ${response.status()}: ${(await response.text()).slice(0, 256)}`);
  return await response.json() as T;
}

async function createRunnerAdmin(page: Page): Promise<void> {
  await signIn(page);
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const created = await postJSON<{ token: string }>(page, "/api/v1/api-tokens", { name: `Browser runner admin ${suffix}`, kind: "bootstrap", roles: ["runner_admin"] });
  await page.context().setExtraHTTPHeaders({ Authorization: `Bearer ${created.token}` });
  const me = await page.request.get("/api/v1/me");
  expect(me.ok()).toBeTruthy();
  expect(await me.json()).toMatchObject({ provider: "api_token", roles: ["runner_admin"] });
}

async function registerRunner(page: Page): Promise<{ id: string; name: string }> {
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const id = `runner_browser_${suffix}`;
  const name = `Browser runner ${suffix}`;
  await postJSON(page, "/api/v1/runners/register", { id, name, tags: ["browser"], capabilities: ["compose-deploy"] });
  return { id, name };
}

test("runner_admin principals can operate runner list and detail controls at desktop and mobile widths", async ({ page }) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);

  await page.goto("/runners", { waitUntil: "networkidle" });
  await expect(page.getByText("Create one-time enrollment", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: new RegExp(runner.name) })).toBeVisible();

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("link", { name: new RegExp(runner.name) }).click();
  await expect(page).toHaveURL(new RegExp(`/runners/${runner.id}`));
  await page.getByRole("button", { name: "Revoke credential" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog", { name: "Revoke runner credential?" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Revoke runner credential?" })).toHaveCount(0);

  await page.goto("/runners", { waitUntil: "networkidle" });
  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    const enrollmentID = `runner_mobile_${width}_${Date.now()}${Math.floor(Math.random() * 10_000)}`;
    await page.getByLabel("Runner ID").fill(enrollmentID);
    await page.getByLabel("Runner name").fill(`Mobile runner ${width}`);
    await page.getByRole("button", { name: "Create enrollment" }).press("Enter");
    await expect(page.getByRole("dialog", { name: "One-time runner enrollment secret" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.keyboard.press("Escape");
  }
});

test("OIDC viewer principals receive runner 403 responses and no runner controls", async ({ browser }) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  try {
    await page.goto("/sign-in");
    await page.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    const me = await page.request.get("/api/v1/me");
    expect(me.ok()).toBeTruthy();
    expect(await me.json()).toMatchObject({ email: "oidc-browser@example.invalid", roles: [] });
    const runners = await page.request.get("/api/v1/runners");
    expect(runners.status()).toBe(403);
    await page.goto("/runners", { waitUntil: "domcontentloaded" });
    await expect(page.getByText("Something went wrong!", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Create enrollment" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Revoke credential" })).toHaveCount(0);
  } finally {
    await context.close();
  }
});
