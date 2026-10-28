import { gitBuildVariablesError } from "./buildSettingsValidation";
import { useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { GitApplicationFields, type GitApplicationDraft } from "./GitApplicationFields";
import { emptyGitDraft } from "./gitApplicationDraft";
afterEach(cleanup);
function Editor({ initial = emptyGitDraft(), disabled = false }: { initial?: GitApplicationDraft; disabled?: boolean } = {}) { const [value, setValue] = useState<GitApplicationDraft>(initial); return <><GitApplicationFields value={value} onChange={setValue} disabled={disabled} /><output>{JSON.stringify(value)}</output></>; }
test("public is explicit by default and private access survives all builder switches", () => { render(<Editor />); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("public"); fireEvent.change(screen.getByLabelText("Git source access"), { target: { value: "https_basic" } }); for (const strategy of ["dockerfile", "railpack", "static", "compose"]) { fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } }); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("https_basic"); expect(screen.getByRole("status").textContent).toContain(`"credential_kind":"https_basic"`); } expect(screen.queryByLabelText(/password|secret value/i)).toBeNull(); });


test("Compose fields use context-relative paths and clear other builder settings", () => {
  const initial = { repository_url: "https://github.com/team/app", requested_ref: "release", credential_kind: "https_basic", settings: { strategy: "dockerfile", context_dir: "services/api", dockerfile_path: "build/Dockerfile", build_target: "production" } } as GitApplicationDraft;
  render(<Editor initial={initial} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
  expect(screen.queryByLabelText("Dockerfile path")).toBeNull();
  expect(screen.getByLabelText<HTMLInputElement>("Compose file").required).toBe(true);
  expect(screen.getByLabelText<HTMLInputElement>("Public Compose service").required).toBe(true);
  fireEvent.change(screen.getByLabelText("Compose file"), { target: { value: "deploy/compose.yaml" } });
  fireEvent.change(screen.getByLabelText("Public Compose service"), { target: { value: "web" } });
  fireEvent.change(screen.getByLabelText("Public Compose service port"), { target: { value: "3456" } });
  expect(screen.getByText("services/api/deploy/compose.yaml")).toBeTruthy();
  const draft = JSON.parse(screen.getByRole("status").textContent!);
  expect(draft).toEqual({ ...initial, settings: { strategy: "compose", context_dir: "services/api", compose_file: "deploy/compose.yaml", public_component: "web", container_port: 3456 } });
});

test.each(["dockerfile", "railpack", "static"])("switching Compose to %s omits all Compose fields", strategy => {
  const initial = { ...emptyGitDraft(), settings: { strategy: "compose", context_dir: "app", compose_file: "compose.yaml", public_component: "web", container_port: 80 } } as GitApplicationDraft;
  render(<Editor initial={initial} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).toEqual({ strategy, context_dir: "app" });
  expect(screen.queryByLabelText("Compose file")).toBeNull();
  expect(screen.queryByLabelText("Public Compose service")).toBeNull();
});

test("switching static to Compose removes publish and SPA settings", () => {
  render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "static", publish_dir: "dist", spa_fallback: true } }} />);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).toEqual({ strategy: "compose", compose_file: "docker-compose.yaml", public_component: "", container_port: 80 });
  expect(screen.getByText("docker-compose.yaml")).toBeTruthy();
});

test("saved Compose settings remain readable and locked while source writes are disabled", () => {
  render(<Editor disabled initial={{ ...emptyGitDraft(), settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 8080 } }} />);
  for (const label of ["Compose file", "Public Compose service", "Public Compose service port", "Git branch, tag, or commit", "Context directory"]) expect(screen.getByLabelText<HTMLInputElement>(label).disabled).toBe(true);
});

