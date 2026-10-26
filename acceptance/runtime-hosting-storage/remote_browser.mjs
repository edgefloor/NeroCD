// Real browser against the candidate's Go-served UI. No routes are intercepted.
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
const require = createRequire(resolve("web/app/package.json"));
const { chromium, expect, request } = require("@playwright/test");
const origin = process.env.NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN;
const key = process.env.NEROCD_STORAGE_REMOTE_BROWSER_KEY;
const evidence = process.env.NEROCD_STORAGE_REMOTE_HOST_EVIDENCE;
if (!/^https:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin ?? "") || !/^[a-f0-9]{64}$/.test(key ?? "") || !evidence?.startsWith("/")) throw Error("exact private browser profile required");
let api, browser, context, page;
let stopped = false;
const cleanupFailures = new Set();
const closing = new Map();
async function closeOwned() {
  // A context must settle before its owning browser is closed. Concurrent
  // signal/finally callers join the same promise for each owned resource.
  for (const [name, resource, method] of [["context", context, "close"], ["browser", browser, "close"], ["api", api, "dispose"]]) {
    if (!resource) continue;
    if (!closing.has(name)) closing.set(name, Promise.resolve().then(() => resource[method]()));
    const [result] = await Promise.allSettled([closing.get(name)]);
    if (result.status === "rejected") cleanupFailures.add(name);
  }
  return cleanupFailures.size === 0;
}
function stop(code) {
  if (stopped) return;
  stopped = true;
  process.exitCode = code;
  void closeOwned();
}
const onInterrupt = () => stop(130);
const onTerminate = () => stop(143);
process.once("SIGINT", onInterrupt);
process.once("SIGTERM", onTerminate);
const deadline = Date.now() + 24 * 60 * 1000;
const deadlineTimer = setTimeout(() => stop(1), 24 * 60 * 1000);
deadlineTimer.unref();
function requireActive() { if (stopped) throw Error("browser workflow cancelled"); }
const pause = () => new Promise(resolve => setTimeout(resolve, 250));
async function state(phase) {
  while (Date.now() < deadline) {
    requireActive();
    try { const response = await api.get("/__storage_remote_fixture/state"); if (response.status() === 200) { const value = await response.json(); if (value.phase === phase) return value; } } catch {}
    requireActive();
    await pause();
  }
  throw Error("fixture phase deadline: " + phase);
}
async function report(action, id) {
  const response = await api.post("/__storage_remote_fixture/report", { data: { action, id } });
  if (response.status() !== 204) throw Error("fixture report rejected: " + action);
}
async function panel(m, reload = false, targetPage = page) {
  if (reload) await targetPage.reload();
  else await targetPage.goto(`/projects#storage-${m.resource_id}`);
  const hosting = targetPage.getByRole("region", { name: "Project hosting", exact: true });
  await hosting.getByLabel("Hosting project", { exact: true }).selectOption("proj_platform");
  await hosting.getByRole("button", { name: "storage runtime fixture", exact: true }).click();
  const application = hosting.getByRole("article", { name: "remote uploaded application B", exact: true });
  const result = application.getByRole("region", { name: "Upload backups and restores", exact: true });
  await expect(result).toBeVisible();
  return result;
}
async function selectBackup(p, id) {
  await p.getByRole("button", { name: `Backup ${id}`, exact: true }).click();
  await expect(p.locator('dl[aria-label="Selected backup metadata"]')).toContainText(id);
}
const safe = { status: "failed", served_candidate_ui: true, intercepted_requests: false };
try {
  requireActive();
  api = await request.newContext({ baseURL: origin, ignoreHTTPSErrors: true, extraHTTPHeaders: { "X-NeroCD-Fixture-Key": key } });
  requireActive();
  browser = await chromium.launch({ headless: true, handleSIGINT: false, handleSIGTERM: false });
  requireActive();
  context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, viewport: { width: 1280, height: 900 } });
  requireActive();
  page = await context.newPage();
  page.setDefaultTimeout(30000);
  let initial = await state("target");
  const manifest=JSON.parse(await readFile(resolve(evidence,"browser-ui-manifest.json"),"utf8"));
  if(manifest.schema!=="nerocd.ui-assets/v1" || !manifest.files["index.html"]) throw Error("candidate UI manifest absent");
  for(const [path,sha] of Object.entries(manifest.files)) {
    if(path===".gitkeep") continue;
    const served=await api.get("/"+path);
    if(served.status()!==200 || createHash("sha256").update(await served.body()).digest("hex")!==sha) throw Error("served candidate UI asset differs: "+path);
  }
  safe.served_assets_match_manifest=true;
  await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill("admin@example.local");
  await page.getByLabel("Password", { exact: true }).fill("admin");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  let p = await panel(initial);
  const destinations=p.getByRole("region",{name:"Backup targets",exact:true});
  for(const [label,value] of [["Destination name","Storage runtime S3"],["S3 endpoint","http://127.0.0.1:9001"],["S3 bucket","nerocd-storage-runtime"],["S3 region","us-east-1"],["S3 access key",initial.access_key],["S3 secret key",initial.secret_key]]) await destinations.getByLabel(label,{exact:true}).fill(value);
  const createResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith("/projects/proj_platform/database-backup-targets"));
  await destinations.getByRole("button",{name:"Create backup destination",exact:true}).click();
  const createdHTTP=await createResponse;
  if(createdHTTP.status()!==200) throw Error("storage-only UI target creation failed");
  const created=await createdHTTP.json();
  initial.access_key="";initial.secret_key="";
  await report("target",created.id);safe.storage_target_created=true;
  const validationState=await state("validation");
  const validationResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(`/database-backup-targets/${created.id}/storage-validations`));
  await destinations.getByRole("button",{name:"Validate Storage runtime S3 from this runner",exact:true}).click();
  const validationHTTP=await validationResponse;
  if(validationHTTP.status()!==202) throw Error("storage-specific UI validation failed");
  await report("validation",(await validationHTTP.json()).id);safe.storage_target_validated=true;
  initial=await state("backup");
  await destinations.getByRole("button",{name:"Refresh destinations",exact:true}).click();
  const target = p.getByLabel("Remote backup target", { exact: true });
  await expect(target.locator(`option[value^="${initial.target_id}:"]`)).toHaveCount(1);
  const value = await target.locator("option").evaluateAll(options => options.find(option => option.textContent.includes("Storage runtime S3"))?.value);
  if (!value?.startsWith(initial.target_id + ":")) throw Error("validated storage target not offered");
  await target.selectOption(value);
  const recurring=p.getByRole("region",{name:"Recurring upload backups",exact:true});
  const dueNow=new Date();
  await recurring.getByLabel("Backup frequency",{exact:true}).fill(`${dueNow.getUTCMinutes()} ${dueNow.getUTCHours()} * * *`);
  await recurring.getByLabel("Backup timezone",{exact:true}).fill("UTC");
  const schedulePath=`/resources/${initial.resource_id}/storage-backup-schedules`;
  const lostResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(schedulePath));
  await recurring.getByRole("button",{name:"Create backup schedule",exact:true}).click();
  const lostHTTP=await lostResponse;
  if(lostHTTP.status()!==503) throw Error("schedule ACK loss fixture absent");
  const saved=lostHTTP.request().postDataJSON();
  const replayResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(schedulePath));
  await recurring.getByRole("button",{name:"Retry identical schedule request",exact:true}).click();
  const replayHTTP=await replayResponse;
  if(replayHTTP.status()!==200||JSON.stringify(replayHTTP.request().postDataJSON())!==JSON.stringify(saved)) throw Error("schedule retry changed identity or inputs");
  const scheduled=await replayHTTP.json();
  if(scheduled.remote_target?.target_id!==initial.target_id)throw Error("schedule did not pin remote target");
  const archive=recurring.getByRole("button",{name:/^View scheduled archive /});
  await expect(archive).toHaveCount(1);
  const backup={id:(await archive.textContent()).replace("View scheduled archive ","")};
  const editResponse=page.waitForResponse(response=>response.request().method()==="PUT"&&response.url().endsWith(schedulePath+"/"+scheduled.id));
  await recurring.getByLabel("Backup frequency",{exact:true}).fill("hourly");
  await recurring.getByRole("button",{name:"Save schedule changes",exact:true}).click();
  const edited=await editResponse;if(edited.status()!==200||(await edited.json()).revision!==2)throw Error("schedule edit revision");
  const disableResponse=page.waitForResponse(response=>response.request().method()==="PUT"&&response.url().endsWith(schedulePath+"/"+scheduled.id));
  await recurring.getByRole("button",{name:"Disable schedule",exact:true}).click();
  const disabled=await disableResponse;const disabledBody=await disabled.json();if(disabled.status()!==200||disabledBody.enabled||disabledBody.revision!==3)throw Error("schedule disable revision");
  await expect(recurring).toContainText("revision 3");
  await report("backup",backup.id);
  safe.schedule_owner_create_replay_edit_disable_history=true;
  safe.target_selected = true;
  const denied = await state("denied");
  p = await panel(denied, true);
  await selectBackup(p, backup.id);
  const transfers = p.getByRole("region", { name: "Remote archive transfers", exact: true });
  await expect(transfers).toContainText("access_denied");
  await expect(transfers).toContainText("Local archive remains independent");
  await expect(transfers.getByRole("button", { name: "Retry remote upload of the same archive", exact: true })).toBeEnabled();
  await report("warning", backup.id);
  safe.denied_warning_survived_reload = true;
  await state("retry");
  const retryResponse = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`/storage-backups/${backup.id}/transfers/retry`));
  await transfers.getByRole("button", { name: "Retry remote upload of the same archive", exact: true }).click();
  const retryHTTP = await retryResponse;
  if (retryHTTP.status() !== 202) throw Error("UI retry not admitted");
  const retry = await retryHTTP.json();
  await report("retry", retry.id);
  safe.real_upload_retry = true;
  const restoreState = await state("restore");
  p = await panel(restoreState, true);
  await selectBackup(p, backup.id);
  const form = p.getByRole("form", { name: "Restore backup into a new resource", exact: true });
  await form.getByRole("checkbox", { name: "Download and verify the remote archive before restoring", exact: true }).check();
  await form.getByLabel("New restored resource name", { exact: true }).fill("remote restored uploads");
  const restoreResponse = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`/storage-backups/${backup.id}/restores`));
  await form.getByRole("button", { name: "Restore into new resource", exact: true }).click();
  const restoreHTTP = await restoreResponse;
  if (restoreHTTP.status() !== 202 || restoreHTTP.request().postDataJSON().remote_source?.transfer_id !== retry.id) throw Error("UI restore did not request exact remote transfer");
  const restored = await restoreHTTP.json();
  await report("restore", restored.operation.id);
  safe.remote_restore_requested = true;
  await state("complete");
  p = await panel(restoreState, true);
  await selectBackup(p, backup.id);
  await p.getByLabel("Restore operation ID", { exact: true }).fill(restored.operation.id);
  const read = p.locator('[aria-label="Read restore history by operation ID"]');
  await read.getByRole("button", { name: "Read restore details", exact: true }).click();
  await expect(p).toContainText(`Restore operation ${restored.operation.id}`);
  await expect(p).toContainText("succeeded");
  safe.restore_history_after_reload = true;
  for (const width of [320, 390, 430, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(p).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
    if (overflow) throw Error("storage workflow horizontal overflow at " + width);
  }
  safe.responsive_widths = [320, 390, 430, 1280];
  const viewerContext=await browser.newContext({baseURL:origin,ignoreHTTPSErrors:true});
  try {
    const viewer=await viewerContext.newPage();
    await viewer.goto("/sign-in");
    await viewer.getByLabel("Email",{exact:true}).fill("viewer@example.local");
    await viewer.getByLabel("Password",{exact:true}).fill("viewer");
    await viewer.getByRole("button",{name:"Sign in",exact:true}).click();
    await expect(viewer.getByRole("button",{name:"Account",exact:true})).toBeVisible();
    const viewerPanel=await panel(restoreState,false,viewer);
    await selectBackup(viewerPanel,backup.id);
    await expect(viewerPanel.getByRole("region",{name:"Remote archive transfers",exact:true})).toContainText(retry.id);
    await expect(viewerPanel.getByRole("button",{name:"Request upload backup",exact:true})).toHaveCount(0);
    await expect(viewerPanel.getByLabel("Remote backup target",{exact:true})).toHaveCount(0);
    await expect(viewerPanel.getByRole("form",{name:"Restore backup into a new resource",exact:true})).toHaveCount(0);
    await expect(viewerPanel.getByRole("button",{name:"Retry remote upload of the same archive",exact:true})).toHaveCount(0);
    const viewerSchedules=viewerPanel.getByRole("region",{name:"Recurring upload backups",exact:true});
    await viewerSchedules.getByRole("button",{name:`View schedule ${scheduled.id}`,exact:true}).click();
    await expect(viewerSchedules).toContainText(backup.id);
    await expect(viewerSchedules.getByRole("button",{name:"Disable schedule",exact:true})).toHaveCount(0);
    await expect(viewerSchedules.getByRole("form",{name:"Save recurring upload backup",exact:true})).toHaveCount(0);
    safe.viewer_schedule_read_only=true;
    safe.viewer_history_and_controls=true;
  } finally {await viewerContext.close();}
  safe.status = "passed";
  await report("done", restored.operation.id);
} catch {
  if (!process.exitCode) process.exitCode=1;
  safe.status="failed"; safe.failure=stopped?"browser_workflow_cancelled":"browser_workflow_failed";
} finally {
  clearTimeout(deadlineTimer);
  if (stopped) {safe.status="failed"; safe.failure="browser_workflow_cancelled";}
  if (!await closeOwned()) {
    safe.status="failed";
    safe.cleanup_failures=[...cleanupFailures].sort();
    safe.failure ??= "browser_cleanup_failed";
    if (!process.exitCode) process.exitCode=1;
  }
  if (stopped) {safe.status="failed"; safe.failure="browser_workflow_cancelled";}
  try {
    await writeFile(resolve(evidence, "browser-safe.json"), JSON.stringify(safe, null, 2) + "\n", { mode: 0o600 });
  } catch {
    if (!process.exitCode) process.exitCode=1;
  } finally {
    process.removeListener("SIGINT", onInterrupt);
    process.removeListener("SIGTERM", onTerminate);
  }
}
