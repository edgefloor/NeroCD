import { ReactNode, useState } from "react";
import { Ban, FileText, ListTree, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import type { ApiSnapshot, RunLog, TaskRun, Project, TaskTemplate } from "@/api";
import { cancelRun } from "@/api/compat";
import type { MutateFn } from "@/hooks/useApi";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, formatDuration, matchesQuery, projectName, templateName } from "@/lib/format";
import { rememberTerminalLogOpener, TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

function canCancel(run: TaskRun): boolean {
  return !["succeeded", "failed", "canceled"].includes(run.status);
}

export function RunsTable({
  runs,
  projects,
  templates,
  token = "",
  busy,
  mutate,
  canOperateProject,
  onOpenLogs,
}: {
  runs: TaskRun[];
  projects: Project[];
  templates: TaskTemplate[];
  token?: string;
  busy?: string;
  mutate?: MutateFn;
  canOperateProject?: (projectID: string) => boolean;
  onOpenLogs?: (runID: string) => void;
}): ReactNode {
  if (runs.length === 0) {
    return <EmptyState title="No runs yet" icon={FileText} />;
  }
  return (
    <>
      <div className="grid min-w-0 grid-cols-1 gap-2 p-3 md:hidden">
        {runs.map((run) => (
          <div key={run.id} className="grid min-w-0 grid-cols-1 gap-2 rounded-xl border border-border/60 bg-card p-3 shadow-sm">
            <div className="flex min-w-0 items-start justify-between gap-2">
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{templateName(templates, run.template_id, run)}</p>
                <p className="truncate text-xs text-muted-foreground">
                  {projectName(projects, run.project_id)} · {formatDate(run.started_at)}
                </p>
              </div>
              <StatusBadge status={run.status} />
            </div>
            <div className="flex min-w-0 gap-2 text-xs text-muted-foreground">
              <span className="shrink-0 rounded-lg bg-muted/60 px-2 py-1 font-mono">{formatDuration(run)}</span>
              <span className="truncate rounded-lg bg-muted/60 px-2 py-1 font-mono">{run.id}</span>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button asChild variant="outline" size="sm" className="h-8 rounded-xl">
                <a data-run-detail-link={run.id} href={`/runs/${run.id}`}>
                  <ListTree className="mr-1.5 h-4 w-4" />
                  Details
                </a>
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
              {mutate && canOperateProject?.(run.project_id) && canCancel(run) ? (
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 rounded-xl"
                  disabled={busy === `cancel:${run.id}`}
                  onClick={() => void mutate(`cancel:${run.id}`, () => cancelRun(token, run.id), "Run canceled")}
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="text-xs font-semibold uppercase tracking-wide">Run</TableHead>
              <TableHead className="text-xs font-semibold uppercase tracking-wide">Project</TableHead>
              <TableHead className="text-xs font-semibold uppercase tracking-wide">Duration</TableHead>
              <TableHead className="text-xs font-semibold uppercase tracking-wide text-right">Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => (
              <TableRow key={run.id} className="hover:bg-muted/40 transition-colors">
                <TableCell className="min-w-0">
                  <div className="truncate text-sm font-medium">{templateName(templates, run.template_id, run)}</div>
                  <div className="truncate text-xs text-muted-foreground font-mono">
                    {run.id} · {formatDate(run.started_at)}
                  </div>
                </TableCell>
                <TableCell className="text-sm">{projectName(projects, run.project_id)}</TableCell>
                <TableCell className="text-sm">{formatDuration(run)}</TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-2">
                    <Button asChild variant="outline" size="sm" className="h-8 rounded-xl">
                      <a data-run-detail-link={run.id} href={`/runs/${run.id}`}>
                        <ListTree className="mr-1.5 h-4 w-4" />
                        Details
                      </a>
                    </Button>
                    {mutate && canOperateProject?.(run.project_id) && canCancel(run) ? (
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-8 rounded-xl"
                        disabled={busy === `cancel:${run.id}`}
                        onClick={() => void mutate(`cancel:${run.id}`, () => cancelRun(token, run.id), "Run canceled")}
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
  token,
  busy,
  mutate,
  canOperateProject,
  loading,
  selectedRunID,
  selectedLogs,
  logsLoading,
  logsError,
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
  token: string;
  busy: string;
  mutate: MutateFn;
  canOperateProject?: (projectID: string) => boolean;
  loading?: boolean;
  selectedRunID?: string;
  selectedLogs?: RunLog[];
  logsLoading?: boolean;
  logsError?: Error;
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
  const visibleRuns = filtering ? snapshot.runs.filter((run) => matchesQuery(q,
    run.id, run.status, run.requested_by, run.run_spec.type, ...(run.runner_tags ?? []),
    projectName(snapshot.projects, run.project_id), templateName(snapshot.templates, run.template_id, run))) : snapshot.runs;
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
            projects={snapshot.projects}
            templates={snapshot.templates}
            token={token}
            busy={busy}
            mutate={mutate}
            canOperateProject={canOperateProject}
            onOpenLogs={openTerminal}
          />}
        </CardContent>
      </Card>
      <TerminalLogDialog run={terminalRun} logs={terminalLogs} loading={controlledLogs && logsLoading} error={controlledLogs ? logsError : undefined} onRetry={onRetryLogs} retrying={logsRetrying} following={controlledLogs && logsFollowing} hasPrevious={controlledLogs && logsHavePrevious} loadingPrevious={controlledLogs && logsLoadingPrevious} previousError={logsPreviousError} onLoadPrevious={onLoadPreviousLogs} open={terminalOpen} onOpenChange={changeTerminalOpen} />
    </section>
  );
}
