import { expect, test, type Page } from "@playwright/test";

// Intercepted UI evidence only. These fixtures prove no database binding,
// controller execution, secret injection or physical workload health.
async function fixture(page: Page) {
  let connected = false;
  let lose = false;
  const writes: string[] = [];
  const list = (items: unknown[]) => ({ items, count: items.length, total: items.length, limit: 100, offset: 0 });
  const resource = { id: "app", project_id: "p", project_environment_id: "e", name: "Connection fixture application", kind: "application", generation: 3, created_by: "operator", created_at: "", updated_at: "", source: { kind: "image", image: { reference: "example.test/ui-fixture:1" } } };
  const connection = { id: "hcn_fixture", project_id: "p", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: `sha256:${"a".repeat(64)}`, state: "active", reason_code: "completed", capture_id: "capture_fixture", upgrade_operation_id: "upgrade_fixture", application_operation_id: "application_fixture", membership_revision: 4, created_at: "2026-10-24T00:00:00Z", updated_at: "2026-10-24T00:00:00Z" };
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path === "/api/v1/me") return route.fulfill({ json: { id: "operator", provider: "oidc", roles: [], name: "Fixture operator", email: "fixture@example.test" } });
    if (path === "/api/v1/health") return route.fulfill({ json: { status: "ok" } });
    if (path === "/api/v1/projects") return route.fulfill({ json: list([{ id: "p", name: "Intercepted database UI fixture", description: "", created_at: "" }]) });
    if (path === "/api/v1/project-members") return route.fulfill({ json: list([{ id: "member", project_id: "p", user_id: "operator", role: "owner", created_at: "" }]) });
    if (path === "/api/v1/project-environments") return route.fulfill({ json: list([{ id: "e", project_id: "p", name: "Fixture environment", created_at: "" }]) });
    if (path === "/api/v1/resources") return route.fulfill({ json: list([resource]) });
    if (path === "/api/v1/resources/app") return route.fulfill({ json: resource });
    if (path === "/api/v1/resources/db") return route.fulfill({ json: { ...resource, id: "db", kind: "database", generation: 7 } });
    if (path.endsWith("/database-connection")) return route.fulfill({ status: connected ? 200 : 404, json: connected ? connection : { error: "Fixture has no connection" } });
    if (path.endsWith("/database-connections/options")) return route.fulfill({ json: { items: [{ resource_id: "db", name: "Fixture PostgreSQL", engine: "postgresql", generation: 2, destination_id: "dest", status: "available", reason_code: "eligible" }] } });
    if (path.endsWith("/database-connections") && request.method() === "POST") {
      writes.push(request.postData()!);
      if (lose) { lose = false; return route.abort("failed"); }
      connected = true; return route.fulfill({ status: 202, json: connection });
    }
    if (path.endsWith("/database-connection/deployments") || path.endsWith("/database/maintenance")) {
      writes.push(request.postData()!); const target = path.includes("/db/") ? "db" : "app";
      return route.fulfill({ status: 202, json: { id: "hop_fixture", project_id: "p", resource_id: target, snapshot: { resource_id: target } } });
    }
    if (path.endsWith("/runtime-configuration")) return route.fulfill({ json: { runtime_configuration: { resource_id: "app", project_id: "p", resource_generation: 3, runtime_generation: 5, entries: [] } } });
    if (path.endsWith("/binding")) return route.fulfill({ json: { id: "binding", resource_id: "app", destination_id: "dest", runtime_namespace: "fixture_namespace", created_by: "operator", created_at: "" } });
    if (path.endsWith("/storage-deployment-options")) return route.fulfill({ status: 503, json: { error: "Unrelated fixture controls unavailable" } });
    if (path.includes("/routes") || path.includes("/retained-releases")) return route.fulfill({ json: [] });
    return route.fulfill({ json: list([]) });
  });
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption("p");
  await hosting.getByRole("button", { name: "Fixture environment", exact: true }).click();
  const panel = page.getByRole("region", { name: "Database connection for Connection fixture application", exact: true });
  await expect(panel.getByLabel("Database to connect", { exact: true })).toBeVisible();
  return { panel, writes, lose: () => { lose = true; } };
}
for (const width of [320, 390, 430, 1440]) {
  test(`intercepted connection flow contains controls and supports keyboard at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 });
    const { panel, writes } = await fixture(page);
    await panel.getByLabel("Database to connect", { exact: true }).selectOption("db");
    await panel.getByLabel("Application container port", { exact: true }).fill("8080");
    const approve = panel.getByRole("form", { name: "Review database connection request" }).getByRole("checkbox"); await approve.focus(); await page.keyboard.press("Space");
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
  const { panel, writes, lose } = await fixture(page);
  await panel.getByLabel("Database to connect", { exact: true }).selectOption("db"); await panel.getByRole("form", { name: "Review database connection request" }).getByRole("checkbox").check(); lose();
  await panel.getByRole("button", { name: "Connect database and deploy application", exact: true }).click();
  await expect(panel.getByText(/No confirmed response/)).toBeVisible();
  await panel.getByRole("button", { name: "Retry identical connection request", exact: true }).click();
  await expect(panel.getByText("Connection workflow completed", { exact: true })).toBeVisible(); expect(writes[1]).toBe(writes[0]);
});
