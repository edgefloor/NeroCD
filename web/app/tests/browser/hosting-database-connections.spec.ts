import { expect, test, type Page } from "@playwright/test";

// Intercepted UI evidence only. These fixtures prove no database binding,
// controller execution, secret injection or physical workload health.
async function fixture(page: Page, connectedStart = false, viewerStart = false) {
  let connected = connectedStart;
  let lose = false;
  let authorityReads = 0;
  let routeGeneration = 7;
  const viewer = viewerStart;
  const writes: string[] = [];
  const list = (items: unknown[]) => ({ items, count: items.length, total: items.length, limit: 100, offset: 0 });
  const resource = { id: "app", project_id: "p", project_environment_id: "e", name: "Connection fixture application", kind: "application", generation: 3, created_by: "operator", created_at: "", updated_at: "", source: { kind: "image", image: { reference: "example.test/ui-fixture:1" } } };
  const connection = { id: "hcn_fixture", project_id: "p", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: `sha256:${"a".repeat(64)}`, state: "active", reason_code: "completed", capture_id: "capture_fixture", upgrade_operation_id: "upgrade_fixture", application_operation_id: "application_fixture", membership_revision: 4, created_at: "2026-10-24T00:00:00Z", updated_at: "2026-10-24T00:00:00Z" };
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (request.method() === "GET" && path === "/api/v1/hosting-catalog") return route.fulfill({ json: [] });
    if (path === "/api/v1/me") { authorityReads++; return route.fulfill({ json: { id: "operator", provider: "oidc", roles: [], name: "Fixture operator", email: "fixture@example.test" } }); }
    if (path === "/api/v1/health") return route.fulfill({ json: { status: "ok" } });
    if (path === "/api/v1/projects") return route.fulfill({ json: list([{ id: "p", name: "Intercepted database UI fixture", description: "", created_at: "" }]) });
    if (path === "/api/v1/project-members") return route.fulfill({ json: list([{ id: "member", project_id: "p", user_id: "operator", role: viewer ? "viewer" : "owner", created_at: "" }]) });
    if (path === "/api/v1/project-environments") return route.fulfill({ json: list([{ id: "e", project_id: "p", name: "Fixture environment", created_at: "" }]) });
    if (path === "/api/v1/resources") return route.fulfill({ json: list([resource]) });
    if (path === "/api/v1/resources/app") return route.fulfill({ json: resource });
    if (path === "/api/v1/resources/db") return route.fulfill({ json: { ...resource, id: "db", kind: "database", generation: 7 } });
    if (path.endsWith("/route-switches")) return route.fulfill({ json: { resource_id: "app", loaded_generation: routeGeneration, loaded_proxy_operation_id: "pop_loaded", available: true, reason: "available", next_after_operation_id: null, routes: [{ id: "route", hostname: "app.example.test", path_prefix: "/", version: 3, selected_release_operation_id: "application_fixture" }], targets: [{ operation_id: "application_fixture", kind: "current_healthy" }] } });
    if (path.startsWith("/api/v1/hosting-operations/")) return route.fulfill({ json: { id: path.split("/").at(-1), project_id: "p", resource_id: "app", state: "verifying", mutation_hold: true, restore_ownership_held: false } });
    if (path.endsWith("/database-connection")) return route.fulfill({ status: connected ? 200 : 404, json: connected ? connection : { error: "Fixture has no connection" } });
    if (path.endsWith("/database-connections/options")) return route.fulfill({ json: { items: [{ resource_id: "db", name: "Fixture PostgreSQL", engine: "postgresql", generation: 2, destination_id: "dest", status: "available", reason_code: "eligible" }] } });
    if (path.endsWith("/database-connections") && request.method() === "POST") {
      writes.push(request.postData()!);
      if (lose) { lose = false; return route.abort("failed"); }
      connected = true; return route.fulfill({ status: 202, json: connection });
    }
    if (path.endsWith("/database-connection/deployments") || path.endsWith("/database/maintenance")) {
      writes.push(request.postData()!); if (lose) { lose = false; return route.abort("failed"); } const target = path.includes("/db/") ? "db" : "app";
      return route.fulfill({ status: 202, json: { id: "hop_fixture", project_id: "p", resource_id: target, snapshot: { resource_id: target } } });
    }
    if (path.endsWith("/runtime-configuration")) return route.fulfill({ json: { runtime_configuration: { resource_id: "app", project_id: "p", resource_generation: 3, runtime_generation: 5, entries: [] } } });
    if (path.endsWith("/binding")) return route.fulfill({ json: { id: "binding", resource_id: "app", destination_id: "dest", runtime_namespace: "fixture_namespace", created_by: "operator", created_at: "" } });
    if (path.endsWith("/storage-deployment-options")) return route.fulfill({ json: { resource_id: "app", resource_generation: 3, storage_ref: null, previous_healthy_operation_id: null, host_port: null, container_port: null, readiness: null, available: false, reason: "storage_unconfigured", routes: [], storage_route_continuity: null, application_database_storage: null } });
    if (path.includes("/routes") || path.includes("/retained-releases")) return route.fulfill({ json: [] });
    return route.fulfill({ json: list([]) });
  });
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption("p");
  await hosting.getByRole("button", { name: "Fixture environment", exact: true }).click();
  const panel = page.getByRole("region", { name: "Database connection for Connection fixture application", exact: true });
  if (connectedStart) await expect(panel.getByText("Connection workflow completed", { exact: true })).toBeVisible();
  else await expect(panel.getByLabel("Database to connect", { exact: true })).toBeVisible();
  return { panel, writes, authorityReads: () => authorityReads, lose: () => { lose = true; }, stale: () => { routeGeneration++; } };
}
for (const width of [320, 390, 430, 1440]) {
  test(`intercepted connection flow contains controls and supports keyboard at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 });
    const { panel, writes } = await fixture(page);
    await panel.getByLabel("Database to connect", { exact: true }).selectOption("db");
    await panel.getByLabel("Application container port", { exact: true }).fill("8080");
    const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request."); await approve.focus(); await page.keyboard.press("Space");
    const connect = panel.getByRole("button", { name: "Connect database and deploy application", exact: true }); await connect.focus(); await page.keyboard.press("Enter");
    await expect(panel.getByText("Connection workflow completed", { exact: true })).toBeVisible();
    expect(JSON.parse(writes[0]).binding.expected_runtime_generation).toBe(5);
    expect(JSON.parse(writes[0]).deployment.expected_runtime_configuration_generation).toBe(5);
    await expect(panel.getByRole("button", { name: "Request image deployment", exact: true })).toHaveCount(0);
    await approve.check(); await panel.getByRole("button", { name: "Redeploy connected database", exact: true }).click();
    await expect(panel.getByText(/Request accepted: hop_fixture/)).toBeVisible();
    expect(JSON.parse(writes[1])).toMatchObject({ expected_generation: 7, expected_membership_revision: 4 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(await panel.evaluate((node) => {
      const bounds = node.getBoundingClientRect();
      return [...node.querySelectorAll("button,input,select")].every((control) => { const box = control.getBoundingClientRect(); return box.left >= bounds.left && box.right <= bounds.right + 1; });
    })).toBe(true);
    await panel.screenshot({ path: info.outputPath(`intercepted-database-connection-${width}.png`), animations: "disabled" });
  });
}
test("intercepted uncertain connection delivery retains an identical request", async ({ page }) => {
  await page.clock.install();
  const { panel, writes, lose, authorityReads } = await fixture(page);
  await panel.getByLabel("Database to connect", { exact: true }).selectOption("db"); await panel.getByRole("form", { name: "Review database connection request" }).getByRole("checkbox").check(); lose();
  await panel.getByRole("button", { name: "Connect database and deploy application", exact: true }).click();
  await expect(panel.getByText(/No confirmed response/)).toBeVisible();
  const before = authorityReads();
  await page.clock.fastForward(11_000);
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));
  await expect.poll(authorityReads).toBeGreaterThan(before);
  await expect(panel.getByText(/Retained request:/)).toBeVisible();
  await panel.getByRole("button", { name: "Retry identical connection request", exact: true }).click();
  await expect(panel.getByText("Connection workflow completed", { exact: true })).toBeVisible(); expect(writes[1]).toBe(writes[0]);
});

for (const width of [320, 390, 430, 1440]) {
  test(`intercepted connected route review and identical retry at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    const { panel, writes, lose } = await fixture(page);
    await panel.getByLabel("Database to connect", { exact: true }).selectOption("db");
    const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request.");
    await approve.check();
    await panel.getByRole("button", { name: "Connect database and deploy application", exact: true }).click();
    const continuation = panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.");
    await expect(continuation).toBeVisible();
    await continuation.focus(); await page.keyboard.press("Space");
    await approve.check(); lose();
    const deploy = panel.getByRole("button", { name: "Redeploy connected application", exact: true });
    await deploy.focus(); await page.keyboard.press("Enter");
    await expect(panel.getByText(/No confirmed response/)).toBeVisible();
    await panel.getByRole("button", { name: "Retry identical application request", exact: true }).click();
    await expect(panel.getByText(/Request accepted: hop_fixture/)).toBeVisible();
    expect(writes[1]).toBe(writes[2]);
    expect(JSON.parse(writes[1]).application_database_route_continuity).toEqual({ expected_loaded_generation: 7, expected_loaded_proxy_operation_id: "pop_loaded", routes: [{ route_id: "route", expected_version: 3, expected_selected_release_operation_id: "application_fixture" }] });
    await expect(panel.getByText(/Application deployment hop_fixture: verifying/)).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  });
}

