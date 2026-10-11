import type { ComponentProps, ReactNode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { TaskRun } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { RunsView } from "./RunsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params, search = {}, ...props }: { children: ReactNode; params: { runId: string }; search?: { q?: string; offset?: number } } & ComponentProps<"a">) => {
  const query = new URLSearchParams();
  if (search.q) query.set("q", search.q);
  if (search.offset) query.set("offset", String(search.offset));
  return <a {...props} href={`/runs/${params.runId}${query.size ? `?${query}` : ""}`}>{children}</a>;
} }));

afterEach(cleanup);
const run: TaskRun = {
  id: "run_release", project_id: "project_platform", template_id: "template_release", status: "running",
  run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] },
  runner_tags: ["production"], requested_by: "operator_one", started_at: "2026-01-01T00:00:00Z",
};
const snapshot = apiSnapshot({
  runs: [run, { ...run, id: "run_other", project_id: "project_other", template_id: undefined, status: "succeeded", runner_tags: [], requested_by: "operator_two", finished_at: "2026-01-01T00:01:00Z" }],
  projects: [{ id: "project_platform", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_release", project_id: "project_platform", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }],
});

test.each(["run_release", "pLaTfOrM", "Release", "running", "production", "operator_one"])("run filter matches %s without unrelated rows", (q) => {
  const { container } = render(<RunsView snapshot={snapshot} q={q} token="" busy="" mutate={vi.fn(async () => undefined)} />);
  expect(container.querySelector('[data-run-detail-link="run_release"]')).not.toBeNull();
  expect(container.querySelector('[data-run-detail-link="run_other"]')).toBeNull();
  expect(screen.getByText("1 of 2 runs match on this page.")).toBeTruthy();
});

test("unmatched run filters explain the empty state and clearing restores rows", () => {
  const props = { snapshot, token: "", busy: "", mutate: vi.fn(async () => undefined) };
  const view = render(<RunsView {...props} q="no-match" />);
  expect(screen.getByText("No runs match this filter on the loaded page.")).toBeTruthy();
  expect(view.container.querySelector('[data-run-detail-link]')).toBeNull();
  view.rerender(<RunsView {...props} q="" />);
  expect(view.container.querySelector('[data-run-detail-link="run_other"]')).not.toBeNull();
});

test("filtering out a selected run preserves its log dialog and output", () => {
  render(<RunsView snapshot={snapshot} q="no-match" token="" busy="" mutate={vi.fn(async () => undefined)} selectedRunID={run.id} selectedLogs={[{ id: "log_one", run_id: run.id, sequence: 1, stream: "stdout", message: "Live release output", created_at: "2026-01-01T00:00:00Z" }]} onSelectRun={vi.fn()} />);
  expect(screen.getByRole("dialog", { name: "run run_release" })).toBeTruthy();
  expect(screen.getByText("Live release output")).toBeTruthy();
  expect(screen.getByText("No runs match this filter on the loaded page.")).toBeTruthy();
});

test("run filtering accepts omitted tags returned as null by the API", () => {
  const nullableTags = apiSnapshot({ ...snapshot, runs: [{ ...run, runner_tags: null as unknown as string[] }] });
  render(<RunsView snapshot={nullableTags} q="release" token="" busy="" mutate={vi.fn(async () => undefined)} />);
  expect(screen.getByText("1 of 1 runs match on this page.")).toBeTruthy();
});

test("run detail links preserve the active filter and page", () => {
  render(<RunsView snapshot={snapshot} q="running" offset={50} token="" busy="" mutate={vi.fn(async () => undefined)} />);
  const links = screen.getAllByRole("link", { name: "Details" });
  expect(links).toHaveLength(2);
  for (const link of links) expect(link.getAttribute("href")).toBe("/runs/run_release?q=running&offset=50");
});
