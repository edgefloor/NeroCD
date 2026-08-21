// Browser-only bridge for the real non-root runner acceptance topology.
// Enrollment plaintext is written only to the caller's 0600 temporary file;
// the detail/revoke phase never receives a runner or enrollment credential.
import playwright from "../../web/app/node_modules/playwright-core/index.js";
import { readFile, writeFile } from "node:fs/promises";

const [base, credentialsFile, runnerID, output, phase = "create", expectedFile] = process.argv.slice(2);
if (!base || !credentialsFile || !runnerID || !output) throw new Error("usage: web-enrollment.mjs base credentials-file runner-id output [create|detail-revoke] [expected-telemetry-file]");
const [email, password] = (await readFile(credentialsFile, "utf8")).trim().split("\n");
if (!email || !password) throw new Error("invalid browser credential file");

const browser = await playwright.chromium.launch({ headless: true });
const page = await browser.newPage();
const forbidden = [];
const allowed = [];
const runnerPath = `/api/v1/runners/${encodeURIComponent(runnerID)}`;
await page.route("**/api/v1/runners/**", async (route) => {
  const request = route.request();
  const path = new URL(request.url()).pathname;
  if ((request.method() === "GET" && path === runnerPath) || (request.method() === "POST" && path === "/api/v1/runners/revoke-token")) {
    allowed.push(`${request.method()} ${path}`);
    await route.continue();
    return;
  }
  forbidden.push(`${request.method()} ${path}`);
  await route.abort();
});

