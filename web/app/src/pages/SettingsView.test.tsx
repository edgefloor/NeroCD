import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { SettingsView } from "./SettingsView";

vi.mock("./ServiceAccountsSettings", () => ({
  ServiceAccountsSettingsPanel: ({ projects }: { projects: Array<{ name: string }> }) => <output>Service accounts for {projects.map((project) => project.name).join(", ")}</output>,
}));

test("owner controls use the complete membership lookup beyond the visible member page", () => {
  render(<SettingsView
    principal={{ id: "usr_owner", name: "Owner", email: "owner@example.test", provider: "local", roles: [] }}
    projects={[{ id: "proj_late", name: "Late project" } as never]}
    members={[]}
    authorizationMembers={[{ id: "member_late", project_id: "proj_late", user_id: "usr_owner", role: "owner", email: "owner@example.test" } as never]}
    onUpsertMember={vi.fn()}
  />);
  expect(screen.getByText("Service accounts for Late project")).toBeTruthy();
});

afterEach(cleanup);

test("project access keeps its draft until saving succeeds", () => {
  const save = vi.fn();
  render(<SettingsView
    principal={{ id: "operator", name: "Operator", email: "operator@example.test", provider: "local", roles: [] }}
    projects={[{ id: "first", name: "First", description: "", created_at: "" }, { id: "second", name: "Second", description: "", created_at: "" }]}
    members={[]} authorizationMembers={["first", "second"].map((project_id) => ({ id: project_id, project_id, user_id: "operator", role: "owner" } as never))} onUpsertMember={save}
  />);
  const email = screen.getByRole("textbox") as HTMLInputElement;
  const [project, role] = screen.getAllByRole("combobox") as HTMLSelectElement[];
  fireEvent.change(email, { target: { value: "member@example.test" } });
  fireEvent.change(project!, { target: { value: "second" } });
  fireEvent.change(role!, { target: { value: "maintainer" } });
  fireEvent.click(screen.getByRole("button", { name: "Update access" }));
  expect(email.value).toBe("member@example.test");
  expect(project!.value).toBe("second");
  expect(role!.value).toBe("maintainer");
  expect(save.mock.calls[0]![0]).toEqual({ project_id: "second", email: "member@example.test", role: "maintainer" });
  const success = save.mock.calls[0]![1] as () => void;
  success();
  expect(email.value).toBe("operator@example.test");
  expect(role!.value).toBe("viewer");
});

test("membership editing offers only owned projects", () => {
  render(<SettingsView
    principal={{ id: "operator", name: "Operator", email: "operator@example.test", provider: "local", roles: [] }}
    projects={["owned", "maintained", "viewed"].map((id) => ({ id, name: id, description: "", created_at: "" }))}
    members={[]}
    authorizationMembers={["owner", "maintainer", "viewer"].map((role, index) => ({ id: role, project_id: ["owned", "maintained", "viewed"][index], user_id: "operator", role, email: "operator@example.test" } as never))}
    onUpsertMember={vi.fn()}
  />);
  const project = screen.getByRole("combobox", { name: "Project" }) as HTMLSelectElement;
  expect(Array.from(project.options).map((option) => option.value)).toEqual(["owned"]);
});

test("non-owners retain member visibility without membership editing", () => {
  render(<SettingsView
    principal={{ id: "operator", name: "Operator", email: "operator@example.test", provider: "local", roles: [] }}
    projects={[{ id: "viewed", name: "Viewed project", description: "", created_at: "" }]}
    members={[{ id: "member", project_id: "viewed", user_id: "operator", role: "viewer", email: "operator@example.test" } as never]}
    onUpsertMember={vi.fn()}
  />);
  expect(screen.queryByRole("button", { name: "Update access" })).toBeNull();
  expect(screen.getByText("operator@example.test · Viewed project")).toBeTruthy();
});
