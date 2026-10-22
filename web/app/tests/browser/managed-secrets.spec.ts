import { expect, test, type Page, type Locator } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

type Secret = { id: string; current_version: number; name: string; status: string };
async function signInAdmin(page: Page) {
  await page.goto("/sign-in");
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID)) throw new Error("browser runtime unavailable");
  const [email, password] = readFileSync(join("/tmp", `nerocd-browser-${runID}`, "credentials"), "utf8").trim().split("\n");
  if (!email || !password) throw new Error("browser credentials unavailable");
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeVisible();
}
const headers = (page: Page) => ({ Origin: new URL(page.url()).origin, "X-NeroCD-CSRF": "1" });
async function post<T>(page: Page, path: string, data: unknown): Promise<T> {
  const response = await page.request.post(path, { data, headers: headers(page) });
  expect(response.ok()).toBe(true);
  return await response.json() as T;
}
async function enterValue(scope: Locator, label: string, value: string) {
  const entry = scope.getByRole("group", { name: `${label} entry`, exact: true });
  await entry.getByRole("button", { name: "Show value to edit", exact: true }).click();
  await entry.getByLabel(label, { exact: true }).fill(value);
  await entry.getByRole("button", { name: "Hide value", exact: true }).click();
}
async function projectFor(page: Page, role: "owner" | "maintainer" | "viewer", suffix: string) {
  const project = await post<{ id: string }>(page, "/api/v1/projects", { name: `Secrets ${role} ${suffix}` });
  await post(page, "/api/v1/project-members", { project_id: project.id, email: "oidc-browser@example.invalid", role });
  return project;
}