test("intercepted stale route generation requires a new reviewed request", async ({ page }) => {
  const { panel, writes, stale } = await fixture(page);
  await panel.getByLabel("Database to connect", { exact: true }).selectOption("db");
  const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request.");
  await approve.check(); await panel.getByRole("button", { name: "Connect database and deploy application", exact: true }).click();
  await panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.").check();
  await approve.check(); stale();
  await panel.getByRole("button", { name: "Redeploy connected application", exact: true }).click();
  await expect(panel.getByText(/Saved configuration or membership changed/)).toBeVisible();
  expect(writes).toHaveLength(1);
});

test("intercepted connected viewer can read progress but cannot request route continuation", async ({ page }) => {
  const { panel, writes } = await fixture(page, true, true);
  await expect(panel.getByRole("region", { name: "Connected application deployment progress" })).toBeVisible();
  await expect(panel.getByRole("button", { name: "Redeploy connected application", exact: true })).toHaveCount(0);
  await expect(panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.")).toHaveCount(0);
  expect(writes).toHaveLength(0);
});

test("intercepted failed route refresh permits keyboard removal from an unsent draft", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 1000 });
  const { panel, writes } = await fixture(page, true);
  await panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.").check();
  const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request.");
  await approve.check();
  await page.route("**/resources/app/route-switches", (route) => route.fulfill({ status: 503, json: { error: "Fixture route refresh unavailable" } }));
  await panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
  await expect(panel.getByText("Loaded route choices are unavailable. Route continuation is paused.")).toBeVisible();
  const clear = panel.getByRole("button", { name: "Remove route continuation from draft", exact: true });
  await expect(clear).toBeEnabled(); await clear.focus(); await page.keyboard.press("Enter");
  await expect(approve).not.toBeChecked();
  const deploy = panel.getByRole("button", { name: "Redeploy connected application", exact: true });
  await expect(deploy).toBeDisabled();
  await approve.focus(); await page.keyboard.press("Space");
  await deploy.focus(); await page.keyboard.press("Enter");
  await expect(panel.getByText(/Request accepted: hop_fixture/)).toBeVisible();
  expect(writes).toHaveLength(1);
  expect(JSON.parse(writes[0])).not.toHaveProperty("application_database_route_continuity");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("intercepted refreshed eligible routes require renewed approval before deployment", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 1000 });
  const { panel, writes, stale } = await fixture(page, true);
  await panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.").check();
  const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request.");
  await approve.check(); stale();
  await panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
  await expect(panel.getByText("Loaded route choices changed. Review the current routes and confirm this request again.")).toBeVisible();
  await expect(approve).not.toBeChecked();
  const deploy = panel.getByRole("button", { name: "Redeploy connected application", exact: true });
  await expect(deploy).toBeDisabled(); expect(writes).toHaveLength(0);
  await approve.focus(); await page.keyboard.press("Space");
  await deploy.focus(); await page.keyboard.press("Enter");
  await expect(panel.getByText(/Request accepted: hop_fixture/)).toBeVisible();
  expect(writes).toHaveLength(1);
  expect(JSON.parse(writes[0]).application_database_route_continuity.expected_loaded_generation).toBe(8);
});

