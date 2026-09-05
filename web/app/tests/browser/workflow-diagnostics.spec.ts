import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function runtimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

async function signIn(page: Page): Promise<void> {
  const status = await page.request.get("/api/v1/bootstrap-status");
  if (!status.ok()) throw new Error(`browser bootstrap status failed with ${status.status()}`);
  const body = await status.json() as { status?: string };
  if (body.status === "required") {
    const [email] = readFileSync(join(runtimeDir(), "credentials"), "utf8").trim().split("\n");
    const databaseURL = readFileSync(join(runtimeDir(), "database-url"), "utf8");
    execFileSync(join(runtimeDir(), "nerocd"), ["bootstrap-admin", "--email", email!, "--name", "Browser Administrator", "--password-file", join(runtimeDir(), "password")], { env: { ...process.env, NEROCD_DATABASE_URL: databaseURL }, stdio: "pipe" });
    await page.reload({ waitUntil: "networkidle" });
  }
  const [email, password] = readFileSync(join(runtimeDir(), "credentials"), "utf8").trim().split("\n");
  await page.getByLabel("Email").fill(email!);
  await page.getByLabel("Password").fill(password!);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
}

async function postJSON<T>(page: Page, path: string, data: unknown): Promise<T> {
  const response = await page.request.post(path, { data, headers: { Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" } });
  if (!response.ok()) throw new Error(`browser fixture request ${path} failed with ${response.status()}`);
  return await response.json() as T;
}

test("run detail keeps declared workflow diagnostics visible and contained", async ({ page }) => {
  await page.goto("/runs");
  await signIn(page);
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const project = await postJSON<{ id: string }>(page, "/api/v1/projects", { name: `Workflow diagnostics ${suffix}`, description: "workflow detail fixture" });
  const template = await postJSON<{ id: string }>(page, "/api/v1/templates", {
    project_id: project.id,
    name: `workflow-${suffix}`,
    kind: "shell",
    run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "workflow-diagnostics"] } },
    workflow: {
      steps: [
        { id: "checkout", name: "Checkout source", run_spec: { type: "shell", inputs: {} }, requires_ack: false },
        { id: "deploy", name: "Deploy release", run_spec: { type: "shell", inputs: {} }, depends_on: ["checkout"], requires_ack: false },
      ],
    },
    runner_tags: ["shell"],
    requires_ack: false,
  });
  const run = await postJSON<{ id: string }>(page, "/api/v1/runs", { template_id: template.id });

  await page.reload();
  const detailLink = page.locator(`[data-run-detail-link="${run.id}"]`).last();
  await expect(detailLink).toBeVisible();
  await detailLink.click();
  const logs = page.getByRole("dialog", { name: `run ${run.id}` });
  await expect(logs).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(logs).toHaveCount(0);
  const diagnostics = page.getByRole("heading", { name: "Workflow steps" }).locator("xpath=ancestor::*[@data-slot='card']");
  await expect(diagnostics).toBeVisible();
  await expect(diagnostics).toContainText("Checkout source");
  await expect(diagnostics).toContainText("Deploy release");
  await expect(diagnostics).toContainText("Logs cover the whole run.");

  for (const width of [320, 390, 430]) {
    await page.setViewportSize({ width, height: 844 });
    const geometry = await diagnostics.evaluate((card) => ({
      cardRight: card.getBoundingClientRect().right,
      documentClientWidth: document.documentElement.clientWidth,
      documentScrollWidth: document.documentElement.scrollWidth,
    }));
    expect(geometry.documentScrollWidth).toBeLessThanOrEqual(geometry.documentClientWidth);
    expect(geometry.cardRight).toBeLessThanOrEqual(geometry.documentClientWidth);
  }
});
