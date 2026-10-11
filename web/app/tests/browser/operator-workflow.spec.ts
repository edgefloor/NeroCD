import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";

async function signIn(page: Page): Promise<void> {
  await ensureBootstrapped(page);
  const port = new URL(page.url()).port;
  if (!port) throw new Error("browser smoke credentials are unavailable");
  const bootstrapCredentialFile = join(browserRuntimeDir(), "credentials");
  const [bootstrapEmail, bootstrapPassword] = readFileSync(bootstrapCredentialFile, "utf8").trim().split("\n");
  if (!bootstrapEmail || !bootstrapPassword) throw new Error("browser smoke credentials are unavailable");
  await page.getByLabel("Email").fill(bootstrapEmail);
  await page.getByLabel("Password").fill(bootstrapPassword);
  await page.getByRole("button", { name: "Sign in" }).click();
}

function bootstrapAdmin(port: string): void {
  const runtimeDir = browserRuntimeDir();
  const [email] = readFileSync(join(runtimeDir, "credentials"), "utf8").trim().split("\n");
  const databaseURL = readFileSync(join(runtimeDir, "database-url"), "utf8");
  if (!email || !databaseURL) throw new Error("browser bootstrap fixture is unavailable");
  execFileSync(join(runtimeDir, "nerocd"), ["bootstrap-admin", "--email", email, "--name", "Browser Administrator", "--password-file", join(runtimeDir, "password")], {
    env: { ...process.env, NEROCD_DATABASE_URL: databaseURL },
    stdio: "pipe",
  });
}

function browserRuntimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

async function ensureBootstrapped(page: Page): Promise<void> {
  const status = await page.request.get("/api/v1/bootstrap-status");
  if (!status.ok()) throw new Error(`browser bootstrap status failed with ${status.status()}`);
  const body = await status.json() as { status?: string };
  if (body.status === "complete") return;
  if (body.status !== "required") throw new Error("browser bootstrap status is invalid");
  const port = new URL(page.url()).port;
  if (!port) throw new Error("browser smoke port is unavailable");
  bootstrapAdmin(port);
  await page.reload({ waitUntil: "networkidle" });
  await expect(page.getByLabel("Email")).toBeVisible();
}

async function postJSON<T>(page: Page, path: string, data: unknown, headers: Record<string, string> = {}): Promise<T> {
  const response = await page.request.post(path, { data, headers: { Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1", ...headers } });
  if (!response.ok()) throw new Error(`browser fixture request ${path} failed with ${response.status()}: ${(await response.text()).slice(0, 256)}`);
  return await response.json() as T;
}

async function createDetailFixture(page: Page): Promise<{ deploymentID: string; projectID: string; serviceID: string; environmentID: string; revisionID: string }> {
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const runner = await postJSON<{ token: string }>(page, "/api/v1/runner-enrollments", {
    runner_id: `runner_web${suffix}`,
    runner_name: "browser-detail-fixture",
    capabilities: ["compose-deploy"],
    ttl_seconds: 600,
  });
  await postJSON(page, "/api/v1/runner-enrollments/consume", {
    request_id: `enroll_consume_${"a".repeat(32)}`,
    credential_hash: "b".repeat(64),
  }, { Authorization: `Bearer ${runner.token}` });
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Browser detail ${suffix}`, description: "public detail fixture" });
  const repository = await postJSON<{ id: string }>(page, "/api/v1/repositories", { project_id: project.id, name: `repository-${suffix}`, url: "https://example.invalid/browser-detail.git" });
  const service = await postJSON<{ id: string }>(page, "/api/v1/services", { project_id: project.id, name: `service-${suffix}`, repository_id: repository.id, compose_path: "compose.yaml", profiles: [] });
  const environment = await postJSON<{ id: string }>(page, "/api/v1/environments", { service_id: service.id, name: `environment-${suffix}`, compose_project: `browser-detail-${suffix}`, timeout_seconds: 60, rollback_safe: true });
  const revision = await postJSON<{ id: string }>(page, "/api/v1/revisions", { service_id: service.id, requested_ref: "main" });
  const deployment = await postJSON<{ id: string }>(page, "/api/v1/deployments", { environment_id: environment.id, desired_revision_id: revision.id, idempotency_key: `browser-detail-${suffix}` });
  return { deploymentID: deployment.id, projectID: project.id, serviceID: service.id, environmentID: environment.id, revisionID: revision.id };
}

async function createRunLogFixture(page: Page, requiresAck = false): Promise<{ runID: string; secondRunID: string; projectID: string; templateID: string }> {
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Browser logs ${suffix}`, description: "run-log fixture" });
  const template = await postJSON<{ id: string }>(page, "/api/v1/templates", {
    project_id: project.id,
    name: `browser-logs-${suffix}`,
    kind: "shell",
    run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "browser-log-fixture"] } },
    workflow: { steps: [] },
    runner_tags: ["shell"],
    requires_ack: requiresAck,
  });
  const first = await postJSON<{ id: string }>(page, "/api/v1/runs", { template_id: template.id });
  const second = await postJSON<{ id: string }>(page, "/api/v1/runs", { template_id: template.id });
  return { runID: first.id, secondRunID: second.id, projectID: project.id, templateID: template.id };
}

test("browser observes CLI-only bootstrap guidance before a supported CLI bootstrap", async ({ page }) => {
  await page.goto("/");
  const status = await page.request.get("/api/v1/bootstrap-status");
  const body = await status.json() as { status?: string };
  if (body.status === "required") {
    await expect(page.getByRole("status", { name: "Administrator bootstrap required" })).toBeVisible();
    await expect(page.getByText("Bootstrap is intentionally CLI-only.")).toBeVisible();
    await expect(page.getByLabel("Email")).toHaveCount(0);
    await expect(page.getByLabel("Password")).toHaveCount(0);
  }
  await ensureBootstrapped(page);
  await expect(page.getByLabel("Email")).toHaveValue("");
  await expect(page.getByLabel("Password")).toHaveValue("");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  await expect(page.locator("h1")).toBeVisible();
});

test("authenticated navigation and sign out work with runtime-only credentials", async ({ page }) => {
  await page.goto("/projects");
  await expect(page).toHaveURL(/\/sign-in\?redirect=/);
  await signIn(page);
  await expect(page).toHaveURL(/\/projects/);
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.locator("h1")).toBeVisible();
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});

test("dashboard route states retain the shell and retry a transient loader failure", async ({ page }) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("navigation", { name: "Desktop navigation" })).toBeVisible();
  const dashboardRequests = /\/api\/v1\/(health|projects|templates|runs|approvals|run-logs)(?:\?|$)/;
  await page.route(dashboardRequests, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1_600));
    try { await route.continue(); } catch { /* navigation cancelled this delayed request */ }
  });
  await page.goto("/", { waitUntil: "domcontentloaded" });
  await expect(page.getByRole("navigation", { name: "Desktop navigation" })).toBeVisible();
  await expect(page.getByLabel("Loading dashboard")).toBeVisible({ timeout: 3_500 });
  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();
  await page.unroute(dashboardRequests);

  await page.getByRole("link", { name: "Projects" }).click();
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await page.getByRole("link", { name: "Home" }).click();
  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();

  let failHealth = true;
  await page.route("**/api/v1/health", async (route) => {
    if (failHealth) await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "temporary dashboard failure" }) });
    else await route.continue();
  });
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.getByRole("alert")).toContainText("We could not load the dashboard", { timeout: 16_000 });
  await expect(page.getByRole("navigation", { name: "Desktop navigation" })).toBeVisible();
  failHealth = false;
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();
  failHealth = true;
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Refresh data", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("We could not load the dashboard", { timeout: 16_000 });
  failHealth = false;
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();
  await page.unroute("**/api/v1/health");
});

test("settings cards contain controls and wrap long membership text on narrow screens", async ({ page }) => {
  await page.goto("/settings");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const projectName = `Settings geometry ${"project-name-".repeat(20)}${suffix}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: projectName, description: "settings layout fixture" });
  await page.reload({ waitUntil: "networkidle" });
  await page.getByRole("combobox").first().selectOption(project.id);
  const response = page.waitForResponse((candidate) => new URL(candidate.url()).pathname === "/api/v1/project-members" && candidate.request().method() === "POST");
  await page.getByRole("button", { name: "Update access" }).click();
  expect((await response).status()).toBe(200);
  const accessCard = page.getByText("Project access", { exact: true }).locator('xpath=ancestor::*[@data-slot="card"]');
  await expect(accessCard.locator("span").filter({ hasText: projectName })).toBeVisible();

  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    const geometry = await accessCard.evaluate((card, name) => {
      const cardBounds = card.getBoundingClientRect();
      const controls = Array.from(card.querySelectorAll<HTMLElement>("select, input, button"), (control) => {
        const bounds = control.getBoundingClientRect();
        return { left: bounds.left, right: bounds.right };
      });
      const member = Array.from(card.querySelectorAll<HTMLElement>("span"), (element) => element.textContent?.includes(String(name)) ? element : undefined).find(Boolean);
      if (!member) throw new Error("long membership text is unavailable");
      return {
        cardLeft: cardBounds.left,
        cardRight: cardBounds.right,
        controls,
        memberClientWidth: member.clientWidth,
        memberScrollWidth: member.scrollWidth,
        documentClientWidth: document.documentElement.clientWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
      };
    }, projectName);
    expect(geometry.documentScrollWidth).toBeLessThanOrEqual(geometry.documentClientWidth);
    expect(geometry.controls.every((control) => control.left >= geometry.cardLeft && control.right <= geometry.cardRight)).toBe(true);
    expect(geometry.memberScrollWidth).toBeLessThanOrEqual(geometry.memberClientWidth);
  }
});

test("dashboard reports unfinished work truthfully and shared cards keep bounded geometry", async ({ page }) => {
  await page.goto("/");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", {
    name: `Dashboard geometry ${suffix}`,
    description: "dashboard layout fixture",
  });
  const template = await postJSON<{ id: string }>(page, "/api/v1/templates", {
    project_id: project.id,
    name: `dashboard-geometry-${suffix}`,
    kind: "shell",
    run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "dashboard-layout"] } },
    workflow: { steps: [] },
    runner_tags: ["shell"],
    requires_ack: false,
  });
  await postJSON(page, "/api/v1/runs", { template_id: template.id });
  await page.reload({ waitUntil: "networkidle" });

  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();
  await expect(page.getByText(/\d+ unfinished run(?:s)?/)).toBeVisible();
  await expect(page.getByText("No approvals are waiting.")).toBeVisible();
  await expect(page.getByText("All Systems Green")).toHaveCount(0);
  await expect(page.getByText("currently executing")).toHaveCount(0);

  const activityCard = page.getByText("Recent Activity", { exact: true }).locator('xpath=ancestor::*[@data-slot="card"]');
  const geometry = await activityCard.evaluate((card) => {
    const header = card.querySelector<HTMLElement>('[data-slot="card-header"]');
    const body = card.querySelector<HTMLElement>('[data-slot="card-content"]');
    if (!header || !body) throw new Error("dashboard card slots are unavailable");
    return {
      bodyTop: body.getBoundingClientRect().top,
      headerBottom: header.getBoundingClientRect().bottom,
      headerPaddingTop: Number.parseFloat(getComputedStyle(header).paddingTop),
    };
  });
  expect(geometry.bodyTop).toBe(geometry.headerBottom);
  expect(geometry.headerPaddingTop).toBe(16);

  await page.goto("/projects", { waitUntil: "networkidle" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  const projectCards = page.locator('[data-slot="card"]:has([data-slot="card-content"] h3)');
  const configureCard = page.getByText("Configure project", { exact: true }).locator('xpath=ancestor::*[@data-slot="card"]');
  expect(await projectCards.first().evaluate((card) => card.getBoundingClientRect().height)).toBeLessThan(
    await configureCard.evaluate((card) => card.getBoundingClientRect().height),
  );

  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  }
});

test("project paging reaches rows beyond one hundred on a narrow viewport", async ({ page }, testInfo) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const projects = Array.from({ length: 151 }, (_, index) => ({
    id: `proj_page_${index + 1}`,
    name: `Paged project ${index + 1}`,
    description: "browser paging fixture",
    created_at: "2026-01-01T00:00:00Z",
  }));
  await page.route("**/api/v1/projects?*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("limit") !== "50") return route.continue();
    const offset = Number(url.searchParams.get("offset") ?? "0");
    const items = projects.slice(offset, offset + 50);
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ items, count: items.length, total: projects.length, limit: 50, offset }),
    });
  });
  await page.reload({ waitUntil: "networkidle" });
  await page.setViewportSize({ width: 320, height: 844 });
  await expect(page.getByText("Showing 1-50 of 151")).toBeVisible();
  await page.getByRole("button", { name: "Next projects" }).click();
  await expect(page.getByText("Showing 51-100 of 151")).toBeVisible();
  await page.getByRole("button", { name: "Next projects" }).click();
  await expect(page.getByText("Showing 101-150 of 151")).toBeVisible();
  await expect(page.getByText("Paged project 101", { exact: true })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("offset")).toBe("100");
  await page.reload();
  await expect(page.getByText("Showing 101-150 of 151")).toBeVisible();
  await page.goBack();
  await expect(page.getByText("Showing 51-100 of 151")).toBeVisible();
  await page.goForward();
  await expect(page.getByText("Showing 101-150 of 151")).toBeVisible();
  await page.goto("/runs");
  await page.goBack();
  await expect(page.getByText("Showing 101-150 of 151")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("projects-page-3-320.png"), fullPage: true });
});

test("settings pages member history while authorization lookup spans every bounded page", async ({ page }, testInfo) => {
  await page.goto("/settings");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Member paging ${Date.now()}`, description: "member paging fixture" });
  const members = Array.from({ length: 131 }, (_, index) => ({
    id: `member_page_${index + 1}`,
    project_id: project.id,
    user_id: `usr_page_${index + 1}`,
    email: `member-${index + 1}@example.test`,
    role: "viewer",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  }));
  await page.route("**/api/v1/project-members?*", async (route) => {
    const url = new URL(route.request().url());
    const limit = Number(url.searchParams.get("limit") ?? "100");
    const offset = Number(url.searchParams.get("offset") ?? "0");
    const items = members.slice(offset, offset + limit);
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ items, count: items.length, total: members.length, limit, offset }) });
  });
  await page.reload({ waitUntil: "networkidle" });
  await page.setViewportSize({ width: 320, height: 844 });
  await expect(page.getByText("Showing 1-50 of 131")).toBeVisible();
  await page.getByRole("button", { name: "Next project members" }).click();
  await expect(page.getByText("Showing 51-100 of 131")).toBeVisible();
  await expect(page.getByText(/member-51@example\.test/)).toBeVisible();
  const pager = page.getByRole("navigation", { name: "project members pages" });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await pager.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => ({ document: document.documentElement.scrollWidth, viewport: window.innerWidth }))).toEqual({ document: width, viewport: width });
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("settings-members-pager-320.png") });
  }
});

