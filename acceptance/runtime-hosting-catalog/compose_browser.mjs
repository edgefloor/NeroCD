// Connected browser against the candidate Go-served UI; no route interception.
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { createHash, randomBytes } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
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
  const gitCompose=Boolean(initial.git_repository);
  let resource;
  if (gitCompose) {
    step="save_git_compose_application";
    const form=h.getByRole("form",{name:"Create application",exact:true});
    await form.getByLabel("Application source",{exact:true}).selectOption("git");
    await form.getByLabel("Application name",{exact:true}).fill(applicationName);
    await form.getByLabel("Git repository URL",{exact:true}).fill(initial.git_repository);
    await form.getByLabel("Git branch, tag, or commit",{exact:true}).fill("main");
    await form.getByLabel("Git source access",{exact:true}).selectOption("https_basic");
    await form.getByLabel("Build strategy",{exact:true}).selectOption("compose");
    await form.getByLabel("Context directory",{exact:true}).fill("project");
    await form.getByLabel("Compose file",{exact:true}).fill("deploy/compose.yaml");
    await form.getByLabel("Public Compose service",{exact:true}).fill("vikunja");
    await form.getByLabel("Public Compose service port",{exact:true}).fill("3456");
    const variables=form.getByRole("region",{name:"Compose build variables",exact:true});
    await variables.getByRole("button",{name:"Add build variable",exact:true}).click();
    await variables.getByLabel("Build variable name 1",{exact:true}).fill("CONFIGURED");
    await variables.getByLabel("Build variable value 1",{exact:true}).fill("operator-value");
    await variables.getByRole("button",{name:"Add build variable",exact:true}).click();
    await variables.getByLabel("Build variable name 2",{exact:true}).fill("MULTILINE");
    await variables.getByLabel("Build variable value 2",{exact:true}).fill("first line\nsecond line");
    const buildRegistry=form.getByRole("region",{name:"Build registry credentials",exact:true});
    await buildRegistry.getByLabel("Build registry credential",{exact:true}).selectOption("private");
    const buildSecrets=buildRegistry.getByLabel("Build registry credential secret",{exact:true});
    const buildSecret=buildSecrets.getByRole("option").filter({hasText:"Compose build registry · "});
    await expect(buildSecret).toHaveCount(1);
    const selectedBuildSecret=await buildSecret.getAttribute("value");
    await buildSecrets.selectOption(selectedBuildSecret);
    await buildRegistry.getByLabel("Build registry credential version",{exact:true}).selectOption("1");
    const buildSecretRows=form.getByRole("region",{name:"Build secrets",exact:true});
    await buildSecretRows.getByRole("button",{name:"Add build secret",exact:true}).click();
    await buildSecretRows.getByLabel("Build secret name 1",{exact:true}).fill("DEPENDENCY_TOKEN");
    const dependencySelect=buildSecretRows.getByRole("combobox",{name:"Build secret 1 secret",exact:true});
    const dependencyOption=dependencySelect.getByRole("option").filter({hasText:"Compose build dependency · "});
    await expect(dependencyOption).toHaveCount(1);
    const selectedDependency=await dependencyOption.getAttribute("value");
    await dependencySelect.selectOption(selectedDependency);
    await buildSecretRows.getByRole("combobox",{name:"Build secret 1 version",exact:true}).selectOption("1");
    const saved=response(page,"/api/v1/resources");
    await form.getByRole("button",{name:"Save application",exact:true}).click();
    resource=await checkedResponse(saved,201);
    if(resource.source.git.settings.compose_file!=="deploy/compose.yaml"||resource.source.git.repository_url!==initial.git_repository)throw Error("saved Git Compose source mismatch");
    if(resource.source.git.settings.build_variables?.length!==2||resource.source.git.settings.build_variables[0].name!=="CONFIGURED"||resource.source.git.settings.build_variables[0].value!=="operator-value"||resource.source.git.settings.build_variables[1].name!=="MULTILINE"||resource.source.git.settings.build_variables[1].value!=="first line\nsecond line")throw Error("saved literal build variables mismatch");
    if(resource.source.git.settings.build_registry_credential?.secret_id!==selectedBuildSecret||resource.source.git.settings.build_registry_credential?.version!==1)throw Error("saved build registry ref mismatch");
    if(resource.source.git.settings.build_secrets?.length!==1||resource.source.git.settings.build_secrets[0].name!=="DEPENDENCY_TOKEN"||resource.source.git.settings.build_secrets[0].secret?.secret_id!==selectedDependency||resource.source.git.settings.build_secrets[0].secret?.version!==1)throw Error("saved build-only secret ref mismatch");
    safe.browser_git_compose_build_variables=true;
    safe.browser_git_compose_create=true;
    safe.saved_compose_configuration=true;
  } else {
  step = "install_catalog_compose";
  const source = await readFile(resolve("examples/compose/vikunja-postgresql.yaml"), "utf8");
  if (createHash("sha256").update(source).digest("hex") !== "2ed2a1cc22b4c020f26e214d4e96a419eb56445a9d14f87f2df703a7ee9de767") throw Error("shipped Compose example changed");
  const catalog = h.getByRole("region", { name: "Hosting catalog", exact: true });
  await catalog.getByLabel("Catalog application", { exact: true }).selectOption("vikunja-with-postgresql");
  await catalog.getByLabel("Installation name", { exact: true }).fill(applicationName);
  await catalog.getByLabel("Catalog destination", { exact: true }).selectOption(initial.id);
  await catalog.getByLabel("Public URL", { exact: true }).fill(appOrigin);
  await expect(catalog.getByRole("heading", { name: "Vikunja with PostgreSQL installation preview", exact: true })).toBeVisible();
  for (const width of [1280, 320, 390, 430]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(catalog.getByLabel("Public URL", { exact: true })).toBeVisible();
    await expect(catalog.getByRole("heading", { name: "Vikunja with PostgreSQL installation preview", exact: true })).toBeVisible();
    await expect(catalog.getByRole("button", { name: "Install application", exact: true })).toBeVisible();
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw Error("catalog installation responsive overflow");
  }
  safe.catalog_compose_install_responsive_widths = [1280, 320, 390, 430];
  await page.setViewportSize({ width: 1280, height: 900 });
  const firstWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog.getByRole("button", { name: "Install application", exact: true }).click();
  const firstInstall = await firstWait;
  if (firstInstall.status() !== 503 || firstInstall.request().postDataJSON().public_url !== appOrigin) throw Error("catalog committed response-loss contract missing");
  const originalRequest = firstInstall.request().postData();
  await expect(catalog.getByRole("alert")).toContainText("Installation unconfirmed");
  const retryWait = response(page, "/api/v1/hosting-catalog/installations");
  await catalog.getByRole("button", { name: "Retry same installation", exact: true }).click();
  const retryInstall = await retryWait;
  if (retryInstall.status() !== 201 || retryInstall.request().postData() !== originalRequest) throw Error("catalog retry changed request");
  const installed = await checkedResponse(retryInstall, 201);
  resource = installed.resource;
  if (!resource?.id || resource.generation !== 1 || resource.source.kind !== "compose" || resource.source.compose.original !== source || installed.request.template_id !== "vikunja-with-postgresql" || installed.request.immutable_version !== "coolify-b12d1386.vikunja-2.7.1.v1" || installed.request.public_url !== appOrigin || installed.binding.destination_id !== initial.id || installed.runtime_configuration.generation !== 1 || !installed.runtime_configuration.hash || installed.storage_ref.storage_id !== "" || installed.storage_ref.generation !== 0 || installed.storage_ref.hash !== "") throw Error("catalog Compose installation receipt mismatch");
  if (!Array.isArray(installed.credentials) || installed.credentials.length !== 2 || installed.credentials.some(v => !["SERVICE_PASSWORD_POSTGRESQL", "SERVICE_PASSWORD_JWTSECRET"].includes(v.name) || v.kind !== "managed_secret" || !v.secret?.secret_id || v.secret.version !== 1 || v.value !== undefined)) throw Error("generated runtime secret references missing");
  await expect(catalog).toContainText(`Installed ${applicationName}`);
  await expect(catalog.getByRole("button", { name: "Reveal login credentials", exact: true })).toHaveCount(0);
  await expect(catalog.locator("output")).toHaveCount(0);
  safe.browser_catalog_compose_install_identical_retry = true;
  safe.catalog_compose_original_source = true;
  safe.catalog_compose_generated_runtime_refs = true;
  safe.saved_compose_configuration = true;
  }
  const resourceID = resource.id;
  const application = h.getByRole("article", { name: applicationName, exact: true });
  if (initial.git_repository) {
    step="git_compose_wait_initial_grant_read";
    const initialBuilds=application.getByRole("region",{name:`Git builds for ${applicationName}`,exact:true});
    // The fixture adds the grant only after this handoff. Settle the initial
    // empty query so the later refresh cannot deduplicate that pending read.
    await expect(initialBuilds.getByRole("region",{name:"Private HTTPS source grants",exact:true}).getByRole("button",{name:"Refresh source grants",exact:true})).toBeEnabled();
  }
  await report("installed", resourceID);
  const review = application.getByRole("region", { name: "Saved Compose deployment review", exact: true });
  let selectedRegistrySecret;
  let lastBuild, gitHostPort;
  async function deployGit(replacement, stoppedOperation) {
    step="git_compose_refresh_build_history";
    const builds=application.getByRole("region",{name:`Git builds for ${applicationName}`,exact:true});
    await builds.getByRole("button",{name:"Refresh build history",exact:true}).click();
    if(!stoppedOperation) {
      const requestBuild=builds.getByRole("region",{name:"Request private Git build",exact:true});
      const grants=requestBuild.getByRole("combobox",{name:"Private source grant",exact:true});
      step="git_compose_verify_eligible_grant";
      await expect(grants.locator("option:not([value=\"\"]):not([disabled])")).toHaveCount(1);
      step="git_compose_select_private_grant";
      await grants.selectOption(await grants.locator("option:not([value=\"\"]):not([disabled])").getAttribute("value"));
      step="git_compose_acknowledge_private_source";
      await requestBuild.getByRole("checkbox").check();
      const requested=response(page,`/api/v1/resources/${resourceID}/builds`);
      step="git_compose_request_private_build";
      await requestBuild.getByRole("button",{name:"Request private Git build",exact:true}).click();
      step="git_compose_read_private_build_response";
      lastBuild=(await checkedResponse(requested,202)).build.id;
      step="git_compose_wait_build_receipt";
      const stopAt=Date.now()+5*60_000;
      let built;
      while(Date.now()<stopAt){const r=await context.request.get(`/api/v1/hosting-builds/${lastBuild}`);if(r.status()!==200)throw Error("build readback denied");built=(await r.json()).build;if(["succeeded","failed","canceled"].includes(built.state))break;await new Promise(r=>setTimeout(r,250));}
      step="git_compose_verify_build_receipt";
      safe.git_build_state=["queued","sourcing","building","succeeded","failed","canceled","cleanup_required"].includes(built?.state)?built.state:"unrecognized";
      safe.git_build_failure_code=["cancel_requested","source_failed","build_failed","deadline_exceeded","limits_exceeded","cleanup_unconfirmed","authority_lost","canceled"].includes(built?.failure_code)?built.failure_code:built?.failure_code?"unrecognized":"none";
      safe.git_build_source_receipt_present=Boolean(built?.source_receipt?.commit);
      safe.git_build_compose_result_present=Boolean(built?.compose_result?.hash);
      safe.git_build_image_artifact_present=Boolean(built?.artifact);
      if(built?.state!=="succeeded"||!built.compose_result?.hash||!built.source_receipt?.commit||built.artifact)throw Error("Git Compose build did not produce immutable result");
      const buildRegistryRef=built.snapshot?.source?.settings?.build_registry_credential;
      if(!buildRegistryRef?.secret_id||buildRegistryRef.version!==(replacement?2:1))throw Error("build receipt registry version mismatch");
      const dependencyRefs=built.snapshot?.source?.settings?.build_secrets;
      if(dependencyRefs?.length!==1||dependencyRefs[0].name!=="DEPENDENCY_TOKEN"||!dependencyRefs[0].secret?.secret_id||dependencyRefs[0].secret.version!==(replacement?2:1)||dependencyRefs[0].secret.secret_id===buildRegistryRef.secret_id)throw Error("build receipt dependency secret ref mismatch");
      safe.browser_git_compose_build_secret_selection=true;
      safe.browser_git_compose_build_registry_selection=true;
      safe.browser_git_compose_build_receipt=true;
    }
    step="git_compose_refresh_completed_build";
    await builds.getByRole("button",{name:"Refresh build history",exact:true}).click();
    step="git_compose_open_selected_build";
    await builds.getByRole("button",{name:`View build ${lastBuild}`,exact:true}).click();
    const deployment=builds.getByRole("form",{name:"Deploy selected build",exact:true});
    step="git_compose_configure_deployment_readiness";
    await deployment.getByLabel("Build readiness path",{exact:true}).fill("/health");
    await deployment.getByLabel("Build readiness timeout in seconds",{exact:true}).fill("120");
    if(replacement) {
      step="git_compose_select_registry_credential";
      const registry=builds.getByRole("region",{name:"Image registry credentials",exact:true});
      await registry.getByLabel("Image access",{exact:true}).selectOption("private");
      const secrets=registry.getByLabel("Registry credential secret",{exact:true});
      const correct=secrets.getByRole("option").filter({hasText:"Compose registry correct · "});
      await expect(correct).toHaveCount(1);
      selectedRegistrySecret=await correct.getAttribute("value");
      await secrets.selectOption(selectedRegistrySecret);
      await registry.getByLabel("Registry credential version",{exact:true}).selectOption("1");
      step="git_compose_review_route_continuity";
      await expect(builds.locator('[aria-label="Compose route continuity review"]')).toContainText("Keep HTTPS route catalog-runtime.example.test/");
    }
    step="git_compose_acknowledge_stack_interruption";
    await deployment.getByRole("checkbox",{name:/^I reviewed this exact successful Compose build/}).check();
    const requested=response(page,`/api/v1/resources/${resourceID}/deployments/from-build`);
    step="git_compose_request_from_build_deployment";
    await deployment.getByRole("button",{name:stoppedOperation?"Start selected Compose build":"Deploy selected build",exact:true}).click();
    step="git_compose_read_from_build_response";
    const r=await requested;
    const input=r.request().postDataJSON();
    step="git_compose_verify_from_build_authority";
    if(input.build_id!==lastBuild||input.managed_stack?.acknowledge_interruption!==true||input.managed_stack?.expected_stopped_operation_id!==stoppedOperation)throw Error("from-build immutable authority mismatch");
    if(Boolean(input.storage_route_continuity)!==replacement)throw Error("from-build route continuity authority missing");
    if(replacement ? input.registry_credential?.secret_id!==selectedRegistrySecret || input.registry_credential?.version!==1 : input.registry_credential!==undefined)throw Error("from-build registry selection mismatch");
    if(replacement && (input.host_port!==gitHostPort || !(input.storage_route_continuity.expected_loaded_generation>0) || !input.storage_route_continuity.expected_loaded_proxy_operation_id || input.storage_route_continuity.routes?.length!==1))throw Error("from-build stable-port route authority changed");
    safe.browser_git_compose_from_build=true;
    step="git_compose_verify_from_build_acceptance";
    const operation=await checkedResponse(r,202);
    if(replacement && (operation.snapshot?.registry_credential?.secret_id!==selectedRegistrySecret || operation.snapshot?.registry_credential?.version!==1))throw Error("admitted Git registry credential missing");
    if(replacement)safe.browser_git_compose_registry_selection=true;
    if(!replacement){gitHostPort=operation.snapshot?.host_port;if(!(gitHostPort>=1024))throw Error("initial Git stack port missing");}
    else {if(operation.snapshot?.host_port!==gitHostPort || !operation.snapshot?.storage_route_continuity?.hash)throw Error("admitted Git route continuity missing");safe.browser_git_compose_route_continuity=true;}
    return operation.id;
  }
  async function deploy(replacement, stoppedOperation) {
    if(gitCompose)return deployGit(replacement,stoppedOperation);
    step = replacement ? "redeploy_saved_compose" : "deploy_saved_compose";
    if (!replacement) {
      step = "verify_catalog_compose_readiness";
      await expect(review.getByLabel("Readiness path", { exact: true })).toHaveValue("/health");
      await expect(review.getByLabel("Readiness timeout in seconds", { exact: true })).toHaveValue("120");
      safe.catalog_compose_readiness_prefill = true;
    }
    if (replacement) {
      step = "select_compose_registry_credential";
      const registry = review.getByRole("region", { name: "Image registry credentials", exact: true });
      await registry.getByLabel("Image access", { exact: true }).selectOption("private");
      const secrets = registry.getByLabel("Registry credential secret", { exact: true });
      const option = secrets.getByRole("option").filter({ hasText: "Compose registry correct · " });
      await expect(option).toHaveCount(1);
      selectedRegistrySecret = await option.getAttribute("value");
      await secrets.selectOption(selectedRegistrySecret);
      await registry.getByLabel("Registry credential version", { exact: true }).selectOption("1");
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
    if (replacement) {
      if (input.registry_credential?.secret_id !== selectedRegistrySecret || input.registry_credential?.version !== 1) throw Error("Compose registry reference missing");
      safe.browser_compose_registry_selection = true;
    } else {
      if (input.registry_credential !== undefined || input.readiness.path !== "/health" || input.readiness.timeout_seconds !== 120) throw Error("untouched public catalog deployment changed");
      safe.browser_initial_public_compose = true;
    }
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
    const follow = logRegion.getByRole("button", { name: "Follow runtime logs", exact: true });
    await follow.scrollIntoViewIfNeeded();
    await expect(follow).toBeEnabled();
    const opened = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/resources/${resourceID}/runtime-log-sessions`);
    await follow.click();
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
  let volumeBackups,volumeSchedules,volumeSchedule;
  if(!gitCompose){
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
  volumeBackups = application.getByRole("region", {name:"Compose volume backups",exact:true});
  volumeSchedules = volumeBackups.getByRole("region", {name:"Recurring Compose volume backups",exact:true});
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
  volumeSchedule = await checkedResponse(Promise.resolve(volumeScheduleResponse),200);
  const volumeScheduleBody = volumeScheduleResponse.request().postDataJSON();
  if(volumeSchedule.version!=="managed-compose.local-volume-archive.schedule.v1" || volumeSchedule.volume!=="vikunja-data" || volumeSchedule.enabled || volumeSchedule.acknowledge_interruption!==true || volumeScheduleBody.expected_deployed_operation_id!==firstOperation || volumeScheduleBody.acknowledge_interruption!==true || volumeScheduleBody.remote_target)throw Error("volume schedule identity changed");
  safe.browser_compose_volume_schedule_saved_disabled=true;
  step = "report_disabled_compose_volume_schedule";
  await report("volume_scheduled", volumeSchedule.id);
  }
  if(gitCompose){await state("app_failure_recheck");await checkApplication("compose-after-failed-build");safe.git_failures_preserved_records_uploads=true;await report("app_failure_checked",String(taskID));}
  await state("redeploy");
  // Reload the Go-served page to fetch current continuity authority.
  await hosting(page);
  const secondOperation = await deploy(true);
  if (secondOperation === firstOperation) throw Error("redeploy reused prior operation");
  safe.browser_redeployment = true;
  await report("redeployed", secondOperation);
  if(gitCompose) {
    await state("recover_route");
    step="git_compose_verify_data_before_route_recovery";
    await checkApplication("compose-before-route-recovery");
    step="git_compose_read_successful_recovery_evidence";
    const completed=await checkedResponse(await context.request.get(`/api/v1/hosting-operations/${secondOperation}`),200);
    if(completed.id!==secondOperation||completed.state!=="succeeded"||completed.mutation_hold||!completed.result?.ready||completed.result?.uncertain||!completed.complete_receipt||!completed.snapshot?.storage_route_continuity?.hash)throw Error("route recovery changed successful deployment evidence");
    const detail=application.getByRole("region",{name:"Deployment operation detail",exact:true});
    step="git_compose_refresh_completed_operation";
    await detail.getByRole("button",{name:"Refresh operation",exact:true}).click();
    const continuity=detail.getByRole("region",{name:"Storage route continuity",exact:true});
    step="git_compose_refresh_continuity_evidence";
    await continuity.getByRole("button",{name:"Refresh continuity evidence",exact:true}).click();
    const recovery=continuity.getByRole("region",{name:"Proxy continuity recovery",exact:true});
    step="git_compose_review_proxy_recovery";
    await recovery.getByRole("button",{name:"Review proxy continuity recovery",exact:true}).click();
    await recovery.getByRole("checkbox",{name:"I reviewed this completed deployment and want to retry its proxy continuity queue.",exact:true}).check();
    const recovering=response(page,`/api/v1/resources/${resourceID}/storage-route-continuity/${secondOperation}/recover`);
    step="git_compose_submit_proxy_recovery";
    await recovery.getByRole("button",{name:"Retry proxy continuity queue",exact:true}).click();
    const recoveredResponse=await recovering;
    const body=recoveredResponse.request().postDataJSON();
    if(Object.keys(body).length!==3||body.expected_descriptor_hash!==completed.snapshot.storage_route_continuity.hash||body.expected_complete_receipt_id!==completed.complete_receipt.id||body.expected_complete_receipt_hash!==completed.complete_receipt.body_hash)throw Error("proxy recovery did not pin exact completed evidence");
    const recovered=await checkedResponse(recoveredResponse,200);
    if(recovered.resource_id!==resourceID||recovered.operation_id!==secondOperation||recovered.successor_proxy_operation_id!==`pop_storage_${secondOperation}`||!["proxy_pending","loaded"].includes(recovered.state))throw Error("proxy recovery successor identity changed");
    safe.browser_git_compose_route_recovery=true;
    await report("route_recovered",secondOperation);
  }
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
  if(!gitCompose){
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
  step = "inspect_local_archive_download_availability";
  const selectedBackup = await checkedResponse(Promise.resolve(await page.request.get(`/api/v1/storage-backups/${volumeResult.id}`)), 200);
  const optionsWait = page.waitForResponse(async r => r.request().method() === "GET" && new URL(r.url()).pathname === `/api/v1/storage-backups/${volumeResult.id}/archive-options` && await r.finished() === null);
  await volumeBackups.getByRole("button", { name: "Refresh download availability", exact: true }).click();
  const downloadOptions = await checkedResponse(optionsWait, 200);
  if (downloadOptions.available !== true || downloadOptions.backup_id !== selectedBackup.id || !selectedBackup.blob || !downloadOptions.blob || ["backup_id", "destination_id", "daemon_id", "sha256", "size_bytes", "format"].some(name => downloadOptions.blob[name] !== selectedBackup.blob[name])) throw Error("local archive download identity mismatch");
  step = "download_local_compose_archive";
  const downloadLink = volumeBackups.getByRole("link", { name: "Download archive", exact: true });
  await expect(downloadLink).toHaveAttribute("href", `/api/v1/storage-backups/${volumeResult.id}/archive`);
  const privateDownload = await mkdtemp(resolve(tmpdir(), "nerocd-compose-archive-download-"));
  try {
    const [download] = await Promise.all([page.waitForEvent("download"), downloadLink.click()]);
    await download.saveAs(resolve(privateDownload, "archive.tar"));
    if (await download.failure()) throw Error("native archive download failed");
    const bytes = await readFile(resolve(privateDownload, "archive.tar"));
    const sha256 = `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
    if (bytes.length !== selectedBackup.blob.size_bytes || sha256 !== selectedBackup.blob.sha256) throw Error("native archive bytes differ from selected blob");
    safe.browser_compose_archive_download = true;
    safe.compose_archive_download_bytes = bytes.length;
    safe.compose_archive_download_sha256 = sha256;
  } finally {
    await rm(privateDownload, { recursive: true });
  }
  await report("volume_checked",String(taskID));
  step = "await_compose_remote_backup_phase";
  const remoteState = await state("volume_remote_backup");
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(remoteState.id ?? "")) throw Error("remote target identity unavailable");
  step = "open_compose_remote_backup_page";
  await hosting(page);
  step = "start_compose_remote_schedule";
  await volumeSchedules.getByRole("button", {name:"New schedule",exact:true}).click();
  step = "select_compose_remote_volume";
  await volumeBackups.getByRole("combobox", {name:"Named volume to back up",exact:true}).selectOption("vikunja-data");
  const remoteSelector = volumeSchedules.getByRole("combobox", {name:"Remote scheduled Compose backup target",exact:true});
  const remoteOption = remoteSelector.locator(`option[value^="${remoteState.id}:"]`);
  step = "await_validated_compose_remote_target";
  await expect(remoteOption).toHaveCount(1);
  step = "read_validated_compose_remote_target_revision";
  const remoteValue = await remoteOption.getAttribute("value");
  const remoteRevision = Number(remoteValue?.slice(remoteState.id.length + 1));
  if (!remoteValue || !Number.isSafeInteger(remoteRevision) || remoteRevision < 1) throw Error("remote target revision unavailable");
  step = "select_compose_remote_target";
  await remoteSelector.selectOption(remoteValue);
  step = "configure_compose_remote_schedule";
  await volumeSchedules.getByLabel("Backup frequency", {exact:true}).fill("every_minute");
  await volumeSchedules.getByRole("checkbox", {name:"Enabled",exact:true}).check();
  step = "review_compose_remote_schedule";
  await volumeSchedules.getByRole("button", {name:"Review Compose volume schedule",exact:true}).click();
  const remoteReview = volumeSchedules.getByRole("region", {name:"Reviewed Compose volume schedule",exact:true});
  step = "verify_compose_remote_volume_deployment";
  await expect(remoteReview).toContainText(startedOperation);
  step = "verify_compose_remote_target_revision";
  await expect(remoteReview).toContainText(`${remoteState.id} revision ${remoteRevision}`);
  step = "acknowledge_compose_remote_schedule";
  await remoteReview.getByRole("checkbox").check();
  for (const width of [320,390,430,1280]) {
    step = "resize_compose_remote_backup_controls";
    await page.setViewportSize({width,height:900});
    step = "verify_compose_remote_backup_responsive_width";
    if (await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth)) throw Error("remote Compose backup responsive overflow");
  }
  const remoteAdmissionPending = response(page, `/api/v1/resources/${resourceID}/storage-backup-schedules`);
  step = "confirm_compose_remote_schedule";
  await remoteReview.getByRole("button", {name:"Confirm Compose volume schedule",exact:true}).click();
  step = "await_compose_remote_schedule_admission";
  const remoteAdmissionResponse = await remoteAdmissionPending;
  step = "validate_compose_remote_schedule_admission";
  const remoteSchedule = await checkedResponse(Promise.resolve(remoteAdmissionResponse),200);
  const remoteBody = remoteAdmissionResponse.request().postDataJSON();
  if (remoteSchedule.version!=="managed-compose.local-volume-archive.schedule.v1" || remoteSchedule.volume!=="vikunja-data" || !remoteSchedule.enabled || remoteSchedule.revision!==1 || !remoteSchedule.acknowledge_interruption || remoteSchedule.remote_target?.target_id!==remoteState.id || remoteSchedule.remote_target?.revision!==remoteRevision || remoteBody.expected_deployed_operation_id!==startedOperation || remoteBody.acknowledge_interruption!==true || remoteBody.remote_target?.target_id!==remoteState.id || remoteBody.remote_target?.revision!==remoteRevision) throw Error("remote Compose schedule identity changed");
  safe.browser_compose_volume_remote_schedule_saved=true;
  step = "await_compose_remote_schedule_occurrence";
  let remoteOccurrence;
  await expect.poll(async()=>{
    const r=await page.request.get(`/api/v1/resources/${resourceID}/storage-backup-schedules/${remoteSchedule.id}/occurrences`);
    if(r.status()!==200)throw Error("remote volume schedule occurrence history unavailable");
    const values=await r.json();
    remoteOccurrence=values.find(v=>v.operation_id && v.backup_id);
    return Boolean(remoteOccurrence);
  },{timeout:75000,intervals:[500,1000,2000]}).toBe(true);
  if(remoteOccurrence.volume!==remoteSchedule.volume || remoteOccurrence.schedule_revision!==remoteSchedule.revision || remoteOccurrence.schedule_id!==remoteSchedule.id)throw Error("remote volume occurrence identity changed");
  const disableRemotePending = page.waitForResponse(r=>r.request().method()==="PUT" && new URL(r.url()).pathname===`/api/v1/resources/${resourceID}/storage-backup-schedules/${remoteSchedule.id}`);
  step = "disable_admitted_compose_remote_schedule";
  await volumeSchedules.getByRole("button", {name:"Disable schedule",exact:true}).click();
  const disabledRemoteResponse=await disableRemotePending;
  const disabledRemoteSchedule=await checkedResponse(Promise.resolve(disabledRemoteResponse),200);
  if(disabledRemoteSchedule.enabled || disabledRemoteSchedule.volume!==remoteSchedule.volume || disabledRemoteSchedule.revision!==remoteSchedule.revision+1 || disabledRemoteSchedule.remote_target?.target_id!==remoteState.id || disabledRemoteSchedule.remote_target?.revision!==remoteRevision)throw Error("remote volume schedule disable identity changed");
  step = "read_scheduled_compose_remote_backup";
  const remoteBackupResponse=await page.request.get(`/api/v1/storage-backups/${remoteOccurrence.backup_id}`);
  const remoteBackup=await checkedResponse(Promise.resolve(remoteBackupResponse),200);
  if(remoteBackup.id!==remoteOccurrence.backup_id || remoteBackup.operation_id!==remoteOccurrence.operation_id || remoteBackup.volume!==remoteSchedule.volume || remoteBackup.source_deployed_operation_id!==startedOperation)throw Error("scheduled remote volume backup source changed");
  safe.browser_compose_volume_remote_schedule_occurrence=true;
  safe.browser_compose_volume_remote_schedule_disabled=true;
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
  }
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
