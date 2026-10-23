import type { ComponentType } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "@/api";
import { Route } from "./_authenticated.index";

const state = vi.hoisted(() => ({ failed: true, noData: false, pending: false, denied: 0, principalError: false, principalFetching: false, actor: "one", refetch: vi.fn(), healthyRefetch: vi.fn(), decide: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({ q: "filter" }), useNavigate: () => vi.fn() }),
  useRouter: () => ({ invalidate: vi.fn() }),
}));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: (options: { queryKey: string[] }) => {
    const key = options.queryKey[0];
    const failed = (key === "health" && state.failed) || (key === "principal" && state.principalError);
    const data = key === "health" ? { status: "ok" } : key === "principal" ? { id: state.actor, provider: "local", roles: ["system_admin"] } : key === "projectMembers" ? [] : { items: [], count: 0, total: 0, limit: 50, offset: 0 };
    return { data: key === "health" && state.noData ? undefined : data, isError: failed, error: failed ? state.denied ? new ApiError(state.denied, "Denied", null) : new Error("Refresh failed") : null, isPending: key === "health" && state.pending, isFetching: key === "principal" && state.principalFetching, refetch: key === "health" ? state.refetch : state.healthyRefetch };
  },
}));
vi.mock("@/api", async (original) => ({ ...await original<typeof import("@/api")>(), useApprovalDecision: () => ({ busy: false, decide: state.decide }) }));
vi.mock("@/pages/HomeView", () => ({ HostingStart: () => <a href="/runners">Connect a server</a>, HomeView: ({ busy, query, onDecide }: { busy: boolean; query: string; onDecide: (runID: string, decision: "approve") => void }) => <><p>Loaded dashboard {query}</p><button disabled={busy} onClick={() => onDecide("run_one", "approve")}>Approve</button></> }));
afterEach(() => { cleanup(); vi.clearAllMocks(); state.failed = true; state.noData = false; state.pending = false; state.denied = 0; state.principalError = false; state.principalFetching = false; state.actor = "one"; });
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


test("non-auth legacy failure keeps hosting links available without fabricated activity", () => {
  state.noData = true; render(<HomeRoute />); expect(screen.getByRole("link", { name: "Connect a server" }).getAttribute("href")).toBe("/runners"); expect(screen.queryByText("Loaded dashboard filter")).toBeNull(); expect(screen.getByRole("alert")).toBeTruthy();
});
test.each([401, 403])("legacy permission failure %s cannot be hidden by guidance or cached activity", (code) => {
  state.denied = code; render(<HomeRoute />); expect(screen.queryByRole("link", { name: "Connect a server" })).toBeNull(); expect(screen.queryByText("Loaded dashboard filter")).toBeNull();
});
test.each(["error", "fetching"])("current principal %s suppresses activity and guidance", (mode) => {
  state.principalError = mode === "error"; state.principalFetching = mode === "fetching"; render(<HomeRoute />); expect(screen.queryByRole("link", { name: "Connect a server" })).toBeNull(); expect(screen.queryByText("Loaded dashboard filter")).toBeNull();
});