test("runs list fetches, follows, closes, and switches terminal logs", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID, secondRunID } = await createRunLogFixture(page);
  await page.reload();
  let failHistory = true;
  let failTail = false;
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  let scopedLogRequests = 0;
  let tailRequests = 0;
  const log = (sequence: number) => ({ id: `log_browser_${sequence}`, run_id: runID, sequence, stream: "stdout", message: `Browser output ${sequence}`, created_at: "2026-01-01T00:00:00Z" });
  await page.route("**/api/v1/run-logs?*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("run_id") !== runID) return route.continue();
    scopedLogRequests += 1;
    const limit = Number(url.searchParams.get("limit") ?? "100");
    const offset = Number(url.searchParams.get("offset") ?? "0");
    let items: ReturnType<typeof log>[] = [];
    let total = 151;
    if (limit === 1 && offset === 0) items = [log(1)];
    else if (limit === 100 && offset === 51) items = Array.from({ length: 100 }, (_, index) => log(index + 52));
    else if (limit === 51 && offset === 0) {
      if (failHistory) return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "History unavailable" }) });
      items = Array.from({ length: 51 }, (_, index) => log(index + 1));
    }
    else if (offset >= 151 && failTail) {
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Live output unavailable" }) });
    }
    else if (limit === 100 && offset === 151) {
      tailRequests += 1;
      items = tailRequests > 1 ? [log(152)] : [];
      total += items.length;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ items, count: items.length, total, limit, offset }),
    });
  });
  const firstLogRequest = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/run-logs" && url.searchParams.get("run_id") === runID;
  });
  await page.locator(`[data-run-log-trigger="${runID}"]`).last().click();
  expect((await firstLogRequest).status()).toBe(200);
  const dialog = page.getByRole("dialog", { name: `run ${runID}` });
  await expect(dialog).toContainText("Browser output 52");
  await dialog.getByRole("searchbox", { name: "Filter loaded output", exact: true }).fill("Browser output 27");
  await expect(dialog.getByText("No loaded output matches this filter.", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Load earlier output", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Clear output filter", exact: true }).click();
  await expect(dialog.getByText("Browser output 1", { exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: "Load earlier output" }).click();
  await expect(dialog.getByRole("alert")).toHaveText("Unable to load earlier output. Try again.");
  await expect(dialog).toContainText("Browser output 52");
  await dialog.screenshot({ path: testInfo.outputPath("earlier-log-error.png") });
  failHistory = false;
  await dialog.getByRole("button", { name: "Retry earlier output" }).click();
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  expect(pageErrors).toHaveLength(0);
  await expect(dialog.getByText("Browser output 1", { exact: true })).toBeVisible();
  await dialog.getByRole("searchbox", { name: "Filter loaded output", exact: true }).fill("Browser output 27");
  await expect(dialog.getByText("Browser output 27", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Browser output 52", { exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: "Clear output filter", exact: true }).click();
  await expect(dialog.getByRole("searchbox", { name: "Filter loaded output", exact: true })).toBeFocused();
  await expect(dialog).toContainText("Following active run output.");
  await expect(dialog).toContainText("Browser output 152", { timeout: 10_000 });
  expect(scopedLogRequests).toBeGreaterThanOrEqual(2);
  failTail = true;
  await expect(dialog.getByRole("alert")).toContainText("Unable to load run output", { timeout: 10_000 });
  await expect(dialog.getByText("Browser output 152", { exact: true })).toHaveCount(1);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await dialog.getByRole("button", { name: "Retry logs", exact: true }).scrollIntoViewIfNeeded();
    await expect(dialog.getByRole("button", { name: "Retry logs", exact: true })).toBeVisible();
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("live-log-error-320.png"), fullPage: false });
  }
  failTail = false;
  await dialog.getByRole("button", { name: "Retry logs", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  await expect(dialog.getByText("Browser output 152", { exact: true })).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(page.locator(`[data-run-log-trigger="${runID}"]`).last()).toBeFocused();

  const secondLogRequest = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/run-logs" && url.searchParams.get("run_id") === secondRunID;
  });
  await page.locator(`[data-run-log-trigger="${secondRunID}"]`).last().click();
  expect((await secondLogRequest).status()).toBe(200);
  await expect(page.getByRole("dialog", { name: `run ${secondRunID}` })).toContainText("Run requested");
  await page.keyboard.press("Escape");
  await expect(page.locator(`[data-run-log-trigger="${secondRunID}"]`).last()).toBeFocused();

  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    await page.locator(`[data-run-log-trigger="${runID}"]`).first().click();
    await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("run-logs-history-320.png"), fullPage: true });
    await page.keyboard.press("Escape");
    await expect(page.locator(`[data-run-log-trigger="${runID}"]`).first()).toBeFocused();
  }
  for (const [width, height] of [[640, 320], [320, 320], [1440, 360]] as const) {
    await page.setViewportSize({ width, height });
    const trigger = page.locator(`[data-run-log-trigger="${runID}"]`);
    await (width < 768 ? trigger.first() : trigger.last()).click();
    const lastLine = dialog.getByText("Browser output 152", { exact: true });
    await lastLine.scrollIntoViewIfNeeded();
    const bounds = await dialog.boundingBox();
    const close = dialog.getByRole("button", { name: "Close", exact: true });
    const closeBounds = await close.boundingBox();
    const lineBounds = await lastLine.boundingBox();
    expect(closeBounds!.y).toBeGreaterThanOrEqual(bounds!.y);
    expect(lineBounds!.y + lineBounds!.height).toBeLessThanOrEqual(bounds!.y + bounds!.height);
    if (width === 640) await page.screenshot({ path: testInfo.outputPath("run-logs-landscape.png"), fullPage: false });
    await close.click();
    await expect(dialog).toHaveCount(0);
  }
});

test("system administrators can open the bounded Operations summary", async ({ page }) => {
	await page.goto("/operations");
	await signIn(page);
  await expect(page.getByRole("heading", { name: "System Operations" })).toBeVisible();
  await expect(page.getByText("Local schedule disabled")).toBeVisible();
	await expect(page.getByText("Manual run-log retention", { exact: true })).toBeVisible();
	const policy = page.getByRole("form", { name: "Run-log retention policy" });
	await policy.getByRole("checkbox", { name: "Enable manual retention" }).check();
	const policySaved = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/run-log-retention" && response.request().method() === "PUT");
	await policy.getByRole("button", { name: "Save policy" }).click();
	expect((await policySaved).status()).toBe(200);
	const executeButton = page.getByRole("button", { name: "Previewed delete batch" });
	await expect(executeButton).toBeEnabled();
	await executeButton.click();
	const retentionDialog = page.getByRole("dialog", { name: "Execute one retention batch?" });
	await expect(retentionDialog).toContainText("cannot delete artifacts, audit records, deployments, or runs");
	const executed = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/run-log-retention/execute" && response.request().method() === "POST");
	await retentionDialog.getByRole("button", { name: "Execute retained batch" }).click();
	expect((await executed).status()).toBe(200);
	await expect(page.getByText(/Execution receipt: deleted/i)).toBeVisible();
	await page.reload();
  await expect(page.getByRole("heading", { name: "System Operations" })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  await expect(page.getByRole("dialog", { name: "Mobile navigation" }).getByRole("link", { name: "Operations" })).toBeVisible();
});

test("mobile menu has labeled routes, traps focus, and keeps account actions reachable", async ({ page }, testInfo) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(page.locator(".mobile-bottom-navigation")).toHaveCount(0);
    const menu = page.getByRole("button", { name: "Open mobile navigation" });
    await expect(menu).toHaveText("Menu");
    await menu.click();
    const drawer = page.getByRole("dialog", { name: "Mobile navigation" });
    await expect(drawer).toBeVisible();
    await expect(drawer).toHaveAttribute("aria-modal", "true");
    await expect(drawer.getByRole("link", { name: "Projects", exact: true })).toHaveAttribute("aria-current", "page");
    await expect(drawer.getByRole("link")).toHaveCount(11);
    await expect(drawer.getByRole("button", { name: "Close", exact: true })).toBeVisible();
    if (width === 390) await page.screenshot({ path: testInfo.outputPath("mobile-drawer-390.png") });
    for (let tab = 0; tab < 14; tab++) {
      await page.keyboard.press("Tab");
      expect(await drawer.evaluate((element) => element.contains(document.activeElement))).toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(menu).toBeFocused();
    await page.getByRole("button", { name: "Account", exact: true }).click();
    await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Refresh data", exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    if (width === 390) await page.screenshot({ path: testInfo.outputPath("mobile-navigation-390.png") });
  }
  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  await page.setViewportSize({ width: 1024, height: 844 });
  await expect(page.getByRole("dialog", { name: "Mobile navigation" })).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Desktop navigation" })).toBeVisible();
  await page.getByRole("link", { name: "Runs", exact: true }).click();
  await expect(page).toHaveURL(/\/runs$/);
});

