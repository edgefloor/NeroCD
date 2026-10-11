import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
const snapshot = apiSnapshot({
  projects: [{ id: "project_one", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_one", project_id: "project_one", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }],
});

test("successful template execution hands the created run ID to the run workflow", async () => {
  const onRunRequested = vi.fn();
  const mutate = vi.fn<() => Promise<{ id: string } | undefined>>().mockResolvedValue({ id: "run_created" });
  render(<TemplatesView canOperateProject={() => true} snapshot={snapshot} busy="" actions={{ create: vi.fn(), update: vi.fn(), run: mutate }} onRunRequested={onRunRequested} />);
  await act(async () => { fireEvent.click(screen.getAllByRole("button", { name: "Play" })[0]!); });
  expect(onRunRequested).toHaveBeenCalledWith("run_created");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("failed template execution stays on the inventory", async () => {
  const onRunRequested = vi.fn();
  render(<TemplatesView canOperateProject={() => true} snapshot={snapshot} busy="" actions={{ create: vi.fn(), update: vi.fn(), run: vi.fn(async () => undefined) }} onRunRequested={onRunRequested} />);
  await act(async () => { fireEvent.click(screen.getAllByRole("button", { name: "Play" })[0]!); });
  expect(onRunRequested).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).toBeNull();
});
