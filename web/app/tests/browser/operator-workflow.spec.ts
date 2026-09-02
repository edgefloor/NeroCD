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
  if (!response.ok()) throw new Error(`browser fixture request ${path} failed with ${response.status()}`);
  return await response.json() as T;
}

async function createDetailFixture(page: Page): Promise<{ deploymentID: string }> {
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
  return { deploymentID: deployment.id };
}

test("browser observes CLI-only bootstrap guidance before a supported CLI bootstrap", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("status", { name: "Administrator bootstrap required" })).toBeVisible();
  await expect(page.getByText("Bootstrap is intentionally CLI-only.")).toBeVisible();
  await expect(page.getByLabel("Email")).toHaveCount(0);
  await expect(page.getByLabel("Password")).toHaveCount(0);
  await ensureBootstrapped(page);
  await expect(page.getByLabel("Email")).toHaveValue("");
  await expect(page.getByLabel("Password")).toHaveValue("");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  await expect(page.locator("h1")).toBeVisible();
});

test("authenticated navigation and sign out work with runtime-only credentials", async ({ page }) => {
  await page.goto("/projects");
  await expect(page).toHaveURL(/\/sign-in\?redirect=/);
  await signIn(page);
  await expect(page).toHaveURL(/\/projects/);
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.locator("h1")).toBeVisible();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
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

test("mobile shell keeps primary navigation usable and safely contained", async ({ page }) => {
  await page.goto("/");
  await signIn(page);
  const primaryNavigation = page.getByRole("navigation", { name: "Primary navigation" });
  const actionNames = ["Home", "Runs", "Deployments", "Runners", "Approvals"];

  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(primaryNavigation).toBeVisible();
    await expect(primaryNavigation.getByRole("button")).toHaveCount(5);
    for (const name of actionNames) {
      await expect(primaryNavigation.getByRole("button", { name })).toBeVisible();
    }

    const mobileMetrics = await page.evaluate(() => {
      const navigation = document.querySelector<HTMLElement>(".mobile-bottom-navigation");
      const content = document.querySelector<HTMLElement>(".mobile-shell-content");
      if (!navigation || !content) throw new Error("mobile shell is unavailable");
      const itemTops = Array.from(navigation.querySelectorAll<HTMLElement>("button"), (button) => button.getBoundingClientRect().top);
      return {
        contentPaddingBottom: Number.parseFloat(window.getComputedStyle(content).paddingBottom),
        documentClientWidth: document.documentElement.clientWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        navigationHeight: navigation.getBoundingClientRect().height,
        itemTops,
      };
    });
    expect(mobileMetrics.documentScrollWidth).toBeLessThanOrEqual(mobileMetrics.documentClientWidth);
    expect(mobileMetrics.contentPaddingBottom).toBeGreaterThanOrEqual(mobileMetrics.navigationHeight);
    expect(new Set(mobileMetrics.itemTops).size).toBe(1);
  }

  expect(await page.locator('meta[name="viewport"]').getAttribute("content")).toContain("viewport-fit=cover");

  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  const drawer = page.getByRole("dialog", { name: "Mobile navigation" });
  await expect(drawer).toBeVisible();
  const drawerMetrics = await drawer.evaluate((element) => {
    const drawer = element as HTMLElement;
    const rect = drawer.getBoundingClientRect();
    const style = window.getComputedStyle(drawer);
    const alpha = style.backgroundColor.match(/^rgba\\([^,]+,[^,]+,[^,]+,\\s*([^)]+)\\)$/)?.[1];
    return {
      backgroundColor: style.backgroundColor,
      backgroundAlpha: alpha === undefined ? 1 : Number(alpha),
      bottom: rect.bottom,
      overflowY: style.overflowY,
      right: rect.right,
      viewportHeight: window.innerHeight,
      viewportWidth: window.innerWidth,
    };
  });
  expect(drawerMetrics.backgroundColor).not.toBe("transparent");
  expect(drawerMetrics.backgroundAlpha).toBe(1);
  expect(drawerMetrics.overflowY).toBe("auto");
  expect(drawerMetrics.right).toBeLessThanOrEqual(drawerMetrics.viewportWidth);
  expect(drawerMetrics.bottom).toBeLessThanOrEqual(drawerMetrics.viewportHeight);

  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 1024, height: 844 });
  await expect(primaryNavigation).toBeHidden();
  await expect(page.getByRole("button", { name: "Open mobile navigation" })).toBeHidden();
  await expect(page.getByRole("navigation", { name: "Desktop navigation" })).toBeVisible();
});

test("runner enrollment stays local to the reveal dialog and browser avoids runner-only APIs", async ({ page }) => {
  const forbiddenRunnerCalls: string[] = [];
  await page.route("**/api/v1/runners/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (/^\/api\/v1\/runners\/[^/]+$/.test(path) || path === "/api/v1/runners/revoke-token") return route.continue();
    forbiddenRunnerCalls.push(path); await route.abort();
  });
  await page.goto("/runners");
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Runners" })).toBeVisible();
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  await page.getByLabel("Runner ID").fill(`runner_browser_${suffix}`);
  await page.getByLabel("Runner name").fill("Browser enrollment runner");
  const creation = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/runner-enrollments" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Create enrollment" }).click();
  expect((await creation).status()).toBe(201);
  const dialog = page.getByRole("dialog", { name: "One-time runner enrollment secret" });
  await expect(dialog).toBeVisible();
  const secret = await dialog.getByLabel("Enrollment secret").innerText();
  expect(secret).toMatch(/^nce_[0-9a-f]{64}$/);
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
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
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
  const commandInput = page.getByPlaceholder("Type a command or search...");
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
