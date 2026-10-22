import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiError, type Principal, type ProjectMember } from "@/api";
import * as api from "@/api/managed-secrets";
import { ProjectSecrets } from "./ProjectSecrets";

vi.mock("@/api/managed-secrets", () => ({ listManagedSecrets: vi.fn(), getManagedSecret: vi.fn(), createManagedSecret: vi.fn(), listManagedSecretVersions: vi.fn(), rotateManagedSecret: vi.fn(), revokeManagedSecret: vi.fn(), revealManagedSecret: vi.fn() }));
const principal: Principal = { id: "user", roles: [], name: "User", email: "user@example.invalid", provider: "oidc" };
const projects = ["one", "two"].map((id) => ({ id, name: `Project ${id}`, description: "", created_at: "2026-01-01T00:00:00Z" }));
const secret: api.ManagedSecret = { id: "secret", project_id: "one", name: "Database", current_version: 2, status: "active", created_by: "user", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
const version = (number: number) => ({ secret_id: secret.id, version: number, created_by: "user", created_at: secret.created_at });
const page = <T,>(items: T[], offset = 0, total = items.length) => ({ items, limit: 20, offset, count: items.length, total });
let clients: QueryClient[] = [];
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.listManagedSecrets).mockResolvedValue(page([secret]));
  vi.mocked(api.getManagedSecret).mockResolvedValue(secret);
  vi.mocked(api.listManagedSecretVersions).mockResolvedValue(page([version(2), version(1)]));
});
afterEach(() => { cleanup(); for (const client of clients) client.clear(); clients = []; vi.unstubAllGlobals(); });
function setup(role: ProjectMember["role"] = "owner") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client);
  const members: ProjectMember[] = [{ id: "member", project_id: "one", user_id: "user", role, name: principal.name, email: principal.email, created_at: secret.created_at, updated_at: secret.updated_at }];
  const view = render(<QueryClientProvider client={client}><ProjectSecrets projects={projects} principal={principal} members={members} /></QueryClientProvider>);
  fireEvent.change(screen.getByLabelText("Secrets project"), { target: { value: "one" } });
  return { client, members, ...view };
}
function pasteValue(label: string, value: string) { fireEvent.paste(screen.getByLabelText(label), { clipboardData: { getData: () => value } }); }
async function detail() { fireEvent.click(await screen.findByRole("button", { name: "Manage Database" })); await screen.findByText("Version 1"); return screen.getByRole("region", { name: "Secret Database" }); }
async function reveal() { const region = await detail(); fireEvent.click(within(region).getByRole("button", { name: "Select version 1 to reveal" })); fireEvent.click(screen.getByRole("button", { name: "Reveal value for version 1" })); return region; }

test.each(["viewer", "maintainer", "owner"] as const)("project %s gets only its permitted secret controls", async (role) => {
  setup(role); const region = await detail();
  expect(Boolean(screen.queryByRole("button", { name: "Create secret" }))).toBe(role !== "viewer");
  expect(Boolean(within(region).queryByRole("button", { name: "Rotate secret" }))).toBe(role !== "viewer");
  expect(Boolean(within(region).queryByRole("button", { name: "Revoke Database" }))).toBe(role === "owner");
  expect(Boolean(within(region).queryByRole("button", { name: "Select version 1 to reveal" }))).toBe(role === "owner");
  expect(api.revealManagedSecret).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Secrets project"), { target: { value: "two" } });
  await screen.findByText(/You have metadata access/);
  expect(screen.queryByRole("button", { name: "Create secret" })).toBeNull();
});

test("create uses masked input, clears it on success, and never reveals or caches plaintext", async () => {
  const { client } = setup(); await screen.findByRole("button", { name: "Manage Database" });
  vi.mocked(api.createManagedSecret).mockResolvedValue(secret);
  const value = screen.getByLabelText<HTMLTextAreaElement>("Secret value");
  expect(value.readOnly).toBe(true);
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "Database" } });
  pasteValue("Secret value", "  test-only-value  ");
  fireEvent.submit(screen.getByRole("form", { name: "Create managed secret" }));
  await screen.findByText("Secret created. Its value is hidden.");
  expect(api.createManagedSecret).toHaveBeenCalledWith({ project_id: "one", name: "Database", value: "  test-only-value  " });
  expect(value.value).toBe("");
  expect(api.revealManagedSecret).not.toHaveBeenCalled();
  expect(JSON.stringify(client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain("test-only-value");
});

