// Connected browser against the candidate Go-served UI; no route interception.
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { createHash, randomBytes } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
const require = createRequire(resolve("web/app/package.json"));
const { chromium, expect, request } = require("@playwright/test");
const origin = process.env.NEROCD_CATALOG_BROWSER_ORIGIN;
const key = process.env.NEROCD_CATALOG_BROWSER_KEY;
const evidence = process.env.NEROCD_CATALOG_HOST_EVIDENCE;
const privateRegistry = process.env.NEROCD_CATALOG_APPLICATION === "private-registry";
const kuma = process.env.NEROCD_CATALOG_APPLICATION === "uptime-kuma";
const actual = process.env.NEROCD_CATALOG_APPLICATION === "actualbudget";
const appID = actual ? "actualbudget" : kuma ? "uptime-kuma" : "linkding";
const applicationName = actual ? "Actual Budget acceptance" : kuma ? "Uptime Kuma acceptance" : "Linkding acceptance";
const appOrigin = "https://catalog-runtime.example.test:25443";
const applicationPassword = randomBytes(24).toString("hex");
if (
  !/^https:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin ?? "") ||
  !/^[a-f0-9]{64}$/.test(key ?? "") ||
  !evidence?.startsWith("/")
)
  throw Error("exact private browser profile required");
let api,
  browser,
  context,
  viewerContext,
  appContext,
  page,
  stopped = false;
const closing = new Map(),
  failures = new Set();
async function closeOwned() {
  for (const [name, value, method] of [
    ["app", appContext, "close"],
    ["viewer", viewerContext, "close"],
    ["context", context, "close"],
    ["browser", browser, "close"],
    ["api", api, "dispose"],
  ]) {
    if (!value) continue;
    if (!closing.has(name))
      closing.set(
        name,
        Promise.resolve().then(() => value[method]()),
      );
    const [r] = await Promise.allSettled([closing.get(name)]);
    if (r.status === "rejected") failures.add(name);
  }
  return failures.size === 0;
}
function stop(code) {
  if (stopped) return;
  stopped = true;
  process.exitCode = code;
  void closeOwned();
}
const interrupt = () => stop(130),
  terminate = () => stop(143);
process.once("SIGINT", interrupt);
process.once("SIGTERM", terminate);
const deadline = Date.now() + 20 * 60 * 1000,
  timer = setTimeout(() => stop(1), 20 * 60 * 1000);
