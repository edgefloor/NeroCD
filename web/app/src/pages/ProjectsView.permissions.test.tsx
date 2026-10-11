import { cleanup, render, screen } from "@testing-library/react";
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
