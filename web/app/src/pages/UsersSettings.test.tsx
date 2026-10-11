import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { User } from "@/api/users";
import { UsersSettings, UsersSettingsPanel, usersPageSize } from "./UsersSettings";

const usersAPI = vi.hoisted(() => ({ listUsers: vi.fn(), offboardUser: vi.fn() }));
vi.mock("@/api/users", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api/users")>(),
  listUsers: usersAPI.listUsers,
  offboardUser: usersAPI.offboardUser,
}));

const activeUser = { id: "usr_operator", email: "operator@example.invalid", name: "Operator", status: "active", global_role: "user", created_at: "2026-09-05T10:00:00Z", updated_at: "2026-09-05T10:00:00Z" } as const;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

test("offboarding confirmation explains login, session, issuer-token, and history effects", async () => {
  const offboard = vi.fn().mockResolvedValue(undefined);
  render(<UsersSettings users={[activeUser]} onOffboard={offboard} />);
  fireEvent.click(screen.getByRole("button", { name: `Offboard ${activeUser.email}` }));
  const dialog = screen.getByRole("dialog");
  expect(dialog.textContent).toMatch(/local and OIDC sign-in/i);
  expect(dialog.textContent).toMatch(/every session/i);
  expect(dialog.textContent).toMatch(/API token issued by this user/i);
  expect(dialog.textContent).toMatch(/service account they own/i);
  expect(dialog.textContent).toMatch(/history remain/i);
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await waitFor(() => expect(offboard).toHaveBeenCalledWith(activeUser.id));
});

test("disabled users remain visible without another destructive control", () => {
  render(<UsersSettings users={[{ ...activeUser, status: "disabled", archived_at: "2026-09-05T11:00:00Z" }]} onOffboard={vi.fn()} />);
  expect(screen.getByText(activeUser.email)).toBeTruthy();
  expect(screen.getByText("disabled")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /offboard/i })).toBeNull();
});

test("later pages remain selected when offboarding refreshes the active page", async () => {
  const firstPageUsers = Array.from({ length: usersPageSize }, (_, index) => ({
    ...activeUser,
    id: `usr_first_${index}`,
    email: `first-${index}@example.invalid`,
  }));
  let laterUser: User = { ...activeUser, id: "usr_later", email: "later@example.invalid" };
  usersAPI.listUsers.mockImplementation(async ({ offset }: { offset: number }) => offset === 0
    ? { items: firstPageUsers, count: usersPageSize, limit: usersPageSize, offset: 0, total: usersPageSize + 1 }
    : { items: [laterUser], count: 1, limit: usersPageSize, offset: usersPageSize, total: usersPageSize + 1 });
  usersAPI.offboardUser.mockImplementation(async () => {
    laterUser = { ...laterUser, status: "disabled" };
    return { user: laterUser, revoked_sessions: 0, revoked_api_tokens: 0, revoked_service_accounts: 0 };
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><UsersSettingsPanel /></QueryClientProvider>);

  await screen.findByText("first-0@example.invalid");
  fireEvent.click(screen.getByRole("button", { name: "Next users" }));
  await screen.findByText(laterUser.email);
  expect(screen.getByText(`Showing ${usersPageSize + 1}-${usersPageSize + 1} of ${usersPageSize + 1}`)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: `Offboard ${laterUser.email}` }));
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await waitFor(() => expect(screen.getByText("disabled")).toBeTruthy());
  expect(screen.getByText(laterUser.email)).toBeTruthy();
  expect(screen.getByText(`Showing ${usersPageSize + 1}-${usersPageSize + 1} of ${usersPageSize + 1}`)).toBeTruthy();
  expect(usersAPI.listUsers.mock.calls.at(-1)?.[0]).toEqual({ limit: usersPageSize, offset: usersPageSize });
});

test("empty user pages have a truthful range and direct first-page recovery", () => {
  const first = vi.fn();
  const props = { users: [], page: { count: 0, limit: 10, offset: 20, total: 15 }, onFirst: first, onPrevious: vi.fn(), onNext: vi.fn(), onOffboard: vi.fn() };
  const view = render(<UsersSettings {...props} />);
  expect(screen.getByText("Showing 0-0 of 15")).toBeTruthy();
  expect(screen.getByText("No users on this page.")).toBeTruthy();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next users" }).disabled).toBe(true);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Previous users" }).disabled).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "First users page" }));
  expect(first).toHaveBeenCalledOnce();
  view.rerender(<UsersSettings {...props} busy />);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "First users page" }).disabled).toBe(true);
});

