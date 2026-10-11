import { ReactNode, useState } from "react";
import { Link } from "@tanstack/react-router";
import { Ban, FileText, ListTree, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import type { ApiSnapshot, RunLog, TaskRun, Project, TaskTemplate } from "@/api";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, formatDuration, matchesRunQuery, projectName, templateName } from "@/lib/format";
import { rememberTerminalLogOpener, TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

function canCancel(run: TaskRun): boolean {
  return !["succeeded", "failed", "canceled"].includes(run.status);
}

export function RunsTable({
  runs,
  projects,
  templates,
  busy,
  onCancel,
  canOperateProject,
  onOpenLogs,
  detailSearch,
}: {
  runs: TaskRun[];
  projects: Project[];
  templates: TaskTemplate[];
  busy?: boolean;
  onCancel?: (runID: string) => void;
  canOperateProject?: (projectID: string) => boolean;
  onOpenLogs?: (runID: string) => void;
  detailSearch?: { q?: string; offset?: number };
}): ReactNode {
  if (runs.length === 0) {
    return <EmptyState title="No runs yet" icon={FileText} />;
  }
  return (
    <>
      <div className="grid min-w-0 grid-cols-1 gap-2 p-3 md:hidden">
        {runs.map((run) => (
          <div key={run.id} className="grid min-w-0 grid-cols-1 gap-2 rounded-xl border border-border/60 bg-card p-3 shadow-sm">
            <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
              <div className="min-w-0 flex-1 basis-48">
                <p className="break-words text-sm font-medium [overflow-wrap:anywhere]">{templateName(templates, run.template_id, run)}</p>
                <p className="break-words text-xs text-muted-foreground [overflow-wrap:anywhere]">{projectName(projects, run.project_id)}</p>
                <p className="text-xs text-muted-foreground">{formatDate(run.started_at)}</p>
              </div>
              <StatusBadge status={run.status} className="shrink-0" />
            </div>
            <div className="flex min-w-0 flex-wrap gap-2 text-xs text-muted-foreground">
              <span className="shrink-0 rounded-lg bg-muted/60 px-2 py-1 font-mono">{formatDuration(run)}</span>
              <span className="min-w-0 max-w-full break-all rounded-lg bg-muted/60 px-2 py-1 font-mono">{run.id}</span>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button asChild variant="outline" size="sm" className="h-8 rounded-xl">
                <Link data-run-detail-link={run.id} to="/runs/$runId" params={{ runId: run.id }} search={{ q: detailSearch?.q || undefined, offset: detailSearch?.offset || undefined }}>
                  <ListTree className="mr-1.5 h-4 w-4" />
                  Details
                </Link>
              </Button>
              {onOpenLogs ? (
                <Button
                  data-run-log-trigger={run.id}
                  variant="outline"
                  size="sm"
                  className="h-8 rounded-xl"
                  onClick={(event) => {
                    rememberTerminalLogOpener(event.currentTarget);
                    onOpenLogs(run.id);
                  }}
                >
                  <Terminal className="mr-1.5 h-4 w-4" />
                  Logs
                </Button>
              ) : null}
              {onCancel && canOperateProject?.(run.project_id) && canCancel(run) ? (
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 rounded-xl"
                  disabled={Boolean(busy)}
                  onClick={() => onCancel(run.id)}
                >
                  <Ban className="mr-1.5 h-4 w-4" />
                  Cancel
                </Button>
              ) : null}
            </div>
          </div>
        ))}
      </div>
      <div className="hidden md:block">
        <Table className="table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className="w-[35%] text-xs font-semibold uppercase tracking-wide">Run</TableHead>
              <TableHead className="w-[20%] text-xs font-semibold uppercase tracking-wide">Project</TableHead>
              <TableHead className="w-[10%] text-xs font-semibold uppercase tracking-wide">Duration</TableHead>
              <TableHead className="w-[35%] text-xs font-semibold uppercase tracking-wide text-right">Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => (
              <TableRow key={run.id} className="hover:bg-muted/40 transition-colors">
                <TableCell className="min-w-0 whitespace-normal align-top">
                  <div className="break-words text-sm font-medium [overflow-wrap:anywhere]">{templateName(templates, run.template_id, run)}</div>
                  <div className="break-words text-xs text-muted-foreground font-mono [overflow-wrap:anywhere]">
                    {run.id} · {formatDate(run.started_at)}
                  </div>
                </TableCell>
                <TableCell className="whitespace-normal break-words align-top text-sm [overflow-wrap:anywhere]">{projectName(projects, run.project_id)}</TableCell>
                <TableCell className="whitespace-normal break-words align-top text-sm">{formatDuration(run)}</TableCell>
                <TableCell className="align-top text-right">
                  <div className="flex flex-wrap items-center justify-end gap-2">
                    <Button asChild variant="outline" size="sm" className="h-8 rounded-xl">
                      <Link data-run-detail-link={run.id} to="/runs/$runId" params={{ runId: run.id }} search={{ q: detailSearch?.q || undefined, offset: detailSearch?.offset || undefined }}>
                        <ListTree className="mr-1.5 h-4 w-4" />
                        Details
                      </Link>
                    </Button>
                    {onCancel && canOperateProject?.(run.project_id) && canCancel(run) ? (
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-8 rounded-xl"
                        disabled={Boolean(busy)}
                        onClick={() => onCancel(run.id)}
                      >
                        <Ban className="mr-1.5 h-4 w-4" />
                        Cancel
                      </Button>
                    ) : null}
                    {onOpenLogs ? (
                      <Button
                        data-run-log-trigger={run.id}
                        variant="outline"
                        size="sm"
                        className="h-8 rounded-xl"
                        onClick={(event) => {
                          rememberTerminalLogOpener(event.currentTarget);
                          onOpenLogs(run.id);
                        }}
                      >
                        <Terminal className="mr-1.5 h-4 w-4" />
                        Logs
                      </Button>
                    ) : null}
                    <StatusBadge status={run.status} />
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  );
}

export function RunsView({
  snapshot,
  q,
  offset,
  busy,
  onCancel,
  canOperateProject,
  loading,
  selectedRunID,
  selectedLogs,
  logsLoading,
  logsError,
  recovery,
  onRetryLogs,
  logsRetrying,
  logsFollowing,
  logsHavePrevious,
  logsLoadingPrevious,
  logsPreviousError,
  onLoadPreviousLogs,
  selectedRun,
  onSelectRun,
  onCloseLogs,
}: {
  snapshot: ApiSnapshot;
  q?: string;
  offset?: number;
  busy: boolean;
  onCancel?: (runID: string) => void;
  canOperateProject?: (projectID: string) => boolean;
  loading?: boolean;
  selectedRunID?: string;
  selectedLogs?: RunLog[];
  logsLoading?: boolean;
  logsError?: Error;
  recovery?: ReactNode;
  onRetryLogs?: () => void;
  logsRetrying?: boolean;
  logsFollowing?: boolean;
  logsHavePrevious?: boolean;
  logsLoadingPrevious?: boolean;
  logsPreviousError?: Error;
  onLoadPreviousLogs?: () => void;
  selectedRun?: TaskRun;
  onSelectRun?: (runID: string) => void;
  onCloseLogs?: () => void;
}): ReactNode {
  const filtering = Boolean(q?.trim());
  const visibleRuns = filtering ? snapshot.runs.filter((run) => matchesRunQuery(q, run, snapshot.projects, snapshot.templates)) : snapshot.runs;
  const [localTerminalRunID, setLocalTerminalRunID] = useState("");
  const [terminalRunFallback, setTerminalRunFallback] = useState<TaskRun | undefined>();
  const [localTerminalOpen, setLocalTerminalOpen] = useState(false);
  const controlledLogs = onSelectRun !== undefined;
  const terminalRunID = controlledLogs ? selectedRunID ?? "" : localTerminalRunID;
  const terminalOpen = controlledLogs ? Boolean(selectedRunID) : localTerminalOpen;
  const terminalRun = selectedRun?.id === terminalRunID ? selectedRun : snapshot.runs.find((run) => run.id === terminalRunID) ?? (terminalRunFallback?.id === terminalRunID ? terminalRunFallback : undefined);
  const terminalLogs = controlledLogs ? selectedLogs ?? [] : snapshot.logs.filter((log) => log.run_id === terminalRunID);

  function openTerminal(runID: string): void {
    setTerminalRunFallback(snapshot.runs.find((run) => run.id === runID));
    if (controlledLogs) {
      onSelectRun(runID);
      return;
    }
    setLocalTerminalRunID(runID);
    setLocalTerminalOpen(true);
  }

  function changeTerminalOpen(open: boolean): void {
    if (controlledLogs) {
      if (!open) onCloseLogs?.();
      return;
    }
    setLocalTerminalOpen(open);
  }

  if (loading) {
    return (
      <section>
        <SkeletonTable rows={5} />
      </section>
    );
  }

  return (
    <section>
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">Execution queue</CardTitle>
          {filtering && <p className="text-sm text-muted-foreground" role="status">{visibleRuns.length} of {snapshot.runs.length} runs match on this page.</p>}
        </CardHeader>
        <CardContent className="p-0">
          {filtering && visibleRuns.length === 0 ? (
            <p className="px-4 py-5 text-sm text-muted-foreground">No runs match this filter on the loaded page.</p>
          ) : <RunsTable
            runs={visibleRuns}
            detailSearch={{ q, offset }}
            projects={snapshot.projects}
            templates={snapshot.templates}
            busy={busy}
            onCancel={onCancel}
            canOperateProject={canOperateProject}
            onOpenLogs={openTerminal}
          />}
        </CardContent>
      </Card>
      <TerminalLogDialog recovery={recovery} run={terminalRun} logs={terminalLogs} loading={controlledLogs && logsLoading} error={controlledLogs ? logsError : undefined} onRetry={onRetryLogs} retrying={logsRetrying} following={controlledLogs && logsFollowing} hasPrevious={controlledLogs && logsHavePrevious} loadingPrevious={controlledLogs && logsLoadingPrevious} previousError={logsPreviousError} onLoadPrevious={onLoadPreviousLogs} open={terminalOpen} onOpenChange={changeTerminalOpen} />
    </section>
  );
}
