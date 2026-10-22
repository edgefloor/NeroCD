import { expect, test, type Page, type Locator } from "@playwright/test";
import { readFileSync, realpathSync } from "node:fs";
import { join } from "node:path";

test.use({ trace: "off", screenshot: "off" });
const headers = (page: Page) => ({ Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" });
async function signIn(page: Page) {
 const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
 if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser fixture unavailable");
 const [email, password] = readFileSync(join(realpathSync(join("/tmp", `nerocd-browser-${runID}`)), "credentials"), "utf8").trim().split("\n");
 await page.goto("/sign-in");
 await page.getByLabel("Email", { exact: true }).fill(email!); await page.getByLabel("Password", { exact: true }).fill(password!);
 await page.getByRole("button", { name: "Sign in", exact: true }).click();
 await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
async function hosting(page: Page, project: string, environment?: string) {
 await page.goto("/projects");
 const region = page.getByRole("region", { name: "Project hosting", exact: true });
 await region.getByLabel("Hosting project", { exact: true }).selectOption(project);
 if (environment) await region.getByRole("button", { name: environment, exact: true }).click();
 return region;
}
async function saveEditor(page: Page, form: Locator, status = 200) {
 const pending = page.waitForResponse((response) => response.request().method() === "PATCH" && /\/api\/v1\/resources\/[^/]+$/.test(new URL(response.url()).pathname));
 await form.getByRole("button", { name: "Save configuration", exact: true }).click();
 const response = await pending; expect(response.status()).toBe(status);
 return response;
}

test("public Git and image configuration persist with CAS and roles at mobile and desktop widths", async ({ page, browser }, info) => {
 test.setTimeout(240_000);
 await signIn(page); await page.setViewportSize({ width: 1440, height: 1000 });
 await page.goto("/projects");
 await page.getByLabel("Project Name", { exact: true }).fill(`Git resource browser ${Date.now()}`);
 const projectResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/projects");
 await page.getByRole("button", { name: "Create project", exact: true }).click();
 const project = await (await projectResponse).json() as { id: string };
 // Accounts/memberships are role fixtures. Every tested application and edit
 // below is created through the real UI, with no SQL or resource API setup.
 const membership = { project_id: project.id, email: "oidc-browser@example.invalid", role: "viewer" };
 expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: membership })).ok()).toBe(true);
 const viewerContext = await browser.newContext();
 const peer = await page.context().newPage();
 try {
  const viewer = await viewerContext.newPage();
  await viewer.goto("/sign-in"); await viewer.getByRole("link", { name: "Continue with SSO" }).click();
  await expect(viewer.getByRole("button", { name: "Account", exact: true })).toBeVisible();
  for (const width of [320, 390, 430, 1440]) {
   await page.setViewportSize({ width, height: 1000 }); await peer.setViewportSize({ width, height: 1000 }); await viewer.setViewportSize({ width, height: 1000 });
   const environment = `Git config ${width}`;
   const region = await hosting(page, project.id);
   await region.getByLabel("Environment name", { exact: true }).fill(environment);
   await region.getByRole("button", { name: "Create shared environment", exact: true }).click();
   const create = region.getByRole("form", { name: "Create application", exact: true });
   await expect(create).toBeVisible();
   const imageName = `Image ${width}`, gitName = `Git ${width}`;
   await create.getByLabel("Application name", { exact: true }).fill(imageName);
   await create.getByLabel("Image reference", { exact: true }).fill("nginx:1.27");
   let created = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/resources");
   await create.getByRole("button", { name: "Save application", exact: true }).click(); expect((await created).status()).toBe(201);
   const imageArticle = region.getByRole("article", { name: imageName, exact: true });
   await imageArticle.getByRole("button", { name: "Edit configuration", exact: true }).click();
   const imageEditor = imageArticle.getByRole("form", { name: `Edit ${imageName}`, exact: true });
   await imageEditor.getByLabel("Image reference", { exact: true }).fill("nginx:1.28");
   expect((await (await saveEditor(page, imageEditor)).json() as { source: { image: { reference: string } } }).source.image.reference).toBe("nginx:1.28");
   await create.getByLabel("Application source", { exact: true }).selectOption("git");
   await create.getByLabel("Application name", { exact: true }).fill(gitName);
   await create.getByLabel("Git repository URL", { exact: true }).fill("https://github.com/acme/public-application.git");
   await create.getByLabel("Build strategy", { exact: true }).selectOption(width === 430 ? "static" : width === 390 ? "railpack" : "dockerfile");
   if (width === 430) { await create.getByLabel("Publish directory", { exact: true }).fill("public"); await create.getByLabel("SPA fallback", { exact: true }).check(); }
   created = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/resources");
   await create.getByRole("button", { name: "Save application", exact: true }).click();
   const gitResponse = await created; expect(gitResponse.status()).toBe(201);
   let git = await gitResponse.json() as { id: string; generation: number; source: { kind: string; git: { source_id: string; generation: number; requested_ref: string } } };
   expect(git.source.kind).toBe("git"); expect(git.source.git.generation).toBe(1);
   const originalSource = git.source.git.source_id;
   expect(JSON.stringify(git)).not.toMatch(/allowed_hosts|allowed_cidrs|fingerprints|repository_policy|"policy"|"image"/);
   const article = region.getByRole("article", { name: gitName, exact: true });
   await expect(article.getByText(/Deployment of a build artifact is separate and unavailable/)).toBeVisible();
   await expect(article.getByRole("button", { name: "Request image deployment", exact: true })).toHaveCount(0);
   await expect(article.getByRole("region", { name: `Desired destination for ${gitName}`, exact: true })).toBeVisible();
   await expect(article.getByRole("region", { name: `Routes for ${gitName}`, exact: true })).toBeVisible();
   await article.getByRole("button", { name: "Edit configuration", exact: true }).click();
   let editor = article.getByRole("form", { name: `Edit ${gitName}`, exact: true });
   await editor.getByLabel("Application name", { exact: true }).fill(`${gitName} saved`);
   git = await (await saveEditor(page, editor)).json();
   expect(git.generation).toBe(2); expect(git.source.git.generation).toBe(1);
   const savedName = `${gitName} saved`;
   const savedArticle = region.getByRole("article", { name: savedName, exact: true });
   await savedArticle.getByRole("button", { name: "Edit configuration", exact: true }).click();
   editor = savedArticle.getByRole("form", { name: `Edit ${savedName}`, exact: true });
   await editor.getByLabel("Git branch, tag, or commit", { exact: true }).fill("release-draft");
   await editor.getByLabel("Build strategy", { exact: true }).selectOption("dockerfile");
   await editor.getByLabel("Context directory", { exact: true }).fill("old-context");
   await editor.getByLabel("Dockerfile path", { exact: true }).fill("docker/Oldfile");
   await editor.getByLabel("Build target", { exact: true }).fill("old-target");
   const peerRegion = await hosting(peer, project.id, environment);
   const peerArticle = peerRegion.getByRole("article", { name: savedName, exact: true });
   await peerArticle.getByRole("button", { name: "Edit configuration", exact: true }).click();
   const peerEditor = peerArticle.getByRole("form", { name: `Edit ${savedName}`, exact: true });
   await peerEditor.getByLabel("Git branch, tag, or commit", { exact: true }).fill("release-peer");
   await peerEditor.getByLabel("Build strategy", { exact: true }).selectOption("static");
   await peerEditor.getByLabel("Context directory", { exact: true }).fill("new-context");
   await peerEditor.getByLabel("Publish directory", { exact: true }).fill("new-public");
   await peerEditor.getByLabel("SPA fallback", { exact: true }).check();
   expect((await (await saveEditor(peer, peerEditor)).json() as typeof git).source.git.generation).toBe(2);
   await saveEditor(page, editor, 409);
   await expect(editor.getByLabel("Git branch, tag, or commit", { exact: true })).toHaveValue("release-draft");
   await expect(editor.getByRole("button", { name: "Save configuration", exact: true })).toBeDisabled();
   const review = editor.getByLabel("Current Git configuration", { exact: true });
   for (const [label, value] of [["Current repository URL", "https://github.com/acme/public-application.git"], ["Current requested ref", "release-peer"], ["Current credential kind", "public"], ["Current source ID", originalSource], ["Current source generation", "2"], ["Current build strategy", "static"], ["Current context directory", "new-context"], ["Current Dockerfile path", "Not applicable"], ["Current build target", "Not applicable"], ["Current publish directory", "new-public"], ["Current SPA fallback", "Enabled"]]) {
    await expect(review.getByText(label!, { exact: true }).locator("xpath=following-sibling::dd[1]")).toHaveText(value!);
   }
   await expect(editor.getByLabel("Build strategy", { exact: true })).toHaveValue("dockerfile");
   await expect(editor.getByLabel("Context directory", { exact: true })).toHaveValue("old-context");
   await expect(editor.getByLabel("Dockerfile path", { exact: true })).toHaveValue("docker/Oldfile");
   await expect(editor.getByLabel("Build target", { exact: true })).toHaveValue("old-target");
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   await page.screenshot({ path: info.outputPath(`git-conflict-current-settings-${width}.png`), fullPage: true, animations: "disabled" });
   await review.screenshot({ path: info.outputPath(`git-conflict-settings-panel-${width}.png`), animations: "disabled" });
   let writesWhileKeeping = 0;
   const countWrite = (request: import("@playwright/test").Request) => { if (request.method() === "PATCH" && new URL(request.url()).pathname === `/api/v1/resources/${git.id}`) writesWhileKeeping++; };
   page.on("request", countWrite);
   await editor.getByRole("button", { name: "Keep my draft with generation 3", exact: true }).click();
   await expect(review).toHaveCount(0);
   await expect(editor.getByLabel("Build strategy", { exact: true })).toHaveValue("dockerfile");
   await expect(editor.getByLabel("Context directory", { exact: true })).toHaveValue("old-context");
   expect(writesWhileKeeping).toBe(0);
   const savedDraft = await saveEditor(page, editor);
   expect(writesWhileKeeping).toBe(1); page.off("request", countWrite);
   expect(savedDraft.request().postDataJSON().source.git.settings).toEqual({ strategy: "dockerfile", context_dir: "old-context", dockerfile_path: "docker/Oldfile", build_target: "old-target" });
   git = await savedDraft.json();
   expect(git.generation).toBe(4); expect(git.source.git.generation).toBe(3); expect(git.source.git.source_id).toBe(originalSource);
   const reloaded = await hosting(page, project.id, environment);
   await expect(reloaded.getByRole("article", { name: savedName, exact: true }).getByText("https://github.com/acme/public-application.git · release-draft", { exact: true })).toBeVisible();
   const deniedDeploy = await page.request.post(`/api/v1/resources/${git.id}/deployments`, { headers: headers(page), data: { expected_generation: 4, idempotency_key: `git-deny-${width}`, container_port: 80, readiness: { path: "/", timeout_seconds: 5 } } });
   expect(deniedDeploy.status()).toBe(400);
   const read = await hosting(viewer, project.id, environment);
   await expect(read.getByRole("article", { name: savedName, exact: true }).getByText(/Git source generation 3 ·/)).toBeVisible();
   await expect(read.getByRole("form", { name: "Create application", exact: true })).toHaveCount(0);
   await expect(read.getByRole("button", { name: "Edit configuration", exact: true })).toHaveCount(0);
   expect((await viewer.request.patch(`/api/v1/resources/${git.id}`, { headers: headers(viewer), data: { expected_generation: 4, name: "forbidden" } })).status()).toBe(403);
   expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: { ...membership, role: "maintainer" } })).ok()).toBe(true);
   const maintain = await hosting(viewer, project.id, environment);
   const maintainArticle = maintain.getByRole("article", { name: savedName, exact: true });
   await maintainArticle.getByRole("button", { name: "Edit configuration", exact: true }).click();
   const maintainEditor = maintainArticle.getByRole("form", { name: `Edit ${savedName}`, exact: true });
   await maintainEditor.getByLabel("Git branch, tag, or commit", { exact: true }).fill("maintainer-ref");
   git = await (await saveEditor(viewer, maintainEditor)).json(); expect(git.generation).toBe(5); expect(git.source.git.generation).toBe(4);
   const maintainImage = maintain.getByRole("article", { name: imageName, exact: true });
   await maintainImage.getByRole("button", { name: "Edit configuration", exact: true }).click();
   const maintainImageEditor = maintainImage.getByRole("form", { name: `Edit ${imageName}`, exact: true });
   await maintainImageEditor.getByLabel("Image reference", { exact: true }).fill("nginx:1.29");
   expect((await (await saveEditor(viewer, maintainImageEditor)).json() as { generation: number }).generation).toBe(3);
   await maintainArticle.getByRole("button", { name: "Edit configuration", exact: true }).click();
   const roleDraft = maintainArticle.getByRole("form", { name: `Edit ${savedName}`, exact: true });
   await roleDraft.getByLabel("Git branch, tag, or commit", { exact: true }).fill("role-loss-draft");
   expect((await page.request.post("/api/v1/project-members", { headers: headers(page), data: membership })).ok()).toBe(true);
   await viewer.getByRole("button", { name: "Account", exact: true }).click();
   await viewer.getByRole("button", { name: "Refresh data", exact: true }).click();
   await expect(roleDraft.getByRole("button", { name: "Save configuration", exact: true })).toBeDisabled();
   await expect(roleDraft.getByLabel("Git branch, tag, or commit", { exact: true })).toHaveValue("role-loss-draft");
   await expect(roleDraft.getByLabel("Git branch, tag, or commit", { exact: true })).toBeDisabled();
   expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   await viewer.screenshot({ path: info.outputPath(`git-resource-maintainer-${width}.png`), fullPage: true, animations: "disabled" });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   await page.screenshot({ path: info.outputPath(`git-resource-admin-${width}.png`), fullPage: true, animations: "disabled" });
  }
 } finally { await peer.close(); await viewerContext.close(); }
});
