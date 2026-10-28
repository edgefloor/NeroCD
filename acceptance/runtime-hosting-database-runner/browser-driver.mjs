import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const mysqlMode = process.env.NEROCD_DATABASE_MYSQL_RUNTIME === "1";
const backupMode = process.env.NEROCD_DATABASE_BACKUP_RUNTIME === "1";
const prefix = backupMode ? "DATABASE_BACKUP_BROWSER_READY " : "DATABASE_CONNECTION_BROWSER_READY ";
const metadataKeys = ["origin", "engine", "project_id", "environment", "application_resource_id", "application_name", "database_resource_id", "foreign_resource_id", "control_path"].sort();
export function validateMetadata(value, origin) {
  const notification = value?.profile === "backup_notifications";
  const keys = notification ? [...metadataKeys, "profile", "tls_spki"].sort() : ["storage", "storage_resume", "storage_complete", "backup", "backup_schedule", "backup_remote", "backup_retention", "backup_retention_recovery", "backup_notifications"].includes(value?.profile) ? [...metadataKeys, "profile"].sort() : metadataKeys;
  if (!/^http:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin) || Number(new URL(origin).port) > 65535 || Number(new URL(origin).port) < 1024 || !value || typeof value !== "object" || Array.isArray(value) || JSON.stringify(Object.keys(value).sort()) !== JSON.stringify(keys) || value.origin !== (notification ? origin.replace("http://", "https://") : origin) || !["postgresql", "redis", "mysql"].includes(value.engine)) throw Error("invalid browser fixture metadata");
  if (notification && (value.engine !== "postgresql" || typeof value.tls_spki !== "string" || !/^[A-Za-z0-9+/]{43}=$/.test(value.tls_spki) || Buffer.from(value.tls_spki, "base64").toString("base64") !== value.tls_spki)) throw Error("invalid notification TLS pin");
  for (const name of ["project_id", "application_resource_id", "database_resource_id", "foreign_resource_id"]) if (typeof value[name] !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value[name])) throw Error("invalid browser fixture identity");
  for (const name of ["environment", "application_name"]) if (typeof value[name] !== "string" || !value[name] || value[name].length > 255 || /[\x00-\x1f\x7f]/.test(value[name])) throw Error("invalid browser fixture label");
  if (!(["backup", "backup_schedule", "backup_remote", "backup_retention", "backup_retention_recovery", "backup_notifications"].includes(value.profile) ? /^\/api\/v1\/fixture\/database-backup\/[a-f0-9]{32}$/ : /^\/api\/v1\/fixture\/database-connection\/[a-f0-9]{32}$/).test(value.control_path)) throw Error("invalid browser fixture controls");
  if (value.engine === "mysql" && !["storage", "backup", "backup_schedule"].includes(value.profile)) throw Error("invalid MySQL browser profile");
  return value;
}

