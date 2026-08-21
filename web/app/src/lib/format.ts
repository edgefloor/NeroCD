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

export function matchesQuery(query: string | undefined, ...values: Array<string | undefined>): boolean {
  if (!query) return true;
  const needle = query.trim().toLocaleLowerCase();
  return values.some((value) => value?.toLocaleLowerCase().includes(needle));
}
