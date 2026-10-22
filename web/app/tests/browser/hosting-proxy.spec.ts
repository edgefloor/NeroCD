import { expect, test, type Page } from "@playwright/test";
import { request as httpsRequest } from "node:https";
import { spawn, spawnSync } from "node:child_process";
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// No automatic screenshot or trace may contain one-time enrollment material.
test.use({ trace: "off", screenshot: "off" });
function fixtureDirectory() {
  const directory = process.env.NEROCD_HOSTING_UI_FIXTURE_DIR;
  if (!directory || !/^\/tmp\/nerocd-proxy-ui\.[A-Za-z0-9]+$/.test(directory)) throw new Error("run through hosting-proxy-fixture.sh");
  return realpathSync(directory);
}
function runtime() {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser runtime unavailable");
  return realpathSync(join("/tmp", `nerocd-browser-${runID}`));
}
const headers = (page: Page) => ({ Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" });
async function signIn(page: Page) {
  const [email, password] = readFileSync(join(runtime(), "credentials"), "utf8").trim().split("\n");
  if (!email || !password) throw new Error("browser credentials unavailable");
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(email); await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
type Agent = { directory: string; origin: string; proxy?: boolean };
function agentArguments(agent: Agent, execute: boolean) {
  const endpoint = process.env.NEROCD_HOSTING_UI_DOCKER_ENDPOINT;
  if (!endpoint?.startsWith("unix:///")) throw new Error("explicit local Docker endpoint unavailable");
  const credential = join(agent.directory, "runner.credential");
  const enrollmentArgs = existsSync(credential) ? [] : ["--enrollment-file", join(agent.directory, "enrollment.token")];
  return ["runner", "--server", agent.origin, ...enrollmentArgs, "--credential-file", credential, "--work-dir", join(agent.directory, "work"), "--journal-dir", join(agent.directory, "journal"), "--compose-image-policy", "pull", "--image-docker-endpoint", endpoint, "--once", `--execute=${execute}`];
}
function runAgent(agent: Agent, execute: boolean) {
  if (agent.proxy) {
    const container = process.env.NEROCD_PROXY_BROWSER_CONTAINER;
    if (!container || !/^[a-f0-9]{64}$/.test(container)) throw new Error("owned proxy daemon unavailable");
    const directory = `/var/lib/agents/${agent.directory.split("/").pop()}`;
    const inspect = spawnSync("docker", ["exec", container, "test", "-d", directory], { stdio: "pipe" });
    if (inspect.status !== 0) {
      const archive = spawnSync("tar", ["-C", join(agent.directory, ".."), "-cf", "-", agent.directory.split("/").pop()!], { stdio: "pipe" });
      if (archive.status !== 0) throw new Error("protected agent archive failed");
      const copy = spawnSync("docker", ["exec", "-i", container, "tar", "-xf", "-", "-C", "/var/lib/agents"], { stdio: "pipe", input: archive.stdout });
      if (copy.status !== 0) throw new Error("protected agent file copy failed");
      if (spawnSync("docker", ["exec", container, "chown", "-R", "0:0", directory], { stdio: "pipe" }).status !== 0) throw new Error("owned Linux agent permissions failed");
    }
    const origin = new URL(agent.origin); origin.hostname = "host.docker.internal";
    const result = spawnSync("docker", ["exec", "--env", `NEROCD_PROXY_BROWSER_AGENT=${directory}`, "--env", `NEROCD_PROXY_BROWSER_SERVER=${origin.origin}`, container, "/tmp/proxy-browser.test", "-test.run", "^TestProxyBrowserHarness$", "-test.timeout", "120s"], { stdio: "pipe", timeout: 130_000 });
    if (result.status !== 0 || result.error) { writeFileSync("/tmp/nerocd-proxy-ui-runtime-agent-error.log", (result.stdout?.toString() ?? "").replace(/ncr_[A-Za-z0-9]+/g, "[redacted]"), { mode: 0o600 }); throw new Error("real proxy agent failed; protected output withheld"); }
    return;
  }
  const result = spawnSync(join(runtime(), "nerocd"), agentArguments(agent, execute), { env: { ...process.env, NEROCD_MODE: "development", NEROCD_TOKEN: "" }, stdio: "pipe", timeout: 45_000 });
  if (result.pid) appendFileSync(join(fixtureDirectory(), "runner-pids"), `${result.pid}\n`, { mode: 0o600 });
  if (result.error || result.status !== 0) throw new Error("task-owned image runner failed; output withheld to protect enrollment material");
}
async function enroll(page: Page, suffix: string, proxy = false): Promise<{ agent: Agent; runnerID: string }> {
  await page.goto("/runners");
  const runnerID = `runner_image_ui_${suffix}`;
  const form = page.getByRole("form", { name: "Create runner enrollment", exact: true });
  await form.getByLabel("Runner ID", { exact: true }).fill(runnerID); await form.getByLabel("Runner name", { exact: true }).fill(`Image host ${suffix}`);
  await form.getByLabel("Runner tags", { exact: true }).fill(`image-ui-${suffix}`);
  await form.getByRole("button", { name: "Use image hosting capability preset", exact: true }).click();
  await expect(form.getByLabel("Runner capabilities", { exact: true })).toHaveValue("compose-deploy,image_deploy,image_release_ports_v1");
  if (proxy) await form.getByLabel("Runner capabilities", { exact: true }).fill("compose-deploy,image_deploy,image_release_ports_v1,proxy_apply");
  const response = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/runner-enrollments");
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const created = await response; expect(created.status()).toBe(201);
  const body = await created.json() as { token: string };
  const directory = mkdtempSync(join(fixtureDirectory(), "agent-")); chmodSync(directory, 0o700);
  mkdirSync(join(directory, "work"), { mode: 0o700 }); mkdirSync(join(directory, "journal"), { mode: 0o700 });
  writeFileSync(join(directory, "enrollment.token"), body.token + "\n", { mode: 0o600 });
  await expect(page.getByLabel("Enrollment secret", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape"); await expect(page.getByLabel("Enrollment secret", { exact: true })).toHaveCount(0);
  const agent = { directory, origin: new URL(page.url()).origin, proxy };
  runAgent(agent, false);
  return { agent, runnerID };
}
async function createProject(page: Page, width: number, name: string) {
  await page.goto("/projects");
  if (width < 1280) await page.getByRole("button", { name: "Configure project", exact: true }).click();
  const scope = width < 1280 ? page.getByRole("dialog", { name: "Configure project", exact: true }) : page.locator("main");
  await scope.getByLabel("Project Name", { exact: true }).fill(name);
  const response = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/projects");
  await scope.getByRole("button", { name: "Create project", exact: true }).click();
  const created = await response; expect(created.status()).toBe(201);
  if (width < 1280) await page.keyboard.press("Escape");
  return (await created.json() as { id: string }).id;
}
async function hosting(page: Page, projectID: string, environment?: string) {
  await page.goto("/projects");
  const region = page.getByRole("region", { name: "Project hosting", exact: true });
  await region.getByLabel("Hosting project", { exact: true }).selectOption(projectID);
  if (environment) await region.getByRole("button", { name: environment, exact: true }).click();
  return region;
}
function captureOperation(operation: { id: string; binding_id: string }) {
  if (!/^hop_[A-Za-z0-9_-]+$/.test(operation.id) || !/^binding_[A-Za-z0-9_-]+$/.test(operation.binding_id)) throw new Error("unexpected operation ownership identity");
  appendFileSync(join(fixtureDirectory(), "operations"), `${operation.binding_id} ${operation.id}\n`, { mode: 0o600 });
}
function removeCompletedFixtureContainer(containerID: string, bindingID: string, operationID: string) {
  if (!/^[a-f0-9]{64}$/.test(containerID)) throw new Error("invalid captured fixture container");
  const endpoint = process.env.NEROCD_HOSTING_UI_DOCKER_ENDPOINT!;
  const inspected = spawnSync("docker", ["--host", endpoint, "inspect", "--format", '{{.Id}}|{{index .Config.Labels "io.nerocd.binding"}}|{{index .Config.Labels "io.nerocd.operation"}}', containerID], { encoding: "utf8", stdio: "pipe" });
  if (inspected.status !== 0 || inspected.stdout.trim() !== `${containerID}|${bindingID}|${operationID}`) throw new Error("fixture container ownership mismatch");
  const removed = spawnSync("docker", ["--host", endpoint, "rm", "-f", containerID], { stdio: "pipe" });
  if (removed.status !== 0) throw new Error("captured fixture container cleanup failed");
}

async function settleProxy(page: Page, agent: Agent, server: string) {
  for (let attempt = 0; attempt < 10; attempt++) {
    runAgent(agent, true);
    const p = await (await page.request.get(`/api/v1/servers/${server}/proxy`)).json() as { desired_generation: number; loaded_generation: number; hold_operation_id?: string };
    if (p.desired_generation === p.loaded_generation && !p.hold_operation_id) return;
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  throw new Error("real proxy generation did not settle");
}
async function trustedRoute(hostname: string, path: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const request = httpsRequest({ hostname: "127.0.0.1", port: Number(process.env.NEROCD_PROXY_BROWSER_HTTPS_PORT), servername: hostname, path, ca: readFileSync(join(fixtureDirectory(), "proxy-issuance.pem")), headers: { Host: hostname }, timeout: 10_000 }, (response) => { let body = ""; response.setEncoding("utf8"); response.on("data", (chunk: string) => body += chunk); response.on("end", () => response.statusCode === 200 ? resolve(body) : reject(new Error(`fixture HTTPS status ${response.statusCode}`))); });
    request.on("error", reject); request.on("timeout", () => request.destroy(new Error("fixture HTTPS timeout"))); request.end();
  });
}

test("real healthy image supports authorized queued proxy forms without fabricated loaded or HTTPS state", async ({ page, browser }, info) => {
  test.setTimeout(240_000); fixtureDirectory();
  const image = process.env.NEROCD_PROXY_IMAGE_A;
  if (!image) throw new Error("task-owned image unavailable");
  await signIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    const suffix = `${width}_${Date.now()}`;
    const projectID = await createProject(page, width, `Proxy UI ${suffix}`);
    const { agent, runnerID } = await enroll(page, suffix, true);
    const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
    await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
    await servers.getByLabel("Hosting server name", { exact: true }).fill(`Proxy host ${suffix}`);
    await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const createdServer = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/servers");
    await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
    const server = await (await createdServer).json() as { server: { id: string }; destination: { id: string } };
    await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID);
    await servers.getByRole("button", { name: "Grant project access", exact: true }).click();
    await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
    const proxy = servers.getByRole("region", { name: "Managed proxy", exact: true });
    await proxy.getByLabel("Immutable control project ID", { exact: true }).fill(projectID);
    await proxy.getByLabel("ACME email", { exact: true }).fill("proxy-ui@example.test");
    await proxy.getByLabel("Optional generated hostname domain", { exact: true }).fill(`generated-${suffix.replaceAll("_", "-")}.example.test`);
    const configured = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/servers/${server.server.id}/proxy`);
    await proxy.getByRole("button", { name: "Configure managed proxy", exact: true }).focus(); await page.keyboard.press("Enter");
    const proxyRecord = await configured; expect(proxyRecord.status()).toBe(202);
    const proxyID = (await proxyRecord.json() as { id: string }).id;
    await expect(proxy.getByText("Loaded generation", { exact: true })).toBeVisible();
    const host = `web-${suffix.replaceAll("_", "-")}.example.test`;
    await proxy.getByLabel("Hostname project ID", { exact: true }).fill(projectID);
    await proxy.getByLabel("Hostname or namespace root", { exact: true }).fill(host);
    const granted = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/hostname-grants"));
    await proxy.getByRole("button", { name: "Grant hostname authority", exact: true }).click(); expect((await granted).status()).toBe(201);
    const namespace = `generated-${suffix.replaceAll("_", "-")}.example.test`;
    await proxy.getByLabel("Hostname project ID", { exact: true }).fill(projectID);
    await proxy.getByLabel("Hostname or namespace root", { exact: true }).fill(namespace);
    await proxy.getByLabel("Authorize descendants of this namespace", { exact: true }).check();
    const wildcardGranted = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/hostname-grants"));
    await proxy.getByRole("button", { name: "Grant hostname authority", exact: true }).click(); expect((await wildcardGranted).status()).toBe(201);
    await page.screenshot({ path: info.outputPath(`proxy-admin-${width}.png`), fullPage: true, animations: "disabled" });
    const region = await hosting(page, projectID);
    const environment = `Production ${suffix}`;
    await region.getByLabel("Environment name", { exact: true }).fill(environment); await region.getByRole("button", { name: "Create shared environment", exact: true }).click();
    const name = `Proxy web ${suffix}`;
    const create = region.getByRole("form", { name: "Create application", exact: true });
    await create.getByLabel("Application name", { exact: true }).fill(name); await create.getByLabel("Image reference", { exact: true }).fill(image);
    const created = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/resources");
    await create.getByRole("button", { name: "Save application", exact: true }).click();
    const resource = await (await created).json() as { id: string };
    const placement = region.getByRole("region", { name: `Desired destination for ${name}`, exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(server.destination.id); await placement.getByRole("button", { name: "Save desired destination", exact: true }).click();
    const deployments = region.getByRole("region", { name: `Image deployments for ${name}`, exact: true });
    await deployments.getByLabel("Container port", { exact: true }).fill("8080"); await deployments.getByLabel("Readiness timeout in seconds", { exact: true }).fill("10"); await deployments.getByLabel(/I understand that/).check();
    const deployed = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${resource.id}/deployments`);
    await deployments.getByRole("button", { name: "Request image deployment", exact: true }).click();
    const op = await (await deployed).json() as { id: string; binding_id: string; snapshot: { host_port: number } }; captureOperation(op);
    let settled: { state: string; result?: { container_id: string } } | undefined;
    for (let attempt = 0; attempt < 5; attempt++) { runAgent(agent, true); settled = await (await page.request.get(`/api/v1/hosting-operations/${op.id}`)).json(); if (settled?.state === "succeeded") break; }
    expect(settled?.state).toBe("succeeded");
    const routes = region.getByRole("region", { name: `Routes for ${name}`, exact: true });
    await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click();
    await routes.getByLabel("Route hostname", { exact: true }).fill(host); await routes.getByLabel("Expected settled healthy deployment", { exact: true }).selectOption(op.id);
    const routePath = `/api/v1/resources/${resource.id}/routes`;
    let initial: { id: string; operation_id: string; version: number } | undefined;
    if (width === 320) {
      await page.route(`**${routePath}`, async (intercept) => { if (intercept.request().method() !== "POST") return intercept.continue(); const response = await intercept.fetch(); expect(response.status()).toBe(202); initial = await response.json(); await intercept.abort("failed"); }, { times: 1 });
      await routes.getByRole("button", { name: "Save desired route", exact: true }).click();
      await expect(routes.getByRole("button", { name: "Retry identical route request", exact: true })).toBeVisible();
    }
    const saved = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === routePath);
    await routes.getByRole("button", { name: width === 320 ? "Retry identical route request" : "Save desired route", exact: true }).click();
    const response = await saved; expect(response.status()).toBe(202); const route = await response.json() as { id: string; operation_id: string; version: number }; if (initial) expect(route.id).toBe(initial.id);
    await expect(routes.getByRole("region", { name: "Route operation status", exact: true })).toBeVisible();
    await expect(routes.getByText("Trusted HTTPS readiness recorded", { exact: true })).toBeVisible(); await expect(routes.getByText("not confirmed", { exact: true })).toHaveCount(2);
    const current = await (await page.request.get(`/api/v1/servers/${server.server.id}/proxy`)).json() as { loaded_generation: number; desired_generation: number };
    expect(current.loaded_generation).toBeGreaterThan(0); expect(current.desired_generation).toBeGreaterThan(0);
    await settleProxy(page, agent, server.server.id);
    await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click();
    await expect(routes.getByText("ready", { exact: true })).toHaveCount(2);
    expect((await trustedRoute(host, "/")).trim()).toBe("A");
    expect((await (await page.request.get(`http://127.0.0.1:${process.env.NEROCD_PROXY_BROWSER_HTTP_PORT}/`, { headers: { Host: host } })).text()).trim()).toBe("A");
    await routes.getByRole("button", { name: `Edit route ${route.id}`, exact: true }).click(); await routes.getByLabel("Canonical literal path prefix", { exact: true }).fill("/app");
    const changed = page.waitForResponse((r) => r.request().method() === "PATCH" && new URL(r.url()).pathname === `/api/v1/routes/${route.id}`);
    await routes.getByRole("button", { name: "Save desired route", exact: true }).click(); expect((await changed).status()).toBe(202);
    await settleProxy(page, agent, server.server.id);
    expect((await trustedRoute(host, "/app")).trim()).toBe("A");
    if (width === 320) {
      await routes.getByRole("button", { name: `Edit route ${route.id}`, exact: true }).click();
      await routes.getByLabel("Canonical literal path prefix", { exact: true }).fill("/draft");
      expect((await page.request.patch(`/api/v1/routes/${route.id}`, { headers: headers(page), data: { expected_route_version: 2, idempotency_key: `concurrent-${suffix}`, hostname: host, path_prefix: "/other", expected_healthy_operation_id: op.id } })).status()).toBe(202);
      const conflict = page.waitForResponse((r) => r.request().method() === "PATCH" && new URL(r.url()).pathname === `/api/v1/routes/${route.id}`);
      await routes.getByRole("button", { name: "Save desired route", exact: true }).click(); expect((await conflict).status()).toBe(409);
      await expect(routes.getByLabel("Canonical literal path prefix", { exact: true })).toHaveValue("/draft");
      await expect(routes.getByRole("button", { name: "Retry identical route request", exact: true })).toBeDisabled();
      await routes.getByRole("button", { name: "Review refreshed routes for a separate request", exact: true }).click();
      await routes.getByRole("button", { name: `Edit route ${route.id}`, exact: true }).click();
      await routes.getByLabel("Canonical literal path prefix", { exact: true }).fill("/app");
      const reviewed = page.waitForResponse((r) => r.request().method() === "PATCH" && new URL(r.url()).pathname === `/api/v1/routes/${route.id}`);
      await routes.getByRole("button", { name: "Save desired route", exact: true }).click(); expect((await reviewed).status()).toBe(202);
      await settleProxy(page, agent, server.server.id);
    }
    await page.reload(); const reload = await hosting(page, projectID, environment); const persisted = reload.getByRole("region", { name: `Routes for ${name}`, exact: true });
    await expect(persisted.getByText(`${host}/app`, { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`proxy-routes-${width}.png`), fullPage: true, animations: "disabled" });
    if (width === 1440) {
      expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: { project_id: projectID, email: "oidc-browser@example.invalid", role: "viewer" } })).ok()).toBe(true);
      const context = await browser.newContext(); try {
        const viewer = await context.newPage(); await viewer.goto("/sign-in"); await viewer.getByRole("link", { name: "Continue with SSO" }).click(); await expect(viewer.getByRole("button", { name: "Account", exact: true })).toBeVisible();
        const view = await hosting(viewer, projectID, environment); const section = view.getByRole("region", { name: `Routes for ${name}`, exact: true }); await expect(section.getByRole("form")).toHaveCount(0);
        const ownStatus = await viewer.request.get(`/api/v1/proxy-operations/${route.operation_id}`); expect(ownStatus.status()).toBe(200); const status = await ownStatus.json(); expect(status).not.toHaveProperty("snapshot"); expect(status).not.toHaveProperty("plan");
        expect((await viewer.request.post(routePath, { headers: headers(viewer), data: response.request().postDataJSON() })).status()).toBe(403);
        expect((await viewer.request.patch(`/api/v1/routes/${route.id}`, { headers: headers(viewer), data: response.request().postDataJSON() })).status()).toBe(403);
        expect((await viewer.request.delete(`/api/v1/routes/${route.id}`, { headers: headers(viewer), data: { expected_route_version: 2, idempotency_key: "viewer-denied" } })).status()).toBe(403);
        expect((await viewer.request.get(`/api/v1/servers/${server.server.id}/proxy`)).status()).toBe(403);
        expect((await viewer.request.post(`/api/v1/servers/${server.server.id}/proxy/disable`, { headers: headers(viewer), data: { idempotency_key: "viewer-disabled-denied" } })).status()).toBe(403);
      } finally { await context.close(); }
    }
    await persisted.getByRole("button", { name: `Review withdrawal ${route.id}`, exact: true }).click(); await persisted.getByLabel("I confirm withdrawal of this exact route.", { exact: true }).check();
    const withdrawn = page.waitForResponse((r) => r.request().method() === "DELETE" && new URL(r.url()).pathname === `/api/v1/routes/${route.id}`);
    await persisted.getByRole("button", { name: "Request route withdrawal", exact: true }).click(); expect((await withdrawn).status()).toBe(202);
    await settleProxy(page, agent, server.server.id);
    await page.goto("/runners");
    const admin = page.getByRole("region", { name: "Hosting servers", exact: true });
    await admin.getByRole("article", { name: `Hosting server Proxy host ${suffix}`, exact: true }).getByRole("button", { name: "View server destination", exact: true }).click();
    const proxyPanel = admin.getByRole("region", { name: "Managed proxy", exact: true });
    await proxyPanel.getByRole("button", { name: `Review revoke ${host}`, exact: true }).click();
    await proxyPanel.getByLabel("I confirm this exact withdrawal.", { exact: true }).check();
    const revoked = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/revoke"));
    await proxyPanel.getByRole("button", { name: "Request proxy withdrawal", exact: true }).click(); expect((await revoked).status()).toBe(202);
    await settleProxy(page, agent, server.server.id);
    await proxyPanel.getByRole("button", { name: "Refresh proxy information", exact: true }).click();
    await proxyPanel.getByRole("button", { name: "Review proxy withdrawal", exact: true }).click();
    await proxyPanel.getByLabel("I confirm this exact withdrawal.", { exact: true }).check();
    const disabled = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/proxy/disable"));
    await proxyPanel.getByRole("button", { name: "Request proxy withdrawal", exact: true }).click(); expect((await disabled).status()).toBe(202);
    await settleProxy(page, agent, server.server.id);
    const container = process.env.NEROCD_PROXY_BROWSER_CONTAINER!;
    const captured = spawnSync("docker", ["exec", container, "docker", "inspect", "--format", '{{.Id}}|{{index .Config.Labels "io.nerocd.proxy"}}|{{index .Config.Labels "io.nerocd.server"}}', `nerocd-proxy-${proxyID}`], { encoding: "utf8", stdio: "pipe" });
    if (captured.status !== 0 || !captured.stdout.trim().endsWith(`|${proxyID}|${server.server.id}`)) throw new Error("owned proxy cleanup identity mismatch");
    const exactID = captured.stdout.trim().split("|")[0]!;
    expect(spawnSync("docker", ["exec", container, "docker", "rm", "-f", exactID], { stdio: "pipe" }).status).toBe(0);
    const imageID = settled!.result!.container_id;
    const imageIdentity = spawnSync("docker", ["exec", container, "docker", "inspect", "--format", '{{.Id}}|{{index .Config.Labels "io.nerocd.binding"}}|{{index .Config.Labels "io.nerocd.operation"}}', imageID], { encoding: "utf8", stdio: "pipe" });
    if (imageIdentity.status !== 0 || imageIdentity.stdout.trim() !== `${imageID}|${op.binding_id}|${op.id}`) throw new Error("owned image cleanup identity mismatch");
    expect(spawnSync("docker", ["exec", container, "docker", "rm", "-f", imageID], { stdio: "pipe" }).status).toBe(0);
  }
});


