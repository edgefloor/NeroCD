import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

function browserRuntimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

async function bootstrapAdmin(): Promise<void> {
  const runtimeDir = browserRuntimeDir();
  const [email] = (await readFile(join(runtimeDir, "credentials"), "utf8")).trim().split("\n");
  const databaseURL = await readFile(join(runtimeDir, "database-url"), "utf8");
  if (!email || !databaseURL) throw new Error("browser bootstrap fixture is unavailable");
  execFileSync(join(runtimeDir, "nerocd"), ["bootstrap-admin", "--email", email, "--name", "Browser Administrator", "--password-file", join(runtimeDir, "password")], {
    env: { ...process.env, NEROCD_DATABASE_URL: databaseURL },
    stdio: "pipe",
  });
}

test("enterprise OIDC completes a real browser code and PKCE flow", async ({ context, page }) => {
  const status = await page.request.get("/api/v1/oidc/status");
  expect(status.ok()).toBeTruthy();
  expect(await status.json()).toEqual({ enabled: true });
  expect(status.headers()["cache-control"]).toContain("no-store");

  await page.goto("/sign-in");
  await expect(page.getByRole("status", { name: "Administrator bootstrap required" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Continue with SSO" })).toHaveCount(0);
  await bootstrapAdmin();
  await page.reload({ waitUntil: "networkidle" });
  await expect(page.getByRole("link", { name: "Continue with SSO" })).toBeVisible();
  await expect(page.getByLabel("Email")).toBeVisible();

  const evidenceRoot = process.env.NEROCD_BROWSER_EVIDENCE_DIR;
  if (evidenceRoot) {
    const evidenceDir = resolve(evidenceRoot);
    await mkdir(evidenceDir, { recursive: true });
    for (const [label, width, height] of [["320", 320, 720], ["390", 390, 844], ["430", 430, 932], ["desktop", 1440, 900]] as const) {
      await page.setViewportSize({ width, height });
      await page.screenshot({ path: join(evidenceDir, `oidc-sign-in-${label}.png`), fullPage: true });
    }
  }

  await page.getByRole("link", { name: "Continue with SSO" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  const browserCookies = await context.cookies();
  const sessionCookie = browserCookies.find((cookie) => cookie.name === "nerocd_session");
  expect(sessionCookie).toMatchObject({ httpOnly: true, sameSite: "Strict", path: "/api/v1" });
  expect(browserCookies.some((cookie) => cookie.name === "nerocd_oidc_flow")).toBeFalsy();

  const me = await page.request.get("/api/v1/me");
  expect(me.ok()).toBeTruthy();
  const principal = await me.json() as { email?: string; roles?: string[] };
  expect(principal.email).toBe("oidc-browser@example.invalid");
  expect(principal.roles).toEqual([]);
  const adminOnly = await page.request.get("/api/v1/operations/status");
  expect(adminOnly.status()).toBe(403);

  if (evidenceRoot) {
    const evidenceDir = resolve(evidenceRoot);
    await mkdir(evidenceDir, { recursive: true });
    await page.screenshot({ path: join(evidenceDir, "oidc-authenticated-desktop.png"), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: join(evidenceDir, "oidc-authenticated-mobile.png"), fullPage: true });
  }

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/sign-in/);
});
