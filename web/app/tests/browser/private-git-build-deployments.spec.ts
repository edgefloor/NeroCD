import { createHash } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";

// Real private Git/runner/daemon fixture only. No secret values enter browser metadata.
test.use({ trace: "off", screenshot: "off" });
async function signIn(page: Page, viewer = false) {
 page.setDefaultTimeout(20_000); await page.goto("/sign-in");
 await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "browser-bootstrap@example.local");
 await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "browser-fixture-password");
 await page.getByRole("button", { name: "Sign in", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
test("owner configures private Git strategy, builds and deploys exact artifact, then preserves HTTPS after genuine failure", async ({ page, browser }, info) => {
 test.skip(!process.env.NEROCD_PRIVATE_GIT_BROWSER_META, "requires NEROCD_PRIVATE_GIT_BROWSER=1 acceptance fixture"); test.setTimeout(480_000);
 const meta = JSON.parse(process.env.NEROCD_PRIVATE_GIT_BROWSER_META!) as { origin: string; project_id: string; resource_id: string; environment: string; complete_path: string; secret_id: string; secret_version: number; source_repository_url: string; strategy: "dockerfile" | "railpack"; destination_id: string; failure_path: string; compiled_vite?: boolean; compiled_diagnostic?: boolean; compiled_probe_path: string; application_origin: string; compiled_port_path: string };
 type CompiledEvidence = { html_sha256: string; bundle_path: string; bundle_sha256: string; missing_asset_status: number };
 let compiledBaseline: CompiledEvidence | undefined;
 async function compiledApplication() {
  if (!meta.compiled_vite) return;
  const origin = new URL(meta.application_origin); expect(origin.protocol).toBe("http:"); expect(origin.hostname).toBe("127.0.0.1");
  const verified = await page.request.post(meta.compiled_probe_path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" } }); expect(verified.status()).toBe(200);
  const evidence = await verified.json() as CompiledEvidence;
  expect(evidence.bundle_path).toMatch(/^\/assets\/[a-zA-Z0-9_-]+\.js$/); expect(evidence.missing_asset_status).toBe(200);
  if (compiledBaseline) expect(evidence).toEqual(compiledBaseline); else compiledBaseline = evidence;
  // Fresh context has no NeroCD session. Browser HTTP reads a fixed bridge whose
  // upstream is independently verified fixture-CA HTTPS, never mocked content.
  const appContext = await browser.newContext({ baseURL: meta.application_origin });
  try {
   expect(await appContext.cookies()).toEqual([]);
   const app = await appContext.newPage(); await app.goto("/account/deep/link");
   await expect(app.locator("#app")).toHaveText("compiled-vite-A");
   const bundle = await app.request.get(evidence.bundle_path); expect(bundle.status()).toBe(200);
   expect(createHash("sha256").update(await bundle.body()).digest("hex")).toBe(evidence.bundle_sha256);
   const missing = await app.request.get("/missing.js"); expect(missing.status()).toBe(200); expect(missing.headers()["content-type"]).toContain("text/html");
   expect(createHash("sha256").update(await missing.body()).digest("hex")).toBe(evidence.html_sha256);
   expect((await app.request.post("/", { data: {} })).status()).toBe(405);
   let clicks = 0;
   for (const width of [320, 390, 430, 1440]) {
    await app.setViewportSize({ width, height: 900 }); const increment = app.getByRole("button", { name: "Increment compiled counter", exact: true });
    await increment.focus(); await app.keyboard.press("Enter"); await expect(app.locator("#app")).toHaveText(`compiled-vite-A clicks=${++clicks}`);
    expect(await app.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   }
   console.log(`compiled Vite browser JS executed from HTTP read bridge; verified upstream HTTPS bundle sha256=${evidence.bundle_sha256}, deep_link=200 missing.js=200 HTML`);
  } finally { await appContext.close(); }
 }
 const secretRevealReads: string[] = [], legacyLogReads: string[] = [];
 page.on("request", (request) => { if (/\/reveal(?:\?|$)/.test(request.url())) secretRevealReads.push(request.url()); });
  const assets = JSON.parse(process.env.NEROCD_PRIVATE_GIT_BROWSER_ASSETS!) as { files: Record<string,string> };
 for (const [path, digest] of Object.entries(assets.files)) { if (path === ".gitkeep") continue; const response = await page.request.get(path === "index.html" ? "/" : "/"+path); expect(response.status()).toBe(200); expect(createHash("sha256").update(await response.body()).digest("hex")).toBe(digest); if (path === "index.html") expect(response.headers()["cache-control"]).toBe("no-cache"); }
 console.log(`private ${meta.strategy} browser embedded assets matched ${Object.keys(assets.files).length-1}/${Object.keys(assets.files).length} files, excluding .gitkeep`);
 await signIn(page); await page.setViewportSize({ width: 320, height: 1000 });
 await page.goto("/projects"); const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
 await hosting.getByRole("combobox", { name: "Hosting project", exact: true }).selectOption(meta.project_id); await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
 const create = hosting.getByRole("form", { name: "Create application", exact: true });
 await create.getByLabel("Application name", { exact: true }).fill("Browser private source"); await create.getByLabel("Application source", { exact: true }).selectOption("git"); await create.getByLabel("Git repository URL", { exact: true }).fill(meta.source_repository_url); await create.getByLabel("Git source access", { exact: true }).selectOption("https_basic"); await create.getByLabel("Build strategy", { exact: true }).selectOption(meta.strategy);
 const creation = page.waitForResponse(r => new URL(r.url()).pathname === "/api/v1/resources" && r.request().method() === "POST");
 await create.getByRole("button", { name: "Save application", exact: true }).focus(); await page.keyboard.press("Enter"); const createdResponse = await creation; expect(createdResponse.status()).toBe(201); const created = await createdResponse.json(); meta.resource_id = created.id;
 expect(created).toMatchObject({ generation: 1, source: { kind: "git", git: { generation: 1, credential_kind: "https_basic", settings: { strategy: meta.strategy } } } });
 async function openResource(target: Page) {
  await target.goto("/projects"); const region = target.getByRole("region", { name: "Project hosting", exact: true });
  await region.getByRole("combobox", { name: "Hosting project", exact: true }).selectOption(meta.project_id); await region.getByRole("button", { name: meta.environment, exact: true }).click();
  const resource = region.getByRole("article").filter({ hasText: meta.source_repository_url }); await expect(resource).toHaveCount(1); return resource;
 }
 const article = await openResource(page);
 await article.getByRole("button", { name: "Edit configuration", exact: true }).click(); const editor = article.getByRole("form", { name: "Edit Browser private source", exact: true });
 await editor.getByLabel("Git branch, tag, or commit", { exact: true }).fill("refs/heads/main");
 await editor.getByLabel("Build strategy", { exact: true }).selectOption(meta.strategy === "railpack" ? "dockerfile" : "railpack"); await editor.getByLabel("Build strategy", { exact: true }).selectOption(meta.strategy); await expect(editor.getByLabel("Git source access", { exact: true })).toHaveValue("https_basic");
 const editResponse = page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}` && r.request().method() === "PATCH");
 await editor.getByRole("button", { name: "Save configuration", exact: true }).focus(); await page.keyboard.press("Enter"); const editedResponse = await editResponse; expect(editedResponse.status()).toBe(200); const edited = await editedResponse.json(); expect(editedResponse.request().postDataJSON().expected_generation).toBe(1);
 expect(edited).toMatchObject({ generation: 2, source: { kind: "git", git: { generation: 2, credential_kind: "https_basic", requested_ref: "refs/heads/main", settings: { strategy: meta.strategy } } } });
 const placement = article.getByRole("form", { name: "Save destination for Browser private source", exact: true }); await placement.getByLabel("Available destination", { exact: true }).selectOption(meta.destination_id); await placement.getByRole("button", { name: "Save desired destination", exact: true }).focus(); await page.keyboard.press("Enter"); await expect(article.getByText("Desired destination saved. This does not deploy the application.", { exact: true })).toBeVisible();
 const grants = article.getByRole("region", { name: "Private HTTPS source grants", exact: true });
 const grantForm = grants.getByRole("form", { name: "Request private source grant", exact: true });
 await grantForm.getByRole("combobox", { name: "Existing managed secret", exact: true }).selectOption(meta.secret_id);
 await grantForm.getByRole("combobox", { name: "Explicit secret version", exact: true }).selectOption(String(meta.secret_version));
 await grantForm.getByLabel(/I reviewed this exact source generation/).check();
 const grantPath = `/api/v1/resources/${meta.resource_id}/source-grants`;
 let firstGrant: { id: string } | undefined, frozenGrantBody: unknown;
 await page.route(`**${grantPath}`, async (route) => {
  if (route.request().method() !== "POST") return route.continue();
  const response = await route.fetch(); expect(response.status()).toBe(201); firstGrant = (await response.json() as { grant: { id: string } }).grant; frozenGrantBody = route.request().postDataJSON(); await route.abort("failed");
 }, { times: 1 });
 await grantForm.getByRole("button", { name: "Create source grant", exact: true }).focus(); await page.keyboard.press("Enter");
 await expect(grantForm.getByRole("button", { name: "Retry identical source grant request", exact: true })).toBeEnabled();
 await expect(grantForm.getByRole("combobox", { name: "Explicit secret version", exact: true })).toBeDisabled();
 const grantReplay = page.waitForResponse((response) => new URL(response.url()).pathname === grantPath && response.request().method() === "POST");
 await grantForm.getByRole("button", { name: "Retry identical source grant request", exact: true }).focus(); await page.keyboard.press("Enter");
 const grantResponse = await grantReplay; expect(grantResponse.status()).toBe(201); expect(grantResponse.request().postDataJSON()).toEqual(frozenGrantBody);
 const createdGrant = (await grantResponse.json() as { grant: { id: string } }).grant; expect(createdGrant.id).toBe(firstGrant?.id);
 expect(grantResponse.request().postDataJSON()).toMatchObject({ secret_id: meta.secret_id, secret_version: meta.secret_version, expected_resource_generation: 2, expected_source_generation: 2, request_id: expect.stringMatching(/^[0-9a-f]{32}$/) });
 expect(Object.keys(grantResponse.request().postDataJSON()).sort()).toEqual(["expected_resource_generation", "expected_source_generation", "request_id", "secret_id", "secret_version"]);
 const buildForm = article.getByRole("region", { name: "Request private Git build", exact: true });
 // Editing saves new generations without silently changing a reviewed build draft.
 await buildForm.getByRole("button", { name: "Refresh current build source", exact: true }).click();
 await expect(buildForm.getByRole("region", { name: "Current build source", exact: true })).toContainText("resource generation 2");
 await buildForm.getByRole("button", { name: "Use reviewed source for a separate build", exact: true }).click();
 await expect(buildForm.getByText(/Reviewed source:/)).toContainText("resource generation 2 / source generation 2");
 await buildForm.getByRole("combobox", { name: "Private source grant", exact: true }).selectOption(createdGrant.id);
 await buildForm.getByLabel(/I reviewed this exact private HTTPS Git source/).check();
 const buildResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds` && response.request().method() === "POST");
 await expect(buildForm.getByRole("button", { name: "Request private Git build", exact: true })).toBeEnabled(); await buildForm.getByRole("button", { name: "Request private Git build", exact: true }).focus(); await page.keyboard.press("Enter");
 const admission = await buildResponse; expect(admission.status()).toBe(202); expect(admission.request().postDataJSON().grant_id).toBe(createdGrant.id);
 const build = (await admission.json() as { build: { id: string; snapshot: { source: { credential_kind: string; settings: { strategy: string } } } } }).build; expect(build.snapshot.source.credential_kind).toBe("https_basic"); expect(build.snapshot.source.settings.strategy).toBe(meta.strategy);
 const detail = article.getByRole("region", { name: "Build operation detail", exact: true }); await expect(detail.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: meta.compiled_diagnostic ? 420_000 : 180_000 });
 // Observe legacy log reads for the typed-output journey; sign-in dashboard reads precede this subject.
 page.on("request", (request) => { const path = new URL(request.url()).pathname; if (path === "/api/v1/run-logs" || /\/runs\/[^/]+\/(logs|events)/.test(path)) legacyLogReads.push(request.url()); });
 const outputResponse = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/hosting-builds/${build.id}/logs`);
 await detail.getByRole("button", { name: "Show build output", exact: true }).focus(); await page.keyboard.press("Enter"); const output = await outputResponse; expect(output.status()).toBe(200);
 const persisted = await output.json() as { items: { sequence: number; message: string; component: string }[] }; expect(persisted.items.length).toBeGreaterThan(0); if (meta.compiled_vite) { expect(persisted.items.some(item => item.message.includes("8.2.2") && item.message.includes("vite"))).toBe(true); } if (meta.strategy === "railpack") { expect(persisted.items.some(item => item.component === "buildkit" && item.message.includes("railpack-plan.json"))).toBe(true); expect(persisted.items.some(item => item.component === "buildkit" && item.message.includes("[railpack]"))).toBe(true); }
 const rows = detail.getByRole("list", { name: "Build output rows", exact: true }); for (const item of persisted.items) await expect(rows.getByRole("listitem").filter({ hasText: `Sequence ${item.sequence} ·` }).locator("pre")).toHaveText(item.message);
 const form = detail.getByRole("form", { name: "Deploy selected build", exact: true });
 if (meta.compiled_vite) {
  const effective = await page.request.post(meta.compiled_port_path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" } }); expect(effective.status()).toBe(200);
  const port = (await effective.json() as { application_port: number }).application_port; expect(port).toBeGreaterThan(0); expect(port).toBeLessThanOrEqual(65535);
  await form.getByLabel("Build container port", { exact: true }).fill(String(port));
  console.log(`compiled Caddy effective application port=${port} from image PORT or pinned template fallback`);
 } await form.getByLabel("Build readiness timeout in seconds", { exact: true }).fill("10"); await form.getByLabel(/I reviewed this exact successful build/).check();
 const deployPath = `/api/v1/resources/${meta.resource_id}/deployments/from-build`;
 const deployment = page.waitForResponse((response) => new URL(response.url()).pathname === deployPath && response.request().method() === "POST");
 await form.getByRole("button", { name: "Deploy selected build", exact: true }).focus(); await page.keyboard.press("Enter"); const deployed = await deployment; expect(deployed.status()).toBe(202);
 const operation = await deployed.json() as { id: string; snapshot: { local_build: { artifact: { image_id: string; build_id: string } } } }; expect(operation.snapshot.local_build.artifact.build_id).toBe(build.id);
 const operationDetail = detail.getByRole("region", { name: "Deployment operation detail", exact: true }); await expect(operationDetail.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 120_000 }); await expect(operationDetail.getByText(operation.snapshot.local_build.artifact.image_id, { exact: true }).first()).toBeVisible();
 const routes = article.getByRole("region", { name: /Routes for / }); await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click(); await routes.getByLabel("Route hostname", { exact: true }).fill("local.example.test"); await routes.getByRole("combobox", { name: "Expected settled healthy deployment", exact: true }).selectOption(operation.id);
 await routes.getByRole("button", { name: "Save desired route", exact: true }).focus(); await page.keyboard.press("Enter"); const route = routes.getByRole("article").first(); await expect(route.getByText("Trusted HTTPS readiness recorded", { exact: true }).locator("+ dd")).toHaveText("ready", { timeout: 120_000 });
 await compiledApplication();
 for (const width of [320, 390, 430, 1440]) { await page.setViewportSize({ width, height: 1000 }); await expect(rows).toBeVisible(); await expect(route.getByText("local.example.test/", { exact: true })).toBeVisible(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await operationDetail.getByRole("button", { name: "Refresh operation", exact: true }).focus(); await page.keyboard.press("Enter"); await detail.evaluate(node => { node.scrollIntoView({ block: "start" }); window.scrollBy(0,-80); }); await page.screenshot({ path: info.outputPath(`private-${meta.strategy}-${width}.png`), animations: "disabled" }); }
 const context = await browser.newContext({ baseURL: meta.origin, viewport: { width: 390, height: 1000 } });
 try { const viewer = await context.newPage(); await signIn(viewer, true); const read = await openResource(viewer); await read.getByRole("button", { name: `View build ${build.id}`, exact: true }).click(); await expect(read.getByRole("button", { name: "Create source grant", exact: true })).toHaveCount(0); await expect(read.getByRole("button", { name: "Request private Git build", exact: true })).toHaveCount(0); await expect(read.getByRole("button", { name: "Deploy selected build", exact: true })).toHaveCount(0); const denied = await viewer.request.post(grantPath, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" }, data: frozenGrantBody }); expect(denied.status()).toBe(403); expect((await viewer.request.post(meta.failure_path, { headers: { Origin: meta.origin } })).status()).toBe(403); } finally { await context.close(); }
  await detail.getByRole("button", { name: "Close build output", exact: true }).focus(); await page.keyboard.press("Enter");
 expect((await page.request.post(meta.failure_path, { headers: { Origin: "http://127.0.0.1:1" } })).status()).toBe(403);
 expect((await page.request.post(meta.failure_path, { headers: { Origin: meta.origin } })).status()).toBe(204);
 await buildForm.getByRole("combobox", { name: "Private source grant", exact: true }).selectOption(createdGrant.id); await buildForm.getByLabel(/I reviewed this exact private HTTPS Git source/).check();
 const failedAdmission = page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds` && r.request().method() === "POST");
 await expect(buildForm.getByRole("button", { name: "Request private Git build", exact: true })).toBeEnabled(); await buildForm.getByRole("button", { name: "Request private Git build", exact: true }).focus(); await page.keyboard.press("Enter"); const failedResponse = await failedAdmission; expect(failedResponse.status()).toBe(202); const failedID = (await failedResponse.json()).build.id;
 await expect(detail.getByText("Failed", { exact: true })).toBeVisible({ timeout: 180_000 });
 const failed = await (await page.request.get(`/api/v1/hosting-builds/${failedID}`)).json(); expect(failed.build.state).toBe("failed"); expect(failed.build.artifact).toBeUndefined(); expect(failed.build.failure_code).toBe("build_failed");
 await expect(detail.getByRole("button", { name: "Deploy selected build", exact: true })).toHaveCount(0);
 const failureOutput = page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/hosting-builds/${failedID}/logs`); await detail.getByRole("button", { name: "Show build output", exact: true }).focus(); await page.keyboard.press("Enter"); expect((await failureOutput).status()).toBe(200); await expect(detail.getByRole("list", { name: "Build output rows", exact: true })).toBeVisible();
 await detail.getByRole("button", { name: "Close build output", exact: true }).click();
 expect(legacyLogReads).toEqual([]); expect(secretRevealReads).toEqual([]);
 await compiledApplication();
 expect((await page.request.post(meta.complete_path, { headers: { Origin: meta.origin } })).status()).toBe(204);
});
