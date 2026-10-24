import { afterEach, expect, test, vi } from "vitest";
import {
  boundedDatabaseRequest,
  createHostingDatabase,
  databaseKey,
  databaseQuery,
  databaseRecipes,
  databaseRequestID,
  getHostingDatabase,
  validateDatabase,
  type HostingDatabase,
} from "./hosting-databases";
const input = {
  request_id: "a".repeat(32),
  project_id: "project",
  project_environment_id: "env",
  name: "Data",
  destination_id: "destination",
  engine: "postgresql",
  recipe_version: databaseRecipes.postgresql,
} as const;
const database: HostingDatabase = {
  resource: {
    id: "database",
    project_id: "project",
    project_environment_id: "env",
    name: "Data",
    kind: "database",
    source: { kind: "image", image: { reference: "postgres:17" } },
    generation: 1,
    created_by: "owner",
    created_at: "",
    updated_at: "",
  },
  binding: {
    id: "binding",
    resource_id: "database",
    destination_id: "destination",
    runtime_namespace: "ncd_database",
    created_by: "owner",
    created_at: "",
  },
  storage_ref: { storage_id: "storage", generation: 1, hash: "hash" },
  engine: "postgresql",
  recipe_version: databaseRecipes.postgresql,
  credentials: [
    {
      role: "admin",
      username: "postgres",
      secret_id: "secret_admin",
      version: 1,
    },
    { role: "app", username: "ncd_app", secret_id: "secret_app", version: 1 },
  ],
  runtime_state: "adapter_unqualified",
};
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
test("creation sends exactly seven fields with CSRF and stable identity; detail encodes resource IDs", async () => {
  const fetch = vi
    .fn()
    .mockImplementation(async () => new Response(JSON.stringify(database)));
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await createHostingDatabase(input, { signal });
  await getHostingDatabase("database", { signal });
  expect(fetch.mock.calls[0]).toMatchObject([
    "/api/v1/databases",
    {
      method: "POST",
      credentials: "include",
      cache: "no-store",
      body: JSON.stringify(input),
      headers: { "X-NeroCD-CSRF": "1" },
      signal: expect.any(AbortSignal),
    },
  ]);
  expect(fetch.mock.calls[1][0]).toBe("/api/v1/resources/database/database");
  expect(databaseRequestID()).toMatch(/^[a-f0-9]{32}$/);
  await expect(getHostingDatabase("database/foreign")).rejects.toThrow("scope");
  expect(fetch.mock.calls[2][0]).toBe(
    "/api/v1/resources/database%2Fforeign/database",
  );
});
test.each([
  { ...input, engine: "redis" },
  { ...input, recipe_version: databaseRecipes.redis },
  { ...input, request_id: "A".repeat(32) },
  { ...input, password: "never" },
])(
  "closed create mapping rejects unsupported bodies before fetch %#",
  async (body) => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(createHostingDatabase(body as typeof input)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  },
);
test("Redis uses only the Redis recipe and closed credential roles", () => {
  const redis: HostingDatabase = {
    ...database,
    engine: "redis",
    recipe_version: databaseRecipes.redis,
    credentials: [
      { role: "app", username: "ncd_app", secret_id: "app", version: 1 },
      { role: "probe", username: "ncd_probe", secret_id: "probe", version: 1 },
    ],
  };
  expect(validateDatabase(redis)).toBe(redis);
  expect(() =>
    validateDatabase({ ...redis, recipe_version: databaseRecipes.postgresql }),
  ).toThrow();
});
test.each([
  { ...database, resource: { ...database.resource, project_id: "foreign" } },
  {
    ...database,
    resource: { ...database.resource, project_environment_id: "foreign" },
  },
  { ...database, resource: { ...database.resource, kind: "application" } },
  { ...database, binding: { ...database.binding, resource_id: "foreign" } },
  { ...database, runtime_state: "running" },
  {
    ...database,
    credentials: [...database.credentials, database.credentials[0]],
  },
  {
    ...database,
    credentials: [
      { ...database.credentials[0], password: "never" },
      database.credentials[1],
    ],
  },
  { ...database, password: "never" },
])(
  "scope, closed metadata and credential validation reject unsafe evidence %#",
  (value) => {
    expect(() =>
      validateDatabase(
        value as HostingDatabase,
        { project: "project", environment: "env" },
        "database",
      ),
    ).toThrow();
  },
);
test.each([400, 401, 403, 404, 409, 503])(
  "HTTP %s remains an error with request identity",
  async (status) => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify({ error: "rejected" }), {
            status,
            headers: { "X-Request-ID": "server-request" },
          }),
        ),
    );
    await expect(createHostingDatabase(input)).rejects.toMatchObject({
      status,
      requestID: "server-request",
    });
  },
);
test("query keys include actor, project, environment and resource and disable retries/cache retention", () => {
  const scope = { actor: "owner", project: "project", environment: "env" };
  for (const next of [
    { ...scope, actor: "other" },
    { ...scope, project: "other" },
    { ...scope, environment: "other" },
  ])
    expect(databaseKey(scope, "database")).not.toEqual(
      databaseKey(next, "database"),
    );
  expect(databaseKey(scope, "database")).not.toEqual(
    databaseKey(scope, "other"),
  );
  expect(databaseQuery(scope, "database")).toMatchObject({
    retry: false,
    gcTime: 0,
  });
});
test("deadline and retirement abort stalled transports even when fetch ignores cancellation", async () => {
  vi.useFakeTimers();
  let signal: AbortSignal | undefined;
  const pending = boundedDatabaseRequest((current) => {
    signal = current;
    return new Promise(() => {});
  });
  const rejected = expect(pending).rejects.toThrow("30 seconds");
  await vi.advanceTimersByTimeAsync(30_000);
  await rejected;
  expect(signal?.aborted).toBe(true);
  const controller = new AbortController();
  const retired = boundedDatabaseRequest(
    () => new Promise(() => {}),
    controller.signal,
  );
  controller.abort();
  await expect(retired).rejects.toMatchObject({ name: "AbortError" });
});
