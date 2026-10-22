import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiError } from "@/api";
import * as api from "@/api/hosting";
import { ProjectHosting } from "./ProjectHosting";

vi.mock("@/api/hosting", () => ({ listProjectEnvironments: vi.fn(), listResources: vi.fn(), createProjectEnvironment: vi.fn(), createResource: vi.fn(), getResource: vi.fn(), updateResource: vi.fn() }));
const projects = ["one", "two"].map((id) => ({ id, name: `Project ${id}`, description: "", created_at: "2026-01-01T00:00:00Z" }));
const environment = { id: "env", project_id: "one", name: "Production", created_at: "2026-01-01T00:00:00Z" };
const resource: api.Resource = { id: "app", project_id: "one", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "team/app:1" } }, generation: 1, created_by: "user", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
const page = <T,>(items: T[], offset = 0, total = items.length) => ({ items, limit: 20, offset, count: items.length, total });
let clients: QueryClient[] = [];
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.listProjectEnvironments).mockResolvedValue(page([environment]));
  vi.mocked(api.listResources).mockResolvedValue(page([resource]));
});
afterEach(() => { cleanup(); for (const client of clients) client.clear(); clients = []; });
function setup(writable = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  const view = render(<QueryClientProvider client={client}><ProjectHosting projects={projects} canOperateProject={() => writable} /></QueryClientProvider>);
  fireEvent.change(screen.getByLabelText("Hosting project"), { target: { value: "one" } });
  return { client, ...view };
}
async function selectEnvironment() { fireEvent.click(await screen.findByRole("button", { name: "Production" })); await screen.findByRole("heading", { name: "Web" }); }
async function edit() { await selectEnvironment(); fireEvent.click(screen.getByRole("button", { name: "Edit configuration" })); return screen.getByRole("form", { name: "Edit Web" }); }

test("viewers read environments and saved application configuration without write controls", async () => {
  setup(false); await selectEnvironment();
  expect(screen.getByText("Saved configuration · Generation 1")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Create shared environment" })).toBeNull();
  expect(screen.queryByRole("form", { name: "Create application" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Edit configuration" })).toBeNull();
  expect(api.createResource).not.toHaveBeenCalled();
});

test("create environment selects the returned environment and saves an image application", async () => {
  setup(); await screen.findByRole("button", { name: "Production" });
  vi.mocked(api.createProjectEnvironment).mockResolvedValue({ ...environment, id: "staging", name: "Staging" });
  fireEvent.change(screen.getByLabelText("Environment name"), { target: { value: "Staging" } });
  fireEvent.click(screen.getByRole("button", { name: "Create shared environment" }));
  const form = await screen.findByRole("form", { name: "Create application" });
  expect(api.createProjectEnvironment).toHaveBeenCalledWith({ project_id: "one", name: "Staging" });
  vi.mocked(api.createResource).mockResolvedValue(resource);
  fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "API" } });
  fireEvent.change(within(form).getByLabelText("Image reference"), { target: { value: "registry.example.com/api:2" } });
  fireEvent.submit(form);
  await screen.findByText("Application configuration saved. It has not been deployed.");
  expect(api.createResource).toHaveBeenCalledWith({ project_id: "one", project_environment_id: "staging", name: "API", kind: "application", source: { kind: "image", image: { reference: "registry.example.com/api:2" } } });
  expect(within(form).getByLabelText<HTMLInputElement>("Application name").value).toBe("");
});

test.each([400, 401, 403, 404, 409, 500])("create failure %s keeps the submitted draft and exposes request identity", async (status) => {
  setup(); await selectEnvironment();
  vi.mocked(api.createResource).mockRejectedValue(new ApiError(status, "save refused", "req-hosting"));
  const form = screen.getByRole("form", { name: "Create application" });
  fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Draft" } });
  fireEvent.change(within(form).getByLabelText("Image reference"), { target: { value: "team/app:2" } });
  fireEvent.submit(form);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("req-hosting"));
  expect(within(form).getByLabelText<HTMLInputElement>("Application name").value).toBe("Draft");
  expect(within(form).getByLabelText<HTMLInputElement>("Image reference").value).toBe("team/app:2");
});

test("environment failure preserves its draft until explicit retry", async () => {
  const { client } = setup(); await screen.findByRole("button", { name: "Production" });
  fireEvent.change(screen.getByLabelText("Environment name"), { target: { value: "Draft environment" } });
  vi.mocked(api.listProjectEnvironments).mockRejectedValueOnce(new Error("offline"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["projectEnvironments", "one"] }); });
  await screen.findByText("Unable to load shared environments. Your draft is preserved.");
  expect(screen.getByLabelText<HTMLInputElement>("Environment name").value).toBe("Draft environment");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Create shared environment" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Retry environments" }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Create shared environment" }).disabled).toBe(false));
});

test("environment creation failure preserves its name for retry", async () => {
  setup(); await screen.findByRole("button", { name: "Production" });
  vi.mocked(api.createProjectEnvironment).mockRejectedValue(new ApiError(409, "environment name already exists", "req-env"));
  fireEvent.change(screen.getByLabelText("Environment name"), { target: { value: "Draft environment" } });
  fireEvent.click(screen.getByRole("button", { name: "Create shared environment" }));
  await screen.findByText(/environment name already exists/);
  expect(screen.getByLabelText<HTMLInputElement>("Environment name").value).toBe("Draft environment");
});