test("Compose build variable rows retain literal and empty values and omit an empty collection", () => {
  render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 80 } }} />);
  fireEvent.click(screen.getByRole("button", { name: "Add build variable" }));
  fireEvent.change(screen.getByLabelText("Build variable name 1"), { target: { value: "APP_MODE" } });
  fireEvent.change(screen.getByLabelText("Build variable value 1"), { target: { value: "  ${LITERAL}\nsecond line  " } });
  fireEvent.click(screen.getByRole("button", { name: "Add build variable" }));
  fireEvent.change(screen.getByLabelText("Build variable name 2"), { target: { value: "EMPTY" } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings.build_variables).toEqual([{ name: "APP_MODE", value: "  ${LITERAL}\nsecond line  " }, { name: "EMPTY", value: "" }]);
  fireEvent.click(screen.getByRole("button", { name: "Remove build variable 2" }));
  fireEvent.click(screen.getByRole("button", { name: "Remove build variable 1" }));
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).not.toHaveProperty("build_variables");
});
test("build variables survive supported builder switches and clear for unsupported builders", () => {
  const variables = Array.from({ length: 64 }, (_, index) => ({ name: `ARG_${index}`, value: "value" }));
  const view = render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 80, build_variables: variables } }} />);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Add build variable" }).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "dockerfile" } });
  expect(screen.queryByRole("region", { name: "Compose build variables" })).toBeNull();
  expect(screen.getByRole("region", { name: "Dockerfile build variables" })).toBeTruthy();
  expect(JSON.parse(screen.getByRole("status").textContent!).settings.build_variables).toEqual(variables);
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "railpack" } });
  expect(JSON.parse(screen.getByRole("status").textContent!).settings).not.toHaveProperty("build_variables");
  expect(screen.queryByRole("region", { name: "Dockerfile build variables" })).toBeNull();
  view.unmount(); render(<Editor disabled initial={{ ...emptyGitDraft(), settings: { strategy: "compose", compose_file: "compose.yaml", public_component: "web", container_port: 80, build_variables: [{ name: "MODE", value: "production" }] } }} />);
  expect(screen.getByLabelText("Build variable value 1").closest("fieldset")!.disabled).toBe(true);
});

test.each([
 { name: "bad-name", value: "value" }, { name: "HTTP_PROXY", value: "value" }, { name: "https_proxy", value: "value" },
 { name: "NeRoCd_config", value: "value" }, { name: "BUILDKIT_SYNTAX", value: "value" }, { name: "buildx_config", value: "value" },
 { name: "TARGETPLATFORM", value: "value" }, { name: "A", value: "\0" },
 { name: "A", value: "é".repeat(2049) },
])("build variables reject invalid or reserved public argument %#", variable => {
 expect(gitBuildVariablesError({ strategy: "compose", build_variables: [variable] })).not.toBe("");
});
test("build variable validation preserves case-sensitive names, empty values and byte limits", () => {
 expect(gitBuildVariablesError({ strategy: "compose", build_variables: [{ name: "PUBLIC", value: "" }, { name: "public", value: "é".repeat(2048) }, { name: "MULTILINE", value: "line1\r\nline2\nline3" }] })).toBe("");
 expect(gitBuildVariablesError({ strategy: "compose", build_variables: [{ name: "PUBLIC", value: "" }, { name: "PUBLIC", value: "other" }] })).toMatch(/unique/);
 expect(gitBuildVariablesError({ strategy: "compose", build_variables: Array.from({ length: 65 }, (_, index) => ({ name: `ARG_${index}`, value: "" })) })).toMatch(/64/);
});

test.each([undefined, null, true, false])("Dockerfile injection setting %j preserves the enabled default and explicit opt-out", injection => {
 render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "dockerfile", build_variables: [{ name: "MODE", value: "release" }], ...(injection !== undefined ? { inject_build_args_to_dockerfile: injection } : {}) } }} />);
 const control = screen.getByRole<HTMLInputElement>("checkbox", { name: "Inject build arguments into Dockerfile" });
 expect(control.checked).toBe(injection !== false);
 fireEvent.click(control);
 expect(JSON.parse(screen.getByRole("status").textContent!).settings.inject_build_args_to_dockerfile).toBe(injection === false);
 fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
 expect(JSON.parse(screen.getByRole("status").textContent!).settings).not.toHaveProperty("inject_build_args_to_dockerfile");
 expect(JSON.parse(screen.getByRole("status").textContent!).settings.build_variables).toEqual([{ name: "MODE", value: "release" }]);
 expect(screen.queryByRole("checkbox", { name: "Inject build arguments into Dockerfile" })).toBeNull();
});

test.each(["railpack", "static"])("switching to %s clears the build registry credential", strategy => {
 render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "dockerfile", build_registry_credential: { secret_id: "credential", version: 2 } } }} />);
 fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } });
 expect(JSON.parse(screen.getByRole("status").textContent!).settings).not.toHaveProperty("build_registry_credential");
});


