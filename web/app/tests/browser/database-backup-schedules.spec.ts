import { expect, test } from "@playwright/test";
// Responsive UI-only fixture: no PostgreSQL, runner or dump qualification.
test("schedule owner create/replay/edit/disable and viewer history at four widths", async ({
  page,
}, testInfo) => {
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

  let schedules: Record<string, unknown>[] = [];
  let occurrences: Record<string, unknown>[] = [];
  let lose = true;
  const requests: Record<string, unknown>[] = [];
  const unexpected: string[] = [];
  const replay = new Map<string, Record<string, unknown>>();
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname;
    let body: unknown;
    if (request.method() === "GET" && path === "/api/v1/hosting-catalog") body = [];
    else if (request.method() === "GET" && path === "/api/v1/resources/db_fixture/database-backup-retention") body = null;
    else if (request.method() === "GET" && path === "/api/v1/resources/db_fixture/database-backup-retention/executions") body = [];
    else if (path === "/api/v1/me")
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
    else if (
      path === "/api/v1/projects/project_fixture/database-backup-targets"
    )
      body = [];
    else if (path === "/api/v1/resources/db_fixture/database-backups")
      body = [];
    else if (
      path === "/api/v1/resources/db_fixture/database-backup-schedules" ||
      path ===
        "/api/v1/resources/db_fixture/database-backup-schedules/schedule_fixture"
    ) {
      if (request.method() !== "GET") {
        if (role !== "owner")
          return route.fulfill({
            status: 403,
            json: { code: "forbidden", error: "denied" },
          });
        const input = request.postDataJSON();
        requests.push(input);
        let saved = replay.get(input.request_id);
        if (!saved) {
          if (input.expected_version !== (schedules[0]?.revision ?? 0))
            return route.fulfill({
              status: 409,
              json: { code: "conflict", error: "stale" },
            });
          saved = {
            id: "schedule_fixture",
            version: "postgresql.logical-local.schedule.v1",
            project_id: "project_fixture",
            resource_id: "db_fixture",
            revision: input.expected_version + 1,
            frequency: input.frequency,
            timezone: input.timezone,
            enabled: input.enabled,
            created_at: time,
            updated_at: time,
          };
          schedules = [saved];
          replay.set(input.request_id, saved);
        }
        if (lose) {
          lose = false;
          return route.abort("failed");
        }
        return route.fulfill({ status: 200, json: saved });
      }
      body = schedules;
    } else if (
      path ===
      "/api/v1/resources/db_fixture/database-backup-schedules/schedule_fixture/occurrences"
    )
      body = occurrences;
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
    role = "owner";
    schedules = [];
    occurrences = [];
    lose = true;
    requests.length = 0;
    replay.clear();
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
      name: "Database backup schedules",
      exact: true,
    });
    await expect(
      panel.getByRole("button", {
        name: "Create backup schedule",
        exact: true,
      }),
    ).toBeEnabled();
    await panel
      .getByLabel("Backup frequency", { exact: true })
      .fill("every_minute");
    await panel
      .getByRole("button", { name: "Create backup schedule", exact: true })
      .click();
    await panel
      .getByRole("button", {
        name: "Retry the same schedule request",
        exact: true,
      })
      .click();
    await expect(
      panel.getByText(/Saved schedule schedule_fixture revision 1/),
    ).toBeVisible();
    expect(requests).toHaveLength(2);
    expect(requests[0]).toEqual(requests[1]);
    expect(requests[0]).toEqual({
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
      expected_resource_generation: 2,
      expected_version: 0,
      frequency: "every_minute",
      timezone: "UTC",
      enabled: true,
    });
    await panel
      .getByLabel("Backup frequency", { exact: true })
      .fill("* * * * *");
    await panel
      .getByLabel("Backup timezone", { exact: true })
      .fill("Europe/Budapest");
    await panel
      .getByRole("button", { name: "Save schedule changes", exact: true })
      .click();
    await expect(
      panel.getByText(/Saved schedule schedule_fixture revision 2/),
    ).toBeVisible();
    await panel
      .getByRole("button", { name: "Disable schedule", exact: true })
      .click();
    await expect(
      panel.getByText(/Saved schedule schedule_fixture revision 3/),
    ).toBeVisible();
    expect(requests[2]).toMatchObject({
      expected_version: 1,
      frequency: "* * * * *",
      timezone: "Europe/Budapest",
      enabled: true,
    });
    expect(requests[3]).toMatchObject({ expected_version: 2, enabled: false });
    occurrences = [
      {
        id: "occurrence_fixture",
        schedule_id: "schedule_fixture",
        project_id: "project_fixture",
        resource_id: "db_fixture",
        schedule_revision: 1,
        frequency: "every_minute",
        timezone: "UTC",
        scheduled_at: time,
        created_at: time,
        operation_id: "backup_scheduled",
        state: "succeeded",
        reason: "",
        ownership_held: false,
      },
    ];
    await panel
      .getByRole("button", { name: "Refresh schedules", exact: true })
      .click();
    await expect(
      panel.getByText("Backup operation: backup_scheduled", { exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`schedule-owner-${width}.png`),
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
      panel.getByText(/Project owners can create, edit and disable/),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", {
        name: "Create backup schedule",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      panel.getByRole("button", { name: "Disable schedule", exact: true }),
    ).toHaveCount(0);
    await panel
      .getByRole("button", { name: "View occurrences", exact: true })
      .click();
    await expect(
      panel.getByText("Backup operation: backup_scheduled", { exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(async () => {
        const r = await fetch(
          "/api/v1/resources/db_fixture/database-backup-schedules",
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              request_id: "viewer_denied",
              expected_resource_generation: 2,
              expected_version: 0,
              frequency: "daily",
              timezone: "UTC",
              enabled: true,
            }),
          },
        );
        return r.status;
      }),
    ).toBe(403);
    expect(requests).toHaveLength(4);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  expect(unexpected).toEqual([]);
});