test("image-only real runner is refused proxy configuration with explicit prerequisite guidance", async ({ page }, info) => {
  test.setTimeout(180_000); fixtureDirectory();
  const image = process.env.NEROCD_IMAGE_TEST_A;
  if (!image) throw new Error("task-owned image unavailable");
  await signIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    const suffix = `${width}_${Date.now()}`;
    const projectID = await createProject(page, width, `Proxy UI ${suffix}`);
    const { agent, runnerID } = await enroll(page, suffix);
    const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
    await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
    await servers.getByLabel("Hosting server name", { exact: true }).fill(`Proxy host ${suffix}`);
    await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const createdServer = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/servers");
    await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
    const server = await (await createdServer).json() as { server: { id: string }; destination: { id: string } };
    await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID);
    await servers.getByRole("button", { name: "Grant project access", exact: true }).click();
    await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
    const region = await hosting(page, projectID);
    const environment = `Production ${suffix}`;
    await region.getByLabel("Environment name", { exact: true }).fill(environment); await region.getByRole("button", { name: "Create shared environment", exact: true }).click();
    const name = `Proxy web ${suffix}`;
    const create = region.getByRole("form", { name: "Create application", exact: true });
    await create.getByLabel("Application name", { exact: true }).fill(name); await create.getByLabel("Image reference", { exact: true }).fill(image);
    const created = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/resources");
    await create.getByRole("button", { name: "Save application", exact: true }).click();
    const resource = await (await created).json() as { id: string };
    const placement = region.getByRole("region", { name: `Desired destination for ${name}`, exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(server.destination.id); await placement.getByRole("button", { name: "Save desired destination", exact: true }).click();
    const deployments = region.getByRole("region", { name: `Image deployments for ${name}`, exact: true });
    await deployments.getByLabel("Container port", { exact: true }).fill("8080"); await deployments.getByLabel("Readiness timeout in seconds", { exact: true }).fill("10"); await deployments.getByLabel(/I understand that/).check();
    const deployed = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${resource.id}/deployments`);
    await deployments.getByRole("button", { name: "Request image deployment", exact: true }).click();
    const op = await (await deployed).json() as { id: string; binding_id: string; snapshot: { host_port: number } }; captureOperation(op);
    let settled: { state: string; result?: { container_id: string } } | undefined;
    for (let attempt = 0; attempt < 5; attempt++) { runAgent(agent, true); settled = await (await page.request.get(`/api/v1/hosting-operations/${op.id}`)).json(); if (settled?.state === "succeeded") break; }
    expect(settled?.state).toBe("succeeded"); expect((await (await page.request.get(`http://127.0.0.1:${op.snapshot.host_port}/`)).text()).trim()).toBe("A");

    await page.goto("/runners");
    const administration = page.getByRole("region", { name: "Hosting servers", exact: true });
    await administration.getByRole("article", { name: `Hosting server Proxy host ${suffix}`, exact: true }).getByRole("button", { name: "View server destination", exact: true }).click();
    const proxy = administration.getByRole("region", { name: "Managed proxy", exact: true });
    await expect(proxy.getByText(/Configuration admission requires the enrolled runner to advertise proxy_apply/)).toBeVisible();
    await proxy.getByLabel("Immutable control project ID", { exact: true }).fill(projectID);
    await proxy.getByLabel("ACME email", { exact: true }).fill("proxy-ui@example.test");
    const configured = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/servers/${server.server.id}/proxy`);
    await proxy.getByRole("button", { name: "Configure managed proxy", exact: true }).focus(); await page.keyboard.press("Enter");
    expect((await configured).status()).toBe(409);
    await expect(proxy.getByRole("alert")).toContainText("Conflict");
    await expect(proxy.getByLabel("ACME email", { exact: true })).toHaveValue("proxy-ui@example.test");
    await expect(proxy.getByRole("button", { name: "Retry identical proxy request", exact: true })).toBeDisabled();
    expect((await page.request.get(`/api/v1/servers/${server.server.id}/proxy`)).status()).toBe(404);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`proxy-prerequisite-${width}.png`), fullPage: true, animations: "disabled" });
    removeCompletedFixtureContainer(settled!.result!.container_id, op.binding_id, op.id);
  }
});
