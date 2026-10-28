vi.mock("./HostingCatalog", () => ({ HostingCatalog: () => null }));
import { request } from "@/api/client";
vi.mock("@/api/client", () => ({ request: vi.fn() }));
import { useDatabaseAccess } from "./ResourceDatabase";
import * as databaseAPI from "@/api/hosting-databases";
import * as backupAPI from "@/api/hosting-database-backups";
import * as transferAPI from "@/api/hosting-database-backup-targets";
vi.mock("@/api/hosting-databases", async original => ({ ...await original<typeof databaseAPI>(), getHostingDatabase: vi.fn() }));
vi.mock("@/api/hosting-database-backups", async original => ({ ...await original<typeof backupAPI>(), listDatabaseBackups: vi.fn(), requestDatabaseBackup: vi.fn(), restoreDatabaseBackup: vi.fn() }));
vi.mock("@/api/hosting-database-backup-targets", async original => ({ ...await original<typeof transferAPI>(), listDatabaseBackupTransfers: vi.fn(), listDatabaseBackupTargets: vi.fn(async () => []), retryDatabaseBackupUpload: vi.fn() }));
vi.mock("@/api/hosting-database-backup-schedules", async original => ({ ...await original<typeof import("@/api/hosting-database-backup-schedules")>(), listDatabaseBackupSchedules: vi.fn(async () => []) }));
vi.mock("@/api/hosting-database-backup-retention", async original => ({ ...await original<typeof import("@/api/hosting-database-backup-retention")>(), getRetentionPolicy: vi.fn(async () => null), listRetentionExecutions: vi.fn(async () => []) }));
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiError, queryKeys } from "@/api";
import * as api from "@/api/hosting";
import { ProjectHosting } from "./ProjectHosting";
vi.mock("./DatabaseCreateForm", () => ({ DatabaseCreateForm: ({ projectID, environmentID }: { projectID: string; environmentID: string }) => <div>Database creation for {projectID}/{environmentID}</div> }));
vi.mock("./ResourceDatabase", () => ({ ResourceDatabase: ({ resource }: { resource: api.Resource }) => <div>Dedicated database metadata for {resource.name}</div>, useDatabaseAccess: vi.fn() }));

vi.mock("./ResourceRuntimeStatus", () => ({ ResourceRuntimeStatus: ({ resource, blocked }: { resource: { name: string }; blocked: boolean }) => <div data-testid="runtime-status" data-blocked={String(blocked)}>Runtime status for {resource.name}</div> }));
vi.mock("./ResourceRuntimeLogs", () => ({ ResourceRuntimeLogs: ({ resource, blocked }: { resource: { name: string }; blocked: boolean }) => <div data-testid="runtime-logs" data-blocked={String(blocked)}>Runtime logs for {resource.name}</div> }));
vi.mock("./ResourceRuntimeConfiguration", () => ({ ResourceRuntimeConfiguration: ({ resource }: { resource: { name: string } }) => <div>Runtime variables for {resource.name}</div> }));
vi.mock("./ResourceBuilds", () => ({ ResourceBuilds: ({ resource }: { resource: { name: string } }) => <div>Build controls for {resource.name}</div> }));
vi.mock("./ResourceRoutes", () => ({ ResourceRoutes: ({ resource }: { resource: { name: string } }) => <div>Resource routes for {resource.name}</div> }));
vi.mock("./ResourceDestination", () => ({ ResourceDestination: ({ resource }: { resource: { name: string } }) => <div>Desired destination for {resource.name}</div> }));
vi.mock("./ResourceDeployments", () => ({ ResourceDeployments: ({ resource }: { resource: { name: string } }) => <div>Image deployments for {resource.name}</div> }));

vi.mock("@/api/hosting", () => ({ listProjectEnvironments: vi.fn(), listResources: vi.fn(), createProjectEnvironment: vi.fn(), createResource: vi.fn(), getResource: vi.fn(), updateResource: vi.fn() }));
const projects = ["one", "two"].map((id) => ({ id, name: `Project ${id}`, description: "", created_at: "2026-01-01T00:00:00Z" }));
const environment = { id: "env", project_id: "one", name: "Production", created_at: "2026-01-01T00:00:00Z" };
const resource: api.Resource = { id: "app", project_id: "one", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "team/app:1" } }, generation: 1, created_by: "user", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
const page = <T,>(items: T[], offset = 0, total = items.length) => ({ items, limit: 20, offset, count: items.length, total });
let clients: QueryClient[] = [];
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(useDatabaseAccess).mockImplementation((project, environment) => ({
    ready: false, refreshing: true, viewer: false, owner: false, maintainer: false,
    scope: { actor: "", project, environment }, epoch: JSON.stringify(["pending", project, environment]), error: undefined,
  }));
  vi.mocked(api.listProjectEnvironments).mockResolvedValue(page([environment]));
  vi.mocked(api.listResources).mockResolvedValue(page([resource]));
});
afterEach(() => { cleanup(); for (const client of clients) client.clear(); clients = []; });
function setup(writable = true, unavailable = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles: [] });
  const view = render(<QueryClientProvider client={client}><ProjectHosting projects={projects} canOperateProject={() => writable} unavailable={unavailable} /></QueryClientProvider>);
  fireEvent.change(screen.getByLabelText("Hosting project"), { target: { value: "one" } });
  return { client, ...view };
}
async function selectEnvironment() { fireEvent.click(await screen.findByRole("button", { name: "Production" })); await screen.findByRole("heading", { name: "Web" }); }
async function edit() { await selectEnvironment(); fireEvent.click(screen.getByRole("button", { name: "Edit configuration" })); return screen.getByRole("form", { name: "Edit Web" }); }
test("mixed resources branch databases before every generic application panel and editor", async () => {
  vi.mocked(api.listResources).mockResolvedValue(page([resource, { ...resource, id: "db", name: "Database", kind: "database", source: { kind: "image", image: { reference: "database-only-source" } } }]));
  setup(); await selectEnvironment();
  expect(screen.getByText("Dedicated database metadata for Database")).toBeTruthy();
  expect(screen.getByText("Database creation for one/env")).toBeTruthy();
  expect(screen.queryByText("database-only-source")).toBeNull();
  for (const label of ["Desired destination", "Build controls", "Runtime status", "Runtime logs", "Runtime variables", "Image deployments", "Resource routes"]) expect(screen.queryByText(`${label} for Database`)).toBeNull();
  expect(screen.queryByRole("region", { name: "Persistent storage for Database" })).toBeNull();
  expect(screen.getAllByRole("button", { name: "Edit configuration" })).toHaveLength(1);
});

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
  await screen.findByText("Application configuration saved. Deployment is a separate request.");
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
  expect(await within(form).findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("req-hosting"));
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
  await screen.findByText("Application configuration updated. Deployment is a separate request.");
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

