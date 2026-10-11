import type { ComponentType, ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useQuery } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.settings";

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options }),
  useSearch: () => ({ offset: 50 }), useNavigate: () => vi.fn(),
}));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(), useQuery: vi.fn(),
  useQueryClient: () => ({}), useMutation: () => ({ isPending: false, mutate: vi.fn() }),
}));
vi.mock("@/pages/SettingsView", () => ({ SettingsView: ({ readOnly, members, memberNavigation }: { readOnly: boolean; members: Array<{ email: string }>; memberNavigation: ReactNode }) => <div>Settings {readOnly ? "read only" : "editable"}{members.map((member) => <p key={member.email}>{member.email}</p>)}{memberNavigation}</div> }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
const SettingsRoute = Route.options.component as ComponentType;
const ready = (data: unknown) => ({ data, isError: false, isPending: false, isFetching: false, refetch: vi.fn() });
function queries(last: object, permissionsError = false) {
  const permissions = { ...ready([]), isError: permissionsError };
  const results = [ready({ id: "operator", roles: [] }), ready([]), permissions, last];
  let index = 0;
  vi.mocked(useQuery).mockImplementation(() => results[index++ % 4] as never);
  return permissions;
}

test("failed member page retries locally without hiding the rest of settings", () => {
  const refetch = vi.fn();
  const permissions = queries({ ...ready(undefined), isError: true, refetch });
  render(<SettingsRoute />);
  expect(screen.getByText("Settings editable")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry settings" }));
  expect(refetch).toHaveBeenCalledOnce();
  expect(permissions.refetch).not.toHaveBeenCalled();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "First project members page" }).disabled).toBe(false);
});

test("failed permission refresh preserves cached members but disables changes", () => {
  const permissions = queries(ready({ items: [{ email: "cached@example.test" }], count: 1, total: 51, limit: 50, offset: 50 }), true);
  render(<SettingsRoute />);
  expect(screen.getByText("Settings read only")).toBeTruthy();
  expect(screen.getByText("cached@example.test")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry settings" }));
  expect(permissions.refetch).toHaveBeenCalledOnce();
});

test("initial settings failure offers retry without rendering empty controls", () => {
  vi.mocked(useQuery).mockReturnValue({ ...ready(undefined), isError: true } as never);
  render(<SettingsRoute />);
  expect(screen.getByRole("alert").textContent).toBe("Unable to load settings.");
  expect(screen.queryByText("Settings editable")).toBeNull();
  expect(screen.getByRole("button", { name: "Retry settings" })).toBeTruthy();
});
