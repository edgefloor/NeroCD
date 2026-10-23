import { expect, test, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

type Metadata = { origin: string; project_id: string; secret_id: string; artifact_id: string; fingerprint: string; ssh_port: string; spki: string };
const metadata = process.env.NEROCD_SERVER_BOOTSTRAP_BROWSER_META;
const meta: Metadata | undefined = metadata ? JSON.parse(metadata) : undefined;
// Chromium trusts only this run's exact ephemeral fixture public key. This is
// same-host test TLS through a Docker-exec TCP relay, not production TLS proof.
test.use({ trace: "off", screenshot: "off", actionTimeout: 15_000, navigationTimeout: 15_000, launchOptions: { args: meta ? [`--ignore-certificate-errors-spki-list=${meta.spki}`] : [] } });

async function signIn(page: Page, viewer = false, navigate = true) {
  if (navigate) await page.goto("/sign-in");
  await page.getByLabel("Email", { exact: true }).fill(viewer ? "viewer@example.local" : "admin@example.local");
  await page.getByLabel("Password", { exact: true }).fill(viewer ? "viewer" : "admin");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}

async function read(page: Page, path: string) {
  // Use the browser's pinned TLS connection and actual session cookies.
  return page.evaluate(async path => { const response = await fetch(path); return { status: response.status, body: await response.text() }; }, path);
}

test("administrator onboards real prepared SSH target and reads independent readiness", async ({ page, browser }, info) => {
  test.skip(!meta, "requires scripts/hosting-server-bootstrap-fixture.sh owned runtime");
  test.setTimeout(180_000);
  const m = meta!;
  // Capture only public, pre-credential navigation evidence. Request bodies,
  // authorization headers, cookies and entered values are never recorded.
  const failed: {path:string;code:string}[]=[];
  const pageErrors:string[]=[];
  const observedAssets:{path:string;status:number;content_type:string}[]=[];
  page.on("requestfailed",request=>{if(failed.length<64)failed.push({path:new URL(request.url()).pathname,code:request.failure()?.errorText.slice(0,128)??"unknown"})});
  page.on("pageerror",error=>{if(pageErrors.length<16)pageErrors.push(error.name.slice(0,64))});
  page.on("response",response=>{const path=new URL(response.url()).pathname;if(path.startsWith("/assets/")&&observedAssets.length<64)observedAssets.push({path,status:response.status(),content_type:response.headers()["content-type"]??""})});
  const navigation=await page.goto("/sign-in");
  expect(navigation).not.toBeNull();
  const initialHTML=await navigation!.text();
  const indexHash=createHash("sha256").update(initialHTML).digest("hex");
  const publicDOM=await page.evaluate(()=>({origin:location.origin,path:location.pathname,title:document.title,headings:Array.from(document.querySelectorAll("h1,h2,[role=status]"),element=>element.textContent?.slice(0,256)).slice(0,16),body:document.body.innerText.slice(0,2048)}));
  const initialAssets=[...new Set(initialHTML.match(/\/assets\/[^" ]+\.(?:js|css)/g))];
  const verifiedAssets:{path:string;status:number;content_type:string;sha256:string;expected:string}[]=[];
  for(const path of initialAssets){const result=await page.evaluate(async path=>{const response=await fetch(path);const digest=await crypto.subtle.digest("SHA-256",await response.arrayBuffer());return{status:response.status,content_type:response.headers.get("content-type")??"",sha256:Array.from(new Uint8Array(digest),byte=>byte.toString(16).padStart(2,"0")).join("")}},path);verifiedAssets.push({path,...result,expected:createHash("sha256").update(readFileSync(`../dist${path}`)).digest("hex")})}
  const bootstrap=await read(page,"/api/v1/bootstrap-status");
  const preflight={navigation_status:navigation!.status(),index_sha256:indexHash,publicDOM,bootstrap,failed,page_error_names:pageErrors,observed_assets:observedAssets,verified_assets:verifiedAssets};
  console.log("SERVER_BOOTSTRAP_PUBLIC_PREFLIGHT "+JSON.stringify(preflight));
  await info.attach("public-pre-credential-navigation",{body:JSON.stringify(preflight,null,2),contentType:"application/json"});
  expect(indexHash).toBe(createHash("sha256").update(readFileSync("../dist/index.html")).digest("hex"));
  for(const asset of verifiedAssets){expect(asset.status,asset.path).toBe(200);expect(asset.sha256,asset.path).toBe(asset.expected)}
  expect(bootstrap.status).toBe(200);expect(JSON.parse(bootstrap.body).status).toBe("complete");
  await signIn(page,false,false);
  await page.goto("/runners");
  const panel = page.getByRole("region", { name: "Add server over SSH", exact: true });
  await expect(panel).toBeVisible();
  await panel.getByLabel("Project for server onboarding", { exact: true }).selectOption(m.project_id);
  const form = panel.getByRole("form", { name: "Prepare SSH server onboarding", exact: true });
  await form.getByLabel("Server name", { exact: true }).fill("browser actual SSH onboarding");
  await form.getByLabel("Literal server IP address", { exact: true }).fill("127.0.0.1");
  await form.getByLabel("SSH port", { exact: true }).fill(m.ssh_port);
  await form.getByLabel("Verified SSH host fingerprint", { exact: true }).fill(m.fingerprint);
  await form.getByLabel("Managed SSH key", { exact: true }).selectOption(m.secret_id);
  await form.getByLabel("SSH key version", { exact: true }).selectOption("1");
  await form.getByLabel("Qualified runner release", { exact: true }).selectOption(m.artifact_id);
  const bodies: string[] = [];
  page.on("request", request => { if (new URL(request.url()).pathname === "/api/v1/server-bootstraps" && request.method() === "POST") bodies.push(request.postData()!); });
  await page.route(m.origin + "/api/v1/server-bootstraps", async route => {
    if (route.request().method() !== "POST") return route.continue();
    // The fixture wrapper executes the production handler and loses only its
    // successful committed response. The browser still owns TLS and cookies.
    await route.continue({ headers: { ...route.request().headers(), "X-NeroCD-Fixture-Lose": "committed" } });
  }, { times: 1 });
  await form.getByRole("button", { name: "Prepare onboarding", exact: true }).click();
  const retry = panel.getByRole("button", { name: "Retry exact onboarding request", exact: true });
  await expect(retry).toBeEnabled();
  const committedListing=await read(page,"/api/v1/server-bootstraps?project_id="+m.project_id);expect(committedListing.status).toBe(200);
  const committed=JSON.parse(committedListing.body).items.filter((item:{name:string})=>item.name==="browser actual SSH onboarding");expect(committed).toHaveLength(1);
  await retry.focus(); await page.keyboard.press("Enter");
  const current = panel.getByRole("region", { name: "Current server onboarding", exact: true });
  await expect(current).toContainText("browser actual SSH onboarding");
  expect(bodies).toHaveLength(2); expect(bodies[1]).toBe(bodies[0]);
  expect(bodies[0]).not.toMatch(/PRIVATE KEY|nce_|controller_url|artifact_path/);
  const listing = await read(page,"/api/v1/server-bootstraps?project_id=" + m.project_id);
  expect(listing.status).toBe(200);
  const operation = JSON.parse(listing.body).items.find((item: { name: string }) => item.name === "browser actual SSH onboarding");
  expect(operation?.id).toBeTruthy();
  expect(operation.id).toBe(committed[0].id);
  const get = async () => { const response = await read(page,"/api/v1/server-bootstraps/" + operation.id); expect(response.status).toBe(200); return JSON.parse(response.body); };
  await current.getByRole("button", { name: "Probe host prerequisites", exact: true }).click();
  await expect.poll(async () => (await get()).probe?.prerequisites?.compose_version, { timeout: 30_000 }).toBe("5.1.1");
  await current.getByRole("button", { name: "Refresh onboarding status", exact: true }).click();
  await current.getByRole("button", { name: "Review planned install", exact: true }).click();
  await expect.poll(async () => Boolean((await get()).review), { timeout: 15_000 }).toBe(true);
  await current.getByRole("button", { name: "Refresh onboarding status", exact: true }).click();
  const review = current.getByRole("region", { name: "Reviewed install plan", exact: true });
  await expect(review).toContainText("create and start its persistent systemd service as root");
  await expect(current).toContainText(`root@127.0.0.1:${m.ssh_port}`);
  await expect(current).toContainText(m.fingerprint);
  await review.getByLabel("Project access after registration", { exact: true }).selectOption("yes");
  const acknowledgement = review.getByRole("checkbox", { name: "I approve installing and starting this runner on the reviewed host.", exact: true });
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await acknowledgement.scrollIntoViewIfNeeded(); await acknowledgement.focus(); await expect(acknowledgement).toBeFocused();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await current.screenshot({ path: info.outputPath(`ssh-onboarding-review-${width}.png`), animations: "disabled" });
  }
  await acknowledgement.check();
  const install = current.getByRole("button", { name: "Install and start runner", exact: true });
  await expect(install).toBeEnabled(); await install.focus(); await page.keyboard.press("Enter");
  await expect.poll(async () => (await get()).state, { timeout: 60_000 }).toBe("completed");
  await current.getByRole("button", { name: "Refresh onboarding status", exact: true }).click();
  const readiness = current.getByRole("region", { name: "Server readiness", exact: true });
  await readiness.scrollIntoViewIfNeeded();
  await expect(readiness.getByText("Server-reported status: ready", { exact: true })).toBeVisible({ timeout: 35_000 });
  await expect(panel).toContainText("Builds, proxy management and runtime logs require separate qualification.");
  const html = (await read(page,"/runners")).body;
  const assets = [...new Set(html.match(/\/assets\/[^" ]+\.(?:js|css)/g))]; expect(assets.length).toBeGreaterThan(0);
  const hashes: Record<string, string> = {};
  for (const asset of assets) { const {status,hash} = await page.evaluate(async path=>{const response=await fetch(path);const digest=await crypto.subtle.digest("SHA-256",await response.arrayBuffer());return{status:response.status,hash:Array.from(new Uint8Array(digest),byte=>byte.toString(16).padStart(2,"0")).join("")};},asset); expect(status).toBe(200); expect(hash).toBe(createHash("sha256").update(readFileSync(`../dist${asset}`)).digest("hex")); hashes[asset] = hash; }
  await info.attach("served-dist-sha256", { body: JSON.stringify(hashes, null, 2), contentType: "application/json" });
  const context = await browser.newContext();
  try { const viewer = await context.newPage(); await signIn(viewer, true); await viewer.goto("/runners"); await expect(viewer.getByRole("region", { name: "Add server over SSH", exact: true })).toHaveCount(0); expect((await read(viewer,"/api/v1/server-bootstraps/" + operation.id)).status).toBe(403); } finally { await context.close(); }
});