test.each(["LF", "CRLF", "mixed", "large"])("create and rotate preserve exact %s secret values and clear entry afterward", async (kind) => {
  setup(); await screen.findByRole("button", { name: "Manage Database" });
  const newline = kind === "CRLF" ? "\r\n" : "\n";
  const original = kind === "large" ? "key-line\r\n".repeat(6000) : `-----BEGIN TEST-----${newline}  content  ${kind === "mixed" ? "\r\n" : newline}-----END TEST-----${newline}${newline}  `;
  const rotated = `\ufeff${original} `;
  vi.mocked(api.createManagedSecret).mockResolvedValue(secret);
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "Exact value" } });
  pasteValue("Secret value", original);
  fireEvent.submit(screen.getByRole("form", { name: "Create managed secret" }));
  await screen.findByText("Secret created. Its value is hidden.");
  expect(api.createManagedSecret).toHaveBeenLastCalledWith({ project_id: "one", name: "Exact value", value: original });
  expect(screen.getByLabelText<HTMLTextAreaElement>("Secret value").value).toBe("");
  expect(screen.getByLabelText<HTMLTextAreaElement>("Secret value").readOnly).toBe(true);
  await screen.findByText("Version 1");
  vi.mocked(api.rotateManagedSecret).mockResolvedValue({ ...secret, current_version: 3 });
  pasteValue("New secret value", rotated);
  fireEvent.submit(screen.getByRole("form", { name: "Rotate secret" }));
  await screen.findByText("Secret rotated. The new value is hidden.");
  expect(api.rotateManagedSecret).toHaveBeenLastCalledWith(secret.id, { expected_version: 2, value: rotated });
  expect(screen.getByLabelText<HTMLTextAreaElement>("New secret value").value).toBe("");
  expect(api.revealManagedSecret).not.toHaveBeenCalled();
});

test.each([400, 403, 409, 503])("create failure %s preserves the name, clears sensitive input, and never echoes server values", async (status) => {
  setup(); await screen.findByRole("button", { name: "Manage Database" });
  vi.mocked(api.createManagedSecret).mockRejectedValue(new ApiError(status, "private-server-value", null));
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "Draft name" } });
  pasteValue("Secret value", "private-input-value");
  fireEvent.submit(screen.getByRole("form", { name: "Create managed secret" }));
  await screen.findByRole("alert");
  expect(screen.getByLabelText<HTMLInputElement>("Secret name").value).toBe("Draft name");
  expect(screen.getByLabelText<HTMLInputElement>("Secret value").value).toBe("");
  expect(screen.queryByText(/private-server-value/)).toBeNull();
  if (status === 503) expect(screen.getByRole("alert").textContent).toContain("keyring is unavailable");
});

