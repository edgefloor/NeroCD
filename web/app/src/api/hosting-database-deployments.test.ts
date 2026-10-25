import historyFixture from "./fixtures/database-upgrade-history.json";
import { afterEach, expect, test, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { databaseRecipes, type HostingDatabase } from "./hosting-databases";
import {
  databaseDeploymentsQuery,
  databaseOperationQuery,
  deployDatabase,
  validateDatabaseOperation,
  validateDatabaseHistoryOperation,
  databaseOperationHeld,
} from "./hosting-database-deployments";

const digest = "sha256:" + "a".repeat(64);
const scope = { actor: "oidc:viewer", project: "project", environment: "env" };
function fixture(engine: "postgresql" | "redis" = "postgresql") {
  const database: HostingDatabase = {
    resource: {
      id: "db",
      project_id: "project",
      project_environment_id: "env",
      name: "Data",
      kind: "database",
      generation: 2,
      source: {
        kind: "image",
        image: {
          reference: `docker.io/library/${engine === "postgresql" ? "postgres" : "redis"}@${digest}`,
        },
      },
      created_by: "owner",
      created_at: "",
      updated_at: "",
    },
    binding: {
      id: "binding",
      resource_id: "db",
      destination_id: "dest",
      runtime_namespace: "ncd_" + "a".repeat(32),
      created_by: "owner",
      created_at: "",
    },
    storage_ref: { storage_id: "storage", generation: 1, hash: digest },
    engine,
    recipe_version: databaseRecipes[engine],
    runtime_state: "adapter_unqualified",
    credentials:
      engine === "postgresql"
        ? [
            {
              role: "admin",
              username: "postgres",
              secret_id: "admin_secret",
              version: 1,
            },
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_secret",
              version: 1,
            },
          ]
        : [
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_secret",
              version: 1,
            },
            {
              role: "probe",
              username: "ncd_probe",
              secret_id: "probe_secret",
              version: 1,
            },
          ],
  };
  const snapshot = {
    resource_id: "db",
    resource_generation: 2,
    image_reference:
      database.resource.source.kind === "image"
        ? database.resource.source.image.reference
        : "",
    binding_id: "binding",
    destination_id: "dest",
    server_id: "server",
    runner_id: "runner",
    runtime_namespace: database.binding.runtime_namespace,
    container_port: engine === "postgresql" ? 5432 : 6379,
    host_port: 0,
    readiness: { path: "", timeout_seconds: 0 },
    policy_version: "database.private.owned-storage.v1",
    database: {
      engine,
      recipe_version: database.recipe_version,
      configuration_hash: digest,
    },
    storage: database.storage_ref,
  };
  const release = {
    canonical_image: snapshot.image_reference,
    image_id: digest,
    os: "linux",
    architecture: "arm64",
    variant: "v8",
    daemon_id: "daemon",
    hash: digest,
  };
  const storage = {
    reference: database.storage_ref,
    volume_name: "ncdv_owned",
    mount_path: engine === "postgresql" ? "/var/lib/postgresql/data" : "/data",
    creation_operation_id: "operation",
    creation_nonce: "nonce",
    daemon_id: "daemon",
  };
  const operation = {
    id: "operation",
    project_id: "project",
    resource_id: "db",
    binding_id: "binding",
    created_by: "owner",
    state: "succeeded",
    snapshot,
    release,
    plan: {
      snapshot,
      release,
      hash: digest,
      compiler_version: "database-hosting.private.owned-storage.v1",
      database: {
        reference: snapshot.database,
        network_name: "ncdn_" + "b".repeat(32),
        credentials: database.credentials,
      },
      storage,
    },
    result: {
      outcome: "succeeded",
      ready: true,
      restored: false,
      uncertain: false,
      container_id: "container",
      plan_hash: digest,
    },
    mutation_hold: false,
    restore_ownership_held: false,
    reconcile_requested: false,
    created_at: "2026-10-24T00:00:00Z",
    updated_at: "2026-10-24T00:01:00Z",
    task_mode: "deploy",
  };
  return { database, operation };
}
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
test.each(["postgresql", "redis"] as const)(
  "%s deployment uses only the frozen body, private evidence and scoped no-store reads",
  async (engine) => {
    const { database, operation } = fixture(engine);
    const page = {
      items: [operation],
      count: 1,
      total: 1,
      limit: 20,
      offset: 0,
    };
    const fetch = vi.fn(async (url: string, _options?: RequestInit) =>
      json(
        url.includes("?limit") ? page : operation,
        url.endsWith("/database/deployments") ? 202 : 200,
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const controller = new AbortController();
    const body = Object.freeze({
      expected_generation: 2,
      idempotency_key: "a".repeat(32),
    });
    const receipt = await deployDatabase(
      database,
      scope,
      body,
      controller.signal,
    );
    await deployDatabase(database, scope, body, controller.signal);
    const client = new QueryClient();
    await client.fetchQuery(databaseDeploymentsQuery(database, scope));
    await client.fetchQuery(
      databaseOperationQuery(database, scope, operation.id),
    );
    expect(fetch.mock.calls[0]?.[1]?.body).toBe(fetch.mock.calls[1]?.[1]?.body);
    expect(fetch.mock.calls[0]).toMatchObject([
      "/api/v1/resources/db/database/deployments",
      {
        method: "POST",
        body: JSON.stringify(body),
        cache: "no-store",
        credentials: "include",
        headers: { "X-NeroCD-CSRF": "1" },
      },
    ]);
    expect(
      fetch.mock.calls.every(
        ([, options]) => (options as RequestInit).cache === "no-store",
      ),
    ).toBe(true);
    expect(databaseDeploymentsQuery(database, scope, 20).queryKey).toEqual([
      "databaseDeployments",
      scope.actor,
      "project",
      "env",
      "db",
      20,
    ]);
    expect(databaseOperationQuery(database, scope, operation.id)).toMatchObject(
      { retry: false, gcTime: 0 },
    );
    expect(receipt).not.toHaveProperty("observation_lease");
    expect(receipt).not.toHaveProperty("plan");
    client.clear();
  },
);
test("history accepts older recorded generations while admission requires the frozen generation", async () => {
  const { database, operation } = fixture();
  operation.snapshot.resource_generation = 1;
  expect(
    validateDatabaseOperation(operation, database, scope).snapshot
      .resource_generation,
  ).toBe(1);
  expect(() =>
    validateDatabaseOperation(operation, database, scope, { generation: 2 }),
  ).toThrow();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    deployDatabase(database, scope, {
      expected_generation: 1,
      idempotency_key: "a".repeat(32),
    }),
  ).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
test.each([
  (op: any) => {
    op.project_id = "foreign";
  },
  (op: any) => {
    op.resource_id = "foreign";
  },
  (op: any) => {
    op.binding_id = "foreign";
  },
  (op: any) => {
    op.snapshot.destination_id = "foreign";
  },
  (op: any) => {
    op.snapshot.storage.hash = "foreign";
  },
  (op: any) => {
    op.snapshot.database.recipe_version = "other";
  },
  (op: any) => {
    op.snapshot.database.engine = "redis";
  },
  (op: any) => {
    op.snapshot.host_port = 5432;
  },
  (op: any) => {
    op.snapshot.readiness.path = "/";
  },
  (op: any) => {
    op.snapshot.runtime_configuration = { generation: 1 };
  },
  (op: any) => {
    op.plan.compiler_version = "image-hosting.v1";
  },
  (op: any) => {
    op.plan.database.credentials[0].password = "secret-canary";
  },
  (op: any) => {
    op.plan.storage.mount_path = "/foreign";
  },
  (op: any) => {
    op.release.canonical_image = "docker.io/library/redis@" + digest;
  },
  (op: any) => {
    op.plan.previous_healthy = {
      snapshot: { ...op.snapshot, resource_id: "foreign" },
      operation_id: "old",
      container_id: "old_container",
      release: op.release,
      plan_hash: digest,
      storage: { plan: op.plan.storage, creation_plan_hash: digest },
    };
  },
  (op: any) => {
    op.password = "secret-canary";
  },
])(
  "foreign or mixed-family evidence is rejected before cache entry %#",
  async (change) => {
    const { database, operation } = fixture();
    // Clone because the fixture intentionally shares its canonical nested values.
    const raw = structuredClone(operation);
    change(raw);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json(raw)),
    );
    const client = new QueryClient();
    await expect(
      client.fetchQuery(databaseOperationQuery(database, scope, "operation")),
    ).rejects.toThrow("Database operation evidence");
    expect(
      client.getQueryData(
        databaseOperationQuery(database, scope, "operation").queryKey,
      ),
    ).toBeUndefined();
    client.clear();
  },
);
test("bounded cancellation fences a transport that replies after scope retirement", async () => {
  const { database, operation } = fixture();
  let resolve!: (value: Response) => void;
  let signal: AbortSignal | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn((_: string, init: RequestInit) => {
      signal = init.signal as AbortSignal;
      return new Promise<Response>((done) => {
        resolve = done;
      });
    }),
  );
  const controller = new AbortController();
  const pending = deployDatabase(
    database,
    scope,
    { expected_generation: 2, idempotency_key: "a".repeat(32) },
    controller.signal,
  );
  controller.abort();
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  expect(signal?.aborted).toBe(true);
  resolve(json(operation, 202));
});
test("a stalled deployment is bounded at thirty seconds without automatic retry", async () => {
  vi.useFakeTimers();
  const { database } = fixture();
  const fetch = vi.fn(() => new Promise<Response>(() => {}));
  vi.stubGlobal("fetch", fetch);
  const pending = deployDatabase(database, scope, {
    expected_generation: 2,
    idempotency_key: "a".repeat(32),
  });
  const rejection = expect(pending).rejects.toThrow("timed out");
  await vi.advanceTimersByTimeAsync(30_000);
  await rejection;
  expect(fetch).toHaveBeenCalledTimes(1);
});