test("managed secret multiline entry round trips LF CRLF BOM and large UTF-8 files exactly", async ({ page, context }, testInfo) => {
  test.setTimeout(120_000);
  await signInAdmin(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: new URL(page.url()).origin });
  const project = await post<{ id: string }>(page, "/api/v1/projects", { name: `Exact secret values ${Date.now()}` });
  await page.goto("/projects");
  const section = page.getByRole("region", { name: "Project managed secrets", exact: true });
  await section.getByLabel("Secrets project", { exact: true }).selectOption(project.id);
  async function clipboardPaste(label: string, value: string) {
    await page.evaluate((text) => navigator.clipboard.writeText(text), value);
    const field = section.getByLabel(label, { exact: true });
    await field.focus(); await field.press("ControlOrMeta+V");
    await expect(field).toHaveValue("••••••••");
  }
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    for (const kind of ["LF", "CRLF", "file"]) {
      const name = `EXACT_${kind}_${width}`;
      let original = "-----BEGIN TEST-----\nline one  \n-----END TEST-----\n\n  ";
      const form = section.getByRole("form", { name: "Create managed secret", exact: true });
      await form.getByLabel("Secret name", { exact: true }).fill(name);
      if (kind === "LF") {
        const entry = form.getByRole("group", { name: "Secret value entry", exact: true });
        await entry.getByRole("button", { name: "Show value to edit", exact: true }).click();
        const field = entry.getByLabel("Secret value", { exact: true }); await field.focus();
        for (const [index, line] of ["-----BEGIN TEST-----", "line one  ", "-----END TEST-----", "", "  "].entries()) {
          if (index > 0) await field.press("Enter");
          if (line) await field.pressSequentially(line);
        }
        await entry.getByRole("button", { name: "Hide value", exact: true }).click();
      } else if (kind === "CRLF") {
        original = original.replaceAll("\n", "\r\n");
        await clipboardPaste("Secret value", original);
        // Native edits must preserve CRLF outside the edited characters.
        const entry = form.getByRole("group", { name: "Secret value entry", exact: true });
        await entry.getByRole("button", { name: "Show value to edit", exact: true }).click();
        const field = entry.getByLabel("Secret value", { exact: true }); await field.focus();
        await field.press("ControlOrMeta+End"); await field.pressSequentially("x"); await field.press("Backspace");
        await field.press("ControlOrMeta+A"); await field.press("ControlOrMeta+C");
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(original);
        await entry.getByRole("button", { name: "Hide value", exact: true }).click();
      } else {
        original = "\ufeff-----BEGIN TEST-----\r\n" + "é-key-line\r\nnext-line\n".repeat(2000) + "-----END TEST-----\r\n\r\n  ";
        await form.getByLabel("Load secret value from UTF-8 file", { exact: true }).setInputFiles({ name: "test-only.pem", mimeType: "text/plain", buffer: Buffer.from(original, "utf8") });
        await expect(form.getByLabel("Secret value", { exact: true })).toHaveValue("••••••••");
      }
      const created = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/managed-secrets");
      await form.getByRole("button", { name: "Create secret", exact: true }).click();
      const createdResponse = await created; expect(createdResponse.status()).toBe(201);
      const secret = await createdResponse.json() as Secret;
      const detail = section.getByRole("region", { name: `Secret ${name}`, exact: true });
      await expect(detail.getByText("Version 1", { exact: true })).toBeVisible();
      await expect(form.getByLabel("Secret value", { exact: true })).toHaveValue("");
      await expect(form.getByRole("button", { name: "Show value to edit", exact: true })).toBeVisible();
      const rotated = `\ufeff${original}\r\n  `;
      const rotate = detail.getByRole("form", { name: "Rotate secret", exact: true });
      if (kind === "file") await rotate.getByLabel("Load new secret value from UTF-8 file", { exact: true }).setInputFiles({ name: "rotated-test-only.pem", mimeType: "text/plain", buffer: Buffer.from(rotated, "utf8") });
      else await clipboardPaste("New secret value", rotated);
      const saved = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/api/v1/managed-secrets/${secret.id}/versions`);
      await rotate.getByRole("button", { name: "Rotate secret", exact: true }).click(); expect((await saved).status()).toBe(201);
      await expect(detail.getByText("Version 2", { exact: true })).toBeVisible();
      await expect(rotate.getByLabel("New secret value", { exact: true })).toHaveValue("");
      for (const [version, expected] of [[1, original], [2, rotated]] as const) {
        await detail.getByRole("button", { name: `Select version ${version} to reveal`, exact: true }).click();
        await detail.getByRole("button", { name: `Reveal value for version ${version}`, exact: true }).click();
        await expect.poll(() => detail.getByLabel("Revealed secret value", { exact: true }).textContent()).toBe(expected);
        await detail.getByRole("button", { name: "Copy revealed value", exact: true }).click();
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
        await detail.getByRole("button", { name: "Close reveal", exact: true }).click();
        await expect(detail.getByLabel("Revealed secret value", { exact: true })).toHaveCount(0);
      }
      await expect(section.getByRole("button", { name: "Hide value", exact: true })).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      if (kind === "file") await page.screenshot({ path: testInfo.outputPath(`managed-secret-multiline-masked-${width}.png`), fullPage: true, animations: "disabled" });
      await detail.getByRole("button", { name: "Close secret", exact: true }).click();
    }
  }
});

test("managed secrets owner creates rotates reveals historical versions and revokes across responsive widths", async ({ page, browser }, testInfo) => {
  test.setTimeout(60_000);
  await signInAdmin(page);
  const project = await projectFor(page, "owner", `${Date.now()}`);
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in");
    await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible();
    await operator.goto("/projects");
    const section = operator.getByRole("region", { name: "Project managed secrets", exact: true });
    await section.getByLabel("Secrets project", { exact: true }).selectOption(project.id);
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      const name = `DATABASE_${width}_${"LONG_NAME_".repeat(6)}`;
      const original = ` test-only-original-${width} `;
      const rotated = `test-only-rotated-${width}`;
      const form = section.getByRole("form", { name: "Create managed secret", exact: true });
      await form.getByLabel("Secret name", { exact: true }).fill(name);
      await expect(form.getByLabel("Secret value", { exact: true })).toHaveAttribute("readonly", "");
      await enterValue(form, "Secret value", original);
      const created = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/managed-secrets");
      await form.getByRole("button", { name: "Create secret", exact: true }).click();
      const response = await created;
      expect(response.status()).toBe(201);
      expect(response.headers()["cache-control"]).toContain("no-store");
      const secret = await response.json() as Secret;
      expect(secret).not.toHaveProperty("value");
      await expect(form.getByLabel("Secret value", { exact: true })).toHaveValue("");
      const detail = section.getByRole("region", { name: `Secret ${name}`, exact: true });
      await expect(detail.getByText("Version 1", { exact: true })).toBeVisible();
      await expect(section.getByLabel("Revealed secret value", { exact: true })).toHaveCount(0);
      const external = await post<Secret>(page, `/api/v1/managed-secrets/${secret.id}/versions`, { expected_version: 1, value: `test-only-concurrent-${width}` });
      expect(external.current_version).toBe(2);
      const rotate = detail.getByRole("form", { name: "Rotate secret", exact: true });
      await enterValue(rotate, "New secret value", rotated);
      const stale = operator.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/managed-secrets/${secret.id}/versions`);
      await rotate.getByRole("button", { name: "Rotate secret", exact: true }).click();
      expect((await stale).status()).toBe(409);
      await expect(rotate.getByLabel("New secret value", { exact: true })).toHaveValue("");
      await expect(rotate.getByRole("button", { name: "Rotate secret", exact: true })).toBeDisabled();
      await detail.getByRole("button", { name: "Use current version 2", exact: true }).click();
      await enterValue(rotate, "New secret value", rotated);
      const saved = operator.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/managed-secrets/${secret.id}/versions`);
      await rotate.getByRole("button", { name: "Rotate secret", exact: true }).click();
      expect((await saved).status()).toBe(201);
      await expect(detail.getByText("Version 3", { exact: true })).toBeVisible();
      for (const [version, expected] of [[1, original], [3, rotated]] as const) {
        await detail.getByRole("button", { name: `Select version ${version} to reveal`, exact: true }).click();
        await expect(detail.getByLabel("Revealed secret value", { exact: true })).toHaveCount(0);
        await detail.getByRole("button", { name: `Reveal value for version ${version}`, exact: true }).click();
        await expect.poll(() => detail.getByLabel("Revealed secret value", { exact: true }).textContent()).toBe(expected);
        expect(await operator.evaluate((value) => !JSON.stringify(localStorage).includes(value) && !JSON.stringify(sessionStorage).includes(value), expected)).toBe(true);
        await detail.getByRole("button", { name: "Close reveal", exact: true }).click();
        await expect(detail.getByLabel("Revealed secret value", { exact: true })).toHaveCount(0);
      }
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      expect(await section.getByRole("button", { name: `Manage ${name}`, exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      expect(await detail.getByRole("heading", { name, exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      expect(await detail.getByRole("button", { name: `Revoke ${name}`, exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
      await operator.screenshot({ path: testInfo.outputPath(`managed-secret-owner-${width}.png`), fullPage: true, animations: "disabled" });
      await detail.getByRole("checkbox", { name: `I understand that revoking ${name} is irreversible.`, exact: true }).check();
      const revoked = operator.waitForResponse((result) => result.request().method() === "POST" && new URL(result.url()).pathname === `/api/v1/managed-secrets/${secret.id}/revoke`);
      await detail.getByRole("button", { name: `Revoke ${name}`, exact: true }).click();
      expect((await revoked).status()).toBe(200);
      await expect(detail.getByText("Revoked · Current version 3 · Values hidden", { exact: true })).toBeVisible();
      await expect(detail.getByRole("button", { name: /Select version.*to reveal/ })).toHaveCount(0);
      await expect(detail.getByText("Version 1", { exact: true })).toBeVisible();
      const denied = await operator.request.post(`/api/v1/managed-secrets/${secret.id}/reveal`, { data: { version: 1 }, headers: headers(operator) });
      expect(denied.status()).toBe(409);
      await operator.reload();
      await section.getByLabel("Secrets project", { exact: true }).selectOption(project.id);
      await section.getByRole("button", { name: `Manage ${name}`, exact: true }).click();
      await expect(section.getByText("Revoked · Current version 3 · Values hidden", { exact: true })).toBeVisible();
      await section.getByRole("button", { name: "Close secret", exact: true }).click();
    }
  } finally { await context.close(); }
});

test("managed secrets maintainer writes and viewer metadata enforce direct API role boundaries", async ({ page, browser }, testInfo) => {
  test.setTimeout(60_000);
  await signInAdmin(page);
  const maintained = await projectFor(page, "maintainer", `${Date.now()}`);
  const viewed = await projectFor(page, "viewer", `${Date.now()}`);
  const secret = await post<Secret>(page, "/api/v1/managed-secrets", { project_id: viewed.id, name: "VIEWED_SECRET", value: "viewer-test-only-value" });
  const context = await browser.newContext();
  try {
    const operator = await context.newPage();
    await operator.goto("/sign-in"); await operator.getByRole("link", { name: "Continue with SSO" }).click();
    await expect(operator.getByRole("button", { name: "Account", exact: true })).toBeVisible(); await operator.goto("/projects");
    const section = operator.getByRole("region", { name: "Project managed secrets", exact: true });
    let maintainedSecretID = "";
    for (const width of [320, 390, 430, 1440]) {
      await operator.setViewportSize({ width, height: 1000 });
      await section.getByLabel("Secrets project", { exact: true }).selectOption(viewed.id);
      await section.getByRole("button", { name: "Manage VIEWED_SECRET", exact: true }).click();
      await expect(section.getByText("Version 1", { exact: true })).toBeVisible();
      await expect(section.getByRole("button", { name: /^(Create secret|Rotate secret|Revoke VIEWED_SECRET|Select version.*to reveal)$/ })).toHaveCount(0);
      await expect(section.getByText("viewer-test-only-value", { exact: true })).toHaveCount(0);
      expect(await operator.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await operator.screenshot({ path: testInfo.outputPath(`managed-secret-viewer-${width}.png`), fullPage: true, animations: "disabled" });
      await section.getByLabel("Secrets project", { exact: true }).selectOption(maintained.id);
      await section.getByLabel("Secret name", { exact: true }).fill(`MAINTAINED_${width}`);
      await enterValue(section, "Secret value", `maintainer-test-only-${width}`);
      const created = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/managed-secrets");
      await section.getByRole("button", { name: "Create secret", exact: true }).click();
      const createdResponse = await created; expect(createdResponse.status()).toBe(201);
      maintainedSecretID = (await createdResponse.json() as Secret).id;
      await expect(section.getByRole("button", { name: "Rotate secret", exact: true })).toBeVisible();
      await expect(section.getByRole("button", { name: /Select version.*to reveal/ })).toHaveCount(0);
      await enterValue(section, "New secret value", `maintainer-rotation-test-${width}`);
      const rotated = operator.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/api/v1/managed-secrets/${maintainedSecretID}/versions`);
      await section.getByRole("button", { name: "Rotate secret", exact: true }).click(); expect((await rotated).status()).toBe(201);
      await expect(section.getByText("Version 2", { exact: true })).toBeVisible();
    }
    for (const [action, data] of [["reveal", { version: 1 }], ["revoke", { expected_version: 2 }]] as const) {
      const response = await operator.request.post(`/api/v1/managed-secrets/${maintainedSecretID}/${action}`, { data, headers: headers(operator) });
      expect(response.status()).toBe(403);
    }
    for (const [path, body] of [["", { project_id: viewed.id, name: "DENIED", value: "denied-test-only" }], [`/${secret.id}/versions`, { expected_version: 1, value: "denied-test-only" }], [`/${secret.id}/reveal`, { version: 1 }], [`/${secret.id}/revoke`, { expected_version: 1 }]] as const) {
      const response = await operator.request.post(`/api/v1/managed-secrets${path}`, { data: body, headers: headers(operator) });
      expect(response.status()).toBe(403);
    }
    const metadata = await operator.request.get(`/api/v1/managed-secrets?project_id=${viewed.id}&limit=20&offset=0`);
    expect(metadata.status()).toBe(200); expect(metadata.headers()["cache-control"]).toContain("no-store");
    const text = await metadata.text(); expect(text).not.toContain("viewer-test-only-value"); expect(text).not.toContain('"value"');
  } finally { await context.close(); }
});
