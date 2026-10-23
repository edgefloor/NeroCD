import { expect, test } from "@playwright/test";
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
