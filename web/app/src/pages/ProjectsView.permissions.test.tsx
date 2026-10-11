import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { ProjectsView } from "./ProjectsView";

afterEach(cleanup);
const snapshot = apiSnapshot({ projects: ["maintained", "viewed"].map((id) => ({ id, name: `${id} project`, description: "", created_at: "2026-01-01T00:00:00Z" })) });
const props = { snapshot, token: "", busy: "", mutate: vi.fn(async () => undefined) };

test("viewers can create their own project but cannot change existing projects", () => {
  render(<ProjectsView {...props} />);
  expect(screen.getByRole("heading", { name: "viewed project" })).toBeTruthy();
  expect(screen.queryAllByRole("button", { name: /^(Edit|Archive|Register source)$/ })).toHaveLength(0);
  expect(screen.getAllByRole("button", { name: "Create project" }).length).toBeGreaterThan(0);
});


test("project management and repository choices are scoped to writable projects", () => {
  render(<ProjectsView {...props} canOperateProject={(projectID) => projectID === "maintained"} />);
  expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(1);
  expect(screen.getAllByRole("button", { name: "Archive" })).toHaveLength(1);
  expect(screen.getByRole("heading", { name: "viewed project" })).toBeTruthy();
  expect(screen.getAllByRole("option", { name: "maintained project" }).length).toBeGreaterThan(0);
  expect(screen.queryAllByRole("option", { name: "viewed project" })).toHaveLength(0);
});


test("project refresh failure preserves edit and source drafts while blocking writes", () => {
  const view = render(<ProjectsView {...props} canOperateProject={() => true} />);
  const source = screen.getByRole<HTMLInputElement>("textbox", { name: "Repository Name" });
  fireEvent.change(source, { target: { value: "Unsaved source" } });
  fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
  const dialog = screen.getByRole("dialog");
  const name = within(dialog).getByRole<HTMLInputElement>("textbox", { name: "Project Name" });
  fireEvent.change(name, { target: { value: "Unsaved project" } });
  const retry = vi.fn();
  view.rerender(<ProjectsView {...props} canOperateProject={() => true} recovery={<button onClick={retry}>Retry projects</button>} />);
  expect(name.value).toBe("Unsaved project");
  expect(source.value).toBe("Unsaved source");
  expect(within(dialog).getByRole<HTMLButtonElement>("button", { name: "Save project" }).disabled).toBe(true);
  fireEvent.click(within(dialog).getByRole("button", { name: "Retry projects" }));
  expect(retry).toHaveBeenCalledOnce();
  view.rerender(<ProjectsView {...props} canOperateProject={() => true} />);
  expect(name.value).toBe("Unsaved project");
  expect(source.value).toBe("Unsaved source");
  expect(within(dialog).getByRole<HTMLButtonElement>("button", { name: "Save project" }).disabled).toBe(false);
});
