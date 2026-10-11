import { render, screen, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { RunsRoute } from "./_authenticated.runs.index";

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
  RunsView: ({ selectedRunID }: { selectedRunID?: string }) => <output data-testid="selected-run">{selectedRunID}</output>,
}));

test("detail run identity follows a changed route parameter", async () => {
  const view = render(<RunsRoute runId="run_first" />);
  expect(screen.getByTestId("selected-run").textContent).toBe("run_first");
  view.rerender(<RunsRoute runId="run_second" />);
  await waitFor(() => expect(screen.getByTestId("selected-run").textContent).toBe("run_second"));
});

vi.mock("@tanstack/react-router", async (original) => ({
  ...await original<typeof import("@tanstack/react-router")>(),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
}));
