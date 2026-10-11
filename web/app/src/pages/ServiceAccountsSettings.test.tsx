import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { ServiceAccount } from "@/api/service-accounts";
import { ServiceAccountsSettingsPanel } from "./ServiceAccountsSettings";

const serviceAccountsAPI = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  rotate: vi.fn(),
  revoke: vi.fn(),
}));
vi.mock("@/api/service-accounts", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api/service-accounts")>(),
  listServiceAccounts: serviceAccountsAPI.list,
  createServiceAccount: serviceAccountsAPI.create,
  updateServiceAccount: serviceAccountsAPI.update,
  rotateServiceAccountCredential: serviceAccountsAPI.rotate,
  revokeServiceAccount: serviceAccountsAPI.revoke,
}));

const projects = [
  { id: "proj_one", name: "One", description: "", created_at: "2026-09-05T10:00:00Z" },
  { id: "proj_two", name: "Two", description: "", created_at: "2026-09-05T10:00:00Z" },
];
const account: ServiceAccount = { id: "svcacct_one", project_id: "proj_one", owner_user_id: "usr_owner", name: "Build bot", role: "maintainer", status: "active", created_at: "2026-09-05T10:00:00Z", updated_at: "2026-09-05T10:00:00Z" };

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { ...render(<QueryClientProvider client={client}><ServiceAccountsSettingsPanel projects={projects} /></QueryClientProvider>), client };
}

test("creates and clears one-time credential on close and project navigation", async () => {
  serviceAccountsAPI.list.mockResolvedValue({ items: [], count: 0, limit: 10, offset: 0, total: 0 });
  serviceAccountsAPI.create.mockResolvedValue({ service_account: account, credential: { id: "pat_one", name: account.name, kind: "service_account", roles: [], status: "active", created_by: account.owner_user_id, service_account_id: account.id, created_at: account.created_at, expires_at: "2026-12-04T10:00:00Z" }, token: "nca_one_time_secret" });
  renderPanel();
  await screen.findByText("No service accounts on this page.");
  fireEvent.change(screen.getByLabelText("Service account name"), { target: { value: "Build bot" } });
  fireEvent.click(screen.getByRole("button", { name: "Create account" }));
  expect(await screen.findByDisplayValue("nca_one_time_secret")).toBeTruthy();
  expect(serviceAccountsAPI.create).toHaveBeenCalledWith(expect.objectContaining({ project_id: "proj_one", name: "Build bot", role: "maintainer" }));
  fireEvent.click(screen.getByRole("button", { name: "I saved it" }));
  expect(screen.queryByDisplayValue("nca_one_time_secret")).toBeNull();
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>("Service account name").value).toBe(""));
  fireEvent.change(screen.getByLabelText("Service account name"), { target: { value: "Next bot" } });

  serviceAccountsAPI.create.mockResolvedValueOnce({ service_account: account, credential: { id: "pat_two", name: account.name, kind: "service_account", roles: [], status: "active", created_by: account.owner_user_id, service_account_id: account.id, created_at: account.created_at, expires_at: "2026-12-04T10:00:00Z" }, token: "nca_navigation_secret" });
  fireEvent.click(screen.getByRole("button", { name: "Create account" }));
  await screen.findByDisplayValue("nca_navigation_secret");
  fireEvent.change(screen.getByLabelText("Service account project"), { target: { value: "proj_two" } });
  await waitFor(() => expect(screen.queryByDisplayValue("nca_navigation_secret")).toBeNull());
});

test("updates, rotates, revokes, and pages service accounts", async () => {
  serviceAccountsAPI.list.mockImplementation(async (projectID: string, _limit: number, offset: number) => ({ items: offset === 0 ? [account] : [{ ...account, id: "svcacct_two", name: "Second bot" }], count: 1, limit: 10, offset, total: 11 }));
  serviceAccountsAPI.update.mockResolvedValue({ ...account, name: "Release bot", role: "viewer" });
  serviceAccountsAPI.rotate.mockResolvedValue({ service_account_id: account.id, credential: { id: "pat_rotated", name: account.name, kind: "service_account", roles: [], status: "active", created_by: account.owner_user_id, service_account_id: account.id, created_at: account.created_at, expires_at: "2026-12-04T10:00:00Z" }, token: "nca_rotated" });
  serviceAccountsAPI.revoke.mockResolvedValue({ ...account, status: "revoked" });
  renderPanel();
  const name = await screen.findByLabelText("Name for Build bot");
  fireEvent.change(name, { target: { value: "Release bot" } });
  fireEvent.change(screen.getByLabelText("Role for Build bot"), { target: { value: "viewer" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(serviceAccountsAPI.update).toHaveBeenCalledWith({ service_account_id: account.id, name: "Release bot", role: "viewer" }));

  fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
  expect(await screen.findByDisplayValue("nca_rotated")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "I saved it" }));
  fireEvent.click(screen.getByRole("button", { name: "Revoke" }));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText(/every credential/i)).toBeTruthy();
  fireEvent.click(within(dialog).getByRole("button", { name: "Revoke account" }));
  await waitFor(() => expect(serviceAccountsAPI.revoke).toHaveBeenCalledWith(account.id));

  fireEvent.click(screen.getByRole("button", { name: "Next accounts" }));
  await waitFor(() => expect(serviceAccountsAPI.list).toHaveBeenCalledWith("proj_one", 10, 10, expect.any(Object)));
});


