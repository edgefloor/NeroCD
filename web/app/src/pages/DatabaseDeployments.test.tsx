import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import { ApiError } from "@/api/errors";
import { databaseRecipes, type HostingDatabase } from "@/api/hosting-databases";
import { ResourceDatabase } from "./ResourceDatabase";

const digest = "sha256:" + "a".repeat(64);
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
  const operation = {
    id: "operation",
    project_id: "project",
    resource_id: "db",
    binding_id: "binding",
    created_by: "owner",
    state: "queued",
    snapshot,
    mutation_hold: true,
    restore_ownership_held: false,
    reconcile_requested: false,
    created_at: "2026-10-24T00:00:00Z",
    updated_at: "2026-10-24T00:01:00Z",
    task_mode: "deploy",
  };
  return { database, operation };
}
type Operation = ReturnType<typeof fixture>["operation"];
type Handler = (
  url: string,
  init: RequestInit,
) => Response | Promise<Response> | undefined;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "X-Request-ID": "safe-request" },
  });
const page = (items: Operation[], offset = 0, total = items.length) => ({
  items,
  count: items.length,
  total,
  limit: 20,
  offset,
});
let client: QueryClient;
function setup({
  role = "maintainer",
  engine = "postgresql",
  items = [],
  handler,
}: {
  role?: string;
  engine?: "postgresql" | "redis";
  items?: Operation[];
  handler?: Handler;
} = {}) {
  const { database, operation } = fixture(engine);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(queryKeys.principal, {
    id: "actor",
    provider: "oidc",
    roles: [],
  });
  client.setQueryData(queryKeys.allProjectMembers, [
    { user_id: "actor", project_id: "project", role },
  ]);
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const response = handler?.(url, init);
    if (response !== undefined) return response;
    if (url.endsWith("/database")) return json(database);
    if (init.method === "POST" || url.includes("hosting-operations"))
      return json(operation, init.method === "POST" ? 202 : 200);
    if (url.includes("/deployments?")) return json(page(items));
    throw new Error("Unexpected database UI request");
  });
  vi.stubGlobal("fetch", fetch);
  const view = render(
    <QueryClientProvider client={client}>
      <ResourceDatabase resource={database.resource} />
    </QueryClientProvider>,
  );
  return { ...view, fetch, database, operation };
}
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
});
async function deploy() {
  const button = await screen.findByRole("button", { name: "Deploy database" });
  await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  fireEvent.click(button);
}
test.each(["postgresql", "redis"] as const)(
  "%s maintainer directly deploys the saved configuration once and sees private recorded status",
  async (engine) => {
    const { fetch } = setup({ engine });
    await deploy();
    await screen.findByRole("region", { name: "Recorded database operation" });
    const writes = fetch.mock.calls.filter(
      ([, init]) => init.method === "POST",
    );
    expect(writes).toHaveLength(1);
    expect(writes[0]?.[0]).toBe("/api/v1/resources/db/database/deployments");
    expect(JSON.parse(writes[0]![1].body as string)).toEqual({
      expected_generation: 2,
      idempotency_key: expect.stringMatching(/^[a-f0-9]{32}$/),
    });
    expect(
      screen
        .getByRole("button", { name: "Deploy again" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(document.body.textContent).toContain("No host port is published");
    expect(document.body.textContent).not.toMatch(
      /127\.0\.0\.1:0|HTTP readiness|https:\/\//,
    );
    expect(
      fetch.mock.calls.some(([url]) =>
        /secrets|credentials|runtime-status|routes|tasks|templates/.test(url),
      ),
    ).toBe(false);
  },
);
test("an ambiguous response keeps the exact original intent and prevents concurrent duplicate clicks", async () => {
  let resolve!: (value: Response) => void;
  let writes = 0;
  const { fetch, operation } = setup({
    handler: (_url, init) => {
      if (init.method !== "POST") return;
      writes++;
      if (writes === 1)
        return Promise.reject(new TypeError("unsafe-secret-canary"));
      return new Promise<Response>((done) => {
        resolve = done;
      });
    },
  });
  await deploy();
  const retry = await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  expect(document.body.textContent).not.toContain("unsafe-secret-canary");
  fireEvent.click(retry);
  fireEvent.click(retry);
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(2);
  const bodies = fetch.mock.calls
    .filter(([, init]) => init.method === "POST")
    .map(([, init]) => init.body);
  expect(bodies[1]).toBe(bodies[0]);
  await act(async () => resolve(json(operation, 202)));
  await screen.findByRole("region", { name: "Recorded database operation" });
});
test("held history after a lost response permits only the retained byte-identical retry", async () => {
  let writes = 0;
  const { operation } = fixture();
  const { fetch } = setup({
    handler: (url, init) => {
      if (url.includes("/deployments?") && writes > 0)
        return json(page([operation]));
      if (init.method !== "POST") return undefined;
      if (++writes === 1) return Promise.reject(new TypeError("lost response"));
      return json(operation, 202);
    },
  });
  await deploy();
  await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  const original = fetch.mock.calls.find(
    ([, init]) => init.method === "POST",
  )![1].body;
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh database deployment history" }),
  );
  await screen.findByRole("button", {
    name: /operation · generation 2 · queued/,
  });
  const retry = screen.getByRole("button", {
    name: "Retry identical deployment request",
  });
  await waitFor(() => expect(retry.hasAttribute("disabled")).toBe(false));
  expect(writes).toBe(1);
  fireEvent.click(retry);
  fireEvent.click(retry);
  await screen.findByRole("region", { name: "Recorded database operation" });
  const requests = fetch.mock.calls.filter(
    ([, init]) => init.method === "POST",
  );
  expect(requests).toHaveLength(2);
  expect(requests[1]![1].body).toBe(original);
  expect(
    screen
      .getByRole("button", { name: "Deploy again" })
      .hasAttribute("disabled"),
  ).toBe(true);
});
test("409 requires explicit refresh and review without silently replacing the request identity", async () => {
  const { fetch } = setup({
    handler: (_url, init) =>
      init.method === "POST"
        ? json({ error: "unsafe-secret-canary" }, 409)
        : undefined,
  });
  await deploy();
  await screen.findByRole("region", { name: "Database deployment conflict" });
  const body = JSON.parse(
    fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
      .body as string,
  );
  expect(document.body.textContent).toContain(body.idempotency_key);
  expect(document.body.textContent).not.toContain("unsafe-secret-canary");
  expect(
    screen.queryByRole("button", {
      name: "Retry identical deployment request",
    }),
  ).toBeNull();
  expect(
    screen
      .getByRole("button", { name: "Deploy database" })
      .hasAttribute("disabled"),
  ).toBe(true);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Refresh and review database configuration",
    }),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Database deployment conflict" }),
    ).toBeNull(),
  );
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
  expect(screen.queryByText(body.idempotency_key, { exact: false })).toBeNull();
});
test.each([401, 403])(
  "HTTP %s from history clears protected metadata and caches",
  async (status) => {
    setup({
      handler: (url) =>
        url.includes("/deployments?")
          ? json({ error: "secret-canary" }, status)
          : undefined,
    });
    await screen.findByText(/Database access is unavailable/);
    expect(
      screen.queryByRole("region", { name: "Database metadata for Data" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Deploy database" }),
    ).toBeNull();
    expect(
      client
        .getQueryCache()
        .getAll()
        .filter((query) =>
          /hostingDatabase|databaseDeployments|databaseOperation/.test(
            String(query.queryKey[0]),
          ),
        ),
    ).toHaveLength(0);
    expect(document.body.textContent).not.toContain("secret-canary");
  },
);
test.each([401, 403])(
  "shared authority %s aborts an admitted browser request and late receipts cannot revive it",
  async (status) => {
    let resolve!: (value: Response) => void;
    let signal: AbortSignal | undefined;
    const { operation, fetch } = setup({
      handler: (_, init) =>
        init.method === "POST"
          ? new Promise<Response>((done) => {
              signal = init.signal as AbortSignal;
              resolve = done;
            })
          : undefined,
    });
    await deploy();
    await act(async () => {
      await client
        .fetchQuery({
          queryKey: queryKeys.allProjectMembers,
          staleTime: 0,
          retry: false,
          queryFn: async () => {
            throw new ApiError(status, "secret-canary", null);
          },
        })
        .catch(() => {});
    });
    await screen.findByText(/Database access is unavailable/);
    expect(signal?.aborted).toBe(true);
    await act(async () => {
      resolve(json(operation, 202));
      client.setQueryData(queryKeys.allProjectMembers, [
        { user_id: "actor", project_id: "project", role: "maintainer" },
      ]);
    });
    expect(
      screen.queryByRole("region", { name: "Recorded database operation" }),
    ).toBeNull();
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(1);
  },
);
test("maintainer to viewer changes the access epoch, aborts pending writes, and preserves read-only history", async () => {
  let resolve!: (value: Response) => void;
  let signal: AbortSignal | undefined;
  const { operation, fetch } = setup({
    handler: (_, init) =>
      init.method === "POST"
        ? new Promise<Response>((done) => {
            resolve = done;
            signal = init.signal as AbortSignal;
          })
        : undefined,
  });
  await deploy();
  await act(async () =>
    client.setQueryData(queryKeys.allProjectMembers, [
      { user_id: "actor", project_id: "project", role: "viewer" },
    ]),
  );
  await screen.findByText(/Project maintainer access is required/);
  expect(signal?.aborted).toBe(true);
  await act(async () => resolve(json(operation, 202)));
  expect(
    screen.queryByRole("region", { name: "Recorded database operation" }),
  ).toBeNull();
  expect(screen.queryByRole("button", { name: /^Deploy/ })).toBeNull();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
});
test.each(["viewer", "outsider"])("%s cannot deploy", async (role) => {
  const { fetch } = setup({ role: role === "outsider" ? "viewer" : role });
  if (role === "outsider")
    await act(async () => client.setQueryData(queryKeys.allProjectMembers, []));
  else await screen.findByText(/Project maintainer access is required/);
  expect(screen.queryByRole("button", { name: /^Deploy/ })).toBeNull();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
});
test.each(["queued", "reconciliation_required", "succeeded"])(
  "known %s operation with mutation hold blocks a new intent",
  async (state) => {
    const { operation } = fixture();
    operation.state = state;
    setup({ items: [operation] });
    await screen.findByText(/A known operation is active/);
    expect(
      screen
        .getByRole("button", { name: "Deploy again" })
        .hasAttribute("disabled"),
    ).toBe(true);
  },
);
test("foreign operation responses remain unavailable and do not enter cache or create a public URL", async () => {
  const { operation } = fixture();
  operation.snapshot.host_port = 5432;
  const { fetch } = setup({
    handler: (url) =>
      url.includes("/deployments?") ? json(page([operation])) : undefined,
  });
  await screen.findByText(/Database deployment history is unavailable/);
  expect(
    screen
      .getByRole("button", { name: "Deploy database" })
      .hasAttribute("disabled"),
  ).toBe(true);
  expect(screen.queryByText(/127\.0\.0\.1/)).toBeNull();
  expect(
    client
      .getQueryCache()
      .findAll({ queryKey: ["databaseDeployments"] })
      .every((query) => query.state.data === undefined),
  ).toBe(true);
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
});
test("older generation history opens dedicated recorded evidence without current-health claims", async () => {
  const { operation } = fixture();
  operation.snapshot.resource_generation = 1;
  operation.state = "failed";
  operation.mutation_hold = false;
  setup({
    items: [operation],
    handler: (url) =>
      url.includes("hosting-operations") ? json(operation) : undefined,
  });
  fireEvent.click(
    await screen.findByRole("button", {
      name: /operation · generation 1 · failed/,
    }),
  );
  const evidence = await screen.findByRole("region", {
    name: "Recorded database operation",
  });
  expect(evidence.textContent).toContain("Recorded configuration generation1");
  expect(evidence.textContent).toContain("No completion evidence");
  expect(evidence.textContent).not.toContain("http");
});
test("a settled recorded operation permits a new deliberate deployment with a new identity", async () => {
  const { operation } = fixture();
  operation.state = "failed";
  operation.mutation_hold = false;
  const { fetch } = setup({
    handler: (url, init) =>
      init.method === "POST" || url.includes("hosting-operations")
        ? json(operation, init.method === "POST" ? 202 : 200)
        : undefined,
  });
  await deploy();
  await screen.findByRole("region", { name: "Recorded database operation" });
  const button = screen.getByRole("button", { name: "Deploy again" });
  await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  fireEvent.click(button);
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(2),
  );
  const bodies = fetch.mock.calls
    .filter(([, init]) => init.method === "POST")
    .map(([, init]) => JSON.parse(init.body as string));
  expect(bodies[0].idempotency_key).not.toBe(bodies[1].idempotency_key);
  expect(bodies[1].expected_generation).toBe(2);
});
test("a hold discovered on an older page cannot be erased by returning to an empty latest page", async () => {
  const { operation } = fixture();
  const terminal = {
    ...operation,
    id: "settled",
    state: "failed",
    mutation_hold: false,
  };
  setup({
    handler: (url) =>
      url.includes("/deployments?")
        ? json(
            url.includes("offset=20")
              ? page([operation], 20, 21)
              : page([terminal], 0, 21),
          )
        : undefined,
  });
  const next = await screen.findByRole("button", {
    name: "Next database deployments",
  });
  await waitFor(() => expect(next.hasAttribute("disabled")).toBe(false));
  fireEvent.click(next);
  await screen.findByRole("button", {
    name: /operation · generation 2 · queued/,
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Previous database deployments" }),
  );
  await screen.findByRole("button", {
    name: /settled · generation 2 · failed/,
  });
  expect(
    screen
      .getByRole("button", { name: "Deploy again" })
      .hasAttribute("disabled"),
  ).toBe(true);
});
test("a transient authority refresh preserves an uncertain deployment for an identical retry", async () => {
  let writes = 0;
  const { fetch } = setup({
    handler: (_, init) => {
      if (init.method === "POST" && ++writes === 1)
        return Promise.reject(new TypeError("lost response"));
      return undefined;
    },
  });
  await deploy();
  await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  const body = fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
    .body;
  let resolve!: (principal: unknown) => void;
  let reading!: Promise<unknown>;
  await act(async () => {
    reading = client.fetchQuery({
      queryKey: queryKeys.principal,
      staleTime: 0,
      queryFn: () =>
        new Promise((done) => {
          resolve = done;
        }),
    });
  });
  await screen.findByText(
    "Database deployment access is pending or unavailable.",
  );
  expect(
    screen.queryByRole("region", { name: "Database metadata for Data" }),
  ).toBeNull();
  await act(async () => {
    resolve({ id: "actor", provider: "oidc", roles: [] });
    await reading;
  });
  const retry = await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  await waitFor(() => expect(retry.hasAttribute("disabled")).toBe(false));
  fireEvent.click(retry);
  await screen.findByRole("region", { name: "Recorded database operation" });
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST")[1]![1].body,
  ).toBe(body);
});
test("an actor change aborts an in-flight deployment and fences its late receipt", async () => {
  let resolve!: (value: Response) => void;
  let signal: AbortSignal | undefined;
  const { operation } = setup({
    handler: (_, init) =>
      init.method === "POST"
        ? new Promise<Response>((done) => {
            resolve = done;
            signal = init.signal as AbortSignal;
          })
        : undefined,
  });
  await deploy();
  await act(async () =>
    client.setQueryData(queryKeys.principal, {
      id: "actor",
      provider: "other-provider",
      roles: [],
    }),
  );
  await screen.findByRole("region", { name: "Database deployments for Data" });
  expect(signal?.aborted).toBe(true);
  await act(async () => resolve(json(operation, 202)));
  expect(
    screen.queryByRole("region", { name: "Recorded database operation" }),
  ).toBeNull();
  await waitFor(() =>
    expect(
      client.getQueryCache().findAll({
        queryKey: ["databaseOperation", JSON.stringify(["oidc", "actor"])],
      }),
    ).toHaveLength(0),
  );
});
test("a stale settled detail cannot erase a newer recorded hold", async () => {
  const { operation } = fixture();
  operation.updated_at = "2026-10-24T00:02:00Z";
  const stale = {
    ...operation,
    mutation_hold: false,
    state: "succeeded",
    updated_at: "2026-10-24T00:01:00Z",
  };
  setup({
    items: [operation],
    handler: (url) =>
      url.includes("hosting-operations") ? json(stale) : undefined,
  });
  fireEvent.click(
    await screen.findByRole("button", {
      name: /operation · generation 2 · queued/,
    }),
  );
  await screen.findByRole("region", { name: "Recorded database operation" });
  expect(
    screen
      .getByRole("button", { name: "Deploy again" })
      .hasAttribute("disabled"),
  ).toBe(true);
});
test("metadata and resource-list refreshes cannot replace an uncertain original generation or request key", async () => {
  let writes = 0;
  const { fetch, database, rerender } = setup({
    handler: (_, init) => {
      if (init.method === "POST" && ++writes === 1)
        return Promise.reject(new TypeError("lost response"));
      return undefined;
    },
  });
  await deploy();
  await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  const body = fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
    .body;
  const resource = { ...database.resource, generation: 3 };
  rerender(
    <QueryClientProvider client={client}>
      <ResourceDatabase resource={resource} blocked />
    </QueryClientProvider>,
  );
  expect(
    screen.queryByRole("button", {
      name: "Retry identical deployment request",
    }),
  ).toBeNull();
  await act(async () =>
    client.setQueryData(
      [
        "hostingDatabase",
        JSON.stringify(["oidc", "actor"]),
        "project",
        "env",
        "db",
      ],
      { ...database, resource },
    ),
  );
  rerender(
    <QueryClientProvider client={client}>
      <ResourceDatabase resource={resource} />
    </QueryClientProvider>,
  );
  const retry = await screen.findByRole("button", {
    name: "Retry identical deployment request",
  });
  await waitFor(() => expect(retry.hasAttribute("disabled")).toBe(false));
  fireEvent.click(retry);
  await screen.findByRole("region", { name: "Recorded database operation" });
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST")[1]![1].body,
  ).toBe(body);
});
