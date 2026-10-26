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
    else if (path === "/api/v1/resources/db_fixture/database-backup-schedules")
      body = [];
    else if (
      path === "/api/v1/projects/project_fixture/database-backup-targets"
    )
      body = [];
    else if (path === "/api/v1/database-backups/backup_fixture/transfers")
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

// Responsive UI-only fixture: no PostgreSQL, runner or dump qualification.
test("remote storage, upload retry, explicit restore and viewer controls at four widths", async ({
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

  let targets: Record<string, unknown>[] = [],
    validations: Record<string, unknown>[] = [],
    backups: Record<string, unknown>[] = [],
    uploads: Record<string, unknown>[] = [],
    schedules: Record<string, unknown>[] = [];
  let loseRetry = true;
  const targetRequests: Record<string, unknown>[] = [],
    validationRequests: Record<string, unknown>[] = [],
    backupRequests: Record<string, unknown>[] = [],
    retryRequests: Record<string, unknown>[] = [],
    restoreRequests: Record<string, unknown>[] = [],
    scheduleRequests: Record<string, unknown>[] = [];
  const unexpected: string[] = [];
  const transfer = {
    version: "postgresql.logical-s3-transfer.v1",
    project_id: "project_fixture",
    target_id: "storage_fixture",
    target_revision: 1,
    destination_id: "destination_fixture",
    failure_code: "",
    created_at: time,
    updated_at: time,
  };
  const backup = {
    remote_target: { target_id: "storage_fixture", revision: 1 },
    id: "backup_fixture",
    backup_id: "backup_fixture",
    version: "postgresql.logical-local.v1",
    project_id: "project_fixture",
    source_resource_id: "db_fixture",
    source_generation: 2,
    mode: "backup",
    state: "succeeded",
    consistency: "logical_snapshot_source_writable",
    ownership_held: false,
    max_bytes: 1073741824,
    timeout_seconds: 900,
    blob: { digest, bytes: 65537, format: "postgresql_custom" },
    created_at: time,
    updated_at: time,
  };
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
    ) {
      if (request.method() === "POST") {
        if (role !== "owner")
          return route.fulfill({
            status: 403,
            json: { code: "forbidden", error: "denied" },
          });
        const input = request.postDataJSON();
        targetRequests.push(input);
        targets = [
          {
            id: "storage_fixture",
            project_id: "project_fixture",
            version: "postgresql.backup-target.v1",
            revision: 1,
            name: input.name,
            endpoint: input.endpoint,
            bucket: input.bucket,
            region: input.region,
            enabled: input.enabled,
            created_at: time,
            updated_at: time,
          },
        ];
        return route.fulfill({ status: 200, json: targets[0] });
      }
      body = targets;
    } else if (
      path ===
      "/api/v1/projects/project_fixture/database-backup-targets/storage_fixture/validations"
    ) {
      if (request.method() === "POST") {
        validationRequests.push(request.postDataJSON());
        validations = [
          {
            ...transfer,
            id: "validation_fixture",
            mode: "validate",
            state: "succeeded",
          },
        ];
        return route.fulfill({ status: 202, json: validations[0] });
      }
      body = validations;
    } else if (path === "/api/v1/resources/db_fixture/database-backups") {
      if (request.method() === "POST") {
        backupRequests.push(request.postDataJSON());
        backups = [backup];
        uploads = [
          {
            ...transfer,
            id: "upload_failed",
            mode: "upload",
            backup_id: backup.id,
            blob: backup.blob,
            state: "failed",
            failure_code: "access_denied",
          },
        ];
        return route.fulfill({ status: 202, json: backup });
      }
      body = backups;
    } else if (path === "/api/v1/database-backups/backup_fixture/transfers")
      body = uploads;
    else if (
      path === "/api/v1/database-backups/backup_fixture/transfers/retry"
    ) {
      const input = request.postDataJSON();
      retryRequests.push(input);
      uploads = [
        {
          ...transfer,
          id: "upload_succeeded",
          mode: "upload",
          state: "succeeded",
          backup_id: backup.id,
          blob: backup.blob,
        },
      ];
      if (loseRetry) {
        loseRetry = false;
        return route.abort("failed");
      }
      return route.fulfill({ status: 202, json: uploads[0] });
    } else if (path === "/api/v1/database-backups/backup_fixture/restores") {
      restoreRequests.push(request.postDataJSON());
      return route.fulfill({
        status: 202,
        json: {
          ...backup,
          id: "restore_fixture",
          mode: "restore",
          target_resource_id: "new_database_fixture",
          state: "waiting_target",
          ownership_held: true,
        },
      });
    } else if (
      path === "/api/v1/resources/db_fixture/database-backup-schedules"
    ) {
      if (request.method() === "POST") {
        const input = request.postDataJSON();
        scheduleRequests.push(input);
        schedules = [
          {
            id: "schedule_fixture",
            version: "postgresql.logical-local.schedule.v1",
            project_id: "project_fixture",
            resource_id: "db_fixture",
            revision: 1,
            frequency: input.frequency,
            timezone: input.timezone,
            enabled: input.enabled,
            remote_target: input.remote_target,
            created_at: time,
            updated_at: time,
          },
        ];
        return route.fulfill({ status: 200, json: schedules[0] });
      }
      body = schedules;
    } else if (
      path ===
      "/api/v1/resources/db_fixture/database-backup-schedules/schedule_fixture/occurrences"
    )
      body = [];
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
    targets = [];
    validations = [];
    backups = [];
    uploads = [];
    schedules = [];
    loseRetry = true;
    targetRequests.length = 0;
    validationRequests.length = 0;
    backupRequests.length = 0;
    retryRequests.length = 0;
    restoreRequests.length = 0;
    scheduleRequests.length = 0;
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
    const destinations = panel.getByRole("region", {
      name: "Database backup destinations",
      exact: true,
    });
    await destinations
      .getByLabel("Destination name", { exact: true })
      .fill("Daily archive");
    await destinations
      .getByLabel("S3 endpoint", { exact: true })
      .fill("https://s3.example.invalid");
    await destinations.getByLabel("S3 bucket", { exact: true }).fill("backups");
    await destinations
      .getByLabel("S3 access key", { exact: true })
      .fill("access_canary");
    await destinations
      .getByLabel("S3 secret key", { exact: true })
      .fill("secret_canary");
    await destinations
      .getByLabel("S3 access key", { exact: true })
      .fill(" access_canary ");
    await expect(
      destinations.getByRole("button", {
        name: "Create backup destination",
        exact: true,
      }),
    ).toBeDisabled();
    await expect(
      destinations.getByLabel("S3 access key", { exact: true }),
    ).toBeEnabled();
    expect(targetRequests).toHaveLength(0);
    await destinations
      .getByLabel("S3 access key", { exact: true })
      .fill("access_canary");
    await destinations
      .getByRole("button", { name: "Create backup destination", exact: true })
      .click();
    await expect(
      destinations.getByText(/Saved destination Daily archive, revision 1/),
    ).toBeVisible();
    await expect(
      destinations.getByLabel("S3 secret key", { exact: true }),
    ).toHaveValue("");
    await destinations
      .getByRole("button", {
        name: "Validate Daily archive from this runner",
        exact: true,
      })
      .click();
    await expect(destinations.getByText(/Validation succeeded/)).toBeVisible();
    expect(validationRequests[0]).toMatchObject({
      expected_revision: 1,
      destination_id: "destination_fixture",
    });
    await panel
      .getByLabel("Immediate backup destination", { exact: true })
      .selectOption("storage_fixture:1");
    await panel
      .getByRole("button", { name: "Back up now", exact: true })
      .click();
    await expect(
      panel.getByText(/Local dump: succeeded. Latest remote upload: failed/),
    ).toBeVisible();
    await expect(
      panel.getByText(/Remote upload failed: access_denied/),
    ).toBeVisible();
    await panel
      .getByRole("button", {
        name: "Retry upload for backup_fixture",
        exact: true,
      })
      .click();
    await panel
      .getByRole("button", {
        name: "Retry the same upload request",
        exact: true,
      })
      .click();
    await expect(
      panel.getByText(/Latest remote upload: succeeded/),
    ).toBeVisible();
    expect(retryRequests).toHaveLength(2);
    expect(retryRequests[0]).toEqual(retryRequests[1]);
    expect(retryRequests[0]).toMatchObject({
      expected_transfer_id: "upload_failed",
    });
    expect(backupRequests).toHaveLength(1);
    expect(backupRequests[0]).toMatchObject({
      remote_target: { target_id: "storage_fixture", revision: 1 },
    });
    await panel
      .getByLabel("Completed backup", { exact: true })
      .selectOption("backup_fixture");
    await panel
      .getByLabel("Restore archive source", { exact: true })
      .selectOption("upload_succeeded");
    await panel
      .getByLabel("New database name", { exact: true })
      .fill("Restored archive");
    await panel
      .getByRole("button", { name: "Restore into new database", exact: true })
      .click();
    await expect(
      panel.getByText(/Accepted operation restore_fixture/),
    ).toBeVisible();
    expect(restoreRequests[0]).toMatchObject({
      remote_source: {
        transfer_id: "upload_succeeded",
        expected_target_revision: 1,
      },
    });
    const schedule = panel.getByRole("region", {
      name: "Database backup schedules",
      exact: true,
    });
    await schedule
      .getByLabel("Scheduled backup destination", { exact: true })
      .selectOption("storage_fixture:1");
    await schedule
      .getByRole("button", { name: "Create backup schedule", exact: true })
      .click();
    await expect(
      schedule.getByText(/Saved schedule schedule_fixture revision 1/),
    ).toBeVisible();
    expect(scheduleRequests[0]).toMatchObject({
      remote_target: { target_id: "storage_fixture", revision: 1 },
    });
    expect(
      await page.evaluate(
        () => JSON.stringify({ ...localStorage }) + location.href,
      ),
    ).not.toContain("canary");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`remote-backup-${width}.png`),
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
      panel.getByText(/Latest remote upload: succeeded/),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", { name: "Back up now", exact: true }),
    ).toHaveCount(0);
    await expect(
      destinations.getByLabel("S3 secret key", { exact: true }),
    ).toHaveCount(0);
    await expect(
      schedule.getByRole("button", {
        name: "Create backup schedule",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  expect(unexpected).toEqual([]);
});
