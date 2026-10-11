import { type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { RunLog, TaskRun } from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { cn } from "@/lib/utils";
import { matchesLogQuery } from "@/lib/format";
export function LogViewer({ logs, runs = [] }: { logs: RunLog[]; runs?: TaskRun[] }): ReactNode {
  if (!logs.length) return <EmptyState title="No logs yet" icon={Terminal} />;
  return <div className="grid min-w-0 grid-cols-1 p-2 font-mono text-sm">
    {logs.map((log) => {
      const run = runs.find((item) => item.id === log.run_id);
      return <div key={log.id} className="grid min-w-0 grid-cols-1 gap-2 border-b px-3 py-2 md:grid-cols-[170px_76px_minmax(0,1fr)]">
        <span className="min-w-0 break-words text-xs text-muted-foreground">
          <Link to="/runs/$runId" params={{ runId: log.run_id }} search={(previous) => previous}
            aria-label={`View run ${log.run_id}`} className="break-all underline underline-offset-2 hover:text-foreground">
            {log.run_id}
          </Link> #{log.sequence}{run ? ` · ${run.status}` : ""}
        </span>
        <span className={cn("text-xs uppercase", log.stream === "stdout" ? "text-success" : log.stream === "stderr" ? "text-destructive" : "text-primary")}>{log.stream}</span>
        <p className="min-w-0 whitespace-pre-wrap break-words">{log.message}</p>
      </div>;
    })}
  </div>;
}

export function LogsView({ logs, runs = [], q, loading }: { logs: RunLog[]; runs?: TaskRun[]; q?: string; loading?: boolean }): ReactNode {
  const filtering = Boolean(q?.trim());
  const visibleLogs = logs.filter((log) => matchesLogQuery(q, log));
  return <Card className="min-w-0">
    <CardHeader><CardTitle>Run log stream</CardTitle></CardHeader>
    <CardContent className="min-w-0 p-0">{loading ? <SkeletonTable rows={8} /> : <>
      {filtering ? <p role="status" className="px-5 py-2 text-sm text-muted-foreground">{visibleLogs.length} of {logs.length} loaded log entries match this filter.</p> : null}
      {visibleLogs.length ? <LogViewer logs={visibleLogs} runs={runs} /> : <p className="px-5 py-5 text-sm text-muted-foreground">{filtering ? "No logs match this filter on the loaded page. Clear the filter or try another page." : "No log output on this page."}</p>}
    </>}</CardContent>
  </Card>;
}