const gitResource: api.Resource = { ...resource, source: { kind: "git", git: { source_id: "source", generation: 2, repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile" } } } };
test("Git create and edit forms describe separately requested builds alongside build controls", async () => {
 vi.mocked(api.listResources).mockResolvedValue(page([gitResource]));
 setup(); const editor = await edit();
 const create = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(create).getByLabelText("Application source"), { target: { value: "git" } });
 expect(screen.getByText("Build controls for Web")).toBeTruthy();
 for (const form of [create, editor]) {
  expect(within(form).getByText(/Saving records configuration only\. Request builds separately when enabled by your operator\. Select a successful build to request deployment of its recorded artifact\./)).toBeTruthy();
  expect(within(form).queryByText(/Git builds and deployment are unavailable|build controller is connected/)).toBeNull();
 }
});
test("Git creation sends public configuration without source identity and preserves rejected drafts", async () => {
 setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 expect(within(form).queryByLabelText("Image reference")).toBeNull();
 fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Git web" } });
 fireEvent.change(within(form).getByLabelText("Git repository URL"), { target: { value: "https://github.com/acme/web" } });
 fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: "static" } });
 fireEvent.change(within(form).getByLabelText("Publish directory"), { target: { value: "public" } });
 fireEvent.click(within(form).getByLabelText("SPA fallback"));
 vi.mocked(api.createResource).mockRejectedValue(new ApiError(503, "unavailable", "git-request"));
 fireEvent.submit(form); await screen.findByRole("alert");
 expect(api.createResource).toHaveBeenCalledWith({ project_id: "one", project_environment_id: "env", name: "Git web", kind: "application", source: { kind: "git", git: { repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "static", publish_dir: "public", spa_fallback: true } } } });
 expect(within(form).getByLabelText<HTMLInputElement>("Git repository URL").value).toBe("https://github.com/acme/web");
});
test("Git conflict exposes changed saved build settings while retaining the original draft until explicit save", async () => {
 const original: api.Resource = { ...gitResource, source: { kind: "git", git: { source_id: "source", generation: 2, repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile", context_dir: "old-context", dockerfile_path: "docker/Oldfile", build_target: "old-target" } } } };
 const current: api.Resource = { ...original, generation: 2, source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/acme/new-web", requested_ref: "new-ref", credential_kind: "public", settings: { strategy: "static", context_dir: "new-context", publish_dir: "new-public", spa_fallback: true } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([original]));
 setup(); const form = await edit();
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "draft" } });
 vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockResolvedValueOnce({ ...original, generation: 3 });
 vi.mocked(api.getResource).mockResolvedValue(current);
 fireEvent.submit(form); await screen.findByRole("button", { name: "Keep my draft with generation 2" });
 const review = within(form).getByLabelText("Current Git configuration");
 for (const [label, value] of [["Current repository URL", "https://github.com/acme/new-web"], ["Current requested ref", "new-ref"], ["Current credential kind", "public"], ["Current source ID", "source"], ["Current source generation", "3"], ["Current build strategy", "static"], ["Current context directory", "new-context"], ["Current Dockerfile path", "Not applicable"], ["Current build target", "Not applicable"], ["Current publish directory", "new-public"], ["Current SPA fallback", "Enabled"]]) {
  expect(within(review).getByText(label!, { exact: true }).nextElementSibling?.textContent).toBe(value);
 }
 expect(within(form).getByLabelText<HTMLInputElement>("Git branch, tag, or commit").value).toBe("draft");
 expect(within(form).getByLabelText<HTMLSelectElement>("Build strategy").value).toBe("dockerfile");
 expect(within(form).getByLabelText<HTMLInputElement>("Context directory").value).toBe("old-context");
 expect(within(form).getByLabelText<HTMLInputElement>("Dockerfile path").value).toBe("docker/Oldfile");
 expect(within(form).getByLabelText<HTMLInputElement>("Build target").value).toBe("old-target");
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
 expect(api.updateResource).toHaveBeenCalledTimes(1);
 fireEvent.click(screen.getByRole("button", { name: "Keep my draft with generation 2" }));
 expect(api.updateResource).toHaveBeenCalledTimes(1);
 expect(within(form).getByLabelText<HTMLSelectElement>("Build strategy").value).toBe("dockerfile");
 expect(within(form).getByLabelText<HTMLInputElement>("Context directory").value).toBe("old-context");
 fireEvent.submit(form); await screen.findByText("Application configuration updated. Deployment is a separate request.");
 expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 2, source: { kind: "git", git: { repository_url: "https://github.com/acme/web", requested_ref: "draft", credential_kind: "public", settings: { strategy: "dockerfile", context_dir: "old-context", dockerfile_path: "docker/Oldfile", build_target: "old-target" } } } });
});
test.each(["dockerfile", "railpack", "static"] as const)("Git conflict labels absent settings explicitly for %s", async (strategy) => {
 vi.mocked(api.listResources).mockResolvedValue(page([gitResource]));
 setup(); const form = await edit();
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "draft" } });
 vi.mocked(api.updateResource).mockRejectedValue(new ApiError(409, "stale", null));
 vi.mocked(api.getResource).mockResolvedValue({ ...gitResource, generation: 2, source: { kind: "git", git: { source_id: "source", generation: 3, repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy } } } });
 fireEvent.submit(form);
 const review = await screen.findByLabelText("Current Git configuration");
 for (const [label, value] of [["Current context directory", "Repository root (default)"], ["Current Dockerfile path", strategy === "dockerfile" ? "Dockerfile (default)" : "Not applicable"], ["Current build target", strategy === "dockerfile" ? "Final stage (default)" : "Not applicable"], ["Current publish directory", strategy === "static" ? "Selected context root (default)" : "Not applicable"], ["Current SPA fallback", "Disabled (default)"]]) {
  expect(within(review).getByText(label!, { exact: true }).nextElementSibling?.textContent).toBe(value);
 }
});
test("viewer reads Git generations without source mutation controls", async () => {
 vi.mocked(api.listResources).mockResolvedValue(page([gitResource]));
 setup(false); await selectEnvironment();
 expect(screen.getByText(/Git source generation 2/)).toBeTruthy();
 expect(screen.queryByRole("button", { name: "Edit configuration" })).toBeNull();
 expect(screen.queryByRole("form", { name: "Create application" })).toBeNull();
});

