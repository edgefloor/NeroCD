import { expect, test } from "@playwright/test";

// UI-only fixture. Every API request is intercepted, with no database, Docker,
// runner, credential delivery, or physical qualification claim.
test("private database deployment retry, recorded status, and read-only history across widths", async ({
  page,
}) => {
  const digest = "sha256:" + "a".repeat(64);
  const time = "2026-10-24T00:00:00Z";
  let engine: "postgresql" | "redis" = "postgresql";
  let role = "maintainer";
  let loseNext = true;
  const bodies: unknown[] = [];
  const unexpected: string[] = [];
  const list = (items: unknown[], limit = 20) => ({
    items,
    count: items.length,
    total: items.length,
    limit,
    offset: 0,
  });
  const database = () => ({
    resource: {
      id: "db_fixture",
      project_id: "project_fixture",
      project_environment_id: "env_fixture",
      name: "Private database",
      kind: "database",
      generation: 2,
      source: {
        kind: "image",
        image: {
          reference: `docker.io/library/${engine === "postgresql" ? "postgres" : "redis"}@${digest}`,
        },
      },
      created_by: "actor_fixture",
      created_at: time,
      updated_at: time,
    },
    binding: {
      id: "binding_fixture",
      resource_id: "db_fixture",
      destination_id: "destination_fixture",
      runtime_namespace: "ncd_" + "b".repeat(32),
      created_by: "actor_fixture",
      created_at: time,
    },
    storage_ref: { storage_id: "storage_fixture", generation: 1, hash: digest },
    engine,
    recipe_version:
      engine === "postgresql"
        ? "postgresql-17.11-bookworm.v1"
        : "redis-8.2.10-bookworm.v1",
    runtime_state: "adapter_unqualified",
    credentials:
      engine === "postgresql"
        ? [
            {
              role: "admin",
              username: "postgres",
              secret_id: "admin_reference",
              version: 1,
            },
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_reference",
              version: 1,
            },
          ]
        : [
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_reference",
              version: 1,
            },
            {
              role: "probe",
              username: "ncd_probe",
              secret_id: "probe_reference",
              version: 1,
            },
          ],
  });
  const operation = () => ({
    id: "operation_fixture",
    project_id: "project_fixture",
    resource_id: "db_fixture",
    binding_id: "binding_fixture",
    created_by: "actor_fixture",
    state: "succeeded",
    mutation_hold: false,
    restore_ownership_held: false,
    reconcile_requested: false,
    created_at: time,
    updated_at: time,
    snapshot: {
      resource_id: "db_fixture",
      resource_generation: 1,
      image_reference: database().resource.source.image.reference,
      binding_id: "binding_fixture",
      destination_id: "destination_fixture",
      server_id: "server_fixture",
      runner_id: "runner_fixture",
      runtime_namespace: database().binding.runtime_namespace,
      container_port: engine === "postgresql" ? 5432 : 6379,
      host_port: 0,
      readiness: { path: "", timeout_seconds: 0 },
      policy_version: "database.private.owned-storage.v1",
      database: {
        engine,
        recipe_version: database().recipe_version,
        configuration_hash: digest,
      },
      storage: database().storage_ref,
    },
    release: {
      canonical_image: database().resource.source.image.reference,
      image_id: digest,
      os: "linux",
      architecture: "arm64",
      variant: "v8",
      daemon_id: "daemon_fixture",
      hash: digest,
    },
    result: {
      outcome: "succeeded",
      ready: true,
      restored: false,
      uncertain: false,
      container_id: "container_fixture",
    },
  });
  let admitted = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    let body: unknown;
    if (path === "/api/v1/me")
      body = {
        id: "actor_fixture",
        provider: "oidc",
        roles: [],
        email: "actor@example.invalid",
      };
    else if (path === "/api/v1/project-members")
      body = list(
        [{ user_id: "actor_fixture", project_id: "project_fixture", role }],
        100,
      );
    else if (path === "/api/v1/projects")
      body = list(
        [{ id: "project_fixture", name: "Database project", created_at: time }],
        Number(url.searchParams.get("limit") ?? 50),
      );
    else if (path === "/api/v1/project-environments")
      body = list([
        {
          id: "env_fixture",
          project_id: "project_fixture",
          name: "Database environment",
          created_by: "actor_fixture",
          created_at: time,
        },
      ]);
    else if (path === "/api/v1/resources") body = list([database().resource]);
    else if (path === "/api/v1/resources/db_fixture/database")
      body = database();
    else if (
      path === "/api/v1/resources/db_fixture/database/deployments" &&
      request.method() === "POST"
    ) {
      bodies.push(request.postDataJSON());
      if (loseNext) {
        loseNext = false;
        return route.abort("failed");
      }
      admitted = true;
      const receipt = operation();
      receipt.snapshot.resource_generation = 2;
      return route.fulfill({ status: 202, json: receipt });
    } else if (path === "/api/v1/resources/db_fixture/deployments")
      body = list(admitted ? [operation()] : []);
    else if (path === "/api/v1/hosting-operations/operation_fixture")
      body = operation();
    else if (path === "/api/v1/health") body = { status: "ok" };
    else if (
      [
        "/api/v1/repositories",
        "/api/v1/runs",
        "/api/v1/templates",
        "/api/v1/approvals",
        "/api/v1/secrets",
      ].includes(path)
    )
      body = list([], Number(url.searchParams.get("limit") ?? 20));
    else {
      unexpected.push(path);
      return route.fulfill({ status: 404, json: {} });
    }
    return route.fulfill({ json: body });
  });
  for (const width of [320, 390, 430, 1440]) {
    engine = width === 390 || width === 1440 ? "redis" : "postgresql";
    role = "maintainer";
    loseNext = true;
    admitted = false;
    bodies.length = 0;
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/projects");
    const hosting = page.getByRole("region", {
      name: "Project hosting",
      exact: true,
    });
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption("project_fixture");
    await hosting
      .getByRole("button", { name: "Database environment", exact: true })
      .click();
    const deployments = hosting.getByRole("region", {
      name: "Database deployments for Private database",
      exact: true,
    });
    const deploy = deployments.getByRole("button", {
      name: "Deploy database",
      exact: true,
    });
    await expect(deploy).toBeEnabled();
    await deploy.focus();
    await page.keyboard.press("Enter");
    const retry = deployments.getByRole("button", {
      name: "Retry identical deployment request",
      exact: true,
    });
    await expect(retry).toBeEnabled();
    await retry.focus();
    await page.keyboard.press("Enter");
    const evidence = deployments.getByRole("region", {
      name: "Recorded database operation",
      exact: true,
    });
    await expect(evidence).toContainText("Ready at recorded completion");
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toEqual(bodies[0]);
    expect(bodies[0]).toEqual({
      expected_generation: 2,
      idempotency_key: expect.stringMatching(/^[a-f0-9]{32}$/),
    });
    await expect(deployments).toContainText("No host port is published");
    await expect(deployments).not.toContainText("127.0.0.1:0");
    await expect(
      deployments.getByRole("link", { name: "Servers", exact: true }),
    ).toHaveAttribute("href", "/runners");
    await expect(
      deployments.getByRole("button", { name: "Deploy again", exact: true }),
    ).toBeEnabled();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: test.info().outputPath(`database-deployments-${width}.png`),
      fullPage: true,
    });
  }
  role = "viewer";
  await page.reload();
  const hosting = page.getByRole("region", {
    name: "Project hosting",
    exact: true,
  });
  await hosting
    .getByLabel("Hosting project", { exact: true })
    .selectOption("project_fixture");
  await hosting
    .getByRole("button", { name: "Database environment", exact: true })
    .click();
  const deployments = hosting.getByRole("region", {
    name: "Database deployments for Private database",
    exact: true,
  });
  await expect(deployments).toContainText(
    "Project maintainer access is required",
  );
  await expect(
    deployments.getByRole("button", { name: /^Deploy/ }),
  ).toHaveCount(0);
  const history = deployments.getByRole("button", {
    name: /operation_fixture · generation 1 · succeeded/,
  });
  await history.focus();
  await page.keyboard.press("Enter");
  await expect(
    deployments.getByRole("region", {
      name: "Recorded database operation",
      exact: true,
    }),
  ).toContainText("Ready at recorded completion");
  expect(unexpected).toEqual([]);
});
