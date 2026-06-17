import { FormEvent, ReactNode } from "react";
import { CheckCircle2, Settings, Shield, User, Users } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { ApiSnapshot } from "@/api";
import { upsertProjectMember } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Badge } from "@/components/ui/badge";
import { projectName } from "@/lib/format";

function Detail({ label, value, icon: Icon }: { label: string; value: string; icon?: React.ComponentType<{ className?: string }> }): ReactNode {
  return (
    <div className="grid grid-cols-[120px_1fr] gap-3 rounded-lg border border-border bg-card p-3 items-center">
      <span className="text-muted-foreground text-sm flex items-center gap-2">
        {Icon && <Icon className="h-4 w-4 text-muted-foreground" />}
        {label}
      </span>
      <span className="font-medium text-sm">{value}</span>
    </div>
  );
}

export function SettingsView({
  snapshot,
  token,
  busy,
  mutate,
  loading,
}: {
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading?: boolean;
}): ReactNode {
  async function grantSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await mutate(
      "project-member",
      () =>
        upsertProjectMember(token, {
          project_id: String(form.get("project_id") ?? ""),
          email: String(form.get("email") ?? ""),
          role: String(form.get("role") ?? "viewer"),
        }),
      "Project access updated",
    );
    event.currentTarget.reset();
  }

  if (loading) {
    return (
      <section className="grid gap-4 lg:grid-cols-2">
        <SkeletonCard rows={4} />
        <SkeletonCard rows={4} />
      </section>
    );
  }

  return (
    <section className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold">Principal</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-2 text-sm">
          <Detail label="Name" value={snapshot.principal.name} icon={User} />
          <Detail label="Email" value={snapshot.principal.email} icon={Shield} />
          <Detail label="Provider" value={snapshot.principal.provider} icon={CheckCircle2} />
          <Detail label="Roles" value={snapshot.principal.roles.join(", ")} icon={Settings} />
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2 text-base font-semibold">
            <Users className="h-4 w-4 text-muted-foreground" />
            Project access
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <form className="grid gap-3" onSubmit={(event) => void grantSubmit(event)}>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">Project</label>
              <select
                name="project_id"
                className="h-9 w-full rounded-md border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
                required
              >
                {snapshot.projects.map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_130px]">
              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">User Email</label>
                <Input name="email" type="email" defaultValue={snapshot.principal.email} required className="h-9" />
              </div>
              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">Role</label>
                <select
                  name="role"
                  defaultValue="viewer"
                  className="h-9 w-full rounded-md border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
                >
                  <option value="viewer">viewer</option>
                  <option value="maintainer">maintainer</option>
                  <option value="owner">owner</option>
                </select>
              </div>
            </div>
            <Button type="submit" disabled={busy === "project-member"} className="h-9">
              Update access
            </Button>
          </form>
          <div className="grid gap-2">
            {snapshot.projectMembers.map((member) => (
              <div key={member.id} className="flex items-center justify-between gap-3 rounded-lg border border-border p-3">
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">{member.name || member.email}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {member.email} · {projectName(snapshot.projects, member.project_id)}
                  </p>
                </div>
                <Badge variant={member.role === "owner" ? "success" : member.role === "maintainer" ? "warning" : "secondary"}>{member.role}</Badge>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </section>
  );
}
