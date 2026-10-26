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
  const application = hosting.getByRole("article", { name: m.resource_name ?? "remote uploaded application B", exact: true });
  const result = application.getByRole("region", { name: "Upload backups and restores", exact: true });
  await expect(result).toBeVisible();
  return result;
}
async function selectBackup(p, id) {
  await p.getByRole("button", { name: `Backup ${id}`, exact: true }).click();
  await expect(p.locator('dl[aria-label="Selected backup metadata"]')).toContainText(id);
}
function deliveredRecoveryURL(m, kind) {
  if(typeof m.source_id!=="string"||!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(m.source_id))throw Error("delivered source identity missing");
  const delivered=new URL(m.recovery_url);
  const expected={project_id:"proj_platform",environment_id:m.environment_id,resource_id:m.resource_id,backup_id:m.backup_id,source_kind:kind};
  if(kind==="storage_transfer") expected.transfer_id=m.source_id;
  else if(kind!=="storage_backup") throw Error("unknown storage recovery family");
  if(Object.values(expected).some(value=>typeof value!=="string"||!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value)))throw Error("recovery selection identity missing");
  if(delivered.origin!==origin||delivered.username||delivered.password||delivered.hash||delivered.pathname!=="/projects"||delivered.searchParams.size!==Object.keys(expected).length)throw Error("delivered recovery URL outside exact scope");
  for(const [name,value] of Object.entries(expected))if(delivered.searchParams.getAll(name).length!==1||delivered.searchParams.get(name)!==value)throw Error("delivered recovery URL selection mismatch");
  return m.recovery_url;
}
async function deliveredPanel(m,kind) {
  const delivered=deliveredRecoveryURL(m,kind);
  const mutations=[];
  const observe=request=>{if(!["GET","HEAD","OPTIONS"].includes(request.method())&&/^\/api\/v1\/(resources|storage-backups|hosting-operations)\//.test(new URL(request.url()).pathname))mutations.push(request.method())};
  page.on("request",observe);
  try {
    await page.goto(delivered);
    deliveredRecoveryURL({...m,recovery_url:page.url()},kind);
    const recovery=page.getByRole("region",{name:"Backup notification recovery",exact:true});
    await expect(recovery).toBeVisible();
    await expect(recovery).toContainText(`Backup ${m.backup_id}`);
    const result=recovery.getByRole("region",{name:"Upload backups and restores",exact:true});
    await expect(result.locator('dl[aria-label="Selected backup metadata"]')).toContainText(m.backup_id);
    if(kind==="storage_transfer"){
      await expect(recovery).toContainText(`upload ${m.source_id}`);
      await expect(result.getByRole("region",{name:"Remote archive transfers",exact:true})).toContainText(m.source_id);
    }else{
      const operation=result.getByRole("region",{name:`backup operation ${m.source_id}`,exact:true});
      await expect(operation).toContainText("failed");
      await expect(operation).toContainText("Writer resumption: resumed");
    }
    if(mutations.length)throw Error("opening a delivered recovery URL caused a mutation");
    return result;
  }finally{page.off("request",observe)}
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
  const retentionMode=initial.retention_mode==="true";
  const deletionMode=initial.deletion_mode==="true";
  let deletionState,deletionExecution,deletionFinalExecution;
  let retentionState,retentionPolicy,retentionExecution,retentionBackupIDs;
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
  p = await deliveredPanel(denied,"storage_transfer");
  safe.delivered_upload_recovery_selection=true;
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
  p = await deliveredPanel(denied,"storage_transfer");
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
  const archiveAlert=await state("archive_alert");
  await deliveredPanel(archiveAlert,"storage_backup");
  await report("archive_warning",archiveAlert.backup_id);
  safe.delivered_archive_recovery_selection=true;
  safe.delivered_links_opened_without_mutation=true;
  if(retentionMode){
    retentionState=await state("retention");
    retentionBackupIDs=retentionState.retention_backup_ids.split(",");
    if(retentionBackupIDs.length!==3||new Set(retentionBackupIDs).size!==3)throw Error("exact retention backup inventory absent");
    p=await panel(retentionState);
    const retention=p.getByRole("region",{name:"Backup retention",exact:true});
    const save=retention.getByRole("button",{name:"Save retention policy",exact:true});
    await expect(save).toBeEnabled();
    await expect(retention.getByRole("checkbox",{name:"Enable retention",exact:true})).not.toBeChecked();
    for(const location of ["local","remote"]){
      await expect(retention.getByLabel(`${location} keep_count`,{exact:true})).toHaveValue("7");
      await expect(retention.getByLabel(`${location} keep_days`,{exact:true})).toHaveValue("0");
      await expect(retention.getByLabel(`${location} max_bytes`,{exact:true})).toHaveValue("0");
    }
    await retention.getByRole("checkbox",{name:"Enable retention",exact:true}).check();
    await retention.getByLabel("local keep_count",{exact:true}).fill("1");
    await retention.getByLabel("remote keep_count",{exact:true}).fill("2");
    const base=`/resources/${retentionState.resource_id}/storage-backup-retention`;
    const policyResponse=page.waitForResponse(response=>response.request().method()==="PUT"&&response.url().endsWith(base));
    await save.click();const policyHTTP=await policyResponse;
    if(policyHTTP.status()!==200)throw Error("browser retention policy save failed");
    retentionPolicy=await policyHTTP.json();
    if(!retentionPolicy.enabled||retentionPolicy.automatic||retentionPolicy.local.keep_count!==1||retentionPolicy.remote.keep_count!==2)throw Error("browser retention independent limits changed");
    const previewResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(base+"/previews"));
    await retention.getByRole("button",{name:"Review deletion preview",exact:true}).click();
    const previewHTTP=await previewResponse;if(previewHTTP.status()!==200)throw Error("browser retention preview failed");
    const preview=await previewHTTP.json();
    const selected=preview.items.filter(copy=>copy.selected);
    const actual=selected.map(copy=>`${copy.backup_id}:${copy.location}`).sort();
    const expected=[`${retentionBackupIDs[0]}:local`,`${retentionBackupIDs[1]}:local`,`${retentionBackupIDs[0]}:remote`].sort();
    if(JSON.stringify(actual)!==JSON.stringify(expected)||preview.policy_revision!==retentionPolicy.revision)throw Error("browser preview selected another copy batch");
    await expect(retention).toContainText(`Batch ${preview.id}`);
    await expect(retention).toContainText(preview.evaluated_at);
    for(const copy of selected){
      const row=retention.getByRole("listitem").filter({hasText:copy.backup_id}).filter({hasText:`· ${copy.location} ·`}).filter({hasText:"Selected for deletion"});
      await expect(row).toHaveCount(1);await expect(row).toBeVisible();
    }
    safe.retention_policy_and_exact_preview=true;
    const executeResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(base+"/executions"));
    await retention.getByRole("button",{name:"Delete selected copies",exact:true}).click();
    const lost=await executeResponse;if(lost.status()!==503)throw Error("retention execution ACK loss fixture absent");
    const savedExecution=lost.request().postDataJSON();
    if(savedExecution.preview_id!==preview.id||savedExecution.expected_plan_hash!==preview.plan_hash||savedExecution.expected_policy_revision!==preview.policy_revision)throw Error("browser execution departed reviewed preview");
    const retryResponse=page.waitForResponse(response=>response.request().method()==="POST"&&response.url().endsWith(base+"/executions"));
    await retention.getByRole("button",{name:"Retry saved retention request",exact:true}).click();
    const replay=await retryResponse;
    if(replay.status()!==202||JSON.stringify(replay.request().postDataJSON())!==JSON.stringify(savedExecution))throw Error("browser retention retry changed exact request");
    retentionExecution=await replay.json();
    if(retentionExecution.preview_id!==preview.id||retentionExecution.plan_hash!==preview.plan_hash)throw Error("browser retention replay selected another batch");
    safe.retention_identical_execution_retry=true;
    await report("retention",retentionExecution.id);
    retentionState=await state("retention_history");
    if(retentionState.retention_execution_id!==retentionExecution.id)throw Error("retention history fixture batch mismatch");
    p=await panel(retentionState);
    await expect(p.getByRole("region",{name:"Backup retention",exact:true})).toContainText(`Batch ${retentionExecution.id}: succeeded`);
    await selectBackup(p,retentionBackupIDs[0]);
    const metadata=p.locator('dl[aria-label="Selected backup metadata"]');
    await expect(metadata).toContainText("local: deleted");await expect(metadata).toContainText("remote: deleted");
    await selectBackup(p,retentionBackupIDs[1]);
    await expect(metadata).toContainText("local: available");await expect(metadata).toContainText("remote: available");
    safe.retention_history_and_copy_availability=true;
    await report("retention_history",retentionExecution.id);
  }
  if(deletionMode){
    deletionState=await state("deletion");
    p=await panel(deletionState);
    await selectBackup(p,deletionState.backup_id);
    let deletion=p.getByRole("region",{name:"Delete selected backup copies",exact:true});
    await deletion.getByRole("checkbox",{name:/^Delete local copy /}).check();
    const base=`/resources/${deletionState.resource_id}/storage-backups/${deletionState.backup_id}`;
    const previewResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletion-previews"));
    await deletion.getByRole("button",{name:"Review selected copy deletion",exact:true}).click();
    const reviewed=await previewResponse;if(reviewed.status()!==200)throw Error("manual browser preview rejected");
    const preview=await reviewed.json();
    if(preview.origin!=="manual_selection"||preview.final_copy||preview.items.length!==1||preview.items[0].location!=="local"||preview.backup_id!==deletionState.backup_id)throw Error("manual browser preview selected wrong copies");
    const executeResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletions"));
    await deletion.getByRole("button",{name:"Permanently delete reviewed copies",exact:true}).click();
    const lost=await executeResponse;if(lost.status()!==503)throw Error("manual committed response loss missing");
    const saved=lost.request().postDataJSON();if(saved.preview_id!==preview.id||saved.expected_plan_hash!==preview.plan_hash)throw Error("manual execution left reviewed selection");
    await expect(deletion).toContainText("does not cancel work already accepted by the server");
    const retryResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletions"));
    await deletion.getByRole("button",{name:"Retry identical deletion request",exact:true}).click();
    const retry=await retryResponse;if(retry.status()!==202||JSON.stringify(retry.request().postDataJSON())!==JSON.stringify(saved))throw Error("manual retry changed saved request");
    deletionExecution=await retry.json();
    safe.manual_deletion_selection_and_exact_retry=true;
    await report("deletion",deletionExecution.id);
    deletionState=await state("deletion_history");
    p=await panel(deletionState);
    await selectBackup(p,deletionState.backup_id);
    deletion=p.getByRole("region",{name:"Delete selected backup copies",exact:true});
    await expect(deletion).toContainText(`Deletion ${deletionExecution.id}: succeeded`);
    const metadata=p.locator('dl[aria-label="Selected backup metadata"]');
    await expect(metadata).toContainText("local: deleted");await expect(metadata).toContainText("remote: available");
    await deletion.getByRole("checkbox",{name:/^Delete remote copy /}).check();
    const finalPreviewResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletion-previews"));
    await deletion.getByRole("button",{name:"Review selected copy deletion",exact:true}).click();
    const finalHTTP=await finalPreviewResponse;if(finalHTTP.status()!==200||(await finalHTTP.json()).final_copy!==true)throw Error("connected final-copy warning missing");
    await expect(deletion.getByRole("button",{name:"Permanently delete reviewed copies",exact:true})).toBeDisabled();
    await expect(deletion.getByRole("checkbox",{name:"I understand this deletes the final recoverable copy of this backup.",exact:true})).not.toBeChecked();
    await deletion.getByRole("button",{name:"Discard saved request and review current copies",exact:true}).click();
    safe.manual_deletion_history_and_final_copy_warning=true;
    await report("deletion_history",deletionExecution.id);
    deletionState=await state("deletion_final");
    p=await panel(deletionState);await selectBackup(p,deletionState.backup_id);
    deletion=p.getByRole("region",{name:"Delete selected backup copies",exact:true});
    // Old deleted generations stay in history. Select only the enabled owned copy.
    const localBoxes=await deletion.getByRole("checkbox",{name:/^Delete local copy /}).all();
    let localCount=0;for(const box of localBoxes)if(await box.isEnabled()){await box.check();localCount++}
    if(localCount!==1)throw Error("expected one available restored local generation");
    await deletion.getByRole("checkbox",{name:/^Delete remote copy /}).check();
    const bothResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletion-previews"));
    await deletion.getByRole("button",{name:"Review selected copy deletion",exact:true}).click();
    const bothHTTP=await bothResponse;if(bothHTTP.status()!==200)throw Error("final manual preview failed");
    const both=await bothHTTP.json();if(!both.final_copy||both.items.length!==2||new Set(both.items.map(c=>c.location)).size!==2)throw Error("final exact both-copy selection differs");
    const finalButton=deletion.getByRole("button",{name:"Permanently delete reviewed copies",exact:true});await expect(finalButton).toBeDisabled();
    await deletion.getByRole("checkbox",{name:"I understand this deletes the final recoverable copy of this backup.",exact:true}).check();
    const finalLostResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletions"));await finalButton.click();
    const finalLost=await finalLostResponse;if(finalLost.status()!==503||finalLost.request().postDataJSON().confirm_final_copy!==true)throw Error("final-copy consent or committed response loss missing");
    const finalSaved=finalLost.request().postDataJSON();
    const finalRetryResponse=page.waitForResponse(r=>r.request().method()==="POST"&&r.url().endsWith(base+"/deletions"));await deletion.getByRole("button",{name:"Retry identical deletion request",exact:true}).click();
    const finalRetry=await finalRetryResponse;if(finalRetry.status()!==202||JSON.stringify(finalRetry.request().postDataJSON())!==JSON.stringify(finalSaved))throw Error("final manual retry changed confirmation/selection");
    deletionFinalExecution=await finalRetry.json();safe.manual_deletion_remote_and_final_confirmation=true;
    await report("deletion_final",deletionFinalExecution.id);
    deletionState=await state("deletion_final_history");
    p=await panel(deletionState);await selectBackup(p,deletionState.backup_id);deletion=p.getByRole("region",{name:"Delete selected backup copies",exact:true});
    await expect(deletion).toContainText(`Deletion ${deletionExecution.id}: succeeded`);await expect(deletion).toContainText(`Deletion ${deletionFinalExecution.id}: succeeded`);
    await expect(p.locator('dl[aria-label="Selected backup metadata"]')).toContainText("remote: deleted");
    safe.manual_deletion_final_history=true;await report("deletion_final_history",deletionFinalExecution.id);

  }
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
    if(retentionMode){
      const retainedPanel=await panel(retentionState,false,viewer);
      const retained=retainedPanel.getByRole("region",{name:"Backup retention",exact:true});
      await expect(retained).toContainText(`Batch ${retentionExecution.id}: succeeded`);
      for(const name of ["Save retention policy","Review deletion preview","Delete selected copies"]){await expect(retained.getByRole("button",{name,exact:true})).toHaveCount(0)}
      await selectBackup(retainedPanel,retentionBackupIDs[1]);
      await expect(retainedPanel.locator('dl[aria-label="Selected backup metadata"]')).toContainText("remote: available");
      const denied=await viewerContext.request.post(`/api/v1/resources/${retentionState.resource_id}/storage-backup-retention/previews`,{headers:{"X-NeroCD-CSRF":"1",Origin:origin},data:{request_id:"f".repeat(32),expected_policy_revision:retentionPolicy.revision}});
      if(denied.status()!==403)throw Error("viewer retention mutation was not denied");
      safe.retention_viewer_read_only_and_denied=true;
    }
    if(deletionMode){
      const manualPanel=await panel(deletionState,false,viewer);await selectBackup(manualPanel,deletionState.backup_id);
      const manual=manualPanel.getByRole("region",{name:"Delete selected backup copies",exact:true});
      await expect(manual).toContainText(`Deletion ${deletionExecution.id}: succeeded`);
      await expect(manual.getByRole("button",{name:"Review selected copy deletion",exact:true})).toHaveCount(0);
      const denied=await viewerContext.request.post(`/api/v1/resources/${deletionState.resource_id}/storage-backups/${deletionState.backup_id}/deletion-previews`,{headers:{"X-NeroCD-CSRF":"1",Origin:origin},data:{request_id:"manual_viewer_denied",expected_resource_generation:2,copy_ids:["foreign"]}});
      if(denied.status()!==403)throw Error("viewer manual deletion mutation not denied");
      safe.manual_deletion_viewer_history_and_denied=true;
    }
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