test("header and notification controls fit narrow and intermediate viewports", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 768, 820, 1024, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    for (const label of ["Search navigation", "Notifications", "Account"]) {
      const bounds = await page.getByRole("button", { name: label, exact: true }).boundingBox();
      expect(bounds).not.toBeNull();
      expect(bounds!.x).toBeGreaterThanOrEqual(0);
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    }
    const filter = page.getByRole("searchbox", { name: "Filter this page", exact: true });
    await filter.fill("deployment-filter");
    await expect(filter).toHaveValue("deployment-filter");
    const filterBounds = await filter.boundingBox();
    expect(filterBounds!.width).toBeGreaterThanOrEqual(160);
    expect(filterBounds!.x + filterBounds!.width).toBeLessThanOrEqual(width);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("filter-active-320.png") });
    await page.getByRole("button", { name: "Clear filter", exact: true }).click();
    await expect(filter).toHaveValue("");
    await page.getByRole("button", { name: "Notifications", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Notifications", exact: true });
    await expect(dialog).toBeVisible();
    const bounds = await dialog.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    await page.keyboard.press("Escape");
    if (width === 768 || width === 1440) await page.screenshot({ path: testInfo.outputPath(`navigation-${width}.png`) });
  }
  await page.setViewportSize({ width: 430, height: 240 });
  for (const name of ["Account", "Notifications"]) {
    await page.getByRole("button", { name, exact: true }).click();
    const dialog = page.getByRole("dialog", { name, exact: true });
    const bounds = await dialog.boundingBox();
    expect(bounds!.y).toBeGreaterThanOrEqual(0);
    expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(240);
    if (name === "Account") {
      const signOut = dialog.getByRole("button", { name: "Sign out", exact: true });
      await signOut.scrollIntoViewIfNeeded();
      await expect(signOut).toBeInViewport();
      await expect(dialog.getByRole("heading", { name: "Account", exact: true })).toBeInViewport();
      await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport();
      await page.screenshot({ path: testInfo.outputPath("account-short-430.png") });
    }
    await page.keyboard.press("Escape");
  }
  await page.setViewportSize({ width: 1440, height: 360 });
  const settings = page.getByRole("navigation", { name: "Desktop navigation" }).getByRole("link", { name: "Settings", exact: true });
  await settings.scrollIntoViewIfNeeded();
  const bounds = await settings.boundingBox();
  expect(bounds!.y).toBeGreaterThanOrEqual(0);
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(360);
  await settings.click();
  await expect(page).toHaveURL(/\/settings$/);
});

test("page filtering preserves deployment context without filling browser history", async ({ page }) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  await page.goto("/deployments?service_id=svc_navigation&environment_id=env_navigation");
  const historyLength = await page.evaluate(() => history.length);
  const filter = page.getByRole("searchbox", { name: "Filter this page", exact: true });
  await filter.pressSequentially("audit", { delay: 40 });
  await expect(page).toHaveURL(/q=audit/);
  expect(new URL(page.url()).searchParams.get("service_id")).toBe("svc_navigation");
  expect(new URL(page.url()).searchParams.get("environment_id")).toBe("env_navigation");
  expect(await page.evaluate(() => history.length)).toBe(historyLength);
  await page.goBack();
  await expect(page).toHaveURL(/\/projects$/);
});

test("navigation search supports unmatched filters and resets between openings", async ({ page }) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  const search = page.getByRole("button", { name: "Search navigation", exact: true });
  await search.click();
  await page.getByRole("combobox").fill("zz-navigation-audit");
  await expect(page.getByRole("option", { name: 'Filter this page for "zz-navigation-audit"' })).toBeVisible();
  await page.getByRole("combobox").press("Enter");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(/q=zz-navigation-audit/);
  await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toHaveValue("zz-navigation-audit");
  await search.click();
  await expect(page.getByRole("combobox")).toHaveValue("");
  await page.getByRole("combobox").fill("Settings");
  await expect(page.getByRole("option", { name: "Settings", exact: true })).toBeVisible();
  await page.getByRole("combobox").press("Enter");
  await expect(page).toHaveURL(/\/settings$/);
  await search.click();
  await page.getByRole("combobox").fill("zz-no-page");
  await expect(page.getByText("No matching pages or actions.")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
});

test("runner enrollment stays local to the reveal dialog and browser avoids runner-only APIs", async ({ page }, testInfo) => {
  const forbiddenRunnerCalls: string[] = [];
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.route("**/api/v1/runners/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (/^\/api\/v1\/runners\/[^/]+$/.test(path) || path === "/api/v1/runners/revoke-token") return route.continue();
    forbiddenRunnerCalls.push(path); await route.abort();
  });
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Runners" })).toBeVisible();
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  await page.getByLabel("Runner ID").fill(`runner_browser_${suffix}_${"x".repeat(24)}`);
  await page.getByLabel("Runner name").fill("Browser enrollment runner");
  const creation = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runner-enrollments" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Create enrollment" }).click();
  expect((await creation).status()).toBe(201);
  const dialog = page.getByRole("dialog", { name: "One-time runner enrollment secret" });
  await expect(dialog).toBeVisible();
  const secret = await dialog.getByLabel("Enrollment secret").innerText();
  expect(secret).toMatch(/^nce_[0-9a-f]{64}$/);
  await page.evaluate(() => { Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("Clipboard denied"); } } }); });
  await dialog.getByRole("button", { name: "Copy", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("Unable to copy. Download the secret instead, or try again.");
  await expect(dialog.getByLabel("Enrollment secret")).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(dialog.getByRole("button", { name: "Copy", exact: true })).toBeEnabled();
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("clipboard-error-320.png"), fullPage: false, mask: [dialog.getByLabel("Enrollment secret")] });
  }
  for (const viewport of [{ width: 320, height: 320 }, { width: 640, height: 320 }, { width: 1440, height: 360 }]) {
    await page.setViewportSize(viewport);
    await expect.poll(async () => {
      const bounds = await dialog.boundingBox();
      return Boolean(bounds && bounds.y >= 0 && bounds.y + bounds.height <= viewport.height);
    }).toBe(true);
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    const downloadButton = dialog.getByRole("button", { name: "Download once", exact: true });
    await downloadButton.scrollIntoViewIfNeeded();
    const downloadBounds = await downloadButton.boundingBox();
    expect(downloadBounds!.y + downloadBounds!.height).toBeLessThanOrEqual(viewport.height);
    await dialog.evaluate((element) => { element.scrollTop = 0; });
    await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport();
    if (viewport.width === 640) await page.screenshot({ path: testInfo.outputPath("enrollment-landscape.png"), fullPage: false, mask: [dialog.getByLabel("Enrollment secret")] });
  }
  await page.evaluate(() => { Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => undefined } }); });
  await dialog.getByRole("button", { name: "Copy", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText("Copied to clipboard.");
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  expect(pageErrors).toEqual([]);
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download once" }).click();
  const saved = await download;
  expect(await saved.suggestedFilename()).toBe("nerocd-runner-enrollment.token");
  expect(page.url()).not.toContain(secret);
  expect(await page.evaluate((value) => [localStorage, sessionStorage].every((storage) => !Array.from({ length: storage.length }, (_, index) => `${storage.key(index)}=${storage.getItem(storage.key(index) ?? "")}`).join("\n").includes(value)), secret)).toBe(true);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(secret);
  expect(forbiddenRunnerCalls).toEqual([]);
});

test("production CSP permits shipped style attributes but blocks executable script sources", async ({ page }) => {
  let externalRequests = 0;
  await page.route("https://csp-probe.invalid/**", async (route) => {
    externalRequests += 1;
    await route.fulfill({ contentType: "application/javascript", body: "globalThis.__nerocdCSPExternal = true" });
  });
  const response = await page.goto("/");
  if (!response) throw new Error("application shell response is unavailable");
  const csp = response.headers()["content-security-policy"];
  expect(csp).toBe("default-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; object-src 'none'");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { deploymentID } = await createDetailFixture(page);
  await page.goto(`/deployments/${deploymentID}`);
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByText("Cancellation requested")).toBeVisible();

  const result = await page.evaluate(async () => {
    const violations: Array<{ directive: string; blocked: string }> = [];
    document.addEventListener("securitypolicyviolation", (event) => violations.push({ directive: event.effectiveDirective, blocked: event.blockedURI }));
    delete (globalThis as Record<string, unknown>).__nerocdCSPInline;
    delete (globalThis as Record<string, unknown>).__nerocdCSPExternal;
    const inline = document.createElement("script");
    inline.text = "globalThis.__nerocdCSPInline = true";
    document.head.append(inline);
    const external = document.createElement("script");
    external.src = "https://csp-probe.invalid/blocked.js";
    document.head.append(external);
    await new Promise((resolve) => setTimeout(resolve, 100));
    const toaster = document.querySelector<HTMLElement>("[data-sonner-toaster]");
    return {
      inline: (globalThis as Record<string, unknown>).__nerocdCSPInline === true,
      external: (globalThis as Record<string, unknown>).__nerocdCSPExternal === true,
      violations,
      toasterStyle: toaster?.style.getPropertyValue("--normal-bg") ?? "",
    };
  });
  expect(result.inline).toBe(false);
  expect(result.external).toBe(false);
  expect(externalRequests).toBe(0);
  expect(result.violations.some((item) => item.blocked.includes("inline") && item.directive.startsWith("script-src"))).toBe(true);
  expect(result.violations.some((item) => item.blocked.includes("csp-probe.invalid") && item.directive.startsWith("script-src"))).toBe(true);
  // Sonner deliberately uses CSS custom properties. This demonstrates that
  // the narrow style-src exception still lets the shipped UI render.
  expect(result.toasterStyle).toBe("var(--popover)");
});

