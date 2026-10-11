import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { HomeView } from "./HomeView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a> }));

afterEach(cleanup);

const snapshot = apiSnapshot({
  projects: [{ id: "project_one", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_one", project_id: "project_one", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: true }],
  runs: [{ id: "run_one", project_id: "project_one", template_id: "template_one", status: "running", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] }, runner_tags: ["production"], requested_by: "operator", started_at: "2026-01-01T00:00:00Z" }],
  approvals: [{ id: "approval_one", run_id: "run_one", status: "pending", requested_by: "operator", created_at: "2026-01-01T00:00:00Z" }],
  logs: [{ id: "log_one", run_id: "run_one", sequence: 1, stream: "stdout", message: "Artifact uploaded", created_at: "2026-01-01T00:00:00Z" }],
});

function show(query: string) {
  const onClearQuery = vi.fn();
  render(<HomeView canOperateProject={() => true} snapshot={snapshot} workSnapshot={snapshot} query={query} onClearQuery={onClearQuery} busy={false} onDecide={vi.fn(async () => undefined)} />);
  return onClearQuery;
}

test("dashboard filtering hides unrelated activity without changing overall totals", () => {
  const clear = show("no-such-activity");
  expect(screen.getByText("No runs match this search.")).toBeTruthy();
  expect(screen.getByText("No pending approvals match this search.")).toBeTruthy();
  expect(screen.getByText("No logs match this search.")).toBeTruthy();
  expect(screen.getByText("0 matching records")).toBeTruthy();
  expect(screen.getByText("1 unfinished run in loaded page")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear" }));
  expect(clear).toHaveBeenCalledOnce();
});

test("dashboard filtering matches related project names and keeps approval context", () => {
  show("pLaTfOrM");
  expect(screen.getAllByText("Release").length).toBeGreaterThan(0);
  expect(screen.getByRole("button", { name: "Approve" })).toBeTruthy();
  expect(screen.getByText("2 matching records")).toBeTruthy();
  expect(screen.getByText("No logs match this search.")).toBeTruthy();
});

test("dashboard filtering matches log messages independently of runs", () => {
  show("uploaded");
  expect(screen.getByText("Artifact uploaded")).toBeTruthy();
  expect(screen.getByText("No runs match this search.")).toBeTruthy();
  expect(screen.getByText("1 matching records")).toBeTruthy();
});


test.each(["SHELL", "production"])("dashboard filtering matches execution metadata %s", (q) => {
  show(q);
  expect(screen.getByText("2 matching records")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Approve" })).toBeTruthy();
});