test.each(["bad-name", "HTTP_PROXY", "https_proxy", "NeRoCd_config", "BUILDKIT_SYNTAX", "buildx_config", "TARGETPLATFORM"])("build secrets reject invalid or reserved name %s", name => {
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: [{ name, secret: { secret_id: "secret", version: 1 } }] })).not.toBe("");
});
test("build secrets enforce unique names, public argument separation and exact version selection", () => {
 const row = { name: "TOKEN", secret: { secret_id: "secret", version: 1 } };
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: [row] })).toBe("");
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: [row, row] })).toMatch(/unique/);
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: [row], build_variables: [{ name: "TOKEN", value: "public" }] })).toMatch(/differ/);
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: [{ ...row, secret: { secret_id: "secret", version: 0 } }] })).toMatch(/explicit version/);
 expect(gitBuildVariablesError({ strategy: "dockerfile", build_secrets: Array.from({ length: 65 }, (_, i) => ({ ...row, name: `TOKEN_${i}` })) })).toMatch(/64/);
});
test.each(["railpack", "static"])("build secrets survive Dockerfile and Compose changes and clear for %s", strategy => {
 const refs = [{ name: "TOKEN", secret: { secret_id: "secret", version: 2 } }];
 render(<Editor initial={{ ...emptyGitDraft(), settings: { strategy: "dockerfile", build_secrets: refs } }} />);
 fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "compose" } });
 expect(JSON.parse(screen.getByRole("status").textContent!).settings.build_secrets).toEqual(refs);
 fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: "dockerfile" } });
 expect(JSON.parse(screen.getByRole("status").textContent!).settings.build_secrets).toEqual(refs);
 fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } });
 expect(JSON.parse(screen.getByRole("status").textContent!).settings).not.toHaveProperty("build_secrets");
});


