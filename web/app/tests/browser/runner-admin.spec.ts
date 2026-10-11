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
    await expect(page.getByRole("alert")).toHaveText("Runner inventory is unavailable.");
    await expect(page.getByRole("button", { name: "Create enrollment" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Revoke credential" })).toHaveCount(0);
  } finally {
    await context.close();
  }
});

test("runner details recover from a failed request and retain the list filter", async ({ page }) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);
  let fail = true;
  await page.route(`**/api/v1/runners/${runner.id}`, (route) => fail
    ? route.fulfill({ status: 503, json: { error: "Temporary runner failure" } })
    : route.continue());
  await page.goto(`/runners/${runner.id}?q=browser`);
  await expect(page.getByRole("alert")).toHaveText("Runner is unavailable.");
  await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toHaveCount(0);
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByText(runner.name, { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toBeVisible();
  await page.goto("/runners/runner_missing?q=browser");
  await expect(page.getByRole("alert")).toHaveText("Runner is unavailable.");
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(page.getByRole("link", { name: "Back to runners", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  }
  await page.getByRole("link", { name: "Back to runners", exact: true }).click();
  await expect(page).toHaveURL((url) => url.pathname === "/runners" && url.searchParams.get("q") === "browser");
});

test("runner credential revocation reports failure and succeeds on retry", async ({ page }, testInfo) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  let fail = true;
  await page.route("**/api/v1/runners/revoke-token", (route) => fail
    ? route.fulfill({ status: 503, json: { error: "Temporary revocation failure" } })
    : route.continue());
  await page.goto(`/runners/${runner.id}`);
  await page.getByRole("button", { name: "Revoke credential", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Revoke runner credential?" });
  await dialog.getByRole("button", { name: "Revoke credential", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("Unable to revoke credential. Try again.");
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(dialog.getByRole("button", { name: "Revoke credential", exact: true })).toBeEnabled();
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("runner-revoke-error-320.png"), fullPage: false });
  }
  fail = false;
  await dialog.getByRole("button", { name: "Revoke credential", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("Credential is revoked.", { exact: true })).toBeVisible();
  expect(pageErrors).toEqual([]);
});


test("runner inventory retries the selected page without losing its filter", async ({ page }) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);
  const response = await page.request.get(`/api/v1/runners/${runner.id}`);
  const record = await response.json();
  let fail = true;
  const offsets: number[] = [];
  await page.route(/\/api\/v1\/runners(?:\?.*)?$/, (route) => {
    const url = new URL(route.request().url());
    offsets.push(Number(url.searchParams.get("offset") ?? 0));
    if (url.searchParams.get("offset") !== "50") return route.continue();
    return fail ? route.fulfill({ status: 503, json: { error: "Temporary inventory failure" } })
      : route.fulfill({ json: { items: [record], count: 1, total: 51, limit: 50, offset: 50 } });
  });
  await page.goto("/runners?q=browser&offset=50");
  await expect(page.getByRole("alert")).toHaveText("Runner inventory is unavailable.");
  expect(offsets.length).toBeGreaterThan(0);
  expect(offsets.every((offset) => offset === 50)).toBe(true);
  await expect(page.getByText("No runners match this view", { exact: true })).toHaveCount(0);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeEnabled();
  }
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("link", { name: new RegExp(runner.name) })).toBeVisible();
  await expect(page.getByText("Showing 51-51 of 51", { exact: true })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toHaveValue("browser");
  await expect(page).toHaveURL((url) => url.pathname === "/runners" && url.searchParams.get("offset") === "50");
});

test("runners registered without tags remain filterable and open normally", async ({ page }) => {
  await createRunnerAdmin(page);
  const id = `runner_untagged_${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  await postJSON(page, "/api/v1/runners/register", { id, name: "Untagged runner", tags: [], capabilities: ["compose-deploy"] });
  const response = await page.request.get(`/api/v1/runners/${id}`);
  expect(response.ok()).toBe(true);
  expect((await response.json()).tags ?? []).toEqual([]);
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/runners?q=${id}`);
    const link = page.getByRole("link", { name: /Untagged runner/ });
    await expect(link).toContainText("no tags");
    await link.click();
    await expect(page.getByText("Untagged runner", { exact: true })).toBeVisible();
    await expect(page.getByText("None", { exact: true })).toBeVisible();
    await expect(page.getByText("compose-deploy", { exact: true })).toBeVisible();
  }
  expect(pageErrors).toEqual([]);
});

test("runner detail observes external credential revocation without reloading", async ({ page }) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);
  await page.goto(`/runners/${runner.id}`);
  await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Revoke credential", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Revoke runner credential?" })).toBeVisible();
  await postJSON(page, "/api/v1/runners/revoke-token", { runner_id: runner.id });
  await expect(page.getByRole("dialog", { name: "Revoke runner credential?" })).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByText("Credential is revoked.", { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toHaveCount(0);
});

