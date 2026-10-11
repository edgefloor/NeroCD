import { FormEvent, ReactNode, useId, useState } from "react";
import { Archive, FolderKanban, GitBranch, Pencil, Plus } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot, Project } from "@/api";
import { archiveProject, createProject, createRepository, updateProject } from "@/api/compat";
import type { MutateFn } from "@/hooks/useApi";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { formatDate, matchesQuery } from "@/lib/format";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

function ProjectCard({
  project,
  templates,
  runs,
  repositories,
  token,
  busy,
  mutate,
  recovery,
  canOperate,
}: {
  project: Project;
  templates: ApiSnapshot["templates"];
  runs: ApiSnapshot["runs"];
  repositories: ApiSnapshot["repositories"];
  token: string;
  busy: string;
  mutate: MutateFn;
  recovery?: ReactNode;
  canOperate: boolean;
}): ReactNode {
  const formId = useId();
  const projectTemplates = templates.filter((template) => template.project_id === project.id);
  const projectRuns = runs.filter((run) => run.project_id === project.id);
  const projectRepos = repositories.filter((repo) => repo.project_id === project.id);
  const [editOpen, setEditOpen] = useState(false);

  async function updateSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (recovery || busy) return;
    const form = new FormData(event.currentTarget);
    const updated = await mutate(
      `project:update:${project.id}`,
      () =>
        updateProject(token, project.id, {
          name: String(form.get("name") ?? ""),
          description: String(form.get("description") ?? ""),
        }),
      "Project updated",
    );
    if (updated !== undefined) setEditOpen(false);
  }

  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4 md:flex-row md:items-center md:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <FolderKanban className="h-4 w-4 text-muted-foreground" />
            <h3 className="text-base font-semibold">{project.name}</h3>
          </div>
          {project.description ? (
            <p className="max-w-2xl text-sm text-muted-foreground">{project.description}</p>
          ) : null}
          <p className="text-xs text-muted-foreground">Created {formatDate(project.created_at)}</p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="secondary" className="text-xs">
            {projectTemplates.length} templates in loaded page
          </Badge>
          <Badge variant="secondary" className="text-xs">
            {projectRuns.length} runs in loaded page
          </Badge>
          <Badge variant="secondary" className="text-xs">
            {projectRepos.length} sources in loaded page
          </Badge>
        </div>
        {canOperate ? <div className="flex flex-wrap gap-2">
          <Dialog open={editOpen} onOpenChange={setEditOpen}>
            <DialogTrigger asChild>
              <Button variant="outline" size="sm" className="h-8 rounded-xl">
                <Pencil className="mr-1.5 h-4 w-4" />
                Edit
              </Button>
            </DialogTrigger>
            <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>Edit project</DialogTitle>
              </DialogHeader>
              {recovery}
              <form aria-busy={Boolean(busy)} className="space-y-3" onSubmit={(event) => void updateSubmit(event)}>
                <div className="space-y-1.5">
                  <label htmlFor={`${formId}-project-name`} className="text-xs font-medium text-muted-foreground">Project Name</label>
                  <Input disabled={Boolean(busy)} id={`${formId}-project-name`} name="name" defaultValue={project.name} required className="h-9 rounded-xl" />
                </div>
                <div className="space-y-1.5">
                  <label htmlFor={`${formId}-description`} className="text-xs font-medium text-muted-foreground">Description</label>
                  <Textarea disabled={Boolean(busy)} id={`${formId}-description`} name="description" defaultValue={project.description} className="min-h-[80px] rounded-xl" />
                </div>
                <Button type="submit" disabled={Boolean(recovery) || Boolean(busy)} className="h-9 w-full rounded-xl">
                  Save project
                </Button>
              </form>
            </DialogContent>
          </Dialog>
          <Button
            variant="outline"
            size="sm"
            className="h-8 rounded-xl"
            disabled={Boolean(recovery) || Boolean(busy)}
            onClick={() => void mutate(`project:archive:${project.id}`, () => archiveProject(token, project.id), "Project archived")}
          >
            <Archive className="mr-1.5 h-4 w-4" />
            Archive
          </Button>
        </div> : null}
      </CardContent>
    </Card>
  );
}