test("private Git editor preserves credential kind and sends only a rename when source is unchanged", async () => {
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...(gitResource.source.kind === "git" ? gitResource.source.git : {}), credential_kind: "https_basic" } } } as api.Resource;
 vi.mocked(api.listResources).mockResolvedValue(page([saved])); vi.mocked(api.updateResource).mockResolvedValue({ ...saved, name: "Renamed" }); setup(); const form = await edit();
 expect(within(form).getByLabelText<HTMLSelectElement>("Git source access").value).toBe("https_basic"); expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
 fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Renamed" } }); fireEvent.submit(form); await screen.findByText("Application configuration updated. Deployment is a separate request.");
 expect(api.updateResource).toHaveBeenCalledWith("app", { expected_generation: 1, name: "Renamed" });
});
test("private Git source edits retain access until an explicit public selection", async () => {
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...(gitResource.source.kind === "git" ? gitResource.source.git : {}), credential_kind: "https_basic" } } } as api.Resource;
 vi.mocked(api.listResources).mockResolvedValue(page([saved])); vi.mocked(api.updateResource).mockRejectedValue(new ApiError(400, "invalid", "request")); setup(); const form = await edit();
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "private-next" } }); fireEvent.submit(form); await screen.findByRole("alert");
 expect(vi.mocked(api.updateResource).mock.calls[0]![1]).toMatchObject({ source: { git: { credential_kind: "https_basic", requested_ref: "private-next" } } });
 fireEvent.change(within(form).getByLabelText("Git source access"), { target: { value: "public" } }); fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
 expect(vi.mocked(api.updateResource).mock.calls[1]![1]).toMatchObject({ source: { git: { credential_kind: "public", requested_ref: "private-next" } } });
});
test("runtime status remains independent of inherited legacy failures but resource errors block it", async () => {
  const { client } = setup(true, true); await selectEnvironment();
  expect(screen.getByTestId("runtime-status").getAttribute("data-blocked")).toBe("false");
  expect(screen.getByTestId("runtime-logs").getAttribute("data-blocked")).toBe("false");
  vi.mocked(api.listResources).mockRejectedValue(new Error("resource unavailable"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["hostingResources"] }); });
  await waitFor(() => expect(screen.getByTestId("runtime-status").getAttribute("data-blocked")).toBe("true"));
  expect(screen.getByTestId("runtime-logs").getAttribute("data-blocked")).toBe("true");
});
test("runtime status clears while the current resource is revalidated", async () => {
  const { client } = setup(); await selectEnvironment();
  expect(screen.getByTestId("runtime-status").getAttribute("data-blocked")).toBe("false");
  vi.mocked(api.listResources).mockImplementation(() => new Promise(() => {}));
  await act(async () => { void client.invalidateQueries({ queryKey: ["hostingResources"] }); });
  await waitFor(() => expect(screen.getByTestId("runtime-status").getAttribute("data-blocked")).toBe("true"));
});

