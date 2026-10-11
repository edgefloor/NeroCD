import type { ReactNode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { ApprovalList } from "./ApprovalsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params }: { children: ReactNode; params: { runId: string } }) => <a href={`/runs/${params.runId}`}>{children}</a> }));

afterEach(cleanup);
const approval = { id: "approval_one", run_id: "run_one", status: "pending", requested_by: "operator", created_at: "2026-01-01T00:00:00Z" } as const;
const run = { id: "run_one", project_id: "project_one", run_spec: { type: "shell", inputs: {} }, runner_tags: [] } as never;
const snapshot = apiSnapshot({ runs: [run] });
const props = { approvals: [approval], snapshot, canOperateProject: () => true, token: "", busy: "", mutate: vi.fn() };

test.each(["approve:run_one", "reject:run_one"])("both decisions are disabled while %s is pending", (busy) => {
  render(<ApprovalList {...props} busy={busy} />);
  for (const name of ["Approve", "Reject"]) expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
});

test("unavailable runs do not expose decisions", () => {
  render(<ApprovalList {...props} snapshot={apiSnapshot({})} />);
  expect(screen.getByText(/Run unavailable/)).toBeTruthy();
  expect(screen.queryAllByRole("button")).toHaveLength(0);
});


test("viewers can inspect a run without decision controls", () => {
  render(<ApprovalList {...props} canOperateProject={() => false} />);
  expect(screen.queryAllByRole("button")).toHaveLength(0);
  expect(screen.getByRole("link", { name: "Run details" }).getAttribute("href")).toBe("/runs/run_one");
});
