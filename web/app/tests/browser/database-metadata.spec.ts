import { expect, test, type Page } from "@playwright/test";
import { randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync, realpathSync } from "node:fs";
import { join } from "node:path";

test.use({ trace: "off", screenshot: "off" });
async function signIn(page: Page) {
  const runID = test.info().config.metadata.NEROCD_BROWSER_RUN_ID;
  if (typeof runID !== "string" || !/^[A-Za-z0-9_-]{8,}$/.test(runID))
    throw new Error("missing browser fixture");
  const runtime = join("/tmp", `nerocd-browser-${runID}`);
  const database = new URL(readFileSync(join(runtime, "database-url"), "utf8"));
  const actualRuntime = realpathSync(runtime);
  const container = `nerocd-browser-postgres-${new URL(String(test.info().project.use.baseURL)).port}-${runID}`;
  const serverProcesses = execFileSync("ps", ["-axo", "pid=,command="], {
    encoding: "utf8",
  })
    .split("\n")
    .filter(
      (line) => line.includes(runID) && line.includes("/nerocd server --addr "),
    )
    .map((line) => Number(line.trim().split(/\s+/)[0]));
  console.log(
    "DATABASE_BROWSER_FIXTURE",
    JSON.stringify({
      runID,
      container,
      containerID: execFileSync(
        "docker",
        ["inspect", "--format", "{{.Id}}", container],
        { encoding: "utf8" },
      ).trim(),
      oidcProcesses: execFileSync("ps", ["-axo", "pid=,command="], {
        encoding: "utf8",
      })
        .split("\n")
        .filter(
          (line) => line.includes(runID) && line.includes("oidc-provider.mjs"),
        )
        .map((line) => Number(line.trim().split(/\s+/)[0])),
      database: database.pathname.slice(1),
      postgresPort: database.port,
      schema: "public",
      runtime,
      actualRuntime,
      serverProcesses,
    }),
  );
  const [email, password] = readFileSync(join(runtime, "credentials"), "utf8")
    .trim()
    .split("\n");
  if (!email || !password) throw new Error("missing browser credentials");
  await page.goto("/sign-in");
  const status = await (
    await page.request.get("/api/v1/bootstrap-status")
  ).json();
  if (status.status !== "complete")
    execFileSync(
      join(runtime, "nerocd"),
      [
        "bootstrap-admin",
        "--email",
        email,
        "--name",
        "Database fixture admin",
        "--password-file",
        join(runtime, "password"),
      ],
      {
        env: {
          ...process.env,
          NEROCD_DATABASE_URL: readFileSync(
            join(runtime, "database-url"),
            "utf8",
          ),
        },
        stdio: "pipe",
      },
    );
  await page.reload();
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Account", exact: true }),
  ).toBeVisible();
}
const headers = (page: Page) => ({
  Origin: new URL(page.url()).origin,
  "X-NeroCD-CSRF": "1",
});
async function post<T>(page: Page, path: string, data: unknown): Promise<T> {
  const result = await page.request.post(path, {
    headers: headers(page),
    data,
  });
  expect(result.ok(), `${path} fixture status ${result.status()}`).toBe(true);
  return result.json();
}
// Intercepted database endpoints qualify metadata UI only. No engine startup,
// credential plaintext, volume persistence or runtime qualification is claimed.
test("database metadata creation and dedicated detail across widths", async ({ page }) => {
  test.setTimeout(120000);
  await signIn(page);
  const project = await post<{ id: string }>(page, "/api/v1/projects", { name: `Database UX ${randomBytes(6).toString("hex")}` });
  const env = await post<{ id: string }>(page, "/api/v1/project-environments", { project_id: project.id, name: "Databases" });
  let persisted: any;
  let receipts: any[] = [];
  let loseNext = false;
  let genericReads = 0;
  const destination = { id: "destination_db_fixture", server_id: "server_db_fixture", name: "Granted database destination", runtime_kind: "docker_compose", created_at: "2026-10-24T00:00:00Z" };
  const receipt = (input: any) => ({
    resource: { id: "resource_db_fixture", project_id: project.id, project_environment_id: env.id, name: input.name, kind: "database", source: {kind: "image", image: { reference: "registry.example.test/metadata-only:1" }}, generation: 1, created_by: "fixture", created_at: "2026-10-24T00:00:00Z", updated_at: "2026-10-24T00:00:00Z" },
    binding: { id: "binding_db_fixture", resource_id: "resource_db_fixture", destination_id: input.destination_id, runtime_namespace: "ncd_database_fixture", created_by: "fixture", created_at: "2026-10-24T00:00:00Z" },
    storage_ref: { storage_id: "storage_db_fixture", generation: 1, hash: "database-storage-hash" },
    engine: input.engine, recipe_version: input.recipe_version, runtime_state: "adapter_unqualified",
    credentials: input.engine === "postgresql"
      ? [{role:"admin",username:"postgres",secret_id:"secret_admin_fixture",version:1},{role:"app",username:"ncd_app",secret_id:"secret_app_fixture",version:1}]
      : [{role:"app",username:"ncd_app",secret_id:"secret_app_fixture",version:1},{role:"probe",username:"ncd_probe",secret_id:"secret_probe_fixture",version:1}],
  });
  await page.route("**/api/v1/destinations?*", route => route.fulfill({ json: { items: [destination], limit: 20, offset: 0, count: 1, total: 1 } }));
  await page.route("**/api/v1/resources?*", route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("project_id") !== project.id) return route.continue();
    return route.fulfill({ json: { items: persisted ? [persisted.resource] : [], limit: 20, offset: 0, count: persisted ? 1 : 0, total: persisted ? 1 : 0 } });
  });
  await page.route("**/api/v1/databases", async route => {
    const input = route.request().postDataJSON();
    receipts.push(input);
    if (loseNext) { loseNext = false; return route.abort("failed"); }
    persisted = receipt(input);
    return route.fulfill({ status: 201, json: persisted });
  });
  await page.route("**/api/v1/resources/resource_db_fixture/**", route => {
    if (new URL(route.request().url()).pathname.endsWith("/database")) return route.fulfill({ json: persisted });
    genericReads++;
    return route.fulfill({ status: 404, json: {} });
  });
  for (const width of [320, 390, 430, 1440]) {
    persisted = undefined;
    receipts = [];
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/projects");
    const hosting = page.getByRole("region", { name: "Project hosting", exact: true });
    await hosting.getByLabel("Hosting project", { exact: true }).selectOption(project.id);
    await hosting.getByRole("button", { name: "Databases", exact: true }).click();
    const form = hosting.getByRole("form", { name: "Create database configuration" });
    await form.getByLabel("Database name", { exact: true }).fill(`Database ${width}`);
    const engine = width === 390 || width === 1440 ? "redis" : "postgresql";
    await form.getByRole("combobox", { name: "Database engine", exact: true }).selectOption(engine);
    await form.getByRole("combobox", { name: "Database destination", exact: true }).selectOption(destination.id);
    loseNext = true;
    await form.getByRole("button", { name: "Save database configuration", exact: true }).focus();
    await page.keyboard.press("Enter");
    const retry = form.getByRole("button", { name: "Retry identical database request", exact: true });
    await expect(retry).toBeEnabled();
    await retry.focus();
    await page.keyboard.press("Enter");
    await expect(hosting.getByRole("region", { name: `Saved database metadata for Database ${width}`, exact: true })).toBeVisible();
    expect(receipts).toHaveLength(2);
    expect(receipts[1]).toEqual(receipts[0]);
    expect(Object.keys(receipts[0]).sort()).toEqual(["request_id","project_id","project_environment_id","name","destination_id","engine","recipe_version"].sort());
    expect(receipts[0].request_id).toMatch(/^[0-9a-f]{32}$/);
    expect(receipts[0].recipe_version).toBe(engine === "postgresql" ? "postgresql-17.11-bookworm.v1" : "redis-8.2.10-bookworm.v1");
    const detail = hosting.getByRole("region", { name: `Database metadata for Database ${width}`, exact: true });
    await expect(detail).toBeVisible();
    await expect(detail).toContainText("Adapter unqualified");
    await expect(detail).toContainText("storage_db_fixture");
    await expect(detail).toContainText("secret_app_fixture");
    await expect(hosting.getByRole("button", { name: "Edit application", exact: true })).toHaveCount(0);
    expect(genericReads).toBe(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
});