const recoveryLocator = { project_id: "one", environment_id: "env", resource_id: "db", backup_id: "backup", transfer_id: "upload" };
const recoveryDatabase = {
  resource: { ...resource, id: "db", name: "Data", kind: "database", source: { kind: "image", image: { reference: "postgres:17" } } },
  binding: { id: "binding", resource_id: "db", destination_id: "runner", runtime_namespace: "namespace", created_by: "user", created_at: environment.created_at },
  storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, engine: "postgresql", recipe_version: "postgresql-17.11-bookworm.v1", runtime_state: "adapter_unqualified",
  credentials: [{ role: "admin", username: "postgres", secret_id: "admin_secret", version: 1 }, { role: "app", username: "ncd_app", secret_id: "app_secret", version: 1 }],
} as databaseAPI.HostingDatabase;
const recoveryBackup = { id: "backup", backup_id: "backup", project_id: "one", source_resource_id: "db", source_generation: 1, max_bytes: 1073741824, timeout_seconds: 900, version: "postgresql.logical-local.v1", mode: "backup", state: "succeeded", consistency: "logical_snapshot_source_writable", ownership_held: false, created_at: environment.created_at, updated_at: environment.created_at, blob: { bytes: 65537, digest: "sha256:" + "a".repeat(64), format: "postgresql_custom" } } as backupAPI.DatabaseBackup;
const recoveryTransfer = { id: "upload", version: "postgresql.logical-s3-transfer.v1", backup_id: "backup", project_id: "one", mode: "upload", state: "failed", failure_code: "remote_unavailable", destination_id: "runner", created_at: environment.created_at, updated_at: environment.created_at, target_id: "target", target_revision: 1, blob: recoveryBackup.blob } as transferAPI.DatabaseBackupTransfer;
function recoverySetup(owner = true, locator = recoveryLocator) {
  vi.mocked(useDatabaseAccess).mockReturnValue({ ready: true, viewer: true, owner, maintainer: owner, refreshing: false, scope: { actor: "current_member", project: "one", environment: "env" }, epoch: "current", error: undefined });
  vi.mocked(databaseAPI.getHostingDatabase).mockResolvedValue(recoveryDatabase);
  vi.mocked(backupAPI.listDatabaseBackups).mockResolvedValue([recoveryBackup]);
  vi.mocked(request).mockImplementation(async path => path.includes("/database-backup-transfers/") ? recoveryTransfer : recoveryBackup);
  vi.mocked(transferAPI.listDatabaseBackupTransfers).mockResolvedValue([{ id: "upload", backup_id: "backup", project_id: "one", mode: "upload", state: "failed", created_at: environment.created_at, updated_at: environment.created_at, target_id: "target", target_revision: 1 } as transferAPI.DatabaseBackupTransfer]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client);
  const element = () => <QueryClientProvider client={client}><ProjectHosting projects={projects} canOperateProject={() => owner} recoverySearch={locator} /></QueryClientProvider>;
  return { ...render(element()), element };
}
test("authenticated recovery resolves current membership and full relationships before rendering the real backup panel", async () => {
  recoverySetup();
  const linked = await screen.findByRole("listitem", { name: "Linked backup history" });
  expect(within(linked).getByText(/Backup · succeeded · backup/)).toBeTruthy();
  expect(screen.getByLabelText<HTMLSelectElement>("Completed backup").value).toBe("backup");
  expect(await screen.findByRole("button", { name: "Retry upload for backup" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Restore into new database" })).toBeTruthy();
  expect(databaseAPI.getHostingDatabase).toHaveBeenCalledWith("db", expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(backupAPI.requestDatabaseBackup).not.toHaveBeenCalled(); expect(backupAPI.restoreDatabaseBackup).not.toHaveBeenCalled(); expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});
test("recovery viewer sees linked history without retry/restore authority even when source actor is no longer present", async () => {
  recoverySetup(false);
  await screen.findByRole("listitem", { name: "Linked backup history" });
  expect(screen.queryByRole("button", { name: "Retry upload for backup" })).toBeNull(); expect(screen.queryByRole("button", { name: "Restore into new database" })).toBeNull();
});
test.each(["project", "environment", "backup", "transfer"])("recovery rejects a foreign %s relationship before any backup controls", async mismatch => {
  recoverySetup();
  if (mismatch === "project" || mismatch === "environment") vi.mocked(databaseAPI.getHostingDatabase).mockResolvedValue({ ...recoveryDatabase, resource: { ...recoveryDatabase.resource, [mismatch === "project" ? "project_id" : "project_environment_id"]: "foreign" } });
  if (mismatch === "backup") vi.mocked(request).mockResolvedValue({ ...recoveryBackup, id: "foreign", backup_id: "foreign" });
  if (mismatch === "transfer") vi.mocked(request).mockImplementation(async path => path.includes("/database-backup-transfers/") ? { ...recoveryTransfer, backup_id: "foreign" } : recoveryBackup);
  await screen.findByText(/Backup recovery is unavailable for this project/);
  expect(screen.queryByRole("button", { name: "Restore into new database" })).toBeNull(); expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});

test("exact recovery includes a retained backup beyond 100 newer operations without automatic mutation", async () => {
  recoverySetup();
  vi.mocked(backupAPI.listDatabaseBackups).mockResolvedValue(Array.from({ length: 100 }, (_, n) => ({ ...recoveryBackup, id: `newer_${n}`, backup_id: `newer_${n}` })));
  await screen.findByRole("listitem", { name: "Linked backup history" });
  expect(screen.getByLabelText<HTMLSelectElement>("Completed backup").value).toBe("backup");
  expect(request).toHaveBeenCalledWith("/api/v1/database-backups/backup", expect.objectContaining({ signal: expect.any(AbortSignal), cache: "no-store" }));
  expect(await screen.findByRole("button", { name: "Retry upload for backup" })).toBeTruthy();
  expect(backupAPI.restoreDatabaseBackup).not.toHaveBeenCalled(); expect(backupAPI.requestDatabaseBackup).not.toHaveBeenCalled(); expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});
test.each(["project_id", "source_resource_id", "mode"] as const)("exact backup detail rejects foreign %s before controls", async key => {
  recoverySetup();
  vi.mocked(request).mockResolvedValue({ ...recoveryBackup, [key]: key === "mode" ? "restore" : "foreign" });
  await screen.findByText(/Backup recovery is unavailable for this project/);
  expect(screen.queryByRole("listitem", { name: "Linked backup history" })).toBeNull();
  expect(backupAPI.restoreDatabaseBackup).not.toHaveBeenCalled(); expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});
test("retiring recovery access aborts an exact lookup and never exposes the late result", async () => {
  const view = recoverySetup();
  let complete!: (value: unknown) => void;
  let signal: AbortSignal | undefined;
  vi.mocked(request).mockImplementation((_path, options) => { signal = options?.signal; return new Promise(resolve => { complete = resolve; }); });
  await waitFor(() => expect(signal).toBeTruthy());
  vi.mocked(useDatabaseAccess).mockReturnValue({ ready: true, viewer: false, owner: false, maintainer: false, refreshing: false, scope: { actor: "current_member", project: "one", environment: "env" }, epoch: "revoked", error: undefined });
  view.rerender(view.element());
  await waitFor(() => expect(signal?.aborted).toBe(true));
  await act(async () => { complete(recoveryBackup); });
  expect(screen.queryByRole("listitem", { name: "Linked backup history" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Restore into new database" })).toBeNull();
});

test("exact linked transfer remains visible beyond 100 newer uploads and does not retry automatically", async () => {
  recoverySetup();
  vi.mocked(transferAPI.listDatabaseBackupTransfers).mockResolvedValue(Array.from({ length: 100 }, (_, n) => ({ ...recoveryTransfer, id: `new_upload_${n}`, created_at: "2026-10-26T12:00:00Z" })));
  const linked = await screen.findByRole("listitem", { name: "Linked upload history" });
  expect(linked.textContent).toContain("Upload upload");
  expect(request).toHaveBeenCalledWith("/api/v1/database-backup-transfers/upload", expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});
test.each(["project_id", "backup_id", "mode"] as const)("exact transfer rejects foreign %s association", async key => {
  recoverySetup();
  vi.mocked(request).mockImplementation(async path => path.includes("/database-backup-transfers/") ? { ...recoveryTransfer, [key]: key === "mode" ? "download" : "foreign" } : recoveryBackup);
  await screen.findByText(/Backup recovery is unavailable for this project/);
  expect(screen.queryByRole("listitem", { name: "Linked backup history" })).toBeNull();
  expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});

test("owner to viewer transition retires recovery mutation controls while retaining authorized history", async () => {
  const view = recoverySetup();
  await screen.findByRole("button", { name: "Retry upload for backup" });
  vi.mocked(useDatabaseAccess).mockReturnValue({ ready: true, viewer: true, owner: false, maintainer: false, refreshing: false, scope: { actor: "current_member", project: "one", environment: "env" }, epoch: "viewer", error: undefined });
  view.rerender(view.element());
  expect(screen.queryByRole("button", { name: "Restore into new database" })).toBeNull();
  await screen.findByRole("listitem", { name: "Linked backup history" });
  expect(screen.queryByRole("button", { name: "Retry upload for backup" })).toBeNull();
  expect(backupAPI.restoreDatabaseBackup).not.toHaveBeenCalled(); expect(transferAPI.retryDatabaseBackupUpload).not.toHaveBeenCalled();
});

test("Compose creation preserves exact source and request identity after a lost response", async () => {
  setup(); await selectEnvironment();
  const form = screen.getByRole("form", { name: "Create application" });
  const original = "# keep original bytes\nservices:\n  app:\n    image: nginx:alpine\n";
  vi.mocked(api.createResource).mockRejectedValueOnce(new ApiError(503, "response unavailable", "req-compose"));
  fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "compose" } });
  fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Stack" } });
  fireEvent.change(within(form).getByLabelText("Compose stack"), { target: { value: original } });
  fireEvent.change(within(form).getByLabelText("Public component"), { target: { value: "app" } });
  fireEvent.change(within(form).getByLabelText("Public component container port"), { target: { value: "8080" } });
  expect(within(form).queryByLabelText("Repository URL")).toBeNull();
  fireEvent.submit(form);
  await screen.findByText(/req-compose/);
  expect(within(form).getByLabelText<HTMLTextAreaElement>("Compose stack").value).toBe(original);
  const first = vi.mocked(api.createResource).mock.calls[0]![0];
  expect(first).toMatchObject({ request_id: expect.stringMatching(/^[a-f0-9]{32}$/), source: { kind: "compose", compose: { original, public_component: "app", container_port: 8080 } } });
  vi.mocked(api.createResource).mockResolvedValueOnce({ ...resource, name: "Stack", source: {kind: "compose", compose: {original, public_component: "app", container_port: 8080}} });
  fireEvent.submit(form);
  await screen.findByText("Application configuration saved. Deployment is a separate request.");
  expect(vi.mocked(api.createResource).mock.calls[1]![0]).toEqual(first);
});


test("Git Compose creation saves repository settings without converting source or requesting deployment", async () => {
  setup(); await selectEnvironment();
  const form = screen.getByRole("form", { name: "Create application" });
  fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
  fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Git stack" } });
  fireEvent.change(within(form).getByLabelText("Git repository URL"), { target: { value: "https://github.com/team/app" } });
  fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "release" } });
  fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: "compose" } });
  fireEvent.change(within(form).getByLabelText("Context directory"), { target: { value: "app" } });
  fireEvent.change(within(form).getByLabelText("Compose file"), { target: { value: "deploy/compose.yaml" } });
  fireEvent.change(within(form).getByLabelText("Public Compose service"), { target: { value: "web" } });
  fireEvent.change(within(form).getByLabelText("Public Compose service port"), { target: { value: "3456" } });
  fireEvent.click(within(form).getByRole("button", { name: "Add build variable" }));
  fireEvent.change(within(form).getByLabelText("Build variable name 1"), { target: { value: "APP_MODE" } });
  fireEvent.change(within(form).getByLabelText("Build variable value 1"), { target: { value: "release" } });
  vi.mocked(api.createResource).mockRejectedValue(new ApiError(503, "unavailable", null));
  fireEvent.submit(form); await screen.findByRole("alert");
  expect(api.createResource).toHaveBeenCalledWith({ project_id: "one", project_environment_id: "env", name: "Git stack", kind: "application", source: { kind: "git", git: { repository_url: "https://github.com/team/app", requested_ref: "release", credential_kind: "public", settings: { strategy: "compose", context_dir: "app", compose_file: "deploy/compose.yaml", public_component: "web", container_port: 3456, build_variables: [{ name: "APP_MODE", value: "release" }] } } } });
  expect(within(form).getByLabelText<HTMLInputElement>("Compose file").value).toBe("deploy/compose.yaml");
  expect(within(form).getByLabelText<HTMLInputElement>("Build variable value 1").value).toBe("release");
  const original = vi.mocked(api.createResource).mock.calls[0]![0];
  vi.mocked(api.createResource).mockResolvedValueOnce(resource);
  fireEvent.submit(form); await screen.findByText("Application configuration saved. Deployment is a separate request.");
  expect(vi.mocked(api.createResource).mock.calls[1]![0]).toEqual(original);
});

