import { expect, test, type Locator, type Page } from "@playwright/test";
// Real collector/controller/Docker only. Setup accounts and deployment are seeded;
// no runtime-status response is mocked. Credentials stay out of browser artifacts.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; project_id: string; environment: string; resource_id: string; hidden_resource_id: string; initial_container_id: string; initial_operation_id: string; complete_path: string; step_path: string };
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
test.afterEach(async ({ page }) => {
  if (!process.env.NEROCD_OBSERVATION_BROWSER_META) return;
  const meta = JSON.parse(process.env.NEROCD_OBSERVATION_BROWSER_META) as Metadata;
  await page.context().setOffline(false);
  await page.request.post(meta.complete_path, { headers: headers(meta), timeout: 5000 }).catch(() => {});
});
async function signIn(page: Page, viewer = false) {
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "status-owner@example.local");
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "status-browser-fixture-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function openStatus(page: Page, meta: Metadata) {
  await page.goto("/projects"); const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id); await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
  const status = hosting.getByRole("region", { name: "Runtime status for observation workload", exact: true }); await status.scrollIntoViewIfNeeded();
  await expect(status.getByRole("button", { name: "Refresh runtime status", exact: true })).toBeEnabled(); return status;
}
async function refresh(status: Locator) {
  await status.scrollIntoViewIfNeeded(); const button = status.getByRole("button", { name: /^(Refresh|Retry) runtime status$/ }); await expect(button).toBeEnabled(); await button.focus(); await status.page().keyboard.press("Enter");
}
test("current observation ages independently of deployment outcome through real collector lifecycle", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_OBSERVATION_BROWSER_META, "requires NEROCD_OBSERVATION_BROWSER=1 acceptance gate"); test.setTimeout(180_000);
  const meta = JSON.parse(process.env.NEROCD_OBSERVATION_BROWSER_META!) as Metadata;
  const path = `/api/v1/resources/${meta.resource_id}/runtime-status`; const writes: string[] = [];
  page.on("request", (request) => { if (new URL(request.url()).pathname === path && request.method() !== "GET") writes.push(request.method()); });
  expect((await page.request.get(path)).status()).toBe(401); await signIn(page);
  expect((await page.request.get(`/api/v1/resources/${meta.hidden_resource_id}/runtime-status`)).status()).toBe(403);
  if (process.env.NEROCD_OBSERVATION_BROWSER_FAIL_AFTER_LOGIN === "1") throw new Error("intentional observation browser failure checks cleanup");
  const status = await openStatus(page, meta), current = status.getByRole("region", { name: "Current runtime observation", exact: true });
  await expect(current.getByText("Container state: Running", { exact: true })).toBeVisible(); await expect(current.getByText("Readiness: Ready", { exact: true })).toBeVisible();
  const target = status.getByRole("region", { name: "Settled deployment target", exact: true }), history = status.getByRole("region", { name: "Historical runtime evidence", exact: true });
  await expect(target.getByText(meta.initial_container_id, { exact: true })).toBeVisible(); await expect(history.getByText(meta.initial_container_id, { exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await refresh(status); await expect(current.getByText("Readiness: Ready", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    // A tall capture keeps the complete section below the fixed app header;
    // interaction and overflow above still use a 1000px-high viewport.
    await page.setViewportSize({ width, height: 2400 }); await status.scrollIntoViewIfNeeded();
    await status.screenshot({ path: info.outputPath(`runtime-status-${width}.png`), animations: "disabled" });
    await page.setViewportSize({ width, height: 1000 });
  }
  const step = async (name: string) => { const response = await page.request.post(meta.step_path + name, { headers: headers(meta), timeout: 30_000 }); expect(response.status()).toBe(200); return await response.json() as { container_id: string; operation_id: string }; };
  await step("stop"); await refresh(status); await expect(current.getByText("Container state: Exited", { exact: true })).toBeVisible(); await expect(current.getByText("Readiness: Not checked", { exact: true })).toBeVisible();
  // The successful deployment remains an earlier outcome despite verified exit.
  const recorded = await (await page.request.get(`/api/v1/hosting-operations/${meta.initial_operation_id}`)).json(); expect(recorded.state).toBe("succeeded"); expect(recorded.mutation_hold).toBe(false);
  await step("disconnect"); await page.context().setOffline(true);
  await expect(current.getByText("Container state: Unknown", { exact: true })).toBeVisible({ timeout: 35_000 }); await expect(status.getByText("Agent reachability: Unknown", { exact: true })).toBeVisible();
  // This is real elapsed controller time, not a mocked stale status response.
  await page.waitForTimeout(31_000); await page.context().setOffline(false); await step("confirm_stale"); await refresh(status);
  await expect(current.getByText("Container state: Unknown", { exact: true })).toBeVisible(); await expect(history.getByText("Exited", { exact: true })).toBeVisible(); await expect(status.getByText("Agent reachability: Unknown", { exact: true })).toBeVisible();
  await step("resume"); await refresh(status); await expect(current.getByText("Container state: Running", { exact: true })).toBeVisible(); await expect(current.getByText("Readiness: Ready", { exact: true })).toBeVisible();
  const replacement = await step("replace"); expect(replacement.container_id).not.toBe(meta.initial_container_id); await refresh(status);
  await expect(target.getByText(replacement.container_id, { exact: true })).toBeVisible(); await expect(target.getByText(meta.initial_container_id, { exact: true })).toHaveCount(0); await expect(history.getByText(meta.initial_container_id, { exact: true })).toBeVisible(); await expect(current.getByText("Container state: Unknown", { exact: true })).toBeVisible();
  await step("observe_replacement"); await refresh(status); await expect(current.getByText("Readiness: Ready", { exact: true })).toBeVisible(); await expect(history.getByText(replacement.container_id, { exact: true })).toBeVisible();
  const context = await browser.newContext({ baseURL: meta.origin, viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await context.newPage(); await signIn(viewer, true); const read = await openStatus(viewer, meta); await expect(read.getByText("Container state: Running", { exact: true })).toBeVisible();
    await expect(read.getByRole("form")).toHaveCount(0); await expect(read.getByRole("textbox")).toHaveCount(0);
    expect((await viewer.request.get(path)).status()).toBe(200); expect((await viewer.request.get(`/api/v1/resources/${meta.hidden_resource_id}/runtime-status`)).status()).toBe(403);
    expect((await viewer.request.post(meta.step_path + "stop", { headers: headers(meta) })).status()).toBe(403); expect((await viewer.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(403);
  } finally { await context.close(); }
  expect(writes).toEqual([]); expect((await page.request.post(meta.complete_path, { headers: { "X-NeroCD-CSRF": "1", Origin: "http://127.0.0.1:1" } })).status()).toBe(403);
  expect((await page.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204);
});
