import type { ComponentProps, ReactNode } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import type { TaskRun } from "@/api";
import { RunsTable, RunsView } from "./RunsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params, search = {}, ...props }: { children: ReactNode; params: { runId: string }; search?: { q?: string; offset?: number } } & ComponentProps<"a">) => {
  const query = new URLSearchParams();
  if (search.q) query.set("q", search.q);
  if (search.offset) query.set("offset", String(search.offset));
  return <a {...props} href={`/runs/${params.runId}${query.size ? `?${query}` : ""}`}>{children}</a>;
} }));

afterEach(() => { cleanup(); vi.clearAllMocks(); });
const run: TaskRun = { id: "run_cancel", project_id: "project", status: "queued", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] }, requested_by: "operator", runner_tags: [], started_at: "2026-01-01T00:00:00Z" };

test("run cancellation delegates the selected identity to its route", async () => {
  const onCancel = vi.fn();
  render(<RunsView canOperateProject={() => true} snapshot={apiSnapshot({ runs: [run] })} busy={false} onCancel={onCancel} />);
  const buttons = screen.getAllByRole("button", { name: "Cancel" });
  expect(buttons).toHaveLength(2);
  await act(async () => { fireEvent.click(buttons[0]!); });
  expect(onCancel).toHaveBeenCalledWith(run.id);
});

test("pending cancellation disables desktop and mobile actions", () => {
  render(<RunsView canOperateProject={() => true} snapshot={apiSnapshot({ runs: [run] })} busy={true} onCancel={vi.fn()} />);
  for (const button of screen.getAllByRole("button", { name: "Cancel" })) {
    expect((button as HTMLButtonElement).disabled).toBe(true);
  }
});

test.each(["succeeded", "failed", "canceled"])("%s runs have no cancellation action", (status) => {
  render(<RunsView canOperateProject={() => true} snapshot={apiSnapshot({ runs: [{ ...run, status }] })} busy={false} onCancel={vi.fn()} />);
  expect(screen.queryAllByRole("button", { name: "Cancel" })).toHaveLength(0);
});

test("read-only run tables do not offer cancellation", () => {
  render(<RunsTable runs={[run]} projects={[]} templates={[]} />);
  expect(screen.queryAllByRole("button", { name: "Cancel" })).toHaveLength(0);
});

test("runs without confirmed project write access retain logs but omit Cancel", () => {
  render(<RunsView snapshot={apiSnapshot({ runs: [run] })} busy={false} onCancel={vi.fn()} />);
  expect(screen.getAllByRole("button", { name: "Logs" })).toHaveLength(2);
  expect(screen.queryAllByRole("button", { name: "Cancel" })).toHaveLength(0);
});


test("cancel actions are evaluated separately for each run's project", () => {
  const other = { ...run, id: "run_other", project_id: "other" };
  render(<RunsView snapshot={apiSnapshot({ runs: [run, other] })} busy={false} onCancel={vi.fn()} canOperateProject={(projectID) => projectID === "project"} />);
  expect(screen.getAllByRole("button", { name: "Logs" })).toHaveLength(4);
  expect(screen.getAllByRole("button", { name: "Cancel" })).toHaveLength(2);
});


test("cancellation failures stay beside only the affected run in both layouts", () => {
  const other = { ...run, id: "run_other" };
  const view = render(<RunsView snapshot={apiSnapshot({ runs: [run, other] })} busy={false} onCancel={vi.fn()} canOperateProject={() => true} cancelError={{ key: run.id, message: "Cancellation unavailable" }} />);
  expect(screen.getAllByRole("alert")).toHaveLength(2);
  for (const alert of screen.getAllByRole("alert")) {
    expect(alert.textContent).toBe("Unable to cancel run: Cancellation unavailable");
    expect(alert.closest("tr, .grid")?.textContent).toContain(run.id);
    expect(alert.closest("tr, .grid")?.textContent).not.toContain(other.id);
  }
  view.rerender(<RunsView snapshot={apiSnapshot({ runs: [run, other] })} busy={true} onCancel={vi.fn()} canOperateProject={() => true} />);
  expect(screen.queryAllByRole("alert")).toHaveLength(0);
});


test.each(["succeeded", "failed", "canceled"])("%s runs without a finish timestamp do not report an active duration", (status) => {
  render(<RunsTable runs={[{ ...run, status }]} projects={[]} templates={[]} />);
  expect(screen.getAllByText("Not reported")).toHaveLength(2);
});
