import { expect, test, type Locator, type Page } from "@playwright/test";

// Actual cookie API, River and compiled runner only. No request interception,
// synthetic runner receipt, secret reveal or account-provisioning qualification.
test.use({ trace: "off", screenshot: "off", video: "off" });
type Metadata = { origin: string; engine: string; project_id: string; environment: string; application_resource_id: string; application_name: string; database_resource_id: string; foreign_resource_id: string; control_path: string };
type Progress = { phase: string; revision: number; operation_id: string };
type PublicRouteProof = { route_id: string; http_ready: boolean; https_ready: boolean; certificate_status: string; selected_release_operation_id: string; https_verified: boolean };
const raw = process.env.NEROCD_DATABASE_CONNECTION_BROWSER_META;
const meta = raw ? JSON.parse(raw) as Metadata : undefined;
const headers = (m: Metadata) => ({ Origin: m.origin, "X-NeroCD-CSRF": "1" });
async function request(page: Pick<Page, "request">, method: "get" | "post", path: string, options: Parameters<Page["request"]["post"]>[1] = {}) {
  try { return await page.request[method](path, { ...options, timeout: options.timeout ?? 5000 }); }
  catch { throw new Error(`live browser fixture ${method.toUpperCase()} request failed`); }
}
async function reportFailure(page: Pick<Page, "request">, m: Metadata) {
  const response=await request(page,"post",m.control_path+"/failure",{headers:headers(m)});
  if(response.status()!==204) throw new Error(`live browser failure diagnostic acknowledgement status ${response.status()}`);
}
test("browser fixture transport diagnostics discard cookie-bearing errors", async () => {
  for (const method of ["get", "post"] as const) {
    let timeout: number | undefined;
    const failing = { request: { [method]: async (_path: string, options: { timeout: number }) => { timeout = options.timeout; throw new Error("Cookie: SENSITIVE-COOKIE-CANARY"); } } } as unknown as Page;
    await expect(request(failing, method, "/fixture")).rejects.toThrow(new RegExp(`^live browser fixture ${method.toUpperCase()} request failed$`));
    expect(timeout).toBe(5000);
  }
  const denied = { status: () => 403 };
  const available = { request: { get: async () => denied } } as unknown as Page;
  expect(await request(available, "get", "/fixture")).toBe(denied);
});
test("browser failure waits for the diagnostic acknowledgement and rejects denial", async () => {
  const events:string[]=[];
  const m={origin:"http://127.0.0.1:18080",control_path:"/api/v1/fixture/database-connection/"+"a".repeat(32)} as Metadata;
  const reporting={request:{post:async(path:string,options:{headers:Record<string,string>;timeout:number})=>{
    expect(path).toBe(m.control_path+"/failure");expect(options.headers.Origin).toBe(m.origin);expect(options.timeout).toBe(5000);
    events.push("diagnostic_emitted");return {status:()=>204};
  }}} as unknown as Page;
  await reportFailure(reporting,m);events.push("cleanup_allowed");
  expect(events).toEqual(["diagnostic_emitted","cleanup_allowed"]);
  const denied={request:{post:async()=>({status:()=>403})}} as unknown as Page;
  await expect(reportFailure(denied,m)).rejects.toThrow("live browser failure diagnostic acknowledgement status 403");
});
async function login(page: Page, viewer = false) {
  const bootstrap = await request(page, "get", "/api/v1/bootstrap-status");
  expect(bootstrap.status(), "live installation bootstrap status must be available before login").toBe(200);
  expect(await bootstrap.json(), "live installation must retain its bootstrap administrator").toEqual({ status: "complete" });
  await page.goto("/sign-in", { waitUntil: "domcontentloaded", timeout: 15_000 });
  await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "admin@example.local", { timeout: 10_000 });
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "admin", { timeout: 10_000 });
  await page.getByRole("button", { name: "Sign in", exact: true }).click({ timeout: 10_000 });
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible({ timeout: 15_000 });
}
async function panel(page: Page, m: Metadata) {
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption(m.project_id);
  await hosting.getByRole("button", { name: m.environment, exact: true }).click();
  const connection = hosting.getByRole("region", { name: `Database connection for ${m.application_name}`, exact: true });
  await expect(connection).toBeVisible(); return connection;
}
async function progress(page: Page, m: Metadata, phase: string) {
  let value!: Progress;
  await expect.poll(async () => {
    const response = await request(page, "get", m.control_path + "/state", { headers: headers(m) });
    if (response.status() !== 200) return "unavailable";
    value = await response.json() as Progress; return value.phase;
  }, { timeout: 180_000, intervals: [100, 250, 500] }).toBe(phase);
  expect(Number.isSafeInteger(value.revision)).toBe(true); expect(value.revision).toBeGreaterThanOrEqual(2);
  return value;
}
async function settled(connection: Locator, state: Progress) {
  await connection.getByRole("button", { name: "Refresh database connection", exact: true }).click();
  await expect(connection.getByText("Connection workflow completed", { exact: true })).toBeVisible({ timeout: 30_000 });
  await expect(connection.locator("dt").filter({ hasText: /^Membership revision$/ }).locator("+ dd")).toHaveText(String(state.revision));
  await expect(connection.getByText(/Changes are paused for operation/)).toHaveCount(0);
}
async function proof(page: Page, m: Metadata, state: Progress, count: number) {
  const response = await request(page, "get", m.control_path + "/proof/" + state.operation_id, { headers: headers(m) });
  expect(response.status()).toBe(200);
  const body = await response.json() as { verified: boolean; container_id: string; record_count: number; route?: PublicRouteProof };
  expect(body.verified).toBe(true); expect(body.container_id).toMatch(/^[a-f0-9]{64}$/); expect(body.record_count).toBe(count);
  if (body.route) {
    expect(body.route.route_id).not.toBe(""); expect(body.route.http_ready).toBe(true); expect(body.route.https_ready).toBe(true);
    expect(body.route.certificate_status).toBe("trusted"); expect(body.route.https_verified).toBe(true);
    expect(body.route.selected_release_operation_id).toBe(state.operation_id);
  }
  return body;
}
async function continueRoutes(connection: Locator, enabled: boolean) {
  if (!enabled) return;
  const checkbox = connection.getByRole("checkbox", { name: "Continue all reviewed public routes when redeploying the connected application.", exact: true });
  await expect(checkbox).toBeVisible();
  await expect(checkbox).toBeEnabled();
  if (!await checkbox.isChecked()) {
    await checkbox.focus();
    await checkbox.press("Space");
  }
  await expect(checkbox).toBeChecked();
}
test("route continuation helper preserves selection on repeated keyboard review", async ({ page }) => {
  // Disposable page tests only the helper. It does not qualify a live workflow.
  await page.setContent(`<section aria-label="Connection helper fixture"><label><input type="checkbox" data-changes="0" onchange="this.dataset.changes = String(Number(this.dataset.changes) + 1)">Continue all reviewed public routes when redeploying the connected application.</label><label><input type="checkbox">Independent request approval</label></section>`);
  const connection = page.getByRole("region", { name: "Connection helper fixture", exact: true });
  const continuation = connection.getByRole("checkbox", { name: "Continue all reviewed public routes when redeploying the connected application.", exact: true });
  const approval = connection.getByRole("checkbox", { name: "Independent request approval", exact: true });
  await continueRoutes(connection, true);
  await expect(continuation).toBeChecked();
  await expect(continuation).toHaveAttribute("data-changes", "1");
  await expect(approval).not.toBeChecked();
  await continueRoutes(connection, true);
  await expect(continuation).toBeChecked();
  await expect(continuation).toHaveAttribute("data-changes", "1");
  await expect(approval).not.toBeChecked();
});
async function admit(page: Page, connection: Locator, name: string, path: string) {
  const form = connection.getByRole("form", { name: "Review database connection request", exact: true });
  const approval = form.getByRole("checkbox", { name: "I reviewed the selected database and application settings and approve this request.", exact: true });
  await approval.focus(); await page.keyboard.press("Space");
  await expect(approval).toBeChecked();
  const response = page.waitForResponse((r) => new URL(r.url()).pathname === path && r.request().method() === "POST", { timeout: 180_000 });
  const button = connection.getByRole("button", { name, exact: true });
  await expect(button).toBeEnabled(); await button.focus(); await page.keyboard.press("Enter");
  const received = await response; expect(received.status()).toBe(202);
  const submitted = received.request().postDataJSON() as Record<string, unknown>;
  if (submitted.application_database_route_continuity) {
    const accepted = await received.json() as { id: string };
    expect(accepted.id).not.toBe("");
    const progress = connection.getByRole("region", { name: "Connected application deployment progress", exact: true });
    await expect(progress).toBeVisible();
    await expect(progress).toContainText(`Application deployment ${accepted.id}:`);
  }
  return submitted;
}