vi.mock("@/api/managed-secrets", () => ({ listManagedSecrets: vi.fn(), listManagedSecretVersions: vi.fn(), createManagedSecret: vi.fn() }));
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { queryKeys } from "@/api";
import * as secretAPI from "@/api/managed-secrets";
const metadataPage = <T,>(items: T[], offset = 0, total = items.length) => ({ items, offset, limit: 20, count: items.length, total });
function SecretEditor({ initial }: { initial: GitApplicationDraft }) {
 const [value, setValue] = useState(initial);
 const [valid, setValid] = useState(false);
 const [project, setProject] = useState("project");
 return <><GitApplicationFields value={value} onChange={setValue} disabled={false} projectID={project} onSecretsValidityChange={setValid} /><output data-testid="secret-valid">{String(valid)}</output><output data-testid="secret-draft">{JSON.stringify(value)}</output><button onClick={() => setValue({ ...initial, settings: { strategy: "dockerfile", build_secrets: [{ name: "RESET", secret: { secret_id: "first", version: 1 } }] } })}>Reset draft</button><button onClick={() => setProject("other")}>Switch project</button></>;
}
function setupSecrets(initial: GitApplicationDraft) {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
 client.setQueryData(queryKeys.principal, { id: "operator", provider: "oidc", roles: [] });
 const view = render(<QueryClientProvider client={client}><SecretEditor initial={initial} /></QueryClientProvider>);
 return { ...view, client };
}
function pagedSecretMetadata() {
 vi.mocked(secretAPI.listManagedSecrets).mockImplementation(async ({ offset = 0, project_id }) => metadataPage([{ id: offset ? "later" : "first", project_id: project_id!, name: offset ? "Later secret" : "First secret", status: "active", current_version: 21, created_by: "operator", created_at: "", updated_at: "" }], offset, 21));
 vi.mocked(secretAPI.listManagedSecretVersions).mockImplementation(async (secret_id, { offset = 0 }) => metadataPage([{ secret_id, version: offset ? 21 : 1, created_by: "operator", created_at: "" }], offset, 21));
}
test("build secret selections retain page two metadata and survive removing an earlier row", async () => {
 pagedSecretMetadata();
 const { client, unmount } = setupSecrets({ ...emptyGitDraft(), settings: { strategy: "dockerfile", build_secrets: [{ name: "FIRST", secret: { secret_id: "first", version: 1 } }, { name: "LATER", secret: { secret_id: "", version: 0 } }] } });
 const region = screen.getByRole("region", { name: "Build secret 2" });
 await within(region).findByRole("option", { name: "First secret · first" });
 fireEvent.click(within(region).getByRole("button", { name: "Next Build secret 2 secrets" }));
 await within(region).findByRole("option", { name: "Later secret · later" });
 fireEvent.change(within(region).getByLabelText("Build secret 2 secret"), { target: { value: "later" } });
 await within(region).findByRole("option", { name: "Version 1" });
 fireEvent.click(within(region).getByRole("button", { name: "Next Build secret 2 versions" }));
 await within(region).findByRole("option", { name: "Version 21" });
 fireEvent.change(within(region).getByLabelText("Build secret 2 version"), { target: { value: "21" } });
 await waitFor(() => expect(screen.getByTestId("secret-valid").textContent).toBe("true"));
 expect(screen.getByLabelText<HTMLSelectElement>("Build secret 2 secret").value).toBe("later");
 expect(screen.getByLabelText<HTMLSelectElement>("Build secret 2 version").value).toBe("21");
 fireEvent.click(screen.getByRole("button", { name: "Remove build secret 1" }));
 await waitFor(() => expect(screen.getByTestId("secret-valid").textContent).toBe("true"));
 expect(screen.getByLabelText<HTMLSelectElement>("Build secret 1 secret").value).toBe("later");
 expect(screen.getByLabelText<HTMLSelectElement>("Build secret 1 version").value).toBe("21");
 expect(JSON.parse(screen.getByTestId("secret-draft").textContent!).settings.build_secrets).toEqual([{ name: "LATER", secret: { secret_id: "later", version: 21 } }]);
 fireEvent.click(screen.getByRole("button", { name: "Reset draft" }));
 await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Build secret 1 secret").value).toBe("first"));
 expect(screen.getByLabelText<HTMLSelectElement>("Build secret 1 version").value).toBe("1");
 await waitFor(() => expect(screen.getByTestId("secret-valid").textContent).toBe("true"));
 fireEvent.click(screen.getByRole("button", { name: "Switch project" }));
 await waitFor(() => expect(secretAPI.listManagedSecrets).toHaveBeenCalledWith(expect.objectContaining({ project_id: "other" }), expect.anything()));
 await waitFor(() => expect(screen.getByLabelText<HTMLSelectElement>("Build secret 1 secret").value).toBe("first"));
 await waitFor(() => expect(screen.getByTestId("secret-valid").textContent).toBe("true"));
 const calls = vi.mocked(secretAPI.listManagedSecrets).mock.calls.length;
 vi.mocked(secretAPI.listManagedSecrets).mockResolvedValue(metadataPage([]));
 act(() => client.setQueryData(queryKeys.principal, { id: "different-operator", provider: "oidc", roles: [] }));
 await waitFor(() => expect(vi.mocked(secretAPI.listManagedSecrets).mock.calls.length).toBeGreaterThan(calls));
 await waitFor(() => expect(screen.getByTestId("secret-valid").textContent).toBe("false"));
 expect(screen.queryByRole("option", { name: "First secret · first" })).toBeNull();
 unmount(); client.clear();
});


test("SSH deploy-key fields retain repository syntax and fingerprints across builders and omit pins for HTTPS", () => {
 render(<Editor />);
 fireEvent.change(screen.getByLabelText("Git source access"), { target: { value: "ssh_private_key" } });
 const repository = screen.getByLabelText<HTMLInputElement>("Git repository URL");
 expect(repository.type).toBe("text");
 fireEvent.change(repository, { target: { value: "git@git.example:team/application.git" } });
 const fingerprints = ["SHA256:" + "A".repeat(43), "SHA256:" + "B".repeat(43)];
 fireEvent.change(screen.getByLabelText("SSH host fingerprints"), { target: { value: fingerprints.join("\n") } });
 for (const strategy of ["dockerfile", "railpack", "static", "compose"]) {
  fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } });
  expect(JSON.parse(screen.getByRole("status").textContent!)).toMatchObject({ repository_url: "git@git.example:team/application.git", credential_kind: "ssh_private_key", ssh_host_fingerprints: fingerprints });
 }
 expect(screen.getByText(/raw, unencrypted PEM or OpenSSH private key/)).toBeTruthy();
 expect(screen.queryByLabelText(/private key value/i)).toBeNull();
 fireEvent.change(screen.getByLabelText("Git source access"), { target: { value: "https_basic" } });
 expect(screen.queryByLabelText("SSH host fingerprints")).toBeNull();
 expect(JSON.parse(screen.getByRole("status").textContent!)).not.toHaveProperty("ssh_host_fingerprints");
 expect(repository.type).toBe("url");
});
