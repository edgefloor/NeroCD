// Browser evidence for the deliberately small System Operations surface.  It
// uses the shipped SPA and cookie-based sign-in; it never manufactures an
// administrative request or calls metrics/runner APIs from the browser.
import playwright from "../../web/app/node_modules/playwright-core/index.js";
import { readFile, writeFile } from "node:fs/promises";

const [base, credentialFile, phase, output] = process.argv.slice(2);
if (!base || !credentialFile || !output || !["required", "none", "success", "unavailable"].includes(phase)) {
  throw new Error("usage: browser.mjs BASE CREDENTIAL_FILE required|none|success|unavailable OUTPUT");
}

const [email, password] = (await readFile(credentialFile, "utf8")).trim().split("\n");
if (!email || !password) throw new Error("invalid operations browser credential file");
const browser = await playwright.chromium.launch({ headless: true });
// The production fixture terminates TLS with a disposable local certificate.
// Chromium still enforces Secure/SameSite cookies; this only trusts that
// fixture certificate, never relaxes CSP or same-origin enforcement.
const context = await browser.newContext({ viewport: { width: 390, height: 844 }, ignoreHTTPSErrors: true });
const page = await context.newPage();
const pageErrors = [];
const forbidden = [];
page.on("pageerror", (error) => pageErrors.push(error.message));
await page.route("**/api/v1/runners/**", async (route) => { forbidden.push(new URL(route.request().url()).pathname); await route.abort(); });
await page.route("**/metrics", async (route) => { forbidden.push(new URL(route.request().url()).pathname); await route.abort(); });

try {
  if (phase === "required") {
    const response = await page.goto(`${base}/sign-in`, { waitUntil: "networkidle" });
    if (!response?.ok()) throw new Error("operations pre-bootstrap shell unavailable");
    const bootstrapResponse = await page.evaluate(async () => {
      const response = await fetch("/api/v1/bootstrap-status");
      return { status: response.status, body: await response.text() };
    });
    if (bootstrapResponse.status !== 200 || bootstrapResponse.body !== '{"status":"required"}\n') throw new Error(`bootstrap status admission failed (${bootstrapResponse.status})`);
    try {
      await page.getByRole("status", { name: "Administrator bootstrap required" }).waitFor();
    } catch {
      const body = (await page.locator("body").innerText()).slice(0, 240).replace(/\s+/g, " ");
      throw new Error(`CLI-only bootstrap guidance absent (body=${body})`);
    }
    if (!(await page.getByText("Bootstrap is intentionally CLI-only.").isVisible())) throw new Error("CLI-only bootstrap guidance absent");
    if (await page.getByLabel("Email").count() || await page.getByLabel("Password").count() || await page.getByRole("button", { name: "Sign in" }).count()) {
      throw new Error("pre-bootstrap UI exposed browser login controls");
    }
    const bootstrap = await page.evaluate(async () => (await fetch("/api/v1/bootstrap-status")).json());
    if (bootstrap.status !== "required" || Object.keys(bootstrap).length !== 1) throw new Error("bootstrap status was not fixed required-only data");
  } else {
    await page.goto(`${base}/operations`, { waitUntil: "networkidle" });
    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    await page.getByRole("button", { name: "Sign in" }).click();
    try {
      if (phase === "unavailable") await page.getByText("Operations status is temporarily unavailable").waitFor();
      else await page.getByRole("heading", { name: "System Operations" }).waitFor();
    } catch {
      const body = (await page.locator("body").innerText()).slice(0, 320).replace(/\s+/g, " ");
      const cookies = (await context.cookies()).map((cookie) => `${cookie.name}:${cookie.secure}`).join(",");
      throw new Error(`browser sign-in did not reach operations (body=${body}; cookies=${cookies})`);
    }
    await page.keyboard.press("Meta+k");
    await page.getByPlaceholder("Type a command or search...").waitFor();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "Open mobile navigation" }).click();
    await page.getByRole("dialog", { name: "Mobile navigation" }).getByRole("link", { name: "Operations" }).waitFor();
    await page.keyboard.press("Escape");
    if (phase === "unavailable") {
      if (await page.locator("body").innerText().then((body) => /postgres:\/\/|SQLSTATE|relation .* does not exist|nerocd_repository_policy_schema_compatible/.test(body))) throw new Error("unavailable operations UI disclosed a partial diagnostic");
    } else {
      const backupCard = page.getByText("Backup", { exact: true }).locator("xpath=../..");
      await backupCard.getByText(phase === "success" ? "success" : "none", { exact: true }).waitFor();
      await page.reload({ waitUntil: "networkidle" });
      await page.getByRole("heading", { name: "System Operations" }).waitFor();
    }
  }
  const body = await page.locator("body").innerText();
  if (body.includes(password) || forbidden.length || pageErrors.length) throw new Error(`operations browser disclosure or forbidden request (forbidden=${forbidden.length} page_errors=${pageErrors.length})`);
  await writeFile(output, JSON.stringify({ phase, runner_or_metrics_calls: 0, page_errors: 0, mobile: phase !== "required", keyboard: phase !== "required" }), { mode: 0o600 });
} finally {
  await context.close();
  await browser.close();
}