test("refresh failure preserves service account edits until retry", async () => {
  const result = { items: [account], count: 1, total: 1, offset: 0, limit: 10 };
  serviceAccountsAPI.list.mockResolvedValue(result);
  const { client } = renderPanel();
  const name = await screen.findByLabelText<HTMLInputElement>("Name for Build bot");
  fireEvent.change(name, { target: { value: "Unsaved rename" } });
  serviceAccountsAPI.list.mockRejectedValue(new Error("Temporary failure"));
  await client.invalidateQueries({ queryKey: ["service-accounts"] });
  await screen.findByText("Unable to refresh service accounts. Showing previously loaded accounts.");
  expect(name.value).toBe("Unsaved rename");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save" }).disabled).toBe(true);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Rotate" }).disabled).toBe(true);
  serviceAccountsAPI.list.mockResolvedValue(result);
  fireEvent.click(screen.getByRole("button", { name: "Retry service accounts" }));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(name.value).toBe("Unsaved rename");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save" }).disabled).toBe(false);
});


test("service account pagination recovers empty later pages through shared navigation", async () => {
  serviceAccountsAPI.list.mockImplementation(async (_projectID: string, _limit: number, offset: number) => offset === 0
    ? { items: [account], count: 1, limit: 10, offset: 0, total: 11 }
    : { items: [], count: 0, limit: 10, offset, total: 1 });
  renderPanel();
  await screen.findByLabelText("Name for Build bot");
  fireEvent.click(screen.getByRole("button", { name: "Next accounts" }));
  await screen.findByText("Showing 0-0 of 1");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next accounts" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "First accounts page" }));
  await screen.findByLabelText("Name for Build bot");
  expect(serviceAccountsAPI.list.mock.calls.at(-1)?.slice(0, 3)).toEqual(["proj_one", 10, 0]);
});


test("revoke confirmation blocks duplicate requests and dismissal, then keeps failures retryable", async () => {
  serviceAccountsAPI.list.mockResolvedValue({ items: [account], count: 1, total: 1, offset: 0, limit: 10 });
  let rejectRequest!: (reason: Error) => void;
  serviceAccountsAPI.revoke.mockImplementationOnce(() => new Promise((_, reject) => { rejectRequest = reject; }));
  renderPanel();
  await screen.findByLabelText("Name for Build bot");
  fireEvent.click(screen.getByRole("button", { name: "Revoke" }));
  const dialog = screen.getByRole("dialog");
  const confirm = within(dialog).getByRole("button", { name: "Revoke account" });
  fireEvent.click(confirm);
  fireEvent.click(confirm);
  await waitFor(() => expect(serviceAccountsAPI.revoke).toHaveBeenCalledTimes(1));
  expect(within(dialog).getByRole<HTMLButtonElement>("button", { name: "Keep account active" }).disabled).toBe(true);
  fireEvent.keyDown(dialog, { key: "Escape" });
  expect(screen.getByRole("dialog")).toBeTruthy();
  rejectRequest(new Error("Temporary failure"));
  await within(dialog).findByRole("alert");
  expect(within(dialog).getByText("Revoke Build bot?")).toBeTruthy();
  serviceAccountsAPI.revoke.mockResolvedValueOnce({ ...account, status: "revoked" });
  fireEvent.click(within(dialog).getByRole("button", { name: "Revoke account" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(serviceAccountsAPI.revoke).toHaveBeenCalledTimes(2);
  expect(serviceAccountsAPI.revoke).toHaveBeenLastCalledWith(account.id);
});


test("pending account updates lock fields and preserve failed edits beside the row", async () => {
  serviceAccountsAPI.list.mockResolvedValue({ items: [account], count: 1, total: 1, offset: 0, limit: 10 });
  let reject!: (error: Error) => void;
  serviceAccountsAPI.update.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; })).mockResolvedValueOnce(account);
  renderPanel();
  const name = await screen.findByLabelText<HTMLInputElement>("Name for Build bot");
  const role = screen.getByLabelText<HTMLSelectElement>("Role for Build bot");
  fireEvent.change(name, { target: { value: "Release bot" } });
  fireEvent.change(role, { target: { value: "viewer" } });
  const row = screen.getByRole("form", { name: "Edit service account Build bot" });
  fireEvent.submit(row);
  await waitFor(() => expect(name.disabled).toBe(true));
  expect(role.disabled).toBe(true);
  expect(row.getAttribute("aria-busy")).toBe("true");
  fireEvent.submit(row);
  expect(serviceAccountsAPI.update).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Temporary update failure")));
  await waitFor(() => expect(name.disabled).toBe(false));
  expect(name.value).toBe("Release bot");
  expect(role.value).toBe("viewer");
  expect(within(row).getByRole("alert").textContent).toBe("Unable to save account: Temporary update failure");
  fireEvent.submit(row);
  await waitFor(() => expect(serviceAccountsAPI.update).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(within(row).queryByRole("alert")).toBeNull());
  expect(serviceAccountsAPI.update.mock.calls[1]).toEqual(serviceAccountsAPI.update.mock.calls[0]);
});
