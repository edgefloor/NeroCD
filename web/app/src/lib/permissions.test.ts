import { expect, test } from "vitest";
import type { Principal, ProjectMember } from "@/api";
import { canMaintainProject } from "./permissions";

const principal: Principal = { id: "user_one", roles: [], name: "Operator", email: "operator@example.test", provider: "oidc" };
const member = (role: string, projectID = "project_one", userID = principal.id): ProjectMember => ({ id: "membership", project_id: projectID, user_id: userID, email: principal.email, role, created_at: "2026-01-01T00:00:00Z" });

test.each(["maintainer", "owner"])("project %s can operate its project only", (role) => {
  expect(canMaintainProject(principal, [member(role)], "project_one")).toBe(true);
  expect(canMaintainProject(principal, [member(role)], "project_two")).toBe(false);
});

test("viewers, other users and missing data do not expose write actions", () => {
  expect(canMaintainProject(principal, [member("viewer")], "project_one")).toBe(false);
  expect(canMaintainProject(principal, [member("owner", "project_one", "someone_else")], "project_one")).toBe(false);
  expect(canMaintainProject(undefined, [member("owner")], "project_one")).toBe(false);
  expect(canMaintainProject(principal, [], "project_one")).toBe(false);
  expect(canMaintainProject(principal, [member("owner")], undefined)).toBe(false);
});

test("system admin is global but project role names in principal roles are not", () => {
  expect(canMaintainProject({ ...principal, roles: ["system_admin"] }, [], "project_one")).toBe(true);
  expect(canMaintainProject({ ...principal, roles: ["maintainer"] }, [], "project_one")).toBe(false);
  expect(canMaintainProject({ ...principal, roles: ["owner"] }, [], "project_one")).toBe(false);
});
