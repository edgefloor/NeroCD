import { expect, test } from "@playwright/test";
// UI-only isolated fixture. All API responses are intercepted; no Docker,
// database, guest tool or physical backup qualification is claimed here.
test("owner backup retry and new-target restore history across responsive widths", async ({
  page,
}) => {
  const digest = "sha256:" + "a".repeat(64),
    time = "2026-10-25T00:00:00Z";
  const engine = "postgresql";
  let role = "owner";
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
          reference: `docker.io/library/${"postgres"}@${digest}`,
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
    engine: "postgresql",
    recipe_version: "postgresql-17.11-bookworm.v1",
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

  let history: Record<string, unknown>[] = [];
  let lose = true;
  const requests: Record<string, unknown>[] = [];
  const unexpected: string[] = [];
  const operation = (id: string, mode = "backup") => ({
    id,
    version: "postgresql.logical-local.v1",
    project_id: "project_fixture",
    source_resource_id: "db_fixture",
    source_generation: 2,
    backup_id: mode === "backup" ? id : "backup_fixture",
    mode,
    state: mode === "backup" ? "queued" : "waiting_target",
    consistency: "logical_snapshot_source_writable",
    ownership_held: true,
    max_bytes: 1073741824,
    timeout_seconds: 900,
    created_at: time,
    updated_at: time,
    ...(mode === "restore"
      ? {
          target_resource_id: "restored_database_fixture",
          blob: { digest, bytes: 65537, format: "postgresql_custom" },
        }
      : {}),
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname;
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
    else if (path === "/api/v1/destinations") body = list([]);
    else if (path === "/api/v1/resources") body = list([database().resource]);
    else if (path === "/api/v1/resources/db_fixture/database")
      body = database();
    else if (path === "/api/v1/resources/db_fixture/deployments")
      body = list([]);
    else if (path === "/api/v1/resources/db_fixture/database-backup-schedules")
      body = [];
    else if (path === "/api/v1/resources/db_fixture/database-backups") {
      if (request.method() === "POST") {
        const input = request.postDataJSON();
        requests.push(input);
        if (!history.length) history = [operation("backup_fixture")];
        if (lose) {
          lose = false;
          return route.abort("failed");
        }
        return route.fulfill({ status: 202, json: history[0] });
      }
      body = history;
    } else if (path === "/api/v1/database-backups/backup_fixture/restores") {
      requests.push(request.postDataJSON());
      const target = operation("restore_fixture", "restore");
      history = [...history, target];
      return route.fulfill({ status: 202, json: target });
    } else if (path === "/api/v1/health") body = { status: "ok" };
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
    role = "owner";
    history = [];
    lose = true;
    requests.length = 0;
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
    const panel = hosting.getByRole("region", {
      name: "Database backups",
      exact: true,
    });
    await expect(
      panel.getByRole("button", { name: "Back up now" }),
    ).toBeEnabled();
    await panel.getByRole("button", { name: "Back up now" }).click();
    await panel.getByRole("button", { name: "Retry the same request" }).click();
    await expect(
      panel.getByText(/Accepted operation backup_fixture/),
    ).toBeVisible();
    expect(requests.length).toBe(2);
    expect(requests[0]).toEqual(requests[1]);
    expect(requests[0]).toEqual({
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
      expected_resource_generation: 2,
    });
    history[0] = {
      ...history[0],
      state: "succeeded",
      ownership_held: false,
      blob: { digest, bytes: 65537, format: "postgresql_custom" },
    };
    await panel.getByRole("button", { name: "Refresh backup history" }).click();
    await panel.getByLabel("Completed backup").selectOption("backup_fixture");
    await panel
      .getByLabel("New database name")
      .fill("Recovered private database");
    await panel
      .getByRole("button", { name: "Restore into new database" })
      .click();
    await expect(
      panel.getByText(/Accepted operation restore_fixture/),
    ).toBeVisible();
    expect(requests[2]).toEqual({
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
      name: "Recovered private database",
    });
    history[1] = { ...history[1], state: "succeeded", ownership_held: false };
    await panel.getByRole("button", { name: "Refresh backup history" }).click();
    await expect(
      panel.getByText(/Import and readiness succeeded/),
    ).toBeVisible();
    await expect(
      panel.getByText(/Target resource: restored_database_fixture/),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `/tmp/nerocd-postgresql-backup-ui-${width}.png`,
      fullPage: true,
    });
    role = "viewer";
    await page.reload();
    await hosting
      .getByLabel("Hosting project", { exact: true })
      .selectOption("project_fixture");
    await hosting
      .getByRole("button", { name: "Database environment", exact: true })
      .click();
    await expect(
      panel.getByText(/Project owners can request backups/),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", { name: "Back up now" }),
    ).toHaveCount(0);
  }
  expect(unexpected).toEqual([]);
});
