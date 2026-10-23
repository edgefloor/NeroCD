import { createHash } from "node:crypto";
import { expect, test, type Locator, type Page } from "@playwright/test";
// Real Docker output, compiled collector, registered API and embedded UI.
// Seeded fixture accounts/placement are setup, not an onboarding qualification.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; project_id: string; environment: string; resource_id: string; initial_container_id: string; initial_operation_id: string; settled_at: string; step_path: string; complete_path: string };
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
let completion: { page: Page; meta: Metadata } | undefined;
test.afterEach(async () => { if (completion) await completion.page.request.post(completion.meta.complete_path, { headers: headers(completion.meta), timeout: 5000 }).catch(() => {}); completion = undefined; });
async function signIn(page: Page, owner = false) {
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(owner ? "actual-logs-owner@example.local" : "viewer@example.local");
  await page.getByLabel("Password", { exact: true }).fill(owner ? "actual-logs-browser-fixture-password" : "viewer");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function press(logs: Locator, name: string) {
  const button = logs.getByRole("button", { name, exact: true }); await button.scrollIntoViewIfNeeded(); await expect(button).toBeEnabled(); await button.focus(); await logs.page().keyboard.press("Enter");
}
test("settled deployment streams fresh Docker stdout and stderr through compiled collector to explicit browser viewer", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_META, "requires optional actual collector browser gate"); test.setTimeout(180_000);
  const meta = JSON.parse(process.env.NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_META!) as Metadata;
  const assets = JSON.parse(process.env.NEROCD_RUNTIME_LOGS_ACTUAL_ASSETS!) as { files: Record<string, string> };
  expect(Object.keys(assets.files)).toHaveLength(74);
  let served = 0;
  for (const [path, digest] of Object.entries(assets.files)) {
    if (path === ".gitkeep") continue;
    const response = await page.request.get(path === "index.html" ? "/" : "/" + path); expect(response.status()).toBe(200);
    expect(createHash("sha256").update(await response.body()).digest("hex")).toBe(digest); served++;
    if (path === "index.html") expect(response.headers()["cache-control"]).toBe("no-cache");
  }
  console.log(`actual collector browser served ${served} matching files of 74 canonical entries; .gitkeep excluded`);
  const ownerContext = await browser.newContext({ baseURL: meta.origin }); const owner = await ownerContext.newPage(); await signIn(owner, true); completion = { page: owner, meta };
  const step = async (name: string) => { const response = await owner.request.post(meta.step_path + name, { headers: headers(meta), timeout: 30_000 }); expect(response.status()).toBe(200); };
  await signIn(page);
  const resourcePath = `/api/v1/resources/${meta.resource_id}/runtime-log-sessions`;
  let opens = 0, reads = 0, renews = 0;
  page.on("request", request => { if (request.method() === "POST" && new URL(request.url()).pathname === resourcePath) opens++; if (request.url().includes("/logs?after_sequence=")) reads++; if (request.url().endsWith("/renew")) renews++; });
  await page.goto("/projects"); const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id); await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
  const logs = hosting.getByRole("region", { name: "Runtime logs for primary actual json-file", exact: true }); await logs.scrollIntoViewIfNeeded();
  await expect(logs.getByRole("button", { name: "Follow runtime logs", exact: true })).toBeEnabled(); expect({ opens, reads, renews }).toEqual({ opens: 0, reads: 0, renews: 0 });
  expect((await page.request.post("/api/v1/runs", { headers: headers(meta), data: { template_id: "tpl_plan" } })).status()).toBe(403);
  const operation = await (await page.request.get(`/api/v1/hosting-operations/${meta.initial_operation_id}`)).json(); expect(operation.state).toBe("succeeded"); expect(operation.result.container_id).toBe(meta.initial_container_id);
  const follow = async () => {
    const response = page.waitForResponse(r => new URL(r.url()).pathname === resourcePath && r.request().method() === "POST");
    await press(logs, "Follow runtime logs"); const got = await response; expect(got.status()).toBe(200); const session = await got.json(); expect(session.target.container_id).toBe(meta.initial_container_id); expect(session.target.operation_id).toBe(meta.initial_operation_id); return session.id as string;
  };
  const session = await follow();
  expect((await owner.request.get(`/api/v1/runtime-log-sessions/${session}/logs?limit=200`)).status()).toBe(404);
  expect((await page.request.post(meta.step_path + "emit", { headers: headers(meta) })).status()).toBe(403);
  await step("emit");
  const rows = logs.getByRole("list", { name: "Sensitive runtime log rows", exact: true });
  await expect(rows.getByText(/stdout line/).first()).toBeVisible({ timeout: 20_000 }); await expect(rows.getByText(/stderr line/).first()).toBeVisible();
  await expect(rows.getByText(/\[REDACTED\].*canary-safe/)).toBeVisible();
  const response = await page.request.get(`/api/v1/runtime-log-sessions/${session}/logs?limit=200`); expect(response.status()).toBe(200); const evidence = await response.json();
  expect(evidence.items.some((row: { stream: string }) => row.stream === "stdout")).toBe(true); expect(evidence.items.some((row: { stream: string }) => row.stream === "stderr")).toBe(true);
  let previous = 0;
  for (const row of evidence.items) { expect(row.sequence).toBeGreaterThan(previous); previous = row.sequence; expect(Date.parse(row.source_timestamp)).toBeGreaterThan(Date.parse(meta.settled_at)); expect(Date.parse(row.received_at)).toBeGreaterThanOrEqual(Date.parse(row.source_timestamp)); expect(row.message).not.toContain("ncr_runtime_logs_fixture_bearer"); }
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await logs.scrollIntoViewIfNeeded(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await logs.evaluate(node => { node.scrollIntoView({ block: "start" }); window.scrollBy(0, -80); }); await page.screenshot({ path: info.outputPath(`actual-runtime-logs-${width}.png`), animations: "disabled" });
  }
  const closed = page.waitForResponse(r => r.request().method() === "DELETE" && new URL(r.url()).pathname === `/api/v1/runtime-log-sessions/${session}`);
  await press(logs, "Stop following"); expect((await closed).status()).toBe(200); await expect(logs.getByText(/Historical output; no live-follow claim/)).toBeVisible();
  const stopped = await (await page.request.get(`/api/v1/runtime-log-sessions/${session}/logs?limit=200`)).json(); expect(stopped.session.state).toBe("ended"); expect(stopped.session.ended_at).toBeTruthy();
  const renewedSession = await follow(); expect(renewedSession).not.toBe(session);
  await step("burst"); await expect(logs.getByText(/Retention prefix removed/)).toBeVisible({ timeout: 25_000 });
  await step("disconnect"); await step("resume"); await expect(logs.getByText(/Source gap: collection coverage is incomplete/)).toBeVisible({ timeout: 25_000 });
  // A hidden viewport stops renewals. Real elapsed time expires admitted demand.
  await page.evaluate(() => window.scrollTo(0, 0)); await page.waitForTimeout(31_000); await logs.scrollIntoViewIfNeeded();
  await expect(logs.getByText(/Demand expired or ended/)).toBeVisible(); await expect(logs.getByRole("button", { name: "Resume runtime logs", exact: true })).toHaveCount(0);
  await follow(); await expect(rows.getByText(/stdout line/).first()).toBeVisible({ timeout: 20_000 });
  await step("revoke"); await expect(logs.getByText(/Sensitive output and session state are cleared/)).toBeVisible({ timeout: 15_000 });
  await expect(rows).toHaveCount(0); await expect(logs.getByText(meta.initial_container_id, { exact: false })).toHaveCount(0);
  expect((await page.request.get(`/api/v1/runtime-log-sessions/${renewedSession}/logs?limit=200`)).status()).toBe(403);
  expect((await owner.request.post(meta.complete_path, { headers: { Origin: "http://127.0.0.1:1", "X-NeroCD-CSRF": "1" } })).status()).toBe(403);
  expect((await owner.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204); completion = undefined; await ownerContext.close();
});
