import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const prefix = "DATABASE_CONNECTION_BROWSER_READY ";
const metadataKeys = ["origin", "engine", "project_id", "environment", "application_resource_id", "application_name", "database_resource_id", "foreign_resource_id", "control_path"].sort();
export function validateMetadata(value, origin) {
  if (!/^http:\/\/127\.0\.0\.1:[1-9][0-9]{3,4}$/.test(origin) || Number(new URL(origin).port) > 65535 || Number(new URL(origin).port) < 1024 || !value || typeof value !== "object" || Array.isArray(value) || JSON.stringify(Object.keys(value).sort()) !== JSON.stringify(metadataKeys) || value.origin !== origin || !["postgresql", "redis"].includes(value.engine)) throw Error("invalid browser fixture metadata");
  for (const name of ["project_id", "application_resource_id", "database_resource_id", "foreign_resource_id"]) if (typeof value[name] !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value[name])) throw Error("invalid browser fixture identity");
  for (const name of ["environment", "application_name"]) if (typeof value[name] !== "string" || !value[name] || value[name].length > 255 || /[\x00-\x1f\x7f]/.test(value[name])) throw Error("invalid browser fixture label");
  if (!/^\/api\/v1\/fixture\/database-connection\/[a-f0-9]{32}$/.test(value.control_path)) throw Error("invalid browser fixture controls");
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
    active = start("bun", ["run", "--cwd", "web/app", "smoke:browser", "database-connection-live.spec.ts", "--project", "chromium", "--no-deps", "--workers", "1", "--output", resolve(evidence, meta.engine)], { ...env, PLAYWRIGHT_BASE_URL: origin, NEROCD_DATABASE_CONNECTION_BROWSER_META: JSON.stringify(meta) }, true);
    active.stdout.on("data", (chunk) => { outputBytes += chunk.length; if (outputBytes > 1 << 20) abort(); else process.stdout.write(chunk); });
    const current = active;
    current.joined.then(async (code) => {
      if (code !== 0) { abort(); return; }
      try { await join(current); } catch { abort(); return; }
      browserPasses++; process.stdout.write(`DATABASE_CONNECTION_BROWSER_PASS ${meta.engine}\n`); active = undefined; nextBrowser();
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
        if (!line.startsWith(prefix)) { process.stdout.write(line + "\n"); continue; }
        try {
          const meta = validateMetadata(JSON.parse(line.slice(prefix.length)), origin);
          if (engines.has(meta.engine) || engines.size >= 2 || (engines.size === 0 ? meta.engine !== "postgresql" : meta.engine !== "redis")) throw Error("duplicate or out-of-order fixture");
          engines.add(meta.engine); queue.push(meta); nextBrowser();
        } catch { abort(); }
      }
    });
    fixture.joined.then((code) => { fixtureDone = true; if (code !== 0 || buffer.trim() || engines.size !== 2) abort(); else if (!active && !queue.length) finish(); });
    await done;
    if (failed || browserPasses !== 2 || !fixtureDone) throw Error("live browser fixture failed");
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
