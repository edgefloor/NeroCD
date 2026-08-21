import { type ReactNode } from "react";
import { Ban, FileText, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import type { Project, RunLog, TaskRun, TaskTemplate } from "@/api";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, formatDuration, matchesQuery, projectName, templateName } from "@/lib/format";
import { rememberTerminalLogOpener, TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

const canCancel = (run: TaskRun) => !["succeeded", "failed", "canceled", "rejected"].includes(run.status);

export function RunsTable({ runs, projects, templates, cancelingRunID, onCancel, onOpenLogs }: { runs: TaskRun[]; projects: Project[]; templates: TaskTemplate[]; cancelingRunID?: string; onCancel?: (runID: string) => void; onOpenLogs?: (runID: string) => void }): ReactNode {
  if (!runs.length) return <EmptyState title="No runs yet" icon={FileText} />;
  return <Table><TableHeader><TableRow><TableHead>Run</TableHead><TableHead>Project</TableHead><TableHead>Duration</TableHead><TableHead className="text-right">Status</TableHead></TableRow></TableHeader><TableBody>{runs.map((run) => <TableRow key={run.id}><TableCell><div className="font-medium">{templateName(templates, run.template_id, run)}</div><div className="font-mono text-xs text-muted-foreground">{run.id} · {formatDate(run.started_at)}</div></TableCell><TableCell>{projectName(projects, run.project_id)}</TableCell><TableCell>{formatDuration(run)}</TableCell><TableCell><div className="flex justify-end gap-2">{canCancel(run) && onCancel ? <Button variant="outline" size="sm" disabled={cancelingRunID === run.id} onClick={() => onCancel(run.id)}><Ban className="mr-1 h-4 w-4" />Cancel</Button> : null}{onOpenLogs ? <Button data-run-log-trigger={run.id} variant="outline" size="sm" onClick={(event) => { rememberTerminalLogOpener(event.currentTarget); onOpenLogs(run.id); }}><Terminal className="mr-1 h-4 w-4" />Logs</Button> : null}<StatusBadge status={run.status} /></div></TableCell></TableRow>)}</TableBody></Table>;
}

export function RunsView({ runs, projects, templates, logs = [], q, selectedRunID, loading, cancelingRunID, onCancel, onOpenLogs, onCloseLogs }: { runs: TaskRun[]; projects: Project[]; templates: TaskTemplate[]; logs?: RunLog[]; q?: string; selectedRunID?: string; loading?: boolean; cancelingRunID?: string; onCancel: (runID: string) => void; onOpenLogs: (runID: string) => void; onCloseLogs: () => void }): ReactNode {
  if (loading) return <SkeletonTable rows={5} />;
  const selectedRun = runs.find((run) => run.id === selectedRunID);
  const visibleRuns = runs.filter((run) => matchesQuery(q, run.id, run.status, run.project_id, run.template_id, templateName(templates, run.template_id, run), projectName(projects, run.project_id)));
  return <section><Card><CardHeader><CardTitle>Execution queue</CardTitle></CardHeader><CardContent className="p-0"><RunsTable runs={visibleRuns} projects={projects} templates={templates} cancelingRunID={cancelingRunID} onCancel={onCancel} onOpenLogs={onOpenLogs} /></CardContent></Card><TerminalLogDialog run={selectedRun} logs={logs} open={Boolean(selectedRunID)} onOpenChange={(open) => !open && onCloseLogs()} /></section>;
}
