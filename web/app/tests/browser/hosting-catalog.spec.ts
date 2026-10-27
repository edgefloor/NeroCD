import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { randomBytes } from "node:crypto";

test.use({ trace: "off", screenshot: "off" });
test("owner checks catalog requirements and destinations before installing with identical retry", async ({ page }, testInfo) => {
  test.setTimeout(60_000);
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = randomBytes(6).toString("hex");
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Catalog ${suffix}` });
  await postJSON(page, "/api/v1/project-environments", { project_id: project.id, name: "Catalog environment" });
  const runnerID = `runner_catalog_${suffix}`;
  const enrollment = await postJSON<{ token: string }>(page, "/api/v1/runner-enrollments", {
    runner_id: runnerID, runner_name: "Catalog browser fixture", capabilities: ["compose-deploy", "image_deploy", "hosting_owned_storage_v1", "hosting_owned_storage_runtime_v1", "hosting_runtime_env_v1", "image_release_ports_v1", "hosting_managed_compose_v1"],
  });
  await postJSON(page, "/api/v1/runner-enrollments/consume", {
    request_id: `enroll_consume_${randomBytes(16).toString("hex")}`, credential_hash: randomBytes(32).toString("hex"),
  }, { Authorization: `Bearer ${enrollment.token}` });
  const server = await postJSON<{ destination: { id: string } }>(page, "/api/v1/servers", { name: "Catalog fixture server", runner_id: runnerID });
  await postJSON(page, `/api/v1/destinations/${server.destination.id}/project-grants`, { project_id: project.id });
  const limitedEnrollment = await postJSON<{ token: string }>(page, "/api/v1/runner-enrollments", {
    runner_id: `runner_catalog_limited_${suffix}`, runner_name: "Catalog missing features", capabilities: ["compose-deploy", "image_deploy"],
  });
  await postJSON(page, "/api/v1/runner-enrollments/consume", {
    request_id: `enroll_consume_${randomBytes(16).toString("hex")}`, credential_hash: randomBytes(32).toString("hex"),
  }, { Authorization: `Bearer ${limitedEnrollment.token}` });
  const limitedServer = await postJSON<{ destination: { id: string } }>(page, "/api/v1/servers", {
    name: "Catalog missing features server", runner_id: `runner_catalog_limited_${suffix}`,
  });
  await postJSON(page, `/api/v1/destinations/${limitedServer.destination.id}/project-grants`, { project_id: project.id });
  const installationRequests: unknown[] = [];
  page.on("request", request => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/hosting-catalog/installations") {
      installationRequests.push(request.postDataJSON());
    }
  });
  let failCatalog = true;
  await page.route("**/api/v1/hosting-catalog?*", route => {
    if (failCatalog) return route.fulfill({ status: 503, json: { error: "fixture unavailable" } });
    return route.continue();
  });
  await page.reload();
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(project.id);
  await hosting.getByRole("button", { name: "Catalog environment", exact: true }).click();
  const catalog = page.getByRole("region", { name: "Hosting catalog", exact: true });
  await expect(catalog.getByText("Catalog or destination access is unavailable.")).toBeVisible();
  failCatalog = false;
  await catalog.getByRole("button", { name: "Retry catalog loading", exact: true }).click();
  await expect(catalog.getByRole("option", { name: /Linkding ·/ })).toBeAttached();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await catalog.getByLabel("Catalog category", { exact: true }).selectOption("rss");
    await expect(catalog.getByRole("status")).toContainText("Catalog matches: 1 of 4.");
    await catalog.getByLabel("Search catalog", { exact: true }).fill("no-such-application");
    await expect(catalog.getByText(/No applications match your search/)).toBeVisible();
    await expect(catalog.getByRole("button", { name: "Install application", exact: true })).toBeDisabled();
    await catalog.getByRole("button", { name: "Clear search", exact: true }).focus();
    await page.keyboard.press("Enter");
    await catalog.getByLabel("Search catalog", { exact: true }).fill(" LINKDING  LD_SUPERUSER_PASSWORD ");
    await catalog.getByLabel("Catalog application", { exact: true }).selectOption("linkding");
    const preview = catalog.getByRole("region", { name: "Catalog installation preview", exact: true });
    await expect(preview).toContainText("/etc/linkding/data");
    await expect(preview).toContainText("CategoryRSS");
    await expect(preview).toContainText("Upstream architecture restrictionNot declared");
    const requirements = preview.getByRole("region", { name: "Catalog application requirements", exact: true });
    await expect(requirements).toContainText("Standalone Docker with a managed application container");
    await expect(requirements).toContainText("Destination architecture is unverified.");
    await expect(requirements).toContainText("Available server capacity is unverified.");
    await expect(requirements).toContainText("/etc/linkding/data");
    const destination = catalog.getByLabel("Catalog destination", { exact: true });
    await expect(destination.locator(`option[value="${limitedServer.destination.id}"]`)).toBeDisabled();
    await expect(destination.locator(`option[value="${server.destination.id}"]`)).toBeEnabled();
    await expect(catalog.getByRole("list", { name: "Catalog destination requirements", exact: true }))
      .toContainText("Required deployment features are disabled.");
    await expect(catalog.getByRole("button", { name: "Install application", exact: true })).toBeDisabled();
    expect(installationRequests).toHaveLength(0);
    await catalog.getByRole("button", { name: "Clear filters", exact: true }).focus();
    await page.keyboard.press("Enter");
    await expect(catalog.getByLabel("Catalog category", { exact: true })).toHaveValue("");
    await expect(catalog.getByLabel("Catalog application", { exact: true })).toHaveValue("");
    await expect(catalog.getByRole("button", { name: "Install application", exact: true })).toBeDisabled();
    await catalog.getByLabel("Catalog category", { exact: true }).selectOption("rss");
    await catalog.getByLabel("Catalog application", { exact: true }).selectOption("linkding");
    await expect(preview).toContainText("LD_SUPERUSER_NAME, LD_SUPERUSER_PASSWORD");
    await expect(preview).toContainText("/health, timeout 120 seconds");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await catalog.screenshot({ path: testInfo.outputPath(`catalog-preview-${width}.png`), animations: "disabled" });
  }
  await catalog.getByRole("button", { name: "Clear filters", exact: true }).click();
  await catalog.getByLabel("Catalog application", { exact: true }).selectOption("vikunja-with-postgresql");
  const composeRequirements = catalog.getByRole("region", { name: "Catalog application requirements", exact: true });
  await expect(composeRequirements).toContainText("Standalone Docker with managed Compose services");
  await expect(composeRequirements).toContainText("Required during installation. Configure a matching route separately.");
  await expect(composeRequirements).toContainText("SERVICE_PASSWORD_JWTSECRET");
  await expect(catalog.getByLabel("Public URL", { exact: true })).toBeVisible();
  await expect(catalog.getByRole("button", { name: "Install application", exact: true })).toBeDisabled();
  expect(installationRequests).toHaveLength(0);
  await catalog.getByLabel("Catalog application", { exact: true }).selectOption("linkding");
  await catalog.getByLabel("Installation name", { exact: true }).fill(`Search install ${suffix}`);
  await catalog.getByLabel("Catalog destination", { exact: true }).selectOption(server.destination.id);
  let committedReceipt: { id: string } | undefined;
  await page.route("**/api/v1/hosting-catalog/installations", async route => {
    if (!committedReceipt) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(201);
      committedReceipt = await committed.json() as { id: string };
      await route.abort("failed");
      return;
    }
    await route.continue();
  });
  await catalog.getByRole("button", { name: "Install application", exact: true }).click();
  const retry = catalog.getByRole("button", { name: "Retry same installation", exact: true });
  await expect(retry).toBeEnabled();
  expect(installationRequests).toHaveLength(1);
  const installedResponse = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/hosting-catalog/installations");
  await retry.click();
  const installed = await installedResponse;
  expect(installed.status()).toBe(201);
  const body = await installed.json();
  expect(installationRequests).toHaveLength(2);
  expect(installationRequests[1]).toEqual(installationRequests[0]);
  expect(body.id).toBe(committedReceipt?.id);
  const persistedResponse = await page.request.get(`/api/v1/hosting-catalog/installations/${body.id}`);
  expect(persistedResponse.status()).toBe(200);
  const persisted = await persistedResponse.json();
  expect(persisted.id).toBe(body.id);
  expect(persisted.resource.id).toBe(body.resource.id);
  expect(persisted.request).toEqual(body.request);
  expect(body.request.template_id).toBe("linkding");
  expect(body.request.immutable_version).toBe("coolify-b12d1386.linkding-1.47.0.v1");
  await expect(catalog.getByRole("status")).toContainText(`Installed Search install ${suffix}`);
  await expect(catalog.locator("output")).toHaveCount(0);
});

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

function bootstrapAdmin(): void {
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
  bootstrapAdmin();
  await page.reload({ waitUntil: "networkidle" });
  await expect(page.getByLabel("Email")).toBeVisible();
}

async function postJSON<T>(page: Page, path: string, data: unknown, headers: Record<string, string> = {}): Promise<T> {
  const response = await page.request.post(path, { data, headers: { Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1", ...headers } });
  if (!response.ok()) throw new Error(`browser fixture request ${path} failed with ${response.status()}`);
  return await response.json() as T;
}
