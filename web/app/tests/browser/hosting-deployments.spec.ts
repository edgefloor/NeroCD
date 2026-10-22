import { expect, test, type Page } from "@playwright/test";
import { spawn, spawnSync } from "node:child_process";
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// No automatic screenshot or trace may contain one-time enrollment material.
test.use({ trace: "off", screenshot: "off" });
function fixtureDirectory() {
  const directory = process.env.NEROCD_HOSTING_UI_FIXTURE_DIR;
  if (!directory || !/^\/tmp\/nerocd-hosting-ui\.[A-Za-z0-9]+$/.test(directory)) throw new Error("run through hosting-deployment-fixture.sh");
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
type Agent = { directory: string; origin: string };
function agentArguments(agent: Agent, execute: boolean) {
  const endpoint = process.env.NEROCD_HOSTING_UI_DOCKER_ENDPOINT;
  if (!endpoint?.startsWith("unix:///")) throw new Error("explicit local Docker endpoint unavailable");
  const credential = join(agent.directory, "runner.credential");
  const enrollmentArgs = existsSync(credential) ? [] : ["--enrollment-file", join(agent.directory, "enrollment.token")];
  return ["runner", "--server", agent.origin, ...enrollmentArgs, "--credential-file", credential, "--work-dir", join(agent.directory, "work"), "--journal-dir", join(agent.directory, "journal"), "--compose-image-policy", "pull", "--image-docker-endpoint", endpoint, "--once", `--execute=${execute}`];
}
function runAgent(agent: Agent, execute: boolean) {
  const result = spawnSync(join(runtime(), "nerocd"), agentArguments(agent, execute), { env: { ...process.env, NEROCD_MODE: "development", NEROCD_TOKEN: "" }, stdio: "pipe", timeout: 45_000 });
  if (result.pid) appendFileSync(join(fixtureDirectory(), "runner-pids"), `${result.pid}\n`, { mode: 0o600 });
  if (result.error || result.status !== 0) throw new Error("task-owned image runner failed; output withheld to protect enrollment material");
}
async function enroll(page: Page, suffix: string): Promise<{ agent: Agent; runnerID: string }> {
  await page.goto("/runners");
  const runnerID = `runner_image_ui_${suffix}`;
  const form = page.getByRole("form", { name: "Create runner enrollment", exact: true });
  await form.getByLabel("Runner ID", { exact: true }).fill(runnerID); await form.getByLabel("Runner name", { exact: true }).fill(`Image host ${suffix}`);
  await form.getByLabel("Runner tags", { exact: true }).fill(`image-ui-${suffix}`);
  await form.getByRole("button", { name: "Use image hosting capability preset", exact: true }).click();
  await expect(form.getByLabel("Runner capabilities", { exact: true })).toHaveValue("compose-deploy,image_deploy,image_release_ports_v1");
  const response = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/runner-enrollments");
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const created = await response; expect(created.status()).toBe(201);
  const body = await created.json() as { token: string };
  const directory = mkdtempSync(join(fixtureDirectory(), "agent-")); chmodSync(directory, 0o700);
  mkdirSync(join(directory, "work"), { mode: 0o700 }); mkdirSync(join(directory, "journal"), { mode: 0o700 });
  writeFileSync(join(directory, "enrollment.token"), body.token + "\n", { mode: 0o600 });
  await expect(page.getByLabel("Enrollment secret", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape"); await expect(page.getByLabel("Enrollment secret", { exact: true })).toHaveCount(0);
  const agent = { directory, origin: new URL(page.url()).origin };
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

test("manual onboarding deploys a real image through River and the runner, preserving history and explicit cancellation", async ({ page, browser }, info) => {
  test.setTimeout(240_000);
  fixtureDirectory();
  const image = process.env.NEROCD_IMAGE_TEST_A;
  if (!image || !/^127\.0\.0\.1:\d+\/nerocd-gate-[a-f0-9]{12}@sha256:[a-f0-9]{64}$/.test(image)) throw new Error("accepted local image fixture unavailable");
  await signIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    const suffix = `${width}_${Date.now()}`;
    const projectID = await createProject(page, width, `Image UI ${suffix}`);
    const { agent, runnerID } = await enroll(page, suffix);
    const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
    await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
    await servers.getByLabel("Hosting server name", { exact: true }).fill(`Image UI host ${suffix}`);
    await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const serverResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/servers");
    await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
    const destination = (await (await serverResponse).json() as { destination: { id: string } }).destination;
    await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID);
    await servers.getByRole("button", { name: "Grant project access", exact: true }).click();
    await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
    const region = await hosting(page, projectID);
    const environmentName = `Production ${suffix}`;
    await region.getByLabel("Environment name", { exact: true }).fill(environmentName);
    await region.getByRole("button", { name: "Create shared environment", exact: true }).click();
    const applicationName = `Web ${suffix}`;
    const create = region.getByRole("form", { name: "Create application", exact: true });
    await create.getByLabel("Application name", { exact: true }).fill(applicationName); await create.getByLabel("Image reference", { exact: true }).fill(image);
    const resourceResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/resources");
    await create.getByRole("button", { name: "Save application", exact: true }).click();
    const resource = await (await resourceResponse).json() as { id: string };
    const deployments = region.getByRole("region", { name: `Image deployments for ${applicationName}`, exact: true });
    await expect(deployments.getByText("Save a desired destination before requesting deployment.", { exact: true })).toBeVisible();
    const placement = region.getByRole("region", { name: `Desired destination for ${applicationName}`, exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(destination.id);
    await placement.getByRole("button", { name: "Save desired destination", exact: true }).click();
    await expect(placement.getByText("Saved placement", { exact: true })).toBeVisible();
    await deployments.getByLabel("Container port", { exact: true }).fill("8080");
    await expect(deployments.getByLabel("Host port allocation", { exact: true })).toHaveValue("automatic");
    await deployments.getByLabel("Host port allocation", { exact: true }).selectOption("fixed");
    await expect(deployments.getByRole("note")).toContainText("stops the current container before replacement readiness is verified, interrupting service");
    await expect(deployments.getByLabel(/I understand that/)).not.toBeChecked();
    await deployments.getByLabel(/I understand that/).check();
    await deployments.getByLabel("Runner-local host port", { exact: true }).fill("9090");
    await expect(deployments.getByLabel(/I understand that/)).not.toBeChecked();
    await expect(deployments.getByRole("button", { name: "Request image deployment", exact: true })).toBeDisabled();
    await expect(deployments.getByLabel("Runner-local host port", { exact: true })).toBeVisible();
    await deployments.getByLabel("Host port allocation", { exact: true }).selectOption("automatic");
    await expect(deployments.getByLabel("Runner-local host port", { exact: true })).toHaveCount(0);
    await deployments.getByLabel("Readiness timeout in seconds", { exact: true }).fill("10");
    await expect(deployments.getByRole("button", { name: "Request image deployment", exact: true })).toBeDisabled();
    await deployments.getByLabel(/I understand that/).check();
    let firstOperation: { id: string; binding_id: string } | undefined;
    const requestPath = `/api/v1/resources/${resource.id}/deployments`;
    if (width === 320) {
      // The real server accepts the request; only its browser response is lost.
      await page.route(`**${requestPath}`, async (route) => {
        if (route.request().method() !== "POST") { await route.continue(); return; }
        const response = await route.fetch(); expect(response.status()).toBe(202);
        firstOperation = await response.json(); captureOperation(firstOperation!);
        await route.abort("failed");
      }, { times: 1 });
      await deployments.getByRole("button", { name: "Request image deployment", exact: true }).click();
      await expect(deployments.getByRole("button", { name: "Retry identical deployment request", exact: true })).toBeVisible();
      await deployments.getByRole("button", { name: "Retry identical deployment request", exact: true }).focus();
    } else await deployments.getByRole("button", { name: "Request image deployment", exact: true }).focus();
    const deploymentResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === requestPath);
    await page.keyboard.press("Enter");
    const accepted = await deploymentResponse; expect(accepted.status()).toBe(202);
    let operation = await accepted.json() as { id: string; binding_id: string; snapshot: { host_port: number } };
    let port = operation.snapshot.host_port;
    expect(accepted.request().postDataJSON()).not.toHaveProperty("host_port");
    captureOperation(operation); if (firstOperation) expect(operation.id).toBe(firstOperation.id);
    for (let attempt = 0; attempt < 5; attempt++) {
      runAgent(agent, true);
      const status = await page.request.get(`/api/v1/hosting-operations/${operation.id}`);
      const body = await status.json() as { state: string };
      if (body.state === "succeeded") break;
      await expect.poll(async () => (await (await page.request.get(`/api/v1/hosting-operations/${operation.id}`)).json() as { state: string }).state).not.toBe("queued");
    }
    const detail = deployments.getByRole("region", { name: "Deployment operation detail", exact: true });
    await detail.getByRole("button", { name: "Refresh operation", exact: true }).click();
    await expect(detail.getByText("Succeeded", { exact: true })).toBeVisible();
    await expect(detail.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible();
    const live = await page.request.get(`http://127.0.0.1:${port}/`); expect(live.status()).toBe(200); expect((await live.text()).trim()).toBe("A");
    await page.reload(); const reloaded = await hosting(page, projectID, environmentName);
    const persisted = reloaded.getByRole("region", { name: `Image deployments for ${applicationName}`, exact: true });
    await persisted.getByRole("button", { name: `View operation ${operation.id}`, exact: true }).click();
    await expect(persisted.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible();
    // A new saved image requests another allocated release port through the UI.
    // Both real containers serve until the exact older release is cleaned up.
    const firstRelease = operation;
    const oldPort = port;
    const imageB = process.env.NEROCD_IMAGE_TEST_B;
    if (!imageB?.includes("@sha256:")) throw new Error("fixture B digest unavailable");
    const application = reloaded.getByRole("article", { name: applicationName, exact: true });
    await application.getByRole("button", { name: "Edit configuration", exact: true }).click();
    const editor = application.getByRole("form", { name: `Edit ${applicationName}`, exact: true });
    await editor.getByLabel("Image reference", { exact: true }).fill(imageB);
    await editor.getByRole("button", { name: "Save configuration", exact: true }).click();
    await expect(editor).toHaveCount(0);
    await persisted.getByLabel("Container port", { exact: true }).fill("8080");
    await persisted.getByLabel(/I understand that/).check();
    const secondResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === requestPath);
    await persisted.getByRole("button", { name: "Request image deployment", exact: true }).click();
    const second = await secondResponse; expect(second.status()).toBe(202);
    operation = await second.json(); captureOperation(operation);
    port = operation.snapshot.host_port; expect(port).not.toBe(oldPort);
    for (let attempt = 0; attempt < 5; attempt++) {
      runAgent(agent, true);
      if ((await (await page.request.get(`/api/v1/hosting-operations/${operation.id}`)).json() as { state: string }).state === "succeeded") break;
      await expect.poll(async () => (await (await page.request.get(`/api/v1/hosting-operations/${operation.id}`)).json() as { state: string }).state).not.toBe("queued");
    }
    expect((await (await page.request.get(`http://127.0.0.1:${oldPort}/`)).text()).trim()).toBe("A");
    expect((await (await page.request.get(`http://127.0.0.1:${port}/`)).text()).trim()).toBe("B");
    const retained = persisted.getByRole("region", { name: "Retained releases", exact: true });
    await retained.getByRole("button", { name: "Refresh retained releases", exact: true }).click();
    const row = retained.getByRole("article", { name: `Retained release ${firstRelease.id}`, exact: true });
    await row.getByRole("button", { name: `Review cleanup ${firstRelease.id}`, exact: true }).click();
    const cleanupForm = retained.getByRole("form", { name: "Confirm retained release cleanup", exact: true });
    await expect(cleanupForm.getByText(new RegExp(firstRelease.id))).toBeVisible();
    await cleanupForm.getByLabel(/I confirm cleanup/).check();
    expect(await page.locator("main").evaluate((element) => element.scrollLeft)).toBe(0);
    await page.screenshot({ path: info.outputPath(`retained-cleanup-${width}.png`), fullPage: true, animations: "disabled" });
    const cleanupPath = `/api/v1/resources/${resource.id}/retained-releases/${firstRelease.id}/cleanup`;
    let lostCleanup: { id: string; binding_id: string } | undefined;
    let cleanupBody: string | null = null;
    if (width === 320) {
      await page.route(`**${cleanupPath}`, async (route) => {
        cleanupBody = route.request().postData();
        const response = await route.fetch(); expect(response.status()).toBe(202);
        lostCleanup = await response.json(); captureOperation(lostCleanup!); await route.abort("failed");
      }, { times: 1 });
      await cleanupForm.getByRole("button", { name: "Request retained release cleanup", exact: true }).click();
      await expect(cleanupForm.getByRole("button", { name: "Retry identical cleanup request", exact: true })).toBeEnabled();
      await retained.getByRole("button", { name: "Refresh retained releases", exact: true }).click();
    }
    const cleanupResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === cleanupPath);
    await cleanupForm.getByRole("button", { name: width === 320 ? "Retry identical cleanup request" : "Request retained release cleanup", exact: true }).click();
    const cleanupAccepted = await cleanupResponse; expect(cleanupAccepted.status()).toBe(202);
    const cleanupOperation = await cleanupAccepted.json() as { id: string; binding_id: string }; captureOperation(cleanupOperation);
    if (lostCleanup) { expect(cleanupOperation.id).toBe(lostCleanup.id); expect(cleanupAccepted.request().postData()).toBe(cleanupBody); }
    for (let attempt = 0; attempt < 5; attempt++) {
      runAgent(agent, true);
      if ((await (await page.request.get(`/api/v1/hosting-operations/${cleanupOperation.id}`)).json() as { state: string }).state === "succeeded") break;
      await expect.poll(async () => (await (await page.request.get(`/api/v1/hosting-operations/${cleanupOperation.id}`)).json() as { state: string }).state).not.toBe("queued");
    }
    await retained.getByRole("button", { name: "Refresh retained releases", exact: true }).click();
    await expect(row).toHaveCount(0);
    expect((await (await page.request.get(`http://127.0.0.1:${port}/`)).text()).trim()).toBe("B");
    await expect(page.request.get(`http://127.0.0.1:${oldPort}/`, { timeout: 1000 })).rejects.toThrow();
    // Saved configuration changes before a new request: generation conflict must
    // preserve the form, refetch, and require a separate explicit intent.
    expect((await page.request.patch(`/api/v1/resources/${resource.id}`, { headers: headers(page), data: { expected_generation: 2, name: applicationName + " updated" } })).status()).toBe(200);
    await persisted.getByLabel("Container port", { exact: true }).fill("8080");
    await persisted.getByLabel(/I understand that/).check();
    const conflictResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === requestPath);
    await persisted.getByRole("button", { name: "Request image deployment", exact: true }).click(); expect((await conflictResponse).status()).toBe(409);
    await expect(persisted.getByText(/Current saved configuration: generation 3/)).toBeVisible();
    await expect(persisted.getByLabel("Host port allocation", { exact: true })).toHaveValue("automatic");
    await persisted.getByLabel(/I reviewed history/).check(); await persisted.getByRole("button", { name: "Use reviewed configuration for a new request", exact: true }).click();
    await persisted.getByLabel(/I understand that/).check();
    const nextResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === requestPath);
    await persisted.getByRole("button", { name: "Request image deployment", exact: true }).click();
    const next = await (await nextResponse).json() as { id: string; binding_id: string }; captureOperation(next);
    const nextDetail = persisted.getByRole("region", { name: "Deployment operation detail", exact: true });
    await nextDetail.getByRole("button", { name: "Request cancellation", exact: true }).click();
    await expect(nextDetail.getByText("Canceled", { exact: true })).toBeVisible();
    expect((await (await page.request.get(`http://127.0.0.1:${port}/`)).text()).trim()).toBe("B");
    await persisted.getByRole("button", { name: "Refresh deployment history", exact: true }).click();
    if (width === 1440) {
      await persisted.getByRole("button", { name: `View operation ${operation.id}`, exact: true }).click();
      const observation = persisted.getByRole("region", { name: "Deployment operation detail", exact: true });
      await observation.getByRole("button", { name: "Request reconciliation", exact: true }).click();
      await expect(observation.getByRole("button", { name: "Reconciliation pending", exact: true })).toBeDisabled();
      runAgent(agent, true);
      await observation.getByRole("button", { name: "Refresh operation", exact: true }).click();
      await expect(observation.getByRole("button", { name: "Request reconciliation", exact: true })).toBeEnabled();
      await expect(observation.getByText("Observation run", { exact: true })).toBeVisible();
      // Authorization fixtures use the same real API; the normal setup above
      // remained entirely UI-driven, including enrollment and placement.
      const membership = { project_id: projectID, email: "oidc-browser@example.invalid", role: "viewer" };
      expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: membership })).ok()).toBe(true);
      const context = await browser.newContext();
      try {
        const viewer = await context.newPage();
        await viewer.goto("/sign-in"); await viewer.getByRole("link", { name: "Continue with SSO" }).click();
        await expect(viewer.getByRole("button", { name: "Account", exact: true })).toBeVisible();
        for (const viewerWidth of [320, 390, 430, 1440]) {
          await viewer.setViewportSize({ width: viewerWidth, height: 1000 });
          const read = await hosting(viewer, projectID, environmentName);
          const section = read.getByRole("region", { name: `Image deployments for ${applicationName} updated`, exact: true });
          await section.getByRole("button", { name: `View operation ${operation.id}`, exact: true }).click();
          await expect(section.getByText("Readiness recorded at completion: passed.", { exact: true })).toBeVisible();
          await expect(section.getByRole("form")).toHaveCount(0);
          await expect(section.getByRole("button", { name: "Request cancellation", exact: true })).toHaveCount(0);
          await expect(section.getByRole("button", { name: "Request reconciliation", exact: true })).toHaveCount(0);
          expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
          await viewer.screenshot({ path: info.outputPath(`image-deployment-viewer-${viewerWidth}.png`), fullPage: true, animations: "disabled" });
        }
        expect((await viewer.request.get(`/api/v1/hosting-operations/${operation.id}`)).status()).toBe(200);
        for (const [path, data] of [[requestPath, { expected_generation: 3, idempotency_key: `denied-${suffix}`, container_port: 8080, host_port: port, readiness: { path: "/", timeout_seconds: 10 } }], [`/api/v1/resources/${resource.id}/retained-releases/${firstRelease.id}/cleanup`, { expected_generation: 3, idempotency_key: `denied-cleanup-${suffix}` }], [`/api/v1/hosting-operations/${operation.id}/cancel`, {}], [`/api/v1/hosting-operations/${operation.id}/reconcile`, {}]] as const) expect((await viewer.request.post(path, { headers: headers(viewer), data })).status()).toBe(403);
        expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: { ...membership, role: "maintainer" } })).ok()).toBe(true);
        await viewer.reload();
        const maintain = await hosting(viewer, projectID, environmentName);
        const section = maintain.getByRole("region", { name: `Image deployments for ${applicationName} updated`, exact: true });
        await expect(section.getByRole("form")).toBeVisible();
        await section.getByRole("button", { name: `View operation ${operation.id}`, exact: true }).click();
        await expect(section.getByRole("button", { name: "Request reconciliation", exact: true })).toHaveCount(0);
        expect((await viewer.request.post(`/api/v1/hosting-operations/${operation.id}/reconcile`, { headers: headers(viewer), data: {} })).status()).toBe(403);
        expect((await viewer.request.post(`/api/v1/resources/${resource.id}/retained-releases/${operation.id}/cleanup`, { headers: headers(viewer), data: { expected_generation: 3, idempotency_key: `protect-current-${suffix}` } })).status()).toBe(409);
      } finally { await context.close(); }
      // Stop only this captured runner after apply admission while the owned
      // fixture container fails readiness. The persisted hold must remain
      // visible and cancellation must not be presented as a proven rollback.
      await persisted.getByLabel("Readiness path", { exact: true }).fill("/unready");
      await persisted.getByLabel("Readiness timeout in seconds", { exact: true }).fill("30");
      await persisted.getByLabel(/I understand that/).check();
      let held: { id: string; binding_id: string } | undefined;
      let heldRequestBody: string | null = null;
      await page.route(`**${requestPath}`, async (route) => {
        if (route.request().method() !== "POST") { await route.continue(); return; }
        heldRequestBody = route.request().postData();
        const accepted = await route.fetch(); expect(accepted.status()).toBe(202);
        held = await accepted.json(); captureOperation(held!);
        await route.abort("failed");
      }, { times: 1 });
      await persisted.getByRole("button", { name: "Request image deployment", exact: true }).click();
      await expect(persisted.getByRole("button", { name: "Retry identical deployment request", exact: true })).toBeVisible();
      if (!held) throw new Error("task-owned accepted operation was not captured");
      const statusPath = `/api/v1/hosting-operations/${held.id}`;
      await expect.poll(async () => (await (await page.request.get(statusPath)).json() as { state: string }).state).not.toBe("queued");
      const child = spawn(join(runtime(), "nerocd"), agentArguments(agent, true), { env: { ...process.env, NEROCD_MODE: "development", NEROCD_TOKEN: "" }, stdio: "ignore" });
      if (!child.pid) throw new Error("task-owned runner did not start");
      appendFileSync(join(fixtureDirectory(), "runner-pids"), `${child.pid}\n`, { mode: 0o600 });
      const closed = new Promise<void>((resolve, reject) => { child.once("close", () => resolve()); child.once("error", reject); });
      try {
        await expect.poll(async () => (await (await page.request.get(statusPath)).json() as { state: string }).state, { intervals: [50, 100, 200], timeout: 15_000 }).toBe("verifying");
        child.kill("SIGKILL"); await closed;
        await persisted.getByRole("button", { name: "Refresh deployment history", exact: true }).click();
        await expect(persisted.getByText(/New deployment requests are paused/)).toBeVisible();
        await expect(persisted.getByLabel("Host port allocation", { exact: true })).toBeDisabled();
        await expect(persisted.getByRole("button", { name: "Retry identical deployment request", exact: true })).toBeEnabled();
        const recoveryResponse = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === requestPath);
        await persisted.getByRole("button", { name: "Retry identical deployment request", exact: true }).click();
        const recovered = await recoveryResponse; expect(recovered.status()).toBe(202);
        expect(recovered.request().postData()).toBe(heldRequestBody);
        expect((await recovered.json() as { id: string }).id).toBe(held.id);
        const heldDetail = persisted.getByRole("region", { name: "Deployment operation detail", exact: true });
        await heldDetail.getByRole("button", { name: "Refresh operation", exact: true }).click();
        await heldDetail.getByRole("button", { name: "Request cancellation", exact: true }).click();
        await expect(heldDetail.getByText("Cancellation requested after admission", { exact: true })).toBeVisible();
        await expect(heldDetail.getByText(/Mutation hold retained/)).toBeVisible();
        await expect(heldDetail.getByText(/not proof of rollback/)).toBeVisible();
        await persisted.getByLabel(/I understand that/).check();
        await expect(persisted.getByRole("button", { name: "Deployment waiting for settlement", exact: true })).toBeDisabled();
        const recorded = await (await page.request.get(statusPath)).json() as { mutation_hold: boolean; restore_ownership_held: boolean };
        expect(recorded.mutation_hold).toBe(true); expect(recorded.restore_ownership_held).toBe(false);
        for (const heldWidth of [320, 390, 430, 1440]) {
          await page.setViewportSize({ width: heldWidth, height: 1000 });
          expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
          await page.screenshot({ path: info.outputPath(`image-deployment-held-${heldWidth}.png`), fullPage: true, animations: "disabled" });
        }
      } finally {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        await closed;
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`image-deployment-${width}.png`), fullPage: true, animations: "disabled" });
    // Independent destination records share this fixture's physical Docker
    // daemon. End the completed width's fixture workload only after all HTTP
    // preservation assertions, so the next width can reuse its allocated range.
    if (width !== 1440) {
      const settled = await (await page.request.get(`/api/v1/hosting-operations/${operation.id}`)).json() as { result: { container_id: string } };
      removeCompletedFixtureContainer(settled.result.container_id, operation.binding_id, operation.id);
    }
  }
});
