import { expect, test, type Locator, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
// Real registered API, PostgreSQL/River and compiled-agent rollback/conflict.
// Request-loss and stale input are deliberate transport/input faults. No accepted
// receipt, engine outcome, review, plan or resolution is fabricated here.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; server_id: string; proxy_id: string; project_id: string; operation_id: string; receipt_id: string; receipt_hash: string; verify_path: string; complete_path: string };
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
async function signIn(page: Page, kind: "admin" | "owner" | "viewer" = "admin") {
  await page.goto("/sign-in"); await page.getByLabel("Email", { exact: true }).fill(kind === "admin" ? "admin@example.local" : kind === "owner" ? "recovery-owner@example.local" : "viewer@example.local");
  await page.getByLabel("Password", { exact: true }).fill(kind === "admin" ? "admin" : kind === "owner" ? "recovery-browser-fixture-password" : "viewer"); await page.getByRole("button", { name: "Sign in", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function selectServer(page: Page) { await page.getByRole("article", { name: "Hosting server switch fixture", exact: true }).getByRole("button", { name: "View server destination", exact: true }).click(); const panel = page.getByRole("region", { name: "Proxy metadata recovery", exact: true }); await expect(panel).toBeVisible(); return panel; }
async function openRecovery(page: Page) { await page.goto("/runners?q=retained"); return selectServer(page); }
async function createPlan(panel: Locator) { await panel.getByRole("button", { name: "Request server recovery plan", exact: true }).click(); return panel.getByRole("region", { name: "Review exact metadata recovery plan", exact: true }); }
async function confirm(panel: Locator) { await panel.getByRole("checkbox", { name: /I confirm this exact metadata repair/ }).check(); const button = panel.getByRole("button", { name: "Apply reviewed metadata recovery", exact: true }); await button.focus(); await panel.page().keyboard.press("Enter"); }
test("administrator reviews exact server plan and recovers genuine metadata conflict without completing convergence", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_PROXY_RECOVERY_BROWSER_META, "requires web/app/scripts/hosting-proxy-recovery-fixture.sh actual-agent gate"); test.setTimeout(250_000);
  const meta = JSON.parse(process.env.NEROCD_PROXY_RECOVERY_BROWSER_META!) as Metadata;
  const base = `/api/v1/proxy-operations/${meta.operation_id}`, planPath = base + "/recovery-plans";
  expect((await page.request.get(base + "/recovery")).status()).toBe(401); await signIn(page);
  const html = await (await page.request.get("/runners")).text(), assets = [...new Set(html.match(/\/assets\/[^" ]+\.(?:js|css)/g))], served: Record<string, string> = {};
  expect(assets.length).toBeGreaterThan(0);
  for (const asset of assets) { const response = await page.request.get(asset); expect(response.status()).toBe(200); const hash = createHash("sha256").update(await response.body()).digest("hex"); expect(hash).toBe(createHash("sha256").update(readFileSync(`../dist${asset}`)).digest("hex")); served[asset] = hash; }
  const proof = info.outputPath("served-dist-sha256.json"); writeFileSync(proof, JSON.stringify(served, null, 2)); await info.attach("served-dist-sha256", { path: proof, contentType: "application/json" });
  let panel = await openRecovery(page); await expect(panel.getByText("Recovery operation: " + meta.operation_id, { exact: true })).toBeVisible();
  await panel.getByRole("button", { name: "Read metadata recovery status", exact: true }).click(); await expect(panel.getByRole("button", { name: "Request server recovery plan", exact: true })).toBeEnabled();
  const raw = await (await page.request.get(base + "/recovery")).json(); expect(raw.eligible).toBe(true); expect(raw.review.engine_outcome).toBe("rolled_back"); expect(raw.review.complete_receipt_id).toBe(meta.receipt_id); expect(raw.review.complete_receipt_hash).toBe(meta.receipt_hash); expect(raw.review.routes.some((route: { enabled: boolean; authorized: boolean }) => route.enabled || route.authorized)).toBe(false);
  // Synthetic stale input is sent to the actual server; the real CAS rejects it.
  await page.route(meta.origin + planPath, async route => { const stale = route.request().postDataJSON(); stale.expected_desired_generation += 100; const response = await route.fetch({ postData: stale }); expect(response.status()).toBe(409); await route.fulfill({ response }); }, { times: 1 });
  await createPlan(panel); await expect(panel.getByText(/Recovery conflict/)).toBeVisible(); await panel.getByRole("button", { name: "Read fresh facts for a new review", exact: true }).click();
  const planBodies: string[] = [], applyBodies: { path: string; body: string }[] = [];
  page.on("request", request => { if (request.method() !== "POST") return; const path = new URL(request.url()).pathname; if (path === planPath) planBodies.push(request.postData()!); if (path.startsWith(planPath + "/") && path.endsWith("/apply")) applyBodies.push({ path, body: request.postData()! }); });
  // Lose one actual accepted plan response. The UI retries its exact key/body,
  // returning the original database expiry rather than creating a new review.
  let originalPlan: { id: string; expires_at: string };
  await page.route(meta.origin + planPath, async route => { const response = await route.fetch(); expect(response.status()).toBe(201); originalPlan = await response.json(); await route.abort("connectionfailed"); }, { times: 1 });
  await createPlan(panel); await expect(panel.getByText(/The plan response was not confirmed/)).toBeVisible(); await panel.getByRole("button", { name: "Retry identical recovery plan request", exact: true }).click(); let review = panel.getByRole("region", { name: "Review exact metadata recovery plan", exact: true }); await expect(review).toBeVisible();
  expect(planBodies).toHaveLength(2); expect(planBodies[0]).toBe(planBodies[1]); await expect(review.getByText("Original database expiry: " + originalPlan!.expires_at, { exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await review.scrollIntoViewIfNeeded(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); const confirmation = review.getByRole("checkbox", { name: /I confirm this exact metadata repair/ }); await confirmation.focus(); await expect(confirmation).toBeFocused(); await page.setViewportSize({ width, height: 3000 }); await panel.screenshot({ path: info.outputPath(`proxy-recovery-review-${width}.png`), animations: "disabled" }); await page.setViewportSize({ width, height: 1000 });
  }
  // Genuine elapsed-time database expiry; no database clock or plan is edited.
  await page.waitForTimeout(Math.max(0, Date.parse(originalPlan!.expires_at) - Date.now() + 1200)); await confirm(panel); await expect(panel.getByText(/Recovery conflict/)).toBeVisible();
  await panel.getByRole("button", { name: "Read fresh facts for a new review", exact: true }).click(); await createPlan(panel); review = panel.getByRole("region", { name: "Review exact metadata recovery plan", exact: true }); await expect(review).toBeVisible();
  expect(JSON.parse(planBodies[2]!).idempotency_key).not.toBe(JSON.parse(planBodies[0]!).idempotency_key);
  const expiry = await review.getByText(/^Original database expiry:/).textContent(); const expiresAt = expiry!.replace("Original database expiry: ", "");
  // Drop an accepted apply response, then replay the frozen request after the
  // original expiry. Concurrent identical API retries must return one resolution.
  await page.route(meta.origin + planPath + "/**/apply", async route => { const response = await route.fetch(); expect(response.status()).toBe(200); await route.abort("connectionfailed"); }, { times: 1 });
  await confirm(panel); await expect(panel.getByText(/The apply response was not confirmed/)).toBeVisible(); const frozen = applyBodies.at(-1)!;
  await page.waitForTimeout(Math.max(0, Date.parse(expiresAt) - Date.now() + 1200));
  const responses = await Promise.all([page.request.post(frozen.path, { headers: headers(meta), data: JSON.parse(frozen.body) }), page.request.post(frozen.path, { headers: headers(meta), data: JSON.parse(frozen.body) })]); expect(responses.map(response => response.status())).toEqual([200, 200]); const copies = await Promise.all(responses.map(response => response.json())); expect(copies[0]).toEqual(copies[1]);
  await panel.getByRole("button", { name: "Retry identical recovery apply request", exact: true }).click(); const recorded = panel.getByRole("region", { name: "Recorded metadata recovery resolution", exact: true }); await expect(recorded).toBeVisible(); expect(applyBodies.at(-1)!.body).toBe(frozen.body); await expect(recorded).toContainText(copies[0].id); await expect(recorded).toContainText(meta.receipt_id);
  expect(new URL(page.url()).searchParams.has("proxy_recovery_operation")).toBe(true); expect(new URL(page.url()).searchParams.get("q")).toBe("retained");
  await page.reload(); panel = await selectServer(page); await expect(panel.getByRole("region", { name: "Recorded metadata recovery resolution", exact: true })).toContainText(copies[0].id); await panel.screenshot({ path: info.outputPath("proxy-recovery-resolved-reload.png"), animations: "disabled" });
  const verification = await page.request.post(meta.verify_path, { headers: headers(meta) }); expect(verification.status()).toBe(200); expect((await verification.json()).id).toBe(copies[0].id);
  // The real queued ordinary convergence has no completed strict receipt and
  // remains unsupported for this API; it cannot be adopted as an unlock.
  await panel.getByText("Look up an exact operation", { exact: true }).click();
  await panel.getByLabel("Original proxy operation ID", { exact: true }).fill(copies[0].convergence_operation_id);
  await panel.getByRole("button", { name: "Read exact metadata recovery", exact: true }).click();
  await expect(panel.getByText(/Recovery is unsupported or unfinished/)).toBeVisible();
  await expect(panel.getByRole("button", { name: "Request server recovery plan", exact: true })).toHaveCount(0);
  await panel.getByLabel("Original proxy operation ID", { exact: true }).fill(meta.operation_id);
  await panel.getByRole("button", { name: "Read exact metadata recovery", exact: true }).click();
  await expect(panel.getByRole("region", { name: "Recorded metadata recovery resolution", exact: true })).toBeVisible();
  const ownerContext = await browser.newContext({ baseURL: meta.origin }), viewerContext = await browser.newContext({ baseURL: meta.origin });
  try {
    const owner = await ownerContext.newPage(); await signIn(owner, "owner"); expect((await owner.request.get(base + "/recovery")).status()).toBe(200);
    const viewer = await viewerContext.newPage(); await signIn(viewer, "viewer"); expect((await viewer.request.get(base + "/recovery")).status()).toBe(403); expect((await viewer.request.post(planPath, { headers: headers(meta), data: JSON.parse(planBodies.at(-1)!) })).status()).toBe(403); expect((await viewer.request.post(frozen.path, { headers: headers(meta), data: JSON.parse(frozen.body) })).status()).toBe(403); expect((await viewer.request.post(meta.verify_path, { headers: headers(meta) })).status()).toBe(403);
  } finally { await ownerContext.close(); await viewerContext.close(); }
  expect((await page.request.post(meta.complete_path, { headers: { ...headers(meta), Origin: "http://127.0.0.1:1" } })).status()).toBe(403); expect((await page.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204);
});
