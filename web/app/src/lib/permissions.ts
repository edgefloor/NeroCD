import type { Principal, ProjectMember } from "@/api";

// Presentation only. The API remains responsible for authorizing each request.
export function canMaintainProject(principal: Principal | undefined, members: ProjectMember[], projectID: string | undefined): boolean {
  if (!principal) return false;
  if (principal.roles.includes("system_admin")) return true;
  return Boolean(projectID && members.some((member) =>
    member.user_id === principal.id && member.project_id === projectID && ["owner", "maintainer"].includes(member.role)));
}

export function canOwnProject(principal: Principal | undefined, members: ProjectMember[], projectID: string | undefined): boolean {
  if (!principal) return false;
  return principal.roles.includes("system_admin") || Boolean(projectID && members.some((member) => member.user_id === principal.id && member.project_id === projectID && member.role === "owner"));
}
