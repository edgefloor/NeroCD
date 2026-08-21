import type { Project, RunLog, TaskRun, TaskTemplate } from "./api";

export type OverviewSummary = {
  projectCount: number;
  templateCount: number;
  approvalTemplateCount: number;
  liveRunCount: number;
  logCount: number;
};

export function summarizeOverview(projects: Project[], templates: TaskTemplate[], runs: TaskRun[], logs: RunLog[]): OverviewSummary {
  return {
    projectCount: projects.length,
    templateCount: templates.length,
    approvalTemplateCount: templates.filter((template) => template.requires_ack).length,
    liveRunCount: runs.filter((run) => !run.finished_at).length,
    logCount: logs.length,
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
