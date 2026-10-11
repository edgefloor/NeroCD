import { FormEvent, ReactNode, useId, useState } from "react";
import { Layers3, Pencil, Play, Plus } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { formatRunnerTags, parseRunnerTags } from "@/lib/runnerTags";
import { formatCommandArguments, parseCommandArguments } from "@/lib/commandArguments";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot, TaskTemplate } from "@/api";
import type { ActionError, TemplateActions } from "@/api/mutations";
import { ActionErrorMessage } from "@/components/common/ActionErrorMessage";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { matchesQuery, projectName } from "@/lib/format";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

const templateKinds = ["ansible", "opentofu", "shell", "terraform", "python", "powershell"];

function TemplateForm({
  snapshot,
  busy,
  actions,
  actionError,
  recovery,
}: {
  snapshot: ApiSnapshot;
  busy: string;
  actions: TemplateActions;
  actionError?: ActionError;
  recovery?: ReactNode;
}): ReactNode {
  const formId = useId();
  const [commandError, setCommandError] = useState("");
  const [tagsError, setTagsError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (recovery || busy) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const kind = String(form.get("kind") ?? "shell");
    let runnerTags: string[];
    try {
      runnerTags = parseRunnerTags(String(form.get("runner_tags") ?? ""));
      setTagsError("");
    } catch (error) {
      setTagsError((error as Error).message);
      event.currentTarget.querySelector<HTMLInputElement>('[name="runner_tags"]')?.focus();
      return;
    }
    let command: string[];
    try {
      command = parseCommandArguments(String(form.get("command") ?? ""));
      if (command.length === 0) throw new Error("Enter the command this template should run.");
      setCommandError("");
    } catch (error) {
      setCommandError((error as Error).message);
      event.currentTarget.querySelector<HTMLTextAreaElement>('[name="command"]')?.focus();
      return;
    }
    const created = await actions.create({
      project_id: String(form.get("project_id") ?? ""),
      name: String(form.get("name") ?? ""),
      kind,
      run_spec: { type: kind, inputs: {}, process: { command } },
      runner_tags: runnerTags,
      requires_ack: form.has("requires_ack"),
    });
    if (created !== undefined) formElement.reset();
  }

  return (
    <form aria-busy={Boolean(busy)} className="space-y-4" onSubmit={(event) => void submit(event)}>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-project`} className="text-xs font-medium text-muted-foreground">Project</label>
        <select disabled={Boolean(busy)} id={`${formId}-project`}
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
        <Input disabled={Boolean(busy)} id={`${formId}-template-name`} name="name" placeholder="Patch Linux fleet" required className="h-9 rounded-xl" />
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-kind`} className="text-xs font-medium text-muted-foreground">Kind</label>
        <select disabled={Boolean(busy)} id={`${formId}-kind`}
          name="kind"
          defaultValue="shell"
          className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
        >
          {templateKinds.map((kind) => (
            <option key={kind} value={kind}>
              {kind}
            </option>
          ))}
        </select>
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-runner-tags`} className="text-xs font-medium text-muted-foreground">Runner Tags</label>
        <Input disabled={Boolean(busy)} id={`${formId}-runner-tags`} name="runner_tags" placeholder="linux, prod" className="h-9 rounded-xl" aria-invalid={Boolean(tagsError)} aria-describedby={`${formId}-tags-help${tagsError ? ` ${formId}-tags-error` : ""}`} onChange={() => setTagsError("")} />
        <p id={`${formId}-tags-help`} className="text-xs text-muted-foreground">Separate tags with commas, or use a JSON array for tags containing commas.</p>
        {tagsError ? <p id={`${formId}-tags-error`} role="alert" className="text-sm text-destructive">{tagsError}</p> : null}
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${formId}-command`} className="text-xs font-medium text-muted-foreground">Command</label>
        <Textarea disabled={Boolean(busy)} id={`${formId}-command`} name="command" placeholder="ansible-playbook site.yml" className="min-h-20 rounded-xl" aria-invalid={Boolean(commandError)} aria-describedby={`${formId}-command-help${commandError ? ` ${formId}-command-error` : ""}`} onChange={() => setCommandError("")} />
        <p id={`${formId}-command-help`} className="text-xs text-muted-foreground">Quote arguments containing spaces. Variables and shell operators are passed literally.</p>
        {commandError ? <p id={`${formId}-command-error`} role="alert" className="text-sm text-destructive">{commandError}</p> : null}
      </div>
      <label className="flex items-center gap-2 text-sm py-2">
        <input disabled={Boolean(busy)} name="requires_ack" type="checkbox" className="rounded border-border" />
        <span>Requires approval before execution</span>
      </label>
      <Button type="submit" disabled={Boolean(recovery) || Boolean(busy)} className="h-9 w-full rounded-xl">
        <Plus className="h-4 w-4 mr-2" />
        Create template
      </Button>
      <ActionErrorMessage error={actionError} actionKey="template" label="Unable to create template" />
    </form>
  );
}

