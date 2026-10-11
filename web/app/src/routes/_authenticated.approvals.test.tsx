import type { ComponentType } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.approvals";

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({}) }),
  useSearch: () => ({ offset: 50 }), useNavigate: () => vi.fn(),
}));
vi.mock("@tanstack/react-query", async (original) => ({ ...await original<typeof import("@tanstack/react-query")>(), useQuery: vi.fn(), useQueries: vi.fn() }));
vi.mock("@/api", async (original) => ({ ...await original<typeof import("@/api")>(), useApprovalDecision: () => ({ busy: false, decide: vi.fn() }) }));
vi.mock("@/pages/ApprovalsView", () => ({ ApprovalsView: ({ busy }: { busy: boolean }) => <button disabled={Boolean(busy)}>Loaded approval</button> }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
const ApprovalsRoute = Route.options.component as ComponentType;
const ready = (data: unknown) => ({ data, isError: false, isPending: false, isFetching: false, refetch: vi.fn() });
function queries(approval: object) {
  const healthy = ready([]);
  const results = [approval, healthy, healthy, ready({ roles: [] }), healthy];
  let index = 0;
  vi.mocked(useQuery).mockImplementation(() => results[index++ % 5] as never);
  vi.mocked(useQueries).mockReturnValue([]);
  return healthy;
}

test("initial approval failure offers local retry and first-page recovery", () => {
  const refetch = vi.fn();
  const healthy = queries({ ...ready(undefined), isError: true, refetch });
  render(<ApprovalsRoute />);
  expect(screen.getByRole("alert").textContent).toBe("Unable to load approvals.");
  expect(screen.queryByText("Loaded approval")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry approvals" }));
  expect(refetch).toHaveBeenCalledOnce();
  expect(healthy.refetch).not.toHaveBeenCalled();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "First approvals page" }).disabled).toBe(false);
});

test("failed run refresh retains approvals and disables decisions until recovery", () => {
  queries(ready({ items: [{ id: "approval_one", run_id: "run_one" }], count: 1, total: 51, limit: 50, offset: 50 }));
  const refetch = vi.fn();
  vi.mocked(useQueries).mockReturnValue([{ ...ready({ id: "run_one" }), isError: true, refetch }] as never);
  const view = render(<ApprovalsRoute />);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Loaded approval" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Retry approvals" }));
  expect(refetch).toHaveBeenCalledOnce();
  vi.mocked(useQueries).mockReturnValue([ready({ id: "run_one" })] as never);
  view.rerender(<ApprovalsRoute />);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Loaded approval" }).disabled).toBe(false);
});
