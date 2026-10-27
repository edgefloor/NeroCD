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
async function hosting(p, beforeEnvironmentSelection) {
  await p.goto("/projects");
  const h = p.getByRole("region", { name: "Project hosting", exact: true });
  await h
    .getByLabel("Hosting project", { exact: true })
    .selectOption("proj_platform");
  // Arm response waits after navigation retires requests from the old document.
  beforeEnvironmentSelection?.();
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
const restoreOrigin = "https://restore.catalog-runtime.example.test:25443";
const applicationName = "Compose Vikunja acceptance";
const payload = Buffer.from("NeroCD saved Compose persistent attachment\n");
const filename = "compose-persistence.txt";
const username = "composeacceptance";
const password = randomBytes(24).toString("hex");
const taskTitle = "Saved Compose persistence task";
const appResponse = (p, method, pattern) => p.waitForResponse((r) =>
  [method].flat().includes(r.request().method()) && [appOrigin, restoreOrigin].includes(new URL(r.url()).origin) && pattern.test(new URL(r.url()).pathname));
async function checkedResponse(pending, status) {
  delete safe.checked_response_status;
  delete safe.checked_response_failure;
  const result = await pending;
  safe.checked_response_status = result.status();
  if (result.status() !== status) {
    safe.checked_response_failure = "status_mismatch";
    throw Error("unexpected application response");
  }
  let body;
  try { body = await result.body(); }
  catch (error) {
    safe.checked_response_failure = "body_read_failed";
    throw error;
  }
  try { return JSON.parse(body.toString("utf8")); }
  catch (error) {
    safe.checked_response_failure = "invalid_json";
    throw error;
  }
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
  browser = await chromium.launch({ headless: true, args: ["--host-resolver-rules=MAP catalog-runtime.example.test 127.0.0.1, MAP restore.catalog-runtime.example.test 127.0.0.1", "--no-proxy-server"] });
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
  let source = await readFile(resolve("examples/compose/vikunja-postgresql.yaml"), "utf8");
  if (createHash("sha256").update(source).digest("hex") !== "2ed2a1cc22b4c020f26e214d4e96a419eb56445a9d14f87f2df703a7ee9de767") throw Error("shipped Compose example changed");
  // Explicit test command emits finite init output; shipped example stays intact.
  source = source.replace("      - |\n", "      - |\n        printf 'nerocd-init-log-fixture\\n'\n");
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
    await runtime.getByRole("combobox", { name: `Value type ${n}`, exact: true }).selectOption(kind);
    if (kind === "literal") await runtime.getByLabel(`Nonsecret value ${n}`, { exact: true }).fill(value);
    else {
      const select = runtime.getByRole("combobox", { name: `Existing managed secret for ${name}`, exact: true });
      const option = select.getByRole("option").filter({ hasText: `${value} · ` });
      await expect(option).toHaveCount(1);
      await select.selectOption(await option.getAttribute("value"));
      await runtime.getByRole("combobox", { name: `Exact secret version for ${name}`, exact: true }).selectOption("1");
    }
  }
  const runtimeWait = page.waitForResponse((r) => r.request().method() === "PUT" && new URL(r.url()).pathname === `/api/v1/resources/${resourceID}/runtime-configuration`);
  await runtime.getByRole("button", { name: "Save runtime configuration", exact: true }).click();
  const savedRuntime = await checkedResponse(runtimeWait, 200);
  if (savedRuntime.runtime_configuration.entries.length !== entries.length) throw Error("runtime configuration incomplete");
  safe.browser_runtime_configuration = true;
  await report("installed", resourceID);
  const review = application.getByRole("region", { name: "Saved Compose deployment review", exact: true });
  async function deploy(replacement, stoppedOperation) {
    step = replacement ? "redeploy_saved_compose" : "deploy_saved_compose";
    if (!replacement) {
      step = "configure_compose_readiness";
      await review.getByLabel("Readiness path", { exact: true }).fill("/health");
      await review.getByLabel("Readiness timeout in seconds", { exact: true }).fill("120");
    }
    step = "review_compose_options";
    await review.getByRole("button", { name: stoppedOperation ? "Review current Compose start" : "Review current Compose deployment", exact: true }).click();
    step = "acknowledge_compose_interruption";
    await review.getByRole("checkbox").check();
    const pending = response(page, `/api/v1/resources/${resourceID}/deployments`);
    step = "submit_compose_deployment";
    await review.getByRole("button", { name: stoppedOperation ? "Start reviewed Compose" : "Deploy reviewed Compose", exact: true }).click();
    const result = await pending;
    if (result.status() !== 202) throw Error("Compose deployment rejected");
    const input = result.request().postDataJSON();
    if (input.managed_stack?.expected_stopped_operation_id !== stoppedOperation) throw Error("Start stopped authority missing");
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
  await expect(components.getByRole("article", { name: "Component init", exact: true })).toContainText("Initialization completed successfully");
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw Error("responsive overflow");
  }
  safe.responsive_widths = [1280, 320, 390, 430];
  await page.setViewportSize({ width: 1280, height: 900 });
  step = "follow_component_logs";
  const logRegion = application.getByRole("region", { name: `Runtime logs for ${applicationName}`, exact: true });
  await logRegion.scrollIntoViewIfNeeded();
  const refreshedTargets = page.waitForResponse((r) => r.request().method() === "GET" && new URL(r.url()).pathname === `/api/v1/resources/${resourceID}/runtime-log-targets`);
  await logRegion.getByRole("button", { name: "Refresh component targets", exact: true }).click();
  await checkedResponse(refreshedTargets, 200);
  const selector = logRegion.getByRole("combobox", { name: "Compose component", exact: true });
  for (const component of ["vikunja", "postgresql", "init"]) {
    await selector.selectOption(component);
    const opened = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${resourceID}/runtime-log-sessions`);
    await logRegion.getByRole("button", { name: "Follow runtime logs", exact: true }).click();
    const session = await checkedResponse(opened, 200);
    if (session.target.component !== component || session.target.operation_id !== firstOperation || !session.target.container_name) throw Error("component target identity missing");
    await expect(logRegion).toContainText(session.id);
    const closed = page.waitForResponse((r) => r.request().method() === "DELETE" && new URL(r.url()).pathname === `/api/v1/runtime-log-sessions/${session.id}`);
    await logRegion.getByRole("button", { name: "Stop following", exact: true }).click();
    await checkedResponse(closed, 200);
    await selector.selectOption("");
  }
  safe.browser_component_log_selection_follow_close = true;
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
  await expect(app.getByRole("heading", { name: "Title", exact: true })).toHaveText(taskTitle);
  await upload(app, filename, payload);
  await download(app, filename, payload);
  safe.app_signup_task_upload = true;
  await report("app_created", String(taskID));
  await appContext.close();
  step = "await_compose_volume_schedule_phase";
  await state("volume_schedule");
  let initialVolumeDiscovery;
  step = "open_compose_volume_schedule_page";
  await hosting(page, () => {
    initialVolumeDiscovery = page.waitForResponse(async r => {
      if (r.request().method() !== "GET" || new URL(r.url()).pathname !== `/api/v1/resources/${resourceID}/storage-backup-volumes`) return false;
      try { return await r.finished() === null; } catch { return false; }
    });
    void initialVolumeDiscovery.catch(() => {});
  });
  step = "read_initial_compose_volume_eligibility";
  const initialVolumeEvidence = await checkedResponse(initialVolumeDiscovery, 200);
  safe.volume_schedule_eligibility_available = initialVolumeEvidence.available === true;
  safe.volume_schedule_eligibility_reason = ["unavailable", "unsupported_source", "archive_unavailable", "deployment_changed", "runner_unavailable", "requires_runtime_ownership_verification", "no_eligible_volumes"].includes(initialVolumeEvidence.reason) ? initialVolumeEvidence.reason : "other";
  if (initialVolumeEvidence.available !== true || initialVolumeEvidence.operation_id !== firstOperation || !initialVolumeEvidence.volumes?.some(v => v.volume === "vikunja-data" && v.eligible === true)) throw Error("initial Compose volume schedule eligibility unavailable");
  const volumeBackups = application.getByRole("region", {name:"Compose volume backups",exact:true});
  const volumeSchedules = volumeBackups.getByRole("region", {name:"Recurring Compose volume backups",exact:true});
  step = "select_initial_scheduled_compose_volume";
  await volumeBackups.getByRole("combobox", {name:"Named volume to back up",exact:true}).selectOption("vikunja-data");
  step = "set_initial_compose_volume_schedule_frequency";
  await volumeSchedules.getByLabel("Backup frequency", {exact:true}).fill("every_minute");
  step = "disable_initial_compose_volume_schedule_draft";
  await volumeSchedules.getByRole("checkbox", {name:"Enabled",exact:true}).uncheck();
  step = "verify_initial_compose_volume_selection";
  await expect(volumeBackups.getByRole("combobox", {name:"Named volume to back up",exact:true})).toHaveValue("vikunja-data");
  step = "verify_initial_compose_volume_schedule_frequency";
  await expect(volumeSchedules.getByLabel("Backup frequency", {exact:true})).toHaveValue("every_minute");
  step = "verify_initial_compose_volume_schedule_disabled_draft";
  await expect(volumeSchedules.getByRole("checkbox", {name:"Enabled",exact:true})).not.toBeChecked();
  step = "review_initial_compose_volume_schedule";
  await volumeSchedules.getByRole("button", {name:"Review Compose volume schedule",exact:true}).click();
  const scheduleReview = volumeSchedules.getByRole("region", {name:"Reviewed Compose volume schedule",exact:true});
  step = "verify_initial_compose_volume_schedule_deployment";
  await expect(scheduleReview).toContainText(firstOperation);
  step = "acknowledge_initial_compose_volume_schedule";
  await scheduleReview.getByRole("checkbox").check();
  const volumeSchedulePending = response(page, `/api/v1/resources/${resourceID}/storage-backup-schedules`);
  step = "confirm_initial_compose_volume_schedule";
  await scheduleReview.getByRole("button", {name:"Confirm Compose volume schedule",exact:true}).click();
  step = "await_initial_compose_volume_schedule_response";
  const volumeScheduleResponse = await volumeSchedulePending;
  step = "validate_initial_compose_volume_schedule_response";
  const volumeSchedule = await checkedResponse(Promise.resolve(volumeScheduleResponse),200);
  const volumeScheduleBody = volumeScheduleResponse.request().postDataJSON();
  if(volumeSchedule.version!=="managed-compose.local-volume-archive.schedule.v1" || volumeSchedule.volume!=="vikunja-data" || volumeSchedule.enabled || volumeSchedule.acknowledge_interruption!==true || volumeScheduleBody.expected_deployed_operation_id!==firstOperation || volumeScheduleBody.acknowledge_interruption!==true || volumeScheduleBody.remote_target)throw Error("volume schedule identity changed");
  safe.browser_compose_volume_schedule_saved_disabled=true;
  step = "report_disabled_compose_volume_schedule";
  await report("volume_scheduled", volumeSchedule.id);
  await state("redeploy");
  // Reload the Go-served page to fetch current continuity authority.
  await hosting(page);
  const secondOperation = await deploy(true);
  if (secondOperation === firstOperation) throw Error("redeploy reused prior operation");
  safe.browser_redeployment = true;
  await report("redeployed", secondOperation);
  await state("app_recheck");
  async function checkApplication(suffix) {
    if (freshAppContext) await freshAppContext.close();
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
  await expect(fresh.getByRole("heading", { name: "Title", exact: true })).toHaveText(taskTitle);
  await download(fresh, filename, payload);
  const newPayload = Buffer.from("New attachment after saved Compose redeployment\n");
  await upload(fresh, `${suffix}.txt`, newPayload);
  await download(fresh, `${suffix}.txt`, newPayload);
  await fresh.goto(`/projects/${projectID}`);
  const newTask = await addTask(fresh, `New task after ${suffix}`);
  if (newTask === taskID) throw Error("new task identity reused");
  safe.app_fresh_login_persistence_new_writes = true;

  }
  await checkApplication("compose-after-redeploy");
  await report("app_checked", String(taskID));
  await state("stop");
  await hosting(page);
  step = "review_compose_stop";
  const lifecycle = application.getByRole("region", { name: "Compose lifecycle", exact: true });
  await lifecycle.getByRole("button", { name: "Refresh Compose lifecycle", exact: true }).click();
  await lifecycle.getByRole("button", { name: "Review Stop Compose", exact: true }).click();
  const stopping = response(page, `/api/v1/resources/${resourceID}/stack-stop`);
  step = "submit_compose_stop";
  await lifecycle.getByRole("button", { name: "Confirm Stop Compose", exact: true }).click();
  const stopResponse = await stopping;
  safe.stop_response_status = stopResponse.status();
  const stopped = await checkedResponse(stopResponse, 202);
  step = "validate_compose_stop_target";
  if (stopped.stack_stop_plan?.target?.operation_id !== secondOperation) throw Error("Stop target changed");
  await report("stopped", stopped.id);
  await state("start");
  await hosting(page);
  const startedOperation = await deploy(true, stopped.id);
  if ([firstOperation, secondOperation].includes(startedOperation)) throw Error("Start reused deployment identity");
  await report("started", startedOperation);
  await state("app_start_recheck");
  await checkApplication("compose-after-start");
  safe.browser_stack_stop_start_persistence = true;
  await report("app_start_checked", String(taskID));
  step = "await_compose_volume_schedule_run_phase";
  await state("volume_schedule_run");
  let savedScheduleList;
  try {
    step = "open_compose_volume_schedule_run_page";
    await hosting(page, () => {
      savedScheduleList = page.waitForResponse(async r => {
        if (r.request().method() !== "GET" || new URL(r.url()).pathname !== `/api/v1/resources/${resourceID}/storage-backup-schedules`) return false;
        try { return await r.finished() === null; } catch { return false; }
      });
      void savedScheduleList.catch(() => {});
    });
    step = "read_saved_compose_volume_schedule_list";
    const listed = await savedScheduleList;
    safe.volume_schedule_run_list_status = listed.status();
    const items = await listed.json();
    safe.volume_schedule_run_list_array = Array.isArray(items);
    safe.volume_schedule_run_list_count = Array.isArray(items) ? items.length : 0;
    safe.volume_schedule_run_list_id_matches = Array.isArray(items) && items.some(v => v?.id === volumeSchedule.id);
    step = "select_saved_compose_volume_schedule";
    await volumeSchedules.getByRole("button", {name:`View schedule ${volumeSchedule.id}`,exact:true}).click();
  } catch (error) {
    // Diagnostic reads are immediate and best effort; retain the workflow error.
    try {
      safe.volume_schedule_run_region_count = await volumeSchedules.count();
      const savedButton = volumeSchedules.getByRole("button", {name:`View schedule ${volumeSchedule.id}`,exact:true});
      safe.volume_schedule_run_button_count = await savedButton.count();
      const disabled = await savedButton.evaluateAll(buttons => buttons.length === 1 ? buttons[0].matches(":disabled") : undefined);
      if (disabled !== undefined) safe.volume_schedule_run_button_disabled = disabled;
      const text = await application.evaluateAll(nodes => nodes.map(node => node.textContent ?? "").join("\n"));
      safe.volume_schedule_run_error_category = [
        ["Schedule metadata is unavailable.", "schedule_metadata_unavailable"],
        ["Compose backup access is unavailable.", "compose_backup_access_unavailable"],
        ["Deployment permission is unavailable.", "deployment_permission_unavailable"],
        ["Deployment access is pending or unavailable.", "deployment_access_pending"],
        ["Unable to refresh deployment information.", "deployment_information_unavailable"],
      ].find(([message]) => text.includes(message))?.[1] ?? "unclassified";
    } catch {}
    throw error;
  }
  step = "enable_saved_compose_volume_schedule_draft";
  await volumeSchedules.getByRole("checkbox", {name:"Enabled",exact:true}).check();
  step = "verify_saved_compose_volume_schedule_selection";
  await expect(volumeSchedules).toContainText(`Scheduled named volume: ${volumeSchedule.volume}`);
  step = "verify_saved_compose_volume_schedule_enabled_draft";
  await expect(volumeSchedules.getByRole("checkbox", {name:"Enabled",exact:true})).toBeChecked();
  step = "review_enabled_compose_volume_schedule";
  await volumeSchedules.getByRole("button", {name:"Review Compose volume schedule",exact:true}).click();
  step = "verify_enabled_compose_volume_schedule_deployment";
  await expect(scheduleReview).toContainText(startedOperation);
  step = "acknowledge_enabled_compose_volume_schedule";
  await scheduleReview.getByRole("checkbox").check();
  const enablePending = page.waitForResponse(r=>r.request().method()==="PUT" && new URL(r.url()).pathname===`/api/v1/resources/${resourceID}/storage-backup-schedules/${volumeSchedule.id}`);
  step = "confirm_enabled_compose_volume_schedule";
  await scheduleReview.getByRole("button", {name:"Confirm Compose volume schedule",exact:true}).click();
  step = "await_enabled_compose_volume_schedule_response";
  const enabledResponse = await enablePending;
  step = "validate_enabled_compose_volume_schedule_response";
  const enabledVolumeSchedule = await checkedResponse(Promise.resolve(enabledResponse),200);
  const enableBody = enabledResponse.request().postDataJSON();
  if(!enabledVolumeSchedule.enabled || enabledVolumeSchedule.volume!==volumeSchedule.volume || enabledVolumeSchedule.revision!==volumeSchedule.revision+1 || enableBody.expected_deployed_operation_id!==startedOperation || enableBody.acknowledge_interruption!==true)throw Error("volume schedule enable identity changed");
  for (const width of [320,390,430,1280]) {
    step = "resize_compose_volume_schedule_controls";
    await page.setViewportSize({width,height:900});
    step = "verify_compose_volume_schedule_disable_control";
    await expect(volumeSchedules.getByRole("button", {name:"Disable schedule",exact:true})).toBeVisible();
    step = "verify_compose_volume_schedule_responsive_width";
    if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth))throw Error("volume schedule responsive overflow");
  }
  step = "await_scheduled_compose_volume_backup";
  let volumeOccurrence;
  await expect.poll(async()=>{
    step = "read_compose_volume_schedule_occurrences";
    const r=await page.request.get(`/api/v1/resources/${resourceID}/storage-backup-schedules/${volumeSchedule.id}/occurrences`);
    if(r.status()!==200)throw Error("volume schedule occurrence history unavailable");
    step = "validate_compose_volume_schedule_occurrences";
    const values=await r.json();
    volumeOccurrence=values.find(v=>v.operation_id && v.backup_id);
    return Boolean(volumeOccurrence);
  },{timeout:75000,intervals:[500,1000,2000]}).toBe(true);
  if(volumeOccurrence.volume!==volumeSchedule.volume || volumeOccurrence.schedule_revision!==enabledVolumeSchedule.revision || volumeOccurrence.schedule_id!==volumeSchedule.id)throw Error("volume occurrence identity changed");
  const disableVolumePending = page.waitForResponse(r=>r.request().method()==="PUT" && new URL(r.url()).pathname===`/api/v1/resources/${resourceID}/storage-backup-schedules/${volumeSchedule.id}`);
  step = "disable_admitted_compose_volume_schedule";
  await volumeSchedules.getByRole("button", {name:"Disable schedule",exact:true}).click();
  step = "await_disabled_compose_volume_schedule_response";
  const disabledVolumeResponse=await disableVolumePending;
  step = "validate_disabled_compose_volume_schedule_response";
  const disabledVolumeSchedule=await checkedResponse(Promise.resolve(disabledVolumeResponse),200);
  if(disabledVolumeSchedule.enabled || disabledVolumeSchedule.volume!==volumeSchedule.volume || disabledVolumeSchedule.revision!==enabledVolumeSchedule.revision+1 || disabledVolumeResponse.request().postDataJSON().expected_deployed_operation_id)throw Error("volume schedule disable identity changed");
  step = "read_scheduled_compose_volume_backup";
  const volumeResultResponse = await page.request.get(`/api/v1/storage-backups/${volumeOccurrence.backup_id}`);
  step = "validate_scheduled_compose_volume_backup";
  const volumeResult = await checkedResponse(Promise.resolve(volumeResultResponse),200);
  if(volumeResult.id!==volumeOccurrence.backup_id || volumeResult.volume!==volumeSchedule.volume || volumeResult.source_deployed_operation_id!==startedOperation)throw Error("scheduled volume backup source changed");
  safe.browser_compose_volume_schedule_enabled_after_redeploy=true;
  safe.browser_compose_volume_schedule_occurrence=true;
  safe.browser_compose_volume_schedule_disabled=true;
  step = "report_scheduled_compose_volume_backup";
  await report("volume_schedule_backed_up",volumeResult.id);
  const failedVolumeBackup = await state("volume_recheck");
  if (!failedVolumeBackup.id || failedVolumeBackup.id === volumeResult.id) throw Error("failed volume backup identity missing");
  await checkApplication("compose-after-volume-backup");
  await volumeBackups.getByRole("button",{name:"Refresh Compose backup evidence and history",exact:true}).click();
  await volumeBackups.getByRole("button",{name:`View volume backup ${volumeResult.id}`,exact:true}).click();
  await expect(volumeBackups).toContainText("Backup state: succeeded");
  await expect(volumeBackups).toContainText("Local artifact:");
  await expect(volumeBackups).toContainText("Writer resumption: resumed");
  safe.browser_compose_volume_backup_persistence_new_writes=true;
  safe.browser_compose_volume_backup_history=true;
  step = "inspect_failed_volume_backup_compensation";
  await volumeBackups.getByRole("button", {name:`View volume backup ${failedVolumeBackup.id}`,exact:true}).click();
  await expect(volumeBackups).toContainText("Backup state: failed");
  await expect(volumeBackups).toContainText("Archive outcome: failed");
  await expect(volumeBackups).toContainText("Writer resumption: resumed");
  await expect(volumeBackups).toContainText("No local archive artifact is confirmed.");
  await expect(volumeBackups).not.toContainText("Local artifact:");
  safe.browser_compose_volume_backup_failed_archive_compensated=true;
  await volumeBackups.getByRole("button", {name:`View volume backup ${volumeResult.id}`,exact:true}).click();
  await expect(volumeBackups).toContainText("Backup state: succeeded");
  await report("volume_checked",String(taskID));
  step = "await_compose_remote_backup_phase";
  const remoteState = await state("volume_remote_backup");
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(remoteState.id ?? "")) throw Error("remote target identity unavailable");
  step = "open_compose_remote_backup_page";
  await hosting(page);
  const remoteSelector = volumeBackups.getByRole("combobox", {name:"Remote Compose backup target",exact:true});
  const remoteOption = remoteSelector.locator(`option[value^="${remoteState.id}:"]`);
  step = "await_validated_compose_remote_target";
  await expect(remoteOption).toHaveCount(1);
  step = "read_validated_compose_remote_target_revision";
  const remoteValue = await remoteOption.getAttribute("value");
  const remoteRevision = Number(remoteValue?.slice(remoteState.id.length + 1));
  if (!remoteValue || !Number.isSafeInteger(remoteRevision) || remoteRevision < 1) throw Error("remote target revision unavailable");
  step = "select_compose_remote_target";
  await remoteSelector.selectOption(remoteValue);
  step = "select_compose_remote_volume";
  await volumeBackups.getByRole("combobox", {name:"Named volume to back up",exact:true}).selectOption("vikunja-data");
  step = "review_compose_remote_volume_backup";
  await volumeBackups.getByRole("button", {name:"Review Compose volume backup",exact:true}).click();
  const remoteReview = volumeBackups.getByRole("region", {name:"Reviewed Compose volume backup",exact:true});
  step = "verify_compose_remote_volume_deployment";
  await expect(remoteReview).toContainText(startedOperation);
  step = "verify_compose_remote_target_revision";
  await expect(remoteReview).toContainText(`${remoteState.id} revision ${remoteRevision}`);
  step = "acknowledge_compose_remote_volume_backup";
  await remoteReview.getByRole("checkbox").check();
  for (const width of [320,390,430,1280]) {
    step = "resize_compose_remote_backup_controls";
    await page.setViewportSize({width,height:900});
    step = "verify_compose_remote_backup_responsive_width";
    if (await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth)) throw Error("remote Compose backup responsive overflow");
  }
  const remoteAdmissionPending = response(page, `/api/v1/resources/${resourceID}/storage-backups`);
  step = "confirm_compose_remote_volume_backup";
  await remoteReview.getByRole("button", {name:"Confirm Compose volume backup",exact:true}).click();
  step = "await_compose_remote_volume_backup_admission";
  const remoteAdmissionResponse = await remoteAdmissionPending;
  step = "validate_compose_remote_volume_backup_admission";
  const remoteBackup = await checkedResponse(Promise.resolve(remoteAdmissionResponse),202);
  const remoteBody = remoteAdmissionResponse.request().postDataJSON();
  if (remoteBackup.volume!=="vikunja-data" || remoteBackup.source_deployed_operation_id!==startedOperation || remoteBody.expected_deployed_operation_id!==startedOperation || remoteBody.acknowledge_interruption!==true || remoteBody.remote_target?.target_id!==remoteState.id || remoteBody.remote_target?.revision!==remoteRevision) throw Error("remote Compose backup identity changed");
  safe.browser_compose_volume_remote_backup_admitted=true;
  step = "report_compose_remote_volume_backup";
  await report("volume_remote_backed_up",remoteBackup.id);
  step = "await_compose_remote_retry_phase";
  const retryState = await state("volume_remote_retry");
  if (retryState.id!==remoteBackup.id) throw Error("remote retry archive identity changed");
  step = "reload_compose_remote_backup_history";
  await hosting(page);
  step = "select_compose_remote_backup_history";
  await volumeBackups.getByRole("button", {name:`View volume backup ${remoteBackup.id}`,exact:true}).click();
  step = "verify_compose_remote_local_archive_completion";
  await expect(volumeBackups).toContainText("Backup state: succeeded");
  step = "verify_compose_remote_local_archive_artifact";
  await expect(volumeBackups).toContainText("Local artifact:");
  step = "verify_compose_remote_writer_resumption";
  await expect(volumeBackups).toContainText("Writer resumption: resumed");
  const remoteHistory = volumeBackups.getByRole("region", {name:"Remote archive transfers",exact:true});
  step = "verify_compose_remote_failed_transfer_history";
  await expect(remoteHistory).toContainText("failed");
  step = "read_compose_remote_failed_transfer";
  const failedTransfersResponse = await page.request.get(`/api/v1/storage-backups/${remoteBackup.id}/transfers`);
  step = "validate_compose_remote_failed_transfer";
  const failedTransfers = await checkedResponse(Promise.resolve(failedTransfersResponse),200);
  const failedUpload = failedTransfers.find(v=>v.mode==="upload" && v.state==="failed");
  if (!failedUpload || failedUpload.backup_id!==remoteBackup.id || failedUpload.target_id!==remoteState.id || failedUpload.target_revision!==remoteRevision) throw Error("failed remote upload identity changed");
  const retryPending = response(page, `/api/v1/storage-backups/${remoteBackup.id}/transfers/retry`);
  step = "retry_compose_remote_upload_same_archive";
  await remoteHistory.getByRole("button", {name:"Retry remote upload of the same archive",exact:true}).click();
  step = "await_compose_remote_upload_retry_response";
  const retryResponse = await retryPending;
  step = "validate_compose_remote_upload_retry_response";
  const retriedUpload = await checkedResponse(Promise.resolve(retryResponse),202);
  if (retryResponse.request().postDataJSON().expected_transfer_id!==failedUpload.id || retriedUpload.backup_id!==remoteBackup.id || retriedUpload.target_id!==remoteState.id || retriedUpload.target_revision!==remoteRevision || retriedUpload.blob?.sha256!==failedUpload.blob?.sha256) throw Error("remote retry archive or target changed");
  step = "verify_compose_remote_upload_success_history";
  await expect(remoteHistory).toContainText(`${retriedUpload.id}: succeeded`,{timeout:30000});
  safe.browser_compose_volume_remote_failure_history_reloaded=true;
  safe.browser_compose_volume_remote_same_archive_retry=true;
  step = "report_compose_remote_upload_retry";
  await report("volume_remote_retried",retriedUpload.id);
  const backupState = await state("database_backup");
  await hosting(page);
  step = "seed_existing_import_target";
  // Seed an existing target via its real application UI, so --clean replacement
  // is exercised against initialized tables rather than an empty PostgreSQL DB.
  const targetContext = await browser.newContext({baseURL:restoreOrigin,ignoreHTTPSErrors:true,acceptDownloads:true,locale:"en-US"});
  try {
    const targetPage = await targetContext.newPage();
    targetPage.setDefaultTimeout(30000);
    await targetPage.goto("/register");
    await targetPage.getByLabel("Username",{exact:true}).fill("targetbeforeimport");
    await targetPage.getByLabel("Email address",{exact:true}).fill("targetbeforeimport@example.test");
    await targetPage.getByLabel("Password",{exact:true}).fill(password);
    const seeded=appResponse(targetPage,"POST",/^\/api\/v[12]\/register$/);
    await targetPage.getByRole("button",{name:"Create account",exact:true}).click();
    const seededResponse=await seeded;if(![200,201].includes(seededResponse.status()))throw Error("target signup rejected");
    await expect(targetPage.locator("#registerform")).toHaveCount(0);
    await targetPage.goto("/projects/new");
    await targetPage.getByLabel("Title",{exact:true}).fill("Target records to replace");
    const targetProject=appResponse(targetPage,["PUT","POST"],/^\/api\/v[12]\/projects$/);
    await targetPage.getByRole("button",{name:"Create",exact:true}).click();
    const createdTarget=await targetProject;if(![200,201].includes(createdTarget.status()))throw Error("target project rejected");
    await addTask(targetPage,"Target record to replace");
  } finally { await targetContext.close(); }
  const backups=application.getByRole("region",{name:"Component database backups",exact:true});
  step="create_component_database_backup_schedule";
  const schedules=backups.getByRole("region",{name:"Component database backup schedules",exact:true});
  await schedules.getByRole("combobox",{name:"Scheduled database component",exact:true}).selectOption("postgresql");
  await schedules.getByRole("textbox",{name:"Backup frequency",exact:true}).fill("every_minute");
  const schedulePending=response(page,`/api/v1/resources/${resourceID}/database-backup-schedules`);
  await schedules.getByRole("button",{name:"Create backup schedule",exact:true}).click();
  const scheduleResponse=await schedulePending;
  const scheduleRequest=scheduleResponse.request().postDataJSON();
  safe.database_backup_http_status=scheduleResponse.status();
  safe.database_backup_request_matches_started_operation=scheduleRequest.expected_deployed_operation_id===startedOperation;
  const schedule=await scheduleResponse.json();
  if(scheduleResponse.status()!==200||schedule.component!=="postgresql"||schedule.resource_id!==resourceID||schedule.version!=="postgresql.compose-logical-local.schedule.v1"||!safe.database_backup_request_matches_started_operation)throw Error("schedule component or deployment identity changed");
  for(const width of [1280,320,390,430]){
    await page.setViewportSize({width,height:900});
    await expect(schedules.getByRole("button",{name:"Disable schedule",exact:true})).toBeVisible();
    if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth))throw Error("schedule responsive overflow");
  }
  await page.setViewportSize({width:1280,height:900});
  step="await_component_database_backup_occurrence";
  let occurrence;
  await expect.poll(async()=>{
    const r=await page.request.get(`/api/v1/resources/${resourceID}/database-backup-schedules/${schedule.id}/occurrences`);
    if(r.status()!==200)throw Error("schedule occurrence history unavailable");
    const values=await r.json();
    occurrence=values.find(v=>v.operation_id);
    return Boolean(occurrence);
  },{timeout:75000,intervals:[500,1000,2000]}).toBe(true);
  if(occurrence.component!=="postgresql"||occurrence.schedule_revision!==schedule.revision||occurrence.schedule_id!==schedule.id)throw Error("occurrence component or revision changed");
  const disablePending=page.waitForResponse(r=>r.request().method()==="PUT"&&new URL(r.url()).pathname===`/api/v1/resources/${resourceID}/database-backup-schedules/${schedule.id}`);
  await schedules.getByRole("button",{name:"Disable schedule",exact:true}).click();
  const disableResponse=await disablePending,disabled=await disableResponse.json();
  if(disableResponse.status()!==200||disabled.enabled||disabled.component!==schedule.component||disabled.revision!==schedule.revision+1)throw Error("schedule disable changed component");
  const backupResponse=await page.request.get(`/api/v1/database-backups/${occurrence.operation_id}`);
  const logicalBackup=await backupResponse.json();
  safe.database_backup_response_resource_matches=logicalBackup.source_resource_id===resourceID;
  safe.database_backup_response_component_matches=logicalBackup.source_component==="postgresql";
  if(backupResponse.status()!==200||logicalBackup.id!==occurrence.operation_id||logicalBackup.source_component!=="postgresql"||logicalBackup.source_resource_id!==resourceID||logicalBackup.source_deployed_operation_id!==startedOperation)throw Error("scheduled backup component identity changed");
  safe.browser_component_database_schedule=true;
  await report("database_backed_up",logicalBackup.id);
  await state("database_mutate");
  step="source_write_after_logical_backup";
  const sourcePage=freshAppContext.pages()[0];
  await sourcePage.goto(`/projects/${projectID}`);
  const afterBackupTitle="Source record after logical backup";
  const afterBackupTask=await addTask(sourcePage,afterBackupTitle);
  await report("database_source_mutated",String(afterBackupTask));
  await state("database_import");
  await hosting(page);
  step="review_component_database_import";
  await schedules.getByRole("button",{name:"Refresh schedules",exact:true}).click();
  await schedules.getByRole("listitem").filter({hasText:schedule.id}).getByRole("button",{name:"View occurrences",exact:true}).click();
  await expect(schedules.getByRole("region",{name:"Backup schedule occurrences",exact:true})).toContainText("succeeded");
  await backups.getByRole("button",{name:"Refresh database backup history",exact:true}).click();
  await backups.getByRole("combobox",{name:"Backup to restore",exact:true}).selectOption(logicalBackup.id);
  await backups.getByRole("combobox",{name:"Restore target application",exact:true}).selectOption(backupState.id);
  await backups.getByRole("combobox",{name:"Restore target database component",exact:true}).selectOption("postgresql");
  await backups.getByRole("checkbox",{name:"Replace target database and interrupt its writers",exact:true}).check();
  const importPending=response(page,`/api/v1/database-backups/${logicalBackup.id}/restores`);
  await backups.getByRole("button",{name:"Restore database backup",exact:true}).click();
  const imported=await checkedResponse(importPending,202);
  if(imported.target_resource_id!==backupState.id||imported.target_component!=="postgresql"||imported.target_resource_id===resourceID)throw Error("import target changed");
  await report("database_imported",imported.id);
  await state("database_check");
  step="verify_fresh_login_logical_replacement";
  const restoredContext=await browser.newContext({baseURL:restoreOrigin,ignoreHTTPSErrors:true,acceptDownloads:true,locale:"en-US"});
  try {
    const restored=await restoredContext.newPage();restored.setDefaultTimeout(30000);
    await restored.goto("/login");
    await restored.getByLabel("Username Or Email Address",{exact:true}).fill("targetbeforeimport");
    await restored.getByLabel("Password",{exact:true}).fill(password);
    const oldLogin=appResponse(restored,"POST",/^\/api\/v[12]\/login$/);
    await restored.getByRole("button",{name:"Login",exact:true}).click();
    if((await oldLogin).status()!==403)throw Error("prior target records survived replacement");
    await restored.getByLabel("Username Or Email Address",{exact:true}).fill(username);
    await restored.getByLabel("Password",{exact:true}).fill(password);
    const restoredLogin=appResponse(restored,"POST",/^\/api\/v[12]\/login$/);
    await restored.getByRole("button",{name:"Login",exact:true}).click();
    if((await restoredLogin).status()!==200)throw Error("restored original login rejected");
    await expect(restored.locator("#loginform")).toHaveCount(0);
    await restored.goto(`/tasks/${taskID}`);
    await expect(restored.getByRole("heading",{name:"Title",exact:true})).toHaveText(taskTitle);
    // DB attachment metadata is restored, but this slice deliberately restores
    // no upload bytes. New files use the target's independently retained volume.
    await restored.goto(`/projects/${projectID}`);
    await expect(restored.getByText(afterBackupTitle,{exact:true})).toHaveCount(0);
    const newTargetTask=await addTask(restored,"Independent target write after import");
    await restored.goto(`/tasks/${newTargetTask}`);
    await upload(restored,"target-after-import.txt",Buffer.from("Independent target upload after logical import\n"));
    await download(restored,"target-after-import.txt",Buffer.from("Independent target upload after logical import\n"));
    await sourcePage.goto(`/tasks/${afterBackupTask}`);
    await expect(sourcePage.getByRole("heading",{name:"Title",exact:true})).toHaveText(afterBackupTitle);
  } finally { await restoredContext.close(); }
  safe.browser_component_database_backup_import=true;
  safe.database_only_original_uploads_not_restored=true;
  safe.database_source_later_write_excluded_target_writes_independent=true;
  await report("database_checked",String(taskID));
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
