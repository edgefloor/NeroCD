import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
const snapshot = apiSnapshot({
  projects: ["maintained", "viewed"].map((id) => ({ id, name: `${id} project`, description: "", created_at: "2026-01-01T00:00:00Z" })),
  templates: ["maintained", "viewed"].map((id) => ({ id: `template_${id}`, project_id: id, name: `${id} template`, kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false })),
});
const props = { snapshot, token: "", busy: "", mutate: vi.fn(async () => undefined), onRunRequested: vi.fn() };

test("viewers retain template inventory without write controls", () => {
  render(<TemplatesView {...props} />);
  expect(screen.getAllByText("viewed template")).toHaveLength(2);
  expect(screen.queryAllByRole("button", { name: /Play|Edit|Create template/ })).toHaveLength(0);
  expect(screen.queryByRole("textbox", { name: "Template Name" })).toBeNull();
});


test("mixed project access limits actions and creation choices without hiding templates", () => {
  render(<TemplatesView {...props} canOperateProject={(projectID) => projectID === "maintained"} />);
  expect(screen.getAllByText("viewed template")).toHaveLength(2);
  expect(screen.getAllByRole("button", { name: "Play" })).toHaveLength(2);
  expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(2);
  expect(screen.getAllByRole("option", { name: "maintained project" }).length).toBeGreaterThan(0);
  expect(screen.queryAllByRole("option", { name: "viewed project" })).toHaveLength(0);
});


test("refresh failures preserve the edit draft and expose recovery inside its dialog", () => {
  const view = render(<TemplatesView {...props} canOperateProject={() => true} />);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = screen.getByRole("dialog");
  const name = within(dialog).getByRole<HTMLInputElement>("textbox", { name: "Template Name" });
  fireEvent.change(name, { target: { value: "Unsaved template" } });
  const retry = vi.fn();
  view.rerender(<TemplatesView {...props} canOperateProject={() => true} recovery={<button onClick={retry}>Retry templates</button>} />);
  expect(name.value).toBe("Unsaved template");
  expect(within(dialog).getByRole<HTMLButtonElement>("button", { name: "Save template" }).disabled).toBe(true);
  fireEvent.click(within(dialog).getByRole("button", { name: "Retry templates" }));
  expect(retry).toHaveBeenCalledOnce();
  view.rerender(<TemplatesView {...props} canOperateProject={() => true} />);
  expect(name.value).toBe("Unsaved template");
  expect(within(dialog).getByRole<HTMLButtonElement>("button", { name: "Save template" }).disabled).toBe(false);
});