test("Git Compose CAS conflict displays current file and service while retaining the reviewed draft", async () => {
  if (gitResource.source.kind !== "git") throw new Error("Expected Git test resource");
  const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "compose", context_dir: "app", compose_file: "compose.yaml", public_component: "web", container_port: 80, build_variables: [{ name: "APP_MODE", value: "old" }] } } } };
  if (saved.source.kind !== "git") throw new Error("Expected saved Git test resource");
  const current: api.Resource = { ...saved, generation: 2, source: { kind: "git", git: { ...saved.source.git, generation: 3, requested_ref: "current-ref", settings: { strategy: "compose", context_dir: "latest", compose_file: "current.yaml", public_component: "api", container_port: 3000, build_variables: [{ name: "APP_MODE", value: "current" }] } } } };
  vi.mocked(api.listResources).mockResolvedValue(page([saved]));
  vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockResolvedValueOnce(current);
  vi.mocked(api.getResource).mockResolvedValue(current);
  setup(); const form = await edit();
  fireEvent.change(within(form).getByLabelText("Compose file"), { target: { value: "draft.yaml" } });
  fireEvent.change(within(form).getByLabelText("Build variable value 1"), { target: { value: "draft" } });
  fireEvent.submit(form); await screen.findByRole("button", { name: "Keep my draft with generation 2" });
  const metadata = within(screen.getByRole("region", { name: "Current Git configuration" }));
  for (const [label, value] of [["Current Compose file", "current.yaml"], ["Current public Compose service", "api"], ["Current public Compose service port", "3000"], ["Current requested ref", "current-ref"], ["Current source generation", "3"]] as const) expect(metadata.getByText(label).nextElementSibling?.textContent).toBe(value);
  expect(within(form).getByLabelText<HTMLInputElement>("Compose file").value).toBe("draft.yaml");
  expect(metadata.getByText("Current build variables").nextElementSibling?.textContent).toBe('APP_MODE="current"');
  expect(within(form).getByLabelText<HTMLInputElement>("Build variable value 1").value).toBe("draft");
  expect(api.updateResource).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Keep my draft with generation 2" }));
  expect(api.updateResource).toHaveBeenCalledTimes(1);
  fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
  expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 2, source: { kind: "git", git: { repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "compose", context_dir: "app", compose_file: "draft.yaml", public_component: "web", container_port: 80, build_variables: [{ name: "APP_MODE", value: "draft" }] } } } });
});

test("invalid Compose build variables cannot leave the actual create form", async () => {
 setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: "compose" } });
 fireEvent.click(within(form).getByRole("button", { name: "Add build variable" }));
 fireEvent.change(within(form).getByLabelText("Build variable name 1"), { target: { value: "HTTP_PROXY" } });
 fireEvent.submit(form); await screen.findByText(/reserved for build execution/);
 expect(api.createResource).not.toHaveBeenCalled();
});

test("Dockerfile creation retains multiline public build variables and injection opt-out across response loss", async () => {
 setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Dockerfile app" } });
 fireEvent.change(within(form).getByLabelText("Git repository URL"), { target: { value: "https://github.com/team/app" } });
 fireEvent.click(within(form).getByRole("button", { name: "Add build variable" }));
 fireEvent.change(within(form).getByLabelText("Build variable name 1"), { target: { value: "MESSAGE" } });
 fireEvent.change(within(form).getByLabelText("Build variable value 1"), { target: { value: "first line\nsecond line" } });
 fireEvent.click(within(form).getByRole("checkbox", { name: "Inject build arguments into Dockerfile" }));
 vi.mocked(api.createResource).mockRejectedValueOnce(new ApiError(503, "unavailable", null)).mockResolvedValueOnce(resource);
 fireEvent.submit(form); await screen.findByRole("alert");
 const original = vi.mocked(api.createResource).mock.calls[0]![0];
 expect(original.source).toMatchObject({ kind: "git", git: { settings: { strategy: "dockerfile", inject_build_args_to_dockerfile: false, build_variables: [{ name: "MESSAGE", value: "first line\nsecond line" }] } } });
 fireEvent.submit(form); await screen.findByText("Application configuration saved. Deployment is a separate request.");
 expect(vi.mocked(api.createResource).mock.calls[1]![0]).toEqual(original);
});
test("Dockerfile source conflict reviews current argument injection while preserving the draft", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const current: api.Resource = { ...gitResource, generation: 2, source: { kind: "git", git: { ...gitResource.source.git, generation: 3, settings: { strategy: "dockerfile", inject_build_args_to_dockerfile: true, build_variables: [{ name: "MODE", value: "current" }] } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([gitResource]));
 vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockResolvedValueOnce(current);
 vi.mocked(api.getResource).mockResolvedValue(current);
 setup(); const form = await edit();
 fireEvent.click(within(form).getByRole("button", { name: "Add build variable" }));
 fireEvent.change(within(form).getByLabelText("Build variable name 1"), { target: { value: "MODE" } });
 fireEvent.change(within(form).getByLabelText("Build variable value 1"), { target: { value: "draft" } });
 fireEvent.click(within(form).getByRole("checkbox", { name: "Inject build arguments into Dockerfile" }));
 fireEvent.submit(form); await screen.findByRole("button", { name: "Keep my draft with generation 2" });
 const metadata = within(screen.getByRole("region", { name: "Current Git configuration" }));
 expect(metadata.getByText("Current Dockerfile ARG injection").nextElementSibling?.textContent).toBe("Enabled");
 expect(metadata.getByText("Current build variables").nextElementSibling?.textContent).toBe('MODE="current"');
 expect(within(form).getByRole<HTMLInputElement>("checkbox", { name: "Inject build arguments into Dockerfile" }).checked).toBe(false);
 fireEvent.click(screen.getByRole("button", { name: "Keep my draft with generation 2" }));
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
 expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 2, source: { kind: "git", git: { repository_url: "https://github.com/acme/web", requested_ref: "main", credential_kind: "public", settings: { strategy: "dockerfile", inject_build_args_to_dockerfile: false, build_variables: [{ name: "MODE", value: "draft" }] } } } });
});

