import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot, type MutateFn } from "@/api/compat";
import { ProjectsView } from "./ProjectsView";
import { TemplatesView } from "./TemplatesView";

afterEach(cleanup);
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
    render(<View snapshot={snapshot} token="" busy="" mutate={mutate as MutateFn} />);
    const form = screen.getByRole("button", { name: buttonName }).closest("form")!;
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
    render(<View snapshot={snapshot} token="" busy="" mutate={mutate as MutateFn} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
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
  render(<TemplatesView snapshot={nullableTags} token="" busy="" mutate={vi.fn(async () => undefined)} />);
  expect(screen.getAllByText("Release").length).toBeGreaterThan(0);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  const dialog = screen.getByRole("dialog", { name: "Edit template" });
  expect((dialog.querySelector('input[name="runner_tags"]') as HTMLInputElement).value).toBe("");
});
