import { expect, test, type Page } from "@playwright/test";

// Interaction-only evidence. API authorization and runner delivery require the
// separate combined hosting fixture; these routes deliberately use no secrets.
async function fixture(page: Page) {
  let role = "owner", generation = 0, entries: unknown[] = [], failure: "none" | "invalid" | "lost" = "none";
  const writes: string[] = [];
  const list = (items: unknown[]) => ({ items, count: items.length, total: items.length, limit: 100, offset: 0 });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    if (request.method() === "GET" && path === "/api/v1/hosting-catalog") return route.fulfill({ json: [] });
    if (path === "/api/v1/me") return route.fulfill({ json: { id: "operator", provider: "oidc", roles: [], name: "Operator", email: "operator@example.test" } });
    if (path === "/api/v1/health") return route.fulfill({ json: { status: "ok" } });
    if (path === "/api/v1/projects") return route.fulfill({ json: list([{ id: "p", name: "Runtime project", description: "", created_at: "2026-10-22T00:00:00Z" }]) });
    if (path === "/api/v1/project-members") return route.fulfill({ json: list([{ id: "member", project_id: "p", user_id: "operator", role, email: "operator@example.test", created_at: "" }]) });
    if (path === "/api/v1/project-environments") return route.fulfill({ json: list([{ id: "e", project_id: "p", name: "Production", created_at: "" }]) });
    if (path === "/api/v1/resources") return route.fulfill({ json: list([{ id: "r", project_id: "p", project_environment_id: "e", name: "Runtime web", kind: "application", generation: 1, created_by: "operator", created_at: "", updated_at: "", source: { kind: "image", image: { reference: "example/web:1" } } }]) });
    if (path === "/api/v1/resources/r/runtime-configuration") {
      if (request.method() === "PUT") {
        writes.push(request.postData()!);
        if (failure === "invalid") { failure = "none"; return route.fulfill({ status: 400, json: { error: "invalid" } }); }
        if (failure === "lost") { failure = "none"; return route.abort("failed"); }
        const body = request.postDataJSON(); entries = body.entries; generation = body.expected_runtime_generation + 1;
      }
      return route.fulfill({ json: { runtime_configuration: { resource_id: "r", project_id: "p", resource_generation: 1, runtime_generation: generation, entries } } });
    }
    if (path.endsWith("/binding")) return route.fulfill({ status: 404, json: { error: "no binding" } });
    if (path.includes("/routes") || path.includes("/retained-releases")) return route.fulfill({ json: [] });
    return route.fulfill({ json: list([]) });
  });
  await page.goto("/projects");
  const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption("p");
  await hosting.getByRole("button", { name: "Production", exact: true }).click();
  const runtime = hosting.getByRole("region", { name: "Runtime configuration for Runtime web", exact: true });
  await expect(runtime.getByRole("button", { name: "Add runtime variable", exact: true })).toBeVisible();
  return { runtime, writes, fail: (next: typeof failure) => { failure = next; }, viewer: () => { role = "viewer"; } };
}
for (const width of [320, 390, 430, 1440]) {
  test(`mocked runtime configuration is keyboard accessible and contained at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 });
    const { runtime } = await fixture(page);
    const add = runtime.getByRole("button", { name: "Add runtime variable", exact: true });
    await add.focus(); await page.keyboard.press("Enter");
    await runtime.getByLabel("Variable name 1", { exact: true }).fill("PUBLIC_URL");
    await runtime.getByLabel("Nonsecret value 1", { exact: true }).fill("https://example.test/" + "long-path/".repeat(20));
    const save = runtime.getByRole("button", { name: "Save runtime configuration", exact: true }); await save.focus(); await page.keyboard.press("Enter");
    await expect(runtime.getByText("Saved runtime generation 1 · resource generation 1", { exact: true })).toBeVisible();
    await expect(runtime.getByText(/Configured values are not evidence of workload application/)).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await runtime.screenshot({ path: info.outputPath(`runtime-config-${width}.png`), animations: "disabled" });
  });
}
test("mocked validation error preserves draft, loss retries identical body, and membership downgrade clears drafts", async ({ page }) => {
  const { runtime, writes, fail, viewer } = await fixture(page);
  await runtime.getByRole("button", { name: "Add runtime variable", exact: true }).click();
  await runtime.getByLabel("Variable name 1", { exact: true }).fill("VISIBLE"); await runtime.getByLabel("Nonsecret value 1", { exact: true }).fill("nonsecret draft");
  fail("invalid"); await runtime.getByRole("button", { name: "Save runtime configuration", exact: true }).click();
  await expect(runtime.getByText(/The server rejected this configuration/)).toBeVisible(); await expect(runtime.getByLabel("Nonsecret value 1", { exact: true })).toHaveValue("nonsecret draft");
  fail("lost"); await runtime.getByRole("button", { name: "Save runtime configuration", exact: true }).click();
  await expect(runtime.getByText(/Save delivery is unconfirmed/)).toBeVisible(); await expect(runtime.getByLabel("Nonsecret value 1", { exact: true })).toBeDisabled();
  await runtime.getByRole("button", { name: "Retry identical runtime save", exact: true }).click(); await expect(runtime.getByText("Saved runtime generation 1 · resource generation 1", { exact: true })).toBeVisible();
  expect(writes).toHaveLength(3); expect(writes[2]).toBe(writes[1]);
  await runtime.getByLabel("Nonsecret value 1", { exact: true }).fill("discard on downgrade"); viewer();
  await runtime.getByRole("button", { name: "Refresh runtime configuration access", exact: true }).click();
  await expect(runtime.getByText("Project owner access is required to save runtime variables.", { exact: true })).toBeVisible(); await expect(runtime.getByRole("form")).toHaveCount(0);
});
