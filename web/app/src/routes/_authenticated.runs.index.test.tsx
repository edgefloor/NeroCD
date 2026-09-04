import { render, screen, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { RunsRoute } from "./_authenticated.runs.index";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...await importOriginal<typeof import("@tanstack/react-query")>(),
  useQuery: vi.fn(() => ({ data: [], isError: false, isPending: false })),
}));

vi.mock("@/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api")>(),
  useRunsPollingQuery: vi.fn(() => ({ data: [], isError: false, isPending: false })),
  useSelectedRunLogsPollingQuery: vi.fn(() => ({ data: undefined, isError: false, isPending: false })),
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