test("runner refresh failure preserves an open enrollment dialog", async ({ page }) => {
  await createRunnerAdmin(page);
  let fail = false;
  await page.route("**/api/v1/runners?*", (route) => fail
    ? route.fulfill({ status: 503, json: { error: "Temporary refresh failure" } })
    : route.continue());
  await page.goto("/runners");
  await page.getByLabel("Runner ID").fill(`runner_refresh_${Date.now()}`);
  await page.getByLabel("Runner name").fill("Refresh enrollment");
  await page.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "One-time runner enrollment secret" });
  await expect(dialog).toBeVisible();
  fail = true;
  await expect(page.locator("section > p[role=alert]")).toHaveText("Runner inventory is unavailable.", { timeout: 15_000 });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel("Enrollment secret")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Create enrollment", exact: true })).toBeDisabled();
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create enrollment", exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByLabel("Runner name")).toHaveValue("Refresh enrollment");
});

test("runner heartbeat ages into stale status when refreshed data is unchanged", async ({ page }) => {
  await createRunnerAdmin(page);
  const runner = await registerRunner(page);
  const response = await page.request.get(`/api/v1/runners/${runner.id}`);
  const record = { ...await response.json(), last_heartbeat_at: new Date(Date.now() - 115_000).toISOString() };
  let requests = 0;
  await page.route(`**/api/v1/runners/${runner.id}`, (route) => {
    requests += 1;
    return route.fulfill({ json: record });
  });
  await page.goto(`/runners/${runner.id}`);
  await expect(page.getByText("Active and recently observed.", { exact: true })).toBeVisible();
  const initialRequests = requests;
  await expect(page.getByText("Heartbeat is stale; wait for recovery or inspect runner connectivity.", { exact: true })).toBeVisible({ timeout: 15_000 });
  expect(requests).toBeGreaterThan(initialRequests);
});

test("long runner identities and tags stay contained on mobile", async ({ page }, testInfo) => {
  await createRunnerAdmin(page);
  const id = `runner_${"edge".repeat(24)}_${Date.now()}`;
  const name = `Runner-${"longname".repeat(15)}`;
  const tag = `pool-${"remote".repeat(15)}`;
  await postJSON(page, "/api/v1/runners/register", { id, name, tags: [tag], capabilities: ["compose-deploy"] });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/runners?q=${id}`);
    const link = page.getByRole("link", { name: new RegExp(name) });
    await expect(link).toBeVisible();
    expect(await link.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    const bounds = await link.boundingBox();
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    await link.click();
    await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toBeVisible();
    await expect(page.getByText(name, { exact: true })).toBeVisible();
    const card = page.locator('[data-slot="card"]').filter({ has: page.getByText(name, { exact: true }) });
    expect(await card.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    const detailBounds = await card.boundingBox();
    expect(detailBounds!.x + detailBounds!.width).toBeLessThanOrEqual(width);
    await expect(page.getByRole("button", { name: "Revoke credential", exact: true })).toBeVisible();
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("long-runner-320.png"), fullPage: true });
  }
});


test("pending enrollment preserves its draft and recovers after failure", async ({ page }, testInfo) => {
  await createRunnerAdmin(page);
  await page.goto("/runners");
  const form = page.getByRole("form", { name: "Create runner enrollment" });
  const id = `runner_pending_${Date.now()}`;
  await form.getByLabel("Runner ID", { exact: true }).fill(id);
  await form.getByLabel("Runner name", { exact: true }).fill("Pending browser runner");
  await form.getByLabel("Enrollment lifetime", { exact: true }).selectOption("1800");
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const payloads: unknown[] = [];
  await page.route("**/api/v1/runner-enrollments", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    payloads.push(route.request().postDataJSON());
    if (payloads.length > 1) return route.continue();
    await pending;
    return route.fulfill({ status: 503, json: { error: "Temporary enrollment failure" } });
  });
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(form).toHaveAttribute("aria-busy", "true");
    for (const label of ["Runner ID", "Runner name", "Runner tags", "Runner capabilities", "Enrollment lifetime"]) {
      await expect(form.getByLabel(label, { exact: true })).toBeDisabled();
    }
    await expect(form.getByRole("button", { name: "Creating…", exact: true })).toBeDisabled();
    expect(await form.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) {
      await form.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("enrollment-pending-320.png"), animations: "disabled" });
    }
  }
  expect(payloads).toHaveLength(1);
  release();
  await expect(form.getByRole("alert")).toContainText("Enrollment could not be created");
  await expect(form).toHaveAttribute("aria-busy", "false");
  await expect(form.getByLabel("Runner ID", { exact: true })).toHaveValue(id);
  await expect(form.getByLabel("Runner name", { exact: true })).toHaveValue("Pending browser runner");
  await expect(form.getByLabel("Enrollment lifetime", { exact: true })).toHaveValue("1800");
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "One-time runner enrollment secret" })).toBeVisible();
  expect(payloads).toHaveLength(2);
  expect(payloads[1]).toEqual(payloads[0]);
  await page.keyboard.press("Escape");
});