async function signIn() {
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

function storageContains(value) {
  return page.evaluate((needle) => [localStorage, sessionStorage].some((storage) => Array.from({ length: storage.length }, (_, index) => `${storage.key(index)}=${storage.getItem(storage.key(index) ?? "")}`).join("\n").includes(needle)), value);
}

async function createEnrollment() {
  await page.goto(`${base}/runners`, { waitUntil: "networkidle" });
  await signIn();
  try { await page.getByRole("heading", { name: "Runners" }).waitFor(); }
  catch { throw new Error(`runner UI did not open after sign-in (${page.url()})`); }
  await page.getByLabel("Runner ID").fill(runnerID);
  await page.getByLabel("Runner name").fill("Runtime web enrollment runner");
  await page.getByLabel("Runner tags").fill("enrollment-runtime");
  await page.getByLabel("Runner capabilities").fill("shell");
  await page.getByRole("button", { name: "Create enrollment" }).click();
  const dialog = page.getByRole("dialog", { name: "One-time runner enrollment secret" });
  await dialog.waitFor();
  const secret = await dialog.getByLabel("Enrollment secret").innerText();
  if (!/^nce_[0-9a-f]{64}$/.test(secret)) throw new Error("invalid enrollment secret shape");
  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download once" }).click();
  const download = await downloaded;
  const downloadedSecret = (await readFile(await download.path(), "utf8")).trim();
  if (downloadedSecret !== secret || page.url().includes(secret) || await storageContains(secret)) throw new Error("browser enrollment secret containment failed");
  await page.keyboard.press("Escape");
  if (await dialog.count() || await page.locator("body").innerText().then((text) => text.includes(secret))) throw new Error("enrollment secret did not clear on close");
  if (forbidden.length) throw new Error("browser used runner-only endpoint");
  await writeFile(output, JSON.stringify({ token: secret, browser_runner_calls: 0, local_storage: 0, session_storage: 0 }), { mode: 0o600 });
}

async function inspectAndRevoke() {
  if (!expectedFile) throw new Error("detail-revoke requires expected telemetry file");
  const expected = JSON.parse(await readFile(expectedFile, "utf8"));
  const detail = page.waitForResponse((response) => response.request().method() === "GET" && new URL(response.url()).pathname === runnerPath && response.status() === 200);
  await page.goto(`${base}/runners/${encodeURIComponent(runnerID)}`, { waitUntil: "networkidle" });
  await signIn();
  const response = await detail;
  const body = await response.json();
  if (body.status !== "active" || !body.telemetry || body.telemetry.journal_depth !== expected.journal_depth || body.telemetry.retry_count !== expected.retry_count || body.telemetry.renew_failures !== expected.renew_failures) throw new Error("admin detail telemetry did not match the authoritative latest bounded observation");
  await page.getByText("Runtime web enrollment runner", { exact: true }).waitFor();
  await page.getByText("Active and recently observed.").waitFor();
  await page.getByText("enrollment-runtime").waitFor();
  await page.getByText("shell", { exact: true }).waitFor();
  if (!await page.locator(`time[datetime="${body.telemetry.observed_at}"]`).count()) throw new Error("detail UI did not render exact telemetry observation time");
  const telemetryText = await page.getByText("Telemetry", { exact: true }).locator("..").innerText();
  if (!telemetryText.includes(`journal ${body.telemetry.journal_depth}`) || !telemetryText.includes(`retries ${body.telemetry.retry_count}`) || !telemetryText.includes(`renewal failures ${body.telemetry.renew_failures}`)) throw new Error("detail UI did not render exact bounded telemetry values");
  await page.keyboard.press("Meta+k");
  if (await page.getByPlaceholder("Type a command or search...").count() === 0) throw new Error("detail keyboard navigation unavailable");
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.reload({ waitUntil: "networkidle" });
  await page.getByText("Active and recently observed.").waitFor();

  await page.getByRole("button", { name: "Revoke credential" }).click();
  const confirmation = page.getByRole("dialog", { name: "Revoke runner credential?" });
  await confirmation.waitFor();
  if (!await page.evaluate(() => Boolean(document.activeElement?.closest('[role="dialog"]')))) throw new Error("revoke confirmation did not take focus");
  await page.keyboard.press("Tab");
  if (!await page.evaluate(() => Boolean(document.activeElement?.closest('[role="dialog"]')))) throw new Error("revoke confirmation did not trap focus");
  await page.getByRole("button", { name: "Keep credential" }).click();
  if (await confirmation.count() || allowed.filter((item) => item === "POST /api/v1/runners/revoke-token").length !== 0) throw new Error("canceling revoke mutated runner credential");

  const revoke = page.waitForResponse((candidate) => candidate.request().method() === "POST" && new URL(candidate.url()).pathname === "/api/v1/runners/revoke-token");
  await page.getByRole("button", { name: "Revoke credential" }).click();
  await page.getByRole("dialog", { name: "Revoke runner credential?" }).getByRole("button", { name: "Revoke credential" }).click();
  if ((await revoke).status() !== 200) throw new Error("public admin revoke failed");
  await page.getByText("Credential revoked; the next authenticated runner operation is denied.").waitFor();
  if (await page.getByRole("button", { name: "Revoke credential" }).count()) throw new Error("revoked UI left credential control enabled");
  await page.reload({ waitUntil: "networkidle" });
  await page.getByText("Credential revoked; the next authenticated runner operation is denied.").waitFor();
  const uiText = await page.locator("body").innerText();
  if (/\bnce_[0-9a-f]{64}\b|\bncr_[0-9a-f]{64}\b/.test(uiText) || /\bnce_[0-9a-f]{64}\b|\bncr_[0-9a-f]{64}\b/.test(page.url()) || await storageContains("nce_") || await storageContains("ncr_")) throw new Error("runner credential leaked to the browser surface");
  if (forbidden.length) throw new Error(`browser used runner-self endpoint (${forbidden.join(",")})`);
  await writeFile(output, JSON.stringify({ telemetry: body.telemetry, admin_runner_calls: allowed.length, runner_self_calls: 0, revoked: true }), { mode: 0o600 });
}

try {
  if (phase === "create") await createEnrollment();
  else if (phase === "detail-revoke") await inspectAndRevoke();
  else throw new Error(`unknown phase ${phase}`);
} finally { await browser.close(); }
