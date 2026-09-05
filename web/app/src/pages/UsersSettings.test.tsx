import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    return { user: laterUser, revoked_sessions: 0, revoked_api_tokens: 0 };
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><UsersSettingsPanel /></QueryClientProvider>);

  await screen.findByText("first-0@example.invalid");
  fireEvent.click(screen.getByRole("button", { name: "Next users" }));
  await screen.findByText(laterUser.email);
  expect(screen.getByText(`Showing ${usersPageSize + 1}–${usersPageSize + 1} of ${usersPageSize + 1}`)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: `Offboard ${laterUser.email}` }));
  fireEvent.click(screen.getByRole("button", { name: "Offboard user" }));
  await waitFor(() => expect(screen.getByText("disabled")).toBeTruthy());
  expect(screen.getByText(laterUser.email)).toBeTruthy();
  expect(screen.getByText(`Showing ${usersPageSize + 1}–${usersPageSize + 1} of ${usersPageSize + 1}`)).toBeTruthy();
  expect(usersAPI.listUsers.mock.calls.at(-1)?.[0]).toEqual({ limit: usersPageSize, offset: usersPageSize });
});