function registryMetadata() {
 vi.mocked(request).mockImplementation(async (url) => page(url.includes("/versions") ? [{ secret_id: "credential", version: 1 }, { secret_id: "credential", version: 2 }] : [{ id: "credential", project_id: "one", name: "Build access", status: "active" }, { id: "foreign", project_id: "other", name: "Foreign", status: "active" }]));
}
async function selectBuildRegistry(form: HTMLElement) {
 fireEvent.change(within(form).getByLabelText("Build registry credential", { exact: true }), { target: { value: "private" } });
 await within(form).findByRole("option", { name: "Build access · credential" });
 expect(within(form).queryByRole("option", { name: "Foreign · foreign" })).toBeNull();
 fireEvent.change(within(form).getByLabelText("Build registry credential secret"), { target: { value: "credential" } });
 await within(form).findByRole("option", { name: "Version 1" });
 fireEvent.change(within(form).getByLabelText("Build registry credential version"), { target: { value: "1" } });
 await within(form).findByText(/Selected build registry credential: credential, version 1/);
}
test.each(["dockerfile", "compose"])("%s source creation requires an explicit build registry version and retains it through retry", async strategy => {
 registryMetadata(); setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: strategy } });
 fireEvent.change(within(form).getByLabelText("Build registry credential", { exact: true }), { target: { value: "private" } });
 fireEvent.submit(form); expect(api.createResource).not.toHaveBeenCalled();
 await selectBuildRegistry(form);
 vi.mocked(api.createResource).mockRejectedValueOnce(new ApiError(503, "unavailable", null)).mockResolvedValueOnce(resource);
 fireEvent.submit(form); await within(form).findByRole("alert");
 const original = vi.mocked(api.createResource).mock.calls[0]![0];
 expect(original.source).toMatchObject({ kind: "git", git: { settings: { strategy, build_registry_credential: { secret_id: "credential", version: 1 } } } });
 expect(JSON.stringify(original)).not.toContain('"registry_credential"');
 fireEvent.submit(form); await screen.findByText("Application configuration saved. Deployment is a separate request.");
 expect(vi.mocked(api.createResource).mock.calls[1]![0]).toEqual(original);
 expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential", { exact: true }).value).toBe("public");
 expect(vi.mocked(request).mock.calls.some(([url, options]) => url.includes("reveal") || options?.method === "POST")).toBe(false);
});
test("saved build credential hydrates without substitution and can be explicitly cleared", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "dockerfile", build_registry_credential: { secret_id: "credential", version: 1 } } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([saved])); registryMetadata(); setup();
 const form = await edit(); await within(form).findByRole("option", { name: "Version 1" });
 expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential version").value).toBe("1");
 expect(within(form).queryByLabelText("Registry password or token")).toBeNull();
 fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: "compose" } });
 await within(form).findByText(/Selected build registry credential: credential, version 1/);
 fireEvent.change(within(form).getByLabelText("Build registry credential", { exact: true }), { target: { value: "public" } });
 await within(form).findByText(/Selected build registry credential: None/);
 vi.mocked(api.updateResource).mockResolvedValue(saved);
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(1));
 const body = vi.mocked(api.updateResource).mock.calls[0]![1];
 expect(body.source?.kind === "git" && body.source.git.settings).not.toHaveProperty("build_registry_credential");
});
test("build credential metadata denial blocks saving the source without falling back to public", async () => {
 vi.mocked(request).mockRejectedValue(new ApiError(403, "denied", null)); setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Build registry credential", { exact: true }), { target: { value: "private" } });
 await within(form).findByText(/Build registry credential access is unavailable/);
 fireEvent.submit(form); expect(api.createResource).not.toHaveBeenCalled();
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
});

test("build credential source conflicts retain the exact draft version and require explicit generation review", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "dockerfile", build_registry_credential: { secret_id: "credential", version: 1 } } } } };
 if (saved.source.kind !== "git") throw new Error("Expected Git source");
 const current: api.Resource = { ...saved, generation: 2, source: { kind: "git", git: { ...saved.source.git, generation: 3, settings: { ...saved.source.git.settings, build_registry_credential: { secret_id: "credential", version: 3 } } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([saved])); registryMetadata();
 vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockResolvedValueOnce(current);
 vi.mocked(api.getResource).mockResolvedValue(current); setup();
 const form = await edit(); await within(form).findByRole("option", { name: "Version 2" });
 fireEvent.change(within(form).getByLabelText("Build registry credential version"), { target: { value: "2" } });
 fireEvent.submit(form); await screen.findByRole("button", { name: "Keep my draft with generation 2" });
 const metadata = within(screen.getByRole("region", { name: "Current Git configuration" }));
 expect(metadata.getByText("Current build registry credential").nextElementSibling?.textContent).toBe("credential, version 3");
 expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential version").value).toBe("2");
 expect(api.updateResource).toHaveBeenCalledTimes(1);
 fireEvent.click(screen.getByRole("button", { name: "Keep my draft with generation 2" }));
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
 expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 2, source: { kind: "git", git: { repository_url: saved.source.git.repository_url, requested_ref: saved.source.git.requested_ref, credential_kind: "public", settings: { strategy: "dockerfile", build_registry_credential: { secret_id: "credential", version: 2 } } } } });
});

test("build credential denial recovers through metadata refresh while preserving the source draft", async () => {
 let deniedAccess = true;
 vi.mocked(request).mockImplementation(async url => {
  if (deniedAccess) throw new ApiError(403, "denied", null);
  return page(url.includes("/versions") ? [{ secret_id: "credential", version: 1 }] : [{ id: "credential", project_id: "one", name: "Build access", status: "active" }]);
 });
 setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Retained Git draft" } });
 fireEvent.change(within(form).getByLabelText("Build registry credential", { exact: true }), { target: { value: "private" } });
 await within(form).findByText(/Build registry credential access is unavailable/);
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
 const deniedReads = vi.mocked(request).mock.calls.length;
 fireEvent.click(within(form).getByRole("button", { name: "Refresh registry credential metadata" }));
 await waitFor(() => expect(vi.mocked(request).mock.calls.length).toBeGreaterThan(deniedReads));
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Refresh registry credential metadata" }).disabled).toBe(false));
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
 fireEvent.submit(form); expect(api.createResource).not.toHaveBeenCalled();
 deniedAccess = false;
 fireEvent.click(within(form).getByRole("button", { name: "Refresh registry credential metadata" }));
 await within(form).findByRole("option", { name: "Build access · credential" });
 await waitFor(() => expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential secret").disabled).toBe(false));
 expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential", { exact: true }).value).toBe("private");
 expect(within(form).getByLabelText<HTMLInputElement>("Application name").value).toBe("Retained Git draft");
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(true);
 expect(api.createResource).not.toHaveBeenCalled();
 await selectBuildRegistry(form);
 vi.mocked(api.createResource).mockResolvedValueOnce(resource);
 fireEvent.submit(form); await waitFor(() => expect(api.createResource).toHaveBeenCalledTimes(1));
 expect(vi.mocked(api.createResource).mock.calls[0]![0].source).toMatchObject({ kind: "git", git: { settings: { build_registry_credential: { secret_id: "credential", version: 1 } } } });
});

test("saved build credential refresh preserves the explicit version until the operator saves", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "dockerfile", build_registry_credential: { secret_id: "credential", version: 1 } } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([saved]));
 let deniedAccess = true;
 vi.mocked(request).mockImplementation(async url => {
  if (deniedAccess) throw new ApiError(403, "denied", null);
  return page(url.includes("/versions") ? [{ secret_id: "credential", version: 1 }, { secret_id: "credential", version: 2 }] : [{ id: "credential", project_id: "one", name: "Build access", status: "active" }]);
 });
 setup(); const form = await edit();
 await within(form).findByText(/Build registry credential access is unavailable/);
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "retained-branch" } });
 deniedAccess = false;
 fireEvent.click(within(form).getByRole("button", { name: "Refresh registry credential metadata" }));
 await within(form).findByRole("option", { name: "Version 2" });
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(false));
 expect(within(form).getByLabelText<HTMLSelectElement>("Build registry credential version").value).toBe("1");
 expect(within(form).getByLabelText<HTMLInputElement>("Git branch, tag, or commit").value).toBe("retained-branch");
 expect(api.updateResource).not.toHaveBeenCalled();
 vi.mocked(api.updateResource).mockResolvedValue(saved);
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(1));
 expect(vi.mocked(api.updateResource).mock.calls[0]![1].source).toMatchObject({ kind: "git", git: { requested_ref: "retained-branch", settings: { build_registry_credential: { secret_id: "credential", version: 1 } } } });
});


