import { ReactNode } from "react";
import { Activity, AlertCircle, Clock } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { ActionError, ApiSnapshot, TaskRun, ApprovalDecision } from "@/api";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Skeleton } from "@/components/ui/skeleton";
import { SearchScope } from "@/components/common/SearchScope";
import { RunsTable } from "./RunsView";
import { ApprovalList } from "./ApprovalsView";
import { LogViewer } from "./LogsView";
import { summarizeOverview } from "@/view-model";
import { matchesLogQuery, matchesQuery, matchesRunQuery } from "@/lib/format";
import { cn } from "@/lib/utils";

function CompactEmpty({ children }: { children: ReactNode }): ReactNode {
  return <p className="px-4 py-5 text-sm text-muted-foreground">{children}</p>;
}

export function HomeView({
  snapshot,
  workSnapshot,
  query,
  onClearQuery,
  busy,
  onDecide,
  decisionError,
  canOperateProject,
  loading,
  totals,
}: {
  snapshot: ApiSnapshot;
  workSnapshot?: ApiSnapshot;
  query?: string;
  onClearQuery?: () => void;
  busy: boolean;
  onDecide: (runID: string, decision: ApprovalDecision) => void;
  decisionError?: ActionError;
  canOperateProject?: (projectID: string) => boolean;
  loading?: boolean;
  totals?: { projects: number; templates: number; logs: number };
}): ReactNode {
  const summary = summarizeOverview(snapshot);
  const pending = snapshot.approvals.filter((approval) => approval.status === "pending");
  const failed = snapshot.runs.filter((run) => ["failed", "error"].includes(run.status));
  const activity = workSnapshot ?? snapshot;
  const matchesRun = (run: TaskRun) => matchesRunQuery(query, run, snapshot.projects, snapshot.templates);
  const visibleRuns = activity.runs.filter(matchesRun).slice(0, 7);
  const visiblePending = activity.approvals.filter((approval) => {
    if (approval.status !== "pending") return false;
    const run = snapshot.runs.find((item) => item.id === approval.run_id);
    return matchesQuery(query, approval.id, approval.run_id, approval.status) || Boolean(run && matchesRun(run));
  });
  const visibleLogs = activity.logs.filter((log) => matchesLogQuery(query, log)).slice(0, 5);
  const resultCount = visibleRuns.length + visiblePending.length + visibleLogs.length;

  if (loading) {
    return (
      <div className="grid gap-6">
        <section className="grid gap-4 border-b border-border/60 pb-5 md:grid-cols-[minmax(0,1fr)_auto] md:items-end">
          <div className="space-y-2">
            <Skeleton className="h-7 w-40 rounded-md" />
            <Skeleton className="h-4 w-72 max-w-full rounded-md" />
          </div>
          <div className="grid grid-cols-3 gap-6">
            <Skeleton className="h-10 w-16 rounded-md" />
            <Skeleton className="h-10 w-16 rounded-md" />
            <Skeleton className="h-10 w-16 rounded-md" />
          </div>
        </section>
        <section className="grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]">
          <SkeletonCard rows={5} />
          <SkeletonCard rows={3} />
        </section>
      </div>
    );
  }

  return (
    <div className="grid gap-6">
      {query && onClearQuery ? (
        <SearchScope query={query} resultCount={resultCount} onClear={onClearQuery} />
      ) : null}
      
      <section aria-labelledby="current-activity-heading" className="grid gap-4 border-b border-border/60 pb-5 md:grid-cols-[minmax(0,1fr)_auto] md:items-end">
        <div className="space-y-2">
          <h2 id="current-activity-heading" className="text-xl font-semibold tracking-tight">Current activity</h2>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground">
            <span className={cn("inline-flex items-center gap-1.5", summary.liveRunCount > 0 && "text-warning")}><Activity className="h-3.5 w-3.5" />{summary.liveRunCount} unfinished run{summary.liveRunCount === 1 ? "" : "s"} in loaded page</span>
            <span className={cn("inline-flex items-center gap-1.5", pending.length > 0 && "text-warning")}><Clock className="h-3.5 w-3.5" />{pending.length} pending approval{pending.length === 1 ? "" : "s"} in loaded page</span>
            <span className={cn("inline-flex items-center gap-1.5", failed.length > 0 && "text-destructive")}><AlertCircle className="h-3.5 w-3.5" />{failed.length} failed run{failed.length === 1 ? "" : "s"} in loaded page</span>
          </div>
        </div>
        <dl aria-label="Inventory" className="grid grid-cols-3 gap-x-6 gap-y-2">
          <div>
            <dt className="text-xs text-muted-foreground">Projects</dt>
            <dd className="mt-0.5 text-lg font-semibold tracking-tight">{totals?.projects ?? summary.projectCount}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Templates</dt>
            <dd className="mt-0.5 text-lg font-semibold tracking-tight">{totals?.templates ?? summary.templateCount}</dd>
            <dd className="text-xs text-muted-foreground">{summary.approvalTemplateCount} in loaded page require approval</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Logs</dt>
            <dd className="mt-0.5 text-lg font-semibold tracking-tight">{totals?.logs ?? summary.logCount}</dd>
          </div>
        </dl>
      </section>

      <section className="grid items-start gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]">
        <Card>
          <CardHeader className="border-b py-3">
            <CardTitle className="text-base font-semibold">Recent Activity</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {visibleRuns.length > 0 ? (
              <RunsTable runs={visibleRuns} projects={snapshot.projects} templates={snapshot.templates} />
            ) : (
              <CompactEmpty>{query ? "No runs match this search." : "Run activity will appear here after a run is requested."}</CompactEmpty>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="border-b py-3">
            <div className="flex items-center justify-between">
              <div>
                <CardTitle className="text-base font-semibold">Approval Inbox</CardTitle>
              </div>
              {visiblePending.length > 0 && (
                <span className="inline-flex h-5 w-5 items-center justify-center rounded-full bg-warning text-[10px] font-semibold text-warning-foreground">
                  {visiblePending.length}
                </span>
              )}
            </div>
          </CardHeader>
          <CardContent className="p-0">
            {visiblePending.length > 0 ? (
              <ApprovalList approvals={visiblePending} snapshot={snapshot} busy={busy} onDecide={onDecide} decisionError={decisionError} canOperateProject={canOperateProject} />
            ) : (
              <CompactEmpty>{query ? "No pending approvals match this search." : "No approvals are waiting."}</CompactEmpty>
            )}
          </CardContent>
        </Card>
      </section>

      <section>
        <Card>
          <CardHeader className="border-b py-3">
             <CardTitle className="text-base font-semibold">Latest Logs</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {visibleLogs.length > 0 ? (
              <LogViewer logs={visibleLogs} />
            ) : (
              <CompactEmpty>{query ? "No logs match this search." : "Run output will appear here when logs are available."}</CompactEmpty>
            )}
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
