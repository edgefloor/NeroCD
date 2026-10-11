import type { ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { ProjectsView } from "./ProjectsView";
import { TemplatesView } from "./TemplatesView";
import { ApprovalsView } from "./ApprovalsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params }: { children: ReactNode; params: { runId: string } }) => <a href={`/runs/${params.runId}`}>{children}</a> }));

afterEach(cleanup);
const snapshot = apiSnapshot({
  projects: [
    { id: "p_alpha", name: "Alpha project", description: "Production infrastructure", created_at: "2026-01-01T00:00:00Z" },
    { id: "p_beta", name: "Beta project", description: "Staging", created_at: "2026-01-01T00:00:00Z" },
  ],
  templates: [
    { id: "t_alpha", project_id: "p_alpha", name: "Alpha deploy", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: ["production"], requires_ack: true },
    { id: "t_beta", project_id: "p_beta", name: "Beta deploy", kind: "python", run_spec: { type: "python", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false },
  ],
  runs: [{ id: "run_alpha", project_id: "p_alpha", template_id: "t_alpha", status: "waiting_approval", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, workflow_state: { steps: [] }, runner_tags: ["production"], requested_by: "alice", started_at: "2026-01-01T00:00:00Z" }],
  approvals: [
    { id: "approval_alpha", run_id: "run_alpha", status: "pending", requested_by: "alice", created_at: "2026-01-01T00:00:00Z" },
    { id: "approval_beta", run_id: "run_beta", status: "approved", requested_by: "bob", approved_by: "carol", created_at: "2026-01-01T00:00:00Z" },
  ],
});
const actions = { run: vi.fn(async () => undefined), create: vi.fn(async () => undefined), update: vi.fn(async () => undefined), archive: vi.fn(async () => undefined), repository: vi.fn(async () => undefined) };
const props = { actions, snapshot, busy: "" };

test("project filtering uses descriptions and preserves form choices and drafts", () => {
  const view = render(<ProjectsView canOperateProject={() => true} {...props} q="production" />);
  expect(screen.getByRole("heading", { name: "Alpha project" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Beta project" })).toBeNull();
  expect(screen.getByRole("option", { name: "Beta project" })).toBeTruthy();
  fireEvent.change(screen.getByPlaceholderText("My Project"), { target: { value: "Draft project" } });
  view.rerender(<ProjectsView canOperateProject={() => true} {...props} q="no-match" />);
  expect(screen.getByText("No projects match this filter on the loaded page.")).toBeTruthy();
  expect((screen.getByPlaceholderText("My Project") as HTMLInputElement).value).toBe("Draft project");
  view.rerender(<ProjectsView canOperateProject={() => true} {...props} q="" />);
  expect(screen.getByRole("heading", { name: "Beta project" })).toBeTruthy();
});

test.each(["p_alpha", "Alpha project", "production", "shell"])("template filtering matches %s while keeping create form context", (q) => {
  render(<TemplatesView canOperateProject={() => true} onRunRequested={vi.fn()} {...props} q={q} />);
  expect(screen.getAllByText("Alpha deploy").length).toBeGreaterThan(0);
  expect(screen.queryByText("Beta deploy")).toBeNull();
  expect(screen.getByRole("option", { name: "Beta project" })).toBeTruthy();
});

test("template filters distinguish no matches and preserve an unfinished create form", () => {
  const view = render(<TemplatesView canOperateProject={() => true} onRunRequested={vi.fn()} {...props} q="" />);
  fireEvent.change(screen.getByPlaceholderText("Patch Linux fleet"), { target: { value: "Draft template" } });
  view.rerender(<TemplatesView canOperateProject={() => true} onRunRequested={vi.fn()} {...props} q="no-match" />);
  expect(screen.getByText("No templates match this filter on the loaded page.")).toBeTruthy();
  expect((screen.getByPlaceholderText("Patch Linux fleet") as HTMLInputElement).value).toBe("Draft template");
});

test.each(["Alpha project", "Alpha deploy", "alice", "approval_alpha", "SHELL", "production"])("approval filtering matches %s and retains actionable run context", (q) => {
  render(<ApprovalsView canOperateProject={() => true} {...props} busy={false} onDecide={vi.fn()} q={q} />);
  expect(screen.getByText("Alpha deploy")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Approve" })).toBeTruthy();
  expect(screen.queryByText("run_beta")).toBeNull();
});

test("approval filter includes history and distinguishes unmatched pending approvals", () => {
  render(<ApprovalsView canOperateProject={() => true} {...props} busy={false} onDecide={vi.fn()} q="carol" />);
  expect(screen.getByRole("link", { name: "run_beta" }).getAttribute("href")).toBe("/runs/run_beta");
  expect(screen.getByText("No pending approvals match this filter on the loaded page.")).toBeTruthy();
  expect(screen.getByText("1 waiting")).toBeTruthy();
});
