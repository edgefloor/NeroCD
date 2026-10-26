import { expect, test, type Page } from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
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
    "STORAGE_BROWSER_FIXTURE",
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
        "Storage fixture admin",
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
test("owner configures immutable retained storage and recovers exact save across responsive widths", async ({
  page,
}, info) => {
  test.setTimeout(120000);
  await signIn(page);
  const suffix = randomBytes(6).toString("hex");
  const project = await post<{ id: string }>(page, "/api/v1/projects", {
    name: `Storage ${suffix}`,
  });
  const runnerID = `runner_storage_${suffix}`;
  const enrollment = await post<{ token: string }>(
    page,
    "/api/v1/runner-enrollments",
    {
      runner_id: runnerID,
      runner_name: "Storage browser fixture",
      capabilities: ["compose-deploy", "image-deploy"],
    },
  );
  const consumed = await page.request.post(
    "/api/v1/runner-enrollments/consume",
    {
      headers: { Authorization: `Bearer ${enrollment.token}` },
      data: {
        request_id: `enroll_consume_${randomBytes(16).toString("hex")}`,
        credential_hash: createHash("sha256")
          .update(randomBytes(32))
          .digest("hex"),
      },
    },
  );
  expect(consumed.ok()).toBe(true);
  const server = await post<{ destination: { id: string } }>(
    page,
    "/api/v1/servers",
    { name: "Storage fixture server", runner_id: runnerID },
  );
  await post(
    page,
    `/api/v1/destinations/${server.destination.id}/project-grants`,
    { project_id: project.id },
  );
  const deployments: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      /\/(deployments|runs)(?:\/|$)/.test(new URL(request.url()).pathname)
    )
      deployments.push(request.url());
  });
  for (const width of [320, 390, 430, 1440]) {
    const env = await post<{ id: string }>(
      page,
      "/api/v1/project-environments",
      { project_id: project.id, name: `Storage ${width}` },
    );
    const resource = await post<{ id: string }>(page, "/api/v1/resources", {
      project_id: project.id,
      project_environment_id: env.id,
      name: `Uploads ${width}`,
      kind: "application",
      source: {
        kind: "image",
        image: { reference: "registry.example.com/uploads:1" },
      },
    });
    await post(page, `/api/v1/resources/${resource.id}/binding`, {
      destination_id: server.destination.id,
    });
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/projects");
    const hosting = page.getByRole("region", {
      name: "Project hosting",
      exact: true,
    });
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption(project.id);
    await hosting
      .getByRole("button", { name: `Storage ${width}`, exact: true })
      .click();
    const panel = hosting.getByRole("region", {
      name: `Persistent storage for Uploads ${width}`,
      exact: true,
    });
    await expect(
      panel.getByText("No configured persistent storage.", { exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", { name: "Save desired storage", exact: true }),
    ).toBeDisabled();
    await panel
      .getByLabel("Container mount path", { exact: true })
      .fill("/app/uploads");
    await panel.getByRole("checkbox").check();
    const requests: string[] = [];
    let lose = true;
    const path = `/api/v1/resources/${resource.id}/storage`;
    await page.route(`**${path}`, async (route) => {
      if (route.request().method() !== "POST") {
        await route.continue();
        return;
      }
      requests.push(route.request().postData()!);
      if (lose) {
        lose = false;
        const response = await route.fetch();
        expect(response.status()).toBe(201);
        await route.abort("failed");
      } else await route.continue();
    });
    await panel
      .getByRole("button", { name: "Save desired storage", exact: true })
      .focus();
    await page.keyboard.press("Enter");
    await expect(panel.getByText(/Save delivery is unconfirmed/)).toBeVisible();
    await panel
      .getByRole("button", {
        name: "Retry identical storage request",
        exact: true,
      })
      .click();
    const metadata = panel.getByLabel("Configured storage metadata", {
      exact: true,
    });
    await expect(metadata).toBeVisible();
    expect(requests).toHaveLength(2);
    expect(requests[1]).toBe(requests[0]);
    expect(JSON.parse(requests[0]!).request_id).toMatch(/^[a-f0-9]{32}$/);
    await expect(
      panel.getByText("Unknown; configured metadata only", { exact: true }),
    ).toBeVisible();
    await expect(panel.getByRole("form", { name: "Configure persistent storage", exact: true })).toHaveCount(0);
    const persisted = await (await page.request.get(path)).json();
    expect(persisted.mount_path).toBe("/app/uploads");
    expect(persisted.retention).toBe("retain");
    expect(persisted.destination_id).toBe(server.destination.id);
    await page.unroute(`**${path}`);
    await page.reload();
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption(project.id);
    await hosting
      .getByRole("button", { name: `Storage ${width}`, exact: true })
      .click();
    await expect(
      panel.getByText(persisted.volume_name, { exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByText(/Image files and existing uploads are not imported/),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await panel.scrollIntoViewIfNeeded();
    await panel.screenshot({
      path: info.outputPath(`persistent-storage-${width}.png`),
      animations: "disabled",
    });
  }
  expect(deployments).toEqual([]);
});
