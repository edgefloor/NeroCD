import { ReactNode, useState } from "react";
import { Ban, FileText, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import type { ApiSnapshot, TaskRun, Project, TaskTemplate } from "@/api";
import { cancelRun } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, formatDuration, projectName, templateName } from "@/lib/format";
import { rememberTerminalLogOpener, TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

function canCancel(run: TaskRun): boolean {
  return !["succeeded", "failed", "canceled"].includes(run.status);
}

export function RunsTable({
  runs,
  projects,
  templates,
  token,
  busy,
  mutate,
  onOpenLogs,
}: {
  runs: TaskRun[];
  projects: Project[];
  templates: TaskTemplate[];
  token?: string;
  busy?: string;
  mutate?: MutateFn;
  onOpenLogs?: (runID: string) => void;
}): ReactNode {
  if (runs.length === 0) {
    return <EmptyState title="No runs yet" icon={FileText} />;
  }
  return (
    <>
      <div className="grid gap-2 p-3 md:hidden">
        {runs.map((run) => (
          <div key={run.id} className="grid gap-2 rounded-xl border border-border/60 bg-card p-3 shadow-sm">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{templateName(templates, run.template_id, run)}</p>
                <p className="truncate text-xs text-muted-foreground">
                  {projectName(projects, run.project_id)} · {formatDate(run.started_at)}
                </p>
              </div>
              <StatusBadge status={run.status} />
            </div>
            <div className="flex gap-2 text-xs text-muted-foreground">
              <span className="rounded-lg bg-muted/60 px-2 py-1 font-mono">{formatDuration(run)}</span>
              <span className="truncate rounded-lg bg-muted/60 px-2 py-1 font-mono">{run.id}</span>
            </div>
            <div className="flex flex-wrap gap-2">
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
              {token && mutate && canCancel(run) ? (
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
                    {token && mutate && canCancel(run) ? (
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
  token,
  busy,
  mutate,
  loading,
}: {
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading?: boolean;
}): ReactNode {
  const [terminalRunID, setTerminalRunID] = useState("");
  const [terminalRunFallback, setTerminalRunFallback] = useState<TaskRun | undefined>();
  const [terminalOpen, setTerminalOpen] = useState(false);
  const terminalRun = snapshot.runs.find((run) => run.id === terminalRunID) ?? (terminalRunFallback?.id === terminalRunID ? terminalRunFallback : undefined);
  const terminalLogs = snapshot.logs.filter((log) => log.run_id === terminalRunID);

  function openTerminal(runID: string): void {
    setTerminalRunID(runID);
    setTerminalRunFallback(snapshot.runs.find((run) => run.id === runID));
    setTerminalOpen(true);
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
        </CardHeader>
        <CardContent className="p-0">
          <RunsTable
            runs={snapshot.runs}
            projects={snapshot.projects}
            templates={snapshot.templates}
            token={token}
            busy={busy}
            mutate={mutate}
            onOpenLogs={openTerminal}
          />
        </CardContent>
      </Card>
      <TerminalLogDialog run={terminalRun} logs={terminalLogs} open={terminalOpen} onOpenChange={setTerminalOpen} />
    </section>
  );
}
