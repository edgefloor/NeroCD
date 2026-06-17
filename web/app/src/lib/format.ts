import type { Project, TaskRun, TaskTemplate } from "@/api";

export function projectName(projects: Project[], projectID: string): string {
  return projects.find((project) => project.id === projectID)?.name ?? projectID;
}

export function templateName(templates: TaskTemplate[], templateID: string | undefined, run: TaskRun): string {
  if (!templateID) {
    return `${run.run_spec.type} ad-hoc run`;
  }
  return templates.find((template) => template.id === templateID)?.name ?? templateID;
}

export function formatDate(value: string | undefined): string {
  if (!value) {
    return "Pending";
  }
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

export function formatDuration(run: TaskRun): string {
  const start = new Date(run.started_at).getTime();
  const end = run.finished_at ? new Date(run.finished_at).getTime() : Date.now();
  const minutes = Math.max(1, Math.round((end - start) / 60000));
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function countFilteredItems(snapshot: {
  projects: unknown[];
  projectMembers: unknown[];
  templates: unknown[];
  runs: unknown[];
  approvals: unknown[];
  logs: unknown[];
  repositories: unknown[];
  auditEvents: unknown[];
  capabilities: unknown[];
}): number {
  return (
    snapshot.projects.length +
    snapshot.projectMembers.length +
    snapshot.templates.length +
    snapshot.runs.length +
    snapshot.approvals.length +
    snapshot.logs.length +
    snapshot.repositories.length +
    snapshot.auditEvents.length +
    snapshot.capabilities.length
  );
}

export function filterSnapshot<T extends {
  projects: Project[];
  projectMembers: { id: string; project_id: string; user_id: string; email: string; name: string; role: string }[];
  templates: TaskTemplate[];
  runs: TaskRun[];
  approvals: { id: string; run_id: string; status: string; created_at: string }[];
  logs: { id: string; run_id: string; stream: string; message: string }[];
  repositories: { id: string; name: string; url: string; provider: string; project_id: string }[];
  auditEvents: { id: string; action: string; target_id: string; metadata: Record<string, unknown> }[];
  capabilities: { name: string; status: string; description: string }[];
}>(snapshot: T, query: string): T {
  const normalized = query.trim().toLowerCase();
  if (!normalized) {
    return snapshot;
  }
  const includes = (...values: Array<unknown>): boolean =>
    values.some((value) => String(value ?? "").toLowerCase().includes(normalized));
  const matchedProjects = snapshot.projects.filter((project) => includes(project.id, project.name, project.description));
  const matchedTemplates = snapshot.templates.filter((template) =>
    includes(template.id, template.name, template.kind, template.runner_tags.join(" "), projectName(snapshot.projects, template.project_id)),
  );
  const runs = snapshot.runs.filter((run) =>
    includes(
      run.id,
      run.status,
      run.run_spec.type,
      run.runner_tags.join(" "),
      projectName(snapshot.projects, run.project_id),
      templateName(snapshot.templates, run.template_id, run),
    ),
  );
  const relatedProjectIds = new Set([
    ...matchedProjects.map((project) => project.id),
    ...matchedTemplates.map((template) => template.project_id),
    ...runs.map((run) => run.project_id),
  ]);
  const relatedTemplateIds = new Set([
    ...matchedTemplates.map((template) => template.id),
    ...runs.map((run) => run.template_id).filter((templateID): templateID is string => Boolean(templateID)),
  ]);
  const projects = snapshot.projects.filter((project) => relatedProjectIds.has(project.id));
  const templates = snapshot.templates.filter((template) => relatedTemplateIds.has(template.id));
  const approvals = snapshot.approvals.filter((approval) => includes(approval.id, approval.run_id, approval.status, approval.created_at));
  const logs = snapshot.logs.filter((log) => includes(log.id, log.run_id, log.stream, log.message));
  const repositories = snapshot.repositories.filter((repo) => includes(repo.id, repo.name, repo.url, repo.provider, projectName(snapshot.projects, repo.project_id)));
  const projectMembers = snapshot.projectMembers.filter((member) =>
    includes(member.id, member.project_id, member.user_id, member.email, member.name, member.role, projectName(snapshot.projects, member.project_id)),
  );
  const auditEvents = snapshot.auditEvents.filter((event) => includes(event.id, event.action, event.target_id, JSON.stringify(event.metadata)));
  const capabilities = snapshot.capabilities.filter((capability) => includes(capability.name, capability.status, capability.description));
  return { ...snapshot, projects, projectMembers, templates, runs, approvals, logs, repositories, auditEvents, capabilities };
}
