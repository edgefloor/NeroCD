// Resolve the repository-pinned browser runtime, not Bun's ambient package.
import playwright from "../../web/app/node_modules/playwright-core/index.js";
const { chromium } = playwright;
import { chmod, readFile, rename, unlink, writeFile } from "node:fs/promises";

const [configPath, phase, resultPath] = process.argv.slice(2);
if (!configPath || !resultPath || !["b", "c"].includes(phase)) throw new Error("usage: operator.mjs config.json b|c result.json");
const failureClasses = new Set(["navigation", "csp", "session", "ui_action", "deployment_creation", "deployment_terminal_failure", "deployment_terminal_timeout", "unavailable"]);
const resultTempPath = `${resultPath}.tmp-${process.pid}`;
let browser;
let page;
let stage = "unavailable";

async function writeResult(payload) {
  await writeFile(resultTempPath, JSON.stringify(payload, null, 2), { mode: 0o600 });
  await chmod(resultTempPath, 0o600);
  await rename(resultTempPath, resultPath);
}

async function writeFailure(failureClass) {
  await writeResult({ failure_class: failureClasses.has(failureClass) ? failureClass : "unavailable" });
}

async function waitForLocation(pattern, timeout = 10_000) {
  try {
    await page.waitForFunction(
      (source) => new RegExp(source).test(`${location.pathname}${location.search}`),
      pattern.source,
      { timeout },
    );
  } catch (cause) {
    throw new Error(`location ${page.url()} did not match ${pattern}: ${cause.message}`);
  }
}

