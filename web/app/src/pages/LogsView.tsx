import { type ReactNode } from "react";
import { Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { RunLog, TaskRun } from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { cn } from "@/lib/utils";
import { matchesQuery } from "@/lib/format";
export function LogViewer({ logs, runs = [] }: { logs: RunLog[]; runs?: TaskRun[] }): ReactNode { if (!logs.length) return <EmptyState title="No logs yet" icon={Terminal} />; return <div className="grid p-2 font-mono text-sm">{logs.map((log) => { const run = runs.find((item) => item.id === log.run_id); return <div key={log.id} className="grid gap-2 border-b px-3 py-2 md:grid-cols-[170px_76px_1fr]"><span className="text-xs text-muted-foreground">{log.run_id} #{log.sequence}{run ? ` · ${run.status}` : ""}</span><span className={cn("text-xs uppercase", log.stream === "stdout" ? "text-success" : log.stream === "stderr" ? "text-destructive" : "text-primary")}>{log.stream}</span><p className="whitespace-pre-wrap">{log.message}</p></div>; })}</div>; }
export function LogsView({ logs, runs = [], q, loading }: { logs: RunLog[]; runs?: TaskRun[]; q?: string; loading?: boolean }): ReactNode { const visibleLogs = logs.filter((log) => matchesQuery(q, log.run_id, log.stream, log.message)); return <Card><CardHeader><CardTitle>Run log stream</CardTitle></CardHeader><CardContent className="p-0">{loading ? <SkeletonTable rows={8} /> : <LogViewer logs={visibleLogs} runs={runs} />}</CardContent></Card>; }
