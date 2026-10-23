import { expect, test, type Page } from "@playwright/test";
// This scenario requires the registered API/real River/compiled runner fixture.
// Interception below loses one real committed response; it never fabricates success.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { base_url: string; project_id: string; environment_name: string; resource_id: string; expected_commit_a: string; expected_commit_b: string; expected_commit_c: string; step_path: string };
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
  const article = hosting.getByRole("article").filter({ hasText: "https://github.com/acme/push-fixture.git" });
  await expect(article).toHaveCount(1); return article;
}
test("owner configures signed push builds, manually deploys B, and sees failed C without replacing B", async ({ page, browser }) => {
  test.skip(!process.env.NEROCD_GITHUB_PUSH_BROWSER_META, "requires actual GitHub push controller browser fixture");
  test.setTimeout(480_000);
  const meta = JSON.parse(process.env.NEROCD_GITHUB_PUSH_BROWSER_META!) as Metadata;
  expect(new URL(meta.base_url).protocol).toMatch(/^https?:$/);
  expect(new URL(meta.step_path, meta.base_url).origin).toBe(new URL(meta.base_url).origin);
  const forbiddenReads: string[] = [], commands: Array<{ path: string; body: unknown }> = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (/\/reveal(?:\/|$)|\/service-accounts\/rotate|\/runner-enrollments/.test(path)) forbiddenReads.push(path);
    if (request.method() !== "GET" && path.includes("/github-push-policy")) commands.push({ path, body: request.postDataJSON() });
  });
  await signIn(page); await page.setViewportSize({ width: 320, height: 1000 });
  const article = await resourceArticle(page, meta), panel = article.getByRole("region", { name: "Build on push", exact: true });
  const form = panel.getByRole("form", { name: "Configure GitHub build on push", exact: true });
  const actorID = await form.getByRole("option").filter({ hasText: /^push-build-automation · maintainer · / }).getAttribute("value"); expect(actorID).toBeTruthy(); await form.getByRole("combobox", { name: "Automation service account", exact: true }).selectOption(actorID!);
  // IDs come from real metadata, while names and immutable versions are fixture inputs.
  const signing = form.getByRole("combobox", { name: "Webhook signing managed secret", exact: true });
  const signingID = await signing.getByRole("option").filter({ hasText: /^push-webhook-signing · / }).getAttribute("value"); expect(signingID).toBeTruthy(); await signing.selectOption(signingID!);
  await form.getByRole("combobox", { name: "Webhook signing immutable version", exact: true }).selectOption("1");
  const provider = form.getByRole("combobox", { name: "GitHub provider read managed secret", exact: true });
  const providerID = await provider.getByRole("option").filter({ hasText: /^push-provider-read · / }).getAttribute("value"); expect(providerID).toBeTruthy(); await provider.selectOption(providerID!);
  await form.getByRole("combobox", { name: "GitHub provider read immutable version", exact: true }).selectOption("1");
  await form.getByLabel(/I reviewed these exact references and generations/).check();
  const policyPath = `/api/v1/resources/${meta.resource_id}/github-push-policy`;
  let firstBody: unknown;
  await page.route(`**${policyPath}`, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    firstBody = route.request().postDataJSON(); const response = await route.fetch(); expect(response.status()).toBe(200); await route.abort("failed");
  }, { times: 1 });
  await form.getByRole("button", { name: "Save disabled push configuration", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(panel.getByRole("button", { name: "Retry identical push decision", exact: true })).toBeEnabled();
  await expect(form.getByRole("combobox", { name: "Automation service account", exact: true })).toBeDisabled();
  const recovered = page.waitForResponse((r) => new URL(r.url()).pathname === policyPath && r.request().method() === "PUT");
  await panel.getByRole("button", { name: "Retry identical push decision", exact: true }).focus(); await page.keyboard.press("Enter");
  const recovery = await recovered; expect(recovery.status()).toBe(200); expect(recovery.request().postDataJSON()).toEqual(firstBody);
  expect(firstBody).toMatchObject({ reset_branch_baseline: true, configuration: { webhook_secret: { id: signingID, version: 1 }, provider_read_secret: { id: providerID, version: 1 } } });
  const configured = await recovery.json() as { callback_url: string; generation: number; enabled: boolean }; expect(configured.enabled).toBe(false);
  await expect(panel.getByLabel("Server callback URL", { exact: true })).toHaveValue(configured.callback_url);
  const verified = page.waitForResponse((r) => new URL(r.url()).pathname === `${policyPath}/verify` && r.request().method() === "POST");
  await panel.getByRole("button", { name: "Verify GitHub repository and branch", exact: true }).focus(); await page.keyboard.press("Enter"); expect((await verified).status()).toBe(200);
  await expect(panel.getByRole("button", { name: "Enable build on push", exact: true })).toBeEnabled();
  const baseline = await page.request.get(`/api/v1/resources/${meta.resource_id}/builds?limit=20&offset=0`); expect(baseline.status()).toBe(200);
  const initialACount = (await baseline.json() as { items: Array<{ source_receipt?: { commit: string } }> }).items.filter((item) => item.source_receipt?.commit === meta.expected_commit_a).length;
  const enabled = page.waitForResponse((r) => new URL(r.url()).pathname === `${policyPath}/enabled` && r.request().method() === "POST");
  await panel.getByRole("button", { name: "Enable build on push", exact: true }).focus(); await page.keyboard.press("Enter"); expect((await enabled).status()).toBe(200);
  async function step(command: "push-b" | "duplicate-b" | "delayed-a" | "fail-next-build" | "push-c" | "complete") { const result = await page.request.post(`${meta.step_path}${command}`, { headers: { Origin: meta.base_url, "X-NeroCD-CSRF": "1" } }); expect(result.status(), command).toBe(204); }
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
  await article.getByRole("button", { name: "Refresh build history", exact: true }).click(); await article.getByRole("button", { name: `View build ${buildB}`, exact: true }).click();
  const detail = article.getByRole("region", { name: "Build operation detail", exact: true });
  const deployment = detail.getByRole("form", { name: "Deploy selected build", exact: true });
  await deployment.getByLabel("Build readiness timeout in seconds", { exact: true }).fill("10"); await deployment.getByLabel(/I reviewed this exact successful build/).check();
  const runtime = await page.request.get(`/api/v1/resources/${meta.resource_id}/runtime-configuration`, { timeout: 10_000 });
  expect(runtime.status(), "fixture must expose real saved runtime configuration before deployment").toBe(200);
  expect(await runtime.json()).toMatchObject({ runtime_configuration: { project_id: meta.project_id, resource_id: meta.resource_id, runtime_generation: 0 } });
  const deployButton = deployment.getByRole("button", { name: "Deploy selected build", exact: true });
  await expect(deployButton).toBeEnabled();
  const [deployResponse] = await Promise.all([
    page.waitForResponse((r) => new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/deployments/from-build` && r.request().method() === "POST", { timeout: 15_000 }),
    (async () => { await deployButton.focus(); await page.keyboard.press("Enter"); })(),
  ]);
  expect(deployResponse.status()).toBe(202);
  const deployed = await deployResponse.json() as { id: string; snapshot: { local_build: { artifact: { build_id: string; image_id: string } } } }; expect(deployed.snapshot.local_build.artifact.build_id).toBe(buildB);
  const operation = detail.getByRole("region", { name: "Deployment operation detail", exact: true }); await expect(operation.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 120_000 });
  await step("fail-next-build"); await step("push-c");
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/resources/${meta.resource_id}/builds?limit=20&offset=0`);
    const history = await response.json() as { items: Array<{ id: string; state: string; source_receipt?: { commit: string } }> };
    return history.items.find((item) => item.source_receipt?.commit === meta.expected_commit_c)?.state;
  }, { timeout: 180_000 }).toBe("failed");
  // Recorded B deployment remains inspectable. The backend fixture separately
  // probes the exact old workload after C fails; stored readiness is historical.
  await operation.getByRole("button", { name: "Refresh operation", exact: true }).click(); await expect(operation.getByText(deployed.snapshot.local_build.artifact.image_id, { exact: true }).first()).toBeVisible();
  for (const width of [320, 390, 430, 1440]) { await page.setViewportSize({ width, height: 1000 }); await expect(panel).toBeVisible(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await panel.getByRole("button", { name: "Refresh build-on-push metadata", exact: true }).focus(); await page.keyboard.press("Enter"); }
  const viewerContext = await browser.newContext({ baseURL: meta.base_url, viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await viewerContext.newPage(); const viewerReads: string[] = [];
    viewer.on("request", (r) => { if (/\/managed-secrets|\/service-accounts|\/source-grants/.test(new URL(r.url()).pathname)) viewerReads.push(r.url()); });
    await signIn(viewer, true); const read = (await resourceArticle(viewer, meta)).getByRole("region", { name: "Build on push", exact: true });
    await expect(read.getByRole("list", { name: "GitHub push deliveries", exact: true })).toBeVisible(); await expect(read.getByRole("form")).toHaveCount(0); expect(viewerReads).toEqual([]);
    const forbidden = await viewer.request.post(`${policyPath}/verify`, { headers: { Origin: meta.base_url, "X-NeroCD-CSRF": "1" }, data: { expected_policy_generation: configured.generation, request_id: crypto.randomUUID() } }); expect(forbidden.status()).toBe(403);
    for (const width of [320, 390, 430, 1440]) { await viewer.setViewportSize({ width, height: 1000 }); expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await read.screenshot({ path: `/tmp/nerocd-github-push-ui-viewer-${width}.png` }); }
  } finally { await viewerContext.close(); }
  expect(commands.filter((command) => command.path === policyPath)).toHaveLength(2); expect(forbiddenReads).toEqual([]);
  await step("complete");
});
