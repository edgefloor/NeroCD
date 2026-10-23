import { expect, test, type Locator, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
// Real registered API, PostgreSQL/River, opt-in compiled agent, Docker, Traefik
// and local Pebble TLS. Transport loss is deliberate; runtime outcomes are real.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; project_id: string; environment: string; resource_id: string; hidden_resource_id: string; release_a: string; release_b: string; route_id: string; unchanged_route_id: string; step_path: string; complete_path: string };
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
async function signIn(page: Page, viewer = false) {
  await page.goto("/sign-in"); await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "switch-owner@example.local");
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "switch-browser-fixture-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function openSwitch(page: Page, meta: Metadata) {
  await page.goto("/projects"); const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id); await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
  const panel = hosting.getByRole("region", { name: "Release switching for switch", exact: true }); await expect(panel).toBeVisible(); return panel;
}
async function refresh(panel: Locator) { const button = panel.getByRole("button", { name: "Refresh route switch status", exact: true }); await expect(button).toBeEnabled(); await button.click(); }
async function review(panel: Locator, meta: Metadata, target: string) {
  const route = panel.getByRole("checkbox").filter({ visible: true }).first(); await expect(route).toBeEnabled();
  // Select the exact root route, preserving the independently verified route.
  await panel.locator("label").filter({ hasText: `${meta.route_id} · version` }).getByRole("checkbox").check();
  await panel.getByLabel("Eligible target release", { exact: true }).selectOption(target); await panel.getByRole("button", { name: "Review release switch", exact: true }).click();
  await expect(panel.getByRole("region", { name: "Review exact release switch", exact: true })).toContainText(target);
}
async function confirm(panel: Locator) { await panel.getByRole("checkbox", { name: /I confirm this exact routing change/ }).check(); const button = panel.getByRole("button", { name: "Confirm release switch", exact: true }); await button.focus(); await panel.page().keyboard.press("Enter"); }
async function recorded(panel: Locator, outcome: string) { await refresh(panel); await expect(panel.getByText(`Historical engine outcome: ${outcome}`, { exact: true })).toBeVisible({ timeout: 15_000 }); }
test("project operator switches exact existing routes through real queued execution and safe recovery", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_ROUTE_SWITCH_BROWSER_META, "requires scripts/hosting-route-switch-fixture.sh actual-agent gate"); test.setTimeout(330_000);
  const meta = JSON.parse(process.env.NEROCD_ROUTE_SWITCH_BROWSER_META!) as Metadata;
  const path = `/api/v1/resources/${meta.resource_id}/route-switches`;
  const adminReads: string[] = []; page.on("request", (request) => { if (/\/servers\/.*\/proxy|\/proxy-operations\//.test(new URL(request.url()).pathname)) adminReads.push(request.url()); });
  expect((await page.request.get(path)).status()).toBe(401); await signIn(page);
  // Verify Go-embedded delivery matches the freshly built local dist bytes.
  const html = await (await page.request.get("/projects")).text();
  const assets = [...new Set(html.match(/\/assets\/[^" ]+\.(?:js|css)/g))]; expect(assets.length).toBeGreaterThan(0);
  const served: Record<string, string> = {};
  for (const asset of assets) { const response = await page.request.get(asset); expect(response.status()).toBe(200); const hash = createHash("sha256").update(await response.body()).digest("hex"); expect(hash).toBe(createHash("sha256").update(readFileSync(`../dist${asset}`)).digest("hex")); served[asset] = hash; }
  await info.attach("served-dist-sha256", { body: JSON.stringify(served, null, 2), contentType: "application/json" });
  expect((await page.request.get(`/api/v1/resources/${meta.hidden_resource_id}/route-switches`)).status()).toBe(403);
  let panel = await openSwitch(page, meta);
  await review(panel, meta, meta.release_b);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await panel.scrollIntoViewIfNeeded(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const confirmation = panel.getByRole("checkbox", { name: /I confirm this exact routing change/ }); await confirmation.focus(); await expect(confirmation).toBeFocused();
    await page.setViewportSize({ width, height: 2400 }); await panel.screenshot({ path: info.outputPath(`route-switch-review-${width}.png`), animations: "disabled" }); await page.setViewportSize({ width, height: 1000 });
  }
  const step = async (name: string) => { const response = await page.request.post(meta.step_path + name, { headers: headers(meta), timeout: 45_000 }); expect(response.status()).toBe(200); return await response.json() as { operation_id: string; run_id: string }; };
  // Synthetic stale-version request injection forwards to the real admission
  // authority. It does not fabricate a rejection or runtime outcome.
  await page.route(meta.origin + path, async route => {
    if (route.request().method() !== "POST") return route.continue();
    const stale = route.request().postDataJSON(); stale.routes[0].expected_version += 100;
    const response = await route.fetch({ postData: stale }); expect(response.status()).toBe(409); await route.fulfill({ response });
  }, { times: 1 });
  await confirm(panel); await expect(panel.getByText(/Switch conflict. Refresh the authoritative choices/)).toBeVisible();
  await expect(panel.getByRole("button", { name: "Retry identical release switch", exact: true })).toBeDisabled();
  await panel.getByRole("button", { name: "Refresh choices for a new review", exact: true }).click(); await review(panel, meta, meta.release_b);
  // Forward exactly one real POST, then drop only its accepted response. Retry
  // must preserve every byte of the intent and recover the single queued op.
  const bodies: string[] = [];
  page.on("request", request => { if (new URL(request.url()).pathname === path && request.method() === "POST") bodies.push(request.postData()!); });
  await page.route(meta.origin + path, async route => { if (route.request().method() !== "POST") return route.continue(); const response = await route.fetch(); expect(response.status()).toBe(202); await route.abort("connectionfailed"); }, { times: 1 });
  await confirm(panel); await expect(panel.getByText(/The response was not confirmed/)).toBeVisible(); await panel.getByRole("button", { name: "Retry identical release switch", exact: true }).click();
  await expect(panel.getByRole("region", { name: "Recorded route switch operation", exact: true })).toBeVisible(); expect(bodies).toHaveLength(2); expect(bodies[1]).toBe(bodies[0]);
  // Reload destroys in-memory intent; authoritative pending ID recovers work.
  panel = await openSwitch(page, meta); await expect(panel.getByText(/Recovered or requested switch:/)).toBeVisible(); await expect(panel.getByRole("button", { name: "Review release switch", exact: true })).toBeDisabled();
  const appliedB = await step("apply-b"); expect(appliedB.run_id).toBeTruthy(); await recorded(panel, "applied");
  await review(panel, meta, meta.release_a); await confirm(panel); const appliedA = await step("apply-a"); expect(appliedA.operation_id).not.toBe(appliedB.operation_id); await recorded(panel, "applied");
  await step("stop-b"); await refresh(panel); await review(panel, meta, meta.release_b); await confirm(panel); await step("failed-b"); await recorded(panel, "rolled_back");
  await panel.screenshot({ path: info.outputPath("route-switch-real-rollback.png"), animations: "disabled" });
  await review(panel, meta, meta.release_b); await confirm(panel); await step("hold-b"); await recorded(panel, "uncertain");
  await expect(panel.getByText(/Mutation ownership remains held/)).toBeVisible(); await expect(panel.getByRole("button", { name: "Review release switch", exact: true })).toBeDisabled();
  await expect(panel.getByRole("button", { name: /unlock|reconcile/i })).toHaveCount(0); await panel.screenshot({ path: info.outputPath("route-switch-real-hold.png"), animations: "disabled" });
  const context = await browser.newContext({ baseURL: meta.origin, viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await context.newPage(); await signIn(viewer, true); const read = await openSwitch(viewer, meta); await expect(read.getByText(/Project maintainer access is required/)).toBeVisible(); await expect(read.getByRole("button", { name: "Review release switch", exact: true })).toHaveCount(0);
    expect((await viewer.request.get(path)).status()).toBe(200); expect((await viewer.request.post(path, { headers: headers(meta), data: JSON.parse(bodies[0]!) })).status()).toBe(403);
    expect((await viewer.request.get(`/api/v1/resources/${meta.hidden_resource_id}/route-switches`)).status()).toBe(403);
    expect((await viewer.request.get(`/api/v1/resources/${meta.hidden_resource_id}/route-switches/${appliedB.operation_id}`)).status()).toBe(403);
    expect((await viewer.request.post(meta.step_path + "stop-b", { headers: headers(meta) })).status()).toBe(403);
  } finally { await context.close(); }
  expect(adminReads).toEqual([]);
  expect((await page.request.post(meta.complete_path, { headers: { ...headers(meta), Origin: "http://127.0.0.1:1" } })).status()).toBe(403);
  expect((await page.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204);
});
