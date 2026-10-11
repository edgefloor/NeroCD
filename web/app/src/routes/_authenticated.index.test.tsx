import type { ComponentType } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.index";

const state = vi.hoisted(() => ({ failed: true, refetch: vi.fn(), healthyRefetch: vi.fn(), decide: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({ q: "filter" }), useNavigate: () => vi.fn() }),
  useRouter: () => ({ invalidate: vi.fn() }),
}));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: (options: { queryKey: string[] }) => {
    const key = options.queryKey[0];
    const failed = key === "health" && state.failed;
    const data = key === "health" ? { status: "ok" } : key === "principal" ? { roles: ["system_admin"] } : key === "projectMembers" ? [] : { items: [], count: 0, total: 0, limit: 50, offset: 0 };
    return { data, isError: failed, error: failed ? new Error("Refresh failed") : null, isPending: false, isFetching: false, refetch: key === "health" ? state.refetch : state.healthyRefetch };
  },
}));
vi.mock("@/api", async (original) => ({ ...await original<typeof import("@/api")>(), useApprovalDecision: () => ({ busy: false, decide: state.decide }) }));
vi.mock("@/pages/HomeView", () => ({ HomeView: ({ busy, query, onDecide }: { busy: boolean; query: string; onDecide: (runID: string, decision: "approve") => void }) => <><p>Loaded dashboard {query}</p><button disabled={busy} onClick={() => onDecide("run_one", "approve")}>Approve</button></> }));
afterEach(() => { cleanup(); vi.clearAllMocks(); state.failed = true; });
const HomeRoute = Route.options.component as ComponentType;

test("background failure preserves the loaded dashboard and disables decisions until local retry", () => {
  const view = render(<HomeRoute />);
  expect(screen.getByText("Loaded dashboard filter")).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toBe("Unable to refresh dashboard. Showing previously loaded information.");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Approve" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Retry dashboard" }));
  expect(state.refetch).toHaveBeenCalledOnce();
  expect(state.healthyRefetch).not.toHaveBeenCalled();
  state.failed = false;
  view.rerender(<HomeRoute />);
  expect(screen.queryByRole("alert")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Approve" }));
  expect(state.decide).toHaveBeenCalledWith("run_one", "approve");
});
