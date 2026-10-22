import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// Enrollment tokens are intentionally visible once. Never retain automatic
// screenshots or traces of this spec; explicit screenshots follow dialog close.
test.use({ trace: "off", screenshot: "off" });
function runtime() {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser runtime unavailable");
  return realpathSync(join("/tmp", `nerocd-browser-${runID}`));
}
async function adminSignIn(page: Page) {
  await page.goto("/sign-in");
  const [email, password] = readFileSync(join(runtime(), "credentials"), "utf8").trim().split("\n");
  if (!email || !password) throw new Error("browser credentials unavailable");
  await page.getByLabel("Email", { exact: true }).fill(email); await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
const headers = (page: Page) => ({ Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" });
async function consumeEnrollmentFromUI(page: Page, runnerID: string, shell = false) {
  const form = page.getByRole("form", { name: "Create runner enrollment", exact: true });
  await form.getByLabel("Runner ID", { exact: true }).fill(runnerID);
  await form.getByLabel("Runner name", { exact: true }).fill(`Host ${runnerID}`);
  await form.getByLabel("Runner tags", { exact: true }).fill(`isolated-${runnerID}`);
  await form.getByLabel("Runner capabilities", { exact: true }).fill(shell ? "shell" : "compose-deploy");
  const response = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/runner-enrollments");
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const created = await response; expect(created.status()).toBe(201);
  const body = await created.json() as { token: string; enrollment: { runner_id: string } };
  expect(body.enrollment.runner_id).toBe(runnerID);
  const directory = mkdtempSync(join(runtime(), "hosting-agent-")); chmodSync(directory, 0o700);
  const enrollment = join(directory, "enrollment.token"), credential = join(directory, "runner.credential");
  const work = join(directory, "work"), journal = join(directory, "journal");
  mkdirSync(work, { mode: 0o700 }); mkdirSync(journal, { mode: 0o700 });
  writeFileSync(enrollment, body.token + "\n", { mode: 0o600 });
  const dialog = page.getByRole("dialog", { name: "One-time runner enrollment secret", exact: true });
  await expect(dialog.getByLabel("Manual runner command", { exact: true })).toContainText("--execute");
  await expect(dialog.getByLabel("Manual runner command", { exact: true })).not.toContainText(body.token);
  await page.keyboard.press("Escape");
  await expect(page.getByLabel("Enrollment secret", { exact: true })).toHaveCount(0);
  try {
    execFileSync(join(runtime(), "nerocd"), ["runner", "--server", new URL(page.url()).origin, "--enrollment-file", enrollment, "--credential-file", credential, "--work-dir", work, "--journal-dir", journal, "--compose-image-policy", "pull", "--once", "--execute=false"], { env: { ...process.env, NEROCD_MODE: "development", NEROCD_TOKEN: "" }, stdio: "pipe", timeout: 20_000 });
  } catch { throw new Error("task-owned runner failed to consume the UI enrollment"); }
  expect(statSync(credential).mode & 0o777).toBe(0o600);
}
async function createProjectUI(page: Page, width: number, name: string): Promise<string> {
  await page.goto("/projects");
  if (width < 1280) await page.getByRole("button", { name: "Configure project", exact: true }).click();
  const scope = width < 1280 ? page.getByRole("dialog", { name: "Configure project", exact: true }) : page.locator("main");
  await scope.getByLabel("Project Name", { exact: true }).fill(name);
  const response = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/projects");
  await scope.getByRole("button", { name: "Create project", exact: true }).click();
  const created = await response; expect(created.status()).toBe(201);
  if (width < 1280) await page.keyboard.press("Escape");
  return (await created.json() as { id: string }).id;
}
async function openProjectHosting(page: Page, projectID: string, environmentName?: string) {
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(projectID);
  if (environmentName) await hosting.getByRole("button", { name: environmentName, exact: true }).click();
  return hosting;
}
async function fixturePost<T>(page: Page, path: string, data: unknown): Promise<T> {
  const response = await page.request.post(path, { data, headers: headers(page) });
  if (!response.ok()) throw new Error(`hosting role fixture request failed: ${response.status()}`);
  return await response.json() as T;
}

test("manual canonical enrollment registers a server grants a project and persists immutable application placement", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  await adminSignIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    const suffix = `${width}_${Date.now()}`;
    const projectID = await createProjectUI(page, width, `Hosting project ${suffix}`);
    await page.goto("/runners");
    const runnerID = `runner_hosting_${suffix}`;
    await consumeEnrollmentFromUI(page, runnerID);
    const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
    await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
    await expect(servers.getByRole("option", { name: new RegExp(runnerID) })).toBeAttached();
    await servers.getByLabel("Hosting server name", { exact: true }).fill(`Server ${suffix}`);
    await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const response = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/servers");
    await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
    const registered = await response; expect(registered.status()).toBe(201);
    const detail = await registered.json() as { server: { id: string }; destination: { id: string } };
    await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID);
    const granted = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/destinations/${detail.destination.id}/project-grants`);
    await servers.getByRole("button", { name: "Grant project access", exact: true }).click(); expect((await granted).ok()).toBe(true);
    await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
    // Duplicate registration is rejected without discarding the draft.
    await servers.getByLabel("Hosting server name", { exact: true }).fill(`Duplicate ${suffix}`);
    await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
    const duplicate = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/servers");
    await servers.getByRole("button", { name: "Register hosting server", exact: true }).click(); expect((await duplicate).status()).toBe(409);
    await expect(servers.getByLabel("Hosting server name", { exact: true })).toHaveValue(`Duplicate ${suffix}`);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`hosting-server-grants-${width}.png`), fullPage: true, animations: "disabled" });
    let hosting = await openProjectHosting(page, projectID);
    await expect(hosting.getByText(`Project ID for destination grants: ${projectID}`, { exact: true })).toBeVisible();
    const environmentName = `Production ${suffix}`;
    await hosting.getByLabel("Environment name", { exact: true }).fill(environmentName);
    await hosting.getByRole("button", { name: "Create shared environment", exact: true }).click();
    const form = hosting.getByRole("form", { name: "Create application", exact: true });
    await form.getByLabel("Application name", { exact: true }).fill(`Web ${suffix}`); await form.getByLabel("Image reference", { exact: true }).fill("registry.example.com/team/web:1");
    const appResponse = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/resources");
    await form.getByRole("button", { name: "Save application", exact: true }).click(); const app = await (await appResponse).json() as { id: string };
    const placement = hosting.getByRole("region", { name: `Desired destination for Web ${suffix}`, exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(detail.destination.id);
    const bindingResponse = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/resources/${app.id}/binding`);
    await placement.getByRole("button", { name: "Save desired destination", exact: true }).focus(); await page.keyboard.press("Enter");
    const saved = await bindingResponse; expect(saved.status()).toBe(201); const binding = await saved.json() as { runtime_namespace: string };
    await expect(placement.getByText(binding.runtime_namespace, { exact: true })).toBeVisible(); await expect(placement.getByRole("combobox")).toHaveCount(0);
    await page.reload(); hosting = await openProjectHosting(page, projectID, environmentName);
    await expect(hosting.getByText(binding.runtime_namespace, { exact: true })).toBeVisible();
    // A second saved application demonstrates removal of future choices.
    const second = hosting.getByRole("form", { name: "Create application", exact: true });
    await second.getByLabel("Application name", { exact: true }).fill(`Unbound ${suffix}`); await second.getByLabel("Image reference", { exact: true }).fill("team/unbound:1");
    await second.getByRole("button", { name: "Save application", exact: true }).click();
    await expect(hosting.getByRole("region", { name: `Desired destination for Unbound ${suffix}`, exact: true }).getByRole("option", { name: new RegExp(detail.server.id) })).toBeAttached();
    await page.goto("/runners");
    await page.getByRole("article", { name: `Hosting server Server ${suffix}`, exact: true }).getByRole("button", { name: "View server destination", exact: true }).click();
    const revoked = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/destinations/${detail.destination.id}/project-grants/revoke`);
    await page.getByRole("button", { name: `Revoke grant for ${projectID}`, exact: true }).click(); expect((await revoked).ok()).toBe(true);
    hosting = await openProjectHosting(page, projectID, environmentName);
    await expect(hosting.getByText(binding.runtime_namespace, { exact: true })).toBeVisible();
    const unbound = hosting.getByRole("region", { name: `Desired destination for Unbound ${suffix}`, exact: true });
    await expect(unbound.getByRole("option", { name: new RegExp(detail.server.id) })).toHaveCount(0);
    await expect(unbound.getByRole("button", { name: "Save desired destination", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`hosting-placement-${width}.png`), fullPage: true, animations: "disabled" });
  }
});

test("hosting controls enforce project and global roles and refetch a concurrent immutable binding", async ({ page, browser }, testInfo) => {
  test.setTimeout(90_000);
  await adminSignIn(page); await page.setViewportSize({ width: 1440, height: 1000 });
  const suffix = String(Date.now());
  const projectID = await createProjectUI(page, 1440, `Role hosting ${suffix}`);
  await page.goto("/runners");
  const runnerID = `runner_roles_${suffix}`;
  await consumeEnrollmentFromUI(page, runnerID);
  const servers = page.getByRole("region", { name: "Hosting servers", exact: true });
  await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
  await servers.getByLabel("Hosting server name", { exact: true }).fill(`Roles ${suffix}`);
  await servers.getByLabel("Enrolled Compose runner", { exact: true }).selectOption(runnerID);
  const registered = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/servers");
  await servers.getByRole("button", { name: "Register hosting server", exact: true }).click();
  const server = await (await registered).json() as { destination: { id: string } };
  await servers.getByLabel("Project ID to grant", { exact: true }).fill(projectID);
  await servers.getByRole("button", { name: "Grant project access", exact: true }).click();
  await expect(servers.getByText("Grant active", { exact: true })).toBeVisible();
  const shellID = `runner_shell_${suffix}`;
  await consumeEnrollmentFromUI(page, shellID, true);
  await servers.getByRole("button", { name: "Refresh eligible runners", exact: true }).click();
  await expect(servers.getByRole("option", { name: new RegExp(shellID) })).toHaveCount(0);
  const ineligible = await page.request.post("/api/v1/servers", { data: { name: "Rejected shell host", runner_id: shellID }, headers: headers(page) });
  expect(ineligible.status()).toBe(409);
  // Role-specific fixtures are separate from the normal UI onboarding test.
  const environment = await fixturePost<{ id: string }>(page, "/api/v1/project-environments", { project_id: projectID, name: "Role environment" });
  const resource = await fixturePost<{ id: string }>(page, "/api/v1/resources", { project_id: projectID, project_environment_id: environment.id, name: "Role application", kind: "application", source: { kind: "image", image: { reference: "team/role:1" } } });
  await fixturePost(page, "/api/v1/project-members", { project_id: projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in"); await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      const hosting = await openProjectHosting(operator, projectID, "Role environment");
      const placement = hosting.getByRole("region", { name: "Desired destination for Role application", exact: true });
      await expect(placement.getByLabel("Available destination", { exact: true })).toBeDisabled();
      await expect(placement.getByRole("button", { name: "Save desired destination", exact: true })).toHaveCount(0);
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await operator.screenshot({ path: testInfo.outputPath(`hosting-viewer-${width}.png`), fullPage: true, animations: "disabled" });
    }
    expect((await operator.request.get("/api/v1/servers")).status()).toBe(403);
    for (const [path, data] of [["/api/v1/servers", { name: "Denied", runner_id: runnerID }], [`/api/v1/destinations/${server.destination.id}/project-grants`, { project_id: projectID }], [`/api/v1/destinations/${server.destination.id}/project-grants/revoke`, { project_id: projectID }], [`/api/v1/resources/${resource.id}/binding`, { destination_id: server.destination.id }]] as const) expect((await operator.request.post(path, { data, headers: headers(operator) })).status()).toBe(403);
    await fixturePost(page, "/api/v1/project-members", { project_id: projectID, email: "oidc-browser@example.invalid", role: "maintainer" });
    await operator.reload();
    const hosting = await openProjectHosting(operator, projectID, "Role environment");
    const placement = hosting.getByRole("region", { name: "Desired destination for Role application", exact: true });
    await placement.getByLabel("Available destination", { exact: true }).selectOption(server.destination.id);
    let failBinding = true;
    await operator.route(`**/api/v1/resources/${resource.id}/binding`, (route) => route.request().method() === "GET" && failBinding ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "temporary fixture failure" }) }) : route.continue());
    await operator.getByRole("button", { name: "Account", exact: true }).click(); await operator.getByRole("button", { name: "Refresh data", exact: true }).click();
    await expect(placement.getByRole("button", { name: "Retry placement", exact: true })).toBeVisible();
    await expect(placement.getByRole("button", { name: "Save desired destination", exact: true })).toBeDisabled();
    await expect(placement.getByLabel("Available destination", { exact: true })).toHaveValue(server.destination.id);
    failBinding = false; await placement.getByRole("button", { name: "Retry placement", exact: true }).click();
    await expect(placement.getByRole("button", { name: "Save desired destination", exact: true })).toBeEnabled();
    const concurrent = await fixturePost<{ runtime_namespace: string }>(page, `/api/v1/resources/${resource.id}/binding`, { destination_id: server.destination.id });
    const conflict = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/api/v1/resources/${resource.id}/binding`);
    await placement.getByRole("button", { name: "Save desired destination", exact: true }).click(); expect((await conflict).status()).toBe(409);
    await expect(placement.getByText(concurrent.runtime_namespace, { exact: true })).toBeVisible();
    await expect(placement.getByText(/no placement was overwritten/)).toBeVisible();
    await operator.goto("/runners"); await expect(operator.getByRole("region", { name: "Hosting servers", exact: true })).toHaveCount(0);
  } finally { await context.close(); }
});

test("enrollment UI clears an expired token and copy/download controls", async ({ page }) => {
  await page.clock.install(); await adminSignIn(page); await page.goto("/runners");
  const form = page.getByRole("form", { name: "Create runner enrollment", exact: true });
  await form.getByLabel("Runner ID", { exact: true }).fill(`runner_expiry_${Date.now()}`); await form.getByLabel("Runner name", { exact: true }).fill("Expiry fixture");
  const response = page.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === "/api/v1/runner-enrollments");
  await form.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const body = await (await response).json() as { enrollment: { expires_at: string } };
  await expect(page.getByLabel("Enrollment secret", { exact: true })).toBeVisible();
  await page.clock.fastForward(Math.max(1, Date.parse(body.enrollment.expires_at) - Date.now() + 5000));
  await expect(page.getByLabel("Enrollment secret", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Download once", exact: true })).toHaveCount(0);
  await expect(page.getByText("Enrollment expired. Create a new enrollment before continuing.", { exact: true })).toBeVisible();
});
