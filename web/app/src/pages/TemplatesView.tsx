import { FormEvent, ReactNode, useId, useState } from "react";
import { Layers3, Pencil, Play, Plus } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot, TaskTemplate } from "@/api";
import { createTemplate, requestRun, updateTemplate } from "@/api/compat";
import type { MutateFn } from "@/hooks/useApi";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { matchesQuery, projectName } from "@/lib/format";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

function TemplateForm({
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
  const formId = useId();
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const kind = String(form.get("kind") ?? "shell");
    const command = String(form.get("command") ?? "").trim().split(/\s+/).filter(Boolean);
    const created = await mutate("template", () =>
      createTemplate(token, {
        project_id: String(form.get("project_id") ?? ""),
        name: String(form.get("name") ?? ""),
        kind,
        run_spec: { type: kind, inputs: {}, process: command.length ? { command } : undefined },
        runner_tags: String(form.get("runner_tags") ?? "").split(",").map((tag) => tag.trim()).filter(Boolean),
        requires_ack: form.has("requires_ack"),
      }), "Template created");
    if (created !== undefined) formElement.reset();
  }

  return (
    <form className="space-y-4" onSubmit={(event) => void submit(event)}>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-project`} className="text-xs font-medium text-muted-foreground">Project</label>
        <select id={`${formId}-project`}
          name="project_id"
          className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
          required
        >
          {snapshot.projects.length === 0 ? <option value="">No projects available</option> : null}
          {snapshot.projects.map((project) => (
            <option key={project.id} value={project.id}>
              {project.name}
            </option>
          ))}
        </select>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-template-name`} className="text-xs font-medium text-muted-foreground">Template Name</label>
        <Input id={`${formId}-template-name`} name="name" placeholder="Patch Linux fleet" required className="h-9 rounded-xl" />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-kind`} className="text-xs font-medium text-muted-foreground">Kind</label>
        <select id={`${formId}-kind`}
          name="kind"
          className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
        >
          {["ansible", "opentofu", "shell", "terraform", "python", "powershell"].map((kind) => (
            <option key={kind} value={kind}>
              {kind}
            </option>
          ))}
        </select>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-runner-tags`} className="text-xs font-medium text-muted-foreground">Runner Tags</label>
        <Input id={`${formId}-runner-tags`} name="runner_tags" placeholder="linux, prod" className="h-9 rounded-xl" />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-command`} className="text-xs font-medium text-muted-foreground">Command</label>
        <Input id={`${formId}-command`} name="command" placeholder="ansible-playbook site.yml" className="h-9 rounded-xl" />
      </div>
      <label className="flex items-center gap-2 text-sm py-2">
        <input name="requires_ack" type="checkbox" className="rounded border-border" />
        <span>Requires approval before execution</span>
      </label>
      <Button type="submit" disabled={busy === "template"} className="h-9 w-full rounded-xl">
        <Plus className="h-4 w-4 mr-2" />
        Create template
      </Button>
    </form>
  );
}

function TemplateEditDialog({
  trigger,
  template,
  snapshot,
  token,
  busy,
  mutate,
}: {
  trigger?: ReactNode;
  template: TaskTemplate;
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
}): ReactNode {
  const formId = useId();
  const [open, setOpen] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const kind = String(form.get("kind") ?? template.kind);
    const command = String(form.get("command") ?? "").trim().split(/\s+/).filter(Boolean);
    const updated = await mutate(
      `template:update:${template.id}`,
      () =>
        updateTemplate(token, template.id, {
          project_id: String(form.get("project_id") ?? template.project_id),
          name: String(form.get("name") ?? template.name),
          kind,
          run_spec: {
            ...template.run_spec,
            type: kind,
            process: command.length ? { ...template.run_spec.process, command } : template.run_spec.process,
          },
          workflow: template.workflow,
          runner_tags: String(form.get("runner_tags") ?? "")
            .split(",")
            .map((tag) => tag.trim())
            .filter(Boolean),
          requires_ack: form.has("requires_ack"),
        }),
      "Template updated",
    );
    if (updated !== undefined) setOpen(false);
  }
  const command = template.run_spec.process?.command?.join(" ") ?? "";

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm" className="h-8 rounded-xl">
            <Pencil className="mr-1.5 h-4 w-4" />
            Edit
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Edit template</DialogTitle>
        </DialogHeader>
        <form className="space-y-4" onSubmit={(event) => void submit(event)}>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-project`} className="text-xs font-medium text-muted-foreground">Project</label>
            <select id={`${formId}-project`}
              name="project_id"
              defaultValue={template.project_id}
              className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
              required
            >
              {snapshot.projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-template-name`} className="text-xs font-medium text-muted-foreground">Template Name</label>
            <Input id={`${formId}-template-name`} name="name" defaultValue={template.name} required className="h-9 rounded-xl" />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-kind`} className="text-xs font-medium text-muted-foreground">Kind</label>
            <select id={`${formId}-kind`}
              name="kind"
              defaultValue={template.kind}
              className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
            >
              {["ansible", "opentofu", "shell", "terraform", "python", "powershell"].map((kind) => (
                <option key={kind} value={kind}>
                  {kind}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-runner-tags`} className="text-xs font-medium text-muted-foreground">Runner Tags</label>
            <Input id={`${formId}-runner-tags`} name="runner_tags" defaultValue={(template.runner_tags ?? []).join(", ")} className="h-9 rounded-xl" />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-command`} className="text-xs font-medium text-muted-foreground">Command</label>
            <Input id={`${formId}-command`} name="command" defaultValue={command} className="h-9 rounded-xl" />
          </div>
          <label className="flex items-center gap-2 text-sm py-2">
            <input name="requires_ack" type="checkbox" defaultChecked={template.requires_ack} className="rounded border-border" />
            <span>Requires approval before execution</span>
          </label>
          <Button type="submit" disabled={busy === `template:update:${template.id}`} className="h-9 w-full rounded-xl">
            Save template
          </Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function TemplatesView({
  snapshot,
  q,
  token,
  busy,
  mutate,
  loading,
  onRunRequested,
  canOperateProject,
}: {
  snapshot: ApiSnapshot;
  q?: string;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading?: boolean;
  onRunRequested: (runID: string) => void;
  canOperateProject?: (projectID: string) => boolean;
}): ReactNode {
  const writableProjects = snapshot.projects.filter((project) => canOperateProject?.(project.id));
  const canCreate = writableProjects.length > 0;
  const creationSnapshot = { ...snapshot, projects: writableProjects };
  const filtering = Boolean(q?.trim());
  const visibleTemplates = snapshot.templates.filter((template) => matchesQuery(q,
    template.id, template.name, template.project_id, projectName(snapshot.projects, template.project_id), template.kind, ...(template.runner_tags ?? [])));
  async function playTemplate(template: TaskTemplate): Promise<void> {
    const run = await mutate(`template:run:${template.id}`, () => requestRun(token, { template_id: template.id }), "Run requested");
    if (run) onRunRequested(run.id);
  }

  if (loading) {
    return (
      <section className={`grid items-start gap-4 ${canCreate ? "xl:grid-cols-[380px_minmax(0,1fr)]" : ""}`}>
        <SkeletonTable rows={4} />
        <SkeletonTable rows={5} />
      </section>
    );
  }

  return (
    <section className={`grid items-start gap-4 ${canCreate ? "xl:grid-cols-[380px_minmax(0,1fr)]" : ""}`}>
      {canCreate ? <>
      <div className="xl:hidden">
        <Dialog>
          <DialogTrigger asChild>
            <Button className="h-9 w-full rounded-xl">Create template</Button>
          </DialogTrigger>
          <DialogContent aria-modal="true" className="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden">
            <DialogHeader className="shrink-0 pr-6">
              <DialogTitle>Create template</DialogTitle>
            </DialogHeader>
            <div className="min-h-0 overflow-y-auto">
              <TemplateForm snapshot={creationSnapshot} token={token} busy={busy} mutate={mutate} />
            </div>
          </DialogContent>
        </Dialog>
      </div>
      <Card className="hidden xl:flex">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Create template</CardTitle>
        </CardHeader>
        <CardContent>
          <TemplateForm snapshot={creationSnapshot} token={token} busy={busy} mutate={mutate} />
        </CardContent>
      </Card>
      </> : null}
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">Template inventory</CardTitle>
          {filtering && <p className="text-sm text-muted-foreground" role="status">{visibleTemplates.length} of {snapshot.templates.length} templates match on this page.</p>}
        </CardHeader>
        <CardContent className="p-0">
          {filtering && visibleTemplates.length === 0 ? <p className="px-4 py-5 text-sm text-muted-foreground">No templates match this filter on the loaded page.</p> : snapshot.templates.length === 0 ? (
            <EmptyState title="No templates" icon={Layers3} />
          ) : (
            <>
              {/* Desktop Table */}
              <div className="hidden xl:block">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide">Name</TableHead>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide">Project</TableHead>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide">Kind</TableHead>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide">Tags</TableHead>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide">Approval</TableHead>
                      <TableHead className="text-xs font-semibold uppercase tracking-wide text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visibleTemplates.map((template) => (
                      <TableRow key={template.id} className="hover:bg-muted/40 transition-colors">
                        <TableCell className="font-medium text-sm">{template.name}</TableCell>
                        <TableCell className="text-sm">{projectName(snapshot.projects, template.project_id)}</TableCell>
                        <TableCell>
                          <Badge variant="outline" className="font-mono text-xs">
                            {template.kind}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          {(template.runner_tags ?? []).map((tag) => (
                            <Badge key={tag} variant="secondary" className="mr-1 text-xs">
                              {tag}
                            </Badge>
                          ))}
                        </TableCell>
                        <TableCell>
                          {template.requires_ack ? (
                            <Badge variant="warning" className="text-xs">Required</Badge>
                          ) : (
                            <Badge variant="success" className="text-xs">Open</Badge>
                          )}
                        </TableCell>
                        <TableCell className="text-right">
                          {canOperateProject?.(template.project_id) ? <div className="flex items-center justify-end gap-2">
                            <Button
                              size="sm"
                              className="h-8 rounded-xl"
                              disabled={busy === `template:run:${template.id}`}
                              onClick={() => void playTemplate(template)}
                            >
                              <Play className="mr-1.5 h-4 w-4" />
                              Play
                            </Button>
                            <TemplateEditDialog template={template} snapshot={snapshot} token={token} busy={busy} mutate={mutate} />
                          </div> : <span className="text-xs text-muted-foreground">Read only</span>}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>

              {/* Mobile Card List */}
              <div className="xl:hidden divide-y divide-border/60">
                {visibleTemplates.map((template) => (
                  <div key={template.id} className="p-4 space-y-2">
                    <div className="flex items-center justify-between gap-2">
                      <div className="font-medium text-sm">{template.name}</div>
                      <Badge variant="outline" className="font-mono text-xs shrink-0">
                        {template.kind}
                      </Badge>
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {projectName(snapshot.projects, template.project_id)}
                    </div>
                    {(template.runner_tags ?? []).length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {(template.runner_tags ?? []).map((tag) => (
                          <Badge key={tag} variant="secondary" className="text-xs">
                            {tag}
                          </Badge>
                        ))}
                      </div>
                    )}
                    <div className="flex items-center justify-between pt-1">
                      <div>
                        {template.requires_ack ? (
                          <Badge variant="warning" className="text-xs">Required</Badge>
                        ) : (
                          <Badge variant="success" className="text-xs">Open</Badge>
                        )}
                      </div>
                      {canOperateProject?.(template.project_id) ? <div className="flex gap-2">
                        <Button
                          size="sm"
                          className="h-8 w-8 rounded-xl"
                          disabled={busy === `template:run:${template.id}`}
                          onClick={() => void playTemplate(template)}
                          aria-label="Play"
                        >
                          <Play className="h-4 w-4" />
                        </Button>
                        <TemplateEditDialog
                          trigger={
                            <Button variant="outline" size="sm" className="h-8 w-8 p-0 rounded-xl" aria-label="Edit">
                              <Pencil className="h-4 w-4" />
                            </Button>
                          }
                          template={template}
                          snapshot={snapshot}
                          token={token}
                          busy={busy}
                          mutate={mutate}
                        />
                      </div> : null}
                    </div>
                  </div>
                ))}
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
