import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
// Synthetic HTTP evidence verifies UI rendering only. It does not exercise the
// compiled collector, real API qualification, Docker, or the full A01 workflow.
test("synthetic server readiness and enrollment command remain usable at mobile and desktop widths", async ({ page }, testInfo) => {
  const server = { id: "server_browser", name: "Readiness fixture", runner_id: "runner_browser", created_by: "admin", created_at: "2026-10-23T00:00:00Z" };
  const destination = { id: "destination_browser", server_id: server.id, name: "default", runtime_kind: "docker_compose", created_at: server.created_at };
  const runner = { id: server.runner_id, name: "Dedicated host", status: "active", capabilities: ["compose-deploy"], tags: [], registered_at: server.created_at, last_heartbeat_at: server.created_at };
  const list = (items: unknown[]) => ({ items, count: items.length, total: items.length, offset: 0, limit: 50 });
  let diagnosis = "no_sample";
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown = list([]);
    if (path === "/api/v1/me") body = { id: "admin", provider: "local", email: "fixture@example.test", roles: ["system_admin"] };
    else if (path === "/api/v1/health") body = { status: "ok" };
    else if (path === "/api/v1/runners") body = list([runner]);
    else if (path === "/api/v1/servers") body = list([server]);
    else if (path === `/api/v1/servers/${server.id}`) body = { server, destination };
    else if (path.endsWith("/readiness")) body = { server_id: server.id, profile: "basic_image_hosting.v1", status: diagnosis === "restore_hold" ? "blocked" : "unknown", reason: diagnosis };
    else if (path.endsWith("/runner-enrollments")) body = { enrollment: { id: "enrollment_fixture", runner_id: runner.id, expires_at: "2099-01-01T00:00:00Z", capabilities: ["compose-deploy", "image_deploy", "image_release_ports_v1", "hosting_readiness_v1"] }, token: "nce_synthetic_secret_mask_me" };
    await route.fulfill({ status: 200, contentType: "application/json", headers: { "Cache-Control": "no-store" }, body: JSON.stringify(body) });
  });
  await page.goto("/runners");
  await page.getByRole("button", { name: "View server destination" }).click();
  const panel = page.getByRole("region", { name: "Server readiness", exact: true });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 }); await panel.scrollIntoViewIfNeeded();
    await expect(panel.getByText("Server-reported status: unknown")).toBeVisible();
    const refresh = panel.getByRole("button", { name: "Refresh server readiness" }); await expect(refresh).toBeEnabled(); await refresh.focus(); await expect(refresh).toBeFocused(); await page.keyboard.press("Enter");
    await expect(panel.getByText(/Last successful refresh, local receipt/)).toBeVisible(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`synthetic-readiness-${width}.png`), fullPage: true, animations: "disabled" });
  }
  diagnosis = "restore_hold"; await panel.getByRole("button", { name: "Refresh server readiness" }).click(); await expect(panel.getByText("Server-reported status: blocked")).toBeVisible();
  await page.getByRole("button", { name: "Use image hosting with server readiness", exact: true }).click(); await expect(page.getByRole("textbox", { name: "Runner capabilities" })).toHaveValue(/hosting_readiness_v1/);
  await page.getByRole("textbox", { name: "Runner ID", exact: true }).fill("runner_browser"); await page.getByRole("textbox", { name: "Runner name", exact: true }).fill("Dedicated host"); await page.getByRole("button", { name: "Create enrollment", exact: true }).click();
  const dialog = page.getByRole("dialog"); await dialog.getByLabel("Runner-reachable server URL").fill("https://control.example.test");
  await expect(dialog.getByLabel("Manual runner command")).toContainText("--hosting-readiness"); await expect(dialog.getByLabel("Manual runner command")).toContainText("--execute");
  await expect(dialog.getByLabel("Manual runner command")).not.toContainText("--hosting-runtime-env");
  for (const width of [320, 390, 430, 1440]) { await page.setViewportSize({ width, height: 1000 }); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); await page.screenshot({ path: testInfo.outputPath(`synthetic-enrollment-${width}.png`), fullPage: true, animations: "disabled", mask: [dialog.getByLabel("Enrollment secret")] }); }
});