test("live owner connects, redeploys and maintains a retained database; viewer only inspects", async ({ page, browser }, info) => {
  test.skip(!meta, "requires the explicitly authorized combined live fixture");
  test.setTimeout(12 * 60_000);
  const m = meta!;
  const errors: string[] = [], reveals: string[] = [];
  page.on("pageerror", () => errors.push("page error"));
  page.on("request", (r) => { if (new URL(r.url()).pathname.endsWith("/reveal")) reveals.push("reveal"); });
  try {
    await login(page);
    const connection = await panel(page, m);
    await expect(page.getByLabel("Hosting project", { exact: true }).getByRole("option", { name: "browser private project", exact: true })).toHaveCount(0);
    await connection.getByLabel("Database to connect", { exact: true }).selectOption(m.database_resource_id);
    await connection.getByLabel("Application container port", { exact: true }).fill("8080");
    await connection.getByLabel("Application readiness path", { exact: true }).fill("/ready");
    const connectPath = `/api/v1/resources/${m.application_resource_id}/database-connections`;
    const deployPath = `/api/v1/resources/${m.application_resource_id}/database-connection/deployments`;
    const maintenancePath = `/api/v1/resources/${m.database_resource_id}/database/maintenance`;
    const connectBody = await admit(page, connection, "Connect database and deploy application", connectPath);
    const first = await progress(page, m, "redeploy"); await settled(connection, first);
    const firstProof = await proof(page, m, first, 1); const a = firstProof.container_id; const routes = !!firstProof.route;
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      await expect(connection.getByRole("button", { name: "Redeploy connected application", exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      expect(await connection.evaluate((node) => { const bounds = node.getBoundingClientRect(); return [...node.querySelectorAll("button,input,select")].every((control) => { const box = control.getBoundingClientRect(); return box.left >= bounds.left && box.right <= bounds.right + 1; }); })).toBe(true);
      await connection.screenshot({ path: info.outputPath(`live-${m.engine}-connection-${width}.png`), animations: "disabled" });
    }
    await continueRoutes(connection, routes);
    const deployBody = await admit(page, connection, "Redeploy connected application", deployPath);
    if (routes) expect(deployBody.application_database_route_continuity).toBeTruthy();
    const second = await progress(page, m, "maintenance"); await settled(connection, second);
    const secondProof = await proof(page, m, second, 2); const b = secondProof.container_id; expect(b).not.toBe(a); expect(!!secondProof.route).toBe(routes);
    const maintenanceBody = await admit(page, connection, "Redeploy connected database", maintenancePath);
    const maintained = await progress(page, m, "replay"); await settled(connection, maintained);
    expect((await proof(page, m, maintained, 3)).container_id).toBe(b);
    await continueRoutes(connection, routes);
    await admit(page, connection, "Redeploy connected application", deployPath);
    const final = await progress(page, m, "finish"); await settled(connection, final);
    const finalProof = await proof(page, m, final, 4); const c = finalProof.container_id; expect(c).not.toBe(b); expect(!!finalProof.route).toBe(routes);
    const viewerContext = await browser.newContext({ baseURL: m.origin, viewport: { width: 390, height: 1000 } });
    try {
      const viewer = await viewerContext.newPage(); await login(viewer, true); const read = await panel(viewer, m);
      await expect(read.getByText("Project owner access is required for database connection changes.", { exact: true })).toBeVisible();
      await expect(read.getByRole("form", { name: "Review database connection request", exact: true })).toHaveCount(0);
      await expect(read.getByText("Connection workflow completed", { exact: true })).toBeVisible();
      expect((await request(viewer, "get", `/api/v1/resources/${m.application_resource_id}/database-connection`)).status()).toBe(200);
      for (const [path, data] of [[connectPath, connectBody], [deployPath, deployBody], [maintenancePath, maintenanceBody]] as const) expect((await request(viewer, "post", path, { headers: headers(m), data })).status()).toBe(403);
      expect((await request(viewer, "get", m.control_path + "/state", { headers: headers(m) })).status()).toBe(403);
      expect((await request(viewer, "post", m.control_path + "/finish", { headers: headers(m) })).status()).toBe(403);
    } finally { await viewerContext.close(); }
    const anonymous = await browser.newContext({ baseURL: m.origin });
    try { expect((await request(anonymous, "get", `/api/v1/resources/${m.application_resource_id}/database-connection`)).status()).toBe(401); expect((await request(anonymous, "get", m.control_path + "/state", { headers: headers(m) })).status()).toBe(401); } finally { await anonymous.close(); }
    expect((await request(page, "get", `/api/v1/resources/${m.foreign_resource_id}/database-connection`)).status()).toBe(403);
    expect((await request(page, "post", `/api/v1/resources/${m.foreign_resource_id}/database-connections`, { headers: headers(m), data: connectBody })).status()).toBe(403);
    expect((await request(page, "post", m.control_path + "/finish", { headers: { ...headers(m), Origin: "http://127.0.0.1:1" } })).status()).toBe(403);
    expect(errors).toEqual([]); expect(reveals).toEqual([]);
    expect((await request(page, "post", m.control_path + "/finish", { headers: headers(m) })).status()).toBe(204);
  } catch (error) {
    await reportFailure(page,m);
    throw error;
  }
});
