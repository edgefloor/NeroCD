import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
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