function ProjectForms({
  projects,
  token,
  busy,
  mutate,
  recovery,
}: {
  projects: Project[];
  token: string;
  busy: string;
  mutate: MutateFn;
  recovery?: ReactNode;
}): ReactNode {
  const formId = useId();
  async function createProjectSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (recovery || busy) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const created = await mutate("project", () => createProject(token, { name: String(form.get("name") ?? ""), description: String(form.get("description") ?? "") }), "Project created");
    if (created !== undefined) formElement.reset();
  }
  async function createRepoSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (recovery || busy) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const created = await mutate("repository", () =>
      createRepository(token, {
        project_id: String(form.get("project_id") ?? ""),
        name: String(form.get("name") ?? ""),
        url: String(form.get("url") ?? ""),
        provider: String(form.get("provider") ?? "git"),
        default_ref: String(form.get("default_ref") ?? "main"),
      }), "Repository registered");
    if (created !== undefined) formElement.reset();
  }

  return (
    <div className="space-y-6">
      <form aria-busy={Boolean(busy)} className="space-y-3" onSubmit={(event) => void createProjectSubmit(event)}>
        <div className="space-y-1.5">
          <label htmlFor={`${formId}-project-name`} className="text-xs font-medium text-muted-foreground">Project Name</label>
          <Input disabled={Boolean(busy)} id={`${formId}-project-name`} name="name" placeholder="My Project" required className="h-9 rounded-xl" />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={`${formId}-description`} className="text-xs font-medium text-muted-foreground">Description</label>
          <Textarea disabled={Boolean(busy)} id={`${formId}-description`} name="description" placeholder="Description" className="min-h-[80px] rounded-xl" />
        </div>
        <Button type="submit" disabled={Boolean(recovery) || Boolean(busy)} className="h-9 rounded-xl">
          <Plus className="h-4 w-4 mr-2" />
          Create project
        </Button>
      </form>
      {projects.length > 0 ? <form aria-busy={Boolean(busy)} className="space-y-3 border-t pt-6" onSubmit={(event) => void createRepoSubmit(event)}>
        <div className="space-y-1.5">
          <label htmlFor={`${formId}-project`} className="text-xs font-medium text-muted-foreground">Project</label>
          <select disabled={Boolean(busy)} id={`${formId}-project`}
            name="project_id"
            className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
            required
          >
            {projects.length === 0 ? <option value="">No projects available</option> : null}
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor={`${formId}-repository-name`} className="text-xs font-medium text-muted-foreground">Repository Name</label>
          <Input disabled={Boolean(busy)} id={`${formId}-repository-name`} name="name" placeholder="Repository name" required className="h-9 rounded-xl" />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={`${formId}-repository-url`} className="text-xs font-medium text-muted-foreground">Repository URL</label>
          <Input disabled={Boolean(busy)} id={`${formId}-repository-url`} name="url" placeholder="https://github.com/acme/infra.git" required className="h-9 rounded-xl" />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-provider`} className="text-xs font-medium text-muted-foreground">Provider</label>
            <Input disabled={Boolean(busy)} id={`${formId}-provider`} name="provider" defaultValue="git" className="h-9 rounded-xl" />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-default-ref`} className="text-xs font-medium text-muted-foreground">Default Ref</label>
            <Input disabled={Boolean(busy)} id={`${formId}-default-ref`} name="default_ref" defaultValue="main" className="h-9 rounded-xl" />
          </div>
        </div>
        <Button type="submit" disabled={Boolean(recovery) || Boolean(busy)} className="h-9 rounded-xl">
          <GitBranch className="h-4 w-4 mr-2" />
          Register source
        </Button>
      </form> : null}
    </div>
  );
}

export function ProjectsView({
  snapshot,
  q,
  formProjects = snapshot.projects,
  token,
  busy,
  mutate,
  recovery,
  canOperateProject,
  loading,
}: {
  snapshot: ApiSnapshot;
  q?: string;
  formProjects?: Project[];
  token: string;
  busy: string;
  mutate: MutateFn;
  recovery?: ReactNode;
  canOperateProject?: (projectID: string) => boolean;
  loading?: boolean;
}): ReactNode {
  const writableProjects = formProjects.filter((project) => canOperateProject?.(project.id));
  const filtering = Boolean(q?.trim());
  const visibleProjects = snapshot.projects.filter((project) => matchesQuery(q, project.id, project.name, project.description));
  if (loading) {
    return (
      <section className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
        <div className="grid gap-3">
          <SkeletonCard rows={2} />
          <SkeletonCard rows={2} />
        </div>
        <SkeletonCard rows={4} />
      </section>
    );
  }

  return (
    <section className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
      <div className="grid items-start gap-3">
        <div className="xl:hidden">
          <Dialog>
            <DialogTrigger asChild>
              <Button className="h-9 w-full rounded-xl">
                <Plus className="mr-2 h-4 w-4" />
                Configure project
              </Button>
            </DialogTrigger>
            <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>Configure project</DialogTitle>
              </DialogHeader>
              {recovery}
              <ProjectForms projects={writableProjects} token={token} busy={busy} mutate={mutate} recovery={recovery} />
            </DialogContent>
          </Dialog>
        </div>
        {filtering && <p className="text-sm text-muted-foreground" role="status">{visibleProjects.length} of {snapshot.projects.length} projects match on this page.</p>}
        {filtering && visibleProjects.length === 0 ? <p className="py-5 text-sm text-muted-foreground">No projects match this filter on the loaded page.</p> : snapshot.projects.length === 0 ? (
          <EmptyState 
            title="No projects" 
            description="Create a project to get started." 
            icon={FolderKanban}
          />
        ) : (
          visibleProjects.map((project) => (
            <ProjectCard
              key={project.id}
              project={project}
              canOperate={Boolean(canOperateProject?.(project.id))}
              templates={snapshot.templates}
              runs={snapshot.runs}
              repositories={snapshot.repositories}
              token={token}
              busy={busy}
              mutate={mutate}
              recovery={recovery}
            />
          ))
        )}
      </div>
      <Card className="hidden xl:flex">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Configure project</CardTitle>
        </CardHeader>
        <CardContent>
          <ProjectForms projects={writableProjects} token={token} busy={busy} mutate={mutate} recovery={recovery} />
        </CardContent>
      </Card>
    </section>
  );
}