test("application list refresh failure preserves create and edit drafts while disabling writes", async () => {
  const { client } = setup(); const editor = await edit();
  const create = screen.getByRole("form", { name: "Create application" });
  fireEvent.change(within(create).getByLabelText("Application name"), { target: { value: "Create draft" } });
  fireEvent.change(within(editor).getByLabelText("Application name"), { target: { value: "Edit draft" } });
  vi.mocked(api.listResources).mockRejectedValueOnce(new Error("offline"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["hostingResources", "one"] }); });
  await screen.findByText("Unable to load application configurations. Your draft is preserved.");
  expect(within(create).getByLabelText<HTMLInputElement>("Application name").value).toBe("Create draft");
  expect(within(editor).getByLabelText<HTMLInputElement>("Application name").value).toBe("Edit draft");
  expect(within(editor).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
  expect(within(create).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Retry applications" }));
  await waitFor(() => expect(within(editor).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(false));
  // A create must not move pagination out from under the active edit.
  expect(within(create).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
});

test("validation failure keeps edit fields and unchanged drafts never patch", async () => {
  setup(); const editor = await edit();
  expect(within(editor).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
  fireEvent.submit(editor);
  expect(api.updateResource).not.toHaveBeenCalled();
  fireEvent.change(within(editor).getByLabelText("Image reference"), { target: { value: "invalid" } });
  vi.mocked(api.updateResource).mockRejectedValue(new ApiError(400, "explicit tag required", "req-edit"));
  fireEvent.submit(editor);
  await screen.findByText(/explicit tag required/);
  expect(within(editor).getByLabelText<HTMLInputElement>("Image reference").value).toBe("invalid");
  expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 1, source: { kind: "image", image: { reference: "invalid" } } });
});

test("409 recovery keeps draft, requires review, and uses the latest generation only after confirmation", async () => {
  setup(); const form = await edit();
  fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "My draft" } });
  vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "generation mismatch", null)).mockResolvedValueOnce({ ...resource, name: "My draft", generation: 3 });
  vi.mocked(api.getResource).mockResolvedValue({ ...resource, name: "Other edit", generation: 2 });
  fireEvent.submit(form);
  await screen.findByText("Other edit");
  expect(within(form).getByLabelText<HTMLInputElement>("Application name").value).toBe("My draft");
  expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
  expect(api.updateResource).toHaveBeenCalledTimes(1);
  expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 1, name: "My draft" });
  fireEvent.click(within(form).getByRole("button", { name: "Keep my draft with generation 2" }));
  expect(api.updateResource).toHaveBeenCalledTimes(1);
  fireEvent.submit(form);
  await screen.findByText("Application configuration updated. It has not been deployed.");
  expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 2, name: "My draft" });
});

test("conflict refetch failure blocks save and permits retry without losing draft", async () => {
  setup(); const form = await edit();
  fireEvent.change(within(form).getByLabelText("Image reference"), { target: { value: "team/app:3" } });
  vi.mocked(api.updateResource).mockRejectedValue(new ApiError(409, "stale", null));
  vi.mocked(api.getResource).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ ...resource, generation: 4 });
  fireEvent.submit(form);
  await screen.findByText(/Unable to load current configuration/);
  expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Reload current configuration" }));
  await screen.findByRole("button", { name: "Keep my draft with generation 4" });
  expect(within(form).getByLabelText<HTMLInputElement>("Image reference").value).toBe("team/app:3");
});

test("bounded environment and application pagination includes project and environment filters", async () => {
  vi.mocked(api.listProjectEnvironments).mockImplementation(async ({ offset = 0 }) => page([environment], offset, 41));
  vi.mocked(api.listResources).mockImplementation(async ({ offset = 0 }) => page([resource], offset, 41));
  setup(); await selectEnvironment();
  fireEvent.click(screen.getByRole("button", { name: "Next shared environments" }));
  await waitFor(() => expect(api.listProjectEnvironments).toHaveBeenCalledWith({ project_id: "one", limit: 20, offset: 20 }, expect.objectContaining({ signal: expect.any(AbortSignal) })));
  fireEvent.click(screen.getByRole("button", { name: "Next applications" }));
  await waitFor(() => expect(api.listResources).toHaveBeenCalledWith({ project_id: "one", project_environment_id: "env", limit: 20, offset: 20 }, expect.objectContaining({ signal: expect.any(AbortSignal) })));
});

test("late environment responses cannot leak the previous project's form or selection", async () => {
  let resolve!: (value: ReturnType<typeof page<api.ProjectEnvironment>>) => void;
  vi.mocked(api.listProjectEnvironments).mockImplementation(({ project_id }) => project_id === "one" ? new Promise((done) => { resolve = done; }) : Promise.resolve(page([{ ...environment, id: "two-env", project_id: "two", name: "Second environment" }])));
  setup();
  fireEvent.change(screen.getByLabelText("Hosting project"), { target: { value: "two" } });
  await screen.findByRole("button", { name: "Second environment" });
  await act(async () => resolve(page([environment])));
  expect(screen.queryByRole("button", { name: "Production" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Second environment" }));
  await waitFor(() => expect(api.listResources).toHaveBeenCalledWith({ project_id: "two", project_environment_id: "two-env", limit: 20, offset: 0 }, expect.anything()));
});
