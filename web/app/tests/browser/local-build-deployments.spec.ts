import { expect, test, type Page } from "@playwright/test";

// This spec requires the actual Linux build/daemon fixture, not an API mock.
// Ordinary browser runs skip it. The opt-in gate drives metadata over pipes.
test.use({ trace: "off", screenshot: "off" });
async function signIn(page: Page, viewer = false) {
  page.setDefaultTimeout(20_000);
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "browser-bootstrap@example.local");
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "browser-fixture-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
test("actual public build to local deployment and settled HTTPS route is available in the UI", async ({ page, browser }) => {
  test.skip(!process.env.NEROCD_LOCAL_BUILD_BROWSER_META, "requires NEROCD_LOCAL_BUILD_BROWSER=1 acceptance/runtime-hosting-local-build/gate.sh");
  test.setTimeout(480_000);
  const meta = JSON.parse(process.env.NEROCD_LOCAL_BUILD_BROWSER_META!) as { origin: string; project_id: string; resource_id: string; environment: string; complete_path: string };
  const genericLogRequests: string[] = [];
  page.on("request", (request) => { if (/\/runs\/[^/]+\/(logs|events)/.test(new URL(request.url()).pathname)) genericLogRequests.push(request.url()); });
  await signIn(page); await page.setViewportSize({ width: 320, height: 1000 });
  async function openResource(target: Page) {
    await target.goto("/projects");
    const region = target.getByRole("region", { name: "Project hosting", exact: true });
    await region.getByLabel("Hosting project", { exact: true }).selectOption(meta.project_id);
    await region.getByRole("button", { name: meta.environment, exact: true }).click();
    return region.getByRole("article", { name: "Browser local artifact", exact: true });
  }
  const article = await openResource(page);
  const builds = article.getByRole("region", { name: "Git builds for Browser local artifact", exact: true });
  await builds.getByLabel(/I reviewed this exact public Git source/).check();
  const buildResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds`);
  await builds.getByRole("button", { name: "Request public Git build", exact: true }).focus(); await page.keyboard.press("Enter");
  const createdBuild = await buildResponse; expect(createdBuild.status()).toBe(202);
  const selectedBuild = (await createdBuild.json() as { build: { id: string } }).build;
  const detail = builds.getByRole("region", { name: "Build operation detail", exact: true });
  await expect(detail.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 180_000 });
  const logsPath = `/api/v1/hosting-builds/${selectedBuild.id}/logs`;
  const outputResponse = page.waitForResponse((r) => new URL(r.url()).pathname === logsPath);
  await detail.getByRole("button", { name: "Show build output", exact: true }).focus(); await page.keyboard.press("Enter");
  const output = await outputResponse; expect(output.status()).toBe(200);
  expect(new URL(output.url()).search).toBe("?after_sequence=0&limit=200");
  const persisted = await output.json() as { items: { sequence: number; component: string; stream: string; message: string }[] };
  expect(persisted.items.length).toBeGreaterThan(0);
  const outputRows = detail.getByRole("list", { name: "Build output rows", exact: true });
  for (const item of persisted.items) {
    const rendered = outputRows.getByRole("listitem").filter({ hasText: `Sequence ${item.sequence} ·` });
    await expect(rendered.locator("pre")).toHaveText(item.message);
    await expect(rendered).toContainText(`component ${item.component || "unspecified"} · stream ${item.stream}`);
  }
  const form = detail.getByRole("form", { name: "Deploy selected build", exact: true });
  await form.getByLabel("Build readiness timeout in seconds", { exact: true }).fill("10");
  await form.getByLabel(/I reviewed this exact successful build/).check();
  const path = `/api/v1/resources/${meta.resource_id}/deployments/from-build`;
  let firstBody: unknown, firstOperation: string | undefined;
  await page.route(`**${path}`, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    const response = await route.fetch(); expect(response.status()).toBe(202);
    firstOperation = (await response.json() as { id: string }).id; firstBody = route.request().postDataJSON();
    await route.abort("failed");
  }, { times: 1 });
  await form.getByRole("button", { name: "Deploy selected build", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(form.getByRole("button", { name: "Retry identical local build deployment", exact: true })).toBeEnabled();
  await expect(form.getByLabel("Build readiness timeout in seconds", { exact: true })).toBeDisabled();
  const accepted = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === path);
  await form.getByRole("button", { name: "Retry identical local build deployment", exact: true }).focus(); await page.keyboard.press("Enter");
  const response = await accepted; expect(response.status()).toBe(202); expect(response.request().postDataJSON()).toEqual(firstBody);
  const operation = await response.json() as { id: string; snapshot: { local_build: { artifact: { image_id: string; build_id: string } } } };
  expect(operation.id).toBe(firstOperation);
  expect(Object.keys(response.request().postDataJSON()).sort()).toEqual(["build_id", "container_port", "expected_generation", "expected_source_generation", "idempotency_key", "readiness"]);
  const operationDetail = detail.getByRole("region", { name: "Deployment operation detail", exact: true });
  await expect(operationDetail.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 120_000 });
  await expect(operationDetail.getByText(operation.snapshot.local_build.artifact.image_id, { exact: true }).first()).toBeVisible();
  const routes = article.getByRole("region", { name: "Routes for Browser local artifact", exact: true });
  await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click();
  await routes.getByLabel("Route hostname", { exact: true }).fill("local.example.test");
  await routes.getByLabel("Expected settled healthy deployment", { exact: true }).selectOption(operation.id);
  await expect(routes.getByRole("option", { name: new RegExp(`Local build ${operation.snapshot.local_build.artifact.build_id}`) })).toBeAttached();
  await routes.getByRole("button", { name: "Save desired route", exact: true }).focus(); await page.keyboard.press("Enter");
  const routeArticle = routes.getByRole("article").first();
  await expect(routeArticle.getByText("Trusted HTTPS readiness recorded", { exact: true }).locator("+ dd")).toHaveText("ready", { timeout: 120_000 });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(outputRows).toBeVisible();
    await expect(outputRows.getByRole("listitem").first().locator("pre")).toHaveCSS("white-space", "pre-wrap");
    await expect(operationDetail.getByText("Recorded artifact image ID", { exact: true })).toBeVisible();
    await expect(routeArticle.getByText("local.example.test/", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await operationDetail.getByRole("button", { name: "Refresh operation", exact: true }).focus(); await page.keyboard.press("Enter");
  }
  const context = await browser.newContext({ viewport: { width: 390, height: 1000 } });
  try {
    const viewer = await context.newPage(); await signIn(viewer, true); const read = await openResource(viewer);
    await read.getByRole("button", { name: `View build ${operation.snapshot.local_build.artifact.build_id}`, exact: true }).click();
    await expect(read.getByRole("button", { name: "Deploy selected build", exact: true })).toHaveCount(0);
    const denied = await viewer.request.post(path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" }, data: firstBody }); expect(denied.status()).toBe(403);
  } finally { await context.close(); }
  expect(genericLogRequests).toEqual([]);
  await detail.getByRole("button", { name: "Close build output", exact: true }).focus(); await page.keyboard.press("Enter");
  await expect(outputRows).toHaveCount(0);
  // Only terminates the fixture test after assertions, never mutates app state.
  expect((await page.request.post(meta.complete_path, { headers: { Origin: meta.origin } })).status()).toBe(204);
});
