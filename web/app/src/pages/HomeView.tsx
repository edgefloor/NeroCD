import { type ReactNode } from "react";
import { Activity, FolderKanban, Layers3, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { Approval, HealthResponse, Project, RunLog, TaskRun, TaskTemplate } from "@/api";
import { MetricCard } from "@/components/common/MetricCard";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { RunsTable } from "./RunsView";
import { ApprovalList } from "./ApprovalsView";
import { LogViewer } from "./LogsView";
import { summarizeOverview } from "@/view-model";
import { matchesQuery } from "@/lib/format";

export function HomeView({ health, projects, templates, runs, approvals, logs, q, loading, approvingRunID, rejectingRunID, onApprove, onReject }: { health: HealthResponse; projects: Project[]; templates: TaskTemplate[]; runs: TaskRun[]; approvals: Approval[]; logs: RunLog[]; q?: string; loading?: boolean; approvingRunID?: string; rejectingRunID?: string; onApprove: (runID: string) => void; onReject: (runID: string) => void }): ReactNode {
  if (loading) return <SkeletonCard rows={6} />;
  const summary = summarizeOverview(projects, templates, runs, logs);
  const pending = approvals.filter((approval) => approval.status === "pending");
  const visibleRuns = runs.filter((run) => matchesQuery(q, run.id, run.status, run.project_id, run.template_id));
  const visibleApprovals = pending.filter((approval) => matchesQuery(q, approval.run_id, approval.status));
  const visibleLogs = logs.filter((log) => matchesQuery(q, log.run_id, log.message, log.stream));
  return <div className="grid gap-6"><Card><CardContent className="flex justify-between p-5"><div><h2 className="text-xl font-semibold">{health.status === "ok" ? "All Systems Green" : "Needs Attention"}</h2><p className="text-sm text-muted-foreground">{pending.length} pending approvals · {summary.liveRunCount} active runs</p></div></CardContent></Card><section className="grid grid-cols-2 gap-3 xl:grid-cols-4"><MetricCard label="Projects" value={summary.projectCount} caption="active domains" icon={FolderKanban} /><MetricCard label="Templates" value={summary.templateCount} caption="available tasks" icon={Layers3} /><MetricCard label="Live Runs" value={summary.liveRunCount} caption="currently executing" icon={Activity} /><MetricCard label="Run Logs" value={summary.logCount} caption="recent events" icon={Terminal} /></section><section className="grid gap-6 xl:grid-cols-2"><Card><CardHeader><CardTitle>Recent Activity</CardTitle></CardHeader><CardContent className="p-0"><RunsTable runs={visibleRuns.slice(0, 7)} projects={projects} templates={templates} /></CardContent></Card><Card><CardHeader><CardTitle>Approval Inbox</CardTitle></CardHeader><CardContent className="p-0"><ApprovalList approvals={visibleApprovals} runs={runs} projects={projects} templates={templates} approvingRunID={approvingRunID} rejectingRunID={rejectingRunID} onApprove={onApprove} onReject={onReject} /></CardContent></Card></section><Card><CardHeader><CardTitle>Latest Logs</CardTitle></CardHeader><CardContent className="p-0"><LogViewer logs={visibleLogs.slice(0, 5)} /></CardContent></Card></div>;
}
