import { ReactNode } from "react";
import { Terminal, FileText } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { RunLog } from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { cn } from "@/lib/utils";

export function LogViewer({ logs }: { logs: RunLog[] }): ReactNode {
  if (logs.length === 0) {
    return <EmptyState title="No logs yet" icon={Terminal} />;
  }
  return (
    <div className="grid gap-0 p-2 font-mono text-sm">
      {logs.map((log) => (
        <div key={log.id} className="grid gap-2 rounded-md px-3 py-2 hover:bg-muted/50 transition-colors md:grid-cols-[minmax(0,170px)_76px_minmax(0,1fr)] border-b border-border/50 last:border-0">
          <span className="min-w-0 truncate text-muted-foreground font-mono text-xs" title={`${log.run_id} #${log.sequence}`}>
            {log.run_id} #{log.sequence}
          </span>
          <span className={cn(
            "text-xs font-medium uppercase",
            log.stream === "stdout" ? "text-success" : 
            log.stream === "stderr" ? "text-destructive" : "text-primary"
          )}>
            {log.stream}
          </span>
          <p className="min-w-0 whitespace-pre-wrap text-sm leading-relaxed">{log.message}</p>
        </div>
      ))}
    </div>
  );
}

export function LogsView({ logs, loading }: { logs: RunLog[]; loading?: boolean }): ReactNode {
  if (loading) {
    return (
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">Run log stream</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <SkeletonTable rows={8} />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader className="border-b py-3">
         <CardTitle className="text-base font-semibold">Run log stream</CardTitle>
      </CardHeader>
      <CardContent className="p-0">
        <LogViewer logs={logs} />
      </CardContent>
    </Card>
  );
}
