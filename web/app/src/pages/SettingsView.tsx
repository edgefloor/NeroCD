import { useId, type FormEvent, type ReactNode } from "react";
import { Users } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import type { Principal, Project, ProjectMember, ProjectMemberInput } from "@/api";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Badge } from "@/components/ui/badge";
import { projectName } from "@/lib/format";
import { UsersSettingsPanel } from "./UsersSettings";
import { ServiceAccountsSettingsPanel } from "./ServiceAccountsSettings";

type SettingsViewProps = {
  principal: Principal;
  projects: Project[];
  members: ProjectMember[];
  memberNavigation?: ReactNode;
  authorizationMembers?: ProjectMember[];
  loading?: boolean;
  readOnly?: boolean;
  updatingMember?: boolean;
  onUpsertMember: (input: ProjectMemberInput, onSuccess: () => void) => void;
};

export function SettingsView({
  principal, projects, members, authorizationMembers = members,
  loading, readOnly, updatingMember, onUpsertMember, memberNavigation,
}: SettingsViewProps): ReactNode {
  const formID = useId();
  if (loading) return <SkeletonCard rows={6} />;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (readOnly || updatingMember) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    onUpsertMember({
      project_id: String(form.get("project_id")),
      email: String(form.get("email")),
      role: String(form.get("role")) as ProjectMemberInput["role"],
    }, () => formElement.reset());
  }

  const manageableProjects = principal.roles.includes("system_admin")
    ? projects
    : projects.filter((project) => authorizationMembers.some((member) =>
      member.project_id === project.id && member.user_id === principal.id && member.role === "owner"));

  return <section className="grid min-w-0 gap-4 lg:grid-cols-2">
    <Card className="min-w-0">
      <CardHeader><CardTitle>Your account</CardTitle></CardHeader>
      <CardContent>
        <dl className="grid min-w-0 gap-3 text-sm">
          <div><dt className="text-xs text-muted-foreground">Name</dt><dd className="break-words">{principal.name}</dd></div>
          <div><dt className="text-xs text-muted-foreground">Email</dt><dd className="break-words">{principal.email}</dd></div>
          <div><dt className="text-xs text-muted-foreground">Global roles</dt><dd className="break-words">{principal.roles.length ? principal.roles.join(", ") : "None"}</dd></div>
        </dl>
      </CardContent>
    </Card>
    <Card className="min-w-0">
      <CardHeader><CardTitle><Users className="mr-2 inline h-4 w-4" />Project access</CardTitle></CardHeader>
      <CardContent className="grid min-w-0 gap-4">
        {manageableProjects.length > 0 ? <form className="grid min-w-0 gap-2" aria-busy={updatingMember} onSubmit={submit}>
          <label htmlFor={`${formID}-project`} className="text-sm font-medium">Project</label>
          <Select id={`${formID}-project`} name="project_id" disabled={updatingMember} required>
            {manageableProjects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
          </Select>
          <label htmlFor={`${formID}-email`} className="text-sm font-medium">Email</label>
          <Input id={`${formID}-email`} name="email" type="email" defaultValue={principal.email} disabled={updatingMember} required />
          <label htmlFor={`${formID}-role`} className="text-sm font-medium">Role</label>
          <Select id={`${formID}-role`} name="role" defaultValue="viewer" disabled={updatingMember}>
            <option>viewer</option><option>maintainer</option><option>owner</option>
          </Select>
          <Button type="submit" disabled={readOnly || updatingMember}>{updatingMember ? "Updating access..." : "Update access"}</Button>
        </form> : null}
        {members.map((member) => <div key={member.id} className="flex min-w-0 flex-wrap items-start justify-between gap-2 rounded border p-2">
          <span className="min-w-0 break-words">{member.email} · {projectName(projects, member.project_id)}</span>
          <Badge>{member.role}</Badge>
        </div>)}
        {memberNavigation}
      </CardContent>
    </Card>
    {manageableProjects.length > 0 ? <ServiceAccountsSettingsPanel projects={manageableProjects} readOnly={readOnly} /> : null}
    {principal.roles.includes("system_admin") ? <UsersSettingsPanel readOnly={readOnly} /> : null}
  </section>;
}