// Actual API/session/compiled-agent qualification. No browser network interception.
test.use({ trace: "off", screenshot: "off" });
test.describe("actual readiness enrollment", () => {
  test("administrator enrolls compiled host and reads current diagnostics", async ({ page, browser }, testInfo) => {
    test.skip(!process.env.NEROCD_READINESS_BROWSER_META, "requires owned compiled-agent browser fixture");
    test.setTimeout(200_000);
    const meta = JSON.parse(process.env.NEROCD_READINESS_BROWSER_META!) as { base_url: string; runner_origin: string; step_path: string; runner_id: string };
    expect(meta.base_url).toBe("http://127.0.0.1:32966");
    expect(new URL(meta.runner_origin).protocol).toBe("https:");
    expect(new URL(meta.step_path, meta.base_url).origin).toBe(meta.base_url);
    async function login(target: Page, email: string) {
      const status = await target.request.get("/api/v1/bootstrap-status"); expect(await status.json()).toEqual({ status: "complete" });
      await target.goto("/sign-in"); await target.getByLabel("Email", { exact: true }).fill(email);
      await target.getByLabel("Password", { exact: true }).fill("readiness-browser-fixture-password");
      await target.getByRole("button", { name: "Sign in", exact: true }).click();
      await expect(target.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    }
    const headers = { Origin: meta.base_url, "X-NeroCD-CSRF": "1" };
    async function step(name: string, data: object = {}, target = page) {
      const response = await target.request.post(meta.step_path + name, { headers, data });
      expect(response.status(), `bounded fixture step ${name}`).toBe(204);
    }
    await login(page, "readiness-admin@example.local");
    const manifest = JSON.parse(readFileSync(process.env.NEROCD_READINESS_UI_MANIFEST!, "utf8")) as { files: Record<string, string> };
    for (const [path, expected] of Object.entries(manifest.files)) {
      if (path === ".gitkeep") continue;
      const response = await page.request.get(path === "index.html" ? "/" : `/${path}`); expect(response.status()).toBe(200);
      expect(createHash("sha256").update(await response.body()).digest("hex"), `served asset ${path}`).toBe(expected);
    }
    await page.goto("/runners");
    await page.getByRole("button", { name: "Use image hosting with server readiness", exact: true }).click();
    await page.getByRole("textbox", { name: "Runner ID", exact: true }).fill(meta.runner_id);
    await page.getByRole("textbox", { name: "Runner name", exact: true }).fill("Readiness actual host");
    const enrolled = page.waitForResponse(r => new URL(r.url()).pathname === "/api/v1/runner-enrollments" && r.request().method() === "POST");
    await page.getByRole("button", { name: "Create enrollment", exact: true }).focus(); await page.keyboard.press("Enter");
    const response = await enrolled; expect(response.status()).toBe(201);
    const created = await response.json() as { enrollment: { id: string; capabilities: string[] } };
    expect(created.enrollment.capabilities).toContain("hosting_readiness_v1"); expect(created.enrollment.capabilities).not.toContain("shell");
    const dialog = page.getByRole("dialog"); await dialog.getByLabel("Runner-reachable server URL").fill(meta.runner_origin);
    const token = (await dialog.getByLabel("Enrollment secret").textContent())!;
    expect(/^nce_[a-f0-9]{64}$/.test(token), "one-time token shape").toBe(true);
    const command = (await dialog.getByLabel("Manual runner command").textContent())!;
    expect(command).toContain("--hosting-readiness"); expect(command).toContain("--execute"); expect(command.includes(token), "generated argv excludes secret").toBe(false);
    // Secrets stay in memory and this bounded authenticated fixture POST only.
    await step("start", { token, command, enrollment: created.enrollment.id });
    await page.keyboard.press("Escape"); await expect(page.getByLabel("Enrollment secret")).toHaveCount(0);
    expect((await page.locator("body").textContent())!.includes(token), "dismissal clears token").toBe(false);
    expect((await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, url: location.href }))).includes(token), "token absent from browser persistence and URL").toBe(false);
    const hosts = page.getByRole("region", { name: "Hosting servers", exact: true });
    await expect.poll(async () => { await hosts.getByRole("button", { name: "Refresh eligible runners" }).click(); return await hosts.getByLabel("Enrolled Compose runner").locator(`option[value="${meta.runner_id}"]`).count(); }).toBe(1);
    await hosts.getByLabel("Hosting server name").fill("Readiness actual server"); await hosts.getByLabel("Enrolled Compose runner").selectOption(meta.runner_id);
    const registered = page.waitForResponse(r => new URL(r.url()).pathname === "/api/v1/servers" && r.request().method() === "POST");
    await hosts.getByRole("button", { name: "Register hosting server", exact: true }).focus(); await page.keyboard.press("Enter");
    const serverResponse = await registered; expect(serverResponse.status()).toBe(201);
    const target = await serverResponse.json() as { server: { id: string } }; const readinessPath = `/api/v1/servers/${target.server.id}/readiness`;
    const panel = page.getByRole("region", { name: "Server readiness", exact: true }); await panel.scrollIntoViewIfNeeded();
    async function diagnosis(status: string, reason: string, timeout = 20_000) {
      await expect.poll(async () => { const result = await page.request.get(readinessPath); expect(result.status()).toBe(200); const value = await result.json(); return `${value.status}/${value.reason}`; }, { timeout }).toBe(`${status}/${reason}`);
      await panel.scrollIntoViewIfNeeded(); await expect(panel.getByRole("button", { name: "Refresh server readiness" })).toBeEnabled(); await panel.getByRole("button", { name: "Refresh server readiness" }).click();
      await expect(panel.getByText(`Server-reported status: ${status}`, { exact: true })).toBeVisible();
    }
    await diagnosis("ready", "qualified");
    for (const width of [320, 390, 430, 1440]) {
      await page.setViewportSize({ width, height: 1000 }); await panel.scrollIntoViewIfNeeded();
      const refresh = panel.getByRole("button", { name: "Refresh server readiness" }); await expect(refresh).toBeEnabled(); await refresh.focus(); await expect(refresh).toBeFocused(); await page.keyboard.press("Enter");
      await expect(panel.getByText("Server-reported status: ready", { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`actual-readiness-${width}.png`), fullPage: true, animations: "disabled" });
    }
    await step("unsafe-root"); await diagnosis("blocked", "prerequisite_failed");
    await step("safe-root"); await diagnosis("ready", "qualified");
    await step("stop"); await diagnosis("unknown", "stale", 40_000);
    await step("restart"); await diagnosis("ready", "qualified");
    const viewerContext = await browser.newContext({ baseURL: meta.base_url, viewport: { width: 390, height: 1000 } });
    try {
      const viewer = await viewerContext.newPage(); await login(viewer, "viewer@example.local");
      expect((await viewer.request.get(readinessPath)).status()).toBe(403);
      expect((await viewer.request.post("/api/v1/runner-enrollments", { headers, data: { runner_id: "runner_denied", runner_name: "denied", capabilities: ["compose-deploy", "hosting_readiness_v1"] } })).status()).toBe(403);
      expect((await viewer.request.post(meta.step_path + "unsafe-root", { headers, data: {} })).status()).toBe(403);
      await viewer.goto("/runners"); await expect(viewer.getByRole("region", { name: "Server readiness", exact: true })).toHaveCount(0);
    } finally { await viewerContext.close(); }
    await step("revoke"); await diagnosis("unknown", "authority_unavailable");
    await step("demote"); expect((await page.request.get(readinessPath)).status()).toBe(403);
    await expect(page.getByRole("alert").filter({ hasText: /Server readiness access is unavailable|Runner administrator access is required/ })).toBeVisible();
    await expect(page.getByRole("region", { name: "Historical server readiness details", exact: true })).toHaveCount(0);
    await page.reload(); await expect(page.getByRole("region", { name: "Server readiness", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Create enrollment", exact: true })).toHaveCount(0);
    // Completion uses an independent fixed recovery administrator, never the demoted actor.
    const recovery = await browser.newContext({ baseURL: meta.base_url });
    try { const recoveryPage = await recovery.newPage(); await login(recoveryPage, "admin@example.local"); await step("complete", {}, recoveryPage); } finally { await recovery.close(); }
  });
});