function TemplateEditDialog({
  trigger,
  template,
  snapshot,
  busy,
  actions,
  actionError,
  recovery,
}: {
  trigger?: ReactNode;
  template: TaskTemplate;
  snapshot: ApiSnapshot;
  busy: string;
  actions: TemplateActions;
  actionError?: ActionError;
  recovery?: ReactNode;
}): ReactNode {
  const formId = useId();
  const [commandError, setCommandError] = useState("");
  const [tagsError, setTagsError] = useState("");
  const [open, setOpen] = useState(false);
  const saving = busy === `template:update:${template.id}`;
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (recovery || busy) return;
    const form = new FormData(event.currentTarget);
    const kind = String(form.get("kind") ?? template.kind);
    let runnerTags: string[];
    try {
      runnerTags = parseRunnerTags(String(form.get("runner_tags") ?? ""));
      setTagsError("");
    } catch (error) {
      setTagsError((error as Error).message);
      event.currentTarget.querySelector<HTMLInputElement>('[name="runner_tags"]')?.focus();
      return;
    }
    let command: string[];
    try {
      const value = String(form.get("command") ?? "");
      const original = template.run_spec.process?.command ?? [];
      command = value === formatCommandArguments(original).replace(/\r\n?/g, "\n") ? original : parseCommandArguments(value);
      if (template.run_spec.process && command.length === 0) throw new Error("Enter a command. This template requires a process command.");
      setCommandError("");
    } catch (error) {
      setCommandError((error as Error).message);
      event.currentTarget.querySelector<HTMLTextAreaElement>('[name="command"]')?.focus();
      return;
    }
    const updated = await actions.update({
      id: template.id,
      project_id: template.project_id,
      name: String(form.get("name") ?? template.name),
      kind,
      run_spec: {
        ...template.run_spec,
        type: kind === template.kind ? template.run_spec.type : kind,
        process: command.length ? { ...template.run_spec.process, command } : undefined,
      },
      workflow: template.workflow,
      runner_tags: runnerTags,
      requires_ack: form.has("requires_ack"),
    });
    if (updated !== undefined) setOpen(false);
  }
  const command = formatCommandArguments(template.run_spec.process?.command ?? []);

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!saving) { setCommandError(""); setTagsError(""); setOpen(nextOpen); } }}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm" className="h-8 rounded-xl">
            <Pencil className="mr-1.5 h-4 w-4" />
            Edit
          </Button>
        )}
      </DialogTrigger>
      <DialogContent showCloseButton={!saving} className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Edit template</DialogTitle>
        </DialogHeader>
        {recovery}
        <form aria-busy={Boolean(busy)} className="space-y-4" onSubmit={(event) => void submit(event)}>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-project`} className="text-xs font-medium text-muted-foreground">Project</label>
            <Input disabled={Boolean(busy)} id={`${formId}-project`} value={projectName(snapshot.projects, template.project_id)} readOnly aria-describedby={`${formId}-project-help`} className="h-9 rounded-xl" />
            <p id={`${formId}-project-help`} className="text-xs text-muted-foreground">The project cannot be changed after creation.</p>
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-template-name`} className="text-xs font-medium text-muted-foreground">Template Name</label>
            <Input disabled={Boolean(busy)} id={`${formId}-template-name`} name="name" defaultValue={template.name} required className="h-9 rounded-xl" />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-kind`} className="text-xs font-medium text-muted-foreground">Kind</label>
            <select disabled={Boolean(busy)} id={`${formId}-kind`}
              name="kind"
              defaultValue={template.kind}
              className="h-9 w-full rounded-xl border border-border bg-card px-2.5 text-sm outline-none focus:border-ring focus:ring-1 focus:ring-ring"
            >
              {(templateKinds.includes(template.kind) ? templateKinds : [template.kind, ...templateKinds]).map((kind) => (
                <option key={kind} value={kind}>
                  {kind}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-runner-tags`} className="text-xs font-medium text-muted-foreground">Runner Tags</label>
            <Input disabled={Boolean(busy)} id={`${formId}-runner-tags`} name="runner_tags" defaultValue={formatRunnerTags(template.runner_tags ?? [])} className="h-9 rounded-xl" aria-invalid={Boolean(tagsError)} aria-describedby={`${formId}-tags-help${tagsError ? ` ${formId}-tags-error` : ""}`} onChange={() => setTagsError("")} />
            <p id={`${formId}-tags-help`} className="text-xs text-muted-foreground">Separate tags with commas, or use a JSON array for tags containing commas.</p>
            {tagsError ? <p id={`${formId}-tags-error`} role="alert" className="text-sm text-destructive">{tagsError}</p> : null}
          </div>
          <div className="space-y-1.5">
            <label htmlFor={`${formId}-command`} className="text-xs font-medium text-muted-foreground">Command</label>
            <Textarea disabled={Boolean(busy)} id={`${formId}-command`} name="command" defaultValue={command} className="min-h-20 rounded-xl" aria-invalid={Boolean(commandError)} aria-describedby={`${formId}-command-help${commandError ? ` ${formId}-command-error` : ""}`} onChange={() => setCommandError("")} />
            <p id={`${formId}-command-help`} className="text-xs text-muted-foreground">Quote arguments containing spaces. Variables and shell operators are passed literally.</p>
            {commandError ? <p id={`${formId}-command-error`} role="alert" className="text-sm text-destructive">{commandError}</p> : null}
          </div>
          <label className="flex items-center gap-2 text-sm py-2">
            <input disabled={Boolean(busy)} name="requires_ack" type="checkbox" defaultChecked={template.requires_ack} className="rounded border-border" />
            <span>Requires approval before execution</span>
          </label>
          <Button type="submit" disabled={Boolean(recovery) || Boolean(busy)} className="h-9 w-full rounded-xl">
            {saving ? "Saving template..." : "Save template"}
          </Button>
          <ActionErrorMessage error={actionError} actionKey={`template:update:${template.id}`} label="Unable to save template" />
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function TemplatesView({
  snapshot,
  q,
  busy,
  actions,
  actionError,
  recovery,
  loading,
  onRunRequested,
  canOperateProject,
}: {
  snapshot: ApiSnapshot;
  q?: string;
  busy: string;
  actions: TemplateActions;
  actionError?: ActionError;
  recovery?: ReactNode;
  loading?: boolean;
  onRunRequested: (runID: string) => void;
  canOperateProject?: (projectID: string) => boolean;
}): ReactNode {
  const [createOpen, setCreateOpen] = useState(false);
  const creating = busy === "template";
  const writableProjects = snapshot.projects.filter((project) => canOperateProject?.(project.id));
  const canCreate = writableProjects.length > 0;
  const creationSnapshot = { ...snapshot, projects: writableProjects };
  const filtering = Boolean(q?.trim());
  const visibleTemplates = snapshot.templates.filter((template) => matchesQuery(q,
    template.id, template.name, template.project_id, projectName(snapshot.projects, template.project_id), template.kind, ...(template.runner_tags ?? [])));
  async function playTemplate(template: TaskTemplate): Promise<void> {
    if (recovery || busy) return;
    const run = await actions.run({ template_id: template.id });
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
        <Dialog open={createOpen} onOpenChange={(open) => { if (!creating) setCreateOpen(open); }}>
          <DialogTrigger asChild>
            <Button className="h-9 w-full rounded-xl">Create template</Button>
          </DialogTrigger>
          <DialogContent showCloseButton={!creating} aria-modal="true" className="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden">
            <DialogHeader className="shrink-0 pr-6">
              <DialogTitle>Create template</DialogTitle>
            </DialogHeader>
            <div className="min-h-0 space-y-4 overflow-y-auto">
              {recovery}
              <TemplateForm snapshot={creationSnapshot} busy={busy} actions={actions} recovery={recovery} actionError={actionError} />
            </div>
          </DialogContent>
        </Dialog>
      </div>
      <Card className="hidden xl:flex">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Create template</CardTitle>
        </CardHeader>
        <CardContent>
          <TemplateForm snapshot={creationSnapshot} busy={busy} actions={actions} recovery={recovery} actionError={actionError} />
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
                <Table className="table-fixed">
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-[20%] text-xs font-semibold uppercase tracking-wide">Name</TableHead>
                      <TableHead className="w-[18%] text-xs font-semibold uppercase tracking-wide">Project</TableHead>
                      <TableHead className="w-[12%] text-xs font-semibold uppercase tracking-wide">Kind</TableHead>
                      <TableHead className="w-[14%] text-xs font-semibold uppercase tracking-wide">Tags</TableHead>
                      <TableHead className="w-[12%] text-xs font-semibold uppercase tracking-wide">Approval</TableHead>
                      <TableHead className="w-[24%] text-xs font-semibold uppercase tracking-wide text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visibleTemplates.map((template) => (
                      <TableRow key={template.id} className="hover:bg-muted/40 transition-colors [&>td]:align-top">
                        <TableCell className="whitespace-normal break-words font-medium text-sm [overflow-wrap:anywhere]">{template.name}</TableCell>
                        <TableCell className="whitespace-normal break-words text-sm [overflow-wrap:anywhere]">{projectName(snapshot.projects, template.project_id)}</TableCell>
                        <TableCell>
                          <Badge variant="outline" className="h-auto max-w-full whitespace-normal break-all font-mono text-xs">
                            {template.kind}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          {(template.runner_tags ?? []).map((tag) => (
                            <Badge key={tag} variant="secondary" className="mr-1 h-auto max-w-full whitespace-normal break-all text-xs">
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
                          {canOperateProject?.(template.project_id) ? <div className="flex flex-wrap items-center justify-end gap-2">
                            <Button
                              size="sm"
                              className="h-8 rounded-xl"
                              disabled={Boolean(recovery) || Boolean(busy)}
                              onClick={() => void playTemplate(template)}
                            >
                              <Play className="mr-1.5 h-4 w-4" />
                              Play
                            </Button>
                            <TemplateEditDialog template={template} snapshot={snapshot} busy={busy} actions={actions} recovery={recovery} actionError={actionError} />
                          </div> : <span className="text-xs text-muted-foreground">Read only</span>}
                          <ActionErrorMessage error={actionError} actionKey={`template:run:${template.id}`} label="Unable to request run" />
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
                    <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0 flex-1 break-words font-medium text-sm [overflow-wrap:anywhere]">{template.name}</div>
                      <Badge variant="outline" className="h-auto max-w-full whitespace-normal break-all font-mono text-xs">
                        {template.kind}
                      </Badge>
                    </div>
                    <div className="break-words text-xs text-muted-foreground [overflow-wrap:anywhere]">
                      {projectName(snapshot.projects, template.project_id)}
                    </div>
                    {(template.runner_tags ?? []).length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {(template.runner_tags ?? []).map((tag) => (
                          <Badge key={tag} variant="secondary" className="h-auto max-w-full whitespace-normal break-all text-xs">
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
                          disabled={Boolean(recovery) || Boolean(busy)}
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

                          busy={busy}
                          actions={actions}
                          recovery={recovery} actionError={actionError}
                        />
                      </div> : null}
                    </div>
                    <ActionErrorMessage error={actionError} actionKey={`template:run:${template.id}`} label="Unable to request run" />
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
