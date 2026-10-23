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
// These intercepted responses qualify review/retry/continuity UX only. They do
// not qualify a runner, deployment admission, retained-volume writes or HTTPS.
test("storage deployment reviews exact authority and separates historical continuity across widths", async ({
  page,
}, info) => {
  test.setTimeout(120000);
  await signIn(page);
  const suffix = randomBytes(6).toString("hex");
  const project = await post<{ id: string }>(page, "/api/v1/projects", {
    name: `Storage deployment UX ${suffix}`,
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
        image: { reference: "registry.example.test/uploads:2" },
      },
    },
  );
  const base = `/api/v1/resources/${resource.id}`;
  const storageRef = {
    storage_id: "storage_fixture",
    generation: 1,
    hash: "storage-fixture-hash",
  };
  const continuity = {
    expected_loaded_generation: 7,
    expected_loaded_proxy_operation_id: "proxy_old",
    routes: [
      {
        route_id: "route_fixture",
        expected_version: 4,
        expected_selected_release_operation_id: "hop_old",
      },
    ],
  };
  const options = {
    resource_id: resource.id,
    resource_generation: resource.generation,
    storage_ref: storageRef,
    previous_healthy_operation_id: "hop_old",
    host_port: 8080,
    container_port: 80,
    readiness: { path: "/health", timeout_seconds: 30 },
    available: true,
    reason: "available",
    routes: [
      {
        id: "route_fixture",
        hostname: "uploads.example.test",
        path_prefix: "/",
        version: 4,
        selected_release_operation_id: "hop_old",
      },
    ],
    storage_route_continuity: continuity,
  };
  const operation = {
    id: "hop_fixture",
    project_id: project.id,
    resource_id: resource.id,
    binding_id: "binding_fixture",
    state: "succeeded",
    snapshot: {
      resource_id: resource.id,
      resource_generation: resource.generation,
      image_reference: "registry.example.test/uploads:2",
      binding_id: "binding_fixture",
      destination_id: "dest_fixture",
      server_id: "server_fixture",
      runner_id: "runner_fixture",
      runtime_namespace: "ncd_fixture",
      container_port: 80,
      host_port: 8080,
      readiness: options.readiness,
      storage: storageRef,
      storage_route_continuity: { hash: "continuity-fixture-hash" },
      policy_version: "fixture",
    },
    result: {
      outcome: "succeeded",
      ready: true,
      restored: false,
      uncertain: false,
    },
    restore_ownership_held: false,
    mutation_hold: false,
    reconcile_requested: false,
    created_by: "fixture",
    created_at: "2026-10-24T00:00:00Z",
    updated_at: "2026-10-24T00:00:00Z",
  };
  let lost = true,
    conflict = false;
  const writes: string[] = [];
  await page.route(`**${base}/**`, async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname;
    const reply = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (path === `${base}/binding`)
      return reply({
        id: "binding_fixture",
        resource_id: resource.id,
        destination_id: "dest_fixture",
        runtime_namespace: "ncd_fixture",
      });
    if (path === `${base}/storage`)
      return reply({
        ...storageRef,
        id: storageRef.storage_id,
        project_id: project.id,
        resource_id: resource.id,
        binding_id: "binding_fixture",
        destination_id: "dest_fixture",
        volume_name: "nerocd_uploads_fixture",
        mount_path: "/app/uploads",
        kind: "platform_managed",
        driver: "local",
        access_mode: "read_write",
        retention: "retain",
        created_by: "fixture",
        created_at: "2026-10-24T00:00:00Z",
      });
    if (path === `${base}/storage-deployment-options`) return reply(options);
    if (path === `${base}/retained-releases`) return reply([]);
    if (path === `${base}/storage-route-continuity/hop_fixture`)
      return reply({
        resource_id: resource.id,
        operation_id: "hop_fixture",
        state: "loaded",
        reason: "loaded_historical",
        loaded_generation: 8,
      });
    if (path === `${base}/deployments` && request.method() === "GET")
      return reply({ items: [], count: 0, total: 0, limit: 20, offset: 0 });
    if (path === `${base}/deployments` && request.method() === "POST") {
      writes.push(request.postData()!);
      if (lost) {
        lost = false;
        return route.abort("failed");
      }
      return reply(
        conflict ? { error: "stale authority" } : operation,
        conflict ? 409 : 202,
      );
    }
    return route.continue();
  });
  await page.route("**/api/v1/hosting-operations/hop_fixture", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(operation),
    }),
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
    const review = hosting.getByRole("region", {
      name: "Persistent upload deployment review",
      exact: true,
    });
    await review
      .getByRole("button", {
        name: "Review current storage deployment",
        exact: true,
      })
      .click();
    await expect(review.getByText(/uploads.example.test/)).toBeVisible();
    await expect(
      review.getByText(/Image rollback does not undo writes/),
    ).toBeVisible();
    const submit = review.getByRole("button", {
      name: "Deploy reviewed image with uploads",
      exact: true,
    });
    await expect(submit).toBeDisabled();
    await review.screenshot({
      path: info.outputPath(`storage-review-${width}.png`),
      animations: "disabled",
    });
    await review.getByRole("checkbox").check();
    await submit.focus();
    await page.keyboard.press("Enter");
    if (width === 320) {
      await expect(review.getByText(/Delivery is unconfirmed/)).toBeVisible();
      await review
        .getByRole("button", {
          name: "Retry identical storage deployment request",
          exact: true,
        })
        .click();
      expect(writes[1]).toBe(writes[0]);
      expect(JSON.parse(writes[0]!).storage_route_continuity).toEqual(
        continuity,
      );
    }
    if (conflict) {
      await expect(
        review.getByRole("button", {
          name: "Retry identical storage deployment request",
          exact: true,
        }),
      ).toBeDisabled();
      await review
        .getByRole("button", {
          name: "Review current storage and routes for a new request",
          exact: true,
        })
        .click();
      await expect(review.getByRole("checkbox")).not.toBeChecked();
    } else {
      await expect(
        hosting.getByText(/Historical loaded receipt for generation 8/),
      ).toBeVisible();
      await expect(
        hosting.getByText(/does not confirm current routes/),
      ).toBeVisible();
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await review.screenshot({
      path: info.outputPath(`storage-deployment-${width}.png`),
      animations: "disabled",
    });
    conflict = width === 430;
  }
});