timer.unref();
const safe = { status: "failed", intercepted_requests: false };
let phase = "startup";
let restoredDeploymentStep = "";
async function state(expected) {
  phase = expected;
  while (Date.now() < deadline && !stopped) {
    try {
      const r = await api.get("/__catalog_fixture/state");
      if (r.status() === 200) {
        const v = await r.json();
        if (v.phase === expected) return v;
      }
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw Error("fixture deadline");
}
async function report(action, id) {
  const r = await api.post("/__catalog_fixture/report", {
    data: { action, id },
  });
  if (r.status() !== 204) throw Error("fixture report rejected");
}
async function signIn(p, email, password) {
  await p.goto("/sign-in");
  await p.getByLabel("Email", { exact: true }).fill(email);
  await p.getByLabel("Password", { exact: true }).fill(password);
  await p.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    p.getByRole("button", { name: "Account", exact: true }),
  ).toBeVisible();
}
async function hosting(p) {
  await p.goto("/projects");
  const h = p.getByRole("region", { name: "Project hosting", exact: true });
  await h
    .getByLabel("Hosting project", { exact: true })
    .selectOption("proj_platform");
  await h
    .getByRole("button", { name: "storage runtime fixture", exact: true })
    .click();
  return h;
}
const response = (p, path) =>
  p.waitForResponse(
    (r) =>
      r.request().method() === "POST" && new URL(r.url()).pathname === path,
  );
async function actualTransaction(p, note, amount) {
  await p.getByRole("button", { name: "Add New", exact: true }).click();
  const row = p.getByTestId("row").first();
  await row.getByTestId("notes").click();
  await p.getByRole("combobox", { name: "Notes", exact: true }).fill(note);
  await row.getByTestId("debit").click();
  await row.getByTestId("debit").locator("input").fill(amount);
  await p.getByTestId("add-button").click();
  await expect(p.getByTestId("transaction-table")).toContainText(note);
  await p.getByRole("button", { name: "Cancel", exact: true }).click();
}
async function actualSync(p) {
  const waiting = p.waitForResponse(async r => new URL(r.url()).pathname === "/sync/sync" && r.request().method() === "POST" && await r.finished() === null);
  await p.getByRole("button", { name: "Server Sync", exact: true }).click();
  if ((await waiting).status() !== 200) throw Error("Actual sync rejected");
}
async function actualOpenServerBudget(p) {
  await p.goto("/");
  await p.getByPlaceholder("Password", { exact: true }).fill(applicationPassword);
  await p.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(p.getByText("Available for download", { exact: true })).toBeVisible();
  const download = p.waitForResponse(async r => new URL(r.url()).pathname === "/sync/download-user-file" && r.request().method() === "GET" && await r.finished() === null);
  await p.getByText("NeroCD retained budget", { exact: true }).click();
  if ((await download).status() !== 200) throw Error("Actual server budget download rejected");
  await p.getByRole("link", { name: /NeroCD retained account/ }).click();
  await expect(p.getByTestId("transaction-table")).toContainText("NeroCD retained transaction");
}
async function kumaMonitor(p, name) {
  await p.getByRole("link", { name: "Add New Monitor", exact: true }).click();
  // Hidden notification dialogs share labels; these are the actual monitor IDs.
  await p.locator("#name").fill(name);
  await p.locator("#url").fill("http://127.0.0.1:3001/");
  await p.locator("#monitor-submit-btn").click();
  await expect(p.getByRole("heading", { level: 1 })).toContainText(name);
  const id = new URL(p.url()).pathname.match(/^\/dashboard\/([1-9][0-9]*)$/)?.[1];
  if (!id) throw Error("monitor identity absent");
  await expect(p.getByRole("cell", { name: "Up", exact: true }).first()).toBeVisible({ timeout: 30000 });
  return id;
}
async function submitCatalogRoute(application, name, resourceID, operationID) {
  const routes = application.getByRole("region", {
    name: `Routes for ${name}`,
    exact: true,
  });
  await routes
    .getByRole("button", { name: "Refresh resource routes", exact: true })
    .click();
  const routeForm = routes.getByRole("form", {
    name: `Configure route for ${name}`,
    exact: true,
  });
  await routeForm
    .getByLabel("Route hostname", { exact: true })
    .fill("catalog-runtime.example.test");
  await routeForm
    .getByLabel("Expected settled healthy deployment", { exact: true })
    .selectOption(operationID);
  const routeWait = response(
    page,
    `/api/v1/resources/${resourceID}/routes`,
  );
  await routeForm
    .getByRole("button", { name: "Save desired route", exact: true })
    .click();
  const routed = await routeWait;
  if (routed.status() !== 202) throw Error("route request rejected");
  const route = await routed.json();
  safe.browser_route_selection = true;
  await report("routed", route.operation_id);
}
async function privateRegistryDeploy(initial) {
  await report("installed", initial.id);
  let last;
  for (const [phaseName, action, name, port, stored] of [
    ["deploy", "deployed", "Private registry stateless", "20101", false],
    ["restore_deploy", "restored_deployed", "Private registry uploads", "20102", true],
  ]) {
    const stateValue = await state(phaseName);
    const h = await hosting(page);
    const application = h.getByRole("article", { name, exact: true });
    const form = stored ? application.getByRole("region", { name: "Persistent upload deployment review", exact: true }) : application.getByRole("form", { name: `Deploy image for ${name}`, exact: true });
    const credentials = form.getByRole("region", { name: "Image registry credentials", exact: true });
    restoredDeploymentStep = "select_registry_access";
    await credentials.getByLabel("Image access", { exact: true }).selectOption("private");
    const option = credentials.getByLabel("Registry credential secret", { exact: true }).getByRole("option", { name: /^private registry correct · / });
    await expect(option).toHaveCount(1);
    const secretID = await option.getAttribute("value");
    restoredDeploymentStep = "select_registry_secret";
    await credentials.getByLabel("Registry credential secret", { exact: true }).selectOption(secretID);
    restoredDeploymentStep = "select_registry_version";
    await credentials.getByLabel("Registry credential version", { exact: true }).selectOption("1");
    for (const width of [1280, 320, 390, 430]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(credentials.getByLabel("Registry credential version", { exact: true })).toBeVisible();
      if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw Error("registry control responsive overflow");
    }
    safe.responsive_widths = [1280, 320, 390, 430];
    await page.screenshot({ path: resolve(evidence, `registry-${stored ? "storage" : "stateless"}-430.png`), fullPage: true });
    await page.setViewportSize({ width: 1280, height: 900 });
    await form.getByLabel("Container port", { exact: true }).fill("8080");
    if (!stored) await form.getByLabel("Host port allocation", { exact: true }).selectOption("fixed");
    await form.getByLabel("Runner-local host port", { exact: true }).fill(port);
    await form.getByLabel("Readiness path", { exact: true }).fill("/");
    if (stored) await form.getByRole("button", { name: "Review current storage deployment", exact: true }).click();
    restoredDeploymentStep = "acknowledge_deployment";
    await form.getByRole("checkbox").check();
    const waiting = response(page, `/api/v1/resources/${stateValue.id}/deployments`);
    restoredDeploymentStep = "submit_deployment";
    await form.getByRole("button", { name: stored ? "Deploy reviewed image with uploads" : "Request image deployment", exact: true }).click();
    const result = await waiting;
    if (result.status() !== 202) throw Error("selected registry deployment rejected");
    const submitted = result.request().postDataJSON();
    if (submitted.registry_credential?.secret_id !== secretID || submitted.registry_credential?.version !== 1 || submitted.expected_runtime_configuration_generation !== 1) throw Error("registry request lost selected immutable inputs");
    const operation = await result.json();
    if (operation.snapshot?.registry_credential?.secret_id !== secretID || operation.snapshot?.registry_credential?.version !== 1) throw Error("registry snapshot lost selected reference");
    safe.registry_credential_selected = true;
    safe.registry_snapshot_pinned = true;
    safe.browser_deployment = true;
    safe[stored ? "browser_storage_registry_deployment" : "browser_stateless_registry_deployment"] = true;
    last = operation.id;
    await report(action, operation.id);
  }
  const routed = await state("route");
  const h = await hosting(page);
  await submitCatalogRoute(h.getByRole("article", { name: "Private registry uploads", exact: true }), "Private registry uploads", routed.id, last);
  await state("complete");
  await report("done", last);
}
try {
  api = await request.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { "X-NeroCD-Fixture-Key": key },
    timeout: 10000,
  });
  browser = await chromium.launch({ headless: true, args: kuma || actual ? ["--host-resolver-rules=MAP catalog-runtime.example.test 127.0.0.1", "--no-proxy-server"] : [] });
  context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    viewport: { width: 1280, height: 900 },
  });
  page = await context.newPage();
  page.setDefaultTimeout(30000);
  const initial = await state("install");
  const manifest = JSON.parse(
    await readFile(resolve(evidence, "browser-ui-manifest.json"), "utf8"),
  );
  if (
    manifest.schema !== "nerocd.ui-assets/v1" ||
    !manifest.files["index.html"]
  )
    throw Error("candidate manifest absent");
  for (const [path, sha] of Object.entries(manifest.files)) {
    if (path === ".gitkeep") continue;
    const r = await api.get("/" + path);
    if (
      r.status() !== 200 ||
      createHash("sha256")
        .update(await r.body())
        .digest("hex") !== sha
    )
      throw Error("candidate asset mismatch");
  }
  safe.served_assets_match_manifest = true;
  await signIn(page, "admin@example.local", "admin");
  if (privateRegistry) {
    await privateRegistryDeploy(initial);
  } else {
  let h = await hosting(page);
  const catalog = h.getByRole("region", {
    name: "Hosting catalog",
    exact: true,
  });
  await expect(catalog).toBeVisible();
  await catalog
    .getByLabel("Catalog application", { exact: true })
    .selectOption(appID);
  await catalog
    .getByLabel("Installation name", { exact: true })
    .fill(applicationName);
  await catalog
    .getByLabel("Catalog destination", { exact: true })
    .selectOption(initial.id);
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(
      catalog.getByRole("button", { name: "Install application", exact: true }),
    ).toBeVisible();
    if (
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      )
    )
      throw Error("responsive overflow");
  }
  safe.responsive_widths = [1280, 320, 390, 430];
  await page.setViewportSize({ width: 1280, height: 900 });
  const firstWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog
    .getByRole("button", { name: "Install application", exact: true })
    .click();
  const first = await firstWait;
  if (first.status() !== 503) throw Error("committed response fault missing");
  const original = first.request().postData();
  await expect(catalog.getByRole("alert")).toContainText(
    "Installation unconfirmed",
  );
  await expect(
    catalog.getByLabel("Installation name", { exact: true }),
  ).toBeDisabled();
  const retryWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog
    .getByRole("button", { name: "Retry same installation", exact: true })
    .click();
  const retry = await retryWait;
  if (retry.status() !== 201 || retry.request().postData() !== original)
    throw Error("retry changed request");
  const installed = await retry.json();
  await expect(catalog).toContainText(`Installed ${applicationName}`);
  await expect(catalog.locator("output")).toHaveCount(0);
  if (kuma || actual) {
    if (!Array.isArray(installed.credentials) || installed.credentials.length !== 0) throw Error("unexpected generated credentials");
    await expect(catalog.getByRole("button", { name: "Reveal login credentials", exact: true })).toHaveCount(0);
    await expect(catalog).toContainText("No login credentials were generated.");
    safe.credential_free_installation = true;
  } else {
  const revealWait = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      /\/managed-secrets\/[^/]+\/reveal$/.test(new URL(r.url()).pathname),
  );
  await catalog
    .getByRole("button", { name: "Reveal login credentials", exact: true })
    .click();
  if ((await revealWait).status() !== 200)
    throw Error("explicit reveal failed");
  await expect(catalog.locator("output")).toHaveCount(2);
  await catalog
    .getByRole("button", { name: "Hide credentials", exact: true })
    .click();
  await expect(catalog.locator("output")).toHaveCount(0);
  }
  safe.catalog_install_identical_retry = true;
  if (!kuma && !actual) safe.explicit_reveal_and_hide = true;
  await report("installed", installed.resource.id);
  await state("deploy");
  const application = h.getByRole("article", {
    name: applicationName,
    exact: true,
  });
  const review = application.getByRole("region", {
    name: "Persistent upload deployment review",
    exact: true,
  });
  await expect(
    review.getByLabel("Container port", { exact: true }),
  ).toHaveValue(actual ? "5006" : kuma ? "3001" : "9090");
  await expect(
    review.getByLabel("Readiness path", { exact: true }),
  ).toHaveValue(kuma ? "/" : "/health");
  await review
    .getByLabel("Runner-local host port", { exact: true })
    .fill("20101");
  await review
    .getByRole("button", {
      name: "Review current storage deployment",
      exact: true,
    })
    .click();
  await review.getByRole("checkbox").check();
  const deployWait = response(
    page,
    `/api/v1/resources/${installed.resource.id}/deployments`,
  );
  await review
    .getByRole("button", {
      name: "Deploy reviewed image with uploads",
      exact: true,
    })
    .click();
  const deployed = await deployWait;
  if (deployed.status() !== 202) throw Error("browser deployment rejected");
  const operation = await deployed.json();
  await report("deployed", operation.id);
  safe.browser_deployment = true;
  await state("healthy");
  const deployments = application.getByRole("region", {
    name: `Image deployments for ${applicationName}`,
    exact: true,
  });
  await deployments
    .getByRole("button", { name: "Refresh deployment history", exact: true })
    .click();
  await deployments
    .getByRole("button", {
      name: `View operation ${operation.id}`,
      exact: true,
    })
    .click();
  const detail = deployments.getByRole("region", {
    name: "Deployment operation detail",
    exact: true,
  });
  await expect(detail).toContainText("Succeeded");
  await expect(
    detail.getByRole("region", {
      name: "Recorded deployment result",
      exact: true,
    }),
  ).toContainText("Readiness recorded at completion: passed");
  safe.healthy_resource_result = true;
  viewerContext = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: true,
    viewport: { width: 390, height: 844 },
  });
  const viewer = await viewerContext.newPage();
  await signIn(viewer, "viewer@example.local", "viewer");
  const vh = await hosting(viewer);
  await expect(vh).toContainText(
    "Project owner access is required to install catalog applications.",
  );
  await expect(
    vh.getByRole("button", { name: "Install application", exact: true }),
  ).toHaveCount(0);
  await expect(
    vh.getByRole("article", { name: applicationName, exact: true }),
  ).toBeVisible();
  const denial = await viewerContext.request.post(
    "/api/v1/hosting-catalog/installations",
    {
      headers: { "X-NeroCD-CSRF": "1", Origin: origin },
      data: JSON.parse(original),
    },
  );
  if (denial.status() !== 403) throw Error("viewer replay not denied");
  safe.current_viewer_denied_replay = true;
  await report("healthy", operation.id);
  await state("route");
  await submitCatalogRoute(application, applicationName, installed.resource.id, operation.id);
  if (actual) {
    await state("app_create");
    appContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, locale: "en-US" });
    let app = await appContext.newPage();
    app.setDefaultTimeout(30000);
    await app.goto("/");
    await expect(app.getByPlaceholder("Confirm password", { exact: true })).toBeVisible();
    const startup = await app.evaluate(() => ({ secure: isSecureContext, isolated: crossOriginIsolated, shared: typeof SharedArrayBuffer === "function" }));
    if (!startup.secure || !startup.isolated || !startup.shared) throw Error("Actual secure context unavailable");
    safe.actual_secure_context = true;
    await app.getByPlaceholder("Password", { exact: true }).fill(applicationPassword);
    await app.getByPlaceholder("Confirm password", { exact: true }).fill(applicationPassword);
    await app.getByRole("button", { name: "OK", exact: true }).click();
    await expect(app.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
    await app.getByPlaceholder("Password", { exact: true }).fill(applicationPassword);
    await app.getByRole("button", { name: "Sign in", exact: true }).click();
    await app.getByRole("button", { name: "Start budgeting", exact: true }).click();
    await app.getByRole("button", { name: "Close", exact: true }).click();
    await app.getByRole("button", { name: "My Finances", exact: true }).click();
    await app.getByText("Rename budget", { exact: true }).click();
    await app.getByRole("textbox").fill("NeroCD retained budget");
    await app.getByRole("textbox").press("Enter");
    await app.getByRole("button", { name: "Add account", exact: true }).click();
    await app.getByRole("button", { name: "Create a local account", exact: true }).click();
    await app.getByLabel("Name:", { exact: true }).fill("NeroCD retained account");
    await app.getByLabel("Balance:", { exact: true }).fill("100");
    await app.getByRole("button", { name: "Create", exact: true }).click();
    await app.getByRole("link", { name: /NeroCD retained account/ }).click();
    await actualTransaction(app, "NeroCD retained transaction", "12.34");
    await expect(app.getByTestId("account-balance")).toContainText("87.66");
    await actualSync(app);
    const accountID = new URL(app.url()).pathname.match(/\/accounts\/([a-f0-9-]{36})$/)?.[1];
    if (!accountID) throw Error("Actual account identity absent");
    safe.actual_password_budget_account_transaction_synced = true;
    await report("app_created", accountID);
    for (const [expectedPhase, action, newWrite] of [["app_recheck", "app_checked", true], ["app_start_recheck", "app_start_checked", false]]) {
      await state(expectedPhase);
      // A completely new context has no IndexedDB or cached budget to read.
      await appContext.close();
      appContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, locale: "en-US" });
      app = await appContext.newPage();
      app.setDefaultTimeout(30000);
      await actualOpenServerBudget(app);
      if (!new URL(app.url()).pathname.endsWith("/accounts/" + accountID)) throw Error("Actual account identity changed");
      if (newWrite) {
        await expect(app.getByTestId("account-balance")).toContainText("87.66");
        await actualTransaction(app, "NeroCD after redeploy", "1.23");
        await actualSync(app);
        safe.actual_fresh_context_server_download_new_write = true;
      } else {
        await expect(app.getByTestId("transaction-table")).toContainText("NeroCD after redeploy");
        safe.actual_failed_replacement_server_persistence = true;
      }
      await expect(app.getByTestId("account-balance")).toContainText("86.43");
      await report(action, accountID);
    }
  } else if (kuma) {
    await state("app_create");
    appContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, locale: "en-US" });
    let app = await appContext.newPage();
    app.setDefaultTimeout(30000);
    await app.goto("/");
    await expect(app.getByText("Which database would you like to use?", { exact: true })).toBeVisible();
    // Bootstrap hides its radio behind the visible associated label.
    await app.locator('label[for="btnradio1"]').click();
    await app.getByRole("button", { name: "Next", exact: true }).click();
    await app.getByLabel("Username", { exact: true }).fill("nerocdacceptance");
    await app.getByLabel("Password", { exact: true }).fill(applicationPassword);
    await app.getByLabel("Repeat Password", { exact: true }).fill(applicationPassword);
    await app.getByRole("button", { name: "Create", exact: true }).click();
    await expect(app.getByRole("link", { name: "Add New Monitor", exact: true })).toBeVisible();
    safe.kuma_sqlite_admin_setup = true;
    const monitorID = await kumaMonitor(app, "NeroCD retained monitor");
    await app.goto(appOrigin + "/settings/general");
    await app.locator("#searchEngineIndexYes").check();
    await app.getByRole("button", { name: "Save", exact: true }).click();
    await expect(app.getByText("Saved.", { exact: true })).toBeVisible();
    safe.kuma_monitor_and_setting_saved = true;
    await report("app_created", monitorID);
    for (const [expectedPhase, action, newWrite] of [["app_recheck", "app_checked", true], ["app_start_recheck", "app_start_checked", false]]) {
      await state(expectedPhase);
      await appContext.close();
      appContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, locale: "en-US" });
      app = await appContext.newPage();
      app.setDefaultTimeout(30000);
      await app.goto("/");
      await app.getByLabel("Username", { exact: true }).fill("nerocdacceptance");
      await app.getByLabel("Password", { exact: true }).fill(applicationPassword);
      await app.getByRole("button", { name: "Log in", exact: true }).click();
      // Wait for authenticated navigation before starting another document.
      await expect(app.getByRole("link", { name: "Add New Monitor", exact: true })).toBeVisible();
      await app.goto(appOrigin + "/dashboard/" + monitorID);
      await expect(app.getByRole("heading", { level: 1 })).toContainText("NeroCD retained monitor");
      await expect(app.getByRole("cell", { name: "Up", exact: true }).first()).toBeVisible({ timeout: 30000 });
      await app.goto(appOrigin + "/settings/general");
      await expect(app.locator("#searchEngineIndexYes")).toBeChecked();
      if (newWrite) {
        await kumaMonitor(app, "NeroCD after redeploy");
        safe.kuma_fresh_login_redeploy_persistence_new_write = true;
      } else {
        await app.goto(appOrigin + "/dashboard");
        await expect(app.getByRole("link", { name: "NeroCD after redeploy", exact: true })).toBeVisible();
        safe.kuma_failed_replacement_persistence = true;
      }
      await report(action, monitorID);
    }
  } else {
  await state("backup");
  const archives = application.getByRole("region", {
    name: "Upload backups and restores", exact: true,
  });
  await archives.getByRole("button", { name: "Refresh archive status", exact: true }).click();
  await archives.getByRole("checkbox", {
    name: "I understand writers may stop and this local backup cannot survive host disk loss.", exact: true,
  }).check();
  const backupWait = response(page, `/api/v1/resources/${installed.resource.id}/storage-backups`);
  await archives.getByRole("button", { name: "Request upload backup", exact: true }).click();
  const backupResponse = await backupWait;
  if (backupResponse.status() !== 202) throw Error("browser backup rejected");
  const backup = await backupResponse.json();
  await report("backed_up", backup.id);
  safe.browser_catalog_backup = true;
  await state("restore");
  await archives.getByRole("button", { name: "Refresh archive status", exact: true }).click();
  const restoreForm = archives.getByRole("form", { name: "Restore backup into a new resource", exact: true });
  await expect(restoreForm).toBeVisible();
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(restoreForm.getByRole("button", { name: "Restore into new resource", exact: true })).toBeVisible();
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth))
      throw Error("archive responsive overflow");
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await restoreForm.getByLabel("New restored resource name", { exact: true }).fill("Restored Linkding acceptance");
  const restoreWait = response(page, `/api/v1/storage-backups/${backup.id}/restores`);
  await restoreForm.getByRole("button", { name: "Restore into new resource", exact: true }).click();
  const restoreResponse = await restoreWait;
  if (restoreResponse.status() !== 202) throw Error("browser restore rejected");
  const restored = await restoreResponse.json();
  await report("restored", restored.operation.id);
  safe.browser_catalog_restore = true;
  const target = await state("restore_deploy");
  if (target.id !== restored.resource.id) throw Error("restored target identity mismatch");
  restoredDeploymentStep = "navigate_hosting";
  h = await hosting(page);
  const restoredApplication = h.getByRole("article", { name: "Restored Linkding acceptance", exact: true });
  const restoredReview = restoredApplication.getByRole("region", { name: "Persistent upload deployment review", exact: true });
  restoredDeploymentStep = "configure_first_deployment";
  await restoredReview.getByLabel("Runner-local host port", { exact: true }).fill("20104");
  await restoredReview.getByLabel("Container port", { exact: true }).fill("9090");
  await restoredReview.getByLabel("Readiness path", { exact: true }).fill("/health");
  await restoredReview.getByLabel("Readiness timeout in seconds", { exact: true }).fill("120");
  restoredDeploymentStep = "review_current_storage";
  await restoredReview.getByRole("button", { name: "Review current storage deployment", exact: true }).click();
  restoredDeploymentStep = "confirm_review";
  await restoredReview.getByRole("checkbox").check();
  const restoredDeployWait = response(page, `/api/v1/resources/${target.id}/deployments`);
  restoredDeploymentStep = "submit_deployment";
  await restoredReview.getByRole("button", { name: "Deploy reviewed image with uploads", exact: true }).click();
  restoredDeploymentStep = "receive_deployment";
  const restoredDeployResponse = await restoredDeployWait;
  if (restoredDeployResponse.status() !== 202) throw Error("browser restored deployment rejected");
  await report("restored_deployed", (await restoredDeployResponse.json()).id);
  safe.browser_restored_deployment = true;
  }
  await state("complete");
  await report("done", operation.id);
  }
  safe.status = "passed";
} catch (error) {
  safe.failure = stopped ? "cancelled" : "browser_workflow_failed";
  safe.failed_phase = phase;
  if ((phase === "restore_deploy" || privateRegistry) && restoredDeploymentStep)
    safe.failed_step = restoredDeploymentStep;
  safe.error_class = ["TimeoutError", "AssertionError", "TypeError"].includes(
    error?.name,
  )
    ? error.name
    : "Error";
  if (!process.exitCode) process.exitCode = 1;
} finally {
  clearTimeout(timer);
  if (!(await closeOwned())) {
    safe.status = "failed";
    safe.cleanup_failures = [...failures].sort();
    process.exitCode ||= 1;
  }
  if (stopped) safe.status = "failed";
  try {
    await writeFile(
      resolve(evidence, "catalog-browser-safe.json"),
      JSON.stringify(safe, null, 2) + "\n",
      { mode: 0o600 },
    );
  } catch {
    process.exitCode ||= 1;
  }
  process.removeListener("SIGINT", interrupt);
  process.removeListener("SIGTERM", terminate);
}