for (const width of [320, 390, 430, 1440]) {
  test(`intercepted connected upload storage review requires interruption approval at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 });
    const { panel, writes, lose } = await fixture(page, true);
    const hash = `sha256:${"a".repeat(64)}`;
    await page.route("**/resources/app/storage-deployment-options", (route) => route.fulfill({ json: { resource_id: "app", resource_generation: 3, storage_ref: { storage_id: "storage", generation: 1, hash }, previous_healthy_operation_id: "application_fixture", host_port: 20000, container_port: 8080, readiness: { path: "/", timeout_seconds: 30 }, available: true, reason: "available", routes: [{ id: "route", hostname: "app.example.test", path_prefix: "/", version: 3, selected_release_operation_id: "application_fixture" }], storage_route_continuity: null, application_database_storage: { version: "application-database.storage.v1", strategy: "replacement_with_interruption", expected_storage_generation: 1, expected_storage_hash: hash, acknowledge_interruption_required: true } } }));
    await page.route("**/resources/app/storage", (route) => route.fulfill({ json: { id: "storage", project_id: "p", resource_id: "app", binding_id: "binding", destination_id: "dest", generation: 1, hash, kind: "platform_managed", driver: "local", access_mode: "read_write", retention: "retain", volume_name: "ncd_uploads", mount_path: "/app/uploads" } }));
    await panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
    await expect(panel.getByText(/Saved upload mount: \/app\/uploads/)).toBeVisible();
    const continuation = panel.getByLabel("Continue all reviewed public routes when redeploying the connected application.");
    await continuation.check();
    const approve = panel.getByLabel("I reviewed the selected database and application settings and approve this request.");
    await approve.check();
    const deploy = panel.getByRole("button", { name: "Redeploy connected application", exact: true });
    await expect(deploy).toBeDisabled();
    const interrupt = panel.getByLabel("I acknowledge application interruption while replacing the release using this saved upload volume.");
    await interrupt.focus(); await page.keyboard.press("Space");
    await expect(approve).not.toBeChecked();
    await approve.focus(); await page.keyboard.press("Space"); lose();
    await deploy.focus(); await page.keyboard.press("Enter");
    await expect(panel.getByText(/No confirmed response/)).toBeVisible();
    await panel.getByRole("button", { name: "Retry identical application request", exact: true }).click();
    await expect(panel.getByText(/Request accepted: hop_fixture/)).toBeVisible();
    expect(writes[0]).toBe(writes[1]);
    expect(JSON.parse(writes[1]).application_database_storage).toEqual({ expected_storage_generation: 1, expected_storage_hash: hash, acknowledge_interruption: true });
    expect(JSON.parse(writes[1]).application_database_route_continuity.routes).toHaveLength(1);
    await panel.screenshot({ path: info.outputPath(`intercepted-connected-storage-${width}.png`), animations: "disabled" });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

for (const action of ["resume_predecessor", "complete_replacement"] as const) {
  test(`intercepted offered storage recovery ${action} requires separate review`, async ({ page }) => {
    await page.clock.install();
    await page.setViewportSize({ width: 390, height: 1000 });
    const { panel } = await fixture(page, true);
    const hash = `sha256:${"a".repeat(64)}`, stage = `sha256:${"b".repeat(64)}`, candidate = `sha256:${"c".repeat(64)}`;
    await page.route("**/resources/app/database-connection", (route) => route.fulfill({ json: { id: "hcn_fixture", project_id: "p", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: hash, state: "held", reason_code: "replacement_failed", capture_id: "capture_fixture", upgrade_operation_id: "upgrade_fixture", application_operation_id: "hop_fixture", hold_operation_id: "hop_fixture", membership_revision: 4, created_at: "", updated_at: "" } }));
    const operation = { id: "hop_fixture", project_id: "p", resource_id: "app", state: "failed", mutation_hold: true, restore_ownership_held: false, application_database_storage: { version: "application-database.storage.v1", strategy: "replacement_with_interruption", storage_ref: { storage_id: "storage", generation: 1, hash }, hash }, application_database_storage_state: { version: "application-database.storage.v1", phase: "replacement_failed", receipt_id: "receipt", state_hash: hash, previous_state_hash: hash, candidate_hash: candidate, stage_hash: stage, recovery_actions: [action], recovery_action: "", recovery_state: "none" }, snapshot: { resource_id: "app" } };
    await page.route("**/hosting-operations/hop_fixture", (route) => route.fulfill({ json: operation }));
    const writes: string[] = [];
    await page.route("**/resources/app/database-connection/recovery", (route) => { writes.push(route.request().postData()!); operation.application_database_storage_state.recovery_action = action; operation.application_database_storage_state.recovery_state = "queued"; operation.application_database_storage_state.recovery_actions = []; return route.fulfill({ status: 202, json: operation }); });
    await panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
    const recovery = panel.getByRole("region", { name: "Connected storage recovery", exact: true });
    const label = action === "resume_predecessor" ? "Resume previous application" : "Complete interrupted replacement";
    await recovery.getByRole("button", { name: `Review: ${label}`, exact: true }).click();
    const submit = recovery.getByRole("button", { name: label, exact: true }); await expect(submit).toBeDisabled();
    const acknowledge = recovery.getByLabel("I acknowledge application interruption and retained data changes for this recovery.");
    await acknowledge.focus(); await page.keyboard.press("Space");
    await submit.focus(); await page.keyboard.press("Enter");
    await expect(recovery.getByText(/Recovery request accepted/)).toBeVisible();
    await expect(recovery.getByText(/Recovery status: queued/)).toBeVisible();
    operation.application_database_storage_state.recovery_state = "succeeded";
    await page.clock.fastForward(5001);
    await expect(recovery.getByText(/server recorded verified recovery settlement/)).toBeVisible();
    await expect(recovery.getByText(/Recovery request accepted/)).toBeVisible();
    expect(writes).toHaveLength(1);
    expect(JSON.parse(writes[0])).toMatchObject({ action, operation_id: "hop_fixture", stage_hash: stage, candidate_hash: candidate, expected_membership_revision: 4, storage_state_hash: hash, application_database_storage: { expected_storage_generation: 1, expected_storage_hash: hash, acknowledge_interruption: true } });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

async function pendingRecoveryFixture(page: Page) {
  await page.clock.install();
  const { panel } = await fixture(page, true);
  const hash = `sha256:${"a".repeat(64)}`;
  let reads = 0, state = "queued", unavailable = false;
  await page.route("**/resources/app/database-connection", (route) => route.fulfill({ json: { id: "hcn_fixture", project_id: "p", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: hash, state: "held", reason_code: "replacement_failed", capture_id: "capture_fixture", upgrade_operation_id: "upgrade_fixture", application_operation_id: "hop_fixture", hold_operation_id: "hop_fixture", membership_revision: 4, created_at: "", updated_at: "" } }));
  await page.route("**/hosting-operations/hop_fixture", (route) => {
    reads++;
    return route.fulfill({ status: unavailable ? 503 : 200, json: unavailable ? {} : { id: "hop_fixture", project_id: "p", resource_id: "app", state: "failed", mutation_hold: state !== "succeeded", restore_ownership_held: false, application_database_storage: { version: "application-database.storage.v1", strategy: "replacement_with_interruption", storage_ref: { storage_id: "storage", generation: 1, hash }, hash }, application_database_storage_state: { version: "application-database.storage.v1", phase: "replacement_failed", receipt_id: "receipt", state_hash: hash, previous_state_hash: hash, stage_hash: hash, candidate_hash: hash, recovery_actions: [], recovery_action: "resume_predecessor", recovery_state: state } } });
  });
  await page.reload();
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption("p");
  await hosting.getByRole("button", { name: "Fixture environment", exact: true }).click();
  await expect(panel.getByText(/Recovery status: queued/)).toBeVisible();
  return { panel, reads: () => reads, settle: () => { state = "succeeded"; }, unavailable: (value: boolean) => { unavailable = value; } };
}
test("intercepted fresh pending recovery polls a failed original until proof-derived settlement", async ({ page }) => {
  const state = await pendingRecoveryFixture(page);
  const before = state.reads(); await page.clock.fastForward(5001);
  await expect.poll(state.reads).toBeGreaterThan(before);
  await expect(state.panel.getByText(/Recovery remains pending, including when the original deployment is failed/)).toBeVisible();
  state.settle(); await page.clock.fastForward(5001);
  await expect(state.panel.getByText(/server recorded verified recovery settlement/)).toBeVisible();
  const after = state.reads(); await page.clock.fastForward(10001); expect(state.reads()).toBe(after);
});
test("intercepted pending recovery polling stops at its bound and manual refresh restarts checks", async ({ page }) => {
  const state = await pendingRecoveryFixture(page);
  await page.clock.fastForward(120_001);
  await expect(state.panel.getByText(/Automatic recovery checks paused after two minutes/)).toBeVisible();
  const stopped = state.reads(); await page.clock.fastForward(10_001); expect(state.reads()).toBe(stopped);
  await state.panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
  await expect.poll(state.reads).toBeGreaterThan(stopped);
  const restarted = state.reads(); await page.clock.fastForward(5001);
  await expect.poll(state.reads).toBeGreaterThan(restarted);
});
test("intercepted unreachable recovery preserves pending evidence and stops automatic reads", async ({ page }) => {
  const state = await pendingRecoveryFixture(page);
  state.unavailable(true); await page.clock.fastForward(5001);
  await expect(state.panel.getByText(/Current progress read: unavailable, recorded evidence only/)).toBeVisible();
  const stopped = state.reads(); await page.clock.fastForward(10001); expect(state.reads()).toBe(stopped);
  state.unavailable(false); await state.panel.getByRole("button", { name: "Refresh database connection", exact: true }).click();
  await expect(state.panel.getByText(/Recovery status: queued/)).toBeVisible();
});
