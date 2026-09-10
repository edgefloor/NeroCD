import type { ReactNode } from "react";
import type { TaskRun, WorkflowStepState } from "@/api";
import { StatusBadge } from "@/components/common/StatusBadge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

function timestamp(value: string | undefined): string {
  if (!value) return "Not reported";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function duration(state: WorkflowStepState): string {
  if (!state.started_at) return "Not reported";
  if (!state.finished_at) return state.status === "running" ? "In progress" : "Not reported";
  const elapsed = Math.max(0, new Date(state.finished_at).getTime() - new Date(state.started_at).getTime());
  const seconds = Math.round(elapsed / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function WorkflowDiagnostics({ run }: { run: TaskRun }): ReactNode {
  const statesByID = new Map((run.workflow_state?.steps ?? []).map((state) => [state.id, state]));
  const declaredSteps = run.workflow?.steps ?? [];

  return (
    <Card aria-labelledby="workflow-diagnostics-title">
      <CardHeader className="border-b">
        <CardTitle><h2 id="workflow-diagnostics-title">Workflow steps</h2></CardTitle>
        <CardDescription>
          Status and timing reported for this run. Logs cover the whole run.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-4">
        {declaredSteps.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No workflow steps were declared for this run. {(run.workflow_state?.steps.length ?? 0) > 0 ? "Reported step state is available, but no declared order is available." : ""}
          </p>
        ) : (
          <ol className="grid gap-3" aria-label="Workflow step timeline">
            {declaredSteps.map((step, index) => {
              const state = statesByID.get(step.id);
              return (
                <li key={step.id} className="grid gap-3 rounded-lg border border-border/60 bg-muted/20 p-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-xs text-muted-foreground">{index + 1}</span>
                      <p className="min-w-0 break-words text-sm font-medium">{step.name}</p>
                    </div>
                    <p className="mt-1 break-all font-mono text-xs text-muted-foreground">{step.id}</p>
                    {step.depends_on?.length ? <p className="mt-2 text-xs text-muted-foreground">Depends on: {step.depends_on.join(", ")}</p> : null}
                  </div>
                  {state ? <StatusBadge status={state.status} className="sm:justify-self-end" /> : <span className="w-fit rounded-md border border-border/80 px-2 py-0.5 text-xs font-medium text-muted-foreground">Status not reported</span>}
                  <dl className="grid gap-2 border-t border-border/60 pt-3 text-xs text-muted-foreground sm:col-span-2 sm:grid-cols-3">
                    <div><dt className="font-medium text-foreground">Started</dt><dd>{state ? timestamp(state.started_at) : "Not reported"}</dd></div>
                    <div><dt className="font-medium text-foreground">Finished</dt><dd>{state ? timestamp(state.finished_at) : "Not reported"}</dd></div>
                    <div><dt className="font-medium text-foreground">Duration</dt><dd>{state ? duration(state) : "Not reported"}</dd></div>
                  </dl>
                </li>
              );
            })}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}
