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

// Uses the existing disposable PostgreSQL/auth/server harness. Archive responses
// are intercepted to qualify operator UX, not physical archive execution.
test("upload archive owner workflow, identical retries and new restore across widths", async ({
  page,
}) => {
  test.setTimeout(120000);
  await signIn(page);
  const suffix = randomBytes(6).toString("hex");
  const project = await post<{ id: string }>(page, "/api/v1/projects", {
    name: `Archive UX ${suffix}`,
  });
  const env = await post<{ id: string }>(page, "/api/v1/project-environments", {
    project_id: project.id,
    name: "Uploads",
  });
  const resource = await post<{ id: string; generation: number }>(
    page,
    "/api/v1/resources",
    {
      project_id: project.id,
      project_environment_id: env.id,
      name: "Uploads",
      kind: "application",
      source: {
        kind: "image",
        image: { reference: "registry.example.test/uploads:1" },
      },
    },
  );
  const hash = `sha256:${"a".repeat(64)}`;
  const base = `/api/v1/resources/${resource.id}`;
  const storage = {
    id: "storage_fixture",
    resource_id: resource.id,
    project_id: project.id,
    binding_id: "binding_fixture",
    destination_id: "dest_fixture",
    kind: "platform_managed",
    driver: "local",
    access_mode: "read_write",
    retention: "retain",
    generation: 1,
    hash,
    volume_name: "owned_uploads",
    mount_path: "/app/uploads",
    created_by: "fixture",
    created_at: "2026-10-24T00:00:00Z",
  };
  const blob = {
    backup_id: "backup_fixture",
    destination_id: "dest_fixture",
    daemon_id: "daemon_fixture",
    sha256: hash,
    size_bytes: 128,
    format: "nerocd-storage-v1",
  };
  const backup = {
    id: "backup_fixture",
    project_id: project.id,
    resource_id: resource.id,
    operation_id: "archive_fixture",
    state: "succeeded",
    blob,
    created_by: "fixture",
    created_at: "2026-10-24T00:00:00Z",
  };
  const operation = {
    id: "archive_fixture",
    resource_id: resource.id,
    project_id: project.id,
    binding_id: storage.binding_id,
    state: "succeeded",
    task_mode: "backup",
    mutation_hold: false,
    restore_ownership_held: false,
    storage_archive: {
      mode: "backup",
      backup_id: backup.id,
      plan: {
        operation_id: "archive_fixture",
        project_id: project.id,
        destination_id: storage.destination_id,
        mode: "backup",
        backup_id: backup.id,
        hash,
        source_storage: storage,
      },
      progress: [
        {
          receipt: {
            id: "receipt_fixture",
            body_hash: hash,
            created_at: backup.created_at,
          },
          plan_hash: hash,
          phase: "archive_published",
          blob,
          restore_verified: false,
        },
      ],
      result: {
        archive_outcome: "succeeded",
        resume_outcome: "resumed",
        restore_verified: false,
        uncertain: false,
      },
    },
  };
  const target = {
    id: "restored_fixture",
    project_id: project.id,
    name: "Restored uploads",
    kind: "application",
    source: {
      kind: "image",
      image: { reference: "registry.example.test/uploads:1" },
    },
  };
  const targetStorage = {
    ...storage,
    id: "restored_storage",
    resource_id: target.id,
    binding_id: "restored_binding",
    volume_name: "new_uploads",
  };
  const restore = {
    resource: target,
    storage: targetStorage,
    operation: {
      ...operation,
      id: "restore_fixture",
      resource_id: target.id,
      binding_id: targetStorage.binding_id,
      task_mode: "restore",
      storage_archive: {
        ...operation.storage_archive,
        mode: "restore",
        restore_id: "restore_fixture",
        plan: {
          ...operation.storage_archive.plan,
          operation_id: "restore_fixture",
          mode: "restore",
          target: {
            resource: target,
            storage: targetStorage,
            plan: { creation_operation_id: "restore_fixture" },
          },
        },
        progress: [],
        result: {
          archive_outcome: "succeeded",
          resume_outcome: "not_needed",
          restore_verified: true,
          uncertain: false,
        },
      },
    },
  };
  let lose = true;
  let restoredResourceCreated = false;
  const writes: string[] = [];
  const reply = (
    route: import("@playwright/test").Route,
    body: unknown,
    status = 200,
  ) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  await page.route(`**${base}/**`, async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === `${base}/binding`)
      return reply(route, {
        id: storage.binding_id,
        resource_id: resource.id,
        destination_id: storage.destination_id,
        runtime_namespace: "ncd_fixture",
      });
    if (path === `${base}/storage`) return reply(route, storage);
    if (path === `${base}/storage-deployment-options`)
      return reply(route, {
        resource_id: resource.id,
        resource_generation: resource.generation,
        storage_ref: { storage_id: storage.id, generation: 1, hash },
        previous_healthy_operation_id: "healthy_fixture",
        host_port: 8080,
        container_port: 80,
        readiness: { path: "/ready", timeout_seconds: 30 },
        available: true,
        reason: "available",
        routes: [],
        storage_route_continuity: null,
      });
    if (path === `${base}/storage-backups`) {
      if (route.request().method() === "POST") {
        writes.push(route.request().postData()!);
        if (lose) {
          lose = false;
          return route.abort("failed");
        }
        return reply(route, backup, 202);
      }
      return reply(route, {
        items: [backup],
        count: 1,
        total: 1,
        offset: 0,
        limit: 20,
      });
    }
    return route.continue();
  });
  await page.route("**/api/v1/hosting-operations/healthy_fixture", (route) =>
    reply(route, {
      id: "healthy_fixture",
      project_id: project.id,
      resource_id: resource.id,
      binding_id: storage.binding_id,
      state: "succeeded",
      mutation_hold: false,
      restore_ownership_held: false,
      snapshot: {
        resource_id: resource.id,
        destination_id: storage.destination_id,
        policy_version: "public-image.owned-storage.release-ports.v1",
        storage: { storage_id: storage.id, hash },
      },
    }),
  );
  await page.route("**/api/v1/storage-backups/backup_fixture", (route) =>
    reply(route, backup),
  );
  await page.route("**/api/v1/storage-backups/backup_fixture/transfers", (route) =>
    route.request().method() === "GET" ? reply(route, []) : route.continue(),
  );
  await page.route("**/api/v1/hosting-operations/archive_fixture", (route) =>
    reply(route, operation),
  );
  await page.route(
    "**/api/v1/storage-backups/backup_fixture/restores",
    async (route) => {
      writes.push(route.request().postData()!);
      if (!restoredResourceCreated) {
        // Materialize only the resource metadata fixture so the manual Projects
        // navigation is checked against a real saved resource. Archive execution
        // remains intercepted and is not qualified by this browser test.
        const saved = await post<{ id: string }>(page, "/api/v1/resources", {
          project_id: project.id,
          project_environment_id: env.id,
          name: target.name,
          kind: "application",
          source: target.source,
        });
        target.id = saved.id;
        targetStorage.resource_id = saved.id;
        restore.operation.resource_id = saved.id;
        restoredResourceCreated = true;
      }
      return reply(route, restore, 202);
    },
  );
  await page.route("**/api/v1/storage-restores/restore_fixture", (route) =>
    reply(route, restore),
  );
  for (const width of [320, 390, 430, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/projects");
    const hosting = page.getByRole("region", {
      name: "Project hosting",
      exact: true,
    });
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption(project.id);
    await hosting.getByRole("button", { name: "Uploads", exact: true }).click();
    const panel = hosting.getByRole("region", {
      name: "Upload backups and restores",
      exact: true,
    });
    await expect(
      panel.getByRole("button", { name: "Backup backup_fixture", exact: true }),
    ).toBeVisible();
    const before = writes.length;
    lose = true;
    await panel.getByRole("checkbox", { name: "I understand writers may stop and this local backup cannot survive host disk loss.", exact: true }).check();
    const request = panel.getByRole("button", {
      name: "Request upload backup",
      exact: true,
    });
    await expect(request).toBeEnabled();
    await request.focus();
    await page.keyboard.press("Enter");
    await expect(
      panel.getByText(/Delivery or current status is unknown/),
    ).toBeVisible();
    await panel
      .getByRole("button", {
        name: "Retry identical archive request",
        exact: true,
      })
      .click();
    await expect(
      panel.getByText("Writer resumption: resumed", { exact: true }),
    ).toBeVisible();
    expect(writes[before]).toBe(writes[before + 1]);
    await panel
      .getByLabel("New restored resource name", { exact: true })
      .fill("Restored uploads");
    await panel
      .getByRole("button", { name: "Restore into new resource", exact: true })
      .click();
    const link = panel.getByRole("link", {
      name: "Open Projects",
      exact: true,
    });
    await expect(
      panel.getByText(/Restore file validation: verified/),
    ).toBeVisible();
    await expect(
      panel.getByText(/Application read\/write verification remains pending/),
    ).toBeVisible();
    expect(JSON.parse(writes[before + 2]!)).toMatchObject({
      name: "Restored uploads",
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    });
    expect(writes.length).toBe(before + 3);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    const bounds = await panel.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.width).toBeLessThanOrEqual(width);
    await link.click();
    await expect(page).toHaveURL(/\/projects$/);
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption(project.id);
    await hosting.getByRole("button", { name: "Uploads", exact: true }).click();
    const restoredArticle = hosting.getByRole("article", {
      name: "Restored uploads",
      exact: true,
    });
    await expect(restoredArticle).toBeVisible();
    await expect(
      restoredArticle.locator(`#storage-${target.id}`),
    ).toBeVisible();
  }
});
