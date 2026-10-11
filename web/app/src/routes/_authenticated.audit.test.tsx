import type { ComponentType } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useQuery } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.audit";

const navigation = vi.hoisted(() => ({ offset: 0, navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({}) }),
  useSearch: () => ({ offset: navigation.offset }), useNavigate: () => navigation.navigate,
}));
vi.mock("@tanstack/react-query", async (original) => ({ ...await original<typeof import("@tanstack/react-query")>(), useQuery: vi.fn() }));
afterEach(() => { cleanup(); vi.clearAllMocks(); navigation.offset = 0; });
const AuditRoute = Route.options.component as ComponentType;

test("initial audit failure offers retry without claiming an empty history", () => {
  const refetch = vi.fn();
  vi.mocked(useQuery).mockReturnValue({ isError: true, isPending: false, isFetching: false, refetch } as never);
  const view = render(<AuditRoute />);
  expect(screen.getByRole("alert").textContent).toBe("Unable to load audit events.");
  expect(screen.queryByText("No audit events on this page.")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry audit events" }));
  expect(refetch).toHaveBeenCalledOnce();
  vi.mocked(useQuery).mockReturnValue({ isError: true, isPending: false, isFetching: true, refetch } as never);
  view.rerender(<AuditRoute />);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry audit events" }).disabled).toBe(true);
});

test("failed refresh keeps previously loaded evidence visible", () => {
  const data = { items: [{ id: "audit_one", actor_id: "operator_one", action: "run.requested", target_id: "run_one", metadata: {}, created_at: "2026-10-11T12:34:56Z" }], count: 1, total: 1, limit: 50, offset: 0 };
  vi.mocked(useQuery).mockReturnValue({ data, isError: true, isPending: false, isFetching: false, refetch: vi.fn() } as never);
  render(<AuditRoute />);
  expect(screen.getByRole("alert").textContent).toBe("Unable to refresh audit events. Showing previously loaded events.");
  expect(screen.getByText("operator_one")).toBeTruthy();
  expect(screen.getByText("run.requested")).toBeTruthy();
});


test("failed later audit pages can return to the first page with their filter", () => {
  navigation.offset = 50;
  vi.mocked(useQuery).mockReturnValue({ isError: true, isPending: false, isFetching: false, refetch: vi.fn() } as never);
  render(<AuditRoute />);
  fireEvent.click(screen.getByRole("button", { name: "First audit events page" }));
  expect(navigation.navigate).toHaveBeenCalledOnce();
  const target = navigation.navigate.mock.calls[0]![0];
  expect(target.search({ offset: 50, q: "release" })).toEqual({ offset: undefined, q: "release" });
});
