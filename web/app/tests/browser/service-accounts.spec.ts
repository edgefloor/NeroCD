import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function browserRuntimeDir(): string {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser run identifier is unavailable");
  return join("/tmp", `nerocd-browser-${runID}`);
}

async function signIn(page: Page): Promise<void> {
  const runtimeDir = browserRuntimeDir();
  const status = await page.request.get("/api/v1/bootstrap-status");
  const bootstrap = await status.json() as { status?: string };
  const [email, password] = readFileSync(join(runtimeDir, "credentials"), "utf8").trim().split("\n");
  if (bootstrap.status === "required") {
    execFileSync(join(runtimeDir, "nerocd"), ["bootstrap-admin", "--email", email, "--name", "Browser Administrator", "--password-file", join(runtimeDir, "password")], {
      env: { ...process.env, NEROCD_DATABASE_URL: readFileSync(join(runtimeDir, "database-url"), "utf8") },
      stdio: "pipe",
    });
    await page.reload({ waitUntil: "networkidle" });
  }
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}

async function postJSON(page: Page, path: string, token: string | undefined, data: unknown) {
  return page.request.post(path, { data, headers: { Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1", ...(token ? { Authorization: `Bearer ${token}` } : {}) } });
}

test("managed account creates runs with stable identity, rotates, isolates projects, and revokes", async ({ page }, testInfo) => {
  await page.goto("/settings");
  await signIn(page);
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10_000)}`;
  const projectOneResponse = await postJSON(page, "/api/v1/projects", undefined, { name: `Service account one ${suffix}`, description: "managed account browser fixture" });
  const projectTwoResponse = await postJSON(page, "/api/v1/projects", undefined, { name: `Service account two ${suffix}`, description: "isolation browser fixture" });
  expect(projectOneResponse.ok()).toBe(true);
  expect(projectTwoResponse.ok()).toBe(true);
  const projectOne = await projectOneResponse.json() as { id: string };
  const projectTwo = await projectTwoResponse.json() as { id: string };
  await page.reload({ waitUntil: "networkidle" });

  await page.getByLabel("Service account project").selectOption(projectOne.id);
  await page.getByLabel("Service account name").fill(`Browser bot ${suffix}`);
  await page.getByLabel("Service account role").selectOption("maintainer");
  let failCreate = true;
  let releaseCreate!: () => void;
  const pendingCreate = new Promise<void>((resolve) => { releaseCreate = resolve; });
  await page.route("**/api/v1/service-accounts", async (route) => {
    if (route.request().method() !== "POST" || !failCreate) return route.continue();
    await pendingCreate;
    return route.fulfill({ status: 503, json: { error: "Temporary account creation failure" } });
  });
  const createForm = page.getByRole("form", { name: "Create service account", exact: true });
  await page.getByRole("button", { name: "Create account" }).click();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(createForm).toHaveAttribute("aria-busy", "true");
    await expect(page.getByLabel("Service account project")).toBeDisabled();
    for (const label of ["Service account name", "Service account role", "Credential expiry"]) await expect(createForm.getByLabel(label)).toBeDisabled();
    await expect(createForm.getByRole("button", { name: "Creating…", exact: true })).toBeDisabled();
  }
  releaseCreate();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(createForm.getByRole("alert")).toHaveText("Unable to create account: Temporary account creation failure");
    await expect(createForm.getByRole("button", { name: "Create account", exact: true })).toBeEnabled();
    expect(await createForm.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) {
      await createForm.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("account-create-error-320.png"), animations: "disabled" });
    }
  }
  await expect(page.getByLabel("Service account name")).toHaveValue(`Browser bot ${suffix}`);
  await expect(page.getByLabel("Service account role")).toHaveValue("maintainer");
  failCreate = false;
  const createResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/service-accounts" && response.request().method() === "POST");
  await page.getByRole("button", { name: "Create account" }).click();
  expect((await createResponse).status()).toBe(201);
  const secretInput = page.getByLabel("One-time service account credential");
  await expect(secretInput).toBeVisible();
  await expect(createForm.getByRole("alert")).toHaveCount(0);
  const firstToken = await secretInput.inputValue();
  expect(firstToken).toMatch(/^nca_/);
  await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("Clipboard denied"); } } }));
  await page.getByRole("button", { name: "Copy credential", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("alert")).toHaveText("Copy failed. Select and copy the credential manually, or try again.");
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(secretInput).toBeVisible();
    await expect.poll(() => page.getByRole("dialog").evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("service-credential-copy-failure-320.png"), fullPage: true, mask: [secretInput] });
  }
  await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => undefined } }));
  await page.getByRole("button", { name: "Copy credential", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("status")).toHaveText("Credential copied.");
  await page.getByRole("button", { name: "I saved it" }).click();
  await expect(secretInput).toHaveCount(0);
  await expect(page.getByLabel("Service account name")).toHaveValue("");
  await expect(page.getByLabel("Service account role")).toHaveValue("maintainer");
  await expect(page.getByLabel("Credential expiry")).toHaveValue("");

  const createdAccountResponse = await page.request.get(`/api/v1/service-accounts?project_id=${encodeURIComponent(projectOne.id)}&limit=10&offset=0`);
  const createdPage = await createdAccountResponse.json() as { items: Array<{ id: string; name: string }> };
  const account = createdPage.items.find((candidate) => candidate.name === `Browser bot ${suffix}`);
  expect(account).toBeTruthy();
  let failList = true;
  await page.route("**/api/v1/service-accounts?*", (route) => failList
    ? route.fulfill({ status: 503, json: { error: "Temporary service account list failure" } })
    : route.continue());
  const draftName = page.getByLabel(`Name for Browser bot ${suffix}`);
  await draftName.fill("Unsaved account rename");
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Refresh data", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Unable to refresh service accounts. Showing previously loaded accounts.");
  await expect(draftName).toHaveValue("Unsaved account rename");
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(page.getByRole("button", { name: "Retry service accounts", exact: true })).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("service-account-refresh-failure-320.png"), fullPage: true });
  }
  failList = false;
  await page.getByRole("button", { name: "Retry service accounts", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(draftName).toHaveValue("Unsaved account rename");
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
  await draftName.fill(`Browser bot ${suffix}`);
  const runResponse = await postJSON(page, "/api/v1/runs", firstToken, { project_id: projectOne.id, run_spec: { type: "shell", inputs: { command: "true" } }, workflow: { steps: [] }, runner_tags: [] });
  expect(runResponse.status()).toBe(201);
  const run = await runResponse.json() as { requested_by: string };
  expect(run.requested_by).toBe(account!.id);
  const crossProject = await postJSON(page, "/api/v1/runs", firstToken, { project_id: projectTwo.id, run_spec: { type: "shell", inputs: { command: "true" } }, workflow: { steps: [] }, runner_tags: [] });
  expect(crossProject.status()).toBe(403);
  const visibleProjects = await page.request.get("/api/v1/projects", { headers: { Authorization: `Bearer ${firstToken}` } });
  const projectPage = await visibleProjects.json() as { items: Array<{ id: string }> };
  expect(projectPage.items.map((project) => project.id)).toEqual([projectOne.id]);

  const accountRow = page.getByLabel(`Name for Browser bot ${suffix}`).locator("xpath=ancestor::form");
  const rotateResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/service-accounts/rotate");
  await accountRow.getByRole("button", { name: "Rotate" }).click();
  expect((await rotateResponse).status()).toBe(201);
  await expect(page.getByRole("dialog").getByText("Credential copied.", { exact: true })).toHaveCount(0);
  const rotatedToken = await secretInput.inputValue();
  expect(rotatedToken).toMatch(/^nca_/);
  expect(rotatedToken).not.toBe(firstToken);
  expect((await page.request.get("/api/v1/me", { headers: { Authorization: `Bearer ${firstToken}` } })).status()).toBe(401);
  expect((await page.request.get("/api/v1/me", { headers: { Authorization: `Bearer ${rotatedToken}` } })).status()).toBe(200);
  await page.getByRole("button", { name: "I saved it" }).click();

  await accountRow.getByRole("button", { name: "Revoke" }).click();
  const revokeDialog = page.getByRole("dialog");
  await expect(revokeDialog).toContainText("every credential");
  let failRevoke = true;
  await page.route("**/api/v1/service-accounts/revoke", async (route) => {
    if (failRevoke) return route.fulfill({ status: 503, json: { error: "Temporary revoke failure" } });
    await route.continue();
  });
  await revokeDialog.getByRole("button", { name: "Revoke account", exact: true }).click();
  await expect(revokeDialog.getByRole("alert")).toHaveText("Unable to revoke this account. Try again.");
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(revokeDialog.getByRole("button", { name: "Revoke account", exact: true })).toBeVisible();
    expect(await revokeDialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) await page.screenshot({ path: testInfo.outputPath("service-account-revoke-failure-320.png"), fullPage: true });
  }
  failRevoke = false;
  const revokeResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/service-accounts/revoke");
  await revokeDialog.getByRole("button", { name: "Revoke account" }).click();
  expect((await revokeResponse).status()).toBe(200);
  expect((await page.request.get("/api/v1/me", { headers: { Authorization: `Bearer ${rotatedToken}` } })).status()).toBe(401);

  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: width === 1440 ? 900 : 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await expect(page.getByRole("navigation", { name: "accounts pages" })).toBeVisible();
  }
});


test("service account saves retain pending and failed row drafts", async ({ page }, testInfo) => {
  await page.goto("/settings");
  await signIn(page);
  const suffix = String(Date.now());
  const projectResponse = await postJSON(page, "/api/v1/projects", undefined, { name: `Account save ${suffix}`, description: "account edit fixture" });
  expect(projectResponse.ok()).toBe(true);
  const project = await projectResponse.json();
  const accountResponse = await postJSON(page, "/api/v1/service-accounts", undefined, { project_id: project.id, name: `Edit bot ${suffix}`, role: "viewer" });
  expect(accountResponse.ok()).toBe(true);
  await page.reload({ waitUntil: "networkidle" });
  await page.getByLabel("Service account project").selectOption(project.id);
  const row = page.getByRole("form", { name: `Edit service account Edit bot ${suffix}`, exact: true });
  const name = row.getByRole("textbox");
  const role = row.getByRole("combobox");
  await name.fill(`Saved bot ${suffix}`);
  await role.selectOption("maintainer");
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const payloads: unknown[] = [];
  await page.route("**/api/v1/service-accounts", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    payloads.push(route.request().postDataJSON());
    if (payloads.length > 1) return route.continue();
    await pending;
    return route.fulfill({ status: 503, json: { error: "Temporary account update failure" } });
  });
  await row.getByRole("button", { name: "Save", exact: true }).click();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(row).toHaveAttribute("aria-busy", "true");
    await expect(name).toBeDisabled();
    await expect(role).toBeDisabled();
    await expect(row.getByRole("button", { name: "Saving…", exact: true })).toBeDisabled();
    await expect(row.getByRole("button", { name: "Rotate", exact: true })).toBeDisabled();
    await expect(row.getByRole("button", { name: "Revoke", exact: true })).toBeDisabled();
  }
  expect(payloads).toHaveLength(1);
  release();
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(row.getByRole("alert")).toHaveText("Unable to save account: Temporary account update failure");
    await expect(name).toHaveValue(`Saved bot ${suffix}`);
    await expect(role).toHaveValue("maintainer");
    await expect(row.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    expect(await row.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width === 320) {
      await row.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("account-save-error-320.png"), animations: "disabled" });
    }
  }
  await row.getByRole("button", { name: "Save", exact: true }).click();
  const saved = page.getByRole("form", { name: `Edit service account Saved bot ${suffix}`, exact: true });
  await expect(saved).toBeVisible();
  await expect(saved.getByRole("alert")).toHaveCount(0);
  await expect(saved.getByRole("combobox")).toHaveValue("maintainer");
  expect(payloads).toHaveLength(2);
  expect(payloads[1]).toEqual(payloads[0]);
});
