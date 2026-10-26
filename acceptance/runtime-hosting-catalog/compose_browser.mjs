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
if (
  !/^https:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin ?? "") ||
  !/^[a-f0-9]{64}$/.test(key ?? "") ||
  !evidence?.startsWith("/")
)
  throw Error("exact private browser profile required");
let api,
  browser,
  context,
  appContext,
  freshAppContext,
  page,
  stopped = false;
const closing = new Map(),
  failures = new Set();
async function closeOwned() {
  for (const [name, value, method] of [
    ["fresh_app", freshAppContext, "close"],
    ["app", appContext, "close"],
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
let step = "startup";
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
const appOrigin = "https://catalog-runtime.example.test:25443";
const applicationName = "Compose Vikunja acceptance";
const payload = Buffer.from("NeroCD saved Compose persistent attachment\n");
const filename = "compose-persistence.txt";
const username = "composeacceptance";
const password = randomBytes(24).toString("hex");
const taskTitle = "Saved Compose persistence task";
const appResponse = (p, method, pattern) => p.waitForResponse((r) =>
  [method].flat().includes(r.request().method()) && new URL(r.url()).origin === appOrigin && pattern.test(new URL(r.url()).pathname));
async function checkedResponse(pending, status) {
  const result = await pending;
  if (result.status() !== status) throw Error("unexpected application response");
  return result.json();
}
async function upload(p, name, content) {
  step = "upload_attachment";
  const pending = appResponse(p, ["PUT", "POST"], /^\/api\/v[12]\/tasks\/\d+\/attachments$/);
  // This is the application's own file input and change handler.
  await p.locator('input[type="file"]#files').setInputFiles({ name, mimeType: "text/plain", buffer: content });
  const result = await pending;
  if (![200, 201].includes(result.status())) throw Error("attachment upload rejected");
  await expect(p.getByText(name, { exact: true })).toBeVisible();
}
async function download(p, name, expected) {
  step = "download_attachment";
  const attachment = p.locator(".attachment").filter({ hasText: name });
  const pending = p.waitForEvent("download");
  await attachment.getByLabel("Download this attachment", { exact: true }).click();
  const file = await pending;
  if (file.suggestedFilename() !== name) throw Error("attachment filename changed");
  const stream = await file.createReadStream();
  const parts = [];
  for await (const part of stream) parts.push(part);
  if (!Buffer.concat(parts).equals(expected)) throw Error("attachment content changed");
}
async function addTask(p, title) {
  step = "create_task";
  const pending = appResponse(p, ["PUT", "POST"], /^\/api\/v[12]\/projects\/\d+\/tasks(?:\/bulk)?$/);
  await p.getByPlaceholder("Add a task…", { exact: true }).fill(title);
  await p.getByRole("button", { name: "Add", exact: true }).click();
  const result = await pending;
  if (![200, 201].includes(result.status())) throw Error("task creation rejected");
  const body = await result.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks ? tasks.find((item) => item?.title === title) : body;
  if (!Number.isSafeInteger(task?.id) || task.id < 1 || task.title !== title) throw Error("task identity missing");
  return task.id;
}
try {
  api = await request.newContext({ baseURL: origin, ignoreHTTPSErrors: true, extraHTTPHeaders: { "X-NeroCD-Fixture-Key": key }, timeout: 10000 });
  browser = await chromium.launch({ headless: true, args: ["--host-resolver-rules=MAP catalog-runtime.example.test 127.0.0.1", "--no-proxy-server"] });
  context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, viewport: { width: 1280, height: 900 } });
  page = await context.newPage();
  page.setDefaultTimeout(30000);
  const initial = await state("install");
  step = "verify_served_assets";
  const manifest = JSON.parse(await readFile(resolve(evidence, "browser-ui-manifest.json"), "utf8"));
  if (manifest.schema !== "nerocd.ui-assets/v1" || !manifest.files["index.html"]) throw Error("candidate manifest absent");
  for (const [path, sha] of Object.entries(manifest.files)) {
    if (path === ".gitkeep") continue;
    const result = await api.get("/" + path);
    if (result.status() !== 200 || createHash("sha256").update(await result.body()).digest("hex") !== sha) throw Error("candidate asset mismatch");
  }
  safe.served_assets_match_manifest = true;
  await signIn(page, "admin@example.local", "admin");
  const h = await hosting(page);
  step = "save_original_compose";
  const source = await readFile(resolve("internal/domain/testdata/vikunja-with-postgresql.yaml"), "utf8");
  if (createHash("sha256").update(source).digest("hex") !== "4ae479af1c9ee76836315b952db8d177fb1b1426316ead2f0f5c09bfa4acdb54") throw Error("vendored upstream source changed");
  const create = h.getByRole("form", { name: "Create application", exact: true });
  await create.getByLabel("Application source", { exact: true }).selectOption("compose");
  await create.getByLabel("Application name", { exact: true }).fill(applicationName);
  await create.getByLabel("Compose stack", { exact: true }).fill(source);
  await create.getByLabel("Public component", { exact: true }).fill("vikunja");
  await create.getByLabel("Public component container port", { exact: true }).fill("3456");
  const createdWait = response(page, "/api/v1/resources");
  await create.getByRole("button", { name: "Save application", exact: true }).click();
  const resource = await checkedResponse(createdWait, 201);
  if (!resource.id || resource.source.kind !== "compose" || resource.source.compose.original !== source) throw Error("saved source mismatch");
  const resourceID = resource.id;
  const application = h.getByRole("article", { name: applicationName, exact: true });
  const destination = application.getByRole("region", { name: `Desired destination for ${applicationName}`, exact: true });
  await destination.getByLabel("Available destination", { exact: true }).selectOption(initial.id);
  const boundWait = response(page, `/api/v1/resources/${resourceID}/binding`);
  await destination.getByRole("button", { name: "Save desired destination", exact: true }).click();
  await checkedResponse(boundWait, 201);
  safe.saved_compose_configuration = true;
  step = "save_runtime_configuration";
  const runtime = application.getByRole("region", { name: `Runtime configuration for ${applicationName}`, exact: true });
  const entries = [
    ["SERVICE_URL_VIKUNJA", "literal", appOrigin],
    ["SERVICE_USER_POSTGRESQL", "literal", "vikunja"],
    ["POSTGRESQL_DATABASE", "literal", "vikunja"],
    ["SERVICE_PASSWORD_POSTGRESQL", "managed_secret", "Compose PostgreSQL"],
    ["SERVICE_PASSWORD_JWTSECRET", "managed_secret", "Compose JWT"],
  ];
  for (const [index, [name, kind, value]] of entries.entries()) {
    const n = index + 1;
    await runtime.getByRole("button", { name: "Add runtime variable", exact: true }).click();
    await runtime.getByLabel(`Variable name ${n}`, { exact: true }).fill(name);
    await runtime.getByLabel(`Value type ${n}`, { exact: true }).selectOption(kind);
    if (kind === "literal") await runtime.getByLabel(`Nonsecret value ${n}`, { exact: true }).fill(value);
    else {
      const select = runtime.getByLabel(`Existing managed secret for ${name}`, { exact: true });
      const option = select.getByRole("option").filter({ hasText: `${value} · ` });
      await expect(option).toHaveCount(1);
      await select.selectOption(await option.getAttribute("value"));
      await runtime.getByLabel(`Exact secret version for ${name}`, { exact: true }).selectOption("1");
    }
  }
  const runtimeWait = page.waitForResponse((r) => r.request().method() === "PUT" && new URL(r.url()).pathname === `/api/v1/resources/${resourceID}/runtime-configuration`);
  await runtime.getByRole("button", { name: "Save runtime configuration", exact: true }).click();
  const savedRuntime = await checkedResponse(runtimeWait, 200);
  if (savedRuntime.runtime_configuration.entries.length !== entries.length) throw Error("runtime configuration incomplete");
  safe.browser_runtime_configuration = true;
  await report("installed", resourceID);
  const review = application.getByRole("region", { name: "Saved Compose deployment review", exact: true });
  async function deploy(replacement) {
    step = replacement ? "redeploy_saved_compose" : "deploy_saved_compose";
    if (!replacement) {
      await review.getByLabel("Readiness path", { exact: true }).fill("/health");
      await review.getByLabel("Readiness timeout in seconds", { exact: true }).fill("120");
    }
    await review.getByRole("button", { name: "Review current Compose deployment", exact: true }).click();
    await review.getByRole("checkbox").check();
    const pending = response(page, `/api/v1/resources/${resourceID}/deployments`);
    await review.getByRole("button", { name: "Deploy reviewed Compose", exact: true }).click();
    const result = await pending;
    if (result.status() !== 202) throw Error("Compose deployment rejected");
    const input = result.request().postDataJSON();
    if (input.managed_stack?.acknowledge_interruption !== true || !input.expected_runtime_configuration_generation || Boolean(input.storage_route_continuity) !== replacement) throw Error("Compose admission authority missing");
    return (await result.json()).id;
  }
  await state("deploy");
  const firstOperation = await deploy(false);
  safe.browser_deployment = true;
  await report("deployed", firstOperation);
  await state("healthy");
  step = "inspect_component_result";
  const deployments = application.getByRole("region", { name: `Image deployments for ${applicationName}`, exact: true });
  await deployments.getByRole("button", { name: "Refresh deployment history", exact: true }).click();
  await deployments.getByRole("button", { name: `View operation ${firstOperation}`, exact: true }).click();
  const components = deployments.getByRole("region", { name: "Recorded Compose components", exact: true });
  await expect(components).toContainText("Observation complete", { timeout: 30000 });
  for (const name of ["vikunja", "postgresql"]) await expect(components.getByRole("article", { name: `Component ${name}`, exact: true })).toContainText("Running");
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw Error("responsive overflow");
  }
  safe.responsive_widths = [1280, 320, 390, 430];
  await page.setViewportSize({ width: 1280, height: 900 });
  safe.healthy_components = true;
  await report("healthy", resourceID);
  await state("route");
  step = "select_https_route";
  const routes = application.getByRole("region", { name: `Routes for ${applicationName}`, exact: true });
  await routes.getByRole("button", { name: "Refresh resource routes", exact: true }).click();
  const routeForm = routes.getByRole("form", { name: `Configure route for ${applicationName}`, exact: true });
  await routeForm.getByLabel("Route hostname", { exact: true }).fill("catalog-runtime.example.test");
  await routeForm.getByLabel("Expected settled healthy deployment", { exact: true }).selectOption(firstOperation);
  const routeWait = response(page, `/api/v1/resources/${resourceID}/routes`);
  await routeForm.getByRole("button", { name: "Save desired route", exact: true }).click();
  const route = await checkedResponse(routeWait, 202);
  safe.browser_route_selection = true;
  await report("routed", route.operation_id);
  await state("app_create");
  step = "signup_vikunja";
  appContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, acceptDownloads: true, locale: "en-US" });
  const app = await appContext.newPage();
  app.setDefaultTimeout(30000);
  await app.goto("/register");
  await app.getByLabel("Username", { exact: true }).fill(username);
  await app.getByLabel("Email address", { exact: true }).fill("composeacceptance@example.test");
  await app.getByLabel("Password", { exact: true }).fill(password);
  const registerWait = appResponse(app, "POST", /^\/api\/v[12]\/register$/);
  await app.getByRole("button", { name: "Create account", exact: true }).click();
  const registered = await registerWait;
  if (![200, 201].includes(registered.status())) throw Error("application registration rejected");
  await expect(app.locator("#registerform")).toHaveCount(0);
  step = "create_project";
  await app.goto("/projects/new");
  await app.getByLabel("Title", { exact: true }).fill("Compose persistence");
  const projectWait = appResponse(app, ["PUT", "POST"], /^\/api\/v[12]\/projects$/);
  await app.getByRole("button", { name: "Create", exact: true }).click();
  const projectResult = await projectWait;
  if (![200, 201].includes(projectResult.status())) throw Error("project creation rejected");
  const projectID = (await projectResult.json()).id;
  const taskID = await addTask(app, taskTitle);
  await app.goto(`/tasks/${taskID}`);
  await expect(app.getByRole("heading", { name: taskTitle, exact: true })).toBeVisible();
  await upload(app, filename, payload);
  await download(app, filename, payload);
  safe.app_signup_task_upload = true;
  await report("app_created", String(taskID));
  await appContext.close();
  await state("redeploy");
  // Reload the Go-served page to fetch current continuity authority.
  await hosting(page);
  const secondOperation = await deploy(true);
  if (secondOperation === firstOperation) throw Error("redeploy reused prior operation");
  safe.browser_redeployment = true;
  await report("redeployed", secondOperation);
  await state("app_recheck");
  step = "fresh_login_vikunja";
  freshAppContext = await browser.newContext({ baseURL: appOrigin, ignoreHTTPSErrors: true, acceptDownloads: true, locale: "en-US" });
  const fresh = await freshAppContext.newPage();
  fresh.setDefaultTimeout(30000);
  await fresh.goto("/login");
  await fresh.getByLabel("Username Or Email Address", { exact: true }).fill(username);
  await fresh.getByLabel("Password", { exact: true }).fill(password);
  const loginWait = appResponse(fresh, "POST", /^\/api\/v[12]\/login$/);
  await fresh.getByRole("button", { name: "Login", exact: true }).click();
  const loggedIn = await loginWait;
  if (loggedIn.status() !== 200) throw Error("fresh login rejected");
  await expect(fresh.locator("#loginform")).toHaveCount(0);
  await fresh.goto(`/tasks/${taskID}`);
  await expect(fresh.getByRole("heading", { name: taskTitle, exact: true })).toBeVisible();
  await download(fresh, filename, payload);
  const newPayload = Buffer.from("New attachment after saved Compose redeployment\n");
  await upload(fresh, "compose-after-redeploy.txt", newPayload);
  await download(fresh, "compose-after-redeploy.txt", newPayload);
  await fresh.goto(`/projects/${projectID}`);
  const newTask = await addTask(fresh, "New task after Compose redeployment");
  if (newTask === taskID) throw Error("new task identity reused");
  safe.app_fresh_login_persistence_new_writes = true;
  await report("app_checked", String(taskID));
  await state("complete");
  await report("done", resourceID);
  safe.status = "passed";
} catch (error) {
  safe.failure = stopped ? "cancelled" : "browser_workflow_failed";
  safe.failed_phase = phase;
  safe.failed_step = step;
  safe.error_class = ["TimeoutError", "AssertionError", "TypeError"].includes(error?.name) ? error.name : "Error";
  process.exitCode ||= 1;
} finally {
  clearTimeout(timer);
  if (!(await closeOwned())) {
    safe.status = "failed";
    safe.cleanup_failures = [...failures].sort();
    process.exitCode ||= 1;
  }
  if (stopped) safe.status = "failed";
  try { await writeFile(resolve(evidence, "compose-browser-safe.json"), JSON.stringify(safe, null, 2) + "\n", { mode: 0o600 }); }
  catch { process.exitCode ||= 1; }
  process.removeListener("SIGINT", interrupt);
  process.removeListener("SIGTERM", terminate);
}
