import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { focusManager, notifyManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { ResourceDatabaseConnections } from "./ResourceDatabaseConnections";
vi.mock("./ResourceDeployments", () => ({ ResourceDeployments: () => <p>Legacy deployment controls</p> }));
const resource: Resource = { id: "app", project_id: "project", project_environment_id: "env", kind: "application", name: "App", generation: 3, source: { kind: "image", image: { reference: "nginx:stable" } }, created_by: "owner", created_at: "", updated_at: "" };
const connection = { id: "hcn_test", project_id: "project", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: `sha256:${"a".repeat(64)}`, state: "active", reason_code: "completed", capture_id: "capture", upgrade_operation_id: "upgrade", application_operation_id: "app_operation", membership_revision: 4, created_at: "2026-10-24T00:00:00Z", updated_at: "2026-10-24T00:00:00Z" };
const option = { resource_id: "db", name: "Data", engine: "postgresql", generation: 2, destination_id: "dest", status: "available", reason_code: "eligible" };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let client: QueryClient;
function setup(handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined, role = "owner") {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(queryKeys.principal, { id: "actor", provider: "oidc", roles: [] });
  client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "actor", role }]);
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const custom = handler?.(url, init);
    if (custom) return custom;
    if (url.endsWith("/database-connection")) return json({}, 404);
    if (url.endsWith("/options")) return json({ items: [option] });
    if (url.endsWith("/runtime-configuration")) return json({ runtime_configuration: { resource_id: "app", project_id: "project", resource_generation: 3, runtime_generation: 5, entries: [] } });
    if (url.endsWith("/binding")) return json({ resource_id: "app", destination_id: "dest" });
    return json(resource);
  });
  vi.stubGlobal("fetch", fetch);
  return { fetch, ...render(<QueryClientProvider client={client}><ResourceDatabaseConnections resource={resource} writable blocked={false} /></QueryClientProvider>) };
}
afterEach(() => { cleanup(); client?.clear(); focusManager.setFocused(undefined); vi.unstubAllGlobals(); });
async function choose() {
  await screen.findByRole("option", { name: "Data · postgresql · Available" });
  fireEvent.change(screen.getByLabelText("Database to connect"), { target: { value: "db" } });
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Connect database and deploy application" }));
}
test("owner connects with pre-injection CAS in both fields without secret access", async () => {
  let saved = false;
  const { fetch } = setup((url, init) => {
    if (url.endsWith("/database-connections") && init.method === "POST") { saved = true; return json(connection, 202); }
    if (saved && url.endsWith("/database-connection")) return json(connection);
  });
  await choose();
  await screen.findByText(/Request accepted: hcn_test/);
  const body = JSON.parse(String(fetch.mock.calls.find(([url, init]) => url.endsWith("/database-connections") && init.method === "POST")?.[1].body));
  expect(body.binding.expected_runtime_generation).toBe(5);
  expect(body.deployment.expected_runtime_configuration_generation).toBe(5);
  expect(screen.queryByText("Legacy deployment controls")).toBeNull();
  expect(fetch.mock.calls.some(([url]) => /secrets|reveal/.test(url))).toBe(false);
});
test("viewer sees durable status but no mutation controls", async () => {
  const { fetch } = setup((url) => url.endsWith("/database-connection") ? json(connection) : undefined as never, "viewer");
  await screen.findByText("Connection workflow completed");
  expect(screen.queryByRole("button", { name: "Redeploy connected application" })).toBeNull();
  expect(fetch.mock.calls.every(([, init]) => init.method !== "POST")).toBe(true);
});
test("503 is unavailable and never exposes legacy controls; unknown state pauses actions", async () => {
  setup((url) => url.endsWith("/database-connection") ? json({}, 503) : undefined as never);
  await screen.findByRole("alert");
  expect(screen.queryByText("Legacy deployment controls")).toBeNull();
  cleanup(); client.clear();
  setup((url) => url.endsWith("/database-connection") ? json({ ...connection, state: "future" }) : undefined as never);
  await screen.findByText(/Unrecognized connection status/);
  expect((screen.getByRole("button", { name: "Redeploy connected application" }) as HTMLButtonElement).disabled).toBe(true);
});
test("400 preserves nonsecret draft; uncertain response retries identical body", async () => {
  let count = 0;
  const { fetch } = setup((url, init) => {
    if (url.endsWith("/database-connections") && init.method === "POST") { count++; return count === 1 ? json({}, 400) : Promise.reject(new TypeError("lost")); }
  });
  await choose();
  await screen.findByText(/Settings were rejected/);
  expect((screen.getByLabelText("Database to connect") as HTMLSelectElement).value).toBe("db");
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Connect database and deploy application" }));
  await screen.findByText(/No confirmed response/);
  fireEvent.click(screen.getByRole("button", { name: "Retry identical connection request" }));
  await waitFor(() => expect(count).toBe(3));
  const bodies = fetch.mock.calls.filter(([url, init]) => url.endsWith("/database-connections") && init.method === "POST").map(([, init]) => init.body);
  expect(bodies[1]).toBe(bodies[2]);
});
test("409 blocks retry until explicit refreshed review and new confirmation", async () => {
  setup((url, init) => url.endsWith("/database-connections") && init.method === "POST" ? json({}, 409) : undefined as never);
  await choose(); await screen.findByRole("region", { name: "Review changed connection" });
  const retry = screen.getByRole("button", { name: "Retry identical connection request" }) as HTMLButtonElement;
  expect(retry.disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh for conflict review" }));
  await waitFor(() => expect((screen.getByRole("button", { name: "Use refreshed configuration for a new request" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Use refreshed configuration for a new request" }));
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
});
test("cross-scope metadata and permission changes prevent mutation and clear retained draft", async () => {
  setup((url) => url.endsWith("/database-connection") ? json({ ...connection, project_id: "foreign" }) : undefined as never);
  await screen.findByRole("alert");
  expect(screen.queryByText("db", { exact: true })).toBeNull();
  cleanup(); client.clear();
  setup();
  await screen.findByLabelText("Database to connect");
  fireEvent.change(screen.getByLabelText("Database to connect"), { target: { value: "db" } });
  act(() => client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "actor", role: "viewer" }]));
  await waitFor(() => expect(screen.queryByLabelText("Database to connect")).toBeNull());
});
test("held status preserves hold and explains unavailable exact observation evidence", async () => {
  const { fetch } = setup((url) => url.endsWith("/database-connection") ? json({ ...connection, state: "held", hold_operation_id: "app_operation" }) : undefined as never);
  await screen.findByText(/Observation is unavailable here/);
  expect(screen.queryByRole("button", { name: /recovery|unlock|observation/i })).toBeNull();
  expect(fetch.mock.calls.some(([url]) => /runners|recovery/.test(url))).toBe(false);
});
test("late accepted response after actor change cannot publish old connection", async () => {
  let resolve!: (value: Response) => void;
  setup((url, init) => url.endsWith("/database-connections") && init.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined as never);
  await choose(); await waitFor(() => expect(resolve).toBeTruthy());
  act(() => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: [] }); client.setQueryData(queryKeys.allProjectMembers, []); });
  await act(async () => resolve(json(connection, 202)));
  expect(screen.queryByText(/Request accepted/)).toBeNull();
});
test("connected redeploy uses dedicated endpoint and database maintenance captures current database generation and revision", async () => {
  const { fetch } = setup((url, init) => {
    if (url.endsWith("/database-connection")) return json(connection);
    if (url.endsWith("/resources/db")) return json({ ...resource, id: "db", kind: "database", generation: 7 });
    if (init.method === "POST") return json({ id: "operation", project_id: "project", resource_id: url.includes("/db/") ? "db" : "app", snapshot: { resource_id: url.includes("/db/") ? "db" : "app" } }, 202);
  });
  await screen.findByText("Connection workflow completed");
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Redeploy connected application" }));
  await screen.findByText(/Request accepted: operation/);
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Redeploy connected database" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(2));
  const mutations = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(mutations[0]![0]).toBe("/api/v1/resources/app/database-connection/deployments");
  expect(mutations[1]![0]).toBe("/api/v1/resources/db/database/maintenance");
  expect(JSON.parse(String(mutations[1]![1].body))).toMatchObject({ expected_generation: 7, expected_membership_revision: 4 });
});
test("application generation drift requires review", async () => {
  const { fetch } = setup((url) => url.endsWith("/resources/app") ? json({ ...resource, generation: 4 }) : undefined);
  await choose();
  await screen.findByText(/Saved configuration or membership changed/);
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("selected database generation drift never sends a connection request", async () => {
  let reads = 0;
  const { fetch } = setup((url) => url.endsWith("/options") ? json({ items: [{ ...option, generation: ++reads === 1 ? 2 : 3 }] }) : undefined);
  await choose(); await screen.findByText(/Saved configuration or membership changed/);
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("mismatched accepted operation is unconfirmed and keeps the exact request", async () => {
  setup((url, init) => {
    if (url.endsWith("/database-connection")) return json(connection);
    if (init.method === "POST") return json({ id: "foreign", project_id: "other", resource_id: "app", snapshot: { resource_id: "app" } }, 202);
  });
  await screen.findByText("Connection workflow completed");
  fireEvent.click(screen.getByRole("checkbox")); fireEvent.click(screen.getByRole("button", { name: "Redeploy connected application" }));
  await screen.findByText(/No confirmed response/);
  expect(screen.queryByText(/Request accepted/)).toBeNull();
  expect(screen.getByRole("button", { name: "Retry identical application request" })).toBeTruthy();
});
test("owner downgrade retires pending mutation and ignores its late accepted response", async () => {
  let resolve!: (value: Response) => void;
  setup((url, init) => url.endsWith("/database-connections") && init.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  await choose(); await waitFor(() => expect(resolve).toBeTruthy());
  act(() => client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "actor", role: "viewer" }]));
  await act(async () => resolve(json(connection, 202)));
  expect(screen.queryByText(/Request accepted/)).toBeNull();
  await waitFor(() => expect(screen.queryByRole("form", { name: "Review database connection request" })).toBeNull());
});
test("unknown authoritative option status remains unavailable even after a synthetic selection", async () => {
  const { fetch } = setup((url) => url.endsWith("/options") ? json({ items: [{ ...option, status: "future" }] }) : undefined);
  const choice = await screen.findByRole("option", { name: "Data · postgresql · Unavailable" });
  expect((choice as HTMLOptionElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Database to connect"), { target: { value: "db" } });
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.submit(screen.getByRole("form", { name: "Review database connection request" }));
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("unchanged permission focus revalidation preserves the uncertain request and pauses replay", async () => {
  let resolveIdentity!: (response: Response) => void;
  let resolveMembers!: (response: Response) => void;
  const { fetch } = setup((url, init) => {
    if (url === "/api/v1/me") return new Promise<Response>((done) => { resolveIdentity = done; });
    if (url.startsWith("/api/v1/project-members")) return new Promise<Response>((done) => { resolveMembers = done; });
    if (url.endsWith("/database-connections") && init.method === "POST") return Promise.reject(new TypeError("lost"));
  });
  await choose(); await screen.findByText(/No confirmed response/);
  const firstBody = fetch.mock.calls.find(([url, init]) => url.endsWith("/database-connections") && init.method === "POST")![1].body;
  // Make both authority reads stale, then trigger the actual focus lifecycle.
  await client.invalidateQueries({ queryKey: queryKeys.principal, refetchType: "none" });
  await client.invalidateQueries({ queryKey: queryKeys.allProjectMembers, refetchType: "none" });
  act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
  await waitFor(() => { expect(resolveIdentity).toBeTruthy(); expect(resolveMembers).toBeTruthy(); });
  expect((screen.getByRole("button", { name: "Retry identical connection request" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByText(/Retained request:/)).toBeTruthy();
  await act(async () => { resolveIdentity(json({ id: "actor", provider: "oidc", roles: [] })); resolveMembers(json({ items: [{ project_id: "project", user_id: "actor", role: "owner" }], count: 1, total: 1, limit: 100, offset: 0 })); });
  await waitFor(() => expect((screen.getByRole("button", { name: "Retry identical connection request" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Retry identical connection request" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([url, init]) => url.endsWith("/database-connections") && init.method === "POST")).toHaveLength(2));
  expect(fetch.mock.calls.filter(([url, init]) => url.endsWith("/database-connections") && init.method === "POST")[1]![1].body).toBe(firstBody);
});
test("accepted response waits for in-flight membership authority and cannot outlive denial", async () => {
  let resolveMutation!: (response: Response) => void;
  let resolveAccess!: (response: Response) => void;
  setup((url, init) => {
    if (url.endsWith("/database-connections") && init.method === "POST") return new Promise<Response>((done) => { resolveMutation = done; });
    if (url.startsWith("/api/v1/project-members")) return new Promise<Response>((done) => { resolveAccess = done; });
  });
  await choose(); await waitFor(() => expect(resolveMutation).toBeTruthy());
  const access = client.refetchQueries({ queryKey: queryKeys.allProjectMembers });
  await waitFor(() => expect(resolveAccess).toBeTruthy());
  await act(async () => resolveMutation(json(connection, 202)));
  expect(screen.queryByText(/Request accepted/)).toBeNull();
  expect(screen.getByText(/Retained request:/)).toBeTruthy();
  await act(async () => { resolveAccess(json({}, 403)); await access; });
  await screen.findByText(/Database connection permission is unavailable/);
  expect(screen.queryByText(/Request accepted/)).toBeNull();
  expect(screen.queryByText(/Retained request:/)).toBeNull();
});

test("cache-level owner revocation blocks success before deferred observers render", async () => {
  let resolve!: (response: Response) => void;
  setup((url, init) => url.endsWith("/database-connections") && init.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  await choose(); await waitFor(() => expect(resolve).toBeTruthy());
  const notifications: (() => void)[] = [];
  notifyManager.setScheduler((callback) => { notifications.push(callback); });
  try {
    await act(async () => {
      client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "actor", role: "viewer" }]);
      resolve(json(connection, 202));
    });
    // The old session is still mounted here. Cache authority must already win.
    expect(screen.queryByText(/Request accepted/)).toBeNull();
  } finally {
    notifyManager.setScheduler((callback) => { setTimeout(callback, 0); });
    act(() => { for (const notify of notifications) notify(); });
  }
  await waitFor(() => expect(screen.queryByRole("form", { name: "Review database connection request" })).toBeNull());
});
