import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { HostingCatalog } from "./HostingCatalog";
import { useDatabaseAccess } from "./ResourceDatabase";
import * as api from "@/api/hosting-catalog";
import { listDestinations } from "@/api/destinations";
import { revealManagedSecret } from "@/api/managed-secrets";
vi.mock("./ResourceDatabase", () => ({ useDatabaseAccess: vi.fn() }));
vi.mock("@/api/hosting-catalog", () => ({ getCatalog: vi.fn(), installCatalog: vi.fn() }));
vi.mock("@/api/destinations", () => ({ listDestinations: vi.fn() }));
vi.mock("@/api/managed-secrets", () => ({ revealManagedSecret: vi.fn() }));
const template: api.CatalogTemplate = { id: "linkding", immutable_version: "coolify-b12d1386.linkding-1.47.0.v1", name: "Linkding", upstream_repository: "https://github.com/coollabsio/coolify", upstream_commit: "b12d1386", upstream_path: "templates/compose/linkding.yaml", original: "services: {}", original_hash: "original", definition_hash: "definition", definition: { image_reference: "image@sha256:digest", container_port: 9090, mount_path: "/etc/linkding/data", readiness: { path: "/health", timeout_seconds: 120 }, credential_names: ["LD_SUPERUSER_NAME", "LD_SUPERUSER_PASSWORD"] } };
let client: QueryClient;
afterEach(() => {
    cleanup();
    client?.clear();
    vi.clearAllMocks();
});
beforeEach(() => {
    vi.mocked(useDatabaseAccess).mockReturnValue({ ready: true, refreshing: false, owner: true, viewer: true, maintainer: true, error: undefined, scope: { actor: "actor", project: "project", environment: "env" }, epoch: "epoch" });
    vi.mocked(api.getCatalog).mockResolvedValue([template]);
    vi.mocked(listDestinations).mockResolvedValue({ items: [{ id: "dest", name: "Docker", server_id: "server", runtime_kind: "docker_compose", created_at: "" }], limit: 20, offset: 0, total: 1, count: 1 });
});
function setup() {
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(<QueryClientProvider client={client}>
    <HostingCatalog projectID="project" environmentID="env" blocked={false}/>
    </QueryClientProvider>);
}
test("viewer cannot select or install a catalog application", () => {
    vi.mocked(useDatabaseAccess).mockReturnValue({ ...useDatabaseAccess("project", "env"), owner: false });
    setup();
    expect(screen.getByText(/Project owner access/)).toBeTruthy();
    expect(api.getCatalog).not.toHaveBeenCalled();
    expect(api.installCatalog).not.toHaveBeenCalled();
});
test("response loss retains exact install identity and reveal is explicit", async () => {
    setup();
    await screen.findByRole("option", { name: /Linkding ·/ });
    fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
    await screen.findByRole("option", { name: "Docker" });
    fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
    vi.mocked(api.installCatalog).mockRejectedValueOnce(new Error("response lost")).mockImplementationOnce(async (body) => ({ id: "res", request: body, template, resource: { id: "res", project_id: "project", project_environment_id: "env", name: "Linkding", kind: "application", source: { kind: "image", image: { reference: template.definition.image_reference } }, generation: 1, created_by: "actor", created_at: "", updated_at: "" }, binding: { id: "binding", resource_id: "res", destination_id: "dest", runtime_namespace: "ncd", created_by: "actor", created_at: "" }, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, runtime_configuration: { generation: 1, hash: "runtime" }, credentials: [{ name: "LD_SUPERUSER_PASSWORD", kind: "managed_secret", secret: { secret_id: "secret", version: 1 } }], created_by: "actor", created_at: "" }));
    fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
    await screen.findByRole("alert");
    const first = vi.mocked(api.installCatalog).mock.calls[0]![0];
    expect(first.request_id).toMatch(/^[0-9a-f]{32}$/);
    fireEvent.click(screen.getByRole("button", { name: "Retry same installation" }));
    await screen.findByText(/Installed Linkding/);
    expect(vi.mocked(api.installCatalog).mock.calls[1]![0]).toEqual(first);
    expect(revealManagedSecret).not.toHaveBeenCalled();
    vi.mocked(revealManagedSecret).mockResolvedValue({ secret_id: "secret", version: 1, value: "private-value" });
    fireEvent.click(screen.getByRole("button", { name: "Reveal login credentials" }));
    await screen.findByText("private-value");
    fireEvent.click(screen.getByRole("button", { name: "Hide credentials" }));
    await waitFor(() => expect(screen.queryByText("private-value")).toBeNull());
});