try {
  const config = JSON.parse(await readFile(configPath, "utf8"));
  const [email, password] = (await readFile(config.credential_file, "utf8")).trim().split("\n");
  if (!email || !password) throw new Error("operator credential file is invalid");
  browser = await chromium.launch({ headless: true });
  page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const runnerCalls = [];
  const pageErrors = [];
  const consoleErrors = [];
  const consoleResourceDiagnostics = [];
  let cspExternalRequests = 0;
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("console", (message) => {
    // Chromium reports ordinary HTTP 404 diagnostics as console errors. Route
    // and page exceptions remain hard failures; a response failure is asserted
    // by the API/browser requests that need it rather than this noisy channel.
    if (message.type() !== "error") return;
    if (/^Failed to load resource/.test(message.text())) {
      consoleResourceDiagnostics.push(message.text());
      return;
    }
    consoleErrors.push(message.text());
  });
  await page.route("**/api/v1/runners/**", async (route) => {
    runnerCalls.push(route.request().url());
    await route.abort();
  });
  await page.route("https://csp-probe.invalid/**", async (route) => {
    cspExternalRequests += 1;
    await route.fulfill({ contentType: "application/javascript", body: "globalThis.__nerocdCSPExternal = true" });
  });
  stage = "navigation";
  const shell = await page.goto(`${config.base}/deployments?q=operator`, { waitUntil: "networkidle" });
  stage = "csp";
  const csp = shell?.headers()["content-security-policy"];
  const expectedCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; object-src 'none'";
  if (csp !== expectedCSP) throw new Error("operator shell CSP is not the approved policy");
  const consoleErrorsBeforeCSPProbe = consoleErrors.length;
  const cspProbe = await page.evaluate(async () => {
    const violations = [];
    document.addEventListener("securitypolicyviolation", (event) => violations.push({ directive: event.effectiveDirective, blocked: event.blockedURI }));
    delete globalThis.__nerocdCSPInline;
    delete globalThis.__nerocdCSPExternal;
    const inline = document.createElement("script"); inline.text = "globalThis.__nerocdCSPInline = true"; document.head.append(inline);
    const external = document.createElement("script"); external.src = "https://csp-probe.invalid/blocked.js"; document.head.append(external);
    await new Promise((resolve) => setTimeout(resolve, 100));
    return {
      inline: globalThis.__nerocdCSPInline === true,
      external: globalThis.__nerocdCSPExternal === true,
      violations,
    };
  });
  if (cspProbe.inline || cspProbe.external || cspExternalRequests || !cspProbe.violations.some((item) => item.blocked.includes("inline") && item.directive.startsWith("script-src")) || !cspProbe.violations.some((item) => item.blocked.includes("csp-probe.invalid") && item.directive.startsWith("script-src"))) throw new Error("operator browser CSP executable-script probe was not blocked");
  // CSP violations are the expected, independently validated result of the
  // two probe scripts; do not treat those diagnostics as application errors.
  consoleErrors.splice(consoleErrorsBeforeCSPProbe);
  stage = "session";
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("heading", { name: "Deployments" }).waitFor();
  stage = "ui_action";
  await page.getByLabel("Project").selectOption(config.project_id);
  await page.getByLabel("Service").selectOption(config.service_id);
  await page.getByLabel("Environment").selectOption(config.environment_id);
  await page.getByLabel("Immutable revision").selectOption(phase === "b" ? config.revision_b : config.revision_c);
  stage = "deployment_creation";
  await page.getByRole("button", { name: "Request deployment" }).click();
  await page.waitForURL(/\/deployments\/dep_[A-Za-z0-9_-]+/);
  const deploymentID = page.url().split("/").pop()?.split("?")[0];
  if (!deploymentID?.startsWith("dep_")) throw new Error("deployment detail navigation did not contain a deployment ID");
  await page.getByText(`Deployment ${deploymentID}`).waitFor();
  // An immediate terminal recovery is still a terminal deployment outcome;
  // keep the active-state assertion, but classify a fixed public failure
  // message distinctly instead of misreporting deployment creation.
  stage = "deployment_terminal_timeout";
  await page.getByText(/Queued for an eligible runner|Preparing the immutable|Applying the declared|Verifying health/).waitFor({ timeout: 30_000 });
  const terminal = phase === "b" ? /Healthy revision verified/ : /Rollback completed/;
  await page.getByText(terminal).waitFor({ timeout: 90_000 });
  stage = "ui_action";
  // The public detail endpoint supports a direct, query-free operator URL.
  // Reload that exact URL before inspecting provenance so cached list state
  // cannot mask the immutable receipt rendered by the detail page.
  await page.goto(`${config.base}/deployments/${deploymentID}`, { waitUntil: "networkidle" });
  await page.reload({ waitUntil: "networkidle" });
  await waitForLocation(new RegExp(`/deployments/${deploymentID}$`));
  await page.getByText(`Deployment ${deploymentID}`).waitFor();
  if (phase === "b") {
    await page.getByText(config.commit_b).first().waitFor();
    await page.getByText("Compose hash").waitFor();
    await page.getByText("Image digests").waitFor();
    await page.getByRole("link", { name: /Run / }).waitFor();
  }
  // Exercise query preservation and browser history with ordinary document
  // navigations. This avoids treating an SPA no-op history entry as proof.
  await page.goto(`${config.base}/deployments?q=operator`, { waitUntil: "networkidle" });
  await page.getByRole("heading", { name: "Deployments" }).waitFor();
  await page.goBack({ waitUntil: "commit", timeout: 10_000 }).catch(() => null);
  await waitForLocation(new RegExp(`/deployments/${deploymentID}$`));
  await page.getByText(`Deployment ${deploymentID}`).waitFor();
  await page.goForward({ waitUntil: "commit", timeout: 10_000 }).catch(() => null);
  await waitForLocation(/\/deployments\/?\?q=operator/);
  await page.getByRole("button", { name: "Open mobile navigation" }).click();
  await page.getByRole("dialog", { name: "Mobile navigation" }).getByRole("link", { name: "Deployments" }).waitFor();
  await page.keyboard.press("Escape");
  const body = await page.locator("body").innerText();
  for (const forbidden of [password, config.token_marker]) if (forbidden && body.includes(forbidden)) throw new Error("operator page disclosed a secret");
  if (runnerCalls.length || pageErrors.length || consoleErrors.length) {
    const consoleKinds = [...new Set(consoleErrors.map((message) => message.split(":", 1)[0].slice(0, 48)))].join(",");
    throw new Error(`operator page made a forbidden runner call or emitted a browser error (runner_calls=${runnerCalls.length}, page_errors=${pageErrors.length}, console_errors=${consoleErrors.length}, kinds=${consoleKinds})`);
  }
  await writeResult({ deployment_id: deploymentID, phase, polling: true, reload: true, history: true, mobile: true, runner_calls: 0, page_errors: 0, console_nonresource_errors: 0, console_resource_diagnostics: consoleResourceDiagnostics.length, csp_inline_script_blocked: true, csp_external_script_blocked: true });
} catch (cause) {
  let failureClass = stage;
  if (stage === "deployment_terminal_timeout" && page) {
    const terminalFailure = await page.getByText(/Rollback failed\. Operator intervention is required\.|Automatic recovery is unsafe\. Operator intervention is required\.|Deployment failed before a safe terminal recovery\.|Canceled before terminal success\./).isVisible().catch(() => false);
    if (terminalFailure) failureClass = "deployment_terminal_failure";
  }
  try {
    await writeFailure(failureClass);
  } catch {
    // The gate treats a missing or unsafe result as unavailable; never expose
    // exception data, browser diagnostics, or configuration values here.
  }
  throw cause;
} finally {
  if (browser) await browser.close();
  await unlink(resultTempPath).catch(() => undefined);
}
