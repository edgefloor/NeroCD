import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot, type MutateFn } from "@/api/compat";
import { ProjectsView } from "./ProjectsView";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
function projectActions(mutate: (key: string, action: () => Promise<undefined>, success: string) => Promise<unknown>) {
  const action = () => mutate("project", async () => undefined, "");
  return { create: action, update: action, archive: action, repository: action };
}
const snapshot = apiSnapshot({
  projects: [{ id: "project_one", name: "Platform", description: "", created_at: "2026-01-01T00:00:00Z" }],
  templates: [{ id: "template_one", project_id: "project_one", name: "Release", kind: "shell", run_spec: { type: "shell", inputs: {} }, workflow: { steps: [] }, runner_tags: [], requires_ack: false }],
});

for (const [kind, View, buttonName] of [
  ["project", ProjectsView, "Create project"],
  ["repository", ProjectsView, "Register source"],
  ["template", TemplatesView, "Create template"],
] as const) {
  test(`${kind} creation preserves a failed draft and resets after a successful retry`, async () => {
    const mutate = vi.fn<MutateFn>().mockResolvedValueOnce(undefined).mockResolvedValueOnce({ id: "created" });
    render(<View canOperateProject={() => true} onRunRequested={vi.fn()} snapshot={snapshot} token="" busy="" mutate={mutate as MutateFn} actions={projectActions(mutate)} />);
    const form = screen.getAllByRole("button", { name: buttonName }).map((button) => button.closest("form")).find(Boolean)!;
    const name = form.elements.namedItem("name") as HTMLInputElement;
    fireEvent.change(name, { target: { value: "My draft" } });
    await act(async () => { fireEvent.submit(form); });
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(name.value).toBe("My draft");
    await act(async () => { fireEvent.submit(form); });
    expect(mutate).toHaveBeenCalledTimes(2);
    expect(name.value).toBe("");
  });
}

for (const [kind, View] of [["project", ProjectsView], ["template", TemplatesView]] as const) {
  test(`${kind} editing keeps the dialog and draft on failure, then closes on success`, async () => {
    const mutate = vi.fn<MutateFn>().mockResolvedValueOnce(undefined).mockResolvedValueOnce({ id: "saved" });
    render(<View canOperateProject={() => true} onRunRequested={vi.fn()} snapshot={snapshot} token="" busy="" mutate={mutate as MutateFn} actions={projectActions(mutate)} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
    const dialog = screen.getByRole("dialog", { name: `Edit ${kind}` });
    const form = dialog.querySelector("form")!;
    const name = form.elements.namedItem("name") as HTMLInputElement;
    fireEvent.change(name, { target: { value: "Updated draft" } });
    await act(async () => { fireEvent.submit(form); });
    expect(screen.getByRole("dialog", { name: `Edit ${kind}` })).toBeTruthy();
    expect(name.value).toBe("Updated draft");
    await act(async () => { fireEvent.submit(form); });
    expect(screen.queryByRole("dialog", { name: `Edit ${kind}` })).toBeNull();
  });
}

test("templates created without tags remain visible and editable", () => {
  const nullableTags = apiSnapshot({ ...snapshot, templates: [{ ...snapshot.templates[0]!, runner_tags: null as unknown as string[] }] });
  render(<TemplatesView canOperateProject={() => true} onRunRequested={vi.fn()} snapshot={nullableTags} token="" busy="" mutate={vi.fn(async () => undefined)} />);
  expect(screen.getAllByText("Release").length).toBeGreaterThan(0);
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = screen.getByRole("dialog", { name: "Edit template" });
  expect((dialog.querySelector('input[name="runner_tags"]') as HTMLInputElement).value).toBe("");
});


test("project form labels identify fields and mobile dialog fields stay independent", () => {
  render(<ProjectsView canOperateProject={() => true} snapshot={snapshot} busy="" actions={projectActions(vi.fn(async () => undefined))} />);
  const desktopName = screen.getByRole("textbox", { name: "Project Name" }) as HTMLInputElement;
  for (const name of ["Description", "Repository Name", "Repository URL", "Provider", "Default Ref"]) {
    expect(screen.getByRole("textbox", { name })).toBeTruthy();
  }
  expect(screen.getByRole("combobox", { name: "Project" })).toBeTruthy();
  fireEvent.change(desktopName, { target: { value: "Desktop draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Configure project" }));
  const dialog = within(screen.getByRole("dialog", { name: "Configure project" }));
  const mobileName = dialog.getByRole("textbox", { name: "Project Name" });
  fireEvent.change(mobileName, { target: { value: "Mobile draft" } });
  expect(desktopName.value).toBe("Desktop draft");
  expect((mobileName as HTMLInputElement).value).toBe("Mobile draft");
});

test("template fields and mobile edit control have accessible names", () => {
  render(<TemplatesView canOperateProject={() => true} onRunRequested={vi.fn()} snapshot={snapshot} token="" busy="" mutate={vi.fn(async () => undefined)} />);
  for (const name of ["Template Name", "Runner Tags", "Command"]) expect(screen.getByRole("textbox", { name })).toBeTruthy();
  for (const name of ["Project", "Kind"]) expect(screen.getByRole("combobox", { name })).toBeTruthy();
  const editButtons = screen.getAllByRole("button", { name: "Edit" });
  expect(editButtons).toHaveLength(2);
  fireEvent.click(editButtons[1]!);
  const dialog = within(screen.getByRole("dialog", { name: "Edit template" }));
  expect((dialog.getByRole("textbox", { name: "Template Name" }) as HTMLInputElement).value).toBe("Release");
  expect(dialog.getByRole("combobox", { name: "Project" })).toBeTruthy();
});
