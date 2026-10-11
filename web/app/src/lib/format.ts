import type { ApiSnapshot, RunLog, Project, TaskRun, TaskTemplate } from "@/api";

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
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "Unavailable";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}

export function formatDuration(run: TaskRun): string {
  const start = new Date(run.started_at).getTime();
  const end = run.finished_at ? new Date(run.finished_at).getTime() : Date.now();
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return "Unavailable";
  const seconds = Math.floor((end - start) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? `${minutes}m ${seconds % 60}s` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function matchesQuery(query: string | undefined, ...values: Array<string | undefined>): boolean {
  if (!query) return true;
  const needle = query.trim().toLocaleLowerCase();
  return values.some((value) => value?.toLocaleLowerCase().includes(needle));
}

export function countFilteredItems(snapshot: ApiSnapshot): number {
  return snapshot.projects.length + snapshot.templates.length + snapshot.runs.length + snapshot.approvals.length + snapshot.logs.length + snapshot.repositories.length;
}


export function matchesRunQuery(query: string | undefined, run: TaskRun, projects: Project[], templates: TaskTemplate[]): boolean {
  return matchesQuery(query, run.id, run.status, run.requested_by, run.run_spec.type, ...(run.runner_tags ?? []),
    projectName(projects, run.project_id), templateName(templates, run.template_id, run));
}


export function matchesLogQuery(query: string | undefined, log: RunLog): boolean {
  return matchesQuery(query, log.run_id, log.stream, log.message, `#${log.sequence}`);
}