async function selectBuildSecret(form: HTMLElement, version = "1") {
 fireEvent.click(within(form).getByRole("button", { name: "Add build secret" }));
 fireEvent.change(within(form).getByLabelText("Build secret name 1"), { target: { value: "DEPENDENCY_TOKEN" } });
 const region = within(form).getByRole("region", { name: "Build secret 1" });
 await within(region).findByRole("option", { name: "Build access · credential" });
 expect(within(region).queryByRole("option", { name: "Foreign · foreign" })).toBeNull();
 fireEvent.change(within(region).getByLabelText("Build secret 1 secret"), { target: { value: "credential" } });
 await within(region).findByRole("option", { name: `Version ${version}` });
 fireEvent.change(within(region).getByLabelText("Build secret 1 version"), { target: { value: version } });
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(false));
}
test.each(["dockerfile", "compose"])("%s build secrets require exact metadata, retain a failed draft, and reset after save", async strategy => {
 registryMetadata(); setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 fireEvent.change(within(form).getByLabelText("Build strategy"), { target: { value: strategy } });
 await selectBuildSecret(form, "2");
 vi.mocked(api.createResource).mockRejectedValueOnce(new ApiError(503, "unavailable", null)).mockResolvedValueOnce(resource);
 fireEvent.submit(form); await within(form).findByRole("alert");
 const original = vi.mocked(api.createResource).mock.calls[0]![0];
 expect(original.source).toMatchObject({ kind: "git", git: { settings: { strategy, build_secrets: [{ name: "DEPENDENCY_TOKEN", secret: { secret_id: "credential", version: 2 } }] } } });
 fireEvent.submit(form); await screen.findByText("Application configuration saved. Deployment is a separate request.");
 expect(vi.mocked(api.createResource).mock.calls[1]![0]).toEqual(original);
 expect(within(form).queryByLabelText("Build secret name 1")).toBeNull();
 expect(vi.mocked(request).mock.calls.some(([url, options]) => url.includes("reveal") || options?.method === "POST")).toBe(false);
});
test("saved build secret versions survive denied metadata refresh and recover without replacing the draft", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "dockerfile", build_secrets: [{ name: "DEPENDENCY_TOKEN", secret: { secret_id: "credential", version: 1 } }] } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([saved]));
 vi.mocked(request).mockRejectedValue(new ApiError(403, "forbidden", null)); setup();
 const form = await edit();
 await within(form).findByText(/Build secret access is unavailable/);
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "my-draft" } });
 fireEvent.submit(form); expect(api.updateResource).not.toHaveBeenCalled();
 const refresh = within(form).getByRole("button", { name: "Refresh build secret 1 metadata" });
 fireEvent.click(refresh); await waitFor(() => expect((refresh as HTMLButtonElement).disabled).toBe(false));
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
 registryMetadata(); fireEvent.click(refresh);
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(false));
 expect(within(form).getByLabelText<HTMLSelectElement>("Build secret 1 version").value).toBe("1");
 expect(within(form).getByLabelText<HTMLInputElement>("Git branch, tag, or commit").value).toBe("my-draft");
 vi.mocked(api.updateResource).mockResolvedValue({ ...saved, generation: 2 });
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalled());
 expect(vi.mocked(api.updateResource).mock.calls[0]![1].source).toMatchObject({ kind: "git", git: { requested_ref: "my-draft", settings: { build_secrets: [{ name: "DEPENDENCY_TOKEN", secret: { secret_id: "credential", version: 1 } }] } } });
 expect(vi.mocked(request).mock.calls.some(([url, options]) => url.includes("reveal") || options?.method === "POST")).toBe(false);
});

test("build secret conflict review keeps the exact draft version while refreshing current metadata", async () => {
 if (gitResource.source.kind !== "git") throw new Error("Expected Git resource");
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...gitResource.source.git, settings: { strategy: "dockerfile", build_secrets: [{ name: "TOKEN", secret: { secret_id: "credential", version: 1 } }] } } } };
 if (saved.source.kind !== "git") throw new Error("Expected Git source");
 const current: api.Resource = { ...saved, generation: 2, source: { kind: "git", git: { ...saved.source.git, generation: 3, settings: { strategy: "dockerfile", build_secrets: [{ name: "CURRENT_TOKEN", secret: { secret_id: "credential", version: 1 } }] } } } };
 vi.mocked(api.listResources).mockResolvedValue(page([saved])); registryMetadata();
 vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockRejectedValueOnce(new ApiError(409, "stale", null));
 vi.mocked(api.getResource).mockResolvedValue(current); setup();
 const form = await edit(); await within(form).findByRole("option", { name: "Version 2" });
 fireEvent.change(within(form).getByLabelText("Build secret 1 version"), { target: { value: "2" } });
 fireEvent.submit(form); await screen.findByRole("button", { name: "Keep my draft with generation 2" });
 expect(within(screen.getByRole("region", { name: "Current Git configuration" })).getByText("Current build secrets").nextElementSibling?.textContent).toBe("CURRENT_TOKEN: credential, version 1");
 expect(within(form).getByLabelText<HTMLSelectElement>("Build secret 1 version").value).toBe("2");
 fireEvent.click(screen.getByRole("button", { name: "Keep my draft with generation 2" }));
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
 expect(vi.mocked(api.updateResource).mock.calls[1]![1]).toMatchObject({ expected_generation: 2, source: { kind: "git", git: { settings: { build_secrets: [{ name: "TOKEN", secret: { secret_id: "credential", version: 2 } }] } } } });
 fireEvent.click(await screen.findByRole("button", { name: "Reload current configuration" }));
 await waitFor(() => expect(api.getResource).toHaveBeenCalledTimes(3));
 expect(within(form).getByLabelText<HTMLInputElement>("Build secret name 1").value).toBe("TOKEN");
 expect(within(form).getByLabelText<HTMLSelectElement>("Build secret 1 version").value).toBe("2");
});
test("removing an incomplete build secret leaves a validated row saveable", async () => {
 registryMetadata(); setup(); await selectEnvironment();
 const form = screen.getByRole("form", { name: "Create application" });
 fireEvent.change(within(form).getByLabelText("Application source"), { target: { value: "git" } });
 await selectBuildSecret(form);
 fireEvent.click(within(form).getByRole("button", { name: "Add build secret" }));
 fireEvent.submit(form); expect(api.createResource).not.toHaveBeenCalled();
 fireEvent.click(within(form).getByRole("button", { name: "Remove build secret 2" }));
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save application" }).disabled).toBe(false));
 vi.mocked(api.createResource).mockResolvedValue(resource); fireEvent.submit(form);
 await waitFor(() => expect(api.createResource).toHaveBeenCalled());
 expect(vi.mocked(api.createResource).mock.calls[0]![0].source).toMatchObject({ kind: "git", git: { settings: { build_secrets: [{ name: "DEPENDENCY_TOKEN", secret: { secret_id: "credential", version: 1 } }] } } });
});

