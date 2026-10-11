import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot, cancelRun, type MutateFn } from "@/api/compat";
import type { TaskRun } from "@/api";
import { RunsTable, RunsView } from "./RunsView";

vi.mock("@/api/compat", async (original) => ({
  ...await original<typeof import("@/api/compat")>(),
  cancelRun: vi.fn(async () => ({ id: "run_cancel", status: "canceled" })),
}));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
const run: TaskRun = { id: "run_cancel", project_id: "project", status: "queued", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] }, requested_by: "operator", runner_tags: [], started_at: "2026-01-01T00:00:00Z" };

test("session-authenticated runs can be canceled without a legacy token", async () => {
  const mutate: MutateFn = async (_key, action) => action();
  render(<RunsView snapshot={apiSnapshot({ runs: [run] })} token="" busy="" mutate={mutate} />);
  const buttons = screen.getAllByRole("button", { name: "Cancel" });
  expect(buttons).toHaveLength(2);
  await act(async () => { fireEvent.click(buttons[0]!); });
  expect(cancelRun).toHaveBeenCalledWith("", run.id);
});

test("pending cancellation disables desktop and mobile actions", () => {
  render(<RunsView snapshot={apiSnapshot({ runs: [run] })} token="" busy={`cancel:${run.id}`} mutate={vi.fn()} />);
  for (const button of screen.getAllByRole("button", { name: "Cancel" })) {
    expect((button as HTMLButtonElement).disabled).toBe(true);
  }
});

test.each(["succeeded", "failed", "canceled"])("%s runs have no cancellation action", (status) => {
  render(<RunsView snapshot={apiSnapshot({ runs: [{ ...run, status }] })} token="" busy="" mutate={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});

test("read-only run tables do not offer cancellation", () => {
  render(<RunsTable runs={[run]} projects={[]} templates={[]} />);
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});
