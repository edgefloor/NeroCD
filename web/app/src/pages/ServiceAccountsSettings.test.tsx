import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
  return render(<QueryClientProvider client={client}><ServiceAccountsSettingsPanel projects={projects} /></QueryClientProvider>);
}

test("creates and clears one-time credential on close and project navigation", async () => {
  serviceAccountsAPI.list.mockResolvedValue({ items: [], count: 0, limit: 10, offset: 0, total: 0 });
  serviceAccountsAPI.create.mockResolvedValue({ service_account: account, credential: { id: "pat_one", name: account.name, kind: "service_account", roles: [], status: "active", created_by: account.owner_user_id, service_account_id: account.id, created_at: account.created_at, expires_at: "2026-12-04T10:00:00Z" }, token: "nca_one_time_secret" });
  renderPanel();
  await screen.findByText("No service accounts in this project.");
  fireEvent.change(screen.getByLabelText("Service account name"), { target: { value: "Build bot" } });
  fireEvent.click(screen.getByRole("button", { name: "Create account" }));
  expect(await screen.findByDisplayValue("nca_one_time_secret")).toBeTruthy();
  expect(serviceAccountsAPI.create).toHaveBeenCalledWith(expect.objectContaining({ project_id: "proj_one", name: "Build bot", role: "maintainer" }));
  fireEvent.click(screen.getByRole("button", { name: "I saved it" }));
  expect(screen.queryByDisplayValue("nca_one_time_secret")).toBeNull();

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