test("deployment operations navigation, deep links, search, and mobile menu stay on public browser APIs", async ({ page }) => {
  await page.route("**/api/v1/runners/**", async (route) => { throw new Error(`browser attempted runner endpoint: ${route.request().url()}`); });
  await page.goto("/deployments?q=rollback");
  await signIn(page);
  await expect(page).toHaveURL(/\/deployments\/?\?q=rollback/);
  await expect(page.getByRole("heading", { name: "Deployments" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Deployments" }).first()).toBeVisible();
  const { deploymentID } = await createDetailFixture(page);
  // The detail resource owns its stable deployment ID. It must not require
  // the list route's environment filter or a query string to be addressable.
  await page.goto(`/deployments/${deploymentID}`);
  await expect(page).toHaveURL(new RegExp(`/deployments/${deploymentID}$`));
  await expect(page.getByText(`Deployment ${deploymentID}`)).toBeVisible();
  await page.keyboard.press("Meta+k");
  const commandInput = page.getByPlaceholder("Find a page or action...");
  await expect(commandInput).toBeFocused();
  await page.keyboard.press("Escape");
  await page.reload();
  await expect(page).toHaveURL(new RegExp(`/deployments/${deploymentID}$`));
  await expect(page.getByText(`Deployment ${deploymentID}`)).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/deployments\/?\?q=rollback/);
  await page.goForward();
  await expect(page).toHaveURL(new RegExp(`/deployments/${deploymentID}$`));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  await expect(page.getByRole("dialog", { name: "Mobile navigation" }).getByRole("link", { name: "Deployments" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Mobile navigation" })).toHaveCount(0);
});


test("dashboard filtering removes unrelated rows and its Clear action restores the URL and input", async ({ page }) => {
  await page.goto("/");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Current activity" })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  const filter = page.getByRole("searchbox", { name: "Filter this page", exact: true });
  const historyLength = await page.evaluate(() => history.length);
  await filter.fill("no-such-dashboard-activity");
  await expect(page.getByText("No runs match this search.")).toBeVisible();
  await expect(page.getByText("No pending approvals match this search.")).toBeVisible();
  await expect(page.getByText("No logs match this search.")).toBeVisible();
  await expect(page.getByText("0 matching records")).toBeVisible();
  await page.getByRole("button", { name: "Clear", exact: true }).click();
  await expect(filter).toHaveValue("");
  await expect(page).toHaveURL(/\/$/);
  expect(await page.evaluate(() => history.length)).toBe(historyLength);
  await expect(page.getByText("No runs match this search.")).toHaveCount(0);
});

test("runs filter narrows the loaded page and retains usable log actions", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID, secondRunID } = await createRunLogFixture(page);
  await page.reload();
  const filter = page.getByRole("searchbox", { name: "Filter this page", exact: true });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await filter.fill(runID);
    await expect(page.locator(`[data-run-detail-link="${runID}"]`)).toHaveCount(2);
    await expect(page.locator(`[data-run-detail-link="${secondRunID}"]`)).toHaveCount(0);
    await expect(page.getByText(/1 of \d+ runs match on this page\./)).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    if (width < 768) {
      const card = page.locator(`[data-run-detail-link="${runID}"]`).first().locator("xpath=../..");
      const geometry = await card.evaluate((element) => {
        const bounds = element.getBoundingClientRect();
        const content = element.closest('[data-slot="card-content"]')!.getBoundingClientRect();
        const controls = [...element.querySelectorAll('a, button, [data-slot="badge"]')].map((control) => {
          const rect = control.getBoundingClientRect();
          return { left: rect.left, right: rect.right };
        });
        return { left: bounds.left, right: bounds.right, contentLeft: content.left, contentRight: content.right, controls };
      });
      expect(geometry.left).toBeGreaterThanOrEqual(geometry.contentLeft);
      expect(geometry.right).toBeLessThanOrEqual(geometry.contentRight);
      expect(geometry.controls.every((control) => control.left >= geometry.left && control.right <= geometry.right)).toBe(true);
      await expect(card.locator('[data-slot="badge"]')).toBeInViewport();
    }
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("runs-filter-320.png") });
  }
  await page.locator(`[data-run-log-trigger="${runID}"]`).last().click();
  await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
  await page.keyboard.press("Escape");
  await filter.fill("no-such-run-filter");
  await expect(page.getByText("No runs match this filter on the loaded page.")).toBeVisible();
  await page.getByRole("button", { name: "Clear filter", exact: true }).click();
  await expect(page.locator(`[data-run-detail-link="${secondRunID}"]`)).toHaveCount(2);
});

test("inventory and approval filters apply URL queries and recover after clearing", async ({ page }, testInfo) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}`;
  const projectName = `Filter project ${suffix}`;
  const templateName = `Filter template ${suffix}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: projectName, description: "Inventory filtering fixture" });
  const template = await postJSON<{ id: string }>(page, "/api/v1/templates", {
    project_id: project.id, name: templateName, kind: "shell",
    run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "filter-fixture"] } },
    workflow: { steps: [] }, runner_tags: ["shell"], requires_ack: true,
  });
  await postJSON(page, "/api/v1/runs", { template_id: template.id });
  const cases = [
    ["projects", "No projects match this filter on the loaded page."],
    ["templates", "No templates match this filter on the loaded page."],
    ["approvals", "No pending approvals match this filter on the loaded page."],
  ] as const;
  for (const [path, emptyMessage] of cases) {
    await page.goto(`/${path}?q=${encodeURIComponent(projectName)}`);
    const filter = page.getByRole("searchbox", { name: "Filter this page", exact: true });
    await expect(filter).toHaveValue(projectName);
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 844 });
      await expect(page.getByText(emptyMessage, { exact: true })).toHaveCount(0);
      if (path === "projects") await expect(page.getByRole("heading", { name: projectName, exact: true })).toBeVisible();
      if (path === "templates") await expect(page.getByText(templateName, { exact: true })).toHaveCount(2);
      if (path === "approvals") await expect(page.getByText(templateName, { exact: true })).toBeVisible();
      await filter.fill("no-such-inventory-result");
      await expect(page.getByText(emptyMessage, { exact: true })).toBeVisible();
      if (width === 320) await page.screenshot({ path: testInfo.outputPath(`${path}-filter-empty-320.png`), fullPage: true });
      await page.getByRole("button", { name: "Clear filter", exact: true }).click();
      await expect(filter).toHaveValue("");
      await expect(page.getByText(emptyMessage, { exact: true })).toHaveCount(0);
      await filter.fill(projectName);
    }
  }
});

test("project and template forms retain failed drafts and reset only after successful saves", async ({ page }) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const failed = new Set<string>();
  for (const path of ["projects", "templates", "repositories"]) {
    await page.route(`**/api/v1/${path}`, async (route) => {
      const method = route.request().method();
      const key = `${path}:${method}`;
      if (["POST", "PATCH"].includes(method) && !failed.has(key)) {
        failed.add(key);
        return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary save failure" }) });
      }
      return route.continue();
    });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}`;
  let projectID = "";
  for (const resource of ["project", "template"] as const) {
    if (resource === "template") await page.goto("/templates");
    const create = page.getByRole("button", { name: `Create ${resource}`, exact: true });
    const form = create.locator("xpath=ancestor::form");
    const name = form.locator('input[name="name"]');
    const draft = `Retry ${resource} ${suffix}`;
    await name.fill(draft);
    if (resource === "template") {
      await form.locator('select[name="project_id"]').selectOption(projectID);
      await form.locator('select[name="kind"]').selectOption("shell");
      await form.locator('input[name="command"]').fill("echo form-fixture");
    }
    const failure = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/${resource}s` && response.request().method() === "POST");
    await create.click();
    expect((await failure).status()).toBe(503);
    await expect(create).toBeEnabled();
    await expect(name).toHaveValue(draft);
    const success = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/${resource}s` && response.request().method() === "POST");
    await create.click();
    const response = await success;
    expect(response.status()).toBe(201);
    if (resource === "project") projectID = (await response.json()).id;
    await expect(name).toHaveValue("");
    await page.getByRole("searchbox", { name: "Filter this page", exact: true }).fill(draft);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: `Edit ${resource}` });
    const editedName = dialog.locator('input[name="name"]');
    await editedName.fill(`${draft} saved`);
    const save = dialog.getByRole("button", { name: `Save ${resource}`, exact: true });
    const editFailure = page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/v1/${resource}s` && candidate.request().method() === "PATCH");
    await save.click();
    expect((await editFailure).status()).toBe(503);
    await expect(save).toBeEnabled();
    await expect(editedName).toHaveValue(`${draft} saved`);
    const editSuccess = page.waitForResponse((candidate) => new URL(candidate.url()).pathname === `/api/v1/${resource}s` && candidate.request().method() === "PATCH");
    await save.click();
    expect((await editSuccess).status()).toBe(200);
    await expect(dialog).toHaveCount(0);
  }
  await page.goto("/projects");
  const register = page.getByRole("button", { name: "Register source", exact: true });
  const sourceForm = register.locator("xpath=ancestor::form");
  await sourceForm.locator('select[name="project_id"]').selectOption(projectID);
  await sourceForm.locator('input[name="name"]').fill(`Retry source ${suffix}`);
  await sourceForm.locator('input[name="url"]').fill("https://example.invalid/repository.git");
  for (const status of [503, 201]) {
    const saved = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/repositories" && response.request().method() === "POST");
    await register.click();
    expect((await saved).status()).toBe(status);
    await expect(register).toBeEnabled();
    await expect(sourceForm.locator('input[name="name"]')).toHaveValue(status === 503 ? `Retry source ${suffix}` : "");
  }
  expect(pageErrors).toEqual([]);
});

