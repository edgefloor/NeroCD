import { expect, test, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
function runtimeDir(): string {
  const id = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof id !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(id)) throw new Error("Missing owned browser runtime");
  return join("/tmp", `nerocd-browser-${id}`);
}
async function signIn(page: Page) {
  await page.goto("/");
  const status = await page.request.get("/api/v1/bootstrap-status");
  expect(status.ok()).toBe(true);
  const [email, password] = readFileSync(join(runtimeDir(), "credentials"), "utf8").trim().split("\n");
  if ((await status.json()).status === "required") {
    execFileSync(join(runtimeDir(), "nerocd"), ["bootstrap-admin", "--email", email!, "--name", "Browser Administrator", "--password-file", join(runtimeDir(), "password")], { env: { ...process.env, NEROCD_DATABASE_URL: readFileSync(join(runtimeDir(), "database-url"), "utf8") }, stdio: "pipe" });
    await page.reload();
  }
  await page.getByLabel("Email", { exact: true }).fill(email!); await page.getByLabel("Password", { exact: true }).fill(password!); await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function navigation(page: Page, width: number) {
  if (width < 1024) { await page.getByRole("button", { name: "Open mobile navigation" }).click(); return page.getByRole("navigation", { name: "Mobile navigation", exact: true }); }
  return page.getByRole("navigation", { name: "Desktop navigation", exact: true });
}
test("compiled Go UI leads with hosting links and preserves navigation, keyboard access, history and deep links", async ({ page }, info) => {
  test.setTimeout(90_000); await signIn(page);
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await page.goto("/");
    await expect(page.getByRole("heading", { name: "Host an application", exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Connect a server", exact: true })).toHaveAttribute("href", "/runners");
    await expect(page.getByRole("link", { name: "Configure an application", exact: true })).toHaveAttribute("href", "/projects");
    await page.screenshot({ path: info.outputPath(`hosting-home-${width}.png`), fullPage: true });
    const nav = await navigation(page, width); expect((await nav.getByRole("link").allTextContents()).slice(0, 3)).toEqual(["Home", "Projects", "Servers and runners"]);
    await page.screenshot({ path: info.outputPath(`hosting-navigation-${width}.png`), fullPage: false });
    await nav.getByRole("link", { name: "Servers and runners", exact: true }).click();
    await expect(page).toHaveURL(/\/runners$/); await expect(page).toHaveTitle("Servers and runners · NeroCD");
    await expect(page.getByRole("heading", { name: "Servers and runners", exact: true })).toBeVisible(); await page.screenshot({ path: info.outputPath(`hosting-servers-${width}.png`), fullPage: false }); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const currentNav = await navigation(page, width); await expect(currentNav.getByRole("link", { name: "Servers and runners", exact: true })).toHaveAttribute("aria-current", "page"); if (width < 1024) await page.keyboard.press("Escape");
    await page.goBack(); await expect(page.getByRole("heading", { name: "Host an application", exact: true })).toBeVisible();
    await page.getByRole("link", { name: "Configure an application", exact: true }).click(); const hosting = page.getByRole("region", { name: "Project hosting", exact: true }); await expect(hosting).toBeVisible();
    expect(await hosting.evaluate((node) => node.parentElement?.firstElementChild === node)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`hosting-projects-${width}.png`), fullPage: true });
    await page.goto("/runners?q=hosting-deep-link"); await expect(page.getByRole("searchbox", { name: "Filter this page", exact: true })).toHaveValue("hosting-deep-link");
    await page.keyboard.press("Tab"); await expect(page.getByRole("link", { name: "Skip to page content", exact: true })).toBeFocused(); await page.keyboard.press("Enter"); await expect(page.getByRole("main", { name: "Servers and runners", exact: true })).toBeFocused();
    const search = page.getByRole("button", { name: "Search navigation", exact: true }); await search.click(); const input = page.getByPlaceholder("Find a page or action...");
    await input.fill("server"); await expect(page.getByRole("option", { name: /Servers and runners/ })).toBeVisible(); await input.fill("runner"); await expect(page.getByRole("option", { name: /Servers and runners/ })).toBeVisible(); await page.keyboard.press("Escape"); await expect(search).toBeFocused();
  }
});
// These intercepts deliberately simulate legacy lookup faults. Authentication,
// routing, assets and normal hosting controls still use the compiled Go fixture.
test("synthetic pending and unavailable legacy lookup leaves real hosting navigation usable", async ({ page }) => {
  test.setTimeout(45_000); await signIn(page);
  let release!: () => void; const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/v1/templates", async (route) => { await pending; try { await route.continue(); } catch { /* navigation retired the delayed read */ } });
  await page.goto("/", { waitUntil: "domcontentloaded" }); await expect(page.getByRole("heading", { name: "Host an application" })).toBeVisible(); await expect(page.getByLabel("Loading dashboard")).toBeVisible();
  await page.getByRole("link", { name: "Connect a server", exact: true }).click(); await expect(page).toHaveURL(/\/runners$/); release(); await page.unroute("**/api/v1/templates");
  await page.route("**/api/v1/templates", (route) => route.fulfill({ status: 503, json: { error: "Synthetic legacy service failure" } }));
  await page.goto("/", { waitUntil: "domcontentloaded" }); await expect(page.getByText("Dashboard unavailable", { exact: true })).toBeVisible({ timeout: 16_000 });
  await expect(page.getByRole("heading", { name: "Host an application" })).toBeVisible(); await page.getByRole("link", { name: "Configure an application", exact: true }).click(); await expect(page).toHaveURL(/\/projects$/);
});
for (const status of [401, 403]) {
  test(`synthetic legacy HTTP ${status} remains an access failure without hosting guidance or activity`, async ({ page }) => {
    await signIn(page); await page.route("**/api/v1/templates", (route) => route.fulfill({ status, json: { error: "Synthetic access failure" } })); await page.reload();
    await expect(page.getByText("Dashboard unavailable", { exact: true })).toBeVisible(); expect(await page.getByRole("heading", { name: "Host an application" }).count()).toBe(0); expect(await page.getByRole("heading", { name: "Advanced automation activity" }).count()).toBe(0);
  });
  test(`principal HTTP ${status} preserves session recovery without hosting guidance`, async ({ page }) => {
    await signIn(page); await page.route("**/api/v1/me", (route) => route.fulfill({ status, json: { error: "Synthetic current-principal failure" } })); await page.reload();
    if (status === 401) await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible(); else await expect(page.getByRole("heading", { name: "Session unavailable", exact: true })).toBeVisible();
    expect(await page.getByRole("heading", { name: "Host an application" }).count()).toBe(0); expect(await page.getByRole("heading", { name: "Advanced automation activity" }).count()).toBe(0);
  });
}


test("compiled Go serves the exact frontend artifact bytes", async ({ request }, info) => {
  const dist = join(process.cwd(), "../dist");
  const paths = (prefix = ""): string[] => readdirSync(join(dist, prefix), { withFileTypes: true }).flatMap((entry) => entry.isDirectory() ? paths(`${prefix}${entry.name}/`) : [`${prefix}${entry.name}`]);
  const hashes: Record<string, string> = {};
  for (const path of paths().filter((path) => path !== ".gitkeep").sort()) {
    const response = await request.get(path === "index.html" ? "/" : `/${path}`); expect(response.ok(), path).toBe(true);
    const actual = createHash("sha256").update(await response.body()).digest("hex"); const expected = createHash("sha256").update(readFileSync(join(dist, path))).digest("hex"); expect(actual, path).toBe(expected); hashes[path] = actual;
  }
  await info.attach("served-ui-sha256.json", { body: Buffer.from(JSON.stringify({ files: hashes }, null, 2)), contentType: "application/json" });
});

test("project-only operator retains hosting navigation without server administration or Operations", async ({ browser }) => {
  const context = await browser.newContext();
  try {
    const page = await context.newPage(); await page.goto("/sign-in"); await page.getByRole("link", { name: "Continue with SSO", exact: true }).click(); await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    const principal = await (await page.request.get("/api/v1/me")).json(); expect(principal.roles).not.toContain("system_admin"); expect(principal.roles).not.toContain("runner_admin");
    for (const width of [320, 1440]) {
      await page.setViewportSize({ width, height: 1000 }); const nav = await navigation(page, width); await expect(nav.getByRole("link", { name: "Servers and runners", exact: true })).toBeVisible(); expect(await nav.getByRole("link", { name: "Operations", exact: true }).count()).toBe(0); await nav.getByRole("link", { name: "Servers and runners", exact: true }).click();
      await expect(page.getByText("Runner inventory is unavailable.", { exact: true })).toBeVisible(); expect(await page.getByRole("button", { name: "Create enrollment", exact: true }).count()).toBe(0); expect(await page.getByRole("region", { name: "Hosting servers", exact: true }).count()).toBe(0);
    }
  } finally { await context.close(); }
});

// A synthetic principal transition checks client cache isolation. The server's
// real session remains the fixture administrator throughout this test.
test("synthetic actor and role change cannot revive cached automation activity", async ({ page }) => {
  await signIn(page); await expect(page.getByRole("heading", { name: "Advanced automation activity", exact: true })).toBeVisible();
  let release!: () => void; const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/v1/templates", async (route) => { await pending; try { await route.continue(); } catch { /* old actor read was retired */ } });
  let otherActor = true;
  await page.route("**/api/v1/me", async (route) => { const response = await route.fetch(); const principal = await response.json(); await route.fulfill({ response, json: otherActor ? { ...principal, id: "synthetic-other-actor", roles: [] } : principal }); });
  try {
    await page.getByRole("button", { name: "Account", exact: true }).click(); await page.getByRole("button", { name: "Refresh data", exact: true }).click(); await expect(page.getByLabel("Loading dashboard")).toBeVisible(); expect(await page.getByRole("heading", { name: "Advanced automation activity", exact: true }).count()).toBe(0);
    otherActor = false; await page.getByRole("button", { name: "Account", exact: true }).click(); await page.getByRole("button", { name: "Refresh data", exact: true }).click(); await expect(page.getByLabel("Loading dashboard")).toBeVisible(); expect(await page.getByRole("heading", { name: "Advanced automation activity", exact: true }).count()).toBe(0);
  } finally { release(); }
});
