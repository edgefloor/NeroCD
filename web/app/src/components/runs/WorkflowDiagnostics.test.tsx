import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { TaskRun } from "@/api";
import { WorkflowDiagnostics } from "./WorkflowDiagnostics";

const run = {
  id: "run_diagnostics",
  project_id: "project_diagnostics",
  run_spec: { type: "shell", inputs: {} },
  workflow: {
    steps: [
      { id: "checkout", name: "Checkout source", run_spec: { type: "shell", inputs: {} }, requires_ack: false },
      { id: "deploy", name: "Deploy", run_spec: { type: "shell", inputs: {} }, depends_on: ["checkout"], requires_ack: false },
    ],
  },
  workflow_state: {
    steps: [{ id: "checkout", name: "Checkout source", status: "succeeded", started_at: "2026-09-05T12:00:00Z", finished_at: "2026-09-05T12:01:30Z" }],
  },
  runner_tags: [],
  status: "running",
  requested_by: "user_diagnostics",
  started_at: "2026-09-05T12:00:00Z",
} satisfies TaskRun;

afterEach(cleanup);

test("shows declared order with only contract-backed status and timing", () => {
  render(<WorkflowDiagnostics run={run} />);
  expect(screen.getByRole("heading", { name: "Workflow steps" })).toBeTruthy();
  expect(screen.getByRole("list", { name: "Workflow step timeline" })).toBeTruthy();
  expect(screen.getByText("Checkout source")).toBeTruthy();
  expect(screen.getByText("Deploy")).toBeTruthy();
  expect(screen.getByText("Succeeded")).toBeTruthy();
  expect(screen.getByText("1m")).toBeTruthy();
  expect(screen.getByText("Status not reported")).toBeTruthy();
  expect(screen.getAllByText("Not reported")).toHaveLength(3);
  expect(screen.getByText("Depends on: checkout")).toBeTruthy();
  expect(screen.getByText(/Logs cover the whole run/)).toBeTruthy();
});

test("does not present unordered reported state as a declared timeline", () => {
  render(<WorkflowDiagnostics run={{ ...run, workflow: { steps: [] } }} />);
  expect(screen.getByText(/No workflow steps were declared/)).toBeTruthy();
  expect(screen.getByText(/no declared order is available/)).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Workflow step timeline" })).toBeNull();
});

test("does not infer active duration from a terminal state without a finish time", () => {
  render(<WorkflowDiagnostics run={{ ...run, workflow_state: { steps: [{ id: "checkout", name: "Checkout source", status: "failed", started_at: "2026-09-05T12:00:00Z" }] } }} />);
  expect(screen.getAllByText("Not reported").length).toBeGreaterThan(0);
  expect(screen.queryByText("In progress")).toBeNull();
});
