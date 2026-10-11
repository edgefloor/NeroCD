import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
const command = ["sh", "-c", "printf '%s\\n' \"hello world\"", "", "path with spaces", "line\r\nbreak"];
const snapshot = apiSnapshot({
  projects: [{ id: "project_one", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_one", project_id: "project_one", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {}, process: { command } }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }],
});

test("renaming a template preserves its exact command arguments", async () => {
  const update = vi.fn(async () => ({ id: "template_one" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create: vi.fn(), update, run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  fireEvent.change(dialog.getByRole("textbox", { name: "Template Name" }), { target: { value: "Renamed" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(update).toHaveBeenCalledWith(expect.objectContaining({ name: "Renamed", run_spec: expect.objectContaining({ process: { command } }) }));
});

test("invalid command quoting keeps the draft and corrected quoting sends complete arguments", async () => {
  const create = vi.fn(async () => ({ id: "created" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create, update: vi.fn(), run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  const name = screen.getByRole<HTMLInputElement>("textbox", { name: "Template Name" });
  const input = screen.getByRole("textbox", { name: "Command" });
  fireEvent.change(name, { target: { value: "Draft" } });
  fireEvent.change(input, { target: { value: 'echo "unfinished' } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain("Close the double quote");
  expect(document.activeElement).toBe(input);
  expect(name.value).toBe("Draft");
  fireEvent.change(input, { target: { value: 'echo "hello world" \'\'' } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ run_spec: expect.objectContaining({ process: { command: ["echo", "hello world", ""] } }) }));
});


test("discarding an invalid command clears its error when the editor reopens", async () => {
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create: vi.fn(), update: vi.fn(), run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  fireEvent.change(dialog.getByRole("textbox", { name: "Command" }), { target: { value: "echo 'unfinished" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(dialog.getByRole("alert")).toBeTruthy();
  fireEvent.click(dialog.getByRole("button", { name: "Close" }));
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  expect(screen.queryByRole("alert")).toBeNull();
});

test("clearing a required command reports the problem without saving the previous command", async () => {
  const update = vi.fn(async () => ({ id: "template_one" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create: vi.fn(), update, run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  const input = dialog.getByRole<HTMLTextAreaElement>("textbox", { name: "Command" });
  fireEvent.change(input, { target: { value: "" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(update).not.toHaveBeenCalled();
  expect(dialog.getByRole("alert").textContent).toBe("Enter a command. This template requires a process command.");
  expect(input.value).toBe("");
  expect(document.activeElement).toBe(input);
});

test.each(["shell", "ansible", "opentofu", "terraform", "python", "powershell"])("new %s templates require a command and preserve incomplete drafts", async (kind) => {
  const create = vi.fn(async () => ({ id: "created" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create, update: vi.fn(), run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  const select = screen.getByRole<HTMLSelectElement>("combobox", { name: "Kind" });
  expect(select.value).toBe("shell");
  fireEvent.change(select, { target: { value: kind } });
  const name = screen.getByRole<HTMLInputElement>("textbox", { name: "Template Name" });
  const input = screen.getByRole("textbox", { name: "Command" });
  fireEvent.change(name, { target: { value: "New task" } });
  fireEvent.change(input, { target: { value: "   " } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toBe("Enter the command this template should run.");
  expect(document.activeElement).toBe(input);
  expect(name.value).toBe("New task");
  fireEvent.change(input, { target: { value: "echo ready" } });
  await act(async () => { fireEvent.submit(input.closest("form")!); });
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ kind, run_spec: expect.objectContaining({ type: kind, process: { command: ["echo", "ready"] } }) }));
  expect(select.value).toBe("shell");
});

test.each([
  ["release-task", "shell"],
  ["python", "shell"],
  ["compose-deploy", "compose-deploy"],
])("renaming kind %s preserves execution type %s", async (kind, type) => {
  const template = { ...snapshot.templates[0]!, kind, run_spec: { ...snapshot.templates[0]!.run_spec, type } };
  const update = vi.fn(async () => ({ id: template.id }));
  render(<TemplatesView snapshot={{ ...snapshot, templates: [template] }} busy="" actions={{ create: vi.fn(), update, run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  expect(dialog.getByRole<HTMLSelectElement>("combobox", { name: "Kind" }).value).toBe(kind);
  fireEvent.change(dialog.getByRole("textbox", { name: "Template Name" }), { target: { value: "Renamed" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(update).toHaveBeenCalledWith(expect.objectContaining({ kind, run_spec: template.run_spec }));
});

test("changing the selected kind also changes the execution type", async () => {
  const update = vi.fn(async () => ({ id: "template_one" }));
  render(<TemplatesView snapshot={snapshot} busy="" actions={{ create: vi.fn(), update, run: vi.fn() }} onRunRequested={vi.fn()} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  fireEvent.change(dialog.getByRole("combobox", { name: "Kind" }), { target: { value: "python" } });
  await act(async () => { fireEvent.click(dialog.getByRole("button", { name: "Save template" })); });
  expect(update).toHaveBeenCalledWith(expect.objectContaining({ kind: "python", run_spec: expect.objectContaining({ type: "python" }) }));
});
