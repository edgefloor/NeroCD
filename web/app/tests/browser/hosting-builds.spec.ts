import { expect, test, type Page } from "@playwright/test";
import { once } from "node:events";
import { createServer } from "node:net";
import { spawn, spawnSync } from "node:child_process";
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// No automatic screenshot or trace may contain one-time enrollment material.
test.use({ trace: "off", screenshot: "off" });
function fixtureDirectory() {
  const directory = process.env.NEROCD_HOSTING_UI_FIXTURE_DIR;
  if (!directory || !/^\/tmp\/nerocd-hosting-ui\.[A-Za-z0-9]+$/.test(directory)) throw new Error("run through hosting-builds-fixture.sh");
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
  const expectedRefusal = "authenticated hosting_build_v1 capability does not match explicit local build configuration";
  if (result.error || result.status === 0 || !Buffer.concat([result.stdout ?? Buffer.alloc(0), result.stderr ?? Buffer.alloc(0)]).toString().includes(expectedRefusal) || !existsSync(join(agent.directory, "runner.credential"))) throw new Error("admission fixture expected only the real build-runtime preflight refusal after enrollment; output withheld");
}
async function enroll(page: Page, suffix: string): Promise<{ agent: Agent; runnerID: string }> {
  await page.goto("/runners");
  const runnerID = `runner_image_ui_${suffix}`;
  const form = page.getByRole("form", { name: "Create runner enrollment", exact: true });
  await form.getByLabel("Runner ID", { exact: true }).fill(runnerID); await form.getByLabel("Runner name", { exact: true }).fill(`Image host ${suffix}`);
  await form.getByLabel("Runner tags", { exact: true }).fill(`image-ui-${suffix}`);
  await form.getByRole("button", { name: "Use image hosting capability preset", exact: true }).click();
  await expect(form.getByLabel("Runner capabilities", { exact: true })).toHaveValue("compose-deploy,image_deploy,image_release_ports_v1");
  await form.getByLabel("Runner capabilities", { exact: true }).fill("compose-deploy,image_deploy,image_release_ports_v1,hosting_build_v1");
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
  const identity = await fetch(`${agent.origin}/api/v1/runners/identity`, { headers: { Authorization: `Bearer ${readFileSync(join(directory, "runner.credential"), "utf8").trim()}` } });
  expect(identity.status).toBe(200);
  const persisted = await identity.json() as { runner_id: string; capabilities: string[] };
  expect(persisted.runner_id).toBe(runnerID); expect(persisted.capabilities).toContain("hosting_build_v1");
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
async function hosting(page: Page, projectID: string, environment?: string, origin?: string) {
  await page.goto(origin ? `${origin}/projects` : "/projects");
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

async function startDisabledAPI(origin: string) {
  const listener = createServer(); await new Promise<void>((resolve) => listener.listen(0, "127.0.0.1", resolve));
  const port = (listener.address() as { port: number }).port; await new Promise<void>((resolve) => listener.close(() => resolve()));
  const disabledOrigin = `http://127.0.0.1:${port}`;
  const child = spawn(join(runtime(), "nerocd"), ["server", "--addr", `127.0.0.1:${port}`], { env: { ...process.env, NEROCD_MODE: "development", NEROCD_DATABASE_URL: readFileSync(join(runtime(), "database-url"), "utf8"), NEROCD_HOSTING_BUILD_POLICY_FILE: "", NEROCD_PUBLIC_ORIGIN: disabledOrigin, NEROCD_COOKIE_SECURE: "false", NEROCD_TOKEN: "", NEROCD_SECRET_KEYRING_FILE: join(runtime(), "secret-keyring.json") }, stdio: "ignore" });
  if (!child.pid) throw new Error("disabled-policy API failed to start");
  appendFileSync(join(fixtureDirectory(), "disabled-api-pids"), `${child.pid}\n`, { mode: 0o600 });
  await expect.poll(async () => { try { return (await fetch(`${disabledOrigin}/api/v1/health`)).status; } catch { return 0; } }).toBe(200);
  return { origin: disabledOrigin, stop: async () => { if (child.exitCode !== null) return; child.kill("SIGTERM"); const timer = setTimeout(() => { if (child.exitCode === null) child.kill("SIGKILL"); }, 5000); await once(child, "exit"); clearTimeout(timer); } };
}
test("public Git build admission, retained replay and cancellation use the real API without build execution", async ({ page, browser }, info) => {
  test.setTimeout(180_000); page.setDefaultTimeout(15_000); fixtureDirectory(); await signIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); const suffix = `${width}_${Date.now()}`;
    const projectID = await createProject(page, width, `Build UI ${suffix}`); const { runnerID } = await enroll(page, suffix);
    // Real enrollment is consumed before work exists. Exact local build-runtime
    // preflight refusal is expected and dedicated identity is verified. No agent
    // is run after admission; no artifact or successful receipt is seeded.
    const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
    await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click(); await servers.getByLabel("Hosting server name", { exact: true }).fill(`Build host ${suffix}`); await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const createdServer = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/servers"); await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
    const destination = (await (await createdServer).json() as { destination: { id: string } }).destination;
    await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID); await servers.getByRole("button", { name: "Grant project access", exact: true }).click(); await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
    let region = await hosting(page, projectID); const environment = `Builds ${suffix}`;
    await region.getByLabel("Environment name", { exact: true }).fill(environment); await region.getByRole("button", { name: "Create shared environment", exact: true }).click();
    const name = `Public Git ${suffix}`; const form = region.getByRole("form", { name: "Create application", exact: true });
    await form.getByLabel("Application source", { exact: true }).selectOption("git"); await form.getByLabel("Application name", { exact: true }).fill(name); await form.getByLabel("Git repository URL", { exact: true }).fill("https://github.com/example/web.git"); await form.getByLabel("Git branch, tag, or commit", { exact: true }).fill("main");
    const createdResource = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/v1/resources"); await form.getByRole("button", { name: "Save application", exact: true }).click(); const resource = await (await createdResource).json() as { id: string; generation: number };
    const article = region.getByRole("article", { name, exact: true }); const builds = article.getByRole("region", { name: `Git builds for ${name}`, exact: true });
    await expect(article.getByRole("button", { name: "Request image deployment", exact: true })).toHaveCount(0); await expect(builds.getByText("Save a desired destination before requesting a build.", { exact: true })).toBeVisible();
    const placement = article.getByRole("region", { name: `Desired destination for ${name}`, exact: true }); await placement.getByLabel("Available destination", { exact: true }).selectOption(destination.id); await placement.getByRole("button", { name: "Save desired destination", exact: true }).click();
    const path = `/api/v1/resources/${resource.id}/builds`;
    await builds.getByLabel(/I reviewed this exact public Git source/).check();
    if (width === 390) {
      expect((await page.request.patch(`/api/v1/resources/${resource.id}`, { headers: headers(page), data: { expected_generation: 1, source: { kind: "git", git: { repository_url: "https://github.com/example/web.git", requested_ref: "next", credential_kind: "public", settings: { strategy: "dockerfile" } } } } })).status()).toBe(200);
      const conflict = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === path); await builds.getByRole("button", { name: "Request public Git build", exact: true }).click(); expect((await conflict).status()).toBe(409);
      await expect(builds.getByText(/Current saved configuration: resource generation 2/)).toBeVisible(); await expect(builds.getByRole("button", { name: "Retry identical build request", exact: true })).toBeDisabled();
      await builds.getByRole("button", { name: "Use reviewed source for a separate build", exact: true }).click(); await expect(builds.getByLabel(/I reviewed this exact public Git source/)).not.toBeChecked(); await builds.getByLabel(/I reviewed this exact public Git source/).check();
    }
    let original: { id: string; revision: number } | undefined; let originalBody: unknown;
    if (width === 320) {
      await page.route(`**${path}`, async (route) => { if (route.request().method() !== "POST") return route.continue(); const response = await route.fetch(); expect(response.status()).toBe(202); original = (await response.json() as { build: { id: string; revision: number } }).build; originalBody = route.request().postDataJSON(); await route.abort("failed"); }, { times: 1 });
      await builds.getByRole("button", { name: "Request public Git build", exact: true }).click(); await expect(builds.getByRole("button", { name: "Retry identical build request", exact: true })).toBeVisible();
    }
    await expect(builds.getByRole("button", { name: width === 320 ? "Retry identical build request" : "Request public Git build", exact: true })).toBeEnabled();
    const accepted = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === path); await builds.getByRole("button", { name: width === 320 ? "Retry identical build request" : "Request public Git build", exact: true }).focus(); await page.keyboard.press("Enter");
    const response = await accepted; expect(response.status()).toBe(202); const build = (await response.json() as { build: { id: string; revision: number; state: string } }).build; expect(build.state).toBe("queued");
    expect(Object.keys(response.request().postDataJSON()).sort()).toEqual(["expected_resource_generation", "expected_source_generation", "idempotency_key"]); if (original) { expect(build.id).toBe(original.id); expect(response.request().postDataJSON()).toEqual(originalBody); }
    const detail = builds.getByRole("region", { name: "Build operation detail", exact: true }); await expect(detail.getByText("Recorded build state", { exact: true })).toBeVisible(); await expect(detail.getByText("Queued", { exact: true })).toBeVisible(); await expect(detail.getByText("Daemon image ID", { exact: true })).toHaveCount(0);
    if (width === 1440) {
      expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: { project_id: projectID, email: "oidc-browser@example.invalid", role: "viewer" } })).ok()).toBe(true);
      const context = await browser.newContext(); try { const viewer = await context.newPage(); await viewer.goto("/sign-in"); await viewer.getByRole("link", { name: "Continue with SSO" }).click(); await expect(viewer.getByRole("button", { name: "Account", exact: true })).toBeVisible(); const read = await hosting(viewer, projectID, environment); const section = read.getByRole("region", { name: `Git builds for ${name}`, exact: true }); await section.getByRole("button", { name: `View build ${build.id}`, exact: true }).click(); await expect(section.getByLabel(/I confirm cancellation/)).toHaveCount(0); await expect(section.getByRole("button", { name: "Request public Git build", exact: true })).toHaveCount(0); expect((await viewer.request.post(path, { headers: headers(viewer), data: response.request().postDataJSON() })).status()).toBe(403); expect((await viewer.request.post(`/api/v1/hosting-builds/${build.id}/cancel`, { headers: headers(viewer), data: { expected_revision: build.revision } })).status()).toBe(403); const publicDetail = await (await viewer.request.get(`/api/v1/hosting-builds/${build.id}`)).json(); expect(JSON.stringify(publicDetail)).not.toMatch(/toolchain_identity|allowed_cidrs|policy_hash|credential_value/); } finally { await context.close(); }
    }
    await expect.poll(async () => (await (await page.request.get(`/api/v1/hosting-builds/${build.id}`)).json() as { build: { run_id?: string } }).build.run_id).toBeTruthy();
    await detail.getByRole("button", { name: "Refresh build detail", exact: true }).click();
    await detail.getByLabel(/I confirm cancellation/).check();
    if (width === 320) { await page.route(`**/api/v1/hosting-builds/${build.id}/cancel`, async (route) => { const result = await route.fetch(); expect(result.status()).toBe(200); await route.abort("failed"); }, { times: 1 }); }
    const cancelPath = `/api/v1/hosting-builds/${build.id}/cancel`;
    const canceled = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === cancelPath).catch(() => undefined);
    await detail.getByRole("button", { name: "Request build cancellation", exact: true }).click();
    if (width === 320) { await expect(detail.getByRole("button", { name: "Retry cancellation at retained revision", exact: true })).toBeVisible(); const conflict = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === cancelPath); await detail.getByRole("button", { name: "Retry cancellation at retained revision", exact: true }).click(); expect((await conflict).status()).toBe(409); await expect(detail.getByText(/Cancellation revision changed/)).toBeVisible(); } else expect((await canceled)!.status()).toBe(200);
    await detail.getByRole("button", { name: "Refresh build detail", exact: true }).click(); await expect(detail.getByText("Canceled", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`build-admission-${width}.png`), fullPage: true, animations: "disabled" });
    await page.reload(); region = await hosting(page, projectID, environment); await region.getByRole("button", { name: `View build ${build.id}`, exact: true }).click(); await expect(region.getByRole("region", { name: "Build operation detail", exact: true }).getByText("Canceled", { exact: true })).toBeVisible();
    if (width === 430) {
      const disabled = await startDisabledAPI(new URL(page.url()).origin); try { await page.goto(`${disabled.origin}/projects`); const unavailable = await hosting(page, projectID, environment, disabled.origin); const section = unavailable.getByRole("region", { name: `Git builds for ${name}`, exact: true }); await section.getByLabel(/I reviewed this exact public Git source/).check(); const denied = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === path); await section.getByRole("button", { name: "Request public Git build", exact: true }).click(); expect((await denied).status()).toBe(503); await expect(section.getByRole("alert")).toContainText("disabled by the server operator"); } finally { await disabled.stop(); }
      // Return to the primary, policy-enabled fixture without reusing its DB
      // outside this task or leaving the disabled server process behind.
      await page.goto(response.url().replace(path, "/projects"));
    }
  }
});
