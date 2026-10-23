import { expect, test, type Locator, type Page } from "@playwright/test";
// Real registered API and PostgreSQL, simulated authorized TLS runner producer.
// No happy-path response mocks; no Docker collector/A29 qualification.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; project_id: string; environment: string; resource_id: string; initial_container_id: string; step_path: string; complete_path: string };
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
let completion: { page: Page; meta: Metadata } | undefined;
test.afterEach(async () => { if (completion) await completion.page.request.post(completion.meta.complete_path, { headers: headers(completion.meta), timeout: 5000 }).catch(() => {}); completion = undefined; });
async function signIn(page: Page, owner = false) {
  await page.goto("/sign-in"); await page.getByLabel("Email", { exact: true }).fill(owner ? "logs-owner@example.local" : "viewer@example.local"); await page.getByLabel("Password", { exact: true }).fill(owner ? "logs-browser-fixture-password" : "viewer"); await page.getByRole("button", { name: "Sign in", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function openControls(page: Page, meta: Metadata) {
  await page.goto("/projects"); const hosting = page.getByRole("region", { name: "Project hosting", exact: true }); await hosting.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id); await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
  const logs = hosting.getByRole("region", { name: "Runtime logs for log fixture workload", exact: true }); await logs.scrollIntoViewIfNeeded(); await expect(logs.getByRole("button", { name: "Follow runtime logs", exact: true })).toBeEnabled(); return logs;
}
async function press(logs: Locator, name: string) { const button = logs.getByRole("button", { name, exact: true }); await button.scrollIntoViewIfNeeded(); await expect(button).toBeEnabled(); await button.focus(); await logs.page().keyboard.press("Enter"); }
test("explicit sensitive runtime log demand uses real API, actor scope, bounded cache and simulated producer", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_RUNTIME_LOGS_BROWSER_META, "requires owned runtime-hosting-logs-browser gate"); test.setTimeout(210_000);
  const meta = JSON.parse(process.env.NEROCD_RUNTIME_LOGS_BROWSER_META!) as Metadata;
  const ownerContext = await browser.newContext({ baseURL: meta.origin }); const owner = await ownerContext.newPage(); await signIn(owner, true); completion = { page: owner, meta };
  const step = async (name: string) => { const response = await owner.request.post(meta.step_path + name, { headers: headers(meta), timeout: 30_000 }); expect(response.status()).toBe(200); return await response.json() as { session_id: string; last_sequence: number }; };
  const opens: string[] = []; let renews = 0, reads = 0;
  page.on("request", (request) => { if (request.url().endsWith("/runtime-log-sessions") && request.method() === "POST") opens.push(request.postData()!); if (request.url().endsWith("/renew")) renews++; if (request.url().includes("/logs?after_sequence=")) reads++; });
  await signIn(page); const logs = await openControls(page, meta); expect(opens).toHaveLength(0); expect(renews).toBe(0); expect(reads).toBe(0); await expect(logs.getByText(/Sensitive application output may contain secrets/)).toBeVisible();
  expect((await page.request.post("/api/v1/runs", { headers: headers(meta), data: { template_id: "tpl_plan" } })).status()).toBe(403);
  const follow = async () => {
    const response = page.waitForResponse((response) => response.url().endsWith("/runtime-log-sessions") && response.request().method() === "POST"); await press(logs, "Follow runtime logs"); const result = await response; expect(result.status()).toBe(200); return await result.json() as { id: string; target: { container_id: string } };
  };
  let session = await follow(); expect(session.target.container_id).toBe(meta.initial_container_id); await step("publish"); await expect(logs.getByText(/simulated output.*<b>literal<\/b>\\u001b\[31m/)).toBeVisible(); await expect(logs.locator("pre b")).toHaveCount(0);
  expect((await owner.request.get(`/api/v1/runtime-log-sessions/${session.id}/logs`)).status()).toBe(404);
  expect((await page.request.get(`/api/v1/runtime-log-sessions/${session.id}/logs`)).status()).toBe(200);
  expect((await page.request.post(meta.step_path + "publish", { headers: headers(meta) })).status()).toBe(403);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await logs.scrollIntoViewIfNeeded(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.setViewportSize({ width, height: 2400 }); await logs.evaluate((node) => { node.scrollIntoView({ block: "start" }); window.scrollBy(0, -80); }); await logs.screenshot({ path: info.outputPath(`runtime-logs-${width}.png`), animations: "disabled" }); await page.setViewportSize({ width, height: 1000 });
  }
  // Resizing/scrolling can legitimately pause explicit following via viewport
  // visibility. Reaffirm demand through the operator control before fault tests.
  await logs.scrollIntoViewIfNeeded(); await page.waitForTimeout(200);
  if (await logs.getByRole("button", { name: "Resume runtime logs", exact: true }).count()) await press(logs, "Resume runtime logs");
  // Fault injection forwards a real sensitive GET, then withholds its response.
  let release!: () => void, forwarded = false;
  const held = new Promise<void>((resolve) => { release = resolve; });
  await page.route(`**/api/v1/runtime-log-sessions/${session.id}/logs?*`, async (route) => { const response = await route.fetch(); expect(response.status()).toBe(200); forwarded = true; await held; await route.fulfill({ response }).catch(() => {}); }, { times: 1 });
  try {
    await expect.poll(() => forwarded, { timeout: 10_000 }).toBe(true); await expect(logs.getByRole("alert")).toContainText("timed out", { timeout: 10_000 }); const paused = { renews, reads, opens: opens.length }; await page.waitForTimeout(11_000); expect({ renews, reads, opens: opens.length }).toEqual(paused);
    await press(logs, "Retry runtime logs"); await expect(logs.getByRole("alert")).toHaveCount(0); release();
  } finally { release(); await page.unroute(`**/api/v1/runtime-log-sessions/${session.id}/logs?*`); }
  await press(logs, "Stop following"); await expect(logs.getByText(/Session state: ended/)).toBeVisible(); session = await follow(); await step("bulk"); await step("gap");
  await expect(logs.getByText(/Source gap:/)).toBeVisible({ timeout: 10_000 }); await expect(logs.getByText(/Retention prefix removed/)).toBeVisible(); await expect(logs.getByText(/Page truncated:/)).toBeVisible(); await expect(logs.getByText(/Local buffer trimmed/)).toBeVisible({ timeout: 20_000 }); await expect(logs.getByText(/Message clipped by producer or server/).first()).toBeVisible();
  await step("replace");
  await expect.poll(async () => (await logs.getByText(/The target changed/).count()) + (await logs.getByRole("alert").count()), { timeout: 10_000 }).toBeGreaterThan(0);
  // A renewal can observe replacement first and return neutral409. Reviewing
  // history then requires the explicit Retry control rather than an auto retry.
  if (await logs.getByRole("alert").count()) await press(logs, "Retry runtime logs");
  await expect(logs.getByText(/The target changed/)).toBeVisible({ timeout: 10_000 }); await expect(logs.getByText(/simulated_initial_container/)).toBeVisible(); session = await follow(); expect(session.target.container_id).toBe("simulated_replacement_container"); await step("publish"); await expect(logs.getByText(/Server sequence 1/)).toBeVisible({ timeout: 10_000 });
  await page.evaluate(() => window.scrollTo(0, 0)); await page.waitForTimeout(1000); const paused = { renews, reads }; await page.waitForTimeout(11_000); expect({ renews, reads }).toEqual(paused);
  await logs.scrollIntoViewIfNeeded(); await expect(logs.getByRole("button", { name: "Resume runtime logs", exact: true })).toBeVisible(); await press(logs, "Resume runtime logs");
  await page.evaluate(() => window.scrollTo(0, 0)); await page.waitForTimeout(31_000); await logs.scrollIntoViewIfNeeded(); await expect(logs.getByText(/Demand expired or ended/)).toBeVisible(); await expect(logs.getByRole("button", { name: "Resume runtime logs", exact: true })).toHaveCount(0);
  session = await follow(); await step("publish"); await expect(logs.getByText(/Server sequence 1/)).toBeVisible({ timeout: 10_000 });
  let releaseRevoked!: () => void, forwardedRevoked = false;
  const heldRevoked = new Promise<void>((resolve) => { releaseRevoked = resolve; });
  await page.route(`**/api/v1/runtime-log-sessions/${session.id}/logs?*`, async (route) => { const response = await route.fetch(); expect(response.status()).toBe(200); forwardedRevoked = true; await heldRevoked; await route.fulfill({ response }).catch(() => {}); }, { times: 1 });
  try {
    await expect.poll(() => forwardedRevoked, { timeout: 10_000 }).toBe(true); await step("revoke");
    // A concurrent renewal can learn revocation before the held read times out.
    await expect(logs.getByRole("alert")).toContainText(/timed out|access is unavailable/, { timeout: 10_000 });
    if (await logs.getByRole("button", { name: "Retry runtime logs", exact: true }).count()) await press(logs, "Retry runtime logs");
    await expect(logs.getByText(/Runtime log access is unavailable/)).toBeVisible(); releaseRevoked(); await page.waitForTimeout(200);
    await expect(logs.getByRole("list", { name: "Sensitive runtime log rows" })).toHaveCount(0); await expect(logs.getByText(session.id, { exact: true })).toHaveCount(0);
  } finally { releaseRevoked(); await page.unroute(`**/api/v1/runtime-log-sessions/${session.id}/logs?*`); }
  await step("restore");
  // Restored current membership may authorize this actor's retained history.
  // The cleared UI must not rehydrate it or resume demand automatically.
  expect((await page.request.get(`/api/v1/runtime-log-sessions/${session.id}/logs`)).status()).toBe(200);
  await expect(logs.getByRole("list", { name: "Sensitive runtime log rows" })).toHaveCount(0); await expect(logs.getByText(session.id, { exact: true })).toHaveCount(0);
  expect((await owner.request.post(meta.complete_path, { headers: { Origin: "http://127.0.0.1:1", "X-NeroCD-CSRF": "1" } })).status()).toBe(403);
  expect((await owner.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204);
  completion = undefined; await ownerContext.close();
});