test("inventory labels focus their own fields and mobile template editing is named", async ({ page }) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}`;
  const projectName = `Labeled project ${suffix}`;
  const templateName = `Labeled template ${suffix}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: projectName, description: "Accessible form fixture" });
  await postJSON(page, "/api/v1/templates", { project_id: project.id, name: templateName, kind: "shell", run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "labels"] } }, workflow: { steps: [] }, runner_tags: [], requires_ack: false });
  await page.reload();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Configure project", exact: true }).click();
  const configure = page.getByRole("dialog", { name: "Configure project" });
  await configure.getByText("Project Name", { exact: true }).click();
  await expect(configure.getByRole("textbox", { name: "Project Name", exact: true })).toBeFocused();
  await configure.getByRole("textbox", { name: "Project Name", exact: true }).fill("Mobile draft");
  await expect(configure.getByRole("combobox", { name: "Project", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(page.getByRole("textbox", { name: "Project Name", exact: true })).toHaveValue("");
  await page.goto(`/templates?q=${encodeURIComponent(templateName)}`);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Edit template" });
    await expect(dialog.getByRole("textbox", { name: "Template Name", exact: true })).toHaveValue(templateName);
    await dialog.getByText("Command", { exact: true }).click();
    await expect(dialog.getByRole("textbox", { name: "Command", exact: true })).toBeFocused();
    await expect(dialog.getByRole("combobox", { name: "Kind", exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
  }
});

test("mobile template inventory is reachable before opening the creation form", async ({ page }, testInfo) => {
  await page.goto("/templates");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Mobile creation ${suffix}`, description: "Mobile creation fixture" });
  await page.reload();
  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(page.getByRole("textbox", { name: "Template Name", exact: true })).toBeHidden();
    await expect(page.getByText("Template inventory", { exact: true })).toBeInViewport();
    await page.getByRole("button", { name: "Create template", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Create template", exact: true });
    await expect(dialog).toBeVisible();
    const name = dialog.getByRole("textbox", { name: "Template Name", exact: true });
    await expect(name).toBeVisible();
    await dialog.getByRole("combobox", { name: "Project", exact: true }).selectOption(project.id);
    if (width === 320) {
      await name.fill(`Mobile template ${suffix}`);
      await dialog.getByRole("combobox", { name: "Kind", exact: true }).selectOption("shell");
      await dialog.getByRole("textbox", { name: "Command", exact: true }).fill("echo mobile");
      const created = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/templates" && response.request().method() === "POST");
      await dialog.getByRole("button", { name: "Create template", exact: true }).click();
      expect((await created).status()).toBe(201);
      await expect(name).toHaveValue("");
      await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport();
    }
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Create template", exact: true })).toBeFocused();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("templates-inventory-320.png") });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(page.getByRole("textbox", { name: "Template Name", exact: true })).toBeVisible();
});

test("running a template opens its detail route and follows scoped output", async ({ page }) => {
  await page.goto("/templates");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = `${Date.now()}`;
  const templateName = `Follow template ${suffix}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Follow project ${suffix}`, description: "Template run fixture" });
  await postJSON(page, "/api/v1/templates", { project_id: project.id, name: templateName, kind: "shell", run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "follow"] } }, workflow: { steps: [] }, runner_tags: [], requires_ack: false });
  let failRequest = true;
  await page.route("**/api/v1/runs", async (route) => {
    if (route.request().method() === "POST" && failRequest) {
      failRequest = false;
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary run request failure" }) });
    }
    return route.continue();
  });
  const logRunIDs: string[] = [];
  await page.route("**/api/v1/run-logs?*", async (route) => {
    const url = new URL(route.request().url());
    const runID = url.searchParams.get("run_id");
    if (!runID) return route.continue();
    logRunIDs.push(runID);
    const offset = Number(url.searchParams.get("offset") ?? "0");
    const limit = Number(url.searchParams.get("limit") ?? "100");
    const sequence = offset === 0 ? 1 : 2;
    const items = offset <= 1 ? [{ id: `${runID}_log_${sequence}`, run_id: runID, sequence, stream: "stdout", message: `Template output ${sequence}`, created_at: "2026-01-01T00:00:00Z" }] : [];
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ items, count: items.length, total: offset === 0 ? 1 : 2, limit, offset }) });
  });
  const inventoryURL = `/templates?q=${encodeURIComponent(templateName)}`;
  await page.goto(inventoryURL);
  const failedRequest = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Play", exact: true }).click();
  expect((await failedRequest).status()).toBe(503);
  await expect(page.getByRole("button", { name: "Play", exact: true })).toBeEnabled();
  expect(new URL(page.url()).pathname).toBe("/templates");
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    const requested = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs" && response.request().method() === "POST");
    await page.getByRole("button", { name: "Play", exact: true }).click();
    const response = await requested;
    expect(response.status()).toBe(201);
    const run = await response.json() as { id: string };
    await expect(page).toHaveURL(new RegExp(`/runs/${run.id}$`));
    const dialog = page.getByRole("dialog", { name: `run ${run.id}` });
    await expect(dialog).toContainText("Template output 1");
    await expect(dialog).toContainText("Following active run output.");
    await expect(dialog).toContainText("Template output 2", { timeout: 10_000 });
    expect(logRunIDs).toContain(run.id);
    await page.keyboard.press("Escape");
    await page.goBack();
    expect(new URL(page.url()).pathname).toBe("/templates");
    await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toHaveValue(templateName);
  }
});

test("session-authenticated operators can cancel queued runs on desktop and mobile", async ({ page }) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const first = await createRunLogFixture(page);
  const second = await createRunLogFixture(page);
  let failFirst = true;
  await page.route("**/api/v1/runs/cancel", async (route) => {
    if (failFirst) {
      failFirst = false;
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary cancellation failure" }) });
    }
    return route.continue();
  });
  for (const [width, runID] of [[320, first.runID], [390, first.secondRunID], [430, second.runID], [1440, second.secondRunID]] as const) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`/runs?q=${runID}`);
    const cancel = page.getByRole("button", { name: "Cancel", exact: true });
    await expect(cancel).toBeInViewport();
    if (width === 320) {
      const failed = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs/cancel");
      await cancel.click();
      expect((await failed).status()).toBe(503);
      await expect(cancel).toBeEnabled();
    }
    const canceled = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs/cancel");
    await cancel.click();
    const response = await canceled;
    expect(response.status()).toBe(200);
    expect(await response.json()).toMatchObject({ id: runID, status: "canceled" });
    await expect(cancel).toHaveCount(0);
    await expect(page.getByText("canceled", { exact: true }).nth(width < 768 ? 0 : 1)).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  }
});

test("project access preserves failed drafts and resets after a successful retry", async ({ page }, testInfo) => {
  await page.goto("/settings");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Access retry ${Date.now()}`, description: "Access form fixture" });
  await page.reload();
  const card = page.getByText("Project access", { exact: true }).locator('xpath=ancestor::*[@data-slot="card"]');
  const email = card.getByRole("textbox", { name: "Email", exact: true });
  const originalEmail = await email.inputValue();
  let failRequest = true;
  await page.route("**/api/v1/project-members", async (route) => {
    if (route.request().method() === "POST" && failRequest) {
      failRequest = false;
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary access update failure" }) });
    }
    return route.continue();
  });
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    failRequest = true;
    await card.getByRole("combobox", { name: "Project", exact: true }).selectOption(project.id);
    await email.fill("draft@example.test");
    await card.getByRole("combobox", { name: "Role", exact: true }).selectOption("maintainer");
    const failed = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/project-members" && response.request().method() === "POST");
    await card.getByRole("button", { name: "Update access" }).click();
    expect((await failed).status()).toBe(503);
    await expect(card.getByRole("button", { name: "Update access" })).toBeEnabled();
    await expect(email).toHaveValue("draft@example.test");
    await expect(card.getByRole("combobox", { name: "Project", exact: true })).toHaveValue(project.id);
    await expect(card.getByRole("combobox", { name: "Role", exact: true })).toHaveValue("maintainer");
    if (width === 390) await card.screenshot({ path: testInfo.outputPath("project-access-draft-390.png") });
    await email.fill(originalEmail);
    const saved = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/project-members" && response.request().method() === "POST");
    await card.getByRole("button", { name: "Update access" }).click();
    expect((await saved).status()).toBe(200);
    await expect(card.getByRole("combobox", { name: "Role", exact: true })).toHaveValue("viewer");
    await expect(email).toHaveValue(originalEmail);
  }
});


test("deployment selection restores deep links and clears dependent choices on project change", async ({ page }) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  const otherProject = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Other deployment project ${Date.now()}`, description: "Selection fixture" });
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/deployments?service_id=${fixture.serviceID}&environment_id=${fixture.environmentID}&offset=50`);
    const project = page.getByRole("combobox", { name: "Project", exact: true });
    const service = page.getByRole("combobox", { name: "Service", exact: true });
    const environment = page.getByRole("combobox", { name: "Environment", exact: true });
    const revision = page.getByRole("combobox", { name: "Immutable revision", exact: true });
    await expect(project).toHaveValue(fixture.projectID);
    await expect(service).toHaveValue(fixture.serviceID);
    await expect(environment).toHaveValue(fixture.environmentID);
    await revision.selectOption(fixture.revisionID);
    await expect(page.getByRole("button", { name: "Request deployment", exact: true })).toBeEnabled();
    await project.selectOption(otherProject.id);
    await expect(service).toHaveValue("");
    await expect(environment).toHaveValue("");
    await expect(revision).toHaveValue("");
    await expect(page.getByRole("button", { name: "Request deployment", exact: true })).toBeDisabled();
    expect(new URL(page.url()).searchParams.has("service_id")).toBe(false);
    expect(new URL(page.url()).searchParams.has("environment_id")).toBe(false);
    expect(new URL(page.url()).searchParams.has("offset")).toBe(false);
    await page.goBack();
    await expect(project).toHaveValue(fixture.projectID);
    await expect(service).toHaveValue(fixture.serviceID);
    await expect(environment).toHaveValue(fixture.environmentID);
    expect(new URL(page.url()).searchParams.get("offset")).toBe("50");
    await page.reload();
    await expect(project).toHaveValue(fixture.projectID);
    await expect(service).toHaveValue(fixture.serviceID);
    await expect(environment).toHaveValue(fixture.environmentID);
  }
});

