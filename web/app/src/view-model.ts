import type { ApiSnapshot, Project, RunLog, TaskRun, TaskTemplate } from "./api";

export type OverviewSummary = {
  projectCount: number;
  templateCount: number;
  approvalTemplateCount: number;
  liveRunCount: number;
  logCount: number;
};

export function summarizeOverview(snapshot: ApiSnapshot): OverviewSummary;
export function summarizeOverview(projects: Project[], templates: TaskTemplate[], runs: TaskRun[], logs: RunLog[]): OverviewSummary;
export function summarizeOverview(
  projectsOrSnapshot: Project[] | ApiSnapshot,
  templates?: TaskTemplate[],
  runs?: TaskRun[],
  logs?: RunLog[],
): OverviewSummary {
  const snapshot = Array.isArray(projectsOrSnapshot)
    ? { projects: projectsOrSnapshot, templates: templates ?? [], runs: runs ?? [], logs: logs ?? [] }
    : projectsOrSnapshot;
  return {
    projectCount: snapshot.projects.length,
    templateCount: snapshot.templates.length,
    approvalTemplateCount: snapshot.templates.filter((template) => template.requires_ack).length,
    liveRunCount: snapshot.runs.filter((run) => !run.finished_at).length,
    logCount: snapshot.logs.length,
  };
}

export function statusTone(status: string): "good" | "pending" | "danger" | "neutral" {
  if (["ok", "active", "succeeded", "scaffolded"].includes(status)) {
    return "good";
  }
  if (["waiting_approval", "planned", "queued", "running", "pending"].includes(status)) {
    return "pending";
  }
  if (["failed", "error", "canceled", "revoked", "archived"].includes(status)) {
    return "danger";
  }
  return "neutral";
}
