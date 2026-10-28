import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { ResourceRuntimeConfiguration } from "./ResourceRuntimeConfiguration";
const resource: Resource = { id: "r", project_id: "p", project_environment_id: "e", name: "Web", kind: "application", generation: 2, source: { kind: "image", image: { reference: "app:1" } }, created_by: "u", created_at: "", updated_at: "" };
const initial = { resource_id: "r", project_id: "p", resource_generation: 2, runtime_generation: 0, entries: [] };
const principal = { id: "u", provider: "oidc", roles: [] };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const page = (items: unknown[]) => ({ items, limit: 100, offset: 0, count: items.length, total: items.length });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function backend(role = "owner", handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  let saved = initial;
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const override = await handler?.(url, options); if (override) return override;
    if (url.includes("/project-members")) return json(page([{ project_id: "p", user_id: "u", role }]));
    if (url.endsWith("/runtime-configuration")) {
      if (options.method === "PUT") { const body = JSON.parse(options.body as string); saved = { ...initial, runtime_generation: body.expected_runtime_generation + 1, entries: body.entries }; }
      return json({ runtime_configuration: saved });
    }
    if (url.includes("/managed-secrets/s/versions")) return json(page([{ secret_id: "s", version: 3 }]));
    if (url.includes("/managed-secrets?")) return json(page([{ id: "s", project_id: "p", name: "Database password", status: "active", current_version: 3 }]));
    throw new Error(`Unexpected endpoint ${url}`);
  });
  vi.stubGlobal("fetch", fetch); return fetch;
}
function setup() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, principal);
  return render(<QueryClientProvider client={client}><ResourceRuntimeConfiguration resource={resource} blocked={false} /></QueryClientProvider>);
}
async function add(name = "VISIBLE", value = "  viewer readable  ") {
  fireEvent.click(await screen.findByRole("button", { name: "Add runtime variable" }));
  fireEvent.change(screen.getByLabelText("Variable name 1"), { target: { value: name } });
  fireEvent.change(screen.getByLabelText("Nonsecret value 1"), { target: { value } });
}
const save = () => fireEvent.submit(screen.getByRole("form", { name: "Save runtime variables" }));
test("owner saves an explicitly nonsecret value with current CAS and a cryptographic request ID", async () => {
  const fetch = backend(); setup(); await add(); save(); await screen.findByText("Saved runtime generation 1 · resource generation 2");
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "PUT"); expect(writes).toHaveLength(1);
  expect(JSON.parse(writes[0]![1].body as string)).toEqual({ expected_resource_generation: 2, expected_runtime_generation: 0, request_id: expect.stringMatching(/^[a-f0-9]{32}$/), entries: [{ name: "VISIBLE", kind: "literal", value: "  viewer readable  " }] });
  expect(screen.getByText(/Configured values are not evidence/)).toBeTruthy();
});
test.each(["viewer", "maintainer"])("%s reads saved metadata but cannot save runtime variables", async (role) => {
  const fetch = backend(role); setup(); await screen.findByText("No saved runtime variables."); expect(screen.queryByRole("form")).toBeNull(); expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(false);
});
test("secret selection submits only exact existing metadata and never calls reveal", async () => {
  const fetch = backend(); setup(); await add("PASSWORD", "");
  fireEvent.change(screen.getByLabelText("Value type 1"), { target: { value: "managed_secret" } });
  await screen.findByRole("option", { name: "Database password · s" });
  fireEvent.change(screen.getByLabelText("Existing managed secret for PASSWORD"), { target: { value: "s" } });
  await screen.findByRole("option", { name: "3" }); fireEvent.change(screen.getByLabelText("Exact secret version for PASSWORD"), { target: { value: "3" } });
  save(); await screen.findByText("Saved runtime generation 1 · resource generation 2");
  const body = JSON.parse(fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body as string);
  expect(body.entries).toEqual([{ name: "PASSWORD", kind: "managed_secret", secret: { secret_id: "s", version: 3 } }]);
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false); expect(body.entries[0]).not.toHaveProperty("value");
});
test.each(["lost", "503"])("%s retains byte-identical save body/key on explicit retry", async (failure) => {
  let count = 0; const fetch = backend("owner", (_url, options) => options.method === "PUT" && ++count === 1 ? failure === "lost" ? Promise.reject(new TypeError("lost")) : json({}, 503) : undefined);
  setup(); await add(); save(); await screen.findByText(/Save delivery is unconfirmed/);
  expect(screen.getByLabelText<HTMLInputElement>("Nonsecret value 1").matches(":disabled")).toBe(true);
  save(); await screen.findByText("Saved runtime generation 1 · resource generation 2");
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "PUT"); expect(writes).toHaveLength(2); expect(writes[1]![1].body).toBe(writes[0]![1].body);
});
test("validation failure preserves editable nonsecret draft without exposing server text", async () => {
  const fetch = backend("owner", (_url, options) => options.method === "PUT" ? json({ error: "unsafe server text" }, 400) : undefined);
  setup(); await add(); save(); await screen.findByText(/The server rejected this configuration/);
  expect(screen.getByLabelText<HTMLInputElement>("Nonsecret value 1").value).toBe("  viewer readable  "); expect(screen.getByLabelText<HTMLInputElement>("Nonsecret value 1").matches(":disabled")).toBe(false); expect(screen.queryByText(/unsafe server text/)).toBeNull();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(1);
});
test("409 requires explicit refreshed generation review before a separate save", async () => {
  let count = 0; const fetch = backend("owner", (_url, options) => options.method === "PUT" && ++count === 1 ? json({}, 409) : count && options.method !== "PUT" && _url.endsWith("/runtime-configuration") ? json({ runtime_configuration: { ...initial, resource_generation: 4, runtime_generation: 2 } }) : undefined);
  setup(); await add(); save(); await screen.findByText(/Runtime or resource generation changed/); save();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Refresh runtime configuration for review" })); await screen.findByText(/Current runtime generation 2/);
  fireEvent.click(screen.getByRole("button", { name: "Keep draft with reviewed generations for a separate save" })); save();
  await waitFor(() => expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(2));
  const bodies = fetch.mock.calls.filter(([, options]) => options.method === "PUT").map(([, options]) => JSON.parse(options.body as string)); expect(bodies[1]).toMatchObject({ expected_resource_generation: 4, expected_runtime_generation: 2 }); expect(bodies[1].request_id).not.toBe(bodies[0].request_id);
});
test.each(["NEROCD_PRIVATE", "bad-name", "é"])("rejects invalid/reserved name %s before save", async (name) => {
  const fetch = backend(); setup(); await add(name); save(); await screen.findByRole("alert"); expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(0);
});
test("duplicate submit sends once and actor loss aborts and discards late response", async () => {
  let resolve!: (value: Response) => void; const fetch = backend("owner", (_url, options) => options.method === "PUT" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  setup(); await add(); save(); save(); await waitFor(() => expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(1));
  const options = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1];
  await act(async () => { client.setQueryData(queryKeys.principal, { ...principal, id: "other" }); });
  await waitFor(() => expect(options.signal?.aborted).toBe(true)); await act(async () => resolve(json({ runtime_configuration: { ...initial, runtime_generation: 1 } })));
  expect(screen.queryByLabelText("Nonsecret value 1")).toBeNull(); expect(screen.queryByText(/Retained save request/)).toBeNull();
});
test("membership refresh pending clears draft, request and metadata instead of trusting cached owner", async () => {
  let pending = false; let resolve!: (value: Response) => void;
  backend("owner", (url) => pending && url.includes("/project-members") ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  setup(); await add(); pending = true; fireEvent.click(screen.getByRole("button", { name: "Refresh runtime configuration access" }));
  await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
  await act(async () => resolve(json(page([{ project_id: "p", user_id: "u", role: "viewer" }]))));
  await screen.findByText("Project owner access is required to save runtime variables."); expect(screen.queryByLabelText("Nonsecret value 1")).toBeNull();
});

test("refreshing saved metadata preserves an uncertain save until explicit separate review", async () => {
  const fetch = backend("owner", (_url, options) => options.method === "PUT" ? Promise.reject(new TypeError("lost")) : undefined); setup(); await add(); save(); await screen.findByText(/Save delivery is unconfirmed/);
  const original = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1].body;
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved runtime configuration" })); await screen.findByText("No saved runtime variables.");
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical runtime save" }).disabled).toBe(false)); save();
  await waitFor(() => expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")).toHaveLength(2)); expect(fetch.mock.calls.filter(([, options]) => options.method === "PUT")[1]![1].body).toBe(original);
});
test("server access denial discards runtime draft and retained attempt", async () => {
  backend("owner", (_url, options) => options.method === "PUT" ? json({}, 403) : undefined); setup(); await add(); save(); await screen.findByText(/Runtime configuration access is unavailable/); expect(screen.queryByRole("form")).toBeNull(); expect(screen.queryByText(/Retained save request/)).toBeNull();
});
test("literal byte limit is checked as UTF-8 and line breaks are rejected", async () => {
  const fetch = backend(); setup(); await add("VISIBLE", "é".repeat(2049)); save(); await screen.findByText(/4096 UTF-8 bytes/);
  fireEvent.change(screen.getByLabelText("Nonsecret value 1"), { target: { value: "valid" } });
  fireEvent.change(screen.getByLabelText("Variable name 1"), { target: { value: "bad name" } }); save(); await screen.findByText(/unique ASCII variable name/); expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(false);
});

test("duplicate variable names cannot submit and the 64-variable limit stops additions", async () => {
  const fetch = backend(); setup(); await add("DUPLICATE");
  const addVariable = screen.getByRole<HTMLButtonElement>("button", { name: "Add runtime variable" });
  fireEvent.click(addVariable); fireEvent.change(screen.getByLabelText("Variable name 2"), { target: { value: "DUPLICATE" } }); save(); await screen.findByText("Runtime variable names must be unique.");
  for (let index = 2; index < 63; index++) fireEvent.click(addVariable);
  expect(addVariable.disabled).toBe(false);
  fireEvent.click(addVariable);
  expect(addVariable.disabled).toBe(true);
  fireEvent.click(addVariable);
  expect(screen.getAllByRole("group")).toHaveLength(64); expect(fetch.mock.calls.some(([, options]) => options.method === "PUT")).toBe(false);
});
test.each(["project", "resource", "blocked"])("%s scope loss aborts pending runtime save and clears metadata and draft", async (scope) => {
  let resolve!: (value: Response) => void; const fetch = backend("owner", (_url, options) => options.method === "PUT" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  const view = setup(); await add(); save(); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  const input = fetch.mock.calls.find(([, options]) => options.method === "PUT")![1];
  view.rerender(<QueryClientProvider client={client}><ResourceRuntimeConfiguration resource={scope === "project" ? { ...resource, project_id: "other" } : scope === "resource" ? { ...resource, id: "other" } : resource} blocked={scope === "blocked"} /></QueryClientProvider>);
  expect(input.signal?.aborted).toBe(true); await act(async () => resolve(json({ runtime_configuration: { ...initial, runtime_generation: 1 } })));
  expect(screen.queryByLabelText("Nonsecret value 1")).toBeNull(); expect(screen.queryByText(/Retained save request/)).toBeNull();
});
