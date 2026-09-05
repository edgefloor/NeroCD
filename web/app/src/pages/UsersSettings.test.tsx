import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { UsersSettings } from "./UsersSettings";

const activeUser = { id: "usr_operator", email: "operator@example.invalid", name: "Operator", status: "active", global_role: "user", created_at: "2026-09-05T10:00:00Z", updated_at: "2026-09-05T10:00:00Z" } as const;

afterEach(cleanup);

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