test("project viewers can browse deployment history without deployment write controls", async ({ page, browser }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  await postJSON(page, "/api/v1/project-members", { project_id: fixture.projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const viewer = await context.newPage();
    await viewer.goto("/sign-in");
    await viewer.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(viewer.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const width of [320, 390, 430, 1440]) {
      await viewer.setViewportSize({ width, height: 1000 });
      await viewer.goto("/deployments");
      await viewer.getByRole("combobox", { name: "Project", exact: true }).selectOption(fixture.projectID);
      await viewer.getByRole("combobox", { name: "Service", exact: true }).selectOption(fixture.serviceID);
      await viewer.getByRole("combobox", { name: "Environment", exact: true }).selectOption(fixture.environmentID);
      const detail = viewer.locator(`a[href*="/deployments/${fixture.deploymentID}"]`);
      await expect(detail).toBeVisible();
      await expect(viewer.getByRole("button", { name: "Request deployment", exact: true })).toHaveCount(0);
      await expect(viewer.getByRole("combobox", { name: "Immutable revision", exact: true })).toHaveCount(0);
      expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (width === 320) await viewer.screenshot({ path: testInfo.outputPath("viewer-deployments-320.png"), fullPage: true });
      await detail.click();
      await expect(viewer.getByText(`Deployment ${fixture.deploymentID}`, { exact: true })).toBeVisible();
      await expect(viewer.getByRole("button", { name: "Confirm", exact: true })).toHaveCount(0);
      await expect(viewer.getByRole("button", { name: "Cancel", exact: true })).toHaveCount(0);
    }
    const denied = await viewer.request.post("/api/v1/deployments", {
      headers: { Origin: new URL(viewer.url()).origin, "X-NeroCD-CSRF": "1" },
      data: { environment_id: fixture.environmentID, desired_revision_id: fixture.revisionID, idempotency_key: `viewer-denied-${Date.now()}` },
    });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});

test("deployment selectors and detail labels include records beyond the first hundred", async ({ page }) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  const targets = {
    services: { id: fixture.serviceID, project_id: fixture.projectID, name: "Later service" },
    environments: { id: fixture.environmentID, service_id: fixture.serviceID, name: "Later environment" },
    revisions: { id: fixture.revisionID, service_id: fixture.serviceID, requested_ref: "Later revision", git_commit: "abc123456789" },
  };
  const requested: string[] = [];
  for (const resource of ["services", "environments", "revisions"] as const) {
    const target = targets[resource];
    const records = [...Array.from({ length: 100 }, (_, index) => ({ ...target, id: `earlier_${resource}_${index}` })), target];
    await page.route(`**/api/v1/${resource}?*`, async (route) => {
      const url = new URL(route.request().url());
      expect(url.searchParams.get("limit")).toBe("100");
      if (resource === "revisions") expect(url.searchParams.get("service_id")).toBe(fixture.serviceID);
      const offset = Number(url.searchParams.get("offset") ?? 0);
      requested.push(`${resource}:${offset}`);
      const items = records.slice(offset, offset + 100);
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ items, count: items.length, total: records.length, limit: 100, offset }) });
    });
  }
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/deployments?service_id=${fixture.serviceID}&environment_id=${fixture.environmentID}`);
    await expect(page.getByRole("combobox", { name: "Service", exact: true })).toHaveValue(fixture.serviceID);
    await expect(page.getByRole("combobox", { name: "Environment", exact: true })).toHaveValue(fixture.environmentID);
    await page.getByRole("combobox", { name: "Immutable revision", exact: true }).selectOption(fixture.revisionID);
    await expect(page.getByRole("button", { name: "Request deployment", exact: true })).toBeEnabled();
    await page.locator(`a[href*="/deployments/${fixture.deploymentID}"]`).click();
    await expect(page.getByText(`Deployment ${fixture.deploymentID}`, { exact: true })).toBeVisible();
    await expect(page.getByText("Later environment", { exact: true })).toBeVisible();
    await expect(page.getByText("Later revision (abc123456789)", { exact: true })).toBeVisible();
  }
  for (const resource of ["services", "environments", "revisions"]) expect(requested).toContain(`${resource}:100`);
});

test("project maintainers get deployment actions only in their maintained project", async ({ page, browser }) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const maintained = await createDetailFixture(page);
  const viewed = await createDetailFixture(page);
  await postJSON(page, "/api/v1/project-members", { project_id: maintained.projectID, email: "oidc-browser@example.invalid", role: "maintainer" });
  await postJSON(page, "/api/v1/project-members", { project_id: viewed.projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const width of [390, 1440]) {
      const environment = await postJSON<{ id: string }>(page, "/api/v1/environments", { service_id: maintained.serviceID, name: `Maintainer ${width}`, compose_project: `maintainer-${width}-${Date.now()}`, timeout_seconds: 60, rollback_safe: true });
      await operator.setViewportSize({ width, height: 1000 });
      await operator.goto(`/deployments?service_id=${maintained.serviceID}&environment_id=${environment.id}`);
      await operator.getByRole("combobox", { name: "Immutable revision", exact: true }).selectOption(maintained.revisionID);
      const created = operator.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/deployments" && response.request().method() === "POST");
      await operator.getByRole("button", { name: "Request deployment", exact: true }).click();
      const response = await created;
      expect(response.status()).toBe(201);
      const deployment = await response.json() as { id: string };
      await expect(operator.getByText(`Deployment ${deployment.id}`, { exact: true })).toBeVisible();
      const canceled = operator.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/deployments/cancel");
      await operator.getByRole("button", { name: "Cancel", exact: true }).click();
      expect((await canceled).status()).toBe(200);
      await operator.goto(`/deployments?service_id=${viewed.serviceID}&environment_id=${viewed.environmentID}`);
      await expect(operator.getByRole("combobox", { name: "Environment", exact: true })).toHaveValue(viewed.environmentID);
      await expect(operator.getByRole("button", { name: "Request deployment", exact: true })).toHaveCount(0);
      await operator.goto(`/deployments/${viewed.deploymentID}`);
      await expect(operator.getByText(`Deployment ${viewed.deploymentID}`, { exact: true })).toBeVisible();
      await expect(operator.getByRole("button", { name: "Cancel", exact: true })).toHaveCount(0);
    }
    const denied = await operator.request.post("/api/v1/deployments/cancel", {
      headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" },
      data: { deployment_id: viewed.deploymentID, request_id: `denied-${Date.now()}` },
    });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});


test("run cancellation respects project membership while viewers retain logs", async ({ page, browser }) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const maintained = await createRunLogFixture(page);
  const viewed = await createRunLogFixture(page);
  await postJSON(page, "/api/v1/project-members", { project_id: maintained.projectID, email: "oidc-browser@example.invalid", role: "maintainer" });
  await postJSON(page, "/api/v1/project-members", { project_id: viewed.projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const [width, runID] of [[390, maintained.runID], [1440, maintained.secondRunID]] as const) {
      await operator.setViewportSize({ width, height: 1000 });
      await operator.goto(`/runs?q=${viewed.runID}`);
      await expect(operator.getByRole("button", { name: "Logs", exact: true })).toBeVisible();
      await expect(operator.getByRole("button", { name: "Cancel", exact: true })).toHaveCount(0);
      await operator.getByRole("button", { name: "Logs", exact: true }).click();
      await expect(operator.getByRole("dialog", { name: `run ${viewed.runID}` })).toContainText("Run requested");
      await operator.keyboard.press("Escape");
      await operator.goto(`/runs?q=${runID}`);
      const canceled = operator.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs/cancel");
      await operator.getByRole("button", { name: "Cancel", exact: true }).click();
      const response = await canceled;
      expect(response.status()).toBe(200);
      expect(await response.json()).toMatchObject({ id: runID, status: "canceled" });
      await expect(operator.getByRole("button", { name: "Cancel", exact: true })).toHaveCount(0);
    }
    const denied = await operator.request.post("/api/v1/runs/cancel", {
      headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" },
      data: { run_id: viewed.runID },
    });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});


test("approval decisions respect project roles and prevent conflicting requests", async ({ page, browser }) => {
  await page.goto("/approvals");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const maintained = await createRunLogFixture(page, true);
  const viewed = await createRunLogFixture(page, true);
  await postJSON(page, "/api/v1/project-members", { project_id: maintained.projectID, email: "oidc-browser@example.invalid", role: "maintainer" });
  await postJSON(page, "/api/v1/project-members", { project_id: viewed.projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    let releaseFailure: (() => void) | undefined;
    let failFirst = true;
    await operator.route("**/api/v1/runs/approve", async (route) => {
      if (!failFirst) return route.continue();
      failFirst = false;
      await new Promise<void>((resolve) => { releaseFailure = resolve; });
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary approval failure" }) });
    });
    for (const [width, path, runID, decision] of [[390, "/", maintained.runID, "Approve"], [1440, "/approvals", maintained.secondRunID, "Reject"]] as const) {
      await operator.setViewportSize({ width, height: 1000 });
      await operator.goto(`${path}?q=${viewed.runID}`);
      await expect(operator.getByRole("link", { name: "Run details", exact: true })).toBeVisible();
      await expect(operator.getByRole("button", { name: "Approve", exact: true })).toHaveCount(0);
      await expect(operator.getByRole("button", { name: "Reject", exact: true })).toHaveCount(0);
      await operator.goto(`${path}?q=${runID}`);
      const approve = operator.getByRole("button", { name: "Approve", exact: true });
      const reject = operator.getByRole("button", { name: "Reject", exact: true });
      if (decision === "Approve") {
        const failed = operator.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs/approve");
        await approve.click();
        await expect(approve).toBeDisabled();
        await expect(reject).toBeDisabled();
        await expect.poll(() => Boolean(releaseFailure)).toBe(true);
        releaseFailure!();
        expect((await failed).status()).toBe(503);
        await expect(approve).toBeEnabled();
        await expect(reject).toBeEnabled();
      }
      const saved = operator.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/runs/${decision.toLowerCase()}`);
      await operator.getByRole("button", { name: decision, exact: true }).click();
      const response = await saved;
      expect(response.status()).toBe(200);
      expect(await response.json()).toMatchObject({ run_id: runID, status: decision === "Approve" ? "approved" : "rejected" });
      await expect(approve).toHaveCount(0);
      await expect(reject).toHaveCount(0);
    }
    const denied = await operator.request.post("/api/v1/runs/approve", { headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" }, data: { run_id: viewed.runID } });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});


test("template actions and creation projects follow project membership", async ({ page, browser }, testInfo) => {
  await page.goto("/templates");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const maintained = await createRunLogFixture(page);
  const viewed = await createRunLogFixture(page);
  await postJSON(page, "/api/v1/project-members", { project_id: maintained.projectID, email: "oidc-browser@example.invalid", role: "maintainer" });
  await postJSON(page, "/api/v1/project-members", { project_id: viewed.projectID, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      await operator.goto(`/templates?q=${viewed.templateID}`);
      await expect(operator.getByText(/^1 of \d+ templates match on this page\.$/)).toBeVisible();
      await expect(operator.getByRole("button", { name: "Play", exact: true })).toHaveCount(0);
      await expect(operator.getByRole("button", { name: "Edit", exact: true })).toHaveCount(0);
      if (width < 1280) await operator.getByRole("button", { name: "Create template", exact: true }).click();
      const project = operator.getByRole("combobox", { name: "Project", exact: true });
      await expect(project.locator(`option[value="${maintained.projectID}"]`)).toHaveCount(1);
      await expect(project.locator(`option[value="${viewed.projectID}"]`)).toHaveCount(0);
      if (width < 1280) await operator.keyboard.press("Escape");
      await operator.goto(`/templates?q=${maintained.templateID}`);
      await operator.getByRole("button", { name: "Edit", exact: true }).click();
      const dialog = operator.getByRole("dialog", { name: "Edit template", exact: true });
      await dialog.getByRole("textbox", { name: "Template Name", exact: true }).fill(`Maintained template ${width}`);
      const edited = operator.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === "/api/v1/templates");
      await dialog.getByRole("button", { name: "Save template", exact: true }).click();
      expect((await edited).status()).toBe(200);
      await expect(dialog).toHaveCount(0);
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (width === 320) await operator.screenshot({ path: testInfo.outputPath("template-project-actions-320.png"), fullPage: true });
      const requested = operator.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runs" && response.request().method() === "POST");
      await operator.getByRole("button", { name: "Play", exact: true }).click();
      const response = await requested;
      expect(response.status()).toBe(201);
      const run = await response.json() as { id: string };
      await expect(operator.getByRole("dialog", { name: `run ${run.id}` })).toBeVisible();
    }
    const denied = await operator.request.post("/api/v1/runs", { headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" }, data: { template_id: viewed.templateID } });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});

test("project controls and source registration follow project membership", async ({ page, browser }, testInfo) => {
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const maintained = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: "Maintained project controls" });
  const viewed = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: "Viewed project controls" });
  await postJSON(page, "/api/v1/project-members", { project_id: maintained.id, email: "oidc-browser@example.invalid", role: "maintainer" });
  await postJSON(page, "/api/v1/project-members", { project_id: viewed.id, email: "oidc-browser@example.invalid", role: "viewer" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      await operator.goto(`/projects?q=${viewed.id}`);
      await expect(operator.getByRole("heading", { name: "Viewed project controls", exact: true })).toBeVisible();
      await expect(operator.getByRole("button", { name: /^(Edit|Archive)$/ })).toHaveCount(0);
      if (width < 1280) await operator.getByRole("button", { name: "Configure project", exact: true }).click();
      await expect(operator.getByRole("button", { name: "Create project", exact: true })).toBeVisible();
      const project = operator.getByRole("combobox", { name: "Project", exact: true });
      await expect(project.locator(`option[value="${maintained.id}"]`)).toHaveCount(1);
      await expect(project.locator(`option[value="${viewed.id}"]`)).toHaveCount(0);
      if (width < 1280) await operator.keyboard.press("Escape");
      await operator.goto(`/projects?q=${maintained.id}`);
      await expect(operator.getByRole("button", { name: "Archive", exact: true })).toBeVisible();
      await operator.getByRole("button", { name: "Edit", exact: true }).click();
      const dialog = operator.getByRole("dialog", { name: "Edit project", exact: true });
      await dialog.getByRole("textbox", { name: "Project Name", exact: true }).fill(`Maintained project ${width}`);
      const edited = operator.waitForResponse((response) => response.request().method() === "PATCH" && new URL(response.url()).pathname === "/api/v1/projects");
      await dialog.getByRole("button", { name: "Save project", exact: true }).click();
      expect((await edited).status()).toBe(200);
      await expect(dialog).toHaveCount(0);
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (width === 320) await operator.screenshot({ path: testInfo.outputPath("project-controls-320.png"), fullPage: true });
    }
    await operator.getByRole("combobox", { name: "Project", exact: true }).selectOption(maintained.id);
    await operator.getByRole("textbox", { name: "Repository Name", exact: true }).fill("Maintained source");
    await operator.getByRole("textbox", { name: "Repository URL", exact: true }).fill("https://example.invalid/maintained.git");
    const registered = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/repositories");
    await operator.getByRole("button", { name: "Register source", exact: true }).click();
    expect((await registered).status()).toBe(201);
    const denied = await operator.request.patch("/api/v1/projects", { headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" }, data: { id: viewed.id, name: "Denied edit" } });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});

test("project access editing follows ownership and refreshes after role changes", async ({ page, browser }, testInfo) => {
  await page.goto("/settings");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const owned = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: "Owned access fixture" });
  const maintained = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: "Maintained access fixture" });
  const viewed = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: "Viewed access fixture" });
  for (const [project, role] of [[owned, "owner"], [maintained, "maintainer"], [viewed, "viewer"]] as const) {
    await postJSON(page, "/api/v1/project-members", { project_id: project.id, email: "oidc-browser@example.invalid", role });
  }
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    await operator.goto("/settings");
    const card = operator.getByText("Project access", { exact: true }).locator('xpath=ancestor::*[@data-slot="card"]');
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      const project = card.getByRole("combobox", { name: "Project", exact: true });
      await expect(project.locator(`option[value="${owned.id}"]`)).toHaveCount(1);
      await expect(project.locator(`option[value="${maintained.id}"]`)).toHaveCount(0);
      await expect(project.locator(`option[value="${viewed.id}"]`)).toHaveCount(0);
      await expect(card.getByText("oidc-browser@example.invalid · Viewed access fixture", { exact: true })).toBeVisible();
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (width === 320) await card.screenshot({ path: testInfo.outputPath("project-access-owner-320.png") });
    }
    await card.getByRole("combobox", { name: "Project", exact: true }).selectOption(owned.id);
    await card.getByRole("combobox", { name: "Role", exact: true }).selectOption("maintainer");
    const changed = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/project-members");
    await card.getByRole("button", { name: "Update access", exact: true }).click();
    expect((await changed).status()).toBe(200);
    await expect(card.locator(`select[name="project_id"] option[value="${owned.id}"]`)).toHaveCount(0);
    const denied = await operator.request.post("/api/v1/project-members", { headers: { Origin: new URL(operator.url()).origin, "X-NeroCD-CSRF": "1" }, data: { project_id: owned.id, email: "oidc-browser@example.invalid", role: "owner" } });
    expect(denied.status()).toBe(403);
  } finally {
    await context.close();
  }
});