// Generated through the de18fae domain test overlay, never from a live controller.
test.each(Object.entries(historyFixture.cases))(
  "%s domain V2 history is a safe read projection for list and selected GET, separate from V1 authority",
  async (_name, saved) => {
    const database = saved.database as HostingDatabase;
    const readScope = { ...scope, project: database.resource.project_id };
    const operation = structuredClone(saved.operation);
    const fetch = vi.fn(async (url: string) =>
      json(
        url.includes("/deployments?")
          ? { items: [operation], count: 1, total: 1, limit: 20, offset: 0 }
          : operation,
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new QueryClient();
    try {
      const list = await client.fetchQuery(
        databaseDeploymentsQuery(database, readScope),
      );
      const selected = await client.fetchQuery(
        databaseOperationQuery(database, readScope, operation.id),
      );
      expect(selected).toEqual(list.items[0]);
      expect(selected).toMatchObject({
        id: operation.id,
        state: "succeeded",
        plan_hash: operation.plan.hash,
      });
      expect(selected).not.toHaveProperty("plan");
      expect(selected).not.toHaveProperty("database_upgrade");
      expect(selected).not.toHaveProperty("database_maintenance");
      expect(selected).not.toHaveProperty("observation_lease");
      expect(() =>
        validateDatabaseOperation(operation, database, readScope),
      ).toThrow();
    } finally {
      client.clear();
    }
  },
);

test.each(Object.entries(historyFixture.cases))(
  "%s matching V2 admission response remains usable with exact generation and unchanged request",
  async (_name, saved) => {
    const database = saved.database as HostingDatabase;
    const readScope = { ...scope, project: database.resource.project_id };
    const operation: Record<string, unknown> = structuredClone(saved.operation);
    operation.state = "queued";
    operation.mutation_hold = true;
    for (const key of [
      "plan",
      "release",
      "prepared_receipt",
      "complete_receipt",
      "result",
    ])
      delete operation[key];
    const fetch = vi.fn(async () => json(operation, 202));
    vi.stubGlobal("fetch", fetch);
    const body = {
      expected_generation: database.resource.generation,
      idempotency_key: "b".repeat(32),
    };
    const admitted = await deployDatabase(database, readScope, body);
    expect(admitted).toMatchObject({
      id: operation.id,
      state: "queued",
      mutation_hold: true,
      evidence_incomplete: true,
    });
    expect(databaseOperationHeld(admitted)).toBe(true);
    expect(fetch.mock.calls[0]).toMatchObject([
      expect.stringContaining("/database/deployments"),
      { body: JSON.stringify(body) },
    ]);
    operation.snapshot = {
      ...(operation.snapshot as object),
      resource_generation: database.resource.generation + 1,
    };
    await expect(deployDatabase(database, readScope, body)).rejects.toThrow();
  },
);
const v2Changes: Record<string, (op: any) => void> = {
  project: (op) => {
    op.project_id = "foreign";
  },
  resource: (op) => {
    op.resource_id = "foreign";
  },
  binding: (op) => {
    op.binding_id = "foreign";
  },
  destination: (op) => {
    op.snapshot.destination_id = "foreign";
  },
  missingHold: (op) => {
    delete op.mutation_hold;
  },
  missingRestoreHold: (op) => {
    delete op.restore_ownership_held;
  },
  missingUncertainty: (op) => {
    delete op.result.uncertain;
  },
  malformedHold: (op) => {
    op.mutation_hold = "false";
  },
  result: (op) => {
    op.result.plan_hash = digest;
  },
  image: (op) => {
    op.result.image_id = digest;
  },
  release: (op) => {
    op.release.daemon_id = "foreign";
  },
  notReady: (op) => {
    op.result.ready = false;
  },
  uncertain: (op) => {
    op.result.uncertain = true;
  },
  missingResult: (op) => {
    delete op.result;
  },
  missingProof: (op) => {
    delete op.database_upgrade;
  },
  missingPlan: (op) => {
    delete op.plan;
  },
  compiler: (op) => {
    op.plan.compiler_version = "foreign";
  },
  version: (op) => {
    op.database_upgrade.version = "foreign";
  },
  digest: (op) => {
    op.database_upgrade.hash = "foreign";
  },
  state: (op) => {
    op.state = "foreign";
  },
  mixed: (op) => {
    op.database_maintenance =
      historyFixture.cases.postgresql_maintenance.operation.database_maintenance;
  },
  openOperation: (op) => {
    op.command = [];
  },
  openProof: (op) => {
    op.database_upgrade.command = [];
  },
  openPlan: (op) => {
    op.plan.command = [];
  },
};
test.each(Object.entries(v2Changes))(
  "V2 history rejects %s evidence in list and selected reads",
  async (_name, change) => {
    const saved = historyFixture.cases.postgresql_upgrade;
    const operation = structuredClone(saved.operation);
    change(operation);
    const database = saved.database as HostingDatabase;
    const readScope = { ...scope, project: database.resource.project_id };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        json(
          url.includes("/deployments?")
            ? { items: [operation], count: 1, total: 1, offset: 0, limit: 20 }
            : operation,
        ),
      ),
    );
    const client = new QueryClient();
    try {
      await expect(
        client.fetchQuery(databaseDeploymentsQuery(database, readScope)),
      ).rejects.toThrow();
      await expect(
        client.fetchQuery(
          databaseOperationQuery(database, readScope, operation.id),
        ),
      ).rejects.toThrow();
    } finally {
      client.clear();
    }
  },
);
test("incomplete and uncertain V2 history remains held without overwriting recorded state or holds", () => {
  const saved = historyFixture.cases.postgresql_upgrade,
    database = saved.database as HostingDatabase;
  const readScope = { ...scope, project: database.resource.project_id };
  const operation: any = structuredClone(saved.operation);
  operation.state = "failed";
  operation.result.outcome = "failed";
  operation.result.ready = false;
  operation.result.uncertain = true;
  let projection = validateDatabaseHistoryOperation(
    operation,
    database,
    readScope,
  );
  expect(projection).toMatchObject({
    state: "failed",
    mutation_hold: false,
    result: { uncertain: true },
  });
  expect(databaseOperationHeld(projection)).toBe(true);
  delete operation.result;
  delete operation.complete_receipt;
  projection = validateDatabaseHistoryOperation(operation, database, readScope);
  expect(projection).toMatchObject({
    state: "failed",
    mutation_hold: false,
    evidence_incomplete: true,
  });
  expect(databaseOperationHeld(projection)).toBe(true);
});