function alive(group) { try { process.kill(-group, 0); return true; } catch (error) { if (error.code === "ESRCH") return false; throw error; } }
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
class BrowserCleanupUnresolved extends Error {}
function join(child) {
  if (child.joinPromise) return child.joinPromise;
  child.joinPromise = joinGroup(child);
  return child.joinPromise;
}
async function joinGroup(child) {
  if (!child.pid) { await child.joined; return; }
  if (alive(child.pid)) process.kill(-child.pid, "SIGTERM");
  await Promise.race([child.joined, delay(1000)]);
  let browserCleanupUnresolved = false;
  if (alive(child.pid)) {
    process.kill(-child.pid, "SIGKILL");
    if (child.browserRunner) {
      // Playwright's browser has its own process group. Killing Bun cannot prove
      // that it exited; inherited browser pipes must not block this diagnostic.
      browserCleanupUnresolved = true;
      child.stdout.destroy(); child.stderr.destroy();
    }
  }
  await Promise.race([child.joined, delay(1000)]);
  for (let i = 0; i < 25 && alive(child.pid); i++) await delay(20);
  if (browserCleanupUnresolved) throw new BrowserCleanupUnresolved();
  if (alive(child.pid)) throw Error("owned browser child group did not join");
}
export async function drive(origin, evidence, command) {
  const children = [], queue = [], engines = new Set();
  const storage = process.env.NEROCD_DATABASE_CONNECTION_STORAGE ?? "0";
  if (!["0", "1"].includes(storage)) throw Error("invalid browser storage profile");
  const schedule = process.env.NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME ?? "0";
  if (!["0", "1"].includes(schedule) || schedule === "1" && !backupMode) throw Error("invalid browser schedule profile");
  const remote = process.env.NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME ?? "0";
  if (!["0", "1"].includes(remote) || remote === "1" && !backupMode) throw Error("invalid browser remote profile");
  const retention = process.env.NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME ?? "0";
  if (!["0", "1"].includes(retention) || retention === "1" && remote !== "1") throw Error("invalid browser retention profile");
  const notifications = process.env.NEROCD_DATABASE_BACKUP_NOTIFICATIONS_RUNTIME ?? "0";
  if (!["0", "1"].includes(notifications) || notifications === "1" && remote !== "1") throw Error("invalid browser notifications profile");
  // Go registers backup_remote_runtime before backup_runtime in lexical file order.
  const expected = mysqlMode ? schedule === "1" ? ["backup_schedule:mysql"] : ["storage:mysql", "backup:mysql"] : backupMode ? [...(notifications === "1" ? ["backup_notifications:postgresql"] : []), ...(remote === "1" ? ["backup_remote:postgresql"] : []), ...(retention === "1" ? ["backup_retention:postgresql", "backup_retention_recovery:postgresql"] : []), "backup:postgresql", ...(schedule === "1" ? ["backup_schedule:postgresql"] : [])] : ["base:postgresql", "base:redis", ...(storage === "1" ? ["storage:postgresql", "storage:redis", "storage_resume:postgresql", "storage_complete:redis"] : [])];
  let active, fixture, failed = false, fixtureDone = false, browserPasses = 0, outputBytes = 0, buffer = "";
  let finish;
  const done = new Promise((resolve) => { finish = resolve; });
  const abort = () => { failed = true; finish(); };
  process.on("SIGINT", abort); process.on("SIGTERM", abort);
  const timer = setTimeout(abort, 40 * 60_000);
  const start = (executable, args, env, browserRunner = false) => {
    const child = spawn(executable, args, { env, detached: true, stdio: ["ignore", "pipe", "pipe"] });
    child.browserRunner = browserRunner;
    child.joined = new Promise((resolve) => { child.once("close", (code) => resolve(code)); child.once("error", () => { failed = true; resolve(1); finish(); }); });
    children.push(child);
    child.stderr.on("data", (chunk) => { outputBytes += chunk.length; if (outputBytes > 1 << 20) abort(); else process.stderr.write(chunk); });
    return child;
  };
  const nextBrowser = () => {
    if (failed || active || !queue.length) return;
    const meta = queue.shift();
    const env = Object.fromEntries(["PATH", "HOME", "TMPDIR", "NODE_PATH", "PLAYWRIGHT_BROWSERS_PATH", "CI"].filter((key) => process.env[key] !== undefined).map((key) => [key, process.env[key]]));
    active = start("bun", ["run", "--cwd", "web/app", "smoke:browser", meta.profile === "backup_notifications" ? "backup-notifications-live.spec.ts" : ["backup_retention", "backup_retention_recovery"].includes(meta.profile) ? "database-backup-retention-live.spec.ts" : meta.profile === "backup_remote" ? "database-remote-backups-live.spec.ts" : meta.profile === "backup_schedule" ? "database-backup-schedules-live.spec.ts" : meta.profile === "backup" ? "database-backups-live.spec.ts" : "database-connection-live.spec.ts", ...(meta.profile === "backup_notifications" ? ["--config", "playwright.notifications.config.ts"] : []), "--project", "chromium", "--no-deps", "--workers", "1", "--output", resolve(evidence, meta.profile ?? "base", meta.engine)], { ...env, PLAYWRIGHT_BASE_URL: meta.origin, ...(meta.profile === "backup_notifications" ? { NEROCD_NOTIFICATION_BROWSER_SPKI: meta.tls_spki } : {}), NEROCD_DATABASE_CONNECTION_BROWSER_META: JSON.stringify(meta) }, true);
    active.stdout.on("data", (chunk) => { outputBytes += chunk.length; if (outputBytes > 1 << 20) abort(); else process.stdout.write(chunk); });
    const current = active;
    current.joined.then(async (code) => {
      if (code !== 0) { abort(); return; }
      try { await join(current); } catch { abort(); return; }
      browserPasses++;
      if (meta.profile === "backup_notifications") process.stdout.write("DATABASE_BACKUP_NOTIFICATIONS_BROWSER_PASS postgresql\n");
      else if (meta.profile === "backup_retention_recovery") process.stdout.write("DATABASE_BACKUP_RETENTION_RECOVERY_BROWSER_PASS postgresql\n");
      else if (meta.profile === "backup_retention") process.stdout.write("DATABASE_BACKUP_RETENTION_BROWSER_PASS postgresql\n");
      else if (meta.profile === "backup_remote") process.stdout.write("DATABASE_BACKUP_REMOTE_BROWSER_PASS postgresql\n");
      else if (meta.profile === "backup_schedule") process.stdout.write(`DATABASE_BACKUP_SCHEDULE_BROWSER_PASS ${meta.engine}\n`);
      else if (meta.profile === "backup") process.stdout.write(`DATABASE_BACKUP_BROWSER_PASS ${meta.engine}\n`);
      else if (["storage_resume", "storage_complete"].includes(meta.profile)) process.stdout.write(`DATABASE_CONNECTION_STORAGE_RECOVERY_BROWSER_PASS ${meta.engine} ${meta.profile === "storage_resume" ? "resume_predecessor" : "complete_replacement"}\n`);
      else process.stdout.write(`DATABASE_CONNECTION_${meta.profile === "storage" ? "STORAGE_" : ""}BROWSER_PASS ${meta.engine}\n`);
      active = undefined; nextBrowser();
      if (fixtureDone && !active && !queue.length) finish();
    });
  };
  try {
    if (!command.length) throw Error("browser fixture command missing");
    await mkdir(evidence, { recursive: true });
    fixture = start(command[0], command.slice(1), process.env);
    fixture.stdout.on("data", (chunk) => {
      outputBytes += chunk.length;
      if (outputBytes > 1 << 20) { abort(); return; }
      buffer += chunk.toString("utf8");
      if (Buffer.byteLength(buffer) > 64 << 10) { abort(); return; }
      let newline;
      while ((newline = buffer.indexOf("\n")) !== -1) {
        const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
        const marker = mysqlMode ? ["DATABASE_CONNECTION_BROWSER_READY ", "DATABASE_BACKUP_BROWSER_READY "].find((value) => line.startsWith(value)) : line.startsWith(prefix) ? prefix : undefined;
        if (!marker) { process.stdout.write(line + "\n"); continue; }
        try {
          const meta = validateMetadata(JSON.parse(line.slice(marker.length)), origin);
          const identity = `${meta.profile ?? "base"}:${meta.engine}`;
          if (engines.has(identity) || identity !== expected[engines.size]) throw Error("duplicate or out-of-order fixture");
          engines.add(identity); queue.push(meta); nextBrowser();
        } catch { abort(); }
      }
    });
    fixture.joined.then((code) => { fixtureDone = true; if (code !== 0 || buffer.trim() || engines.size !== expected.length) abort(); else if (!active && !queue.length) finish(); });
    await done;
    if (failed || browserPasses !== expected.length || !fixtureDone) throw Error("live browser fixture failed");
    return 0;
  } finally {
    clearTimeout(timer);
    let cleanupFailed = false, browserCleanupUnresolved = false;
    for (const child of children.reverse()) { try { await join(child); } catch (error) { cleanupFailed = true; browserCleanupUnresolved ||= error instanceof BrowserCleanupUnresolved; } }
    process.removeListener("SIGINT", abort); process.removeListener("SIGTERM", abort);
    if (browserCleanupUnresolved) throw new BrowserCleanupUnresolved();
    if (cleanupFailed) throw Error("live browser child cleanup failed");
  }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [origin, evidence, separator, ...command] = process.argv.slice(2);
  try { if (separator !== "--") throw Error("invalid driver arguments"); process.exitCode = await drive(origin, evidence, command); }
  catch (error) {
    if (error instanceof BrowserCleanupUnresolved) process.stderr.write("database connection browser cleanup unresolved: forced runner termination cannot prove detached browser exit\n");
    process.stderr.write("database connection browser driver failed\n"); process.exitCode = 1;
  }
}
