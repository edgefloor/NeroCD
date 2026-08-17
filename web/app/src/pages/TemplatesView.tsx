import { FormEvent, ReactNode, useState } from "react";
import { Layers3, Pencil, Play, Plus } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import type { ApiSnapshot, TaskRun, TaskTemplate } from "@/api";
import { createTemplate, requestRun, updateTemplate } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { projectName } from "@/lib/format";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

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
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const kind = String(form.get("kind") ?? "shell");
    const command = String(form.get("command") ?? "").trim().split(/\s+/).filter(Boolean);
    await mutate("template", () =>
      createTemplate(token, {
        project_id: String(form.get("project_id") ?? ""),
        name: String(form.get("name") ?? ""),
        kind,
        run_spec: { type: kind, inputs: {}, process: command.length ? { command } : undefined },
        runner_tags: String(form.get("runner_tags") ?? "").split(",").map((tag) => tag.trim()).filter(Boolean),
        requires_ack: form.has("requires_ack"),
      }), "Template created");
    event.currentTarget.reset();
  }

  return (
    <form className="space-y-4" onSubmit={(event) => void submit(event)}>
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
        <label className="text-xs font-medium text-muted-foreground">Template Name</label>
        <Input name="name" placeholder="Patch Linux fleet" required className="h-9" />
      </div>
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">Kind</label>
        <Select name="kind">
          {["ansible", "opentofu", "shell", "terraform", "python", "powershell"].map((kind) => (
            <option key={kind} value={kind}>
              {kind}
            </option>
          ))}
        </Select>
      </div>
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">Runner Tags</label>
        <Input name="runner_tags" placeholder="linux, prod" className="h-9" />
      </div>
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">Command</label>
        <Input name="command" placeholder="ansible-playbook site.yml" className="h-9" />
      </div>
      <label className="flex items-center gap-2 text-sm py-2">
        <input name="requires_ack" type="checkbox" className="rounded border-border" />
        <span>Requires approval before execution</span>
      </label>
      <Button type="submit" disabled={busy === "template"} className="h-9 w-full">
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
  const [open, setOpen] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const kind = String(form.get("kind") ?? template.kind);
    const command = String(form.get("command") ?? "").trim().split(/\s+/).filter(Boolean);
    await mutate(
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
    setOpen(false);
  }
  const command = template.run_spec.process?.command?.join(" ") ?? "";

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm" className="h-8">
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
            <label className="text-xs font-medium text-muted-foreground">Project</label>
            <Select name="project_id" defaultValue={template.project_id} required>
              {snapshot.projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Template Name</label>
            <Input name="name" defaultValue={template.name} required className="h-9" />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Kind</label>
            <Select name="kind" defaultValue={template.kind}>
              {["ansible", "opentofu", "shell", "terraform", "python", "powershell"].map((kind) => (
                <option key={kind} value={kind}>
                  {kind}
                </option>
              ))}
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Runner Tags</label>
            <Input name="runner_tags" defaultValue={template.runner_tags.join(", ")} className="h-9" />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Command</label>
            <Input name="command" defaultValue={command} className="h-9" />
          </div>
          <label className="flex items-center gap-2 text-sm py-2">
            <input name="requires_ack" type="checkbox" defaultChecked={template.requires_ack} className="rounded border-border" />
            <span>Requires approval before execution</span>
          </label>
          <Button type="submit" disabled={busy === `template:update:${template.id}`} className="h-9 w-full">
            Save template
          </Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function TemplatesView({
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
  const [terminalRunID, setTerminalRunID] = useState("");
  const [terminalRunFallback, setTerminalRunFallback] = useState<TaskRun | undefined>();
  const [terminalOpen, setTerminalOpen] = useState(false);
  const terminalRun = snapshot.runs.find((run) => run.id === terminalRunID) ?? (terminalRunFallback?.id === terminalRunID ? terminalRunFallback : undefined);
  const terminalLogs = snapshot.logs.filter((log) => log.run_id === terminalRunID);

  async function playTemplate(template: TaskTemplate): Promise<void> {
    const run = await mutate(`template:run:${template.id}`, () => requestRun(token, { template_id: template.id }), "Run requested");
    if (run) {
      setTerminalRunID(run.id);
      setTerminalRunFallback(run);
      setTerminalOpen(true);
    }
  }

  if (loading) {
    return (
      <section className="grid gap-4 xl:grid-cols-[380px_minmax(0,1fr)]">
        <SkeletonTable rows={4} />
        <SkeletonTable rows={5} />
      </section>
    );
  }

  return (
    <section className="grid gap-4 xl:grid-cols-[380px_minmax(0,1fr)]">
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold">Create template</CardTitle>
        </CardHeader>
        <CardContent>
          <TemplateForm snapshot={snapshot} token={token} busy={busy} mutate={mutate} />
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">Template inventory</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {snapshot.templates.length === 0 ? (
            <EmptyState title="No templates" icon={Layers3} />
          ) : (
            <>
              {/* Desktop Table */}
              <div className="hidden xl:block">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="text-xs font-medium">Name</TableHead>
                      <TableHead className="text-xs font-medium">Project</TableHead>
                      <TableHead className="text-xs font-medium">Kind</TableHead>
                      <TableHead className="text-xs font-medium">Tags</TableHead>
                      <TableHead className="text-xs font-medium">Approval</TableHead>
                      <TableHead className="text-xs font-medium text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {snapshot.templates.map((template) => (
                      <TableRow key={template.id} className="hover:bg-muted/50 transition-colors">
                        <TableCell className="font-medium text-sm">{template.name}</TableCell>
                        <TableCell className="text-sm">{projectName(snapshot.projects, template.project_id)}</TableCell>
                        <TableCell>
                          <Badge variant="outline" className="font-mono text-xs">
                            {template.kind}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          {template.runner_tags.map((tag) => (
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
                          <div className="flex items-center justify-end gap-2">
                            <Button
                              size="sm"
                              className="h-8"
                              disabled={busy === `template:run:${template.id}`}
                              onClick={() => void playTemplate(template)}
                            >
                              <Play className="mr-1.5 h-4 w-4" />
                              Play
                            </Button>
                            <TemplateEditDialog template={template} snapshot={snapshot} token={token} busy={busy} mutate={mutate} />
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>

              {/* Mobile Card List */}
              <div className="xl:hidden divide-y divide-border">
                {snapshot.templates.map((template) => (
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
                    {template.runner_tags.length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {template.runner_tags.map((tag) => (
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
                      <div className="flex gap-2">
                        <Button
                          size="sm"
                          className="h-8 w-8"
                          disabled={busy === `template:run:${template.id}`}
                          onClick={() => void playTemplate(template)}
                          aria-label="Play"
                        >
                          <Play className="h-4 w-4" />
                        </Button>
                        <TemplateEditDialog
                          trigger={
                            <Button variant="outline" size="sm" className="h-8 w-8 p-0">
                              <Pencil className="h-4 w-4" />
                            </Button>
                          }
                          template={template}
                          snapshot={snapshot}
                          token={token}
                          busy={busy}
                          mutate={mutate}
                        />
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </>
          )}
        </CardContent>
      </Card>
      <TerminalLogDialog run={terminalRun} logs={terminalLogs} open={terminalOpen} onOpenChange={setTerminalOpen} />
    </section>
  );
}