test("run detail selection keeps URL diagnostics and browser history aligned", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID, secondRunID } = await createRunLogFixture(page);
  const diagnostics = page.locator('[aria-labelledby="workflow-diagnostics-title"]');
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/runs/${runID}?q=browser-logs`);
    await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(diagnostics).toContainText(runID);
    const trigger = page.locator(`[data-run-log-trigger="${secondRunID}"]`);
    await (width < 768 ? trigger.first() : trigger.last()).click();
    await expect(page).toHaveURL(new RegExp(`/runs/${secondRunID}\\?q=browser-logs$`));
    await expect(page.getByRole("dialog", { name: `run ${secondRunID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(diagnostics).toContainText(secondRunID);
    await expect(diagnostics).not.toContainText(runID);
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/runs/${runID}\\?q=browser-logs$`));
    await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(diagnostics).toContainText(runID);
    await page.goForward();
    await expect(page.getByRole("dialog", { name: `run ${secondRunID}` })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("dialog", { name: `run ${secondRunID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(diagnostics).toContainText(secondRunID);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("run-detail-history-320.png"), fullPage: true });
  }
});

test("workflow details keep updating after logs close and stop at completion", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  let finished = false;
  let requests = 0;
  await page.route(`**/api/v1/runs/${runID}`, async (route) => {
    requests += 1;
    const response = await route.fetch();
    const run = await response.json();
    await route.fulfill({ response, json: {
      ...run, status: finished ? "succeeded" : "running",
      workflow: { steps: [{ id: "build", name: "Build application", run_spec: { type: "shell", inputs: {} }, requires_ack: false }] },
      workflow_state: { steps: [{ id: "build", name: "Build application", status: finished ? "succeeded" : "running", started_at: "2026-01-01T00:00:00Z", ...(finished ? { finished_at: "2026-01-01T00:00:05Z" } : {}) }] },
    } });
  });
  const diagnostics = page.locator('[aria-labelledby="workflow-diagnostics-title"]');
  for (const width of [320, 390, 430, 1440]) {
    finished = false;
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/runs/${runID}`);
    await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(diagnostics.getByText("Running", { exact: true })).toBeVisible();
    finished = true;
    await expect(diagnostics.getByText("Succeeded", { exact: true })).toBeVisible();
    await expect(diagnostics.getByText("5s", { exact: true })).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 320) await diagnostics.screenshot({ path: testInfo.outputPath("workflow-completed-320.png") });
  }
  const completedRequests = requests;
  // Observe beyond the three-second interval to ensure completed runs stop polling.
  await page.waitForTimeout(3500);
  expect(requests).toBe(completedRequests);
});

test("unavailable run links show a return path without requesting logs", async ({ page, browser }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    const logRequests: string[] = [];
    operator.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/v1/run-logs") logRequests.push(request.url());
    });
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      for (const id of ["run_missing_browser_fixture", runID]) {
        await operator.goto(`/runs/${id}?q=browser&offset=50`);
        await expect(operator.getByRole("heading", { name: "Run unavailable", exact: true })).toBeVisible();
        await expect(operator.getByText("This run does not exist or you do not have access to it.", { exact: true })).toBeVisible();
        await expect(operator.getByRole("dialog")).toHaveCount(0);
        expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        if (width === 320 && id === runID) await operator.screenshot({ path: testInfo.outputPath("run-unavailable-320.png"), fullPage: true });
        await operator.getByRole("link", { name: "Back to runs", exact: true }).click();
        await expect(operator).toHaveURL((url) => url.pathname === "/runs" && url.searchParams.get("q") === "browser" && url.searchParams.get("offset") === "50");
      }
    }
    expect(logRequests).toHaveLength(0);
  } finally {
    await context.close();
  }
});

test("run detail request failures can be retried without losing the route", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  let fail = true;
  await page.route(`**/api/v1/runs/${runID}`, (route) => fail
    ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary run lookup failure" }) })
    : route.continue());
  await page.setViewportSize({ width: 320, height: 1000 });
  await page.goto(`/runs/${runID}?q=browser`);
  await expect(page.getByRole("alert")).toHaveText("Unable to load run. Try again.");
  await page.screenshot({ path: testInfo.outputPath("run-retry-320.png"), fullPage: true });
  await expect(page.getByRole("dialog")).toHaveCount(0);
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/runs/${runID}\\?q=browser$`));
});

test("deployment rollback links include history beyond the first page", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  const response = await page.request.get(`/api/v1/deployments/${fixture.deploymentID}`);
  expect(response.status()).toBe(200);
  const deployment = await response.json();
  const relatedID = "dep_older_rollback_fixture";
  const history = Array.from({ length: 101 }, (_, index) => ({ ...deployment, id: index === 100 ? relatedID : `dep_history_${index}`, status: "failed", rollback_of_id: index === 100 ? fixture.deploymentID : undefined }));
  const offsets: number[] = [];
  await page.route("**/api/v1/deployments?*", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("environment_id") !== fixture.environmentID) return route.continue();
    const offset = Number(url.searchParams.get("offset") ?? 0);
    const limit = Number(url.searchParams.get("limit") ?? 50);
    offsets.push(offset);
    const items = history.slice(offset, offset + limit);
    return route.fulfill({ json: { items, count: items.length, total: history.length, limit, offset } });
  });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/deployments/${fixture.deploymentID}`);
    const related = page.getByRole("link").filter({ hasText: relatedID });
    await expect(related).toBeVisible();
    await expect(related).toHaveAttribute("href", `/deployments/${relatedID}`);
    for (const link of [related, page.getByRole("link").filter({ hasText: deployment.task_run_id })]) {
      const bounds = await link.boundingBox();
      expect(bounds).not.toBeNull();
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
      expect(await link.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("rollback-history-320.png"), fullPage: true });
  }
  expect(offsets).toContain(100);
});

test("deployment log previews show the latest output and follow the linked run", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { deploymentID } = await createDetailFixture(page);
  const deploymentResponse = await page.request.get(`/api/v1/deployments/${deploymentID}`);
  const { task_run_id: runID } = await deploymentResponse.json();
  let appended = false;
  const offsets: number[] = [];
  await page.route("**/api/v1/run-logs?*", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("run_id") !== runID) return route.continue();
    const offset = Number(url.searchParams.get("offset") ?? 0);
    const limit = Number(url.searchParams.get("limit") ?? 100);
    offsets.push(offset);
    const all = Array.from({ length: appended ? 152 : 151 }, (_, index) => ({ id: `log_preview_${index}`, run_id: runID, sequence: index + 1, stream: "stdout", message: `Deployment output ${index + 1}`, created_at: "2026-01-01T00:00:00Z" }));
    const items = all.slice(offset, offset + limit);
    return route.fulfill({ json: { items, count: items.length, total: all.length, limit, offset } });
  });
  await page.setViewportSize({ width: 320, height: 1000 });
  await page.goto(`/deployments/${deploymentID}`);
  const preview = page.getByRole("list", { name: "Deployment run logs", exact: true });
  await expect(preview).toContainText("Deployment output 151");
  await expect(preview.getByRole("listitem")).toHaveCount(20);
  await expect(preview.getByText("Deployment output 100", { exact: true })).toHaveCount(0);
  appended = true;
  await expect(preview).toContainText("Deployment output 152");
  await expect(preview.getByRole("listitem")).toHaveCount(20);
  expect(offsets).toContain(51);
  expect(offsets).toContain(151);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    const bounds = await preview.boundingBox();
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("deployment-live-logs-320.png"), fullPage: true });
  }
});

test("deployment log failures preserve status and controls and can be retried", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { deploymentID } = await createDetailFixture(page);
  let fail = true;
  await page.route("**/api/v1/run-logs?*", (route) => fail
    ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary logs failure" }) })
    : route.continue());
  for (const width of [320, 1440]) {
    fail = true;
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/deployments/${deploymentID}`);
    await expect(page.getByRole("button", { name: "Retry logs", exact: true })).toBeVisible();
    await expect(page.getByText(`Deployment ${deploymentID}`, { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeVisible();
    await expect(page.getByRole("alert")).toHaveText("Unable to load run output.");
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("deployment-log-error-320.png"), fullPage: true });
    fail = false;
    await page.getByRole("button", { name: "Retry logs", exact: true }).click();
    await expect(page.getByText("No run logs have been recorded.", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Retry logs", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeVisible();
  }
});

