import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { randomBytes } from "node:crypto";

test.use({ trace: "off", screenshot: "off" });
test("owner searches catalog previews settings and installs after loading recovers", async ({ page }, testInfo) => {
  test.setTimeout(60_000);
  await page.goto("/projects");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  const suffix = randomBytes(6).toString("hex");
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Catalog ${suffix}` });
  await postJSON(page, "/api/v1/project-environments", { project_id: project.id, name: "Catalog environment" });
  const runnerID = `runner_catalog_${suffix}`;
  const enrollment = await postJSON<{ token: string }>(page, "/api/v1/runner-enrollments", {
    runner_id: runnerID, runner_name: "Catalog browser fixture", capabilities: ["compose-deploy", "image-deploy"],
  });
  await postJSON(page, "/api/v1/runner-enrollments/consume", {
    request_id: `enroll_consume_${randomBytes(16).toString("hex")}`, credential_hash: randomBytes(32).toString("hex"),
  }, { Authorization: `Bearer ${enrollment.token}` });
  const server = await postJSON<{ destination: { id: string } }>(page, "/api/v1/servers", { name: "Catalog fixture server", runner_id: runnerID });
  await postJSON(page, `/api/v1/destinations/${server.destination.id}/project-grants`, { project_id: project.id });
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
    await expect(catalog.getByRole("status")).toContainText("Catalog matches: 1 of 1.");
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
    await expect(preview).toContainText("does not verify compatibility with the selected destination");
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
  await catalog.getByLabel("Installation name", { exact: true }).fill(`Search install ${suffix}`);
  await catalog.getByLabel("Catalog destination", { exact: true }).selectOption(server.destination.id);
  const installedResponse = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/hosting-catalog/installations");
  await catalog.getByRole("button", { name: "Install application", exact: true }).click();
  const installed = await installedResponse;
  expect(installed.status()).toBe(201);
  const body = await installed.json();
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
