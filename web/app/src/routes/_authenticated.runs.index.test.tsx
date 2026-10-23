import { Profiler, StrictMode } from "react";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { RunsRoute } from "./_authenticated.runs.index";

const navigate = vi.hoisted(() => vi.fn());
const cancellation = vi.hoisted(() => ({ isPending: false, mutate: vi.fn() }));
afterEach(() => { cleanup(); navigate.mockClear(); cancellation.mutate.mockClear(); });

const noLogs: never[] = [];
const loadPrevious = vi.fn();

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...await importOriginal<typeof import("@tanstack/react-query")>(),
  useQuery: vi.fn(() => ({ data: undefined, isError: false, isPending: false, isFetching: false })),
}));

vi.mock("@/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api")>(),
  useRunMutations: vi.fn(() => ({ cancel: cancellation })),
  useRunsPollingQuery: vi.fn(() => ({ data: { items: [], count: 0, total: 0, limit: 50, offset: 0 }, isError: false, isPending: false, isFetching: false })),
  useSelectedRunLogsPollingQuery: vi.fn(() => ({ data: noLogs, isError: false, isPending: false, hasPrevious: false, loadingPrevious: false, loadPrevious })),
}));

vi.mock("@/pages/RunsView", () => ({
  RunsView: ({ selectedRunID, onSelectRun, onCloseLogs, onCancel }: { onCancel: (runID: string) => void; selectedRunID?: string; onSelectRun: (id: string) => void; onCloseLogs: () => void }) => <><button onClick={() => onCancel("run_first")}>Cancel first run</button><output data-testid="selected-run">{selectedRunID}</output><button onClick={() => onSelectRun("run_second")}>Open second run</button><button onClick={onCloseLogs}>Close logs</button></>,
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


test("cancellation blocks repeated submissions until the request settles", () => {
  render(<RunsRoute />);
  const button = screen.getByRole("button", { name: "Cancel first run" });
  fireEvent.click(button);
  fireEvent.click(button);
  expect(cancellation.mutate).toHaveBeenCalledTimes(1);
  expect(cancellation.mutate.mock.calls[0]?.[0]).toBe("run_first");
  cancellation.mutate.mock.calls[0]?.[1].onSettled();
  fireEvent.click(button);
  expect(cancellation.mutate).toHaveBeenCalledTimes(2);
});


test("StrictMode commits the current URL identity directly and repeated props preserve explicit closing", () => {
  const committedIDs: string[] = [];
  const observe = () => committedIDs.push(document.querySelector('[data-testid="selected-run"]')?.textContent ?? "");
  const ui = (runId?: string) => <StrictMode><Profiler id="runs" onRender={observe}><RunsRoute runId={runId} /></Profiler></StrictMode>;
  const view = render(ui("run_first"));
  committedIDs.length = 0;
  view.rerender(ui("run_second"));
  expect(committedIDs.length).toBeGreaterThan(0);
  expect(committedIDs.every((id) => id === "run_second")).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Close logs" }));
  view.rerender(ui("run_second")); view.rerender(ui("run_second"));
  expect(screen.getByTestId("selected-run").textContent).toBe("");
  view.rerender(ui("run_first"));
  expect(screen.getByTestId("selected-run").textContent).toBe("run_first");
  expect(cancellation.mutate).not.toHaveBeenCalled();
});
