import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
const tags = ["linux", "zone=west,east", "line\nbreak"];
const snapshot = apiSnapshot({
  projects: [{ id: "project_one", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_one", project_id: "project_one", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {}, process: { command: ["echo", "ready"] } }, workflow: { steps: [] }, runner_tags: tags, requires_ack: false }],
});

test("renaming a template preserves comma and newline characters within runner tags", async () => {
  const update = vi.fn(async () => ({ id: "template_one" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create: vi.fn(), update, run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  fireEvent.change(dialog.getByRole("textbox", { name: "Template Name" }), { target: { value: "Renamed" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(update).toHaveBeenCalledWith(expect.objectContaining({ runner_tags: tags }));
});

test("invalid tag arrays preserve the creation draft and can be corrected", async () => {
  const create = vi.fn(async () => ({ id: "created" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create, update: vi.fn(), run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  const name = screen.getByRole<HTMLInputElement>("textbox", { name: "Template Name" });
  const input = screen.getByRole("textbox", { name: "Runner Tags" });
  fireEvent.change(name, { target: { value: "Draft" } });
  fireEvent.change(screen.getByRole("textbox", { name: "Command" }), { target: { value: "echo ready" } });
  fireEvent.change(input, { target: { value: '["linux", 123]' } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toBe("Enter comma-separated tags or a JSON array of strings.");
  expect(document.activeElement).toBe(input);
  expect(name.value).toBe("Draft");
  fireEvent.change(input, { target: { value: JSON.stringify(tags) } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ runner_tags: tags }));
});