test("failed user refresh preserves loaded identities and disables destructive controls until retry", async () => {
  const result = { items: [activeUser], count: 1, limit: usersPageSize, offset: 0, total: 1 };
  usersAPI.listUsers.mockRejectedValue(new Error("Temporary failure"));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["users", { limit: usersPageSize, offset: 0 }], result);
  render(<QueryClientProvider client={client}><UsersSettingsPanel /></QueryClientProvider>);
  await screen.findByText("Unable to refresh users. Showing previously loaded users.");
  expect(screen.getByText(activeUser.email)).toBeTruthy();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: `Offboard ${activeUser.email}` }).disabled).toBe(true);
  usersAPI.listUsers.mockResolvedValue(result);
  fireEvent.click(screen.getByRole("button", { name: "Retry users" }));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(screen.getByRole<HTMLButtonElement>("button", { name: `Offboard ${activeUser.email}` }).disabled).toBe(false);
});

test("offboarding failures stay in the confirmation dialog and can be retried", async () => {
  const offboard = vi.fn().mockRejectedValueOnce(new Error("Temporary failure")).mockResolvedValue(undefined);
  render(<UsersSettings users={[activeUser]} onOffboard={offboard} />);
  fireEvent.click(screen.getByRole("button", { name: `Offboard ${activeUser.email}` }));
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await screen.findByText("Unable to offboard this user. Try again.");
  expect(screen.getByRole("dialog").textContent).toContain(activeUser.email);
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(offboard).toHaveBeenCalledTimes(2);
  expect(offboard).toHaveBeenLastCalledWith(activeUser.id);
});


test("pending offboarding prevents duplicate requests and dismissal", async () => {
  let resolve!: () => void;
  const offboard = vi.fn(() => new Promise<void>((done) => { resolve = done; }));
  render(<UsersSettings users={[activeUser]} onOffboard={offboard} />);
  fireEvent.click(screen.getByRole("button", { name: `Offboard ${activeUser.email}` }));
  const submit = screen.getByRole("button", { name: "Offboard user" });
  fireEvent.click(submit);
  fireEvent.click(submit);
  expect(offboard).toHaveBeenCalledTimes(1);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Offboarding..." }).disabled).toBe(true);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Keep user active" }).disabled).toBe(true);
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  expect(screen.getByRole("dialog")).toBeTruthy();
  await act(async () => resolve());
  expect(screen.queryByRole("dialog")).toBeNull();
});


test("confirmed offboarding survives a failed user refresh and invalidates service accounts", async () => {
  const disabled = { ...activeUser, status: "disabled" };
  usersAPI.listUsers.mockResolvedValueOnce({ items: [activeUser], count: 1, limit: usersPageSize, offset: 0, total: 1 }).mockRejectedValue(new Error("Refresh unavailable"));
  usersAPI.offboardUser.mockResolvedValueOnce({ user: disabled, revoked_sessions: 1, revoked_api_tokens: 2, revoked_service_accounts: 1 });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const accountsKey = ["service-accounts", "project_one", 0];
  client.setQueryData(accountsKey, { items: [], count: 0, total: 0, limit: 10, offset: 0 });
  render(<QueryClientProvider client={client}><UsersSettingsPanel /></QueryClientProvider>);
  fireEvent.click(await screen.findByRole("button", { name: `Offboard ${activeUser.email}` }));
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await screen.findByText("Unable to refresh users. Showing previously loaded users.");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(screen.getByText("disabled")).toBeTruthy();
  expect(screen.queryByRole("button", { name: `Offboard ${activeUser.email}` })).toBeNull();
  expect(client.getQueryState(accountsKey)?.isInvalidated).toBe(true);
});


test("failed later user pages can return directly to the first page", async () => {
  usersAPI.listUsers.mockImplementation(async ({ offset }: { offset: number }) => {
    if (offset > 0) throw new Error("Later page unavailable");
    return { items: [activeUser], count: 1, limit: usersPageSize, offset: 0, total: 11 };
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><UsersSettingsPanel /></QueryClientProvider>);
  await screen.findByText(activeUser.email);
  expect(screen.queryByRole("button", { name: "First users page" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Next users" }));
  await screen.findByText("Unable to load users.");
  fireEvent.click(screen.getByRole("button", { name: "First users page" }));
  await screen.findByText(activeUser.email);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(usersAPI.listUsers.mock.calls.at(-1)?.[0]).toEqual({ limit: usersPageSize, offset: 0 });
});
