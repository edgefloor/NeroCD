// Browser evidence for the deliberately small System Operations surface.  It
// uses the shipped SPA and cookie-based sign-in; it never manufactures an
// administrative request or calls metrics/runner APIs from the browser.
import playwright from "../../web/app/node_modules/playwright-core/index.js";
import { readFile, writeFile } from "node:fs/promises";

const [base, credentialFile, phase, output] = process.argv.slice(2);
if (!base || !credentialFile || !output || !["required", "none", "success", "unavailable"].includes(phase)) {
  throw new Error("usage: browser.mjs BASE CREDENTIAL_FILE required|none|success|unavailable OUTPUT");
}

const allowedOutcomes = new Set(["none", "success", "failure"]);
const allowedCheckpoints = new Set(["browser_launch", "required_navigation", "bootstrap_status", "sign_in_navigation", "sign_in_submit", "operations_view", "command_palette", "mobile_navigation", "backup_card", "operations_reload", "disclosure_check"]);
let checkpoint = "browser_launch";
let operationsStatus = 0;
let backupOutcome = null;
let operationsObservation = Promise.resolve();
const pageErrors = [];
const forbidden = [];
let browser;
let context;
let email;
let password;

try {
  [email, password] = (await readFile(credentialFile, "utf8")).trim().split("\n");
  if (!email || !password) throw new Error("invalid operations browser credential file");
  browser = await playwright.chromium.launch({ headless: true });
  // The production fixture terminates TLS with a disposable local certificate.
  // Chromium still enforces Secure/SameSite cookies; this only trusts that
  // fixture certificate, never relaxes CSP or same-origin enforcement.
  context = await browser.newContext({ viewport: { width: 390, height: 844 }, ignoreHTTPSErrors: true });
  const page = await context.newPage();
  page.on("pageerror", () => pageErrors.push(true));
  page.on("response", (response) => {
    if (new URL(response.url()).pathname !== "/api/v1/operations/status") return;
    operationsStatus = response.status();
    operationsObservation = response.json().then((payload) => {
      const outcome = payload?.snapshot?.backup_outcome;
      if (allowedOutcomes.has(outcome)) backupOutcome = outcome;
    }).catch(() => {});
  });
  await page.route("**/api/v1/runners/**", async (route) => { forbidden.push(true); await route.abort(); });
  await page.route("**/metrics", async (route) => { forbidden.push(true); await route.abort(); });
  if (phase === "required") {
    checkpoint = "required_navigation";
    const response = await page.goto(`${base}/sign-in`, { waitUntil: "networkidle" });
    if (!response?.ok()) throw new Error("operations pre-bootstrap shell unavailable");
    checkpoint = "bootstrap_status";
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
    checkpoint = "sign_in_navigation";
    await page.goto(`${base}/operations`, { waitUntil: "networkidle" });
    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    checkpoint = "sign_in_submit";
    await page.getByRole("button", { name: "Sign in" }).click();
    checkpoint = "operations_view";
    try {
      if (phase === "unavailable") await page.getByText("Operations status is temporarily unavailable").waitFor();
      else await page.getByRole("heading", { name: "System Operations" }).waitFor();
    } catch {
      const body = (await page.locator("body").innerText()).slice(0, 320).replace(/\s+/g, " ");
      const cookies = (await context.cookies()).map((cookie) => `${cookie.name}:${cookie.secure}`).join(",");
      throw new Error(`browser sign-in did not reach operations (body=${body}; cookies=${cookies})`);
    }
    checkpoint = "command_palette";
    await page.keyboard.press("Meta+k");
    await page.getByPlaceholder("Type a command or search...").waitFor();
    await page.keyboard.press("Escape");
    checkpoint = "mobile_navigation";
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "Open mobile navigation" }).click();
    await page.getByRole("dialog", { name: "Mobile navigation" }).getByRole("link", { name: "Operations" }).waitFor();
    await page.keyboard.press("Escape");
    if (phase === "unavailable") {
      if (await page.locator("body").innerText().then((body) => /postgres:\/\/|SQLSTATE|relation .* does not exist|nerocd_repository_policy_schema_compatible/.test(body))) throw new Error("unavailable operations UI disclosed a partial diagnostic");
    } else {
      checkpoint = "backup_card";
      const backupCard = page.getByText("Backup", { exact: true }).locator("xpath=../..");
      await backupCard.getByText(phase === "success" ? "success" : "none", { exact: true }).waitFor();
      backupOutcome = phase === "success" ? "success" : "none";
      checkpoint = "operations_reload";
      await page.reload({ waitUntil: "networkidle" });
      await page.getByRole("heading", { name: "System Operations" }).waitFor();
    }
  }
  checkpoint = "disclosure_check";
  const body = await page.locator("body").innerText();
  if (body.includes(password) || forbidden.length || pageErrors.length) throw new Error(`operations browser disclosure or forbidden request (forbidden=${forbidden.length} page_errors=${pageErrors.length})`);
  await writeFile(output, JSON.stringify({ phase, runner_or_metrics_calls: 0, page_errors: 0, mobile: phase !== "required", keyboard: phase !== "required" }), { mode: 0o600 });
} catch {
  await operationsObservation;
  const diagnostic = {
    phase,
    checkpoint: allowedCheckpoints.has(checkpoint) ? checkpoint : "browser_launch",
    http_status: Number.isInteger(operationsStatus) ? operationsStatus : 0,
    backup_outcome: allowedOutcomes.has(backupOutcome) ? backupOutcome : null,
    error_count: pageErrors.length,
    forbidden_request_count: forbidden.length,
  };
  console.error(`NEROCD_BROWSER_DIAGNOSTIC ${JSON.stringify(diagnostic)}`);
  process.exitCode = 1;
} finally {
  try { await context?.close(); } catch {}
  try { await browser?.close(); } catch {}
}
