import { ApiError } from "@/api/errors";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { HostingCatalog } from "./HostingCatalog";
import { useDatabaseAccess } from "./ResourceDatabase";
import * as api from "@/api/hosting-catalog";
import * as placement from "@/api/hosting-catalog-destinations";
import { revealManagedSecret } from "@/api/managed-secrets";
vi.mock("./ResourceDatabase", () => ({ useDatabaseAccess: vi.fn() }));
vi.mock("@/api/hosting-catalog", () => ({ getCatalog: vi.fn(), installCatalog: vi.fn() }));
vi.mock("@/api/hosting-catalog-destinations", async importOriginal => ({ ...(await importOriginal<typeof import("@/api/hosting-catalog-destinations")>()), getCatalogDestinationOptions: vi.fn() }));
vi.mock("@/api/managed-secrets", () => ({ revealManagedSecret: vi.fn() }));
const template: api.CatalogTemplate = { id: "linkding", immutable_version: "coolify-b12d1386.linkding-1.47.0.v1", name: "Linkding", category: "RSS", upstream_repository: "https://github.com/coollabsio/coolify", upstream_commit: "b12d1386", upstream_path: "templates/compose/linkding.yaml", original: "services: {}", original_hash: "original", definition_hash: "definition", definition: { image_reference: "image@sha256:digest", container_port: 9090, mount_path: "/etc/linkding/data", readiness: { path: "/health", timeout_seconds: 120 }, credential_names: ["LD_SUPERUSER_NAME", "LD_SUPERUSER_PASSWORD"] } };
function optionsFor(selected: api.CatalogTemplate, offset = 0): placement.CatalogDestinationOptions {
  return { template_id: selected.id, immutable_version: selected.immutable_version, requirements: {
    source_kind: selected.definition.compose ? "compose" : "image", required_capabilities: selected.definition.compose ? ["image_deploy", "hosting_managed_compose_v1", "hosting_runtime_env_v1", "image_release_ports_v1"] : ["image_deploy", "hosting_owned_storage_v1", "hosting_owned_storage_runtime_v1", "hosting_runtime_env_v1", "image_release_ports_v1"], architecture_restriction: selected.amd_only ? "amd64" : selected.arm_only ? "arm64" : "undeclared", architecture_verification: "unknown", cpu: "undeclared", memory: "undeclared", disk: "undeclared", mount_paths: selected.definition.compose ? ["/app/vikunja/files", "/var/lib/postgresql/data"] : [selected.definition.mount_path], public_url_required: !!selected.definition.compose, managed_credential_names: selected.definition.credential_names,
  }, items: [{ destination: { id: "dest", name: "Docker", server_id: "server", runtime_kind: "docker_compose", created_at: "" }, server_name: "Hosting server", available: true, reason_code: "", missing_capabilities: [] }], total: offset + 1, offset, limit: 20 };
}
let client: QueryClient;
afterEach(() => {
    cleanup();
    client?.clear();
    vi.clearAllMocks();
});
beforeEach(() => {
    vi.mocked(useDatabaseAccess).mockReturnValue({ ready: true, refreshing: false, owner: true, viewer: true, maintainer: true, error: undefined, scope: { actor: "actor", project: "project", environment: "env" }, epoch: "epoch" });
    vi.mocked(api.getCatalog).mockResolvedValue([template]);
    vi.mocked(placement.getCatalogDestinationOptions).mockImplementation(async (_project, selected, offset) => optionsFor(selected, offset));
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
    expect(screen.getByLabelText<HTMLInputElement>("Search catalog").disabled).toBe(true);
    expect(screen.getByLabelText<HTMLSelectElement>("Catalog category").disabled).toBe(true);
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

async function selectCatalogDraft() {
  setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), {
    target: { value: "linkding" },
  });
  await screen.findByRole("option", { name: "Docker" });
  fireEvent.change(screen.getByLabelText("Catalog destination"), {
    target: { value: "dest" },
  });
}
for (const name of ["   ", " surrounding ", "ár".repeat(65), "control\u0001"]) {
  test(`invalid name is corrected before freezing a request: ${JSON.stringify(name)}`, async () => {
    await selectCatalogDraft();
    fireEvent.change(screen.getByLabelText("Installation name"), {
      target: { value: name },
    });
    fireEvent.submit(
      screen.getByRole("form", { name: "Install catalog application" }),
    );
    await screen.findByText(/Enter a name with 1 to 128 UTF-8 bytes/);
    expect(api.installCatalog).not.toHaveBeenCalled();
    expect(
      screen.getByLabelText<HTMLInputElement>("Installation name").disabled,
    ).toBe(false);
    fireEvent.change(screen.getByLabelText("Installation name"), {
      target: { value: "Corrected" },
    });
    vi.mocked(api.installCatalog).mockRejectedValueOnce(
      new Error("unconfirmed"),
    );
    fireEvent.submit(
      screen.getByRole("form", { name: "Install catalog application" }),
    );
    await screen.findByRole("button", { name: "Retry same installation" });
    expect(vi.mocked(api.installCatalog).mock.calls[0]![0].name).toBe(
      "Corrected",
    );
  });
}
test("definitive validation rejection unlocks correction with a fresh request identity", async () => {
  await selectCatalogDraft();
  vi.mocked(api.installCatalog)
    .mockRejectedValueOnce(new ApiError(400, "rejected", null))
    .mockRejectedValueOnce(new Error("unconfirmed"));
  fireEvent.submit(
    screen.getByRole("form", { name: "Install catalog application" }),
  );
  await screen.findByText(/Installation settings were rejected/);
  expect(
    screen.getByLabelText<HTMLInputElement>("Installation name").disabled,
  ).toBe(false);
  const first = vi.mocked(api.installCatalog).mock.calls[0]![0];
  fireEvent.change(screen.getByLabelText("Installation name"), {
    target: { value: "Corrected" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Install catalog application" }),
  );
  await screen.findByRole("button", { name: "Retry same installation" });
  const second = vi.mocked(api.installCatalog).mock.calls[1]![0];
  expect(second.name).toBe("Corrected");
  expect(second.request_id).not.toBe(first.request_id);
});

test("catalog search matches metadata and clears a hidden selection before install", async () => {
  await selectCatalogDraft();
  const search = screen.getByLabelText("Search catalog");
  expect(screen.getByRole("region", { name: "Catalog installation preview" }).textContent).toContain("LD_SUPERUSER_PASSWORD");
  fireEvent.change(search, { target: { value: "missing-application" } });
  expect(screen.queryByRole("option", { name: /Linkding ·/ })).toBeNull();
  expect(screen.queryByRole("region", { name: "Catalog installation preview" })).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog application").value).toBe("");
  expect(screen.getByRole("option", { name: /Linkding ·/ })).toBeTruthy();
  for (const query of [" LINKDING  1.47.0 ", "sha256:digest", "LD_SUPERUSER_PASSWORD", "/etc/linkding/data"]) {
    fireEvent.change(search, { target: { value: query } });
    expect(screen.getByRole("option", { name: /Linkding ·/ })).toBeTruthy();
  }
  fireEvent.change(search, { target: { value: "linkding absent-term" } });
  expect(screen.queryByRole("option", { name: /Linkding ·/ })).toBeNull();
});

test("failed catalog load can be retried without submitting an installation", async () => {
  vi.mocked(api.getCatalog).mockRejectedValueOnce(new Error("unavailable")).mockResolvedValueOnce([template]);
  setup();
  await screen.findByText("Catalog or destination access is unavailable.");
  fireEvent.click(screen.getByRole("button", { name: "Retry catalog loading" }));
  await screen.findByRole("option", { name: /Linkding ·/ });
  expect(screen.queryByText("Catalog or destination access is unavailable.")).toBeNull();
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test("failed destination load retries without refetching the successful catalog", async () => {
  vi.mocked(placement.getCatalogDestinationOptions).mockRejectedValueOnce(new Error("unavailable"));
  setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await screen.findByText("Catalog or destination access is unavailable.");
  expect(screen.queryByRole("option", { name: "Docker" })).toBeNull();
  expect(api.getCatalog).toHaveBeenCalledTimes(1);
  expect(placement.getCatalogDestinationOptions).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Retry catalog loading" }));
  await screen.findByRole("option", { name: "Docker" });
  expect(screen.queryByText("Catalog or destination access is unavailable.")).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry catalog loading" })).toBeNull();
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").disabled).toBe(false);
  expect(placement.getCatalogDestinationOptions).toHaveBeenCalledTimes(2);
  expect(api.getCatalog).toHaveBeenCalledTimes(1);
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test("catalog and selected-template destination failures recover separately", async () => {
  vi.mocked(api.getCatalog).mockRejectedValueOnce(new Error("unavailable"));
  vi.mocked(placement.getCatalogDestinationOptions).mockRejectedValueOnce(new Error("unavailable"));
  setup();
  await screen.findByText("Catalog or destination access is unavailable.");
  expect(placement.getCatalogDestinationOptions).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Retry catalog loading" }));
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await screen.findByText("Catalog or destination access is unavailable.");
  fireEvent.click(screen.getByRole("button", { name: "Retry catalog loading" }));
  await screen.findByRole("option", { name: "Docker" });
  expect(screen.queryByText("Catalog or destination access is unavailable.")).toBeNull();
  expect(api.getCatalog).toHaveBeenCalledTimes(2);
  expect(placement.getCatalogDestinationOptions).toHaveBeenCalledTimes(2);
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test("empty catalog is distinct from a search with no matches", async () => {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([]);
  setup();
  await screen.findByText("No catalog applications are available.");
  expect(screen.queryByRole("button", { name: "Clear search" })).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(true);
});


test("categories use upstream comma-separated membership and combine with search", async () => {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([
    { ...template, category: "RSS, Tools" },
    { ...template, id: "other", name: "Other", immutable_version: "other-v1", category: "tools, Databases" },
  ]);
  await selectCatalogDraft();
  const category = screen.getByLabelText("Catalog category");
  fireEvent.change(category, { target: { value: "tools" } });
  expect(screen.getByRole("option", { name: /Linkding ·/ })).toBeTruthy();
  expect(screen.getByRole("option", { name: /Other ·/ })).toBeTruthy();
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog application").value).toBe("");
  fireEvent.change(screen.getByLabelText("Search catalog"), { target: { value: "1.47.0" } });
  expect(screen.queryByRole("option", { name: /Other ·/ })).toBeNull();
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  fireEvent.change(category, { target: { value: "databases" } });
  expect(screen.queryByRole("region", { name: "Catalog installation preview" })).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog category").value).toBe("");
  expect(screen.getByLabelText<HTMLInputElement>("Search catalog").value).toBe("");
  expect(screen.getByRole("option", { name: /Linkding ·/ })).toBeTruthy();
  expect(screen.getByRole("option", { name: /Other ·/ })).toBeTruthy();
});

for (const [flags, restriction] of [
  [{ amd_only: true }, "AMD only"],
  [{ arm_only: true }, "ARM only"],
  [{}, "Not declared"],
] as const) {
  test(`installation preview reports upstream architecture declaration: ${restriction}`, async () => {
    vi.mocked(api.getCatalog).mockResolvedValueOnce([{ ...template, ...flags }]);
    await selectCatalogDraft();
    const preview = screen.getByRole("region", { name: "Catalog installation preview" });
    expect(preview.querySelector("dt:nth-of-type(2) + dd")?.textContent).toBe(restriction);
    await screen.findByRole("region", { name: "Catalog application requirements" });
    expect(preview.textContent).toContain("Destination architecture is unverified.");
    expect(preview.textContent).toContain("Not declared by the upstream template. Available server capacity is unverified.");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(false);
  });
}

test("older entries without category metadata remain available under All categories", async () => {
  const { category: _category, ...legacy } = template;
  vi.mocked(api.getCatalog).mockResolvedValueOnce([legacy]);
  await selectCatalogDraft();
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog category").options.length).toBe(1);
  expect(screen.getByRole("region", { name: "Catalog installation preview" }).textContent).toContain("CategoryNot declared");
});


const kuma: api.CatalogTemplate = {
  ...template,
  id: "uptime-kuma",
  name: "Uptime Kuma",
  immutable_version: "kuma-test-version",
  category: "monitoring",
  upstream_path: "templates/compose/uptime-kuma.yaml",
  definition: {
    image_reference: "docker.io/louislam/uptime-kuma@sha256:test-digest",
    container_port: 3001,
    mount_path: "/app/data",
    readiness: { path: "/", timeout_seconds: 120 },
    credential_names: [],
  },
};

test("credential-free application filters by its upstream category and previews first-run setup", async () => {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([template, kuma]);
  setup();
  await screen.findByRole("option", { name: /Uptime Kuma ·/ });
  expect(screen.getByText("Catalog matches: 2 of 2.")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Catalog category"), { target: { value: "monitoring" } });
  expect(screen.queryByRole("option", { name: /Linkding ·/ })).toBeNull();
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "uptime-kuma" } });
  expect(screen.getByLabelText<HTMLInputElement>("Installation name").value).toBe("Uptime Kuma");
  const preview = screen.getByRole("region", { name: "Catalog installation preview" });
  expect(preview.textContent).toContain("Generated variablesNone");
  expect(preview.textContent).toContain("Container port3001");
  expect(preview.textContent).toContain("Persistent data/app/data");
  expect(preview.textContent).toContain("No login credentials are generated. After deployment, open the application to complete its initial setup.");
  expect(preview.textContent).not.toContain("Credentials are generated once");
  expect(preview.textContent).not.toContain("Runtime environment defaults");
  fireEvent.change(screen.getByLabelText("Installation name"), { target: { value: "My monitor" } });
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  expect(screen.getByLabelText<HTMLInputElement>("Installation name").value).toBe("My monitor");
});

test("credential-free installation retries the identical request and offers setup without secret controls", async () => {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([template, kuma]);
  vi.mocked(api.installCatalog).mockRejectedValueOnce(Error("lost response")).mockImplementationOnce(async (body) => ({
    id: "res", request: body, template: kuma,
    resource: { id: "res", project_id: "project", project_environment_id: "env", name: body.name, kind: "application", source: { kind: "image", image: { reference: kuma.definition.image_reference } }, generation: 1, created_by: "actor", created_at: "", updated_at: "" },
    binding: { id: "binding", resource_id: "res", destination_id: "dest", runtime_namespace: "ncd", created_by: "actor", created_at: "" },
    storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, runtime_configuration: { generation: 1, hash: "runtime" }, credentials: [], created_by: "actor", created_at: "",
  }));
  setup();
  await screen.findByRole("option", { name: /Uptime Kuma ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "uptime-kuma" } });
  await screen.findByRole("option", { name: "Docker" });
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  await screen.findByRole("button", { name: "Retry same installation" });
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog application").disabled).toBe(true);
  const first = vi.mocked(api.installCatalog).mock.calls[0]![0];
  expect(first.template_id).toBe("uptime-kuma");
  expect(first.immutable_version).toBe(kuma.immutable_version);
  fireEvent.click(screen.getByRole("button", { name: "Retry same installation" }));
  await screen.findByText("No login credentials were generated. After deployment, open the application to complete its initial setup.");
  expect(vi.mocked(api.installCatalog).mock.calls[1]![0]).toBe(first);
  expect(screen.queryByRole("button", { name: "Reveal login credentials" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Hide credentials" })).toBeNull();
  expect(screen.queryByText(/Linkding creates the initial account/)).toBeNull();
  expect(revealManagedSecret).not.toHaveBeenCalled();
});


const actual: api.CatalogTemplate = {
  ...kuma,
  id: "actualbudget",
  name: "Actual Budget",
  immutable_version: "coolify-b12d1386.actualbudget-26.10.0.v1",
  category: "finance",
  upstream_path: "templates/compose/actualbudget.yaml",
  definition: {
    image_reference: "docker.io/actualbudget/actual-server@sha256:24645e971da6bb1a1f953d6859e307a0b29be1e8b4130a35b3220c209b5b860c",
    container_port: 5006,
    mount_path: "/data",
    readiness: { path: "/health", timeout_seconds: 120 },
    credential_names: [],
    runtime_environment: { ACTUAL_LOGIN_METHOD: "password" },
  },
};
test("Actual Budget previews curated literal defaults and retries installation without generated secrets", async () => {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([template, kuma, actual]);
  vi.mocked(api.installCatalog).mockRejectedValueOnce(Error("lost response")).mockImplementationOnce(async (body) => ({
    id: "res", request: body, template: actual,
    resource: { id: "res", project_id: "project", project_environment_id: "env", name: body.name, kind: "application", source: { kind: "image", image: { reference: actual.definition.image_reference } }, generation: 1, created_by: "actor", created_at: "", updated_at: "" },
    binding: { id: "binding", resource_id: "res", destination_id: "dest", runtime_namespace: "ncd", created_by: "actor", created_at: "" },
    storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, runtime_configuration: { generation: 1, hash: "runtime" }, credentials: [], created_by: "actor", created_at: "",
  }));
  setup();
  await screen.findByRole("option", { name: /Actual Budget ·/ });
  fireEvent.change(screen.getByLabelText("Catalog category"), { target: { value: "finance" } });
  expect(screen.getByText("Catalog matches: 1 of 3.")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "actualbudget" } });
  expect(screen.getByLabelText<HTMLInputElement>("Installation name").value).toBe("Actual Budget");
  const preview = screen.getByRole("region", { name: "Catalog installation preview" });
  expect(preview.textContent).toContain("Runtime environment defaultsACTUAL_LOGIN_METHOD=password");
  expect(preview.textContent).toContain("Generated variablesNone");
  expect(preview.textContent).toContain("Persistent data/data");
  expect(preview.textContent).toContain("Container port5006");
  expect(preview.textContent).toContain("complete its initial setup.");
  expect(preview.textContent).not.toContain("initial account setup");
  await screen.findByRole("option", { name: "Docker" });
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  await screen.findByRole("button", { name: "Retry same installation" });
  const first = vi.mocked(api.installCatalog).mock.calls[0]![0];
  expect(first.template_id).toBe("actualbudget");
  expect(first.immutable_version).toBe(actual.immutable_version);
  expect(first).not.toHaveProperty("runtime_environment");
  fireEvent.click(screen.getByRole("button", { name: "Retry same installation" }));
  await screen.findByText("No login credentials were generated. After deployment, open the application to complete its initial setup.");
  expect(vi.mocked(api.installCatalog).mock.calls[1]![0]).toBe(first);
  expect(screen.queryByRole("button", { name: "Reveal login credentials" })).toBeNull();
  expect(revealManagedSecret).not.toHaveBeenCalled();
});

const vikunja: api.CatalogTemplate = {
  ...kuma, id: "vikunja-with-postgresql", name: "Vikunja with PostgreSQL",
  immutable_version: "coolify-b12d1386.vikunja-2.7.1.v1", category: "productivity",
  upstream_path: "templates/compose/vikunja-with-postgresql.yaml",
  definition: {
    ...kuma.definition, container_port: 3456, readiness: { path: "/health", timeout_seconds: 120 },
    credential_names: ["SERVICE_PASSWORD_JWTSECRET", "SERVICE_PASSWORD_POSTGRESQL"],
    compose: { original: "services:\n  vikunja:\n    image: vikunja/vikunja@sha256:pinned\nvolumes:\n  vikunja-data: {}\n", public_component: "vikunja", container_port: 3456 },
  },
};
async function selectVikunja() {
  vi.mocked(api.getCatalog).mockResolvedValueOnce([template, vikunja]);
  setup();
  await screen.findByRole("option", { name: /Vikunja with PostgreSQL ·/ });
  fireEvent.change(screen.getByLabelText("Search catalog"), { target: { value: "vikunja-data" } });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: vikunja.id } });
  await screen.findByRole("option", { name: "Docker" });
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
}
test("Compose catalog previews exact source and retains the URL and secret identity across retry", async () => {
  await selectVikunja();
  const preview = screen.getByRole("region", { name: "Catalog installation preview" });
  expect(preview.querySelector("pre")?.textContent).toBe(vikunja.definition.compose!.original);
  expect(preview.textContent).toContain("Public componentvikunja");
  expect(preview.textContent).toContain("Runtime secrets are generated once");
  expect(preview.textContent).not.toContain("No login credentials are generated");
  fireEvent.change(screen.getByLabelText("Public URL"), { target: { value: "https://tasks.example.test" } });
  vi.mocked(api.installCatalog).mockRejectedValueOnce(Error("lost response")).mockImplementationOnce(async body => ({
    id: "res", request: body, template: vikunja,
    resource: { id: "res", project_id: "project", project_environment_id: "env", name: body.name, kind: "application", source: { kind: "compose", compose: vikunja.definition.compose! }, generation: 1, created_by: "actor", created_at: "", updated_at: "" },
    binding: { id: "binding", resource_id: "res", destination_id: "dest", runtime_namespace: "ncd", created_by: "actor", created_at: "" },
    storage_ref: { storage_id: "", generation: 0, hash: "" }, runtime_configuration: { generation: 1, hash: "runtime" },
    credentials: [{ name: "SERVICE_PASSWORD_POSTGRESQL", kind: "managed_secret", secret: { secret_id: "secret", version: 1 } }], created_by: "actor", created_at: "",
  }));
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  await screen.findByRole("button", { name: "Retry same installation" });
  expect(screen.getByLabelText<HTMLInputElement>("Public URL").disabled).toBe(true);
  const first = vi.mocked(api.installCatalog).mock.calls[0]![0];
  expect(first).toMatchObject({ template_id: vikunja.id, immutable_version: vikunja.immutable_version, public_url: "https://tasks.example.test" });
  fireEvent.click(screen.getByRole("button", { name: "Retry same installation" }));
  await screen.findByText(/Generated runtime secrets are saved as managed-secret references/);
  expect(vi.mocked(api.installCatalog).mock.calls[1]![0]).toBe(first);
  expect(screen.queryByRole("button", { name: "Reveal login credentials" })).toBeNull();
  expect(screen.queryByText(/Linkding creates the initial account/)).toBeNull();
  expect(revealManagedSecret).not.toHaveBeenCalled();
});
for (const url of ["", "ftp://tasks.example.test", "https://user:pass@tasks.example.test", "https://tasks.example.test?q=1", "https://tasks.example.test#part", "https://tasks.example.test/subpath", "https://tasks.example.test\\path"]) {
  test(`Compose catalog rejects an invalid public URL before freezing: ${url}`, async () => {
    await selectVikunja();
    fireEvent.change(screen.getByLabelText("Public URL"), { target: { value: url } });
    fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
    await screen.findByText("Enter a public HTTP or HTTPS base URL without credentials, a path, a query or a fragment.");
    expect(api.installCatalog).not.toHaveBeenCalled();
    expect(screen.getByLabelText<HTMLInputElement>("Public URL").disabled).toBe(false);
  });
}
test("switching from Compose to an image template omits the public URL", async () => {
  await selectVikunja();
  fireEvent.change(screen.getByLabelText("Public URL"), { target: { value: "http://tasks.example.test" } });
  fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  expect(screen.queryByLabelText("Public URL")).toBeNull();
  await screen.findByRole("option", { name: "Docker" });
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").value).toBe("");
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
  vi.mocked(api.installCatalog).mockRejectedValueOnce(Error("lost response"));
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  await screen.findByRole("button", { name: "Retry same installation" });
  expect(vi.mocked(api.installCatalog).mock.calls[0]![0]).not.toHaveProperty("public_url");
});

test("a changed pinned catalog hash cannot confirm installation and retains the exact attempt", async () => {
  await selectVikunja();
  fireEvent.change(screen.getByLabelText("Public URL"), { target: { value: "http://tasks.example.test" } });
  vi.mocked(api.installCatalog).mockImplementationOnce(async body => ({
    id: "res", request: body, template: { ...vikunja, definition_hash: "changed" },
    resource: { id: "res", project_id: "project", project_environment_id: "env", name: body.name, kind: "application", source: { kind: "compose", compose: vikunja.definition.compose! }, generation: 1, created_by: "actor", created_at: "", updated_at: "" },
    binding: { id: "binding", resource_id: "res", destination_id: "dest", runtime_namespace: "ncd", created_by: "actor", created_at: "" },
    storage_ref: { storage_id: "", generation: 0, hash: "" }, runtime_configuration: { generation: 1, hash: "runtime" }, credentials: [], created_by: "actor", created_at: "",
  }));
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  await screen.findByRole("button", { name: "Retry same installation" });
  expect(screen.getByRole("alert").textContent).toContain("Installation unconfirmed");
  expect(screen.queryByText(/Installed Vikunja/)).toBeNull();
  expect(vi.mocked(api.installCatalog).mock.calls[0]![0].public_url).toBe("http://tasks.example.test");
});


test("destination options are selected-template scoped, unavailable choices explain missing features and cannot install", async () => {
  const options = optionsFor(template);
  options.items.unshift({ ...options.items[0]!, destination: { ...options.items[0]!.destination, id: "unavailable", name: "Needs setup" }, available: false, reason_code: "missing_capabilities", missing_capabilities: ["hosting_owned_storage_runtime_v1"] });
  options.total = 2;
  vi.mocked(placement.getCatalogDestinationOptions).mockResolvedValue(options);
  setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  expect(placement.getCatalogDestinationOptions).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  const unavailable = await screen.findByRole<HTMLOptionElement>("option", { name: /Needs setup · Required deployment features/ });
  expect(unavailable.disabled).toBe(true);
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").value).toBe("");
  expect(await screen.findByText("Missing features: Persistent volume attachment")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Catalog application requirements" }).textContent).toContain("Available server capacity is unverified");
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "unavailable" } });
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Install application" }).disabled).toBe(false);
});

test("template change cancels stale options and resets selection before another application can install", async () => {
  const other = { ...template, id: "other", immutable_version: "other.v1", name: "Other" };
  vi.mocked(api.getCatalog).mockResolvedValue([template, other]);
  let finish!: (value: placement.CatalogDestinationOptions) => void;
  vi.mocked(placement.getCatalogDestinationOptions).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await waitFor(() => expect(placement.getCatalogDestinationOptions).toHaveBeenCalledTimes(1));
  const oldSignal = vi.mocked(placement.getCatalogDestinationOptions).mock.calls[0]![3];
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "other" } });
  await screen.findByRole("option", { name: "Docker" });
  expect(oldSignal?.aborted).toBe(true);
  const stale = optionsFor(template); stale.items[0]!.destination.name = "Stale old destination";
  await act(async () => finish(stale));
  expect(screen.queryByRole("option", { name: "Stale old destination" })).toBeNull();
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").value).toBe("");
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test("paging and explicit compatibility refresh clear the prior destination", async () => {
  vi.mocked(placement.getCatalogDestinationOptions).mockImplementation(async (_project, selected, offset) => ({ ...optionsFor(selected, offset), total: 21 }));
  await selectCatalogDraft();
  fireEvent.click(screen.getByRole("button", { name: "Next destinations" }));
  await waitFor(() => expect(vi.mocked(placement.getCatalogDestinationOptions).mock.calls.at(-1)![2]).toBe(20));
  await screen.findByRole("option", { name: "Docker" });
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").value).toBe("");
  fireEvent.change(screen.getByLabelText("Catalog destination"), { target: { value: "dest" } });
  const unavailable = optionsFor(template, 20); unavailable.total = 21; unavailable.items[0]!.available = false; unavailable.items[0]!.reason_code = "runner_unavailable";
  vi.mocked(placement.getCatalogDestinationOptions).mockResolvedValueOnce(unavailable);
  fireEvent.click(screen.getByRole("button", { name: "Refresh destination compatibility" }));
  await screen.findByRole("option", { name: /Docker · The server is offline/ });
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog destination").value).toBe("");
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test("project identity change retires options and clears the previous draft", async () => {
  let finish!: (value: placement.CatalogDestinationOptions) => void;
  vi.mocked(placement.getCatalogDestinationOptions).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const view = setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await waitFor(() => expect(placement.getCatalogDestinationOptions).toHaveBeenCalledTimes(1));
  const oldSignal = vi.mocked(placement.getCatalogDestinationOptions).mock.calls[0]![3];
  view.rerender(<QueryClientProvider client={client}><HostingCatalog projectID="newproject" environmentID="newenv" blocked={false} /></QueryClientProvider>);
  expect(oldSignal?.aborted).toBe(true);
  await act(async () => finish(optionsFor(template)));
  await screen.findByRole("option", { name: /Linkding ·/ });
  expect(screen.getByLabelText<HTMLSelectElement>("Catalog application").value).toBe("");
  expect(screen.queryByRole("option", { name: "Docker" })).toBeNull();
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await screen.findByRole("option", { name: "Docker" });
  expect(vi.mocked(placement.getCatalogDestinationOptions).mock.calls.at(-1)![0]).toBe("newproject");
  expect(api.installCatalog).not.toHaveBeenCalled();
});

test.each([401, 403])("denied compatibility %i leaves no installable destination", async status => {
  vi.mocked(placement.getCatalogDestinationOptions).mockRejectedValueOnce(new ApiError(status, "denied", null));
  setup();
  await screen.findByRole("option", { name: /Linkding ·/ });
  fireEvent.change(screen.getByLabelText("Catalog application"), { target: { value: "linkding" } });
  await screen.findByText("Catalog or destination access is unavailable.");
  expect(screen.queryByRole("option", { name: "Docker" })).toBeNull();
  fireEvent.submit(screen.getByRole("form", { name: "Install catalog application" }));
  expect(api.installCatalog).not.toHaveBeenCalled();
});
