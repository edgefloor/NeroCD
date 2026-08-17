import { FormEvent, ReactNode, useState } from "react";
import { Archive, FolderKanban, GitBranch, Pencil, Plus } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot, Project } from "@/api";
import { archiveProject, createProject, createRepository, updateProject } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { formatDate } from "@/lib/format";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

function ProjectCard({
  project,
  templates,
  runs,
  repositories,
  token,
  busy,
  mutate,
}: {
  project: Project;
  templates: ApiSnapshot["templates"];
  runs: ApiSnapshot["runs"];
  repositories: ApiSnapshot["repositories"];
  token: string;
  busy: string;
  mutate: MutateFn;
}): ReactNode {
  const projectTemplates = templates.filter((template) => template.project_id === project.id);
  const projectRuns = runs.filter((run) => run.project_id === project.id);
  const projectRepos = repositories.filter((repo) => repo.project_id === project.id);
  const [editOpen, setEditOpen] = useState(false);

  async function updateSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await mutate(
      `project:update:${project.id}`,
      () =>
        updateProject(token, project.id, {
          name: String(form.get("name") ?? ""),
          description: String(form.get("description") ?? ""),
        }),
      "Project updated",
    );
    setEditOpen(false);
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
            {projectTemplates.length} templates
          </Badge>
          <Badge variant="secondary" className="text-xs">
            {projectRuns.length} runs
          </Badge>
          <Badge variant="secondary" className="text-xs">
            {projectRepos.length} sources
          </Badge>
        </div>
        <div className="flex flex-wrap gap-2">
          <Dialog open={editOpen} onOpenChange={setEditOpen}>
            <DialogTrigger asChild>
              <Button variant="outline" size="sm" className="h-8">
                <Pencil className="mr-1.5 h-4 w-4" />
                Edit
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Edit project</DialogTitle>
              </DialogHeader>
              <form className="space-y-3" onSubmit={(event) => void updateSubmit(event)}>
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">Project Name</label>
                  <Input name="name" defaultValue={project.name} required className="h-9" />
                </div>
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">Description</label>
                  <Textarea name="description" defaultValue={project.description} className="min-h-[80px]" />
                </div>
                <Button type="submit" disabled={busy === `project:update:${project.id}`} className="h-9 w-full">
                  Save project
                </Button>
              </form>
            </DialogContent>
          </Dialog>
          <Button
            variant="outline"
            size="sm"
            className="h-8"
            disabled={busy === `project:archive:${project.id}`}
            onClick={() => void mutate(`project:archive:${project.id}`, () => archiveProject(token, project.id), "Project archived")}
          >
            <Archive className="mr-1.5 h-4 w-4" />
            Archive
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function ProjectForms({
  snapshot,
  token,
  busy,
  mutate,
}: {
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
}): ReactNode {
  async function createProjectSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await mutate("project", () => createProject(token, { name: String(form.get("name") ?? ""), description: String(form.get("description") ?? "") }), "Project created");
    event.currentTarget.reset();
  }
  async function createRepoSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await mutate("repository", () =>
      createRepository(token, {
        project_id: String(form.get("project_id") ?? ""),
        name: String(form.get("name") ?? ""),
        url: String(form.get("url") ?? ""),
        provider: String(form.get("provider") ?? "git"),
        default_ref: String(form.get("default_ref") ?? "main"),
      }), "Repository registered");
    event.currentTarget.reset();
  }

  return (
    <div className="space-y-6">
      <form className="space-y-3" onSubmit={(event) => void createProjectSubmit(event)}>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">Project Name</label>
          <Input name="name" placeholder="My Project" required className="h-9" />
        </div>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">Description</label>
          <Textarea name="description" placeholder="Description" className="min-h-[80px]" />
        </div>
        <Button type="submit" disabled={busy === "project"} className="h-9">
          <Plus className="h-4 w-4 mr-2" />
          Create project
        </Button>
      </form>
      <form className="space-y-3 border-t pt-6" onSubmit={(event) => void createRepoSubmit(event)}>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">Project</label>
          <Select name="project_id" required>
            {snapshot.projects.length === 0 ? <option value="">No projects available</option> : null}
            {snapshot.projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">Repository Name</label>
          <Input name="name" placeholder="Repository name" required className="h-9" />
        </div>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">Repository URL</label>
          <Input name="url" placeholder="https://github.com/acme/infra.git" required className="h-9" />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Provider</label>
            <Input name="provider" defaultValue="git" className="h-9" />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Default Ref</label>
            <Input name="default_ref" defaultValue="main" className="h-9" />
          </div>
        </div>
        <Button type="submit" disabled={busy === "repository"} className="h-9">
          <GitBranch className="h-4 w-4 mr-2" />
          Register source
        </Button>
      </form>
    </div>
  );
}

export function ProjectsView({
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
  if (loading) {
    return (
      <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
        <div className="grid gap-3">
          <SkeletonCard rows={2} />
          <SkeletonCard rows={2} />
        </div>
        <SkeletonCard rows={4} />
      </section>
    );
  }

  return (
    <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
      <div className="grid gap-3">
        <div className="xl:hidden">
          <Dialog>
            <DialogTrigger asChild>
              <Button className="h-9 w-full">
                <Plus className="mr-2 h-4 w-4" />
                Configure project
              </Button>
            </DialogTrigger>
            <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>Configure project</DialogTitle>
              </DialogHeader>
              <ProjectForms snapshot={snapshot} token={token} busy={busy} mutate={mutate} />
            </DialogContent>
          </Dialog>
        </div>
        {snapshot.projects.length === 0 ? (
          <EmptyState 
            title="No projects" 
            description="Create a project to get started." 
            icon={FolderKanban}
          />
        ) : (
          snapshot.projects.map((project) => (
            <ProjectCard
              key={project.id}
              project={project}
              templates={snapshot.templates}
              runs={snapshot.runs}
              repositories={snapshot.repositories}
              token={token}
              busy={busy}
              mutate={mutate}
            />
          ))
        )}
      </div>
      <Card className="hidden xl:block">
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold">Configure project</CardTitle>
        </CardHeader>
        <CardContent>
          <ProjectForms snapshot={snapshot} token={token} busy={busy} mutate={mutate} />
        </CardContent>
      </Card>
    </section>
  );
}