const storedDockerfileResource: api.Resource = { ...resource, source: { kind: "dockerfile", dockerfile: { source_id: "stored-source", generation: 2, content_hash: `sha256:${"a".repeat(64)}`, content: 'FROM nginx\nEXPOSE 8080\n', settings: {strategy: "dockerfile"} } } };
test("stored Dockerfile creation sends content without Git metadata and preserves a rejected draft", async () => {
 setup(); await selectEnvironment(); const form = screen.getByRole("form", {name:"Create application"});
 fireEvent.change(within(form).getByLabelText("Application source"), {target:{value:"dockerfile"}});
 expect(within(form).queryByLabelText("Git repository URL")).toBeNull(); expect(within(form).queryByLabelText("Dockerfile path")).toBeNull();
 fireEvent.change(within(form).getByLabelText("Application name"), {target:{value:"Stored web"}});
 fireEvent.change(within(form).getByLabelText("Dockerfile content"), {target:{value:'FROM nginx\nEXPOSE 8080\n'}});
 fireEvent.change(within(form).getByLabelText("Build target"), {target:{value:"web"}});
 vi.mocked(api.createResource).mockRejectedValue(new ApiError(503,"unavailable",null));
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button", {name:"Save application"}).disabled).toBe(false));
 fireEvent.submit(form); await screen.findByRole("alert");
 expect(api.createResource).toHaveBeenCalledWith({project_id:"one",project_environment_id:"env",name:"Stored web",kind:"application",source:{kind:"dockerfile",dockerfile:{content:'FROM nginx\nEXPOSE 8080\n',settings:{strategy:"dockerfile",build_target:"web"}}}});
 expect(within(form).getByLabelText<HTMLTextAreaElement>("Dockerfile content").value).toBe('FROM nginx\nEXPOSE 8080\n');
});
test("stored Dockerfile conflict shows saved content while retaining and explicitly resaving the draft", async () => {
 vi.mocked(api.listResources).mockResolvedValue(page([storedDockerfileResource])); setup(); const form = await edit();
 fireEvent.change(within(form).getByLabelText("Dockerfile content"), {target:{value:"FROM nginx\nEXPOSE 9000\n"}});
 if (storedDockerfileResource.source.kind !== "dockerfile") throw new Error("Expected Dockerfile");
 const latest: api.Resource = {...storedDockerfileResource,generation:3,source:{kind:"dockerfile",dockerfile:{...storedDockerfileResource.source.dockerfile,generation:4,content:"FROM busybox\n",content_hash:`sha256:${"b".repeat(64)}`}}};
 vi.mocked(api.updateResource).mockRejectedValueOnce(new ApiError(409,"stale",null)).mockResolvedValueOnce(latest); vi.mocked(api.getResource).mockResolvedValue(latest);
 await waitFor(() => expect(within(form).getByRole<HTMLButtonElement>("button",{name:"Save configuration"}).disabled).toBe(false));
 fireEvent.submit(form); await screen.findByRole("button",{name:"Keep my draft with generation 3"});
 expect(within(form).getByLabelText("Current Dockerfile configuration").textContent).toContain("FROM busybox");
 expect(within(form).getByLabelText<HTMLTextAreaElement>("Dockerfile content").value).toContain("EXPOSE 9000");
 fireEvent.click(within(form).getByRole("button",{name:"Keep my draft with generation 3"})); fireEvent.submit(form);
 await screen.findByText("Application configuration updated. Deployment is a separate request.");
 expect(api.updateResource).toHaveBeenLastCalledWith("app",{expected_generation:3,source:{kind:"dockerfile",dockerfile:{content:"FROM nginx\nEXPOSE 9000\n",settings:{strategy:"dockerfile"}}}});
});


test("SSH editor preserves saved host fingerprints for source edits and sends a rename alone", async () => {
 const fingerprints = ["SHA256:" + "A".repeat(43)];
 const saved: api.Resource = { ...gitResource, source: { kind: "git", git: { ...(gitResource.source.kind === "git" ? gitResource.source.git : {}), credential_kind: "ssh_private_key", repository_url: "ssh://git@git.example/team/app.git", ssh_host_fingerprints: fingerprints } } } as api.Resource;
 vi.mocked(api.listResources).mockResolvedValue(page([saved]));
 vi.mocked(api.updateResource).mockRejectedValue(new ApiError(400, "invalid", "request"));
 setup(); const form = await edit();
 expect(within(form).getByLabelText<HTMLTextAreaElement>("SSH host fingerprints").value).toBe(fingerprints[0]);
 expect(within(form).getByRole<HTMLButtonElement>("button", { name: "Save configuration" }).disabled).toBe(true);
 fireEvent.change(within(form).getByLabelText("Application name"), { target: { value: "Renamed" } });
 fireEvent.submit(form); await screen.findByRole("alert");
 expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 1, name: "Renamed" });
 fireEvent.change(within(form).getByLabelText("Git branch, tag, or commit"), { target: { value: "release" } });
 fireEvent.submit(form); await waitFor(() => expect(api.updateResource).toHaveBeenCalledTimes(2));
 expect(api.updateResource).toHaveBeenLastCalledWith("app", { expected_generation: 1, name: "Renamed", source: { kind: "git", git: { repository_url: "ssh://git@git.example/team/app.git", requested_ref: "release", credential_kind: "ssh_private_key", ssh_host_fingerprints: fingerprints, settings: { strategy: "dockerfile" } } } });
 expect(within(form).getByLabelText<HTMLTextAreaElement>("SSH host fingerprints").value).toBe(fingerprints[0]);
});
