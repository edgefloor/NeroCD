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
import type { Resource } from "@/api/hosting";
import { ResourceDatabase } from "./ResourceDatabase";
const resource: Resource = {
  id: "db",
  project_id: "project",
  project_environment_id: "env",
  name: "Data",
  kind: "database",
  source: { kind: "image", image: { reference: "postgres:17" } },
  generation: 1,
  created_by: "owner",
  created_at: "",
  updated_at: "",
};
const database = {
  resource,
  binding: {
    id: "binding",
    resource_id: "db",
    destination_id: "dest",
    runtime_namespace: "ncd_db",
    created_by: "owner",
    created_at: "",
  },
  storage_ref: { storage_id: "storage", generation: 1, hash: "hash" },
  engine: "postgresql",
  recipe_version: "postgresql-17.11-bookworm.v1",
  credentials: [
    {
      role: "admin",
      username: "postgres",
      secret_id: "admin_secret",
      version: 1,
    },
    { role: "app", username: "ncd_app", secret_id: "app_secret", version: 1 },
  ],
  runtime_state: "adapter_unqualified",
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
let client: QueryClient;
function setup(
  handler?: (url: string, init: RequestInit) => Response | Promise<Response>,
) {
  client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  });
  client.setQueryData(queryKeys.principal, {
    id: "viewer",
    provider: "oidc",
    roles: [],
  });
  client.setQueryData(queryKeys.allProjectMembers, [
    { user_id: "viewer", project_id: "project", role: "viewer" },
  ]);
  const fetch = vi.fn(
    handler ??
      (async (url: string) =>
        json(
          url.includes("/database-backups")
            ? []
            : url.includes("/deployments")
              ? { items: [], count: 0, limit: 20, offset: 0, total: 0 }
              : database,
        )),
  );
  vi.stubGlobal("fetch", fetch);
  const view = render(
    <QueryClientProvider client={client}>
      <ResourceDatabase resource={resource} />
    </QueryClientProvider>,
  );
  return { fetch, ...view };
}
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
});
test("viewer reads database metadata and history without mutation or secret access", async () => {
  const { fetch } = setup();
  await screen.findByRole("region", { name: "Database metadata for Data" });
  for (const text of [
    "postgresql",
    "postgresql-17.11-bookworm.v1",
    "ncd_db",
    "dest",
    "storage",
    "hash",
  ])
    expect(screen.getByText(text, { exact: true })).toBeTruthy();
  expect(screen.getByText(/secret ID admin_secret/)).toBeTruthy();
  expect(screen.getByText(/does not start an engine/)).toBeTruthy();
  expect(screen.queryByRole("form")).toBeNull();
  expect(
    screen.queryByRole("button", { name: /^(reveal|copy|deploy|connect)/i }),
  ).toBeNull();
  expect(fetch.mock.calls.map(([url]) => url)).toEqual([
    "/api/v1/resources/db/database",
    "/api/v1/resources/db/deployments?limit=20&offset=0",
    "/api/v1/projects/project/database-backup-targets",
    "/api/v1/resources/db/database-backup-retention",
    "/api/v1/resources/db/database-backup-retention/executions",
    "/api/v1/resources/db/database-backup-schedules",
    "/api/v1/resources/db/database-backups",
  ]);
  for (const [, init] of fetch.mock.calls) expect(init.method).toBe("GET");
});
test.each([401, 403])(
  "current HTTP%s hides metadata and retires scoped cache",
  async (status) => {
    let denied = false;
    setup(async () => (denied ? json({}, status) : json(database)));
    await screen.findByRole("region", { name: "Database metadata for Data" });
    denied = true;
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh database metadata" }),
    );
    await screen.findByText(/Database access is unavailable/);
    expect(screen.queryByText("admin_secret")).toBeNull();
    expect(
      screen.queryByRole("region", { name: "Database metadata for Data" }),
    ).toBeNull();
    expect(
      client.getQueryCache().findAll({ queryKey: ["hostingDatabase"] }),
    ).toHaveLength(0);
  },
);
test.each([400, 404, 409, 503])(
  "known database HTTP%s remains unavailable detail, never an empty create form",
  async (status) => {
    setup(async () => json({}, status));
    await screen.findByRole("alert");
    expect(screen.getByRole("alert").textContent).toContain(`HTTP ${status}`);
    expect(screen.queryByRole("form")).toBeNull();
    expect(
      screen.queryByRole("region", { name: "Database metadata for Data" }),
    ).toBeNull();
  },
);
test("foreign metadata and plaintext extensions never enter displayed or cached query data", async () => {
  setup(async () =>
    json({
      ...database,
      password: "plaintext-never-render",
      resource: { ...resource, project_id: "foreign" },
    }),
  );
  await screen.findByRole("alert");
  expect(screen.queryByText(/plaintext-never-render/)).toBeNull();
  expect(
    client.getQueryCache().findAll({ queryKey: ["hostingDatabase"] })[0]?.state
      .data,
  ).toBeUndefined();
});
test("resource and actor transitions fence late metadata", async () => {
  let resolve!: (response: Response) => void;
  let signal: AbortSignal | null | undefined;
  const { rerender } = setup(async (_, init) => {
    signal = init.signal;
    return new Promise<Response>((done) => {
      resolve = done;
    });
  });
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  rerender(
    <QueryClientProvider client={client}>
      <ResourceDatabase resource={{ ...resource, id: "other" }} blocked />
    </QueryClientProvider>,
  );
  expect(signal?.aborted).toBe(true);
  await act(async () => resolve(json(database)));
  expect(
    screen.queryByRole("region", { name: "Database metadata for Data" }),
  ).toBeNull();
});
