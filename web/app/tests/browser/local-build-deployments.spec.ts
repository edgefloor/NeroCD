import { createHash } from "node:crypto";
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
test("actual source build to local deployment and settled HTTPS route is available in the UI", async ({ page, browser }) => {
  test.skip(!process.env.NEROCD_LOCAL_BUILD_BROWSER_META, "requires NEROCD_LOCAL_BUILD_BROWSER=1 acceptance/runtime-hosting-local-build/gate.sh");
  test.setTimeout(480_000);
  const meta = JSON.parse(process.env.NEROCD_LOCAL_BUILD_BROWSER_META!) as { origin: string; project_id: string; resource_id: string; environment: string; complete_path: string; stored_dockerfile: boolean; destination_id: string; content_a: string; content_b: string; content_failed: string; probe_path: string };
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
  if (meta.stored_dockerfile) {
    const assets = JSON.parse(process.env.NEROCD_LOCAL_BUILD_BROWSER_ASSETS!) as { files: Record<string, string> };
    for (const [path, digest] of Object.entries(assets.files)) {
      if (path === ".gitkeep") continue;
      const served = await page.request.get(path === "index.html" ? "/" : "/" + path);
      expect(served.status()).toBe(200);
      expect(createHash("sha256").update(await served.body()).digest("hex")).toBe(digest);
    }
    await page.goto("/projects");
    const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
    await hosting.getByRole("combobox", { name: "Hosting project", exact: true }).selectOption(meta.project_id);
    await hosting.getByRole("button", { name: meta.environment, exact: true }).click();
    const create = hosting.getByRole("form", { name: "Create application", exact: true });
    await create.getByLabel("Application name", { exact: true }).fill("Browser local artifact");
    await create.getByLabel("Application source", { exact: true }).selectOption("dockerfile");
    await create.getByLabel("Dockerfile content", { exact: true }).fill(meta.content_a);
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      await expect(create.getByLabel("Dockerfile content", { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
    const creation = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/resources");
    await create.getByRole("button", { name: "Save application", exact: true }).click();
    const saved = await creation; expect(saved.status()).toBe(201);
    const resource = await saved.json(); meta.resource_id = resource.id;
    expect(resource.source.dockerfile.content).toBe(meta.content_a);
    expect(resource.source.dockerfile.content_hash).toBe("sha256:" + createHash("sha256").update(meta.content_a).digest("hex"));
  }
  const article = await openResource(page);
  if (meta.stored_dockerfile) {
    const placement = article.getByRole("form", { name: "Save destination for Browser local artifact", exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(meta.destination_id);
    await placement.getByRole("button", { name: "Save desired destination", exact: true }).click();
    await expect(article.getByText("Desired destination saved. This does not deploy the application.", { exact: true })).toBeVisible();
  }
  const builds = article.getByRole("region", { name: `${meta.stored_dockerfile ? "Dockerfile" : "Git"} builds for Browser local artifact`, exact: true });
  await builds.getByLabel(meta.stored_dockerfile ? "I reviewed this exact stored Dockerfile and generations for this build request." : /I reviewed this exact public Git source/).check();
  const buildResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds`);
  await builds.getByRole("button", { name: meta.stored_dockerfile ? "Request Dockerfile build" : "Request public Git build", exact: true }).focus(); await page.keyboard.press("Enter");
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
  if (meta.stored_dockerfile) {
    const probe = async (want: string) => {
      const response = await page.request.get(meta.probe_path, { headers: { Origin: meta.origin } });
      expect(response.status()).toBe(200); expect(await response.text()).toBe(want + "\n");
    };
    await probe("stored-A");
    async function editContent(content: string) {
      await article.getByRole("button", { name: "Edit configuration", exact: true }).click();
      const editor = article.getByRole("form", { name: "Edit Browser local artifact", exact: true });
      await editor.getByLabel("Dockerfile content", { exact: true }).fill(content);
      for (const width of [320, 390, 430, 1440]) {
        await page.setViewportSize({ width, height: 1000 });
        await expect(editor.getByLabel("Dockerfile content", { exact: true })).toHaveValue(content);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      }
      const saved = page.waitForResponse(r => r.request().method() === "PATCH" && new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}`);
      await editor.getByRole("button", { name: "Save configuration", exact: true }).click();
      const response = await saved; expect(response.status()).toBe(200);
      const resource = await response.json(); expect(resource.source.dockerfile.content).toBe(content);
      await builds.getByRole("button", { name: "Refresh current build source", exact: true }).click();
      await builds.getByRole("button", { name: "Use reviewed source for a separate build", exact: true }).click();
      return resource;
    }
    const changed = await editContent(meta.content_b);
    expect(changed.source.dockerfile.generation).toBe(2);
    const stale = await page.request.post(path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" }, data: { ...firstBody as object, idempotency_key: "stored-stale-build" } });
    expect(stale.status()).toBe(409);
    await builds.getByLabel("I reviewed this exact stored Dockerfile and generations for this build request.", { exact: true }).check();
    const requestedB = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds`);
    await builds.getByRole("button", { name: "Request Dockerfile build", exact: true }).click();
    const requestedResponseB = await requestedB; expect(requestedResponseB.status()).toBe(202);
    const requestedBuildB = (await requestedResponseB.json()).build;
    expect(requestedBuildB.id).not.toBe(selectedBuild.id);
    await expect(detail.getByText(`Build ${requestedBuildB.id}`, { exact: true })).toBeVisible();
    await expect(detail.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 180_000 });
    const recordedB = await page.request.get(`/api/v1/hosting-builds/${requestedBuildB.id}`);
    expect(recordedB.status()).toBe(200);
    const buildB = (await recordedB.json()).build;
    expect(buildB.snapshot.source.generation).toBe(changed.source.dockerfile.generation);
    expect(buildB.source_receipt).toMatchObject({ kind: "dockerfile", content_hash: changed.source.dockerfile.content_hash });
    expect(buildB.source_receipt.commit).toBeUndefined();
    expect(buildB.source_receipt.tree_digest).toMatch(/^sha256:/);
    expect(Object.keys(buildB.snapshot.source).sort()).toEqual(["content_hash", "generation", "kind", "settings", "source_id"]);
    expect(Object.keys(buildB.source_receipt).sort()).toEqual(["content_hash", "kind", "tree_digest"]);
    await form.getByLabel("Build readiness timeout in seconds", { exact: true }).fill("10");
    await form.getByLabel(/I reviewed this exact successful build/).check();
    const deployedB = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname === path);
    await form.getByRole("button", { name: "Deploy selected build", exact: true }).click();
    const responseB = await deployedB; expect(responseB.status()).toBe(202);
    const operationB = await responseB.json();
    expect(operationB.snapshot.local_build.artifact.image_id).not.toBe(operation.snapshot.local_build.artifact.image_id);
    await expect(operationDetail.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible({ timeout: 120_000 });
    await probe("stored-A");
    await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click();
    await routes.getByRole("button", { name: "Review refreshed routes for a separate request", exact: true }).click();
    await routeArticle.getByRole("button", { name: /^Edit route / }).click();
    await routes.getByLabel("Expected settled healthy deployment", { exact: true }).selectOption(operationB.id);
    const routeChange = page.waitForResponse(r => r.request().method() === "PATCH" && new URL(r.url()).pathname.startsWith("/api/v1/routes/"));
    await routes.getByRole("button", { name: "Save desired route", exact: true }).click();
    const changedRouteResponse = await routeChange; expect(changedRouteResponse.status()).toBe(202);
    const changedRoute = await changedRouteResponse.json(); expect(changedRoute.selected_release_operation_id).toBe(operationB.id);
    await expect(routeArticle.getByText("Trusted HTTPS readiness recorded", { exact: true }).locator("+ dd")).toHaveText("ready", { timeout: 120_000 });
    await expect.poll(async () => { const r = await page.request.get(meta.probe_path, { headers: { Origin: meta.origin } }); return r.status() === 200 ? r.text() : ""; }, { timeout: 120_000 }).toBe("stored-B\n");
    const settledBResponse = await page.request.get(`/api/v1/hosting-operations/${operationB.id}`);
    expect(settledBResponse.status()).toBe(200);
    const settledB = await settledBResponse.json();
    expect(settledB.result).toMatchObject({ ready: true, uncertain: false, restored: false, image_id: operationB.snapshot.local_build.artifact.image_id });
    expect(settledB.result.container_id).toMatch(/^[a-f0-9]{64}$/);
    await editContent(meta.content_failed);
    await builds.getByLabel("I reviewed this exact stored Dockerfile and generations for this build request.", { exact: true }).check();
    const requestedFailure = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${meta.resource_id}/builds`);
    await builds.getByRole("button", { name: "Request Dockerfile build", exact: true }).click();
    const failedResponse = await requestedFailure; expect(failedResponse.status()).toBe(202);
    const failedBuild = (await failedResponse.json()).build;
    await expect(detail.getByText(`Build ${failedBuild.id}`, { exact: true })).toBeVisible();
    await expect(detail.getByText("Failed", { exact: true })).toBeVisible({ timeout: 180_000 });
    const failureLogs = await page.request.get(`/api/v1/hosting-builds/${failedBuild.id}/logs?after_sequence=0&limit=200`);
    expect(failureLogs.status()).toBe(200);
    const failureOutput = (await failureLogs.json()).items.map((row: { message: string }) => row.message).join("\n");
    expect(failureOutput).toMatch(/did not complete successfully: exit code: 9/);
    const failedResource = await (await page.request.get(`/api/v1/resources/${meta.resource_id}`)).json();
    const denied = await page.request.post(path, { headers: { Origin: meta.origin, "X-NeroCD-CSRF": "1" }, data: { build_id: failedBuild.id, expected_generation: failedResource.generation, expected_source_generation: failedResource.source.dockerfile.generation, idempotency_key: "stored-failed-deploy", container_port: 80, readiness: { path: "/", timeout_seconds: 10 } } });
    expect(denied.status()).toBe(409);
    const retained = await page.request.get(`/api/v1/hosting-operations/${operationB.id}`);
    expect(retained.status()).toBe(200);
    const retainedB = await retained.json();
    expect(retainedB.state).toBe("succeeded"); expect(retainedB.result).toEqual(settledB.result); expect(retainedB.snapshot).toEqual(settledB.snapshot);
    const preservedRouteResponse = await page.request.get(`/api/v1/routes/${changedRoute.id}`); expect(preservedRouteResponse.status()).toBe(200);
    expect(await preservedRouteResponse.json()).toMatchObject({ version: changedRoute.version, selected_release_operation_id: operationB.id, https_ready: true });
    await probe("stored-B");
    console.log("stored Dockerfile UI save/build/deploy/edit/rebuild/route and failed-build preservation passed");
  }
  // Only terminates the fixture test after assertions, never mutates app state.
  expect((await page.request.post(meta.complete_path, { headers: { Origin: meta.origin } })).status()).toBe(204);
});