test("explicit historical reveal stays out of caches and storage and clears on close", async () => {
  const { client } = setup();
  const clipboard = vi.fn().mockResolvedValue(undefined); vi.stubGlobal("navigator", { clipboard: { writeText: clipboard } });
  vi.mocked(api.revealManagedSecret).mockResolvedValue({ secret_id: secret.id, version: 1, value: "historical-test-value" });
  await reveal();
  await screen.findByLabelText("Revealed secret value");
  expect(api.revealManagedSecret).toHaveBeenCalledWith(secret.id, 1, expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(clipboard).not.toHaveBeenCalled();
  expect(JSON.stringify(client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain("historical-test-value");
  expect(JSON.stringify(localStorage)).not.toContain("historical-test-value");
  expect(JSON.stringify(sessionStorage)).not.toContain("historical-test-value");
  expect(window.location.href).not.toContain("historical-test-value");
  fireEvent.click(screen.getByRole("button", { name: "Copy revealed value" }));
  await waitFor(() => expect(clipboard).toHaveBeenCalledWith("historical-test-value"));
  fireEvent.click(screen.getByRole("button", { name: "Close reveal" }));
  expect(screen.queryByText("historical-test-value")).toBeNull();
});

test.each(["project", "close", "version", "unmount"])("late reveal response is ignored after %s context ends", async (context) => {
  let resolve!: (value: Awaited<ReturnType<typeof api.revealManagedSecret>>) => void;
  vi.mocked(api.revealManagedSecret).mockImplementation(() => new Promise((done) => { resolve = done; }));
  const view = setup(); await reveal();
  const signal = vi.mocked(api.revealManagedSecret).mock.calls[0]![2]!.signal!;
  if (context === "project") fireEvent.change(screen.getByLabelText("Secrets project"), { target: { value: "two" } });
  if (context === "close") fireEvent.click(screen.getByRole("button", { name: "Close reveal" }));
  if (context === "version") fireEvent.click(screen.getByRole("button", { name: "Select version 2 to reveal" }));
  if (context === "unmount") view.unmount();
  await act(async () => resolve({ secret_id: secret.id, version: 1, value: "stale-test-value" }));
  expect(signal.aborted).toBe(true);
  expect(screen.queryByText("stale-test-value")).toBeNull();
});

test("mismatched reveal response never displays plaintext", async () => {
  setup(); vi.mocked(api.revealManagedSecret).mockResolvedValue({ secret_id: "other", version: 1, value: "wrong-test-value" });
  await reveal();
  await screen.findByText("The reveal response did not match the selected secret version.");
  expect(screen.queryByText("wrong-test-value")).toBeNull();
});

test("opening secret details and changing reveal version clear unrelated sensitive drafts", async () => {
  setup(); await screen.findByRole("button", { name: "Manage Database" });
  pasteValue("Secret value", "create-draft-test-value");
  const region = await detail();
  expect(screen.getByLabelText<HTMLInputElement>("Secret value").value).toBe("");
  pasteValue("New secret value", "rotate-draft-test-value");
  fireEvent.click(within(region).getByRole("button", { name: "Select version 1 to reveal" }));
  expect(screen.getByLabelText<HTMLInputElement>("New secret value").value).toBe("");
  expect(api.revealManagedSecret).not.toHaveBeenCalled();
});

test("rotation conflict refetches metadata and requires explicit current-version review and re-entry", async () => {
  setup(); const region = await detail();
  vi.mocked(api.rotateManagedSecret).mockRejectedValueOnce(new ApiError(409, "stale", null)).mockResolvedValueOnce({ ...secret, current_version: 4 });
  vi.mocked(api.getManagedSecret).mockResolvedValue({ ...secret, current_version: 3 });
  pasteValue("New secret value", "rotation-test-value");
  fireEvent.submit(screen.getByRole("form", { name: "Rotate secret" }));
  await screen.findByRole("button", { name: "Use current version 3" });
  expect(screen.getByLabelText<HTMLInputElement>("New secret value").value).toBe("");
  expect(within(region).getByRole<HTMLButtonElement>("button", { name: "Rotate secret" }).disabled).toBe(true);
  expect(api.rotateManagedSecret).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Use current version 3" }));
  expect(api.rotateManagedSecret).toHaveBeenCalledTimes(1);
  pasteValue("New secret value", "retry-test-value");
  fireEvent.submit(screen.getByRole("form", { name: "Rotate secret" }));
  await screen.findByText("Secret rotated. The new value is hidden.");
  expect(api.rotateManagedSecret).toHaveBeenLastCalledWith(secret.id, { expected_version: 3, value: "retry-test-value" });
});

test("revoking names the secret, requires confirmation, and clears a revealed value immediately", async () => {
  setup(); vi.mocked(api.revealManagedSecret).mockResolvedValue({ secret_id: secret.id, version: 1, value: "revoke-test-value" });
  await reveal(); await screen.findByText("revoke-test-value");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Revoke Database" }).disabled).toBe(true);
  vi.mocked(api.revokeManagedSecret).mockResolvedValue({ ...secret, status: "revoked" });
  vi.mocked(api.getManagedSecret).mockResolvedValue({ ...secret, status: "revoked" });
  fireEvent.click(screen.getByRole("checkbox", { name: "I understand that revoking Database is irreversible." }));
  fireEvent.submit(screen.getByRole("form", { name: "Revoke Database" }));
  expect(screen.queryByText("revoke-test-value")).toBeNull();
  await screen.findByText("Secret revoked. Its metadata and version history are retained.");
  expect(api.revokeManagedSecret).toHaveBeenCalledWith(secret.id, { expected_version: 2 });
  expect(screen.queryByRole("button", { name: "Select version 1 to reveal" })).toBeNull();
  expect(screen.getByText("Version 1")).toBeTruthy();
});

test("metadata and history pagination are bounded and late project responses stay scoped", async () => {
  vi.mocked(api.listManagedSecrets).mockImplementation(async ({ offset = 0 }) => page([secret], offset, 41));
  vi.mocked(api.listManagedSecretVersions).mockImplementation(async (_id, { offset = 0 }) => page([version(1)], offset, 41));
  setup(); await detail();
  fireEvent.click(screen.getByRole("button", { name: "Next managed secrets" }));
  await waitFor(() => expect(api.listManagedSecrets).toHaveBeenCalledWith({ project_id: "one", limit: 20, offset: 20 }, expect.anything()));
  fireEvent.click(screen.getByRole("button", { name: "Next secret versions" }));
  await waitFor(() => expect(api.listManagedSecretVersions).toHaveBeenCalledWith(secret.id, { limit: 20, offset: 20 }, expect.anything()));
});

test("project switch clears password input and ignores delayed metadata from the previous project", async () => {
  let resolve!: (value: Awaited<ReturnType<typeof api.listManagedSecrets>>) => void;
  vi.mocked(api.listManagedSecrets).mockImplementation(({ project_id }) => project_id === "one" ? new Promise((done) => { resolve = done; }) : Promise.resolve(page([])));
  setup();
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "Unsaved name" } });
  pasteValue("Secret value", "switch-test-value");
  fireEvent.change(screen.getByLabelText("Secrets project"), { target: { value: "two" } });
  await screen.findByText("No managed secrets in this project.");
  await act(async () => resolve(page([secret])));
  expect(screen.queryByRole("button", { name: "Manage Database" })).toBeNull();
  fireEvent.change(screen.getByLabelText("Secrets project"), { target: { value: "one" } });
  expect(screen.getByLabelText<HTMLInputElement>("Secret value").value).toBe("");
  expect(screen.getByLabelText<HTMLInputElement>("Secret name").value).toBe("");
});

