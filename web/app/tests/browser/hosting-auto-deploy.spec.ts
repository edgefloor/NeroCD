import { expect, test, type Page, type Locator } from "@playwright/test";
// This scenario requires the registered API/real River/compiled runner fixture.
// Interception below loses one real committed response; it never fabricates success.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { base_url: string; project_id: string; environment_name: string; resource_name: string; resource_id: string; expected_commit_a: string; expected_commit_b: string; expected_commit_c: string; step_path: string };
async function keyboardActivate(page: Page, button: Locator) {
  // A preceding metadata read or viewport transition can briefly disable the
  // control and retire focus. Retry focus only; submit exactly once when ready.
  await expect(async () => {
    await expect(button).toBeEnabled({ timeout: 200 }); await button.focus();
    await expect(button).toBeFocused({ timeout: 200 });
  }).toPass({ timeout: 15_000, intervals: [100, 250] });
  await page.keyboard.press("Enter");
}
async function signIn(page: Page, viewer = false) {
  const bootstrap = await page.request.get("/api/v1/bootstrap-status", { timeout: 10_000 });
  expect(bootstrap.status(), "fixture bootstrap status must be available before sign-in").toBe(200);
  expect(await bootstrap.json(), "fixture must complete supported administrator bootstrap").toEqual({ status: "complete" });
  await page.goto("/sign-in", { timeout: 15_000, waitUntil: "domcontentloaded" });
  const email = page.getByLabel("Email", { exact: true });
  await expect(email, "actual sign-in page must render the email form").toBeVisible({ timeout: 10_000 });
  await email.fill(viewer ? "push-viewer@example.local" : "push-owner@example.local", { timeout: 10_000 });
  await page.getByLabel("Password", { exact: true }).fill("push-browser-fixture-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function resourceArticle(page: Page, meta: Metadata) {
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByRole("combobox", { name: "Hosting project", exact: true }).selectOption(meta.project_id);
  await hosting.getByRole("button", { name: meta.environment_name, exact: true }).click();
  const article = hosting.getByRole("article", { name: meta.resource_name, exact: true }).filter({ hasText: "https://github.com/acme/push-fixture.git" });
  await expect(article).toHaveCount(1); return article;
}
test("owner reviews automatic unprotected deployment, B reaches HTTPS automatically, and failed C preserves B", async ({ page, browser }) => {
  test.skip(!process.env.NEROCD_AUTO_DEPLOY_BROWSER_META, "requires actual automatic deployment browser fixture");
  test.setTimeout(960_000);
  page.setDefaultTimeout(15_000);
  const meta = JSON.parse(process.env.NEROCD_AUTO_DEPLOY_BROWSER_META!) as Metadata;
  expect(new URL(meta.base_url).protocol).toMatch(/^https?:$/);
  expect(new URL(meta.step_path, meta.base_url).origin).toBe(new URL(meta.base_url).origin);
  const manualDeployments: string[] = [];
  const forbiddenReads: string[] = [], commands: Array<{ path: string; body: unknown }> = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST" && /\/deployments(?:\/|$)/.test(path)) manualDeployments.push(path);
    if (/\/reveal(?:\/|$)|\/service-accounts\/rotate|\/runner-enrollments/.test(path)) forbiddenReads.push(path);
    if (request.method() !== "GET" && path.includes("/github-push-policy")) commands.push({ path, body: request.postDataJSON() });
  });
  const started = Date.now();
  page.on("response", async (response) => {
    if (new URL(response.url()).pathname !== `/api/v1/resources/${meta.resource_id}/github-push-policy` || response.request().method() !== "GET" || response.status() !== 200) return;
    const snapshot = await response.json() as { owner_automatic_deployment_options?: { available: boolean; reason: string } };
    const options = snapshot.owner_automatic_deployment_options;
    console.info("owner options snapshot", JSON.stringify({ elapsed_ms: Date.now() - started, available: options?.available, reason: options?.reason }));
  });
  await signIn(page); await page.setViewportSize({ width: 320, height: 1000 });
  const initialUIOptions = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/github-push-policy` && r.request().method() === "GET");
  const article = await resourceArticle(page, meta), panel = article.getByRole("region", { name: "Build on push", exact: true });
  const initialUIResponse = await initialUIOptions;
  expect(new URL(initialUIResponse.url()).pathname).toBe(`/api/v1/resources/${meta.resource_id}/github-push-policy`);
  expect(initialUIResponse.status()).toBe(200);
  expect((await initialUIResponse.json() as { owner_automatic_deployment_options?: { available: boolean; reason: string } }).owner_automatic_deployment_options).toMatchObject({ available: true, reason: "eligible" });
  const ownerOptions = await page.request.get(`/api/v1/resources/${meta.resource_id}/github-push-policy`);
  expect(ownerOptions.status()).toBe(200);
  const ownerPolicy = await ownerOptions.json() as { can_manage: boolean; owner_automatic_deployment_options?: { available: boolean; reason: string } };
  expect(ownerPolicy.can_manage).toBe(true);
  expect(ownerPolicy.owner_automatic_deployment_options, "ordinary owner requires real eligible manual A + loaded HTTPS options").toMatchObject({ available: true, reason: "eligible" });
  // Review current server evidence through the existing explicit UI refresh.
  const refreshedOptions = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/github-push-policy` && r.request().method() === "GET");
  await panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }).click();
  const refreshedPolicy = await refreshedOptions;
  expect(refreshedPolicy.status()).toBe(200);
  expect((await refreshedPolicy.json() as typeof ownerPolicy).owner_automatic_deployment_options).toMatchObject({ available: true, reason: "eligible" });
  const form = panel.getByRole("form", { name: "Configure GitHub build on push", exact: true });
  await expect(form.getByRole("radio", { name: "Build only. Deploy each successful build manually.", exact: true })).toBeChecked();
  async function reviewConfiguration() {
    const actorID = await form.getByRole("option").filter({ hasText: /^push-build-automation · maintainer · / }).getAttribute("value"); expect(actorID).toBeTruthy(); await form.getByRole("combobox", { name: "Automation service account", exact: true }).selectOption(actorID!);
  // IDs come from real metadata, while names and immutable versions are fixture inputs.
  const signing = form.getByRole("combobox", { name: "Webhook signing managed secret", exact: true });
  const signingID = await signing.getByRole("option").filter({ hasText: /^push-webhook-signing · / }).getAttribute("value"); expect(signingID).toBeTruthy(); await signing.selectOption(signingID!);
  await form.getByRole("combobox", { name: "Webhook signing immutable version", exact: true }).selectOption("1");
  const provider = form.getByRole("combobox", { name: "GitHub provider read managed secret", exact: true });
  const providerID = await provider.getByRole("option").filter({ hasText: /^push-provider-read · / }).getAttribute("value"); expect(providerID).toBeTruthy(); await provider.selectOption(providerID!);
  await form.getByRole("combobox", { name: "GitHub provider read immutable version", exact: true }).selectOption("1");
    await expect(form.getByRole("radio", { name: "Automatic deployment without approval", exact: true })).toBeEnabled();
    await form.getByRole("radio", { name: "Automatic deployment without approval", exact: true }).check();
    const route = form.getByRole("checkbox", { name: "https://automatic.example.test/", exact: true });
    await expect(route).toBeVisible(); await route.check();
    await expect(form.getByText(/Container port [0-9]+; readiness/)).toBeVisible();
    await form.getByLabel(/I reviewed these exact references and generations/).check();
  }
  await reviewConfiguration();
  const policyPath = `/api/v1/resources/${meta.resource_id}/github-push-policy`;
  let conflictBody: { request_id: string } | undefined;
  await page.route(`**${policyPath}`, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    conflictBody = route.request().postDataJSON() as { request_id: string };
    // A distinct real owner decision wins this generation before the UI request.
    const winner = await page.request.put(policyPath, { headers: { Origin: meta.base_url, "X-NeroCD-CSRF": "1" }, data: { ...conflictBody, request_id: crypto.randomUUID() } });
    expect(winner.status()).toBe(200); await route.continue();
  }, { times: 1 });
  const conflict = page.waitForResponse((r) => new URL(r.url()).pathname === policyPath && r.request().method() === "PUT");
  await form.getByRole("button", { name: "Save disabled push configuration", exact: true }).click();
  expect((await conflict).status()).toBe(409);
  await expect(panel.getByRole("button", { name: "Retry identical push decision", exact: true })).toBeDisabled();
  await panel.getByRole("button", { name: "Use reviewed policy for a separate decision", exact: true }).click();
  await reviewConfiguration();
  let firstBody: unknown;
  await page.route(`**${policyPath}`, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    firstBody = route.request().postDataJSON(); const response = await route.fetch(); expect(response.status()).toBe(200); await route.abort("failed");
  }, { times: 1 });
  await keyboardActivate(page, form.getByRole("button", { name: "Save disabled push configuration", exact: true }));
  await expect(panel.getByRole("button", { name: "Retry identical push decision", exact: true })).toBeEnabled();
  await expect(form.getByRole("combobox", { name: "Automation service account", exact: true })).toBeDisabled();
  const recovered = page.waitForResponse((r) => new URL(r.url()).pathname === policyPath && r.request().method() === "PUT");
  await keyboardActivate(page, panel.getByRole("button", { name: "Retry identical push decision", exact: true }));
  const recovery = await recovered; expect(recovery.status()).toBe(200); expect(recovery.request().postDataJSON()).toEqual(firstBody);
  expect(firstBody).toMatchObject({ reset_branch_baseline: true, configuration: { automatic_deployment: { mode: "automatic_unprotected", routes: [expect.objectContaining({ route_id: expect.any(String) })] } } });
  expect((firstBody as { request_id: string }).request_id).not.toBe(conflictBody!.request_id);
  const configured = await recovery.json() as { callback_url: string; generation: number; enabled: boolean }; expect(configured.enabled).toBe(false);
  await expect(panel.getByLabel("Server callback URL", { exact: true })).toHaveValue(configured.callback_url);
  const verified = page.waitForResponse((r) => new URL(r.url()).pathname === `${policyPath}/verify` && r.request().method() === "POST");
  await keyboardActivate(page, panel.getByRole("button", { name: "Verify GitHub repository and branch", exact: true })); expect((await verified).status()).toBe(200);
  await expect(panel.getByRole("button", { name: "Enable build and automatic deployment", exact: true })).toBeEnabled();
  const baseline = await page.request.get(`/api/v1/resources/${meta.resource_id}/builds?limit=20&offset=0`); expect(baseline.status()).toBe(200);
  const initialACount = (await baseline.json() as { items: Array<{ source_receipt?: { commit: string } }> }).items.filter((item) => item.source_receipt?.commit === meta.expected_commit_a).length;
  const enabled = page.waitForResponse((r) => new URL(r.url()).pathname === `${policyPath}/enabled` && r.request().method() === "POST");
  await keyboardActivate(page, panel.getByRole("button", { name: "Enable build and automatic deployment", exact: true })); expect((await enabled).status()).toBe(200);
  async function step(command: "push-b" | "duplicate-b" | "delayed-a" | "probe-b" | "probe-failed-c" | "push-c" | "complete") { const result = await page.request.post(`${meta.step_path}${command}`, { headers: { Origin: meta.base_url, "X-NeroCD-CSRF": "1" }, timeout: 420_000 }); expect(result.status(), command).toBe(204); }
  await step("push-b"); await step("duplicate-b"); await step("delayed-a");
  let buildB = "";
  await expect.poll(async () => {
    const response = await page.request.get(`${policyPath}/deliveries?limit=20`); expect(response.status()).toBe(200);
    const history = await response.json() as { items: Array<{ verified_commit: string; build_id: string }> };
    const b = history.items.find((item) => item.verified_commit === meta.expected_commit_b && item.build_id); if (!b) return "waiting"; buildB = b.build_id;
    const detail = await page.request.get(`/api/v1/hosting-builds/${buildB}`); return (await detail.json() as { build: { state: string } }).build.state;
  }, { timeout: 180_000 }).toBe("succeeded");
  const currentPolicy = await page.request.get(policyPath); expect(currentPolicy.status()).toBe(200); expect((await currentPolicy.json() as { desired_commit: string }).desired_commit).toBe(meta.expected_commit_b);
  const buildHistory = await page.request.get(`/api/v1/resources/${meta.resource_id}/builds?limit=20&offset=0`); expect(buildHistory.status()).toBe(200);
  const recorded = (await buildHistory.json() as { items: Array<{ source_receipt?: { commit: string } }> }).items;
  expect(recorded.filter((item) => item.source_receipt?.commit === meta.expected_commit_b)).toHaveLength(1);
  expect(recorded.filter((item) => item.source_receipt?.commit === meta.expected_commit_a)).toHaveLength(initialACount);
  const deliveryHistory = await page.request.get(`${policyPath}/deliveries?limit=20`); expect(deliveryHistory.status()).toBe(200);
  const delivered = (await deliveryHistory.json() as { items: Array<{ after_commit: string; verified_commit: string; state: string; reason: string; build_id: string }> }).items;
  const delayed = delivered.find((item) => item.after_commit === meta.expected_commit_a); expect(delayed).toBeTruthy(); expect(delayed!.state).toBe("ignored"); expect(delayed!.reason).toBe("obsolete"); expect(["", meta.expected_commit_b]).toContain(delayed!.verified_commit); expect(delayed!.build_id).toBe("");
  await panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }).click();
  await expect(panel.getByRole("list", { name: "GitHub push deliveries", exact: true })).toContainText(meta.expected_commit_b);
  await step("probe-b");
  const summary = panel.getByRole("region", { name: "Recorded automatic deployment", exact: true });
  await panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }).click();
  await expect(summary.getByText("Recorded HTTPS switch succeeded", { exact: true })).toBeVisible();
  await expect(summary).toContainText("not a current uptime or health check");
  await step("push-c"); await step("probe-failed-c");
  await panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }).click();
  await expect(summary.getByText("Recorded automatic deployment failed", { exact: true })).toBeVisible();
  await expect(summary).toContainText("A failed build is not a deployed release.");
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await expect(panel).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const refreshed = page.waitForResponse((r) => new URL(r.url()).pathname === policyPath && r.request().method() === "GET");
    await keyboardActivate(page, panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }));
    expect((await refreshed).status()).toBe(200);
  }
  const viewerContext = await browser.newContext({ baseURL: meta.base_url, viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await viewerContext.newPage(); const viewerReads: string[] = [];
    viewer.on("request", (r) => { const path = new URL(r.url()).pathname; if (path.startsWith("/api/v1/") && /\/(?:managed-secrets|service-accounts|source-grants)(?:\/|$)/.test(path)) viewerReads.push(r.url()); });
    await signIn(viewer, true); const read = (await resourceArticle(viewer, meta)).getByRole("region", { name: "Build on push", exact: true });
    await expect(read.getByRole("list", { name: "GitHub push deliveries", exact: true })).toBeVisible(); await expect(read.getByRole("form")).toHaveCount(0); expect(viewerReads).toEqual([]);
    const forbidden = await viewer.request.post(`${policyPath}/verify`, { headers: { Origin: meta.base_url, "X-NeroCD-CSRF": "1" }, data: { expected_policy_generation: configured.generation, request_id: crypto.randomUUID() } }); expect(forbidden.status()).toBe(403);
    for (const width of [320, 390, 430, 1440]) { await viewer.setViewportSize({ width, height: 1000 }); expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await expect(read.getByRole("region", { name: "Recorded automatic deployment", exact: true })).toBeVisible(); }
    expect(viewerReads).toEqual([]);
  } finally { await viewerContext.close(); }
  expect(commands.filter((command) => command.path === policyPath)).toHaveLength(3); expect(forbiddenReads).toEqual([]); expect(manualDeployments).toEqual([]);
  await step("complete");
});
