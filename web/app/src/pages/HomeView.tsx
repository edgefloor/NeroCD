import { ReactNode } from "react";
import { Activity, FolderKanban, Layers3, Terminal, AlertCircle, Clock } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { ApiSnapshot } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { MetricCard } from "@/components/common/MetricCard";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonCard, SkeletonMetricCard } from "@/components/common/SkeletonCard";
import { SearchScope } from "@/components/common/SearchScope";
import { RunsTable } from "./RunsView";
import { ApprovalList } from "./ApprovalsView";
import { LogViewer } from "./LogsView";
import { summarizeOverview } from "@/view-model";
import { countFilteredItems } from "@/lib/format";
import { cn } from "@/lib/utils";

function HealthCard({ snapshot }: { snapshot: ApiSnapshot }) {
  const summary = summarizeOverview(snapshot);
  const pending = snapshot.approvals.filter((approval) => approval.status === "pending");
  const failed = snapshot.runs.filter((run) => ["failed", "error"].includes(run.status));
  const liveRuns = snapshot.runs.filter((run) => !run.finished_at);
  const isHealthy = failed.length === 0 && pending.length === 0;

  return (
    <Card className={cn("overflow-hidden", !isHealthy && "border-warning/30")}>
      <div className={cn("h-1", isHealthy ? "bg-success" : "bg-warning")} />
      <CardContent className="flex flex-col gap-4 p-5 md:flex-row md:items-center md:justify-between">
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className={cn(
              "grid h-2 w-2 place-items-center rounded-full",
              isHealthy ? "bg-success" : "bg-warning"
            )}>
              <span className={cn("h-1.5 w-1.5 rounded-full", isHealthy ? "bg-success" : "bg-warning")} />
            </span>
            <h2 className="text-xl font-semibold tracking-tight text-foreground">
              {isHealthy ? "All Systems Green" : "Needs Attention"}
            </h2>
          </div>
          <div className="flex flex-wrap items-center gap-4 text-sm text-muted-foreground">
            <span className="inline-flex items-center gap-1.5">
              <Clock className="h-3.5 w-3.5" />
              {pending.length} pending
            </span>
            <span className="inline-flex items-center gap-1.5">
              <AlertCircle className="h-3.5 w-3.5" />
              {failed.length} failed
            </span>
            <span className="inline-flex items-center gap-1.5">
              <Activity className="h-3.5 w-3.5" />
              {liveRuns.length} active
            </span>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <div className="grid grid-cols-3 gap-2">
            <div className="flex flex-col items-center gap-0.5 rounded-lg bg-muted/50 px-3 py-2">
              <span className="text-base font-semibold">{summary.projectCount}</span>
              <span className="text-[10px] font-medium text-muted-foreground">Projects</span>
            </div>
            <div className="flex flex-col items-center gap-0.5 rounded-lg bg-muted/50 px-3 py-2">
              <span className="text-base font-semibold">{summary.templateCount}</span>
              <span className="text-[10px] font-medium text-muted-foreground">Templates</span>
            </div>
            <div className="flex flex-col items-center gap-0.5 rounded-lg bg-muted/50 px-3 py-2">
              <span className="text-base font-semibold">{summary.logCount}</span>
              <span className="text-[10px] font-medium text-muted-foreground">Logs</span>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

export function HomeView({
  snapshot,
  workSnapshot,
  query,
  onClearQuery,
  token,
  busy,
  mutate,
  loading,
}: {
  snapshot: ApiSnapshot;
  workSnapshot?: ApiSnapshot;
  query?: string;
  onClearQuery?: () => void;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading?: boolean;
}): ReactNode {
  const summary = summarizeOverview(snapshot);
  const pending = snapshot.approvals.filter((approval) => approval.status === "pending");
  const visiblePending = workSnapshot?.approvals.filter((approval) => approval.status === "pending") ?? pending;

  if (loading) {
    return (
      <div className="grid gap-6">
        <SkeletonCard rows={1} />
        <section className="grid grid-cols-2 gap-3 xl:grid-cols-4">
          <SkeletonMetricCard />
          <SkeletonMetricCard />
          <SkeletonMetricCard />
          <SkeletonMetricCard />
        </section>
        <section className="grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]">
          <SkeletonCard rows={5} />
          <SkeletonCard rows={3} />
        </section>
        <section className="grid gap-6 xl:grid-cols-[1fr_1fr]">
          <SkeletonCard rows={5} />
          <SkeletonCard rows={4} />
        </section>
      </div>
    );
  }

  return (
    <div className="grid gap-6">
      {query && onClearQuery ? (
        <SearchScope query={query} resultCount={countFilteredItems(workSnapshot ?? snapshot)} onClear={onClearQuery} />
      ) : null}
      
      <HealthCard snapshot={snapshot} />

      <section className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <MetricCard label="Projects" value={summary.projectCount} caption="active domains" icon={FolderKanban} />
        <MetricCard 
          label="Templates" 
          value={summary.templateCount} 
          caption={`${summary.approvalTemplateCount} require approval`} 
          icon={Layers3} 
          tone={summary.approvalTemplateCount > 0 ? "warning" : "neutral"} 
        />
        <MetricCard 
          label="Live Runs" 
          value={summary.liveRunCount} 
          caption="currently executing" 
          icon={Activity} 
          tone={summary.liveRunCount > 0 ? "warning" : "success"} 
        />
        <MetricCard label="Run Logs" value={summary.logCount} caption="indexed events" icon={Terminal} />
      </section>

      <section className="grid gap-6 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,0.9fr)]">
        <Card>
          <CardHeader className="border-b py-3">
            <CardTitle className="text-base font-semibold">Recent Activity</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {workSnapshot && workSnapshot.runs.length > 0 ? (
              <RunsTable runs={workSnapshot.runs.slice(0, 7)} projects={workSnapshot.projects} templates={workSnapshot.templates} />
            ) : (
              <EmptyState title="No runs yet" />
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
              <ApprovalList approvals={visiblePending} snapshot={workSnapshot ?? snapshot} token={token} busy={busy} mutate={mutate} />
            ) : (
              <EmptyState title="No approvals waiting" />
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
            {workSnapshot && workSnapshot.logs.length > 0 ? (
              <LogViewer logs={workSnapshot.logs.slice(0, 5)} />
            ) : (
              <EmptyState title="No logs yet" />
            )}
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