test("loss of owner access aborts a pending reveal and never restores its value", async () => {
  let resolve!: (value: Awaited<ReturnType<typeof api.revealManagedSecret>>) => void;
  vi.mocked(api.revealManagedSecret).mockImplementation(() => new Promise((done) => { resolve = done; }));
  const { client, members, rerender } = setup(); await reveal();
  const signal = vi.mocked(api.revealManagedSecret).mock.calls[0]![2]!.signal!;
  rerender(<QueryClientProvider client={client}><ProjectSecrets projects={projects} principal={principal} members={members.map((member) => ({ ...member, role: "viewer" }))} /></QueryClientProvider>);
  await act(async () => resolve({ secret_id: secret.id, version: 1, value: "lost-owner-test-value" }));
  expect(signal.aborted).toBe(true);
  expect(screen.queryByText("lost-owner-test-value")).toBeNull();
  expect(screen.queryByRole("button", { name: "Reveal value for version 1" })).toBeNull();
});

test("conflict metadata failure requires refresh, then revoke requires renewed explicit confirmation", async () => {
  setup(); await detail();
  vi.mocked(api.revokeManagedSecret).mockRejectedValue(new ApiError(409, "stale", null));
  vi.mocked(api.getManagedSecret).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ ...secret, current_version: 3 });
  fireEvent.click(screen.getByRole("checkbox", { name: "I understand that revoking Database is irreversible." }));
  fireEvent.submit(screen.getByRole("form", { name: "Revoke Database" }));
  await screen.findByText("Unable to refresh the current secret. Retry metadata before continuing.");
  expect(screen.queryByRole("button", { name: "Use current version 2" })).toBeNull();
  expect(api.revokeManagedSecret).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Refresh current secret" }));
  fireEvent.click(await screen.findByRole("button", { name: "Use current version 3" }));
  expect(screen.getByRole<HTMLInputElement>("checkbox").checked).toBe(false);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Revoke Database" }).disabled).toBe(true);
  expect(api.revokeManagedSecret).toHaveBeenCalledTimes(1);
});
