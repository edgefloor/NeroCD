import { createHash, randomBytes } from "node:crypto";
import { expect, test, type Locator, type Page } from "@playwright/test";

// Actual API, compiled runner and owned Docker only. Seeded fixture accounts and
// trusted persisted Git artifact setup are not an onboarding or fresh-build proof.
// Login credentials and selected secret values must never enter artifacts.
test.use({ trace: "off", screenshot: "off" });
type Metadata = { origin: string; project_id: string; environment: string; image_resource_id: string; git_resource_id: string; build_id: string; secret_id: string; secret_version: number; complete_path: string; proof_path: string };
// Even a failed assertion must release the Go fixture so its schema cleanup runs.
test.afterEach(async ({ page }) => {
  if (!process.env.NEROCD_RUNTIME_ENVIRONMENT_BROWSER_META) return;
  const meta = JSON.parse(process.env.NEROCD_RUNTIME_ENVIRONMENT_BROWSER_META) as Metadata;
  await page.request.post(meta.complete_path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" }, timeout: 5000 }).catch(() => {});
});
const literal = "browser literal #=$ 東京  ";
const headers = (meta: Metadata) => ({ Origin: meta.origin, "X-NeroCD-CSRF": "1" });
async function signIn(page: Page, viewer = false) {
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "runtime-owner@example.local");
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "runtime-browser-fixture-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function openResource(page: Page, meta: Metadata, name: string) {
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id);
  await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
  const article = hosting.getByRole("article", { name, exact: true }); await expect(article).toBeVisible(); return article;
}
async function fillRuntime(article: Locator, meta: Metadata) {
  const runtime = article.getByRole("region", { name: /^Runtime configuration for / });
  await runtime.getByRole("button", { name: "Add runtime variable", exact: true }).focus(); await runtime.page().keyboard.press("Enter");
  await runtime.getByLabel("Variable name 1", { exact: true }).fill("APP_VISIBLE"); await runtime.getByLabel("Nonsecret value 1", { exact: true }).fill(literal);
  await runtime.getByRole("button", { name: "Add runtime variable", exact: true }).click();
  await runtime.getByLabel("Variable name 2", { exact: true }).fill("APP_SECRET");
  await runtime.getByRole("combobox", { name: "Value type 2", exact: true }).selectOption("managed_secret");
  await runtime.getByRole("combobox", { name: "Existing managed secret for APP_SECRET", exact: true }).selectOption(meta.secret_id);
  await runtime.getByRole("combobox", { name: "Exact secret version for APP_SECRET", exact: true }).selectOption(String(meta.secret_version));
  return runtime;
}
async function actualProof(page: Page, meta: Metadata, id: string) {
  let result!: { digests: Record<string, string>; container_id: string; image_id: string; verified: boolean };
  await expect.poll(async () => {
    const response = await page.request.get(meta.proof_path + id, { headers: headers(meta) });
    if (response.status() === 200) result = await response.json(); return response.status();
  }).toBe(200);
  expect(result.verified).toBe(true); expect(Object.keys(result.digests).sort()).toEqual(["APP_SECRET", "APP_VISIBLE"]);
  expect(result.digests.APP_VISIBLE).toBe(createHash("sha256").update(literal).digest("hex")); expect(result.digests.APP_SECRET).toMatch(/^[0-9a-f]{64}$/);
  return result;
}
test("real runtime variables save and deploy to image and persisted Git artifact without rebuild", async ({ page, browser }, info) => {
  test.skip(!process.env.NEROCD_RUNTIME_ENVIRONMENT_BROWSER_META, "requires NEROCD_HOSTING_ENVIRONMENT_BROWSER=1 acceptance gate"); test.setTimeout(240_000);
  const meta = JSON.parse(process.env.NEROCD_RUNTIME_ENVIRONMENT_BROWSER_META!) as Metadata;
  const reveals: string[] = [], builds: string[] = [];
  const observe = (target: Page) => target.on("request", (request) => { const path = new URL(request.url()).pathname; if (path.endsWith("/reveal")) reveals.push(path); if (request.method() === "POST" && path.endsWith("/builds")) builds.push(path); });
  observe(page); expect((await page.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(401); await signIn(page);
  if (process.env.NEROCD_HOSTING_ENVIRONMENT_BROWSER_FAIL_AFTER_LOGIN === "1") throw new Error("intentional authenticated browser failure verifies fixture cleanup");
  const identity = await (await page.request.get("/api/v1/me")).json(); expect(identity.roles).not.toContain("system_admin");
  const article = await openResource(page, meta, "Runtime browser image"); const runtime = await fillRuntime(article, meta);
  const savePath = `/api/v1/resources/${meta.image_resource_id}/runtime-configuration`;
  const saveBodies: string[] = []; let savedGeneration = 0;
  // Fault injection forwards the actual server request and commits its receipt,
  // then drops only the first response. It never fabricates an API success.
  await page.route("**" + savePath, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    saveBodies.push(route.request().postData()!); const response = await route.fetch(); expect(response.status()).toBe(200);
    savedGeneration = (await response.json()).runtime_configuration.runtime_generation;
    if (saveBodies.length === 1) return route.abort("failed");
    return route.fulfill({ response });
  });
  await runtime.getByRole("button", { name: "Save runtime configuration", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(runtime.getByText(/Save delivery is unconfirmed/)).toBeVisible(); await expect(runtime.getByLabel("Nonsecret value 1", { exact: true })).toBeDisabled();
  await runtime.getByRole("button", { name: "Retry identical runtime save", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(runtime.getByText("Saved runtime generation 1 · resource generation 1", { exact: true })).toBeVisible(); expect(savedGeneration).toBe(1); expect(saveBodies).toHaveLength(2); expect(saveBodies[0]).toBe(saveBodies[1]);
  const savedBody = JSON.parse(saveBodies[0]!); expect(savedBody.entries).toEqual([{ name: "APP_VISIBLE", kind: "literal", value: literal }, { name: "APP_SECRET", kind: "managed_secret", secret: { secret_id: meta.secret_id, version: 1 } }]);
  await page.unroute("**" + savePath);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await expect(runtime.getByRole("button", { name: "Save runtime configuration", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await runtime.screenshot({ path: info.outputPath(`real-runtime-configuration-${width}.png`), animations: "disabled" });
  }
  const form = article.getByRole("form", { name: "Deploy image for Runtime browser image", exact: true });
  await form.getByLabel("Container port", { exact: true }).fill("8080"); await form.getByLabel("Readiness timeout in seconds", { exact: true }).fill("2"); await form.getByLabel(/I understand that/).check();
  const deployPath = `/api/v1/resources/${meta.image_resource_id}/deployments`; const deploymentBodies: string[] = []; let imageOperation = "";
  await page.route("**" + deployPath, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    deploymentBodies.push(route.request().postData()!); const response = await route.fetch(); expect(response.status()).toBe(202);
    const admitted = await response.json(); if (!imageOperation) imageOperation = admitted.id; expect(admitted.id).toBe(imageOperation);
    if (deploymentBodies.length === 1) return route.abort("failed"); return route.fulfill({ response });
  });
  await form.getByRole("button", { name: "Request image deployment", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(form.getByRole("button", { name: "Retry identical deployment request", exact: true })).toBeVisible();
  await form.getByRole("button", { name: "Retry identical deployment request", exact: true }).focus(); await page.keyboard.press("Enter");
  const detail = article.getByRole("region", { name: "Deployment operation detail", exact: true }); await expect(detail.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 60_000 });
  expect(deploymentBodies).toHaveLength(2); expect(deploymentBodies[0]).toBe(deploymentBodies[1]); expect(JSON.parse(deploymentBodies[0]!).expected_runtime_configuration_generation).toBe(1);
  await page.unroute("**" + deployPath); const firstProof = await actualProof(page, meta, imageOperation);
  await form.getByLabel("Readiness path", { exact: true }).fill("/unready"); await form.getByLabel(/I understand that/).check();
  const failedResponse = page.waitForResponse((response) => new URL(response.url()).pathname === deployPath && response.request().method() === "POST");
  await form.getByRole("button", { name: "Request image deployment", exact: true }).click(); expect((await failedResponse).status()).toBe(202);
  await expect(detail.getByText("The result records restoration of the previous workload.", { exact: true })).toBeVisible({ timeout: 60_000 }); expect(await actualProof(page, meta, imageOperation)).toEqual(firstProof);
  // Hold an actual GET response while a separate actual PUT advances its CAS.
  // Returning the original bytes creates a real concurrent-edit 409 on admission.
  let edited = false, staleAdmissions = 0;
  page.on("request", (request) => { if (edited && new URL(request.url()).pathname === deployPath && request.method() === "POST") staleAdmissions++; });
  await page.route("**" + savePath, async (route) => {
    if (route.request().method() !== "GET" || edited) return route.continue();
    const response = await route.fetch(); expect(response.status()).toBe(200); const current = (await response.json()).runtime_configuration;
    const updated = await page.request.put(savePath, { headers: headers(meta), data: { expected_resource_generation: current.resource_generation, expected_runtime_generation: current.runtime_generation, request_id: randomBytes(16).toString("hex"), entries: current.entries } }); expect(updated.status()).toBe(200); edited = true; return route.fulfill({ response });
  });
  await form.getByLabel("Readiness path", { exact: true }).fill("/"); await form.getByLabel(/I understand that/).check();
  const stale = page.waitForResponse((response) => new URL(response.url()).pathname === deployPath && response.request().method() === "POST");
  await form.getByRole("button", { name: "Request image deployment", exact: true }).click(); expect((await stale).status()).toBe(409);
  await expect(form.getByRole("button", { name: "Retry identical deployment request", exact: true })).toBeDisabled(); expect(staleAdmissions).toBe(1); expect(await actualProof(page, meta, imageOperation)).toEqual(firstProof); await page.unroute("**" + savePath);
  const git = await openResource(page, meta, "runtime Git artifact"); const gitRuntime = await fillRuntime(git, meta);
  await gitRuntime.getByRole("button", { name: "Save runtime configuration", exact: true }).focus(); await page.keyboard.press("Enter"); await expect(gitRuntime.getByText("Saved runtime generation 1 · resource generation 1", { exact: true })).toBeVisible();
  await git.getByRole("button", { name: `View build ${meta.build_id}`, exact: true }).click();
  const local = git.getByRole("form", { name: "Deploy selected build", exact: true }); await local.getByLabel("Build container port", { exact: true }).fill("8080"); await local.getByLabel("Build readiness timeout in seconds", { exact: true }).fill("2"); await local.getByLabel(/I reviewed this exact successful build/).check();
  const localResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/resources/${meta.git_resource_id}/deployments/from-build` && response.request().method() === "POST");
  await local.getByRole("button", { name: "Deploy selected build", exact: true }).focus(); await page.keyboard.press("Enter"); const admitted = await localResponse; expect(admitted.status()).toBe(202); const localOperation = await admitted.json(); expect(localOperation.snapshot.local_build.artifact.build_id).toBe(meta.build_id); expect(admitted.request().postDataJSON().expected_runtime_configuration_generation).toBe(1);
  await expect(git.getByRole("region", { name: "Deployment operation detail", exact: true }).getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 60_000 }); const localProof = await actualProof(page, meta, localOperation.id); expect(localProof.image_id).toBe(firstProof.image_id); expect(localProof.digests).toEqual(firstProof.digests); expect(builds).toEqual([]);
  const context = await browser.newContext({ baseURL: meta.origin, viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await context.newPage(); observe(viewer); await signIn(viewer, true); const read = await openResource(viewer, meta, "Runtime browser image");
    await expect(read.getByText("Project owner access is required to save runtime variables.", { exact: true })).toBeVisible(); await expect(read.getByRole("form", { name: "Save runtime variables", exact: true })).toHaveCount(0); await expect(read.getByRole("form", { name: "Deploy image for Runtime browser image", exact: true })).toHaveCount(0);
    expect((await viewer.request.put(savePath, { headers: headers(meta), data: savedBody })).status()).toBe(403);
    expect((await viewer.request.post(deployPath, { headers: headers(meta), data: JSON.parse(deploymentBodies[0]!) })).status()).toBe(403);
    expect((await viewer.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(403);
  } finally { await context.close(); }
  expect(reveals).toEqual([]); expect(builds).toEqual([]);
  expect((await page.request.post(meta.complete_path, { headers: { "X-NeroCD-CSRF": "1", Origin: "http://127.0.0.1:1" } })).status()).toBe(403);
  expect((await page.request.post(meta.complete_path, { headers: headers(meta) })).status()).toBe(204);
});