test("completed run logs recover from failed loading without reopening the dialog", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  await postJSON(page, "/api/v1/runs/cancel", { run_id: runID });
  let fail = true;
  await page.route("**/api/v1/run-logs?*", (route) => fail
    ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Temporary output failure" }) })
    : route.continue());
  for (const width of [320, 1440]) {
    fail = true;
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/runs/${runID}`);
    const dialog = page.getByRole("dialog", { name: `run ${runID}` });
    await expect(dialog.getByRole("button", { name: "Retry logs", exact: true })).toBeEnabled();
    await expect(dialog.getByRole("alert")).toContainText("Unable to load run output");
    const dialogBounds = await dialog.boundingBox();
    const closeBounds = await dialog.getByRole("button", { name: "Close", exact: true }).boundingBox();
    expect(closeBounds!.x + closeBounds!.width).toBeLessThanOrEqual(dialogBounds!.x + dialogBounds!.width);
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("run-log-retry-320.png"), fullPage: true });
    fail = false;
    await dialog.getByRole("button", { name: "Retry logs", exact: true }).click();
    await expect(dialog.getByText("Run canceled by user", { exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Retry logs", exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Following active run output.", { exact: true })).toHaveCount(0);
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toHaveCount(0);
  }
});

test("run Details links retain filtered page context", async ({ page }) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  const response = await page.request.get(`/api/v1/runs/${runID}`);
  const run = await response.json();
  await page.route("**/api/v1/runs?*", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("offset") !== "50") return route.continue();
    return route.fulfill({ json: { items: [run], count: 1, total: 51, offset: 50, limit: 50 } });
  });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/runs?q=browser-logs&offset=50");
    const links = page.locator(`[data-run-detail-link="${runID}"]`);
    const link = width < 768 ? links.first() : links.last();
    const target = new URL((await link.getAttribute("href"))!, page.url());
    expect(target.searchParams.get("q")).toBe("browser-logs");
    expect(target.searchParams.get("offset")).toBe("50");
    await link.click();
    await expect(page).toHaveURL((url) => url.pathname === `/runs/${runID}` && url.searchParams.get("q") === "browser-logs" && url.searchParams.get("offset") === "50");
    await expect(page.getByRole("dialog", { name: `run ${runID}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByText("Showing 51-51 of 51", { exact: true })).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL((url) => url.pathname === "/runs" && url.searchParams.get("q") === "browser-logs" && url.searchParams.get("offset") === "50");
    await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toHaveValue("browser-logs");
  }
});


test("empty run output reflects whether the run can still produce logs", async ({ page }) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  await page.route("**/api/v1/run-logs?*", (route) => {
    const url = new URL(route.request().url());
    return route.fulfill({ json: { items: [], count: 0, total: 0, offset: Number(url.searchParams.get("offset")), limit: Number(url.searchParams.get("limit")) } });
  });
  await page.goto(`/runs/${runID}`);
  const dialog = page.getByRole("dialog", { name: `run ${runID}` });
  await expect(dialog.getByText("waiting for runner output...", { exact: true })).toBeVisible();
  await postJSON(page, "/api/v1/runs/cancel", { run_id: runID });
  await expect(dialog.getByText("No run output was recorded.", { exact: true })).toBeVisible();
  await expect(dialog.getByText("waiting for runner output...", { exact: true })).toHaveCount(0);
  await expect(dialog.getByText("Following active run output.", { exact: true })).toHaveCount(0);
});

test("deployment provenance remains explicit before a revision is resolved", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(`/deployments?service_id=${fixture.serviceID}&environment_id=${fixture.environmentID}`);
    await expect(page.getByRole("option", { name: "main (commit unavailable)", exact: true })).toHaveCount(1);
    await expect(page.getByText(`Commit unavailable · ${fixture.deploymentID}`, { exact: true })).toBeVisible();
    await page.goto(`/deployments/${fixture.deploymentID}`);
    await expect(page.getByText("main (commit unavailable)", { exact: true })).toBeVisible();
    await expect(page.getByText("Not recorded", { exact: true })).toHaveCount(2);
    await expect(page.getByText("None", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("deployment-unresolved-320.png"), fullPage: true });
  }
});


test("deployment detail loading, retry, and missing states preserve a return path", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  let release: (() => void) | undefined;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let fail = true;
  await page.route(`**/api/v1/deployments/${fixture.deploymentID}`, async (route) => {
    await held;
    return fail ? route.fulfill({ status: 503, json: { error: "Temporary deployment failure" } }) : route.continue();
  });
  const search = `?service_id=${fixture.serviceID}&environment_id=${fixture.environmentID}`;
  await page.goto(`/deployments/${fixture.deploymentID}${search}`);
  await expect(page.getByRole("status").filter({ hasText: "Loading deployment" })).toBeVisible();
  await expect(page.getByText("Deployment is unavailable or you do not have access.", { exact: true })).toHaveCount(0);
  release!();
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeEnabled();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Back to deployments", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("deployment-retry-320.png"), fullPage: false });
  }
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByText(`Deployment ${fixture.deploymentID}`, { exact: true })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/deployments/${fixture.deploymentID}`));
  await page.goto(`/deployments/dep_missing${search}`);
  await expect(page.getByText("Deployment is unavailable or you do not have access.", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Back to deployments", exact: true }).click();
  await expect(page).toHaveURL((url) => url.pathname === "/deployments" && url.searchParams.get("service_id") === fixture.serviceID && url.searchParams.get("environment_id") === fixture.environmentID);
});


test("run output filter includes matching live arrivals", async ({ page }, testInfo) => {
  await page.goto("/runs");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const { runID } = await createRunLogFixture(page);
  let appended = false;
  const records = [
    { id: "filter_log_1", run_id: runID, sequence: 1, stream: "stdout", message: "Build started", created_at: "2026-01-01T00:00:00Z" },
    { id: "filter_log_2", run_id: runID, sequence: 2, stream: "stderr", message: "Needle found", created_at: "2026-01-01T00:00:01Z" },
  ];
  await page.route("**/api/v1/run-logs?*", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("run_id") !== runID) return route.continue();
    const offset = Number(url.searchParams.get("offset"));
    const limit = Number(url.searchParams.get("limit"));
    const total = appended ? 2 : 1;
    const items = records.slice(0, total).slice(offset, offset + limit);
    return route.fulfill({ json: { items, count: items.length, total, offset, limit } });
  });
  await page.goto(`/runs/${runID}`);
  const dialog = page.getByRole("dialog", { name: `run ${runID}` });
  const input = dialog.getByRole("searchbox", { name: "Filter loaded output", exact: true });
  await input.fill("needle");
  await expect(dialog.getByText("No loaded output matches this filter.", { exact: true })).toBeVisible();
  appended = true;
  await expect(dialog.getByText("Needle found", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("status")).toHaveText("1 of 2 loaded entries match.");
  await expect(dialog.getByText("Build started", { exact: true })).toHaveCount(0);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(input).toBeVisible();
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("filtered-live-logs-320.png"), fullPage: false });
  }
  await dialog.getByRole("button", { name: "Clear output filter", exact: true }).click();
  await expect(dialog.getByText("Build started", { exact: true })).toBeVisible();
  await expect(input).toBeFocused();
});

test("deployment refresh failure retains details and recovers controls", async ({ page }, testInfo) => {
  await page.goto("/deployments");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const fixture = await createDetailFixture(page);
  let fail = false;
  await page.route(`**/api/v1/deployments/${fixture.deploymentID}`, (route) => fail
    ? route.fulfill({ status: 503, json: { error: "Temporary refresh failure" } })
    : route.continue());
  await page.goto(`/deployments/${fixture.deploymentID}`);
  const title = page.getByText(`Deployment ${fixture.deploymentID}`, { exact: true });
  const cancel = page.getByRole("button", { name: "Cancel", exact: true });
  await expect(title).toBeVisible();
  await expect(cancel).toBeEnabled();
  fail = true;
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeEnabled({ timeout: 15_000 });
  await expect(title).toBeVisible();
  await expect(cancel).toBeDisabled();
  await expect(page.getByText("Unable to refresh deployment. Showing previously loaded data.", { exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(title).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("deployment-refresh-retained-320.png"), fullPage: true });
  }
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(cancel).toBeEnabled();
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toHaveCount(0);
  await expect(title).toBeVisible();
});

test("page code loads on demand during navigation", async ({ page }) => {
  const operationsChunks: number[] = [];
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("response", (response) => {
    if (/\/assets\/_authenticated\.operations-[^/]+\.js$/.test(new URL(response.url()).pathname)) operationsChunks.push(response.status());
  });
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
  expect(operationsChunks).toEqual([]);
  await page.getByRole("link", { name: "Operations", exact: true }).click();
  await expect(page.getByRole("heading", { name: "System Operations", exact: true })).toBeVisible();
  expect(operationsChunks).toEqual([200]);
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("failed page downloads offer reload recovery at the same URL", async ({ page }, testInfo) => {
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
  let fail = true;
  await page.route("**/assets/_authenticated.operations-*.js", (route) => fail ? route.abort("failed") : route.continue());
  await page.goto("/operations");
  const reload = page.getByRole("button", { name: "Reload page", exact: true });
  await expect(reload).toBeVisible();
  await expect(page.getByText("This page could not be loaded. Reload it to try again.", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/operations$/);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(reload).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  }
  await page.setViewportSize({ width: 320, height: 800 });
  await page.screenshot({ path: testInfo.outputPath("page-download-recovery-320.png"), fullPage: false });
  fail = false;
  await reload.click();
  await expect(page.getByRole("heading", { name: "System Operations", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/operations$/);
});

test("unknown URLs provide a usable return to the dashboard", async ({ page }, testInfo) => {
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
  for (const path of ["/missing-page", "/runners/missing/extra", "/deployments/missing/extra"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: "Page not found", exact: true })).toBeVisible();
    const dashboard = page.getByRole("link", { name: "Go to dashboard", exact: true });
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(dashboard).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
      if (path === "/missing-page" && width === 320) await page.screenshot({ path: testInfo.outputPath("unknown-page-320.png"), fullPage: false });
    }
    await dashboard.click();
    await expect(page).toHaveURL((url) => url.pathname === "/");
    await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  }
});

test("saved theme applies to sign-in, unknown routes, and account toggles", async ({ page }) => {
  await page.goto("/missing-theme-page");
  await page.evaluate(() => localStorage.setItem("nerocd.theme.v2", "dark"));
  await page.reload();
  await expect(page.getByRole("heading", { name: "Page not found", exact: true })).toBeVisible();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.goto("/sign-in");
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  await page.goto("/runners");
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Switch to light mode", exact: true }).click();
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  expect(await page.evaluate(() => localStorage.getItem("nerocd.theme.v2"))).toBe("light");
  await page.goto("/missing-theme-page");
  await expect(page.getByRole("heading", { name: "Page not found", exact: true })).toBeVisible();
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  await page.getByRole("link", { name: "Go to dashboard", exact: true }).click();
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Switch to dark mode", exact: true }).click();
  await page.goto("/missing-theme-page");
  await expect(page.locator("html")).toHaveClass(/dark/);
});

test("keyboard users can skip shell navigation to page content", async ({ page }, testInfo) => {
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/runners?q=keyboard");
    await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
    await page.keyboard.press("Tab");
    const skip = page.getByRole("link", { name: "Skip to page content", exact: true });
    await expect(skip).toBeFocused();
    await expect(skip).toBeInViewport();
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("skip-navigation-320.png"), fullPage: false });
    await page.keyboard.press("Enter");
    await expect(page.getByRole("main", { name: "Runners", exact: true })).toBeFocused();
    await expect(page).toHaveURL((url) => url.pathname === "/runners" && url.searchParams.get("q") === "keyboard" && !url.hash);
    await page.keyboard.press("Tab");
    await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toBeFocused();
  }
});

test("browser titles follow navigation and history", async ({ page }) => {
  await page.goto("/sign-in/");
  await expect(page).toHaveTitle("Sign in · NeroCD");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  await expect(page).toHaveTitle("Home · NeroCD");
  await page.getByRole("link", { name: "Runners", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Runners", exact: true })).toBeVisible();
  await expect(page).toHaveTitle("Runners · NeroCD");
  await page.getByRole("link", { name: "Runs", exact: true }).click();
  await expect(page).toHaveTitle("Runs · NeroCD");
  await page.goBack();
  await expect(page).toHaveTitle("Runners · NeroCD");
  await page.goto("/unknown-title-page");
  await expect(page.getByRole("heading", { name: "Page not found", exact: true })).toBeVisible();
  await expect(page).toHaveTitle("Page not found · NeroCD");
});
