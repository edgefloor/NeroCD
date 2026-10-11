import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { RunsRoute } from "./_authenticated.runs.index";

const navigate = vi.hoisted(() => vi.fn());
afterEach(() => { cleanup(); navigate.mockClear(); });

const noLogs: never[] = [];
const loadPrevious = vi.fn();

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...await importOriginal<typeof import("@tanstack/react-query")>(),
  useQuery: vi.fn(() => ({ data: undefined, isError: false, isPending: false, isFetching: false })),
}));

vi.mock("@/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api")>(),
  useRunsPollingQuery: vi.fn(() => ({ data: { items: [], count: 0, total: 0, limit: 50, offset: 0 }, isError: false, isPending: false, isFetching: false })),
  useSelectedRunLogsPollingQuery: vi.fn(() => ({ data: noLogs, isError: false, isPending: false, hasPrevious: false, loadingPrevious: false, loadPrevious })),
}));

vi.mock("@/api/compat", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api/compat")>(),
  useSnapshotMutation: vi.fn(() => ({ busy: "", mutate: vi.fn() })),
}));

vi.mock("@/pages/RunsView", () => ({
  RunsView: ({ selectedRunID, onSelectRun, onCloseLogs }: { selectedRunID?: string; onSelectRun: (id: string) => void; onCloseLogs: () => void }) => <><output data-testid="selected-run">{selectedRunID}</output><button onClick={() => onSelectRun("run_second")}>Open second run</button><button onClick={onCloseLogs}>Close logs</button></>,
}));

test("detail run identity follows a changed route parameter", async () => {
  const view = render(<RunsRoute runId="run_first" />);
  expect(screen.getByTestId("selected-run").textContent).toBe("run_first");
  view.rerender(<RunsRoute runId="run_second" />);
  await waitFor(() => expect(screen.getByTestId("selected-run").textContent).toBe("run_second"));
});

vi.mock("@tanstack/react-router", async (original) => ({
  ...await original<typeof import("@tanstack/react-router")>(),
  useNavigate: () => navigate,
  useSearch: () => ({}),
}));


test("opening a different run from details navigates while preserving search", () => {
  render(<RunsRoute runId="run_first" />);
  fireEvent.click(screen.getByRole("button", { name: "Close logs" }));
  fireEvent.click(screen.getByRole("button", { name: "Open second run" }));
  expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/runs/$runId", params: { runId: "run_second" }, search: true, resetScroll: false }));
});

test("list previews and reopening the current detail stay on the current route", () => {
  const view = render(<RunsRoute />);
  fireEvent.click(screen.getByRole("button", { name: "Open second run" }));
  expect(screen.getByTestId("selected-run").textContent).toBe("run_second");
  expect(navigate).not.toHaveBeenCalled();
  view.rerender(<RunsRoute runId="run_second" />);
  fireEvent.click(screen.getByRole("button", { name: "Close logs" }));
  fireEvent.click(screen.getByRole("button", { name: "Open second run" }));
  expect(screen.getByTestId("selected-run").textContent).toBe("run_second");
  expect(navigate).not.toHaveBeenCalled();
});
